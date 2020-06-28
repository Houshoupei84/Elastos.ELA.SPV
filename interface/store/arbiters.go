package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sort"
	"sync"

	"github.com/elastos/Elastos.ELA/common"
	"github.com/syndtr/goleveldb/leveldb"
	dbutil "github.com/syndtr/goleveldb/leveldb/util"
)

var (
	BKTArbiters    = []byte("C")
	BKTArbPosition = []byte("P")
)

// Ensure arbiters implement arbiters interface.
var _ Arbiters = (*arbiters)(nil)

type arbiters struct {
	batch
	sync.RWMutex
	db    *leveldb.DB
	b     *leveldb.Batch
	cache map[common.Uint256]uint32
}

func NewArbiters(db *leveldb.DB) *arbiters {
	return &arbiters{
		db:    db,
		b:     new(leveldb.Batch),
		cache: make(map[common.Uint256]uint32),
	}
}

func (c *arbiters) Put(height uint32, crcArbiters [][]byte, normalArbiters [][]byte) error {
	c.Lock()
	defer c.Unlock()
	if err := c.batchPut(height, crcArbiters, normalArbiters, c.b); err != nil {
		return err
	}
	c.db.Write(c.b, nil)
	return nil
}

func (c *arbiters) batchPut(height uint32, crcArbiters [][]byte, normalArbiters [][]byte, batch *leveldb.Batch) error {
	pos := c.getCurrentPosition()
	if height <= pos {
		return errors.New("height must be bigger than existed position")
	}
	batch.Put(BKTArbPosition, uint32toBytes(height))
	var ars [][]byte
	for _, a := range crcArbiters {
		ars = append(ars, a)
	}
	for _, a := range normalArbiters {
		ars = append(ars, a)
	}
	copyars := make([][]byte, len(ars))
	copy(copyars, ars)
	sort.Slice(copyars, func(i, j int) bool {
		return bytes.Compare(copyars[i], copyars[j]) < 0
	})
	hash := sha256.Sum256(getValueBytes(copyars, uint8(len(crcArbiters))))
	key, err := common.Uint256FromBytes(hash[:])
	if err != nil {
		return err
	}
	val, ok := c.cache[*key]
	index := getIndex(height)
	if !ok {
		existHeight, err := c.db.Get(hash[:], nil)
		if err == nil {
			c.cache[*key] = bytesToUint32(existHeight)
			err = c.db.Put(index, existHeight, nil)
			return nil
		} else if err == leveldb.ErrNotFound {
			c.cache[*key] = height
			batch.Put(index, getValueBytes(ars, uint8(len(crcArbiters))))
			batch.Put(hash[:], uint32toBytes(height))
			return nil
		} else {
			return err
		}
	}

	batch.Put(index, uint32toBytes(val))
	return nil
}

func (c *arbiters) BatchPut(height uint32, crcArbiters [][]byte, normalArbiters [][]byte, batch *leveldb.Batch) error {
	c.Lock()
	defer c.Unlock()
	return c.batchPut(height, crcArbiters, normalArbiters, batch)
}

func (c *arbiters) Get() (crcArbiters [][]byte, normalArbiters [][]byte, err error) {
	c.RLock()
	defer c.RUnlock()
	return c.get(c.getCurrentPosition())
}

func (c *arbiters) get(height uint32) (crcArbiters [][]byte, normalArbiters [][]byte, err error) {
	var val []byte
	val, err = c.db.Get(getIndex(height), nil)
	if err != nil {
		return
	}
	if len(val) == 4 {
		val, err = c.db.Get(getIndex(bytesToUint32(val)), nil)
		if err != nil {
			return
		}
	}
	buf := new(bytes.Buffer)
	buf.WriteByte(val[0])
	var crclen uint8
	crclen, err = common.ReadUint8(buf)
	if err != nil {
		return
	}
	for i := 0; i < (len(val)-1)/33; i++ {
		prefix := i*33 + 1
		suffix := (i+1)*33 + 1
		if i <= int(crclen-1) {
			crcArbiters = append(crcArbiters, val[prefix:suffix])
		} else {
			normalArbiters = append(normalArbiters, val[prefix:suffix])
		}
	}
	return
}

func (c *arbiters) GetByHeight(height uint32) (crcArbiters [][]byte, normalArbiters [][]byte, err error) {
	c.RLock()
	defer c.RUnlock()
	return c.get(height)
}

func (c *arbiters) Close() error {
	c.Lock()
	return nil
}

func (c *arbiters) Clear() error {
	c.Lock()
	defer c.Unlock()
	it := c.db.NewIterator(dbutil.BytesPrefix(BKTArbiters), nil)
	defer it.Release()
	for it.Next() {
		c.b.Delete(it.Key())
	}
	c.b.Delete(BKTArbPosition)
	return c.db.Write(c.b, nil)
}

func (c *arbiters) getCurrentPosition() uint32 {
	pos, err := c.db.Get(BKTArbPosition, nil)
	if err == nil {
		return bytesToUint32(pos)
	}

	return 0
}

func (c *arbiters) Commit() error {
	return c.db.Write(c.b, nil)
}

func (c *arbiters) Rollback() error {
	c.b.Reset()
	return nil
}

func (c *arbiters) CommitBatch(batch *leveldb.Batch) error {
	return c.db.Write(batch, nil)
}

func (c *arbiters) RollbackBatch(batch *leveldb.Batch) error {
	batch.Reset()
	return nil
}

func uint32toBytes(data uint32) []byte {
	var r [4]byte
	binary.LittleEndian.PutUint32(r[:], data)
	return r[:]
}

func getIndex(data uint32) []byte {
	var kdata [4]byte
	binary.LittleEndian.PutUint32(kdata[:], data)
	return toKey(BKTArbiters, kdata[:]...)
}

func bytesToUint32(data []byte) uint32 {
	return binary.LittleEndian.Uint32(data)
}

func getValueBytes(data [][]byte, crclen uint8) []byte {
	buf := new(bytes.Buffer)
	common.WriteUint8(buf, crclen)
	for _, v := range data {
		buf.Write(v)
	}
	return buf.Bytes()
}
