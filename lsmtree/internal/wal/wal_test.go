package wal_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/wal"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

func TestRoundTrip(t *testing.T) {
	maxKey := strings.Repeat("k", keys.MaxKeySize)
	maxVal := strings.Repeat("v", keys.MaxValueSize)
	cases := []struct {
		name string
		es   []keys.Entry
	}{
		{"zero records", nil},
		{"one record", []keys.Entry{itertest.Put("a", 1, "1")}},
		{"many records", manyEntries(500)},
		{"empty value", []keys.Entry{itertest.Put("a", 7, "")}},
		{"one-byte key", []keys.Entry{itertest.Put("k", 7, "v")}},
		{"tombstone", []keys.Entry{itertest.Put("a", 1, "1"), itertest.Del("a", 2)}},
		{"max-size key and value", []keys.Entry{itertest.Put(maxKey, 1, maxVal)}},
		{"max sequence number", []keys.Entry{itertest.Put("a", ^uint64(0), "b")}},
		{"binary bytes", []keys.Entry{itertest.Put("\x00\xff\n", 3, "\x00\x00\x80")}},
		{"same key, many versions", []keys.Entry{itertest.Put("k", 1, "a"), itertest.Put("k", 2, "b"), itertest.Del("k", 3), itertest.Put("k", 4, "c")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, ends := encode(t, tc.es)
			got, rep := replayBytes(t, data)
			if len(got) != len(tc.es) {
				t.Fatalf("replayed %d entries, want %d", len(got), len(tc.es))
			}
			checkPrefix(t, got, tc.es)
			want := wal.Report{Records: len(tc.es), GoodBytes: int64(len(data))}
			if rep != want {
				t.Fatalf("report %+v, want %+v", rep, want)
			}
			if len(ends) > 0 && ends[len(ends)-1] != int64(len(data)) {
				t.Fatalf("Size() = %d, file has %d bytes", ends[len(ends)-1], len(data))
			}
		})
	}
}

// The record layout is the one in DESIGN.md §5, byte for byte.
func TestRecordLayout(t *testing.T) {
	data, _ := encode(t, []keys.Entry{itertest.Put("ab", 0x0102030405060708, "xyz")})
	want := []byte{
		0, 0, 0, 0, // crc, checked below
		16, 0, 0, 0, // len: 1 + 2 + 8 + 1 + 1 + 3
		2, 'a', 'b', // keylen, key
		8, 7, 6, 5, 4, 3, 2, 1, // seq, little-endian
		1,                // kind: put
		3, 'x', 'y', 'z', // vallen, val
	}
	if !bytes.Equal(data[4:], want[4:]) {
		t.Fatalf("record\n got %v\nwant %v", data, want)
	}
	if got := binary.LittleEndian.Uint32(data); got != crc32c(data[4:]) {
		t.Fatalf("crc %#x, want CRC32-C of len and payload %#x", got, crc32c(data[4:]))
	}
	// Flipping any CRC byte makes the record unreadable, so the CRC covers it.
	for i := range 4 {
		bad := bytes.Clone(data)
		bad[i] ^= 0xff
		if got, _ := replayBytes(t, bad); len(got) != 0 {
			t.Fatalf("flipped crc byte %d still replayed", i)
		}
	}
}

// Truncating the file at every byte offset leaves exactly the records that
// end at or before the cut, and the rest is reported as dropped.
func TestTornTailAtEveryOffset(t *testing.T) {
	es := []keys.Entry{itertest.Put("a", 1, "1"), itertest.Del("b", 2), itertest.Put("c", 3, "three")}
	data, ends := encode(t, es)
	for cut := 0; cut <= len(data); cut++ {
		whole, good := 0, int64(0)
		for _, end := range ends {
			if end <= int64(cut) {
				whole, good = whole+1, end
			}
		}
		got, rep := replayBytes(t, data[:cut])
		if len(got) != whole {
			t.Fatalf("cut %d: replayed %d entries, want %d", cut, len(got), whole)
		}
		checkPrefix(t, got, es)
		want := wal.Report{Records: whole, GoodBytes: good, DroppedBytes: int64(cut) - good}
		if rep != want {
			t.Fatalf("cut %d: report %+v, want %+v", cut, rep, want)
		}
	}
}

// A damaged record in the middle looks like a torn tail: it and every
// record after it are dropped, even though the later ones are intact.
func TestFlippedByteDropsRecordAndRest(t *testing.T) {
	es := []keys.Entry{itertest.Put("a", 1, "1"), itertest.Put("b", 2, "2"), itertest.Put("c", 3, "3")}
	data, ends := encode(t, es)
	for i := ends[0]; i < ends[1]; i++ {
		for _, mask := range []byte{0x01, 0x80, 0xff} {
			bad := bytes.Clone(data)
			bad[i] ^= mask
			got, rep := replayBytes(t, bad)
			checkPrefix(t, got, es)
			want := wal.Report{Records: 1, GoodBytes: ends[0], DroppedBytes: int64(len(data)) - ends[0]}
			if rep != want {
				t.Fatalf("byte %d ^ %#x: report %+v, want %+v", i, mask, rep, want)
			}
		}
	}
}

// Garbage that cannot be a record: impossible lengths and payloads that
// pass the CRC but do not decode. Each is dropped whole.
func TestReplayRejectsInvalidRecords(t *testing.T) {
	good, _ := encode(t, []keys.Entry{itertest.Put("a", 1, "1")})
	cases := []struct {
		name    string
		payload []byte // CRC fixed up by record()
		rawLen  uint32 // used instead of len(payload) when nonzero
	}{
		{"length zero", nil, 0},
		{"length below minimum", make([]byte, 11), 0},
		{"length over maximum", nil, 0xffffffff},
		{"length past end of file", make([]byte, 12), 1000},
		{"invalid kind", payload([]byte{1, 'k'}, 1, 2, []byte{0}), 0},
		{"tombstone with value", payload([]byte{1, 'k'}, 1, 0, []byte{1, 'v'}), 0},
		{"empty key", payload([]byte{0}, 1, 1, []byte{1, 'v'}), 0},
		{"malformed key varint", payload([]byte{0x80, 0x80}, 1, 1, []byte{0}), 0},
		{"non-minimal key varint", payload([]byte{0x81, 0x00, 'k'}, 1, 1, []byte{0}), 0},
		{"key length over limit", payload([]byte{0x81, 0x20}, 1, 1, []byte{0}), 0}, // 4097
		{"key length past payload", payload([]byte{100, 'a'}, 1, 1, []byte{0}), 0},
		{"value length over limit", payload([]byte{1, 'k'}, 1, 1, []byte{0x81, 0x80, 0x40}), 0}, // 1<<20 + 1
		{"value length past payload", payload([]byte{1, 'k'}, 1, 1, []byte{3, 'a'}), 0},
		{"non-minimal value varint", payload([]byte{1, 'k'}, 1, 1, []byte{0x80, 0x00}), 0},
		{"trailing bytes", payload([]byte{1, 'k'}, 1, 1, []byte{0, 'x'}), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := record(tc.payload, tc.rawLen)
			data := append(bytes.Clone(good), bad...)
			got, rep := replayBytes(t, data)
			want := wal.Report{Records: 1, GoodBytes: int64(len(good)), DroppedBytes: int64(len(bad))}
			if len(got) != 1 || rep != want {
				t.Fatalf("replayed %d entries, report %+v; want 1 entry, %+v", len(got), rep, want)
			}
		})
	}
}

// payload builds a record payload from raw parts: the key field as given
// (length varint included), seq, kind, then the value field as given.
func payload(keyField []byte, seq uint64, kind byte, valField []byte) []byte {
	p := append([]byte{}, keyField...)
	p = binary.LittleEndian.AppendUint64(p, seq)
	p = append(p, kind)
	return append(p, valField...)
}

// record frames p with a len field (rawLen if nonzero) and a correct CRC.
func record(p []byte, rawLen uint32) []byte {
	n := uint32(len(p))
	if rawLen != 0 {
		n = rawLen
	}
	rec := []byte{0, 0, 0, 0, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	rec = append(rec, p...)
	c := crc32c(rec[4:])
	rec[0], rec[1], rec[2], rec[3] = byte(c), byte(c>>8), byte(c>>16), byte(c>>24)
	return rec
}

func TestWriterSizeCountsAppendedBytes(t *testing.T) {
	f, err := vfstest.NewMemFS().Create(logName)
	if err != nil {
		t.Fatal(err)
	}
	w := wal.NewWriter(f)
	if w.Size() != 0 {
		t.Fatalf("new writer Size() = %d", w.Size())
	}
	if err := w.Append(itertest.Put("ab", 1, "xyz")); err != nil {
		t.Fatal(err)
	}
	if w.Size() != 24 { // 8-byte header + 16-byte payload (see TestRecordLayout)
		t.Fatalf("Size() = %d, want 24", w.Size())
	}
}

func TestWriterAfterClose(t *testing.T) {
	f, err := vfstest.NewMemFS().Create(logName)
	if err != nil {
		t.Fatal(err)
	}
	w := wal.NewWriter(f)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"append": w.Append(itertest.Put("a", 1, "b")),
		"sync":   w.Sync(),
		"close":  w.Close(),
	} {
		if !errors.Is(err, fs.ErrClosed) {
			t.Errorf("%s after close: got %v, want fs.ErrClosed", name, err)
		}
	}
}

// The writer's preconditions are assertions: the store validates entries
// at its public API, so a bad one reaching the log is a bug.
func TestAppendAssertsPreconditions(t *testing.T) {
	cases := []struct {
		name string
		e    keys.Entry
		want string
	}{
		{"empty key", itertest.Put("", 1, "v"), "empty key"},
		{"key one over the limit", itertest.Put(strings.Repeat("k", keys.MaxKeySize+1), 1, "v"), "key of 4097 bytes"},
		{"value one over the limit", itertest.Put("k", 1, strings.Repeat("v", keys.MaxValueSize+1)), "value of 1048577 bytes"},
		{"invalid kind", keys.Entry{Key: []byte("k"), Kind: 7}, "invalid kind 7"},
		{"tombstone with a value", keys.Entry{Key: []byte("k"), Kind: keys.KindDelete, Value: []byte("v")}, "tombstone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := vfstest.NewMemFS().Create(logName)
			if err != nil {
				t.Fatal(err)
			}
			w := wal.NewWriter(f)
			asserttest.Panics(t, tc.want, func() { _ = w.Append(tc.e) })
		})
	}
}
