// Package codec is the one byte encoding of a keys.Entry, shared by the
// write-ahead log's record payloads and the SSTable's data block entries,
// plus the CRC both formats use.
//
// # Entry format
//
//	keylen uvarint | key | seq u64 | kind u8 | vallen uvarint | value
//
// seq is little-endian. Every valid entry has exactly one encoding:
// DecodeEntry rejects a uvarint written in a longer form than needed
// (such as 0x80 0x00 for zero), so re-encoding a decoded entry gives back
// the same bytes.
package codec

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// Bounds on the size of one encoded valid entry.
const (
	// MinEntrySize is a one-byte key, seq, kind, and an empty value.
	MinEntrySize = 1 + 1 + 8 + 1 + 1

	// MaxEntrySize is a maximal key and value with their uvarint lengths.
	// It is loose by a few bytes (varints are at most MaxVarintLen64
	// long), which is fine for its use: capping how much a corrupt length
	// can make a reader allocate.
	MaxEntrySize = 2*binary.MaxVarintLen64 + keys.MaxKeySize + 8 + 1 + keys.MaxValueSize
)

// castagnoli is the CRC32-C table, built once. CRC32-C has hardware
// support on most CPUs.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Checksum returns the CRC32-C (Castagnoli) of b.
func Checksum(b []byte) uint32 { return crc32.Checksum(b, castagnoli) }

// EntrySize returns the length of e's encoding.
//
//	post: EntrySize(e) == len(AppendEntry(nil, e))
func EntrySize(e keys.Entry) int {
	return uvarintLen(len(e.Key)) + len(e.Key) + 8 + 1 + uvarintLen(len(e.Value)) + len(e.Value)
}

// AppendEntry appends the encoding of e to dst and returns the result.
// It does not check e; callers assert e.Validate() first.
//
//	post: DecodeEntry of the appended bytes gives back e when e is valid
func AppendEntry(dst []byte, e keys.Entry) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(e.Key)))
	dst = append(dst, e.Key...)
	dst = binary.LittleEndian.AppendUint64(dst, e.Seq)
	dst = append(dst, byte(e.Kind))
	dst = binary.AppendUvarint(dst, uint64(len(e.Value)))
	return append(dst, e.Value...)
}

// DecodeEntry decodes one entry from the front of b and returns it with
// the bytes after it. The entry's Key and Value alias b, capped so an
// append to one cannot overwrite what follows.
//
// ok is false if b does not start with a well-formed encoding: a
// truncated field, a malformed or non-minimal uvarint, a length over the
// keys limits, or an entry that fails keys.Entry.Validate. It never
// panics.
//
//	post: ok ⇒ e.Validate() == nil and AppendEntry(nil, e) is the prefix
//	      of b before rest
func DecodeEntry(b []byte) (e keys.Entry, rest []byte, ok bool) {
	if e.Key, b, ok = lenBytes(b, keys.MaxKeySize); !ok || len(b) < 9 {
		return keys.Entry{}, nil, false
	}
	e.Seq = binary.LittleEndian.Uint64(b)
	e.Kind = keys.Kind(b[8])
	if e.Value, b, ok = lenBytes(b[9:], keys.MaxValueSize); !ok {
		return keys.Entry{}, nil, false
	}
	if e.Validate() != nil {
		return keys.Entry{}, nil, false
	}
	return e, b, true
}

// lenBytes reads a minimal uvarint length n <= limit, then n bytes. It
// returns those bytes (capacity capped) and what follows them.
func lenBytes(b []byte, limit int) (v, rest []byte, ok bool) {
	n, w := binary.Uvarint(b)
	if w <= 0 || w != uvarintLen64(n) || n > uint64(limit) || n > uint64(len(b)-w) {
		return nil, nil, false
	}
	b = b[w:]
	return b[:n:n], b[n:], true
}

func uvarintLen(n int) int { return uvarintLen64(uint64(n)) }

// uvarintLen64 returns the length of the minimal uvarint encoding of n.
func uvarintLen64(n uint64) int {
	w := 1
	for n >= 0x80 {
		n >>= 7
		w++
	}
	return w
}
