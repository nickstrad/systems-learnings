package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync/atomic"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/bloom"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// ReaderStats counts a Reader's work, for the read-amplification
// experiments. Only data block reads count as BlocksRead: Open loads the
// filter and index once and keeps them in memory.
type ReaderStats struct {
	BlocksRead     uint64 // data blocks read by Get and iterators
	BloomChecks    uint64 // Gets that consulted the filter
	BloomNegatives uint64 // of those, Gets the filter answered without a block read
}

// Reader reads one table. Open keeps the footer, filter, and index in
// memory; Get and iterators read data blocks on demand.
//
// Get and NewIterator are safe for concurrent use: the file is only read
// with ReadAt and the counters are atomic. Each iterator is single-user.
//
// Invariant: index is non-empty; its blocks tile [0, dataEnd) in order,
// and their last keys strictly increase.
type Reader struct {
	f      vfs.ReadableFile
	filter bloom.Filter // nil if the table has none
	index  []blockHandle

	blocksRead, bloomChecks, bloomNegatives atomic.Uint64
}

// Open reads and checks the footer, filter, and index of the table in f.
// The Reader owns f and closes it in Close; if Open fails it closes f.
// Data blocks are checked when they are read.
//
//	post: bad bytes ⇒ err wraps ErrCorrupt; Open never panics on them
func Open(f vfs.ReadableFile) (*Reader, error) {
	r, err := open(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func open(f vfs.ReadableFile) (*Reader, error) {
	size, err := f.Size()
	if err != nil {
		return nil, fmt.Errorf("sstable: size: %w", err)
	}
	if size < footerSize {
		return nil, fmt.Errorf("%w: file is %d bytes, shorter than the %d-byte footer", ErrCorrupt, size, footerSize)
	}
	footerOff := uint64(size - footerSize)
	buf, err := readAt(f, nil, footerOff, footerSize)
	if err != nil {
		return nil, err
	}
	ft, ok := decodeFooter(buf)
	if !ok {
		return nil, fmt.Errorf("%w: bad footer magic or checksum", ErrCorrupt)
	}
	// The regions must tile the file: data, bloom, index, footer. Each
	// comparison is checked before the next sum, so nothing overflows.
	if ft.bloomOff > ft.indexOff || ft.indexOff > footerOff ||
		ft.bloomOff+uint64(ft.bloomLen)+crcSize != ft.indexOff ||
		ft.indexOff+uint64(ft.indexLen)+crcSize != footerOff {
		return nil, fmt.Errorf("%w: footer regions (bloom %d+%d, index %d+%d) do not tile a %d-byte file",
			ErrCorrupt, ft.bloomOff, ft.bloomLen, ft.indexOff, ft.indexLen, size)
	}
	if ft.entries == 0 {
		return nil, fmt.Errorf("%w: footer says the table has no entries", ErrCorrupt)
	}

	filter, err := readBlock(f, nil, ft.bloomOff, uint64(ft.bloomLen), "bloom filter")
	if err != nil {
		return nil, err
	}
	indexBytes, err := readBlock(f, nil, ft.indexOff, uint64(ft.indexLen), "index")
	if err != nil {
		return nil, err
	}
	index, err := decodeIndex(indexBytes, ft.bloomOff)
	if err != nil {
		return nil, err
	}

	r := &Reader{f: f, index: index}
	if len(filter) > 0 {
		r.filter = filter
	}
	return r, nil
}

// decodeIndex parses and checks the index: at least one block, blocks
// contiguous from offset 0 to dataEnd, and last keys strictly increasing.
func decodeIndex(b []byte, dataEnd uint64) ([]blockHandle, error) {
	var index []blockHandle
	d := decoder{b: b}
	var next uint64 // where the next block must start
	for len(d.b) > 0 {
		h := blockHandle{lastKey: d.lenBytes(), offset: d.uvarint(), length: d.uvarint()}
		switch {
		case !d.ok():
			return nil, fmt.Errorf("%w: index entry %d is truncated", ErrCorrupt, len(index))
		case h.offset != next || h.length > dataEnd-next || dataEnd-next-h.length < crcSize:
			return nil, fmt.Errorf("%w: index entry %d (offset %d, length %d) is out of place",
				ErrCorrupt, len(index), h.offset, h.length)
		case len(index) > 0 && bytes.Compare(index[len(index)-1].lastKey, h.lastKey) >= 0:
			return nil, fmt.Errorf("%w: index keys out of order at entry %d", ErrCorrupt, len(index))
		}
		next = h.offset + h.length + crcSize
		index = append(index, h)
	}
	if len(index) == 0 || next != dataEnd {
		return nil, fmt.Errorf("%w: index covers %d of %d data bytes", ErrCorrupt, next, dataEnd)
	}
	return index, nil
}

// Get returns the entry for key, which may be a tombstone. It checks the
// filter first, then reads at most one data block. The returned entry
// owns its memory.
//
//	post: found ⇒ bytes.Equal(e.Key, key)
//	post: err != nil ⇒ !found; bad bytes ⇒ err wraps ErrCorrupt
func (r *Reader) Get(key []byte) (e keys.Entry, found bool, err error) {
	if r.filter != nil {
		r.bloomChecks.Add(1)
		if !r.filter.MayContain(key) {
			r.bloomNegatives.Add(1)
			return keys.Entry{}, false, nil
		}
	}
	i := r.search(key)
	if i == len(r.index) {
		return keys.Entry{}, false, nil // after the last key
	}
	entries, _, err := r.readDataBlock(i, nil, nil)
	if err != nil {
		return keys.Entry{}, false, err
	}
	j := sort.Search(len(entries), func(j int) bool { return bytes.Compare(entries[j].Key, key) >= 0 })
	if j < len(entries) && bytes.Equal(entries[j].Key, key) {
		return entries[j], true, nil
	}
	return keys.Entry{}, false, nil
}

// NewIterator returns an unpositioned iterator over the whole table. It
// holds one decoded data block at a time.
func (r *Reader) NewIterator() iterator.Iterator {
	return &tableIter{r: r, block: -1}
}

// Stats returns a snapshot of the Reader's counters.
func (r *Reader) Stats() ReaderStats {
	return ReaderStats{
		BlocksRead:     r.blocksRead.Load(),
		BloomChecks:    r.bloomChecks.Load(),
		BloomNegatives: r.bloomNegatives.Load(),
	}
}

// Close closes the file. Iterators must not be used afterwards.
func (r *Reader) Close() error { return r.f.Close() }

// search returns the first block whose last key is >= key: the only block
// that can hold key. It returns len(r.index) if key is after every key.
func (r *Reader) search(key []byte) int {
	return sort.Search(len(r.index), func(i int) bool {
		return bytes.Compare(r.index[i].lastKey, key) >= 0
	})
}

// readDataBlock reads, checks, and decodes data block i. It reads into
// buf's memory and appends the entries to entries[:0], growing either as
// needed, and returns both so an iterator can reuse them for its next
// block. The entries alias buf. With nil buf and entries the result is
// fresh memory the caller owns.
func (r *Reader) readDataBlock(i int, buf []byte, entries []keys.Entry) ([]keys.Entry, []byte, error) {
	h := r.index[i]
	r.blocksRead.Add(1)
	b, err := readBlock(r.f, buf, h.offset, h.length, "data block")
	if err != nil {
		return entries, buf, err
	}
	var lower []byte // the previous block's last key; this block's keys are above it
	if i > 0 {
		lower = r.index[i-1].lastKey
	}
	entries, err = decodeBlock(entries[:0], b, i > 0, lower, h.lastKey)
	if err != nil {
		return entries, b, fmt.Errorf("%w (data block %d at offset %d)", err, i, h.offset)
	}
	return entries, b, nil
}

// decodeBlock parses a data block's entries, appending them to dst, and
// checks that they are well formed (codec.DecodeEntry), non-empty,
// strictly increasing, above lower (if hasLower), and end at exactly
// last, the key the index promised.
func decodeBlock(dst []keys.Entry, b []byte, hasLower bool, lower, last []byte) ([]keys.Entry, error) {
	entries := dst
	for n := 0; len(b) > 0; n++ {
		e, rest, ok := codec.DecodeEntry(b)
		switch {
		case !ok:
			return entries, fmt.Errorf("%w: entry %d is malformed", ErrCorrupt, n)
		case n > 0 && bytes.Compare(entries[len(entries)-1].Key, e.Key) >= 0,
			n == 0 && hasLower && bytes.Compare(lower, e.Key) >= 0:
			return entries, fmt.Errorf("%w: entry %d is out of order", ErrCorrupt, n)
		}
		entries = append(entries, e)
		b = rest
	}
	if len(entries) == len(dst) || !bytes.Equal(entries[len(entries)-1].Key, last) {
		return entries, fmt.Errorf("%w: block does not end at its index key", ErrCorrupt)
	}
	return entries, nil
}

// readBlock reads length bytes at off plus the CRC after them, into
// buf's memory (see readAt), and returns the contents if the CRC matches.
// Their capacity also covers the CRC, so buf can be reused for a block of
// the same size.
//
//	pre: off+length+crcSize is inside the file (Open checked the layout)
func readBlock(f io.ReaderAt, buf []byte, off, length uint64, what string) ([]byte, error) {
	buf, err := readAt(f, buf, off, length+crcSize)
	if err != nil {
		return nil, err
	}
	contents, crc := buf[:length], binary.LittleEndian.Uint32(buf[length:])
	if codec.Checksum(contents) != crc {
		return nil, fmt.Errorf("%w: %s at offset %d fails its checksum", ErrCorrupt, what, off)
	}
	return contents, nil
}

// readAt reads exactly n bytes at off into buf's memory, growing it if it
// is too small (nil buf allocates). A short read means the file is
// shorter than its own layout says, so it is reported as corruption.
func readAt(f io.ReaderAt, buf []byte, off, n uint64) ([]byte, error) {
	buf = slices.Grow(buf[:0], int(n))[:n]
	got, err := f.ReadAt(buf, int64(off))
	if got == len(buf) {
		return buf, nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: short read of %d bytes at offset %d (got %d)", ErrCorrupt, n, off, got)
	}
	return nil, fmt.Errorf("sstable: read at offset %d: %w", off, err)
}
