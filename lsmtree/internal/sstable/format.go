// Package sstable reads and writes sorted string tables: the immutable,
// sorted files that make up the disk components (C1 … CK) of the LSM-tree.
//
// A table is written once, front to back, by a Writer (a memtable flush or
// a compaction output), then only read. Its data is cut into blocks of
// about BlockSize bytes. A data block is this project's version of the
// paper's multi-page block: the unit of I/O that the read-amplification
// experiments count.
//
// # File format
//
// All fixed-width integers are little-endian. Every block is followed by
// the CRC32-C (Castagnoli) of its contents.
//
//	+----------------------+  offset 0
//	| data block 0         |  entry entry entry ...
//	| crc u32              |
//	+----------------------+
//	| ...                  |
//	+----------------------+
//	| data block n-1       |
//	| crc u32              |
//	+----------------------+  bloomOff
//	| bloom filter         |  bloom.Filter bytes; empty if there is no filter
//	| crc u32              |
//	+----------------------+  indexOff
//	| index block          |  one index entry per data block
//	| crc u32              |
//	+----------------------+  size - 40
//	| footer (40 bytes)    |
//	+----------------------+  size
//
// A data block entry is one entry in the shared entry encoding (package
// codec, the same bytes as a WAL record's payload):
//
//	keylen uvarint | key | seq u64 | kind u8 | vallen uvarint | value
//
// An index entry, one per data block, in block order:
//
//	keylen uvarint | last key of the block | offset uvarint | length uvarint
//
// Offset and length name the block's contents, not counting its CRC.
//
// The footer:
//
//	indexOff u64 | indexLen u32 | bloomOff u64 | bloomLen u32 |
//	entries u64 | crc u32 (of the 32 bytes before it) | magic u32
//
// Lengths again leave out the trailing CRC.
//
// Why the footer looks this way (DESIGN.md §5): it has its own CRC (otherwise a
// flipped bit in the entry count would go unnoticed), so the magic number
// shrinks to 32 bits to keep the footer at 40 bytes. The bloom block is
// always present, and empty when the table has no filter, so the regions
// tile the file with no gaps; Open checks that they do.
//
// # Contents
//
// Entries are in strictly increasing user-key order. A table holds at
// most one version of each key: a flush writes the newest version from
// the memtable and a compaction keeps only the newest. Every table has at
// least one entry and so at least one data block. Within a block, lookups
// scan from the start; blocks are small, so there are no restart points.
package sstable

import (
	"encoding/binary"
	"errors"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
)

// ErrCorrupt is wrapped by every error caused by bad bytes in a table:
// a bad magic number, CRC, offset, or encoding.
var ErrCorrupt = errors.New("sstable: corrupt table")

// ErrEmpty is returned by Writer.Finish when no entries were added. The
// store never writes an empty table.
var ErrEmpty = errors.New("sstable: table has no entries")

// DefaultBlockSize is the data block size used when WriterOptions leaves
// BlockSize at 0: 4 KiB, a typical page.
const DefaultBlockSize = 4 << 10

const (
	footerSize = 40
	crcSize    = 4
	magic      = 0x4c534d31 // "LSM1"
)

// footer locates the bloom and index blocks.
type footer struct {
	indexOff uint64
	indexLen uint32
	bloomOff uint64
	bloomLen uint32
	entries  uint64
}

func (f footer) encode() []byte {
	b := make([]byte, 0, footerSize)
	b = binary.LittleEndian.AppendUint64(b, f.indexOff)
	b = binary.LittleEndian.AppendUint32(b, f.indexLen)
	b = binary.LittleEndian.AppendUint64(b, f.bloomOff)
	b = binary.LittleEndian.AppendUint32(b, f.bloomLen)
	b = binary.LittleEndian.AppendUint64(b, f.entries)
	b = binary.LittleEndian.AppendUint32(b, codec.Checksum(b))
	return binary.LittleEndian.AppendUint32(b, magic)
}

// decodeFooter checks the magic number and the footer's CRC.
//
//	pre: len(b) == footerSize
func decodeFooter(b []byte) (footer, bool) {
	le := binary.LittleEndian
	if le.Uint32(b[36:]) != magic || le.Uint32(b[32:]) != codec.Checksum(b[:32]) {
		return footer{}, false
	}
	return footer{
		indexOff: le.Uint64(b[0:]),
		indexLen: le.Uint32(b[8:]),
		bloomOff: le.Uint64(b[12:]),
		bloomLen: le.Uint32(b[20:]),
		entries:  le.Uint64(b[24:]),
	}, true
}

// blockHandle is one index entry: where a data block lives and the last
// key in it.
type blockHandle struct {
	lastKey []byte
	offset  uint64
	length  uint64
}

// appendIndexEntry appends h's index entry to dst and returns the
// result.
func appendIndexEntry(dst []byte, h blockHandle) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(h.lastKey)))
	dst = append(dst, h.lastKey...)
	dst = binary.AppendUvarint(dst, h.offset)
	return binary.AppendUvarint(dst, h.length)
}

// indexEntrySize is the length of h's index entry.
func indexEntrySize(h blockHandle) int {
	return uvarintLen(uint64(len(h.lastKey))) + len(h.lastKey) + uvarintLen(h.offset) + uvarintLen(h.length)
}

func uvarintLen(n uint64) int {
	var buf [binary.MaxVarintLen64]byte
	return binary.PutUvarint(buf[:], n)
}

// decoder reads index fields from untrusted bytes. The first failure sticks:
// every later read returns zero values and ok() reports false, so callers
// check once at the end of a record.
type decoder struct {
	b   []byte
	bad bool
}

func (d *decoder) ok() bool { return !d.bad }

func (d *decoder) uvarint() uint64 {
	if d.bad {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.bad = true
		return 0
	}
	d.b = d.b[n:]
	return v
}

// bytes returns the next n bytes, aliasing the input.
func (d *decoder) bytes(n uint64) []byte {
	if d.bad || n > uint64(len(d.b)) {
		d.bad = true
		return nil
	}
	v := d.b[:n:n]
	d.b = d.b[n:]
	return v
}

// lenBytes reads a uvarint length, then that many bytes.
func (d *decoder) lenBytes() []byte { return d.bytes(d.uvarint()) }
