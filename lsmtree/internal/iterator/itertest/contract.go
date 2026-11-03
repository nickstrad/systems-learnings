// Package itertest is the contract suite every iterator.Iterator must
// pass: the memtable, SSTable, and merging iterators.
package itertest

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// Build returns an iterator over exactly entries, which are given in
// keys.Compare order with no two entries comparing equal. Sequence numbers
// are unique, so a builder may re-sort entries by seq to insert them.
//
// A builder may call t.Skip for inputs its implementation cannot hold,
// such as no entries (an SSTable is never empty) or several versions of
// one key (an SSTable holds one). The skip drops only that subtest.
type Build func(t *testing.T, entries []keys.Entry) iterator.Iterator

// Contract runs the iterator contract against the iterators build makes.
func Contract(t *testing.T, build Build) {
	sizes := []int{0, 1, 2, 100, 1000}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("FirstWalksAll/n=%d", n), func(t *testing.T) {
			want := Entries(n)
			it := build(t, want)
			defer it.Close()
			it.First()
			got := Drain(t, it)
			if !Equal(got, want) {
				t.Fatalf("walk from First returned %d entries, want %d (first mismatch at %d)",
					len(got), len(want), mismatch(got, want))
			}
		})
	}

	t.Run("NotValidBeforePositioning", func(t *testing.T) {
		it := build(t, Entries(3))
		defer it.Close()
		if it.Valid() {
			t.Fatal("a new iterator is Valid before First or SeekGE")
		}
	})

	t.Run("SeekGE", func(t *testing.T) {
		// Keys k000, k002, ..., k008, one version each.
		var entries []keys.Entry
		for i := 0; i < 10; i += 2 {
			entries = append(entries, Put(fmt.Sprintf("k%03d", i), uint64(100+i), "v"))
		}
		cases := []struct {
			target  string
			wantKey string // "" means exhausted
		}{
			{"", "k000"},     // before everything
			{"k000", "k000"}, // exact first
			{"k001", "k002"}, // between keys
			{"k004", "k004"}, // exact, in the middle
			{"k008", "k008"}, // exact last
			{"k009", ""},     // past the end
		}
		it := build(t, entries)
		defer it.Close()
		for _, c := range cases {
			it.SeekGE([]byte(c.target))
			switch {
			case c.wantKey == "" && it.Valid():
				t.Errorf("SeekGE(%q) is on %q, want exhausted", c.target, it.Entry().Key)
			case c.wantKey != "" && !it.Valid():
				t.Errorf("SeekGE(%q) is exhausted (err %v), want %q", c.target, it.Err(), c.wantKey)
			case c.wantKey != "" && string(it.Entry().Key) != c.wantKey:
				t.Errorf("SeekGE(%q) = %q, want %q", c.target, it.Entry().Key, c.wantKey)
			}
		}
	})

	t.Run("SeekGELandsOnNewestVersion", func(t *testing.T) {
		entries := []keys.Entry{Put("a", 9, "a"), Put("k", 50, "newer"), Put("k", 7, "older"), Put("z", 1, "z")}
		it := build(t, entries)
		defer it.Close()
		it.SeekGE([]byte("k"))
		if !it.Valid() || string(it.Entry().Key) != "k" || it.Entry().Seq != 50 {
			t.Fatalf("SeekGE(k) did not land on the newest version k@50 (valid=%v)", it.Valid())
		}
		it.Next()
		if !it.Valid() || it.Entry().Seq != 7 {
			t.Fatal("Next after the newest version did not reach the older version k@7")
		}
	})

	t.Run("SeekThenNextContinuesInOrder", func(t *testing.T) {
		entries := Entries(50)
		it := build(t, entries)
		defer it.Close()
		it.SeekGE(entries[20].Key)
		if got := Drain(t, it); !Equal(got, entries[20:]) {
			t.Fatalf("SeekGE then Next gave %d entries, want %d", len(got), len(entries[20:]))
		}
	})

	t.Run("RepositionAfterExhausted", func(t *testing.T) {
		entries := Entries(5)
		it := build(t, entries)
		defer it.Close()
		it.First()
		Drain(t, it)
		it.First()
		if !it.Valid() || !bytes.Equal(it.Entry().Key, entries[0].Key) {
			t.Fatal("First after exhausting did not return to the first entry")
		}
	})

	t.Run("TombstonesAndEmptyValues", func(t *testing.T) {
		entries := []keys.Entry{
			Put("a", 3, ""),
			Del("b", 2),
			Put("c", 1, "x"),
		}
		it := build(t, entries)
		defer it.Close()
		it.First()
		if got := Drain(t, it); !Equal(got, entries) {
			t.Fatalf("got %v, want %v", got, entries)
		}
	})

	t.Run("CloseReturnsNilWithoutError", func(t *testing.T) {
		it := build(t, Entries(3))
		it.First()
		if err := it.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	})
}

// Entries returns n puts with distinct, sorted keys "key000000", ... and
// varied value lengths, including empty.
func Entries(n int) []keys.Entry {
	out := make([]keys.Entry, n)
	for i := range out {
		out[i] = Put(fmt.Sprintf("key%06d", i), uint64(i+1), string(bytes.Repeat([]byte{'v'}, i%37)))
	}
	return out
}

// Drain reads every remaining entry, cloned, and fails the test on an
// iterator error.
func Drain(t *testing.T, it iterator.Iterator) []keys.Entry {
	t.Helper()
	var out []keys.Entry
	for ; it.Valid(); it.Next() {
		out = append(out, it.Entry().Clone())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	return out
}

// Equal reports whether two entry lists match exactly. Nil and empty
// values count as equal.
func Equal(a, b []keys.Entry) bool {
	return mismatch(a, b) == -1
}

func mismatch(a, b []keys.Entry) int {
	for i := range min(len(a), len(b)) {
		x, y := a[i], b[i]
		if !bytes.Equal(x.Key, y.Key) || x.Seq != y.Seq || x.Kind != y.Kind || !bytes.Equal(x.Value, y.Value) {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

// Put returns a put entry.
func Put(key string, seq uint64, value string) keys.Entry {
	return keys.Entry{Key: []byte(key), Seq: seq, Kind: keys.KindPut, Value: []byte(value)}
}

// Del returns a tombstone.
func Del(key string, seq uint64) keys.Entry {
	return keys.Entry{Key: []byte(key), Seq: seq, Kind: keys.KindDelete}
}

// NewestOnly is the reference model for one version per key: given
// entries in keys.Compare order, it keeps the first (newest) entry of each
// user key, and with dropTombstones also leaves out keys whose newest
// entry is a delete. Flush, compaction, and Scan must all agree with it.
func NewestOnly(sorted []keys.Entry, dropTombstones bool) []keys.Entry {
	var out []keys.Entry
	for i, e := range sorted {
		if i > 0 && bytes.Equal(sorted[i-1].Key, e.Key) {
			continue
		}
		if dropTombstones && e.Kind == keys.KindDelete {
			continue
		}
		out = append(out, e)
	}
	return out
}
