package iterator_test

import (
	"slices"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// scriptIter is a test fake: it walks entries as given, with no order
// checks, and fails with err when it would move onto index failAt
// (failAt < 0 never fails). It lets tests feed broken or failing input
// that NewSlice would refuse.
type scriptIter struct {
	entries []keys.Entry
	failAt  int
	err     error
	pos     int // -1 before positioning
	closed  bool
}

func newScript(failAt int, err error, entries ...keys.Entry) *scriptIter {
	return &scriptIter{entries: entries, failAt: failAt, err: err, pos: -1}
}

func (s *scriptIter) moveTo(i int) {
	s.pos = i
	if i == s.failAt {
		s.pos = len(s.entries)
		s.failAt = -2 // report the error from now on
	}
}

func (s *scriptIter) First() { s.moveTo(0) }

func (s *scriptIter) SeekGE(key []byte) {
	i := slices.IndexFunc(s.entries, func(e keys.Entry) bool { return string(e.Key) >= string(key) })
	if i < 0 {
		i = len(s.entries)
	}
	s.moveTo(i)
}

func (s *scriptIter) Valid() bool { return s.Err() == nil && s.pos >= 0 && s.pos < len(s.entries) }

func (s *scriptIter) Entry() keys.Entry { return s.entries[s.pos] }

func (s *scriptIter) Next() { s.moveTo(s.pos + 1) }

func (s *scriptIter) Err() error {
	if s.failAt == -2 {
		return s.err
	}
	return nil
}

func (s *scriptIter) Close() error {
	s.closed = true
	return s.Err()
}

var _ iterator.Iterator = (*scriptIter)(nil)

// sorted returns a sorted copy of entries.
func sorted(entries ...keys.Entry) []keys.Entry {
	out := slices.Clone(entries)
	slices.SortFunc(out, keys.Compare)
	return out
}

// drainAll walks it from its current position and returns the cloned
// entries, without failing on error (callers check Err themselves).
func drainAll(it iterator.Iterator) []keys.Entry {
	var out []keys.Entry
	for ; it.Valid(); it.Next() {
		out = append(out, it.Entry().Clone())
	}
	return out
}
