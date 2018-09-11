package sync

import (
	"github.com/elastos/Elastos.ELA.SPV/blockchain"
	"github.com/elastos/Elastos.ELA/bloom"
)

const (
	DefaultMinPeersForSync = 3
	DefaultMaxPeers        = 125
)

// Config is a configuration struct used to initialize a new SyncManager.
type Config struct {
	Chain *blockchain.BlockChain

	MinPeersForSync int
	MaxPeers        int

	UpdateFilter func() *bloom.Filter
}

func NewDefaultConfig(chain *blockchain.BlockChain,
	updateFilter func() *bloom.Filter) *Config {
	return &Config{
		Chain:           chain,
		MinPeersForSync: DefaultMinPeersForSync,
		MaxPeers:        DefaultMaxPeers,
		UpdateFilter:    updateFilter,
	}
}