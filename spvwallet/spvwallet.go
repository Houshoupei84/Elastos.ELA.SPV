package spvwallet

import (
	"errors"
	"fmt"
	"time"
	"github.com/elastos/Elastos.ELA.SPV/bloom"
	tx "github.com/elastos/Elastos.ELA.SPV/core/transaction"
	"github.com/elastos/Elastos.ELA.SPV/sdk"
	"github.com/elastos/Elastos.ELA.SPV/spvwallet/config"
	"github.com/elastos/Elastos.ELA.SPV/spvwallet/log"
	"github.com/elastos/Elastos.ELA.SPV/msg"
	"github.com/elastos/Elastos.ELA.SPV/p2p"
)

var spvWallet *SPVWallet

func Init(clientId uint64) (*SPVWallet, error) {
	var err error
	spvWallet = new(SPVWallet)
	// Initialize blockchain
	spvWallet.chain, err = NewBlockchain()
	if err != nil {
		return nil, err
	}

	// Initialize P2P network client
	client, err := sdk.GetSPVClient(sdk.TypeMainNet, clientId, config.Values().SeedList)
	if err != nil {
		return nil, err
	}
	// Set p2p message handler
	client.SetMessageHandler(spvWallet)

	spvWallet.SPVClient = client

	// Initialize sync manager
	spvWallet.SyncManager = NewSyncManager()
	spvWallet.chain.OnTxCommit = OnTxCommit
	spvWallet.chain.OnBlockCommit = OnBlockCommit
	spvWallet.chain.OnRollback = OnRollback

	return spvWallet, nil
}

type SPVWallet struct {
	sdk.SPVClient
	*SyncManager
	chain *Blockchain
}

func (wallet *SPVWallet) OnPeerEstablish(peer *p2p.Peer) {
	// Send filterload message
	peer.Send(wallet.chain.GetBloomFilter().GetFilterLoadMsg())
}

func (wallet *SPVWallet) Start() {
	wallet.SPVClient.Start()
	go wallet.keepUpdate()
	log.Info("SPV service started...")
}

func (wallet *SPVWallet) Stop() {
	wallet.chain.Close()
	log.Info("SPV service stopped...")
}

func (wallet *SPVWallet) BlockChain() *Blockchain {
	return wallet.chain
}

func (wallet *SPVWallet) keepUpdate() {
	ticker := time.NewTicker(time.Second * p2p.InfoUpdateDuration)
	defer ticker.Stop()
	for range ticker.C {
		// Keep synchronizing blocks
		wallet.SyncBlocks()
	}
}

func (wallet *SPVWallet) OnInventory(peer *p2p.Peer, inv *msg.Inventory) error {
	switch inv.Type {
	case msg.TRANSACTION:
		// Do nothing, transaction inventory is not supported
	case msg.BLOCK:
		log.Info("SPV receive block inventory")
		return wallet.HandleBlockInvMsg(peer, inv)
	}
	return nil
}

func (wallet *SPVWallet) NotifyNewAddress(hash []byte) error {
	// Reload address filter to include new address
	wallet.chain.Addrs().ReloadAddrFilter()
	// Broadcast filterload message to connected peers
	wallet.PeerManager().Broadcast(wallet.chain.GetBloomFilter().GetFilterLoadMsg())
	return nil
}

func (wallet *SPVWallet) SendTransaction(tx tx.Transaction) error {
	// Broadcast transaction to connected peers
	wallet.PeerManager().Broadcast(wallet.NewTxn(tx))
	return nil
}

func (wallet *SPVWallet) OnMerkleBlock(peer *p2p.Peer, block *bloom.MerkleBlock) error {
	wallet.dataLock.Lock()
	defer wallet.dataLock.Unlock()

	blockHash := block.BlockHeader.Hash()
	log.Trace("Receive merkle block hash:", blockHash.String())

	if wallet.chain.IsKnownBlock(*blockHash) {
		return errors.New(fmt.Sprint("Received block that already known,", blockHash.String()))
	}

	err := wallet.chain.CheckProofOfWork(&block.BlockHeader)
	if err != nil {
		return err
	}

	if wallet.chain.IsSyncing() && !wallet.InRequestQueue(*blockHash) {
		// Put non syncing blocks into orphan pool
		wallet.AddOrphanBlock(*blockHash, block)
		return nil
	}

	if !wallet.chain.IsSyncing() {
		// Check if new block can connect to previous
		tip := wallet.chain.ChainTip()
		// If block is already added, return
		if tip.Hash().IsEqual(blockHash) {
			return nil
		}
		// Meet an orphan block
		if !tip.Hash().IsEqual(&block.BlockHeader.Previous) {
			// Put non syncing blocks into orphan pool
			wallet.AddOrphanBlock(*blockHash, block)
			return nil
		}
		// Set start hash and stop hash to the same block hash
		wallet.startHash = blockHash
		wallet.stopHash = blockHash

	} else if wallet.blockLocator == nil || wallet.PeerManager().GetSyncPeer() == nil || wallet.PeerManager().GetSyncPeer().ID() != peer.ID() {

		log.Error("Receive message from non sync peer, disconnect")
		wallet.ChangeSyncPeerAndRestart()
		return errors.New("Receive message from non sync peer, disconnect")
	}
	// Mark block as received
	wallet.BlockReceived(*blockHash, block)

	return wallet.RequestBlockTxns(peer, block)
}

func (wallet *SPVWallet) OnTxn(peer *p2p.Peer, txn *msg.Txn) error {
	wallet.dataLock.Lock()
	defer wallet.dataLock.Unlock()

	txId := txn.Transaction.Hash()
	log.Debug("Receive transaction hash: ", txId.String())

	if wallet.chain.IsSyncing() && !wallet.InRequestQueue(*txId) {
		// Put non syncing txns into orphan pool
		wallet.AddOrphanTxn(*txId, txn)
		return nil
	}

	if !wallet.chain.IsSyncing() {
		// Check if transaction already received
		if wallet.MemCache.TxCached(*txId) {
			return errors.New("Received transaction already cached")
		}
		// Put txn into unconfirmed txnpool
		fPositive, err := wallet.chain.CommitUnconfirmedTxn(txn.Transaction)
		if err != nil {
			return err
		}
		if fPositive {
			wallet.handleFPositive(1)
		}

	} else if wallet.blockLocator == nil || wallet.PeerManager().GetSyncPeer() == nil || wallet.PeerManager().GetSyncPeer().ID() != peer.ID() {

		log.Error("Receive message from non sync peer, disconnect")
		wallet.ChangeSyncPeerAndRestart()
		return errors.New("Receive message from non sync peer, disconnect")
	}

	wallet.TxnReceived(*txId, txn)

	// All request finished, submit received block and txn data
	if wallet.RequestFinished() {

		err := wallet.CommitData()
		if err != nil {
			return err
		}

		// Continue syncing
		wallet.startSync()

		return nil
	}

	return nil
}

func (wallet *SPVWallet) OnNotFound(peer *p2p.Peer, msg *msg.NotFound) error {
	log.Error("Receive not found message, disconnect")
	wallet.ChangeSyncPeerAndRestart()
	return nil
}

func (wallet *SPVWallet) updateLocalHeight() {
	wallet.PeerManager().Local().SetHeight(uint64(wallet.chain.Height()))
}