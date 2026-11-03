package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// ErrFailed is returned by every Writer method after an earlier Write or
// Sync failed. The log may then end in a partial record, and anything
// appended after it would be dropped by Replay, so the writer refuses to
// go on. The caller must start a new log (after flushing the memtable).
var ErrFailed = errors.New("wal: an earlier write or sync failed; this log takes no more records")

// Writer appends records to one log file. It is not safe for concurrent
// use; the store serializes writes.
//
// Invariant: size is the number of bytes successfully written to f, and
// those bytes are whole records. (Append writes a record with one Write
// and adds its length only on success; nothing here can check the file,
// so there is no checkInvariant.)
type Writer struct {
	f      vfs.WritableFile
	buf    []byte // reused encoding buffer
	size   int64
	err    error // first Write or Sync error; sticky
	closed bool
}

// NewWriter returns a Writer that appends to f.
//
//	pre: f is empty (a fresh file from vfs.FS.Create)
func NewWriter(f vfs.WritableFile) *Writer {
	assert.That(f != nil, "wal: NewWriter with nil file")
	return &Writer{f: f}
}

// Append writes one record for e with a single Write call, so a record is
// never interleaved with another. The record is not durable until Sync.
//
// The store validates entries at its public API, so a bad entry here is a
// bug and trips an assertion.
//
//	pre:  e.Validate() == nil
//	post: err == nil ⇒ Size() grew by the record's length
//	post: err != nil ⇒ every later call returns an error wrapping ErrFailed
func (w *Writer) Append(e keys.Entry) error {
	if assert.Enabled {
		if err := e.Validate(); err != nil {
			assert.Fail("wal: append seq %d: %v", e.Seq, err)
		}
	}
	if err := w.usable(); err != nil {
		return err
	}
	// Frame the entry: header placeholder, payload, then len and crc.
	var header [headerSize]byte
	rec := codec.AppendEntry(append(w.buf[:0], header[:]...), e)
	binary.LittleEndian.PutUint32(rec[4:8], uint32(len(rec)-headerSize))
	binary.LittleEndian.PutUint32(rec[0:4], codec.Checksum(rec[4:]))
	w.buf = rec
	if _, err := w.f.Write(rec); err != nil {
		w.err = err
		return fmt.Errorf("wal: append seq %d: %w", e.Seq, err)
	}
	w.size += int64(len(rec))
	return nil
}

// Sync makes every record appended so far durable.
//
//	post: err == nil ⇒ the records survive a crash (once the file's
//	      directory entry is durable too; see vfs.FS.SyncDir)
//	post: err != nil ⇒ every later call returns an error wrapping ErrFailed.
//	      After a failed fsync the kernel may have dropped the dirty pages,
//	      so retrying could report success for lost data.
func (w *Writer) Sync() error {
	if err := w.usable(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		w.err = err
		return fmt.Errorf("wal: sync: %w", err)
	}
	return nil
}

// Close closes the file. It does not sync: call Sync first if the records
// must be durable. After Close, Append and Sync fail with an error
// wrapping fs.ErrClosed.
func (w *Writer) Close() error {
	if w.closed {
		return fmt.Errorf("wal: close: %w", fs.ErrClosed)
	}
	w.closed = true
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("wal: close: %w", err)
	}
	return nil
}

// Size returns the number of bytes appended so far, for stats.
func (w *Writer) Size() int64 { return w.size }

// usable returns an error if the writer is closed or has failed.
func (w *Writer) usable() error {
	if w.closed {
		return fmt.Errorf("wal: %w", fs.ErrClosed)
	}
	if w.err != nil {
		return fmt.Errorf("%w: %w", ErrFailed, w.err)
	}
	return nil
}
