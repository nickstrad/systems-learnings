package sstable

import (
	"bytes"
	"sort"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// tableIter walks a table one data block at a time. It reuses one block
// buffer and one entry slice for every block it loads, so an entry it
// returned is only good until the next block is loaded (the Iterator
// contract allows that; callers Clone what they keep).
//
// Invariant: Valid() ⇔ err == nil && 0 <= pos < len(entries), and then
// entries is the decoded data block number block.
type tableIter struct {
	r       *Reader
	block   int          // index of the loaded block; -1 if none
	entries []keys.Entry // the loaded block's entries; empty if none
	buf     []byte       // the loaded block's bytes, which entries alias
	pos     int
	err     error // first error; sticky
}

func (it *tableIter) First() {
	it.load(0)
	it.pos = 0
	it.checkInvariant()
}

func (it *tableIter) SeekGE(key []byte) {
	i := it.r.search(key)
	if i == len(it.r.index) {
		it.exhaust()
		return
	}
	it.load(i)
	// The block's last key is >= key, so this lands inside the block.
	it.pos = sort.Search(len(it.entries), func(j int) bool {
		return bytes.Compare(it.entries[j].Key, key) >= 0
	})
	it.checkInvariant()
}

func (it *tableIter) Valid() bool {
	return it.err == nil && it.pos < len(it.entries)
}

func (it *tableIter) Entry() keys.Entry {
	assert.That(it.Valid(), "sstable: Entry on an invalid iterator")
	return it.entries[it.pos]
}

func (it *tableIter) Next() {
	assert.That(it.Valid(), "sstable: Next on an invalid iterator")
	it.pos++
	if it.pos == len(it.entries) {
		if it.block+1 < len(it.r.index) {
			it.load(it.block + 1)
		} else {
			it.exhaust()
		}
		it.pos = 0
	}
	it.checkInvariant()
}

func (it *tableIter) Err() error { return it.err }

func (it *tableIter) Close() error {
	it.exhaust()
	it.entries, it.buf = nil, nil
	return it.err
}

// load reads block i, unless it is already loaded. On error the iterator
// becomes invalid and keeps the error.
func (it *tableIter) load(i int) {
	if it.err != nil || it.block == i {
		return
	}
	var err error
	it.entries, it.buf, err = it.r.readDataBlock(i, it.buf, it.entries)
	if err != nil {
		it.err = err
		it.exhaust()
		return
	}
	it.block = i
}

// exhaust drops the loaded block, leaving the iterator past the end. It
// keeps the memory for reuse.
func (it *tableIter) exhaust() {
	it.block, it.entries, it.pos = -1, it.entries[:0], 0
}

func (it *tableIter) checkInvariant() {
	if it.err == nil && len(it.entries) > 0 && (it.block < 0 || it.pos >= len(it.entries)) {
		assert.Fail("sstable: iterator at pos %d of %d in block %d", it.pos, len(it.entries), it.block)
	}
}
