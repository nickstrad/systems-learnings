package lsmtree

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/memtable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/wal"
)

// flush writes the memtable to a new L0 table and switches to a fresh,
// empty memtable and WAL. It also runs at Open, where it is what starts
// the first WAL. The steps follow "copy, commit, clean up":
//
//  1. write the table and sync it; create the new WAL
//  2. commit: write a manifest that lists the table and names the new WAL
//  3. clean up: delete the old WAL, whose entries are now in the table
//
// A crash before step 2 leaves the old manifest, which still names the
// old WAL, so the entries are replayed again. A crash after it leaves the
// table in place. Either way nothing is lost; Open deletes leftovers.
func (db *DB) flush() error {
	start := time.Now()
	next := db.v // AddL0 copies the level slices it changes
	var written *tableOutput
	if db.mem.Len() > 0 {
		var err error
		written, err = db.writeTable(&next.NextFileNum, iterator.NewDedup(db.mem.NewIterator(), false), false, 0)
		if err != nil {
			return err
		}
		next = next.AddL0(written.files[0])
	}

	logNum := next.NextFileNum
	next.NextFileNum++
	f, err := db.fs.Create(db.logPath(logNum))
	if err != nil {
		db.removeOutputs(written)
		return fmt.Errorf("cannot create WAL %d: %w", logNum, err)
	}
	newLog := wal.NewWriter(f)
	oldLogNum := next.LogNum
	next.LogNum = logNum

	if err := manifest.Write(db.fs, db.dir, next); err != nil {
		// The outcome is unknown (see runCompaction), so keep the table
		// and both WALs; Open sorts out which ones the manifest names.
		newLog.Close()
		return fmt.Errorf("cannot record the flush in the manifest: %w", err)
	}
	// Committed. From here on, the new state is the truth.
	db.v = next
	if db.log != nil {
		db.log.Close() // its entries are in the table now; errors don't matter
	}
	db.log = newLog
	if written != nil {
		if err := db.openTable(written.files[0].Num); err != nil {
			return err
		}
	}
	memEntries := db.mem.Len()
	db.mem = memtable.New(db.v.LastSeq + 1)
	if oldLogNum != 0 {
		if err := db.fs.Remove(db.logPath(oldLogNum)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("flushed, but cannot remove the old WAL %d: %w", oldLogNum, err)
		}
	}
	db.checkInvariant()

	if written != nil {
		db.stats.Flushes++
		db.stats.FlushBytesWritten += written.bytes
		db.emit(Event{Kind: EventFlush, FromLevel: -1, ToLevel: 0, Duration: time.Since(start),
			OutputFiles: 1, BytesWritten: written.bytes,
			EntriesIn: int64(memEntries), EntriesOut: written.entries,
			NewEntries: written.entries, OutputBlocks: written.blocks})
	}
	return nil
}

// tableOutput is what writeTable wrote.
type tableOutput struct {
	files             []meta.FileMeta
	bytes             int64
	entries           int64
	blocks            int64
	tombstonesDropped int64
}

// writeTable drains it into new tables, numbered from *nextNum (which it
// advances). It starts a new table once the current one
// reaches splitAt bytes; splitAt 0 means one table. With dropTombstones,
// deletes are left out of the output. it must yield one version per user
// key in increasing order. It closes it.
//
// On error it removes any tables it wrote; nothing has referenced them.
func (db *DB) writeTable(nextNum *uint64, it iterator.Iterator, dropTombstones bool, splitAt int64) (*tableOutput, error) {
	out := &tableOutput{}
	var w *sstable.Writer
	var num uint64

	finish := func() error {
		sum, err := w.Finish() // never empty: w exists only after an Add
		w = nil
		if err != nil {
			return fmt.Errorf("cannot write table %d: %w", num, err)
		}
		out.files = append(out.files, meta.FileMeta{
			Num: num, Size: sum.Size, Smallest: sum.Smallest, Largest: sum.Largest,
			MinSeq: sum.MinSeq, MaxSeq: sum.MaxSeq, Entries: int64(sum.Entries),
		})
		out.bytes += sum.Size
		out.entries += int64(sum.Entries)
		out.blocks += int64(sum.Blocks)
		return nil
	}
	fail := func(err error) (*tableOutput, error) {
		it.Close()
		if w != nil {
			w.Finish()
			out.files = append(out.files, meta.FileMeta{Num: num})
		}
		db.removeOutputs(out)
		return nil, err
	}

	for it.First(); it.Valid(); it.Next() {
		e := it.Entry()
		if dropTombstones && e.Kind == keys.KindDelete {
			out.tombstonesDropped++
			continue
		}
		if w == nil {
			num = *nextNum
			*nextNum++
			f, err := db.fs.Create(db.tablePath(num))
			if err != nil {
				return fail(fmt.Errorf("cannot create table %d: %w", num, err))
			}
			w = sstable.NewWriter(f, sstable.WriterOptions{BlockSize: db.opts.BlockSize, BloomBitsPerKey: db.opts.BloomBitsPerKey})
		}
		if err := w.Add(e); err != nil {
			return fail(fmt.Errorf("cannot write table %d: %w", num, err))
		}
		if splitAt > 0 && w.EstimatedSize() >= splitAt {
			if err := finish(); err != nil {
				return fail(err)
			}
		}
	}
	if err := it.Close(); err != nil {
		return fail(fmt.Errorf("cannot read the merge input: %w", tableErr(err)))
	}
	if w != nil {
		if err := finish(); err != nil {
			return fail(err)
		}
	}
	return out, nil
}

// removeOutputs deletes tables that were written but never committed.
// It is best effort: Open removes anything left behind.
func (db *DB) removeOutputs(out *tableOutput) {
	if out == nil {
		return
	}
	for _, f := range out.files {
		db.fs.Remove(db.tablePath(f.Num))
	}
}

// removeObsolete deletes files the manifest does not name: tables and
// WALs left by a crash or a failed step, and a stray MANIFEST.tmp. It
// runs at Open, when nothing else can be using them.
func (db *DB) removeObsolete() error {
	names, err := db.fs.List(db.dir)
	if err != nil {
		return err
	}
	live := map[uint64]bool{db.v.LogNum: true}
	for _, level := range db.v.Levels {
		for _, f := range level {
			live[f.Num] = true
		}
	}
	removed := false
	for _, name := range names {
		num, _, ok := meta.ParseFileName(name)
		obsolete := (ok && !live[num]) || name == manifest.TmpName
		if !obsolete {
			continue
		}
		if err := db.fs.Remove(db.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot remove obsolete file %s: %w", name, err)
		}
		removed = true
	}
	if removed {
		return db.fs.SyncDir(db.dir)
	}
	return nil
}
