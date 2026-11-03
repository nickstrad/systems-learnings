package codec_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// encode builds an entry's bytes field by field, independently of
// AppendEntry, so tests can also build malformed ones.
func encode(keyLen []byte, key string, seq uint64, kind byte, valLen []byte, val string) []byte {
	var b []byte
	b = append(b, keyLen...)
	b = append(b, key...)
	b = binary.LittleEndian.AppendUint64(b, seq)
	b = append(b, kind)
	b = append(b, valLen...)
	return append(b, val...)
}

func uv(n int) []byte { return binary.AppendUvarint(nil, uint64(n)) }

func TestAppendEntryLayout(t *testing.T) {
	got := codec.AppendEntry([]byte("prefix"), itertest.Put("key", 0x0102030405060708, "value"))
	want := append([]byte("prefix"), encode(uv(3), "key", 0x0102030405060708, 1, uv(5), "value")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("AppendEntry = %x, want %x", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		e    keys.Entry
	}{
		{"one-byte key, empty value", itertest.Put("k", 1, "")},
		{"tombstone", itertest.Del("gone", 7)},
		{"seq zero", itertest.Put("k", 0, "v")},
		{"max seq", itertest.Put("k", ^uint64(0), "v")},
		{"127-byte key: last one-byte uvarint", itertest.Put(string(bytes.Repeat([]byte{'k'}, 127)), 1, "v")},
		{"128-byte key: first two-byte uvarint", itertest.Put(string(bytes.Repeat([]byte{'k'}, 128)), 1, "v")},
		{"max key", itertest.Put(string(bytes.Repeat([]byte{'k'}, keys.MaxKeySize)), 1, "v")},
		{"max value", itertest.Put("k", 1, string(bytes.Repeat([]byte{'v'}, keys.MaxValueSize)))},
		{"binary bytes", keys.Entry{Key: []byte{0, 0xff}, Seq: 3, Kind: keys.KindPut, Value: []byte{0x80, 0}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := codec.AppendEntry(nil, c.e)
			if len(b) != codec.EntrySize(c.e) {
				t.Fatalf("encoded %d bytes, EntrySize says %d", len(b), codec.EntrySize(c.e))
			}
			if len(b) < codec.MinEntrySize || len(b) > codec.MaxEntrySize {
				t.Fatalf("encoded %d bytes, outside [%d, %d]", len(b), codec.MinEntrySize, codec.MaxEntrySize)
			}
			trailer := []byte("next")
			got, rest, ok := codec.DecodeEntry(append(b, trailer...))
			if !ok {
				t.Fatal("DecodeEntry rejected a valid encoding")
			}
			if !itertest.Equal([]keys.Entry{got}, []keys.Entry{c.e}) {
				t.Fatalf("decoded %+v, want %+v", got, c.e)
			}
			if !bytes.Equal(rest, trailer) {
				t.Fatalf("rest = %q, want %q", rest, trailer)
			}
		})
	}
}

func TestDecodeAliasesInputWithCappedSlices(t *testing.T) {
	b := codec.AppendEntry(nil, itertest.Put("k", 1, "v"))
	b = append(b, "tail"...)
	e, _, ok := codec.DecodeEntry(b)
	if !ok {
		t.Fatal("rejected")
	}
	if cap(e.Key) != len(e.Key) || cap(e.Value) != len(e.Value) {
		t.Fatalf("Key cap %d, Value cap %d: appends could overwrite the input", cap(e.Key), cap(e.Value))
	}
	_ = append(e.Value, 'X')
	if !bytes.HasSuffix(b, []byte("tail")) {
		t.Fatal("append to Value overwrote the bytes after it")
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	good := encode(uv(1), "k", 1, 1, uv(1), "v")
	key := func(n int) string { return string(bytes.Repeat([]byte{'k'}, n)) }
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty input", nil},
		{"truncated key length", []byte{0x80}},
		{"truncated key", encode(uv(5), "ab", 0, 0, nil, "")[:3]},
		{"truncated seq", good[:5]},
		{"missing kind", good[:10]},
		{"missing value length", good[:11]},
		{"truncated value", good[:len(good)-1]},
		{"value length past the end", encode(uv(1), "k", 1, 1, uv(9), "v")},
		{"overlong uvarint (11 bytes)", append(bytes.Repeat([]byte{0xff}, 10), 1)},
		{"non-minimal key length", encode([]byte{0x81, 0x00}, "k", 1, 1, uv(1), "v")},
		{"non-minimal value length", encode(uv(1), "k", 1, 1, []byte{0x80, 0x00}, "")},
		{"empty key", encode(uv(0), "", 1, 1, uv(1), "v")},
		{"key over the limit", encode(uv(keys.MaxKeySize+1), key(keys.MaxKeySize+1), 1, 1, uv(0), "")},
		{"value length over the limit", encode(uv(1), "k", 1, 1, uv(keys.MaxValueSize+1), "")},
		{"invalid kind", encode(uv(1), "k", 1, 2, uv(0), "")},
		{"tombstone with a value", encode(uv(1), "k", 1, 0, uv(1), "v")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if e, rest, ok := codec.DecodeEntry(c.b); ok {
				t.Fatalf("DecodeEntry(%x) = %+v, rest %x; want rejection", c.b, e, rest)
			}
		})
	}
}

func TestDecodeAcceptsLimits(t *testing.T) {
	big := string(bytes.Repeat([]byte{'k'}, keys.MaxKeySize))
	b := encode(uv(keys.MaxKeySize), big, 1, 0, uv(0), "")
	if _, _, ok := codec.DecodeEntry(b); !ok {
		t.Fatal("rejected a tombstone with a key at the limit")
	}
}

func TestChecksumIsCRC32C(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("a"), []byte("123456789")} {
		want := crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
		if got := codec.Checksum(b); got != want {
			t.Fatalf("Checksum(%q) = %#x, want %#x", b, got, want)
		}
	}
	// The standard CRC-32C check value.
	if got := codec.Checksum([]byte("123456789")); got != 0xe3069283 {
		t.Fatalf("Checksum(123456789) = %#x, want 0xe3069283", got)
	}
}
