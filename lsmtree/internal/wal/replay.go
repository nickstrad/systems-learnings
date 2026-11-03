package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Report says what Replay read, so the caller can log how much it
// discarded.
//
// Invariant: after a Replay that returns no error,
// GoodBytes + DroppedBytes is the file's size.
type Report struct {
	Records      int   // whole, valid records passed to fn
	GoodBytes    int64 // length of the prefix made of those records
	DroppedBytes int64 // bytes after it, discarded as a torn tail
}

// Replay reads the records of f in order and calls fn with each entry.
// It reads with ReadAt from offset 0, so f's read offset does not matter.
//
// fn's entry aliases a buffer that Replay reuses: fn must Clone it to keep
// it past the call.
//
// Replay stops at the first record that is short, fails its CRC, has an
// impossible length, or does not decode (see codec.DecodeEntry). That
// is a torn tail, not an error: the rest of the file is counted in
// DroppedBytes. See the package comment for why a bad record in the
// middle is treated the same way.
//
//	post: err == nil ⇒ fn saw the longest prefix of whole valid records,
//	      and GoodBytes + DroppedBytes == f's size
//	post: err != nil ⇒ it is an I/O error or fn's error (returned as is);
//	      the report covers the records fn accepted before it
func Replay(f vfs.ReadableFile, fn func(keys.Entry) error) (Report, error) {
	var rep Report
	size, err := f.Size()
	if err != nil {
		return rep, fmt.Errorf("wal: replay: size: %w", err)
	}
	r := bufio.NewReader(io.NewSectionReader(f, 0, size))
	var rec []byte // reused for every record
	for {
		var ok bool
		rec, ok, err = readRecord(r, rec, size-rep.GoodBytes)
		if err != nil {
			return rep, fmt.Errorf("wal: replay at offset %d: %w", rep.GoodBytes, err)
		}
		if !ok {
			break
		}
		e, rest, ok := codec.DecodeEntry(rec[headerSize:])
		if !ok || len(rest) != 0 {
			break
		}
		if err := fn(e); err != nil {
			return rep, err
		}
		rep.Records++
		rep.GoodBytes += int64(len(rec))
	}
	rep.DroppedBytes = size - rep.GoodBytes
	assert.That(rep.DroppedBytes >= 0, "wal: read %d bytes of a %d-byte file", rep.GoodBytes, size)
	return rep, nil
}

// readRecord reads the next record (header and payload) from r, which has
// left bytes remaining, into buf's memory. ok is false if the record is
// torn: short, of an impossible length, or failing its CRC. err is an I/O
// error other than the file ending.
func readRecord(r io.Reader, buf []byte, left int64) (rec []byte, ok bool, err error) {
	rec = slices.Grow(buf[:0], headerSize)[:headerSize]
	if ok, err = readFull(r, rec); !ok {
		return rec, false, err
	}
	n := binary.LittleEndian.Uint32(rec[4:8])
	if n < codec.MinEntrySize || n > codec.MaxEntrySize || int64(n) > left-headerSize {
		return rec, false, nil // impossible, or longer than what is left of the file
	}
	rec = slices.Grow(rec, int(n))[:headerSize+int(n)]
	if ok, err = readFull(r, rec[headerSize:]); !ok {
		return rec, false, err
	}
	return rec, codec.Checksum(rec[4:]) == binary.LittleEndian.Uint32(rec[0:4]), nil
}

// readFull fills p from r. ok is false if the file ended first (a torn
// tail, err == nil) or the read failed (err != nil).
func readFull(r io.Reader, p []byte) (ok bool, err error) {
	_, err = io.ReadFull(r, p)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil
	}
	return err == nil, err
}
