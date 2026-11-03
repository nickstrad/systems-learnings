package sstable_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

// corruptOpts gives a small table with several data blocks and a filter.
var corruptOpts = sstable.WriterOptions{BlockSize: 64, BloomBitsPerKey: 10}

// checkTable opens the table at tablePath, scans it, and gets every key
// in want. It returns the first error, which must wrap ErrCorrupt. Only
// a table that reads back exactly want gives nil.
func checkTable(t *testing.T, fs *vfstest.MemFS, wantKeys [][]byte) error {
	t.Helper()
	f, err := fs.Open(tablePath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := sstable.Open(f)
	if err != nil {
		return err
	}
	defer r.Close()
	it := r.NewIterator()
	n := 0
	for it.First(); it.Valid(); it.Next() {
		n++
	}
	if err := it.Close(); err != nil {
		return err
	}
	if n != len(wantKeys) {
		t.Fatalf("a damaged table read back %d entries without an error, want %d", n, len(wantKeys))
	}
	for _, k := range wantKeys {
		if _, _, err := r.Get(k); err != nil {
			return err
		}
	}
	return nil
}

// TestEveryByteFlipIsDetected flips each byte of a table in turn. Every
// byte is covered by a CRC, so each flip must surface as ErrCorrupt from
// Open, the iterator, or Get, and never as a panic or a wrong answer.
func TestEveryByteFlipIsDetected(t *testing.T) {
	fs := newFS(t)
	entries := numbered(20, 5)
	write(t, fs, corruptOpts, entries)
	good := vfstest.ReadFile(t, fs, tablePath)
	var ks [][]byte
	for _, e := range entries {
		ks = append(ks, e.Key)
	}
	for off := range good {
		bad := append([]byte(nil), good...)
		bad[off] ^= 0x01
		vfstest.WriteFile(t, fs, tablePath, bad)
		if err := checkTable(t, fs, ks); !errors.Is(err, sstable.ErrCorrupt) {
			t.Fatalf("flip at offset %d of %d: err = %v, want ErrCorrupt", off, len(good), err)
		}
	}
}

// TestFlipInEachRegion checks where each region's damage is reported:
// the filter, index, and footer at Open; a data block only when it is
// read, so the rest of the table stays readable.
func TestFlipInEachRegion(t *testing.T) {
	fs := newFS(t)
	entries := numbered(20, 5)
	sum := write(t, fs, corruptOpts, entries)
	good := vfstest.ReadFile(t, fs, tablePath)
	footer := good[len(good)-40:]
	indexOff := int(binary.LittleEndian.Uint64(footer[0:]))
	bloomOff := int(binary.LittleEndian.Uint64(footer[12:]))

	cases := []struct {
		name      string
		off       int
		atOpen    bool
		goodKey   []byte // still readable after a data block flip
		brokenKey []byte // in the damaged data block
	}{
		{"first data block", 0, false, entries[19].Key, entries[0].Key},
		{"last data block crc", bloomOff - 1, false, entries[0].Key, entries[19].Key},
		{"bloom filter", bloomOff, true, nil, nil},
		{"bloom crc", indexOff - 1, true, nil, nil},
		{"index", indexOff, true, nil, nil},
		{"index crc", len(good) - 41, true, nil, nil},
		{"footer index offset", len(good) - 40, true, nil, nil},
		{"footer entry count", len(good) - 16, true, nil, nil},
		{"footer crc", len(good) - 8, true, nil, nil},
		{"footer magic", len(good) - 1, true, nil, nil},
	}
	if sum.Blocks < 3 {
		t.Fatalf("test table has %d blocks, want several", sum.Blocks)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := append([]byte(nil), good...)
			bad[c.off] ^= 0x80
			vfstest.WriteFile(t, fs, tablePath, bad)
			f, _ := fs.Open(tablePath)
			r, err := sstable.Open(f)
			if c.atOpen {
				if !errors.Is(err, sstable.ErrCorrupt) {
					t.Fatalf("Open = %v, want ErrCorrupt", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open = %v; a data block flip should only show when the block is read", err)
			}
			defer r.Close()
			if _, ok, err := r.Get(c.goodKey); !ok || err != nil {
				t.Errorf("Get(%q) in an undamaged block = %v, %v", c.goodKey, ok, err)
			}
			if _, ok, err := r.Get(c.brokenKey); ok || !errors.Is(err, sstable.ErrCorrupt) {
				t.Errorf("Get(%q) in the damaged block = %v, %v; want ErrCorrupt", c.brokenKey, ok, err)
			}
			it := r.NewIterator()
			for it.First(); it.Valid(); it.Next() {
			}
			if !errors.Is(it.Err(), sstable.ErrCorrupt) || it.Valid() {
				t.Errorf("iterator Err = %v, Valid = %v; want ErrCorrupt and not valid", it.Err(), it.Valid())
			}
			if err := it.Close(); !errors.Is(err, sstable.ErrCorrupt) {
				t.Errorf("Close = %v, want the iterator's error", err)
			}
		})
	}
}

// TestIteratorErrorIsSticky checks that after a corrupt block, moving the
// iterator again does not hide the error.
func TestIteratorErrorIsSticky(t *testing.T) {
	fs := newFS(t)
	entries := numbered(20, 5)
	write(t, fs, corruptOpts, entries)
	bad := vfstest.ReadFile(t, fs, tablePath)
	bad[0] ^= 0xff
	vfstest.WriteFile(t, fs, tablePath, bad)
	r := open(t, fs)
	it := r.NewIterator()
	it.First()
	if it.Valid() || it.Err() == nil {
		t.Fatal("First on a corrupt first block is valid")
	}
	it.SeekGE(entries[19].Key) // an undamaged block
	if it.Valid() || !errors.Is(it.Err(), sstable.ErrCorrupt) {
		t.Fatalf("after an error, SeekGE gave Valid=%v Err=%v", it.Valid(), it.Err())
	}
}

func TestTruncatedTablesAreCorrupt(t *testing.T) {
	fs := newFS(t)
	entries := numbered(20, 5)
	write(t, fs, corruptOpts, entries)
	good := vfstest.ReadFile(t, fs, tablePath)
	for n := range len(good) {
		vfstest.WriteFile(t, fs, tablePath, good[:n])
		f, _ := fs.Open(tablePath)
		if _, err := sstable.Open(f); !errors.Is(err, sstable.ErrCorrupt) {
			t.Fatalf("Open of the first %d of %d bytes = %v, want ErrCorrupt", n, len(good), err)
		}
	}
}

func TestTrailingGarbageIsCorrupt(t *testing.T) {
	fs := newFS(t)
	write(t, fs, corruptOpts, numbered(5, 5))
	vfstest.WriteFile(t, fs, tablePath, append(vfstest.ReadFile(t, fs, tablePath), 0))
	f, _ := fs.Open(tablePath)
	if _, err := sstable.Open(f); !errors.Is(err, sstable.ErrCorrupt) {
		t.Fatalf("Open with a trailing byte = %v, want ErrCorrupt", err)
	}
}

// TestUndamagedTablePassesCheck guards checkTable itself: it must accept
// a good table, or the flip tests prove nothing.
func TestUndamagedTablePassesCheck(t *testing.T) {
	fs := newFS(t)
	entries := numbered(20, 5)
	write(t, fs, corruptOpts, entries)
	var ks [][]byte
	for _, e := range entries {
		ks = append(ks, e.Key)
	}
	if err := checkTable(t, fs, ks); err != nil {
		t.Fatalf("a good table failed the check: %v", err)
	}
}

// entryBytes encodes one data block entry with one-byte lengths.
func entryBytes(key string, seq byte, kind byte, value string) []byte {
	b := append([]byte{byte(len(key))}, key...)
	b = append(b, seq, 0, 0, 0, 0, 0, 0, 0, kind, byte(len(value)))
	return append(b, value...)
}

// TestDecodeBlockRejects covers each way a data block can be malformed
// while still passing its CRC (as a buggy writer could produce).
func TestDecodeBlockRejects(t *testing.T) {
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	good := entryBytes("b", 1, 1, "v")
	cases := []struct {
		name     string
		block    []byte
		hasLower bool
		lower    string
		last     string
	}{
		{"empty block", nil, false, "", "b"},
		{"truncated key", []byte{5, 'b'}, false, "", "b"},
		{"truncated seq", good[:5], false, "", "b"},
		{"truncated value", good[:len(good)-1], false, "", "b"},
		{"bad uvarint", []byte{0xff}, false, "", "b"},
		{"non-minimal key length", append([]byte{0x81, 0x00}, good[1:]...), false, "", "b"},
		{"empty key", entryBytes("", 1, 1, "v"), false, "", ""},
		{"invalid kind", entryBytes("b", 1, 2, ""), false, "", "b"},
		{"tombstone with a value", entryBytes("b", 1, 0, "v"), false, "", "b"},
		{"keys out of order", cat(entryBytes("c", 1, 1, ""), good), false, "", "b"},
		{"duplicate key", cat(good, good), false, "", "b"},
		{"first key not above the previous block", good, true, "b", "b"},
		{"last key differs from the index", good, false, "", "c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := sstable.DecodeBlock(c.block, c.hasLower, []byte(c.lower), []byte(c.last))
			if !errors.Is(err, sstable.ErrCorrupt) {
				t.Fatalf("DecodeBlock = %v, want ErrCorrupt", err)
			}
		})
	}
	if _, err := sstable.DecodeBlock(cat(entryBytes("a", 2, 0, ""), good), true, []byte(""), []byte("b")); err != nil {
		t.Fatalf("a good block was rejected: %v", err)
	}
}

// TestDecodeIndexRejects covers each way an index can be malformed while
// still passing its CRC.
func TestDecodeIndexRejects(t *testing.T) {
	// handle encodes an index entry for a one-letter key.
	handle := func(key string, off, length byte) []byte { return []byte{1, key[0], off, length} }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	cases := []struct {
		name    string
		index   []byte
		dataEnd uint64
	}{
		{"empty index", nil, 0},
		{"truncated entry", handle("a", 0, 6)[:3], 10},
		{"first block not at 0", handle("a", 1, 6), 11},
		{"gap between blocks", cat(handle("a", 0, 6), handle("b", 11, 6)), 21},
		{"block past the data", handle("a", 0, 20), 10},
		{"no room for the crc", handle("a", 0, 8), 10},
		{"keys out of order", cat(handle("b", 0, 6), handle("a", 10, 6)), 20},
		{"duplicate keys", cat(handle("a", 0, 6), handle("a", 10, 6)), 20},
		{"data not fully covered", handle("a", 0, 6), 11},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := sstable.DecodeIndex(c.index, c.dataEnd); !errors.Is(err, sstable.ErrCorrupt) {
				t.Fatalf("DecodeIndex = %v, want ErrCorrupt", err)
			}
		})
	}
	if n, err := sstable.DecodeIndex(cat(handle("a", 0, 6), handle("b", 10, 6)), 20); n != 2 || err != nil {
		t.Fatalf("a good index decoded to %d, %v", n, err)
	}
}
