package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/bloom"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// WriterOptions configures a Writer.
type WriterOptions struct {
	// BlockSize is the target size of a data block's contents, in bytes.
	// A block is cut before an entry that would push it past BlockSize,
	// so blocks never exceed it unless a single entry is larger on its
	// own. 0 means DefaultBlockSize.
	BlockSize int

	// BloomBitsPerKey is the Bloom filter budget. 0 means no filter.
	BloomBitsPerKey int
}

// Summary describes a finished table: what the store records about it
// in the manifest (meta.FileMeta), plus counts for experiments.
type Summary struct {
	Smallest, Largest []byte // first and last user keys; owned by the Summary
	MinSeq, MaxSeq    uint64
	Entries           uint64
	Size              int64 // bytes in the file
	Blocks            int   // data blocks
}

// Writer streams sorted entries into a new table. Only the current data
// block, the index, and one hash per key (for the filter) stay in memory.
//
// A Writer is not safe for concurrent use. After an I/O error every call
// returns that error; Finish still closes the file.
//
// Invariant: the bytes written to the file so far are exactly the
// complete data blocks recorded in index, so offset == the end of the
// last one.
type Writer struct {
	f     vfs.WritableFile
	opts  WriterOptions
	bloom *bloom.Builder // nil if no filter

	block      []byte        // contents of the open data block
	index      []blockHandle // one per flushed data block
	indexBytes int           // encoded size of index, for EstimatedSize
	offset     uint64        // bytes written to f
	sum        Summary
	lastKey    []byte // last user key added; valid when sum.Entries > 0
	err        error  // first I/O error; sticky
	done       bool   // Finish was called
}

// NewWriter returns a Writer that writes a table to f. The Writer owns f
// and closes it in Finish.
//
//	pre: opts.BlockSize >= 0 && opts.BloomBitsPerKey >= 0
func NewWriter(f vfs.WritableFile, opts WriterOptions) *Writer {
	assert.That(opts.BlockSize >= 0 && opts.BloomBitsPerKey >= 0,
		"sstable: negative option %+v", opts)
	if opts.BlockSize == 0 {
		opts.BlockSize = DefaultBlockSize
	}
	w := &Writer{f: f, opts: opts}
	if opts.BloomBitsPerKey > 0 {
		w.bloom = bloom.NewBuilder(opts.BloomBitsPerKey)
	}
	return w
}

// Add appends e to the table. It copies what it needs, so the caller may
// reuse e's slices afterwards.
//
//	pre:  Finish has not been called
//	pre:  e.Key is strictly greater than the previous key added
//	pre:  e.Validate() == nil
//	post: err != nil ⇒ the table is unusable; call Finish to close the file
func (w *Writer) Add(e keys.Entry) error {
	assert.That(!w.done, "sstable: Add after Finish")
	if assert.Enabled {
		if w.sum.Entries > 0 && bytes.Compare(w.lastKey, e.Key) >= 0 {
			assert.Fail("sstable: key %q added after %q; keys must strictly increase", e.Key, w.lastKey)
		}
		if err := e.Validate(); err != nil {
			assert.Fail("sstable: add: %v", err)
		}
	}
	if w.err != nil {
		return w.err
	}

	if len(w.block) > 0 && len(w.block)+codec.EntrySize(e) > w.opts.BlockSize {
		if err := w.flushBlock(); err != nil {
			return err
		}
	}
	w.block = codec.AppendEntry(w.block, e)

	if w.bloom != nil {
		w.bloom.Add(e.Key)
	}
	if w.sum.Entries == 0 {
		w.sum.Smallest = bytes.Clone(e.Key)
		w.sum.MinSeq, w.sum.MaxSeq = e.Seq, e.Seq
	}
	w.sum.MinSeq = min(w.sum.MinSeq, e.Seq)
	w.sum.MaxSeq = max(w.sum.MaxSeq, e.Seq)
	w.sum.Entries++
	w.lastKey = append(w.lastKey[:0], e.Key...)
	return nil
}

// EstimatedSize returns roughly how big the file would be if Finish were
// called now. Compaction uses it to cut outputs at a target size.
func (w *Writer) EstimatedSize() int64 {
	bloomBytes := 0
	if w.bloom != nil {
		bloomBytes = w.bloom.Size()
	}
	pending := 0
	if len(w.block) > 0 {
		// The open block, its CRC, and roughly its index entry.
		pending = len(w.block) + crcSize + len(w.lastKey) + 2*binary.MaxVarintLen32
	}
	return int64(w.offset) + int64(pending+bloomBytes+crcSize+w.indexBytes+crcSize+footerSize)
}

// Finish writes the last data block, the filter, the index, and the
// footer, then syncs and closes the file. It closes the file even on
// failure; the caller then removes the partial file. On success the
// caller makes the file's name durable (SyncDir).
//
//	pre:  Finish has not been called
//	post: err == nil ⇒ the file is durable and Open accepts it
//	post: no entries were added ⇒ err wraps ErrEmpty
func (w *Writer) Finish() (Summary, error) {
	assert.That(!w.done, "sstable: Finish called twice")
	w.done = true
	err := w.finish()
	if cerr := w.f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("sstable: close: %w", cerr)
	}
	if err != nil {
		return Summary{}, err
	}
	return w.sum, nil
}

func (w *Writer) finish() error {
	if w.err != nil {
		return w.err
	}
	if w.sum.Entries == 0 {
		return ErrEmpty
	}
	if err := w.flushBlock(); err != nil {
		return err
	}

	var ft footer
	ft.entries = w.sum.Entries
	var filter []byte
	if w.bloom != nil {
		filter = w.bloom.Build()
	}
	ft.bloomOff, ft.bloomLen = w.offset, uint32(len(filter))
	if err := w.writeBlock(filter); err != nil {
		return err
	}

	index := make([]byte, 0, w.indexBytes+crcSize)
	for _, h := range w.index {
		index = appendIndexEntry(index, h)
	}
	assert.That(len(index) == w.indexBytes, "sstable: index is %d bytes, counted %d", len(index), w.indexBytes)
	ft.indexOff, ft.indexLen = w.offset, uint32(len(index))
	if err := w.writeBlock(index); err != nil {
		return err
	}

	if err := w.write(ft.encode()); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		w.err = fmt.Errorf("sstable: sync: %w", err)
		return w.err
	}
	w.sum.Largest = bytes.Clone(w.lastKey)
	w.sum.Size = int64(w.offset)
	w.sum.Blocks = len(w.index)
	return nil
}

// flushBlock writes the open data block and records it in the index.
//
//	pre: len(w.block) > 0
func (w *Writer) flushBlock() error {
	assert.That(len(w.block) > 0, "sstable: flushing an empty block")
	h := blockHandle{lastKey: bytes.Clone(w.lastKey), offset: w.offset, length: uint64(len(w.block))}
	if err := w.writeBlock(w.block); err != nil {
		return err
	}
	w.index = append(w.index, h)
	w.indexBytes += indexEntrySize(h)
	w.block = w.block[:0]
	w.checkInvariant()
	return nil
}

// writeBlock writes contents followed by their CRC, in one Write. It may
// use contents' spare capacity.
func (w *Writer) writeBlock(contents []byte) error {
	return w.write(binary.LittleEndian.AppendUint32(contents, codec.Checksum(contents)))
}

func (w *Writer) write(p []byte) error {
	if _, err := w.f.Write(p); err != nil {
		w.err = fmt.Errorf("sstable: write at offset %d: %w", w.offset, err)
		return w.err
	}
	w.offset += uint64(len(p))
	return nil
}

func (w *Writer) checkInvariant() {
	if len(w.index) == 0 {
		return
	}
	if last := w.index[len(w.index)-1]; last.offset+last.length+crcSize != w.offset {
		assert.Fail("sstable: last block ends at %d, but %d bytes were written", last.offset+last.length+crcSize, w.offset)
	}
}
