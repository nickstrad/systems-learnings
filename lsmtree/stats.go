package lsmtree

import (
	"bytes"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/compaction"
)

// Stats are counters since Open, plus the current shape of the tree.
type Stats struct {
	Levels []LevelStats // Levels[0] is L0

	// L0Trigger is the L0 file count that triggers an L0 compaction: the
	// L0CompactionTrigger in effect, after defaults. L0's LevelStats has
	// no byte Target; this is its target instead.
	L0Trigger int

	MemtableBytes   int64
	MemtableEntries int

	Writes           int64 // Put and Delete calls that succeeded
	UserBytesWritten int64 // key plus value bytes of those writes

	Flushes                int64
	FlushBytesWritten      int64 // table bytes written by flushes
	Compactions            int64
	CompactionBytesRead    int64 // table bytes read by compactions
	CompactionBytesWritten int64 // table bytes written by compactions
	WALBytesWritten        int64 // bytes appended to the current WAL

	Gets           int64
	TablesProbed   int64  // tables a Get consulted (one Bloom check each, if filters are on)
	BlocksRead     uint64 // data blocks read by Gets, Scans, and compactions
	BloomChecks    uint64
	BloomNegatives uint64 // Bloom checks that ruled a table out without reading it
}

// LevelStats describes one level.
type LevelStats struct {
	Files  int
	Bytes  int64
	Target int64 // 0 for L0, which is sized by file count

	// Tables lists the level's files: L0 oldest first, deeper levels in
	// key order. Cursor is the level's rolling-merge cursor: the next
	// compaction of this level starts after it (nil: from the start).
	Tables []TableInfo
	Cursor []byte

	// Next is the index in Tables of the table the next compaction of
	// this level takes, by the same rule the planner uses. It is -1 for
	// L0 (whose compaction takes every table), for an empty level, and
	// for the last level, which never compacts.
	Next int
}

// TableInfo describes one SSTable.
type TableInfo struct {
	Num               uint64
	Size              int64
	Entries           int64
	Smallest, Largest []byte
}

// WriteAmplification is table bytes written (flushes plus compactions)
// divided by user bytes written. It leaves out the WAL, which every
// design pays once. 0 if nothing was written.
func (s Stats) WriteAmplification() float64 {
	if s.UserBytesWritten == 0 {
		return 0
	}
	return float64(s.FlushBytesWritten+s.CompactionBytesWritten) / float64(s.UserBytesWritten)
}

// Sub returns the work done between before and s: each counter minus
// before's. The gauges (Levels, the memtable, L0Trigger) stay s's.
func (s Stats) Sub(before Stats) Stats {
	s.Writes -= before.Writes
	s.UserBytesWritten -= before.UserBytesWritten
	s.Flushes -= before.Flushes
	s.FlushBytesWritten -= before.FlushBytesWritten
	s.Compactions -= before.Compactions
	s.CompactionBytesRead -= before.CompactionBytesRead
	s.CompactionBytesWritten -= before.CompactionBytesWritten
	s.WALBytesWritten -= before.WALBytesWritten
	s.Gets -= before.Gets
	s.TablesProbed -= before.TablesProbed
	s.BlocksRead -= before.BlocksRead
	s.BloomChecks -= before.BloomChecks
	s.BloomNegatives -= before.BloomNegatives
	return s
}

// Stats returns the counters and the shape of the tree.
func (db *DB) Stats() Stats {
	db.mu.Lock()
	defer db.mu.Unlock()
	s := db.stats
	s.L0Trigger = db.opts.L0CompactionTrigger
	s.Levels = make([]LevelStats, len(db.v.Levels))
	for i, files := range db.v.Levels {
		l := LevelStats{Files: len(files), Bytes: db.v.LevelSize(i), Target: db.opts.LevelTargets[i], Next: -1}
		for _, f := range files {
			l.Tables = append(l.Tables, TableInfo{Num: f.Num, Size: f.Size, Entries: f.Entries,
				Smallest: bytes.Clone(f.Smallest), Largest: bytes.Clone(f.Largest)})
		}
		if i < len(db.v.Cursors) {
			l.Cursor = bytes.Clone(db.v.Cursors[i])
		}
		if i == 0 {
			l.Target = 0 // L0 is sized by file count, not bytes
		} else if i < len(db.v.Levels)-1 && len(files) > 0 {
			l.Next = compaction.NextInput(files, l.Cursor)
		}
		s.Levels[i] = l
	}
	s.MemtableBytes = db.mem.Size()
	s.MemtableEntries = db.mem.Len()
	if db.log != nil {
		s.WALBytesWritten = db.log.Size()
	}
	rs := db.retired
	for _, r := range db.tables {
		addReaderStats(&rs, r.Stats())
	}
	s.BlocksRead, s.BloomChecks, s.BloomNegatives = rs.BlocksRead, rs.BloomChecks, rs.BloomNegatives
	return s
}
