package iterator

import (
	"bytes"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// dedupIter keeps only the newest version of each user key.
//
// Invariant: the yielded user keys strictly increase.
type dedupIter struct {
	it             Iterator
	dropTombstones bool

	prev    []byte // the user key yielded last; our own copy
	hasPrev bool
	skip    []byte // scratch copy of a key being skipped past
}

// NewDedup returns an Iterator that yields only the newest version of
// each user key in it: the first one seen, because keys.Compare puts the
// highest sequence number first. Older versions are hidden, which is how
// a newer put or delete overrides older ones on reads, and how flush and
// compaction throw away overwritten values.
//
// If dropTombstones is true, a key whose newest version is a delete is
// hidden entirely. Scan uses this, since a deleted key has no value to
// show. A key that was deleted and later put again still shows: its
// newest version is the put.
//
// Dropping tombstones while writing a file is only safe if no older
// version of the key can exist anywhere below the output, i.e. when
// compacting into the bottom level. Otherwise the tombstone disappears
// but an older put in a deeper level does not, and the deleted value
// comes back ("resurrection"). The caller decides: pass true only for
// reads that see every level, or for compaction into the bottom level.
//
// NewDedup owns it: Close closes it.
//
//	pre:  it yields entries in keys.Compare order
//	post: user keys come out strictly increasing, each at its newest
//	      version
func NewDedup(it Iterator, dropTombstones bool) Iterator {
	return &dedupIter{it: it, dropTombstones: dropTombstones}
}

func (d *dedupIter) First() {
	d.hasPrev = false
	d.it.First()
	d.settle()
}

func (d *dedupIter) SeekGE(key []byte) {
	// The inner SeekGE lands on the newest version of the first key
	// >= key, which is exactly where dedup should start.
	d.hasPrev = false
	d.it.SeekGE(key)
	d.settle()
}

func (d *dedupIter) Valid() bool { return d.it.Valid() }

func (d *dedupIter) Entry() keys.Entry {
	assert.That(d.Valid(), "Entry called on an invalid dedup iterator")
	return d.it.Entry()
}

func (d *dedupIter) Next() {
	assert.That(d.Valid(), "Next called on an invalid dedup iterator")
	d.prev = append(d.prev[:0], d.it.Entry().Key...)
	d.hasPrev = true
	d.skipVersionsOf(d.prev)
	d.settle()
	d.checkInvariant()
}

func (d *dedupIter) Err() error { return d.it.Err() }

func (d *dedupIter) Close() error { return d.it.Close() }

// settle leaves the inner iterator on the newest version of a key to
// yield, skipping deleted keys when dropTombstones is set.
//
//	pre:  the inner iterator is on the newest version of its key, or
//	      not Valid
func (d *dedupIter) settle() {
	for d.dropTombstones && d.it.Valid() && d.it.Entry().Kind == keys.KindDelete {
		d.skip = append(d.skip[:0], d.it.Entry().Key...)
		d.skipVersionsOf(d.skip)
	}
}

// skipVersionsOf advances the inner iterator past every entry whose user
// key is key. key must not alias the inner iterator's buffers.
func (d *dedupIter) skipVersionsOf(key []byte) {
	for d.it.Next(); d.it.Valid() && bytes.Equal(d.it.Entry().Key, key); d.it.Next() {
	}
}

// checkInvariant asserts that the key now yielded sorts strictly after
// the previous one.
func (d *dedupIter) checkInvariant() {
	if !assert.Enabled || !d.hasPrev || !d.Valid() {
		return
	}
	if cur := d.it.Entry().Key; bytes.Compare(d.prev, cur) >= 0 { // per Next: no boxing on the passing path
		assert.Fail("dedup iterator: key %q came out after %q", cur, d.prev)
	}
}
