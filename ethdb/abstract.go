package ethdb

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	"github.com/dgraph-io/badger/v2"
	"github.com/ledgerwatch/bolt"
	"github.com/ledgerwatch/turbo-geth/common/dbutils"
	"github.com/ledgerwatch/turbo-geth/ethdb/remote"
)

type DbProvider uint8

const (
	Bolt DbProvider = iota
	Badger
	Remote
)

const DefaultProvider = Bolt

type Options struct {
	provider DbProvider
	Remote   remote.DbOpts
	Bolt     *bolt.Options
	Badger   badger.Options

	path string
}

func Opts() Options {
	return ProviderOpts(DefaultProvider)
}

func (opts Options) Path(path string) Options {
	opts.path = path
	switch opts.provider {
	case Bolt:
		// nothing to do
	case Badger:
		opts.Badger = opts.Badger.WithDir(path).WithValueDir(path)
	case Remote:
		opts.Remote = opts.Remote.Addr(path)
	}
	return opts
}

func (opts Options) InMem(val bool) Options {
	switch opts.provider {
	case Bolt:
		opts.Bolt.MemOnly = val
	case Badger:
		opts.Badger = opts.Badger.WithInMemory(val)
	case Remote:
		panic("not supported")
	}
	return opts
}

func ProviderOpts(provider DbProvider) Options {
	opts := Options{provider: provider}
	switch opts.provider {
	case Bolt:
		opts.Bolt = bolt.DefaultOptions
	case Badger:
		opts.Badger = badger.DefaultOptions(opts.path)
	case Remote:
		opts.Remote = remote.DefaultOpts
	default:
		panic("unknown db provider: " + strconv.Itoa(int(provider)))
	}

	return opts
}

type DB struct {
	opts   Options
	bolt   *bolt.DB
	badger *badger.DB
	remote *remote.DB
}

var buckets = [][]byte{
	dbutils.IntermediateTrieHashBucket,
}

func (opts Options) Open(ctx context.Context) (db *DB, err error) {
	return Open(ctx, opts)
}

func Open(ctx context.Context, opts Options) (db *DB, err error) {
	db = &DB{opts: opts}

	switch db.opts.provider {
	case Bolt:
		db.bolt, err = bolt.Open(opts.path, 0600, opts.Bolt)
		if err != nil {
			return nil, err
		}
		err = db.bolt.Update(func(tx *bolt.Tx) error {
			for _, name := range buckets {
				_, createErr := tx.CreateBucketIfNotExists(name, false)
				if createErr != nil {
					return createErr
				}
			}
			return nil
		})
	case Badger:
		db.badger, err = badger.Open(opts.Badger)
	case Remote:
		db.remote, err = remote.Open(ctx, opts.Remote)
	}
	if err != nil {
		return nil, err
	}

	return db, nil
}

// Close closes DB
// All transactions must be closed before closing the database.
func (db *DB) Close() error {
	switch db.opts.provider {
	case Bolt:
		return db.bolt.Close()
	case Badger:
		return db.badger.Close()
	case Remote:
		return db.remote.Close()
	}
	return nil
}

type Tx struct {
	ctx context.Context
	db  *DB

	bolt   *bolt.Tx
	badger *badger.Txn
	remote *remote.Tx

	badgerIterators []*badger.Iterator
}

type Bucket struct {
	tx *Tx

	bolt         *bolt.Bucket
	badgerPrefix []byte
	nameLen      uint
	remote       *remote.Bucket
}

type Cursor struct {
	ctx      context.Context
	bucket   Bucket
	provider DbProvider
	prefix   []byte

	remoteOpts remote.CursorOpts
	badgerOpts badger.IteratorOptions

	bolt   *bolt.Cursor
	badger *badger.Iterator
	remote *remote.Cursor

	k   []byte
	v   []byte
	err error
}

func (db *DB) View(ctx context.Context, f func(tx *Tx) error) (err error) {
	t := &Tx{db: db, ctx: ctx}
	switch db.opts.provider {
	case Bolt:
		return db.bolt.View(func(tx *bolt.Tx) error {
			defer t.cleanup()
			t.bolt = tx
			return f(t)
		})
	case Badger:
		return db.badger.View(func(tx *badger.Txn) error {
			defer t.cleanup()
			t.badger = tx
			return f(t)
		})
	case Remote:
		return db.remote.View(ctx, func(tx *remote.Tx) error {
			t.remote = tx
			return f(t)
		})
	}
	return err
}

func (db *DB) Update(ctx context.Context, f func(tx *Tx) error) (err error) {
	t := &Tx{db: db, ctx: ctx}
	switch db.opts.provider {
	case Bolt:
		return db.bolt.Update(func(tx *bolt.Tx) error {
			defer t.cleanup()
			t.bolt = tx
			return f(t)
		})
	case Badger:
		return db.badger.Update(func(tx *badger.Txn) error {
			defer t.cleanup()
			t.badger = tx
			return f(t)
		})
	case Remote:
		return fmt.Errorf("remote db provider doesn't support .Update method")
	}
	return err
}

func (tx *Tx) Bucket(name []byte) Bucket {
	b := Bucket{tx: tx, nameLen: uint(len(name))}
	switch tx.db.opts.provider {
	case Bolt:
		b.bolt = tx.bolt.Bucket(name)
	case Badger:
		b.badgerPrefix = name
	case Remote:
		b.remote = tx.remote.Bucket(name)
	}
	return b
}

func (tx *Tx) cleanup() {
	switch tx.db.opts.provider {
	case Bolt:
		// nothing to cleanup
	case Badger:
		for _, it := range tx.badgerIterators {
			it.Close()
		}
	case Remote:
		// nothing to cleanup
	}
}

func (c *Cursor) Prefix(v []byte) *Cursor {
	c.prefix = v
	return c
}

func (c *Cursor) Prefetch(v uint) *Cursor {
	switch c.provider {
	case Bolt:
		// nothing to do
	case Badger:
		c.badgerOpts.PrefetchSize = int(v)
	case Remote:
		c.remoteOpts.PrefetchSize(uint64(v))
	}
	return c
}

func (c *Cursor) NoValues() *Cursor {
	switch c.provider {
	case Bolt:
		// nothing to do
	case Badger:
		c.badgerOpts.PrefetchValues = false
	case Remote:
		c.remoteOpts.PrefetchValues(false)
	}
	return c
}

func (c *Cursor) From() *Cursor {
	switch c.provider {
	case Bolt:
		// nothing to do
	case Badger:
		c.badgerOpts.PrefetchValues = false
	case Remote:
		c.remoteOpts.PrefetchValues(false)
	}
	return c
}

func (b Bucket) Get(key []byte) (val []byte, err error) {
	select {
	case <-b.tx.ctx.Done():
		return nil, b.tx.ctx.Err()
	default:
	}

	switch b.tx.db.opts.provider {
	case Bolt:
		val, _ = b.bolt.Get(key)
	case Badger:
		var item *badger.Item
		b.badgerPrefix = append(b.badgerPrefix[:b.nameLen], key...)
		item, err = b.tx.badger.Get(b.badgerPrefix)
		if item != nil {
			val, err = item.ValueCopy(nil) // can improve this by using pool
		}
	case Remote:
		val, err = b.remote.Get(key)
	}
	return val, err
}

func (b Bucket) Put(key []byte, value []byte) error {
	select {
	case <-b.tx.ctx.Done():
		return b.tx.ctx.Err()
	default:
	}

	switch b.tx.db.opts.provider {
	case Bolt:
		return b.bolt.Put(key, value)
	case Badger:
		b.badgerPrefix = append(b.badgerPrefix[:b.nameLen], key...)
		return b.tx.badger.Set(b.badgerPrefix, value)
	case Remote:
		panic("not supported")
	}
	return nil
}

func (b Bucket) Delete(key []byte) error {
	select {
	case <-b.tx.ctx.Done():
		return b.tx.ctx.Err()
	default:
	}

	switch b.tx.db.opts.provider {
	case Bolt:
		return b.bolt.Delete(key)
	case Badger:
		b.badgerPrefix = append(b.badgerPrefix[:b.nameLen], key...)
		return b.tx.badger.Delete(b.badgerPrefix)
	case Remote:
		panic("not supported")
	}
	return nil
}

func (b Bucket) Cursor() *Cursor {
	c := &Cursor{bucket: b, ctx: b.tx.ctx, provider: b.tx.db.opts.provider}
	switch c.provider {
	case Bolt:
		// nothing to do
	case Badger:
		c.badgerOpts = badger.DefaultIteratorOptions
		c.badgerOpts.Prefix = append(b.badgerPrefix[:b.nameLen], c.prefix...) // set bucket
	case Remote:
		c.remoteOpts = remote.DefaultCursorOpts
	}
	return c
}

func (c *Cursor) initCursor() {
	switch c.provider {
	case Bolt:
		if c.bolt == nil {
			c.bolt = c.bucket.bolt.Cursor()
		}
	case Badger:
		if c.badger == nil {
			c.badger = c.bucket.tx.badger.NewIterator(c.badgerOpts)
			// add to auto-cleanup on end of transactions
			if c.bucket.tx.badgerIterators == nil {
				c.bucket.tx.badgerIterators = make([]*badger.Iterator, 0, 1)
			}
			c.bucket.tx.badgerIterators = append(c.bucket.tx.badgerIterators, c.badger)
		}
	case Remote:
		if c.remote == nil {
			c.remote = c.bucket.remote.Cursor(c.remoteOpts)
		}
	}
}

func (c *Cursor) First() ([]byte, []byte, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, c.ctx.Err()
	default:
	}

	c.initCursor()

	switch c.provider {
	case Bolt:
		if c.prefix != nil {
			c.k, c.v = c.bolt.Seek(c.prefix)
		} else {
			c.k, c.v = c.bolt.First()
		}
	case Badger:
		c.badger.Rewind()
		if !c.badger.Valid() {
			c.k = nil
			break
		}
		item := c.badger.Item()
		c.k = item.Key()[c.bucket.nameLen:]
		if c.badgerOpts.PrefetchValues {
			c.v, c.err = item.ValueCopy(c.v) // bech show: using .ValueCopy on same buffer has same speed as item.Value()
		}
	case Remote:
		if c.prefix != nil {
			c.k, c.v, c.err = c.remote.Seek(c.prefix)
		} else {
			c.k, c.v, c.err = c.remote.First()
		}
	}
	return c.k, c.v, c.err
}

func (c *Cursor) Seek(seek []byte) ([]byte, []byte, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, c.ctx.Err()
	default:
	}

	c.initCursor()

	switch c.provider {
	case Bolt:
		c.k, c.v = c.bolt.Seek(seek)
	case Badger:
		c.bucket.badgerPrefix = append(c.bucket.badgerPrefix[:c.bucket.nameLen], seek...)
		c.badger.Seek(c.bucket.badgerPrefix)
		if !c.badger.Valid() {
			c.k = nil
			break
		}
		item := c.badger.Item()
		c.k = item.Key()[c.bucket.nameLen:]
		if c.badgerOpts.PrefetchValues {
			c.v, c.err = item.ValueCopy(c.v)
		}
	case Remote:
		c.k, c.v, c.err = c.remote.Seek(seek)
	}
	return c.k, c.v, c.err
}

func (c *Cursor) Next() ([]byte, []byte, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, c.ctx.Err()
	default:
	}

	switch c.provider {
	case Bolt:
		c.k, c.v = c.bolt.Next()
		if c.prefix != nil && !bytes.HasPrefix(c.k, c.prefix) {
			return nil, nil, nil
		}
	case Badger:
		c.badger.Next()
		if !c.badger.Valid() {
			c.k = nil
			break
		}
		item := c.badger.Item()
		c.k = item.Key()[c.bucket.nameLen:]
		if c.badgerOpts.PrefetchValues {
			c.v, c.err = item.ValueCopy(c.v)
		}
	case Remote:
		c.k, c.v, c.err = c.remote.Next()
		if c.err != nil {
			return nil, nil, c.err
		}

		if c.prefix != nil && !bytes.HasPrefix(c.k, c.prefix) {
			return nil, nil, nil
		}
	}
	return c.k, c.v, c.err
}

func (c *Cursor) FirstKey() ([]byte, uint64, error) {
	select {
	case <-c.ctx.Done():
		return nil, 0, c.ctx.Err()
	default:
	}

	c.initCursor()

	var vSize uint64
	switch c.provider {
	case Bolt:
		var v []byte
		if c.prefix != nil {
			c.k, v = c.bolt.Seek(c.prefix)
		} else {
			c.k, v = c.bolt.First()
		}
		vSize = uint64(len(v))
	case Badger:
		c.badger.Rewind()
		if !c.badger.Valid() {
			c.k = nil
			break
		}
		item := c.badger.Item()
		c.k = item.Key()[c.bucket.nameLen:]
		vSize = uint64(item.ValueSize())
	case Remote:
		var vIsEmpty bool
		if c.prefix != nil {
			c.k, vIsEmpty, c.err = c.remote.SeekKey(c.prefix)
		} else {
			c.k, vIsEmpty, c.err = c.remote.FirstKey()
		}
		if !vIsEmpty {
			vSize = 1
		}
	}
	return c.k, vSize, c.err
}

func (c *Cursor) SeekKey(seek []byte) ([]byte, uint64, error) {
	select {
	case <-c.ctx.Done():
		return nil, 0, c.ctx.Err()
	default:
	}

	c.initCursor()

	var vSize uint64
	switch c.provider {
	case Bolt:
		var v []byte
		c.k, v = c.bolt.Seek(seek)
		vSize = uint64(len(v))
	case Badger:
		c.bucket.badgerPrefix = append(c.bucket.badgerPrefix[:c.bucket.nameLen], seek...)
		c.badger.Seek(c.bucket.badgerPrefix)
		if !c.badger.Valid() {
			c.k = nil
			break
		}
		item := c.badger.Item()
		c.k = item.Key()[c.bucket.nameLen:]
		vSize = uint64(item.ValueSize())
	case Remote:
		var vIsEmpty bool
		c.k, vIsEmpty, c.err = c.remote.SeekKey(seek)
		if !vIsEmpty {
			vSize = 1
		}
	}
	return c.k, vSize, c.err
}

func (c *Cursor) NextKey() ([]byte, uint64, error) {
	select {
	case <-c.ctx.Done():
		return nil, 0, c.ctx.Err()
	default:
	}

	var vSize uint64
	switch c.provider {
	case Bolt:
		var v []byte
		c.k, v = c.bolt.Next()
		vSize = uint64(len(v))
		if c.prefix != nil && !bytes.HasPrefix(c.k, c.prefix) {
			return nil, 0, nil
		}
	case Badger:
		c.badger.Next()
		if !c.badger.Valid() {
			c.k = nil
			break
		}
		item := c.badger.Item()
		c.k = item.Key()[c.bucket.nameLen:]
		vSize = uint64(item.ValueSize())
	case Remote:
		var vIsEmpty bool
		c.k, vIsEmpty, c.err = c.remote.NextKey()
		if !vIsEmpty {
			vSize = 1
		}
		if c.err != nil {
			return nil, 0, c.err
		}
		if c.prefix != nil && !bytes.HasPrefix(c.k, c.prefix) {
			return nil, 0, nil
		}
	}
	return c.k, vSize, c.err
}

func (c *Cursor) Walk(walker func(k, v []byte) (bool, error)) error {
	for k, v, err := c.First(); k != nil || err != nil; k, v, err = c.Next() {
		if err != nil {
			return err
		}
		ok, err := walker(k, v)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return nil
}

func (c *Cursor) WalkKeys(walker func(k []byte, vSize uint64) (bool, error)) error {
	for k, vSize, err := c.FirstKey(); k != nil || err != nil; k, vSize, err = c.NextKey() {
		if err != nil {
			return err
		}
		ok, err := walker(k, vSize)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return nil
}
