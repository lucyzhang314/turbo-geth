package pool

import (
	"sync"
	"sync/atomic"

	"github.com/valyala/bytebufferpool"
)

// pool represents byte buffer pool.
//
// Distinct pools may be used for distinct types of byte buffers.
// Properly determined byte buffer types with their own pools may help reducing
// memory waste.
type pool struct {
	defaultSize uint64
	maxSize     uint64

	pool sync.Pool
}

func newPool(defaultSize uint) *pool {
	return &pool{
		defaultSize: uint64(defaultSize),
		maxSize:     uint64(defaultSize),
	}
}

// Get returns new byte buffer with zero length.
//
// The byte buffer may be returned to the pool via Put after the use
// in order to minimize GC overhead.
func (p *pool) Get() *bytebufferpool.ByteBuffer {
	v := p.pool.Get()
	if v != nil {
		return v.(*bytebufferpool.ByteBuffer)
	}

	return &bytebufferpool.ByteBuffer{
		B: make([]byte, 0, atomic.LoadUint64(&p.defaultSize)),
	}
}

// Put releases byte buffer obtained via Get to the pool.
//
// The buffer mustn't be accessed after returning to the pool.
func (p *pool) Put(b *bytebufferpool.ByteBuffer) {
	maxSize := int(atomic.LoadUint64(&p.maxSize))
	if maxSize == 0 || cap(b.B) <= maxSize {
		b.Reset()
		p.pool.Put(b)
	}
}
