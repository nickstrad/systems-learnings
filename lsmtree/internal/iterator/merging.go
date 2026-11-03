package iterator

import (
	"container/heap"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// mergingIter yields the union of its children in keys.Compare order. It
// is a k-way merge: a min-heap holds every child that is on an entry,
// ordered by that entry, so the heap's top is the next entry to yield.
//
// This is the core of both reads (memtable + L0 files + levels) and
// compaction (the paper's rolling merge, §2): the merged stream goes
// through NewDedup to keep only the newest version of each key.
//
// Invariant: every child in the heap is Valid; once err is set the heap
// is empty; the yielded entries strictly increase in keys.Compare order.
type mergingIter struct {
	heap childHeap // holds the children, and the heap of those on an entry
	err  error     // the first child error; sticky

	// The previous yielded entry, kept only to assert the order
	// invariant. last.Key is our own copy, reused across steps.
	last    keys.Entry
	hasLast bool
}

// NewMerging returns an Iterator over the union of children's entries.
// It owns the children: Close closes them all.
//
// The first error any child reports stops the merge: Valid becomes false
// and Err returns that error until the iterator is closed.
//
//	pre: no two children hold an entry that compares equal. This holds
//	     in the store because sequence numbers are globally unique.
//	post: entries come out strictly increasing in keys.Compare order
func NewMerging(children ...Iterator) Iterator {
	return &mergingIter{heap: childHeap{children: children}}
}

func (m *mergingIter) First() {
	m.reposition(func(c Iterator) { c.First() })
}

func (m *mergingIter) SeekGE(key []byte) {
	m.reposition(func(c Iterator) { c.SeekGE(key) })
}

// reposition moves every child with move, then rebuilds the heap from the
// children that landed on an entry.
func (m *mergingIter) reposition(move func(Iterator)) {
	m.heap.idx = m.heap.idx[:0]
	m.hasLast = false
	if m.err != nil {
		return
	}
	for i, c := range m.heap.children {
		move(c)
		if err := c.Err(); err != nil {
			m.fail(err)
			return
		}
		if c.Valid() {
			m.heap.idx = append(m.heap.idx, i)
		}
	}
	heap.Init(&m.heap)
	m.checkInvariant()
}

func (m *mergingIter) Valid() bool { return len(m.heap.idx) > 0 }

func (m *mergingIter) Entry() keys.Entry {
	assert.That(m.Valid(), "Entry called on an invalid merging iterator")
	return m.top().Entry()
}

func (m *mergingIter) Next() {
	assert.That(m.Valid(), "Next called on an invalid merging iterator")
	if assert.Enabled {
		e := m.top().Entry()
		m.last.Key = append(m.last.Key[:0], e.Key...)
		m.last.Seq = e.Seq
		m.hasLast = true
	}

	c := m.top()
	c.Next()
	switch {
	case c.Err() != nil:
		m.fail(c.Err())
		return
	case c.Valid():
		heap.Fix(&m.heap, 0)
	default:
		heap.Pop(&m.heap)
	}
	m.checkInvariant()
}

func (m *mergingIter) Err() error { return m.err }

// Close closes every child, even after one fails, and returns the first
// error: the merge's own error, else the first child's Close error.
func (m *mergingIter) Close() error {
	first := m.err
	for _, c := range m.heap.children {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	m.heap.idx = m.heap.idx[:0]
	return first
}

func (m *mergingIter) top() Iterator { return m.heap.children[m.heap.idx[0]] }

// fail records the first error and stops the merge.
func (m *mergingIter) fail(err error) {
	if m.err == nil {
		m.err = err
	}
	m.heap.idx = m.heap.idx[:0]
}

// checkInvariant asserts that the entry now on top sorts strictly after
// the previous one. An equal entry means two children held the same
// (key, seq), which globally unique sequence numbers rule out.
func (m *mergingIter) checkInvariant() {
	if !assert.Enabled || !m.hasLast || !m.Valid() {
		return
	}
	// This runs on every Next, so it uses the if/Fail form: assert.That
	// would box its arguments even when the check passes.
	e := m.top().Entry()
	switch c := keys.Compare(m.last, e); {
	case c == 0:
		assert.Fail("merging iterator: two children hold the same entry %q@%d", e.Key, e.Seq)
	case c > 0:
		assert.Fail("merging iterator: %q@%d came out after %q@%d", e.Key, e.Seq, m.last.Key, m.last.Seq)
	}
}

// childHeap is a min-heap of child indexes, ordered by each child's
// current entry. It implements heap.Interface. children is every child,
// in the order given to NewMerging; idx holds the heap, the indexes of
// the children that are on an entry.
type childHeap struct {
	children []Iterator
	idx      []int
}

func (h *childHeap) Len() int { return len(h.idx) }

func (h *childHeap) Less(i, j int) bool {
	c := keys.Compare(h.children[h.idx[i]].Entry(), h.children[h.idx[j]].Entry())
	if c == 0 {
		// Two children are on the same entry: a broken precondition.
		// Fall back to child order so the heap stays well-defined; the
		// order invariant reports the duplicate when it is yielded.
		return h.idx[i] < h.idx[j]
	}
	return c < 0
}

func (h *childHeap) Swap(i, j int) { h.idx[i], h.idx[j] = h.idx[j], h.idx[i] }

func (h *childHeap) Push(x any) { h.idx = append(h.idx, x.(int)) }

func (h *childHeap) Pop() any {
	n := len(h.idx) - 1
	x := h.idx[n]
	h.idx = h.idx[:n]
	return x
}
