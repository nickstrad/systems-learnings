package iterator_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// fuzzKeys is a small key space, so versions of one key collide across
// children. It includes the empty key and bytes at both ends of the range.
var fuzzKeys = []string{"", "a", "ab", "b", "ba", "c", "\x00", "\xff"}

// FuzzMergeIterator checks the read stack (dedup over merge) against the
// reference model itertest.NewestOnly: sort everything, keep the first
// version of each key, and optionally drop tombstones.
//
// Bytes: data[0] picks 1–5 children; each following byte b is one entry,
// with key fuzzKeys[b&7], kind put if b&8, and child (b>>4) % children.
// Sequence numbers are the entry's index, so they are unique.
func FuzzMergeIterator(f *testing.F) {
	f.Add([]byte{}, "")
	f.Add([]byte{0, 1, 9, 2}, "a")
	f.Add([]byte{2, 0x19, 0x21, 0x09, 0x41, 0x1b, 0x23}, "b")
	f.Add([]byte{4, 0x0f, 0x17, 0x27, 0x37, 0x47, 0x0e, 0x1e, 0x08, 0x00}, "\x00")
	f.Add([]byte{1, 0x01, 0x09, 0x11, 0x19, 0x01}, "zz")
	f.Fuzz(func(t *testing.T, data []byte, seek string) {
		if len(data) == 0 {
			data = []byte{0}
		}
		n := int(data[0])%5 + 1
		parts := make([][]keys.Entry, n)
		var all []keys.Entry
		for i, b := range data[1:] {
			e := keys.Entry{Key: []byte(fuzzKeys[b&7]), Seq: uint64(i + 1), Kind: keys.KindDelete}
			if b&8 != 0 {
				e.Kind = keys.KindPut
				e.Value = []byte{b}
			}
			c := int(b>>4) % n
			parts[c] = append(parts[c], e)
			all = append(all, e)
		}
		slices.SortFunc(all, keys.Compare)
		build := func() iterator.Iterator {
			children := make([]iterator.Iterator, n)
			for i, p := range parts {
				children[i] = iterator.NewSlice(sorted(p...))
			}
			return iterator.NewMerging(children...)
		}

		// The merge alone yields everything, in order.
		merged := build()
		merged.First()
		if got := itertest.Drain(t, merged); !itertest.Equal(got, all) {
			t.Fatalf("merge: got %v, want %v", got, all)
		}
		merged.Close()

		for _, drop := range []bool{false, true} {
			want := itertest.NewestOnly(all, drop)
			it := iterator.NewDedup(build(), drop)
			it.First()
			if got := itertest.Drain(t, it); !itertest.Equal(got, want) {
				t.Fatalf("drop=%v from First: got %v, want %v", drop, got, want)
			}
			it.SeekGE([]byte(seek))
			wantFrom := slices.DeleteFunc(slices.Clone(want), func(e keys.Entry) bool {
				return bytes.Compare(e.Key, []byte(seek)) < 0
			})
			if got := itertest.Drain(t, it); !itertest.Equal(got, wantFrom) {
				t.Fatalf("drop=%v from SeekGE(%q): got %v, want %v", drop, seek, got, wantFrom)
			}
			if err := it.Close(); err != nil {
				t.Fatalf("Close = %v", err)
			}
		}
	})
}
