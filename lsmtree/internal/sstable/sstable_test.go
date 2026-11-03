package sstable_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

func TestFinishWithNoEntriesFails(t *testing.T) {
	fs := newFS(t)
	f, err := fs.Create(tablePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sstable.NewWriter(f, sstable.WriterOptions{}).Finish(); !errors.Is(err, sstable.ErrEmpty) {
		t.Fatalf("Finish with no entries = %v, want ErrEmpty", err)
	}
	if _, err := f.Write([]byte("x")); err == nil {
		t.Fatal("Finish left the file open")
	}
}

// TestRoundTripAtBlockBoundaries writes and reads back tables whose sizes
// sit on and just past the data block boundary.
func TestRoundTripAtBlockBoundaries(t *testing.T) {
	const blockSize = 100
	one := encodedSize(5, 10) // "k0000" with a 10-byte value: 26 bytes
	cases := []struct {
		name       string
		blockSize  int
		entries    []keys.Entry
		wantBlocks int
	}{
		{"one entry", blockSize, numbered(1, 10), 1},
		{"exactly one full block", one * 4, numbered(4, 10), 1},
		{"one byte over a block", one*4 - 1, numbered(4, 10), 2},
		{"many blocks", blockSize, numbered(1000, 10), 334}, // 3 entries (78 B) per block; a 4th would make 104
		{"key larger than the block", 16, []keys.Entry{itertest.Put("a", 1, ""), itertest.Put(strings.Repeat("b", 64), 2, "v"), itertest.Put("c", 3, "")}, 3},
		{"key of the maximum size", blockSize, []keys.Entry{itertest.Put(strings.Repeat("m", keys.MaxKeySize), 1, "v")}, 1},
		{"empty values and tombstones", blockSize, []keys.Entry{itertest.Put("0", 9, ""), itertest.Del("a", 8), itertest.Put("b", 7, ""), itertest.Del("c", 1)}, 1},
		{"one-byte key alone", blockSize, []keys.Entry{itertest.Put("k", 1, "v")}, 1},
		{"default block size", 0, numbered(1000, 10), 7}, // 157 entries (4082 B) per 4 KiB block
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, sum := build(t, sstable.WriterOptions{BlockSize: c.blockSize, BloomBitsPerKey: 10}, c.entries)
			if sum.Blocks != c.wantBlocks {
				t.Errorf("Blocks = %d, want %d", sum.Blocks, c.wantBlocks)
			}
			it := r.NewIterator()
			it.First()
			if got := itertest.Drain(t, it); !itertest.Equal(got, c.entries) {
				t.Fatalf("read back %d entries, want %d: %v", len(got), len(c.entries), got)
			}
			if err := it.Close(); err != nil {
				t.Fatal(err)
			}
			for _, e := range c.entries {
				got, ok, err := r.Get(e.Key)
				if err != nil || !ok || !itertest.Equal([]keys.Entry{got}, []keys.Entry{e}) {
					t.Fatalf("Get(%q) = %v, %v, %v; want %v", e.Key, got, ok, err, e)
				}
			}
		})
	}
}

func TestSummary(t *testing.T) {
	fs := newFS(t)
	entries := []keys.Entry{itertest.Put("apple", 7, "red"), itertest.Del("banana", 3), itertest.Put("cherry", 12, "dark")}
	sum := write(t, fs, sstable.WriterOptions{BlockSize: 16, BloomBitsPerKey: 10}, entries)
	want := sstable.Summary{
		Smallest: []byte("apple"), Largest: []byte("cherry"),
		MinSeq: 3, MaxSeq: 12, Entries: 3,
		Size: int64(len(vfstest.ReadFile(t, fs, tablePath))), Blocks: 3,
	}
	if !bytes.Equal(sum.Smallest, want.Smallest) || !bytes.Equal(sum.Largest, want.Largest) ||
		sum.MinSeq != want.MinSeq || sum.MaxSeq != want.MaxSeq || sum.Entries != want.Entries ||
		sum.Size != want.Size || sum.Blocks != want.Blocks {
		t.Fatalf("Summary = %+v, want %+v", sum, want)
	}
}

func TestAddCopiesItsInput(t *testing.T) {
	fs := newFS(t)
	f, _ := fs.Create(tablePath)
	w := sstable.NewWriter(f, sstable.WriterOptions{})
	key, value := []byte("a"), []byte("v1")
	if err := w.Add(keys.Entry{Key: key, Seq: 1, Kind: keys.KindPut, Value: value}); err != nil {
		t.Fatal(err)
	}
	key[0], value[0] = 'z', 'X' // the caller reuses its buffers
	sum, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if string(sum.Smallest) != "a" {
		t.Fatalf("Smallest = %q, want %q", sum.Smallest, "a")
	}
	got, ok, err := open(t, fs).Get([]byte("a"))
	if err != nil || !ok || string(got.Value) != "v1" {
		t.Fatalf("Get(a) = %v, %v, %v; want v1", got, ok, err)
	}
}

func TestEstimatedSize(t *testing.T) {
	fs := newFS(t)
	f, _ := fs.Create(tablePath)
	w := sstable.NewWriter(f, sstable.WriterOptions{BlockSize: 256, BloomBitsPerKey: 10})
	prev := w.EstimatedSize()
	for _, e := range numbered(500, 20) {
		if err := w.Add(e); err != nil {
			t.Fatal(err)
		}
		if got := w.EstimatedSize(); got <= prev {
			t.Fatalf("EstimatedSize did not grow after Add(%q): %d -> %d", e.Key, prev, got)
		}
		prev = w.EstimatedSize()
	}
	sum, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if diff := prev - sum.Size; diff < 0 || diff > sum.Size/10 {
		t.Fatalf("EstimatedSize %d, real size %d: want an overestimate within 10%%", prev, sum.Size)
	}
}

// TestGet covers hits and each kind of miss: before the first key, after
// the last, between keys in one block, and between blocks.
func TestGet(t *testing.T) {
	// Keys b, d, f, h, j, one per block.
	entries := []keys.Entry{itertest.Put("b", 1, "vb"), itertest.Del("d", 2), itertest.Put("f", 3, ""), itertest.Put("h", 4, "vh"), itertest.Put("j", 5, "vj")}
	for _, bits := range []int{0, 10} {
		r, _ := build(t, sstable.WriterOptions{BlockSize: 1, BloomBitsPerKey: bits}, entries)
		cases := []struct {
			key  string
			want *keys.Entry
		}{
			{"b", &entries[0]},
			{"d", &entries[1]}, // a tombstone is found, not missing
			{"f", &entries[2]},
			{"j", &entries[4]},
			{"", nil},   // before the first key
			{"a", nil},  // before the first key
			{"c", nil},  // between blocks
			{"bb", nil}, // a longer key sharing a prefix
			{"k", nil},  // after the last key
		}
		for _, c := range cases {
			got, ok, err := r.Get([]byte(c.key))
			switch {
			case err != nil:
				t.Errorf("bits=%d Get(%q): %v", bits, c.key, err)
			case c.want == nil && ok:
				t.Errorf("bits=%d Get(%q) found %v, want a miss", bits, c.key, got)
			case c.want != nil && (!ok || !itertest.Equal([]keys.Entry{got}, []keys.Entry{*c.want})):
				t.Errorf("bits=%d Get(%q) = %v, %v; want %v", bits, c.key, got, ok, *c.want)
			}
		}
	}
}

func TestGetAfterLastKeyReadsNoBlock(t *testing.T) {
	r, _ := build(t, sstable.WriterOptions{}, numbered(10, 1))
	if _, ok, err := r.Get([]byte("z")); ok || err != nil {
		t.Fatalf("Get(z) = %v, %v", ok, err)
	}
	if got := r.Stats().BlocksRead; got != 0 {
		t.Fatalf("BlocksRead = %d after a key past the end, want 0", got)
	}
}

// TestBloomNegativesAvoidBlockReads checks the point of the filter: a
// miss it rules out costs no data block read.
func TestBloomNegativesAvoidBlockReads(t *testing.T) {
	entries := numbered(1000, 10)
	r, _ := build(t, sstable.WriterOptions{BlockSize: 256, BloomBitsPerKey: 10}, entries)

	const misses = 1000
	for i := range misses {
		// Each miss key sorts between two present keys, so without the
		// filter every one would read a block.
		if _, ok, err := r.Get([]byte(string(entries[i].Key) + "-missing")); ok || err != nil {
			t.Fatalf("miss %d: found=%v err=%v", i, ok, err)
		}
	}
	s := r.Stats()
	if s.BloomChecks != misses {
		t.Errorf("BloomChecks = %d, want %d", s.BloomChecks, misses)
	}
	if s.BlocksRead != s.BloomChecks-s.BloomNegatives {
		t.Errorf("BlocksRead = %d, want one per filter false positive (%d)", s.BlocksRead, s.BloomChecks-s.BloomNegatives)
	}
	if s.BloomNegatives < misses*95/100 { // ~1% false positives at 10 bits/key
		t.Errorf("BloomNegatives = %d of %d, want at least 95%%", s.BloomNegatives, misses)
	}

	r.Get(entries[0].Key) // a hit reads exactly one block
	if got := r.Stats().BlocksRead - s.BlocksRead; got != 1 {
		t.Errorf("a hit read %d blocks, want 1", got)
	}
}

func TestNoFilterMeansNoBloomChecks(t *testing.T) {
	r, _ := build(t, sstable.WriterOptions{BloomBitsPerKey: 0}, numbered(10, 1))
	r.Get([]byte("k0003-missing"))
	if s := r.Stats(); s.BloomChecks != 0 || s.BlocksRead != 1 {
		t.Fatalf("Stats = %+v, want 0 bloom checks and 1 block read", s)
	}
}

// TestIteratorReadsBlocksLazily checks that a full scan reads each block
// once and a seek reads only the block it lands in.
func TestIteratorReadsBlocksLazily(t *testing.T) {
	entries := numbered(100, 10)
	r, sum := build(t, sstable.WriterOptions{BlockSize: 100}, entries)
	it := r.NewIterator()
	defer it.Close()
	if r.Stats().BlocksRead != 0 {
		t.Fatal("NewIterator read a block")
	}
	it.SeekGE(entries[50].Key)
	if got := r.Stats().BlocksRead; got != 1 {
		t.Fatalf("SeekGE read %d blocks, want 1", got)
	}
	it.First()
	itertest.Drain(t, it)
	if got := r.Stats().BlocksRead; got != uint64(sum.Blocks)+1 {
		t.Fatalf("seek plus full scan read %d blocks, want %d", got, sum.Blocks+1)
	}
}

func TestAddOutOfOrderTripsAssertion(t *testing.T) {
	cases := []struct {
		name   string
		second keys.Entry
		want   string
	}{
		{"smaller key", itertest.Put("a", 1, ""), "keys must strictly increase"},
		{"same key, older version", itertest.Put("b", 1, ""), "keys must strictly increase"},
		{"same key, newer version", itertest.Put("b", 9, ""), "keys must strictly increase"},
		{"invalid kind", keys.Entry{Key: []byte("c"), Seq: 1, Kind: 7}, "invalid kind 7"},
		{"tombstone with a value", keys.Entry{Key: []byte("c"), Seq: 1, Kind: keys.KindDelete, Value: []byte("v")}, "tombstone"},
		{"key over the limit", itertest.Put(strings.Repeat("c", keys.MaxKeySize+1), 1, ""), "key of 4097 bytes"},
		{"value over the limit", itertest.Put("c", 1, strings.Repeat("v", keys.MaxValueSize+1)), "value of 1048577 bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := newFS(t)
			f, _ := fs.Create(tablePath)
			w := sstable.NewWriter(f, sstable.WriterOptions{})
			if err := w.Add(itertest.Put("b", 5, "")); err != nil {
				t.Fatal(err)
			}
			asserttest.Panics(t, c.want, func() { w.Add(c.second) })
		})
	}
}

// TestWriterInvariantTrips shows the writer's byte-accounting check can
// fail: after a block is written, the bytes written must end exactly
// where the last indexed block (and its CRC) ends.
func TestWriterInvariantTrips(t *testing.T) {
	f, err := vfstest.NewMemFS().Create("/t.sst")
	if err != nil {
		t.Fatal(err)
	}
	w := sstable.NewWriter(f, sstable.WriterOptions{BlockSize: 64})
	for i := range 20 { // enough entries to write at least one block
		if err := w.Add(itertest.Put(fmt.Sprintf("k%03d", i), uint64(i+1), "value")); err != nil {
			t.Fatal(err)
		}
	}
	w.CheckInvariant() // holds
	w.SkewOffset()
	asserttest.Panics(t, "last block ends at", w.CheckInvariant)
}
