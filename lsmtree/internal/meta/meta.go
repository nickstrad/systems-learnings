// Package meta describes the store's shape: which SSTables are live, on
// which level, and the counters that name new files and writes. The
// manifest persists a Version; compaction plans over one; the DB reads
// through one. It also owns the store's file names, so every package
// that names or classifies a file agrees on them.
package meta

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nickstrad/systems-learnings/shared/assert"
)

// FileMeta describes one SSTable.
type FileMeta struct {
	Num      uint64 // file number: the file is TableName(Num)
	Size     int64  // bytes on disk
	Smallest []byte // smallest user key in the file
	Largest  []byte // largest user key in the file
	MinSeq   uint64
	MaxSeq   uint64
	Entries  int64 // entries, tombstones included
}

// Overlaps reports whether the file's key range meets [smallest, largest].
func (f FileMeta) Overlaps(smallest, largest []byte) bool {
	return bytes.Compare(f.Smallest, largest) <= 0 && bytes.Compare(smallest, f.Largest) <= 0
}

// Version is one consistent view of the live files.
//
// Invariant (Validate, asserted by CheckInvariant):
//   - every file has Smallest <= Largest and MinSeq <= MaxSeq
//   - L0 files are ordered by file number, oldest first; their ranges may overlap
//   - in L1 and below, files are sorted by Smallest and no two ranges overlap
//   - every file number is below NextFileNum, and LogNum is too
//   - every file's MaxSeq is at most LastSeq
//   - no file number appears twice
//   - len(Cursors) <= len(Levels): a cursor belongs to a level that exists
type Version struct {
	Levels      [][]FileMeta // Levels[0] is L0
	NextFileNum uint64       // the next number to hand out for a .sst or .log file
	LastSeq     uint64       // the highest sequence number in any table or the WAL
	LogNum      uint64       // the live WAL: LogName(LogNum)

	// Cursors[i] is the rolling-merge cursor of level i: the largest key
	// of the last file compacted out of it. A nil entry, or a missing one
	// (Cursors may be shorter than Levels), means "start from the
	// smallest key". L0 never has a cursor: an L0 compaction takes every
	// L0 file. The cursor is part of the Version, so the manifest
	// persists it with the file set it was computed against; the paper's
	// checkpoint likewise records where each rolling merge stands (§4.2).
	Cursors [][]byte
}

// Clone returns a deep enough copy that changing the copy's level and
// cursor slices leaves v alone. FileMeta values and cursor keys are never
// mutated in place, so their byte slices are shared.
func (v Version) Clone() Version {
	c := v
	c.Levels = make([][]FileMeta, len(v.Levels))
	for i, l := range v.Levels {
		c.Levels[i] = slices.Clone(l)
	}
	c.Cursors = slices.Clone(v.Cursors)
	return c
}

// AddL0 returns v with f added to L0 as its newest file: the result of a
// memtable flush. It never changes v.
//
//	pre:  f.Num is above every L0 file number and below v.NextFileNum;
//	      f.MaxSeq <= v.LastSeq
//	post: the result satisfies the Version invariant (asserted)
func (v Version) AddL0(f FileMeta) Version {
	n := v.Clone()
	if len(n.Levels) == 0 {
		n.Levels = append(n.Levels, nil)
	}
	n.Levels[0] = append(n.Levels[0], f)
	n.CheckInvariant()
	return n
}

// LevelSize returns the total bytes of the files on level.
func (v Version) LevelSize(level int) int64 {
	var n int64
	for _, f := range v.Levels[level] {
		n += f.Size
	}
	return n
}

// Validate returns an error describing the first way v breaks the
// Version invariant, or nil. The manifest decoder uses it on untrusted
// files (a bad Version on disk is corrupt input); CheckInvariant asserts
// it on Versions the store built (a bad Version in memory is a bug).
// Beyond the invariant listed on Version, it rejects negative sizes and
// entry counts, which would break level scoring.
func (v Version) Validate() error {
	if v.NextFileNum != 0 && v.LogNum >= v.NextFileNum {
		return fmt.Errorf("log number %d is not below next file number %d", v.LogNum, v.NextFileNum)
	}
	if len(v.Cursors) > len(v.Levels) {
		return fmt.Errorf("%d cursors for %d levels", len(v.Cursors), len(v.Levels))
	}
	seen := map[uint64]bool{}
	for level, files := range v.Levels {
		for i, f := range files {
			switch {
			case seen[f.Num]:
				return fmt.Errorf("file %d appears twice", f.Num)
			case bytes.Compare(f.Smallest, f.Largest) > 0:
				return fmt.Errorf("L%d file %d has smallest key %q above largest %q", level, f.Num, f.Smallest, f.Largest)
			case f.MinSeq > f.MaxSeq:
				return fmt.Errorf("L%d file %d has minSeq %d > maxSeq %d", level, f.Num, f.MinSeq, f.MaxSeq)
			case f.Num >= v.NextFileNum:
				return fmt.Errorf("file %d is not below next file number %d", f.Num, v.NextFileNum)
			case f.MaxSeq > v.LastSeq:
				return fmt.Errorf("file %d has maxSeq %d above last seq %d", f.Num, f.MaxSeq, v.LastSeq)
			case f.Size < 0 || f.Entries < 0:
				return fmt.Errorf("file %d has negative size %d or entry count %d", f.Num, f.Size, f.Entries)
			}
			seen[f.Num] = true
			if i == 0 {
				continue
			}
			prev := files[i-1]
			if level == 0 && prev.Num >= f.Num {
				return fmt.Errorf("L0 files %d and %d are out of order", prev.Num, f.Num)
			}
			if level > 0 && bytes.Compare(prev.Largest, f.Smallest) >= 0 {
				return fmt.Errorf("L%d files %d [%q..%q] and %d [%q..%q] overlap or are out of order",
					level, prev.Num, prev.Smallest, prev.Largest, f.Num, f.Smallest, f.Largest)
			}
		}
	}
	return nil
}

// CheckInvariant asserts the Version invariant. It walks every file, so it
// does nothing when assertions are compiled out.
func (v Version) CheckInvariant() {
	if !assert.Enabled {
		return
	}
	err := v.Validate()
	assert.That(err == nil, "version: %v", err)
}

// FileKind says what a numbered file in the store's directory holds.
type FileKind int

// The kinds of numbered file. The manifest is not numbered; see package
// manifest for its names.
const (
	KindTable FileKind = iota + 1 // an SSTable: TableName(num)
	KindLog                       // a write-ahead log: LogName(num)
)

const (
	tableExt = ".sst"
	logExt   = ".log"
)

// TableName returns the file name of SSTable num, e.g. "000012.sst".
func TableName(num uint64) string { return fmt.Sprintf("%06d%s", num, tableExt) }

// LogName returns the file name of WAL num, e.g. "000012.log".
func LogName(num uint64) string { return fmt.Sprintf("%06d%s", num, logExt) }

// ParseFileName is the inverse of TableName and LogName: it splits
// "000012.sst" into 12 and KindTable. name is a base name, not a path.
// Any number of decimal digits is accepted, so a number too large for
// six digits still parses.
//
//	post: ok ⇒ kind is KindTable or KindLog
//	post: !ok ⇒ name is not a table or log name (num and kind are zero)
func ParseFileName(name string) (num uint64, kind FileKind, ok bool) {
	ext := filepath.Ext(name)
	switch ext {
	case tableExt:
		kind = KindTable
	case logExt:
		kind = KindLog
	default:
		return 0, 0, false
	}
	num, err := strconv.ParseUint(strings.TrimSuffix(name, ext), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return num, kind, true
}
