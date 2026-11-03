package sstable_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

// entriesFromBytes turns fuzz input into a valid table's contents: sorted,
// one entry per key. Each record is a key length byte, the key, a kind
// byte, a value length byte, and the value; short input ends the list.
// Records with an empty key are dropped, since keys are never empty.
func entriesFromBytes(data []byte) []keys.Entry {
	var out []keys.Entry
	take := func(n int) []byte {
		n = min(n, len(data))
		b := data[:n]
		data = data[n:]
		return b
	}
	for seq := uint64(1); len(data) > 0; seq++ {
		key := bytes.Clone(take(int(take(1)[0]) % 32))
		var kind byte
		if b := take(1); len(b) == 1 {
			kind = b[0]
		}
		e := keys.Entry{Key: key, Seq: seq * 7919, Kind: keys.KindPut}
		if kind%4 == 0 {
			e.Kind = keys.KindDelete
		} else if b := take(1); len(b) == 1 {
			e.Value = bytes.Clone(take(int(b[0])))
		}
		if len(key) > 0 {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b keys.Entry) int { return bytes.Compare(a.Key, b.Key) })
	return slices.CompactFunc(out, func(a, b keys.Entry) bool { return bytes.Equal(a.Key, b.Key) })
}

// FuzzSSTableRoundTrip writes fuzzer-chosen entries with a tiny block
// size and checks that the iterator, SeekGE, and Get give them back.
func FuzzSSTableRoundTrip(f *testing.F) {
	f.Add(uint8(1), uint8(10), []byte("\x01a\x01\x01x"))
	f.Add(uint8(16), uint8(0), []byte("\x03abc\x01\x05hello\x03abd\x00\x00\x01z\x02\x00"))
	f.Add(uint8(0), uint8(5), []byte("\x00\x01\x00\x01k\x01\x00"))                    // an empty key, dropped
	f.Add(uint8(40), uint8(20), bytes.Repeat([]byte("\x04keyA\x01\x08valuevvv"), 30)) // duplicates
	f.Add(uint8(255), uint8(1), []byte("\x1fzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz\x01\xff"))
	f.Fuzz(func(t *testing.T, blockSize, bits uint8, data []byte) {
		entries := entriesFromBytes(data)
		if len(entries) == 0 {
			t.Skip("a table needs at least one entry")
		}
		opts := sstable.WriterOptions{BlockSize: int(blockSize) + 1, BloomBitsPerKey: int(bits % 24)}
		r, sum := build(t, opts, entries)
		if sum.Entries != uint64(len(entries)) ||
			!bytes.Equal(sum.Smallest, entries[0].Key) || !bytes.Equal(sum.Largest, entries[len(entries)-1].Key) {
			t.Fatalf("Summary %+v does not match %d entries", sum, len(entries))
		}

		it := r.NewIterator()
		it.First()
		if got := itertest.Drain(t, it); !itertest.Equal(got, entries) {
			t.Fatalf("iterator read back %v, want %v", got, entries)
		}
		for i, e := range entries {
			it.SeekGE(e.Key)
			if !it.Valid() || !itertest.Equal([]keys.Entry{it.Entry()}, entries[i:i+1]) {
				t.Fatalf("SeekGE(%q) did not land on it (err %v)", e.Key, it.Err())
			}
			got, ok, err := r.Get(e.Key)
			if err != nil || !ok || !itertest.Equal([]keys.Entry{got}, entries[i:i+1]) {
				t.Fatalf("Get(%q) = %v, %v, %v", e.Key, got, ok, err)
			}
			missing := append(bytes.Clone(e.Key), 0) // sorts right after e.Key
			if i+1 < len(entries) && bytes.Equal(missing, entries[i+1].Key) {
				continue
			}
			if _, ok, err := r.Get(missing); ok || err != nil {
				t.Fatalf("Get(%q) of a missing key = %v, %v", missing, ok, err)
			}
		}
		if err := it.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzSSTableOpen feeds arbitrary bytes to Open, the iterator, and Get.
// None may panic, and every failure must be ErrCorrupt. Seeds include
// valid tables, so mutations explore near-valid files.
func FuzzSSTableOpen(f *testing.F) {
	seeds := [][]keys.Entry{
		{itertest.Put("a", 1, "x")},
		{itertest.Put("a", 3, "x"), itertest.Del("b", 2), itertest.Put("c", 1, "")},
		numbered(20, 3),
	}
	for i, entries := range seeds {
		fs := newFS(f)
		write(f, fs, sstable.WriterOptions{BlockSize: 16, BloomBitsPerKey: 10 * (i % 2)}, entries)
		f.Add(vfstest.ReadFile(f, fs, tablePath))
	}
	f.Add([]byte{})
	f.Add(make([]byte, 40))
	f.Fuzz(func(t *testing.T, data []byte) {
		fs := newFS(t)
		vfstest.WriteFile(t, fs, tablePath, data)
		file, err := fs.Open(tablePath)
		if err != nil {
			t.Fatal(err)
		}
		r, err := sstable.Open(file)
		if err != nil {
			if !errors.Is(err, sstable.ErrCorrupt) {
				t.Fatalf("Open = %v, want ErrCorrupt", err)
			}
			return
		}
		defer r.Close()

		var seen [][]byte
		it := r.NewIterator()
		for it.First(); it.Valid(); it.Next() {
			e := it.Entry()
			if len(seen) > 0 && bytes.Compare(seen[len(seen)-1], e.Key) >= 0 {
				t.Fatalf("iterator keys out of order: %q then %q", seen[len(seen)-1], e.Key)
			}
			seen = append(seen, bytes.Clone(e.Key))
		}
		if err := it.Close(); err != nil && !errors.Is(err, sstable.ErrCorrupt) {
			t.Fatalf("iterator Err = %v, want ErrCorrupt", err)
		}
		for _, k := range append(seen, []byte("a"), []byte{}) {
			if _, _, err := r.Get(k); err != nil && !errors.Is(err, sstable.ErrCorrupt) {
				t.Fatalf("Get(%q) = %v, want ErrCorrupt", k, err)
			}
		}
	})
}

// FuzzDecoders feeds arbitrary bytes straight to the data block and index
// decoders, which sit behind CRCs that random input almost never passes.
// They must not panic, and must accept or reject with ErrCorrupt.
func FuzzDecoders(f *testing.F) {
	f.Add([]byte("\x01a\x01\x00\x00\x00\x00\x00\x00\x00\x01\x01x"), uint64(10))
	f.Add([]byte("\x01a\x00\x0a\x01b\x0e\x05"), uint64(23))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}, uint64(1<<63))
	f.Fuzz(func(t *testing.T, data []byte, dataEnd uint64) {
		entries, err := sstable.DecodeBlock(data, len(data)%2 == 0, []byte("m"), []byte("z"))
		if err != nil && !errors.Is(err, sstable.ErrCorrupt) {
			t.Fatalf("DecodeBlock = %v, want ErrCorrupt", err)
		}
		if err == nil && (len(entries) == 0 || string(entries[len(entries)-1].Key) != "z") {
			t.Fatalf("DecodeBlock accepted a block that does not end at its index key: %v", entries)
		}
		n, err := sstable.DecodeIndex(data, dataEnd)
		if err != nil && !errors.Is(err, sstable.ErrCorrupt) {
			t.Fatalf("DecodeIndex = %v, want ErrCorrupt", err)
		}
		if err == nil && n == 0 {
			t.Fatal("DecodeIndex accepted an empty index")
		}
	})
}
