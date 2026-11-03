package iterator_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

// Two children holding the same (key, seq) means sequence numbers were
// reused somewhere: a bug the merge must not hide.
func TestMergingAssertsNoEqualEntries(t *testing.T) {
	it := iterator.NewMerging(
		iterator.NewSlice([]keys.Entry{itertest.Put("a", 1, ""), itertest.Put("k", 7, "x")}),
		iterator.NewSlice([]keys.Entry{itertest.Put("k", 7, "y")}),
	)
	it.First()
	asserttest.Panics(t, "two children hold the same entry", func() {
		for ; it.Valid(); it.Next() {
		}
	})
}

// A child that yields out of order breaks the merge's output order.
func TestMergingAssertsIncreasingOutput(t *testing.T) {
	it := iterator.NewMerging(newScript(-1, nil, itertest.Put("b", 1, ""), itertest.Put("a", 2, "")))
	it.First()
	asserttest.Panics(t, "came out after", func() { it.Next() })
}

func TestDedupAssertsIncreasingKeys(t *testing.T) {
	it := iterator.NewDedup(newScript(-1, nil, itertest.Put("b", 1, ""), itertest.Put("a", 2, "")), false)
	it.First()
	asserttest.Panics(t, "came out after", func() { it.Next() })
}

func TestSliceAssertsSortedInput(t *testing.T) {
	asserttest.Panics(t, "does not sort after", func() {
		iterator.NewSlice([]keys.Entry{itertest.Put("a", 1, ""), itertest.Put("a", 1, "")})
	})
}
