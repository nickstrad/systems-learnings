// Package memtable is the store's memory component: the paper's C0 tree
// (O'Neil et al. 1996, §2).
//
// Writes land here first. The memtable keeps every version of every key
// in keys.Compare order (user key ascending, newest first) until the DB
// flushes it to an L0 SSTable. It is a skip list (Pugh 1990) with
// p = 1/4: each node is promoted to the next level with probability 1/4,
// so a search makes about 4·log₄ n comparisons on average (Pugh's
// log_{1/p}(n)/p; under 20 for a 64 KiB memtable of ~500 entries), and
// each node carries 4/3 forward pointers on average.
//
// Node heights come from a PCG generator seeded by the caller, so the
// same seed and the same adds always build the same list. Tests rely on
// that to be reproducible.
//
// A Memtable is not safe for concurrent use. The DB serialises every
// call (Add, Get, iteration) under its own lock.
package memtable

import (
	"bytes"
	"math"
	"math/rand/v2"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

const (
	// maxHeight caps a node's level count. With p = 1/4, 12 levels index
	// 4^12 ≈ 16M entries well, far more than a memtable holds.
	maxHeight = 12

	// fullCheckEvery bounds how far apart the O(n) invariant walks are.
	// See Memtable.Add.
	fullCheckEvery = 1 << 10
)

// EntryOverhead is the fixed number of bytes Size charges per entry on
// top of its key and value: roughly a node's struct (the keys.Entry with
// two slice headers, the seq and the kind: 64 bytes), its forward-pointer
// slice header (24 bytes), and 4/3 forward pointers on average (~11
// bytes), rounded to 100. It is an estimate for deciding when to flush,
// not an exact allocation count.
const EntryOverhead = 100

// node is one entry in the skip list. next[i] is the following node at
// level i; len(next) is the node's height.
type node struct {
	entry keys.Entry
	next  []*node
}

// Memtable is a skip list of entries in keys.Compare order.
//
//	Invariant: level 0 is strictly ordered by keys.Compare and holds
//	           exactly Len() nodes
//	Invariant: every level h > 0 is a subsequence of level h-1, and
//	           holds only nodes whose height is > h
//	Invariant: Size() == Σ (len(key) + len(value) + EntryOverhead)
//	Invariant: every entry's seq is <= maxSeq
type Memtable struct {
	head   node // sentinel; head.next has maxHeight levels
	height int  // levels in use, 1..maxHeight
	rng    *rand.Rand
	n      int
	size   int64
	maxSeq uint64 // highest seq added; meaningless while n == 0
}

// New returns an empty memtable whose node heights are drawn from a PCG
// generator seeded with seed.
func New(seed uint64) *Memtable {
	m := &Memtable{
		head:   node{next: make([]*node, maxHeight)},
		height: 1,
		rng:    rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	}
	m.checkInvariant()
	return m
}

// Len returns the number of entries (versions, not distinct keys).
func (m *Memtable) Len() int { return m.n }

// Size returns the approximate memory the entries use, in bytes: the sum
// over entries of len(Key) + len(Value) + EntryOverhead. The DB flushes
// the memtable once Size passes its limit.
//
//	post: Size() never decreases
func (m *Memtable) Size() int64 { return m.size }

// Add inserts one version of a key. It copies Key and Value, so the
// caller may reuse its buffers.
//
//	pre:  e.Validate() == nil
//	pre:  e.Seq > every seq already added (the DB hands them out in order)
//	post: Len() grew by one; Size() grew by len(Key)+len(Value)+EntryOverhead
//
// Because seqs are unique, no two entries ever compare equal, and a new
// version of a key sorts before the older ones.
func (m *Memtable) Add(e keys.Entry) {
	if assert.Enabled {
		if err := e.Validate(); err != nil {
			assert.Fail("memtable: add: %v", err)
		}
		if m.n > 0 && e.Seq <= m.maxSeq {
			assert.Fail("memtable: seq %d not above last seq %d", e.Seq, m.maxSeq)
		}
	}

	// One allocation holds both key and value. The three-index slice
	// stops an append to Key from overwriting Value.
	buf := make([]byte, len(e.Key)+len(e.Value))
	copy(buf, e.Key)
	copy(buf[len(e.Key):], e.Value)
	stored := keys.Entry{Key: buf[:len(e.Key):len(e.Key)], Seq: e.Seq, Kind: e.Kind}
	if len(e.Value) > 0 {
		stored.Value = buf[len(e.Key):]
	}

	var prev [maxHeight]*node
	m.findLess(stored, &prev)

	h := m.randomHeight()
	for i := m.height; i < h; i++ {
		prev[i] = &m.head
	}
	m.height = max(m.height, h)

	x := &node{entry: stored, next: make([]*node, h)}
	for i := range h {
		x.next[i] = prev[i].next[i]
		prev[i].next[i] = x
	}

	m.n++
	m.size += int64(len(buf)) + EntryOverhead
	m.maxSeq = e.Seq

	// A full walk after every Add would make building a list of n
	// entries cost O(n²) in tests. Instead, check the new node's
	// neighbours at every level (O(height)) on each Add, and walk the
	// whole list when Len hits a power of two or a multiple of
	// fullCheckEvery. The power-of-two walks cost O(n) in total, so
	// small lists (all of the fuzz and unit tests) are checked often,
	// and large ones still get a full walk every 1024 adds.
	if assert.Enabled {
		m.checkNeighbours(prev[:h], x)
		if m.n&(m.n-1) == 0 || m.n%fullCheckEvery == 0 {
			m.checkInvariant()
		}
	}
}

// Get returns the newest version of key, which may be a tombstone; the
// caller decides what a tombstone means. ok is false if the memtable
// holds no version of key.
//
//	post: ok ⇒ e.Key equals key, and e.Seq is the highest seq of key
//	post: e's slices belong to the memtable; the caller must not modify
//	      them
func (m *Memtable) Get(key []byte) (e keys.Entry, ok bool) {
	x := m.seekGE(key)
	if x == nil || !bytes.Equal(x.entry.Key, key) {
		return keys.Entry{}, false
	}
	return x.entry, true
}

// NewIterator returns an iterator over every version of every key, in
// keys.Compare order. Flush deduplicates versions later.
//
// The iterator reads the live list. Entries added while it is open may
// or may not be seen; the DB only iterates a memtable it has stopped
// writing to, or under its lock.
func (m *Memtable) NewIterator() iterator.Iterator {
	return &memIterator{m: m}
}

// findLess fills prev[i], for every level i < m.height, with the last
// node at level i that sorts before e (the head if none does).
func (m *Memtable) findLess(e keys.Entry, prev *[maxHeight]*node) {
	x := &m.head
	for i := m.height - 1; i >= 0; i-- {
		for x.next[i] != nil && keys.Compare(x.next[i].entry, e) < 0 {
			x = x.next[i]
		}
		prev[i] = x
	}
}

// seekGE returns the first node whose user key is >= key, or nil. With
// several versions of that key it is the newest, because the probe
// carries the highest possible seq and so sorts before all of them.
func (m *Memtable) seekGE(key []byte) *node {
	var prev [maxHeight]*node
	m.findLess(keys.Entry{Key: key, Seq: math.MaxUint64}, &prev)
	return prev[0].next[0]
}

// randomHeight returns 1 plus the number of consecutive successes, each
// with probability 1/4, capped at maxHeight.
func (m *Memtable) randomHeight() int {
	h := 1
	for h < maxHeight && m.rng.Uint32()&3 == 0 {
		h++
	}
	return h
}

// checkNeighbours asserts the local ordering around a newly inserted
// node x: at each of its levels, its predecessor sorts before it and its
// successor after it.
func (m *Memtable) checkNeighbours(prev []*node, x *node) {
	for i, p := range prev {
		assert.That(p == &m.head || keys.Compare(p.entry, x.entry) < 0, "memtable: predecessor not before new node")
		assert.That(x.next[i] == nil || keys.Compare(x.entry, x.next[i].entry) < 0, "memtable: successor not after new node")
	}
}

// checkInvariant walks the whole list and asserts the type's invariants.
// It is O(n·height), so it runs only when assertions are enabled. The
// per-node checks pass no format args, so a passing walk allocates
// nothing.
func (m *Memtable) checkInvariant() {
	if !assert.Enabled {
		return
	}
	assert.That(m.height >= 1 && m.height <= maxHeight, "memtable: height %d out of range", m.height)
	for i := m.height; i < maxHeight; i++ {
		assert.That(m.head.next[i] == nil, "memtable: unused level %d is not empty", i)
	}

	count, size := 0, int64(0)
	for x := m.head.next[0]; x != nil; x = x.next[0] {
		count++
		size += int64(len(x.entry.Key)+len(x.entry.Value)) + EntryOverhead
		assert.That(x.entry.Seq <= m.maxSeq, "memtable: an entry's seq is above maxSeq")
		if y := x.next[0]; y != nil {
			assert.That(keys.Compare(x.entry, y.entry) < 0, "memtable: level 0 out of order")
		}
	}
	assert.That(count == m.n, "memtable: level 0 has %d nodes, Len is %d", count, m.n)
	assert.That(size == m.size, "memtable: entries add up to %d bytes, Size is %d", size, m.size)

	// Level h is a subsequence of level h-1: walk both together, and
	// every node met at level h must turn up at level h-1 before we
	// pass it.
	for h := 1; h < m.height; h++ {
		lower := m.head.next[h-1]
		for x := m.head.next[h]; x != nil; x = x.next[h] {
			assert.That(len(x.next) > h, "memtable: node linked above its height")
			for lower != nil && lower != x {
				lower = lower.next[h-1]
			}
			assert.That(lower == x, "memtable: a level is not a subsequence of the one below")
		}
	}
}

// memIterator walks the memtable's level 0.
type memIterator struct {
	m   *Memtable
	cur *node // nil when not positioned or exhausted
}

func (it *memIterator) First() { it.cur = it.m.head.next[0] }

func (it *memIterator) SeekGE(key []byte) { it.cur = it.m.seekGE(key) }

func (it *memIterator) Valid() bool { return it.cur != nil }

func (it *memIterator) Entry() keys.Entry {
	assert.That(it.cur != nil, "memtable: Entry on an invalid iterator")
	return it.cur.entry
}

func (it *memIterator) Next() {
	assert.That(it.cur != nil, "memtable: Next on an invalid iterator")
	it.cur = it.cur.next[0]
}

// Err is always nil: the memtable lives in memory and cannot be corrupt.
func (it *memIterator) Err() error { return nil }

func (it *memIterator) Close() error {
	it.cur = nil
	return nil
}
