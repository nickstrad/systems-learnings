package iterator

import (
	"bytes"
	"sort"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// sliceIter is an Iterator over an in-memory, already sorted slice.
type sliceIter struct {
	entries []keys.Entry
	pos     int // len(entries) when exhausted; -1 before positioning
}

// NewSlice returns an Iterator over entries. It is the simplest Iterator:
// tests use it as a source, and so can any caller that already holds
// sorted entries in memory.
//
// The iterator does not copy entries; the caller must not change them
// while it is in use.
//
//	pre: entries are strictly increasing in keys.Compare order
func NewSlice(entries []keys.Entry) Iterator {
	if assert.Enabled {
		for i := 1; i < len(entries); i++ {
			if keys.Compare(entries[i-1], entries[i]) >= 0 {
				assert.Fail("NewSlice: entry %d (%q@%d) does not sort after entry %d (%q@%d)",
					i, entries[i].Key, entries[i].Seq, i-1, entries[i-1].Key, entries[i-1].Seq)
			}
		}
	}
	return &sliceIter{entries: entries, pos: -1}
}

func (s *sliceIter) First() { s.pos = 0 }

func (s *sliceIter) SeekGE(key []byte) {
	// Versions of one key sort newest first, so the first entry whose
	// user key is >= key is the newest version.
	s.pos = sort.Search(len(s.entries), func(i int) bool {
		return bytes.Compare(s.entries[i].Key, key) >= 0
	})
}

func (s *sliceIter) Valid() bool { return s.pos >= 0 && s.pos < len(s.entries) }

func (s *sliceIter) Entry() keys.Entry {
	assert.That(s.Valid(), "Entry called on an invalid slice iterator")
	return s.entries[s.pos]
}

func (s *sliceIter) Next() {
	assert.That(s.Valid(), "Next called on an invalid slice iterator")
	s.pos++
}

func (s *sliceIter) Err() error { return nil }

func (s *sliceIter) Close() error { return nil }
