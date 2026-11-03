package iterator_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

func TestMergingZeroChildren(t *testing.T) {
	it := iterator.NewMerging()
	it.First()
	if it.Valid() {
		t.Fatal("merge of no children is Valid after First")
	}
	it.SeekGE([]byte("a"))
	if it.Valid() {
		t.Fatal("merge of no children is Valid after SeekGE")
	}
	if err := it.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

// TestMergingInterleaves covers the cases that matter for an LSM read:
// versions of one key spread over several sources (memtable, L0, L1),
// which must come out newest first.
func TestMergingInterleaves(t *testing.T) {
	cases := []struct {
		name     string
		children [][]keys.Entry
	}{
		{"one child", [][]keys.Entry{{itertest.Put("a", 1, "x"), itertest.Put("b", 2, "y")}}},
		{"all empty", [][]keys.Entry{nil, nil, nil}},
		{"disjoint ranges", [][]keys.Entry{
			{itertest.Put("a", 1, ""), itertest.Put("b", 2, "")},
			{itertest.Put("c", 3, ""), itertest.Put("d", 4, "")},
		}},
		{"versions across children", [][]keys.Entry{
			{itertest.Put("k", 9, "memtable"), itertest.Put("z", 1, "")},
			{itertest.Del("k", 5)},
			{itertest.Put("a", 2, ""), itertest.Put("k", 1, "old")},
		}},
		{"tombstones kept", [][]keys.Entry{
			{itertest.Del("a", 3)},
			{itertest.Put("a", 2, "v"), itertest.Del("b", 1)},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var all []keys.Entry
			var children []iterator.Iterator
			for _, es := range c.children {
				all = append(all, es...)
				children = append(children, iterator.NewSlice(es))
			}
			it := iterator.NewMerging(children...)
			defer it.Close()
			it.First()
			if got, want := itertest.Drain(t, it), sorted(all...); !itertest.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

// After a SeekGE, every child is repositioned, including ones that were
// already exhausted.
func TestMergingRepositions(t *testing.T) {
	it := iterator.NewMerging(
		iterator.NewSlice([]keys.Entry{itertest.Put("a", 1, ""), itertest.Put("c", 3, "")}),
		iterator.NewSlice([]keys.Entry{itertest.Put("b", 2, ""), itertest.Put("d", 4, "")}),
	)
	defer it.Close()
	it.SeekGE([]byte("c"))
	if got := itertest.Drain(t, it); len(got) != 2 || string(got[0].Key) != "c" {
		t.Fatalf("SeekGE(c) walk = %v, want c, d", got)
	}
	it.SeekGE([]byte("b"))
	if got := itertest.Drain(t, it); len(got) != 3 || string(got[0].Key) != "b" {
		t.Fatalf("second SeekGE(b) walk = %v, want b, c, d", got)
	}
}
