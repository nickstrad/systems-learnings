package lsmtree

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/compaction"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
)

// compact runs compactions until no level needs one.
func (db *DB) compact() error {
	for {
		plan, ok := compaction.Pick(db.v, db.compactionOptions())
		if !ok {
			return nil
		}
		if err := db.runCompaction(plan); err != nil {
			return err
		}
	}
}

// runCompaction merges plan's files into new tables on the level below.
// This is one step of the paper's rolling merge: the upper input (one
// file of Li, or all of L0) is merged with the files of Li+1 it overlaps,
// keeping only the newest version of each key, and the result replaces
// both. It follows the same copy, commit, clean up order as flush.
func (db *DB) runCompaction(plan compaction.Plan) error {
	start := time.Now()
	next := db.v // compaction.Apply copies the level slices it changes
	inputs := slices.Concat(plan.Inputs, plan.Overlaps)

	// The merge reads every input in full, so the inputs' sizes and entry
	// counts are exactly what it reads. The upper input's entries are the
	// "new" entries merged down; with the output blocks they give the
	// paper's batch-merge parameter M.
	var children []iterator.Iterator
	var bytesRead, entriesIn, newEntries int64
	for i, f := range inputs {
		children = append(children, db.tables[f.Num].NewIterator())
		bytesRead += f.Size
		entriesIn += f.Entries
		if i < len(plan.Inputs) {
			newEntries += f.Entries
		}
	}

	merged := iterator.NewDedup(iterator.NewMerging(children...), false)
	out, err := db.writeTable(&next.NextFileNum, merged, plan.Bottom, db.opts.TableSize)
	if err != nil {
		return fmt.Errorf("cannot compact L%d into L%d: %w", plan.Level, plan.Level+1, err)
	}
	next = compaction.Apply(next, plan, out.files)
	if err := manifest.Write(db.fs, db.dir, next); err != nil {
		// Don't remove the outputs: if only the final directory sync
		// failed, the new manifest is already in place and names them.
		// The outcome is unknown, so leave every file; Open deletes the
		// ones the surviving manifest doesn't list.
		return fmt.Errorf("cannot record the compaction of L%d in the manifest: %w", plan.Level, err)
	}

	// Committed: switch readers, then delete the inputs.
	db.v = next
	for _, f := range out.files {
		if err := db.openTable(f.Num); err != nil {
			return err
		}
	}
	for _, f := range inputs {
		db.retireTable(f.Num)
		if err := db.fs.Remove(db.tablePath(f.Num)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("compacted, but cannot remove input table %d: %w", f.Num, err)
		}
	}
	db.checkInvariant()

	db.stats.Compactions++
	db.stats.CompactionBytesRead += bytesRead
	db.stats.CompactionBytesWritten += out.bytes
	db.emit(Event{
		Kind: EventCompaction, FromLevel: plan.Level, ToLevel: plan.Level + 1, Duration: time.Since(start),
		InputFiles: len(inputs), OutputFiles: len(out.files),
		BytesRead: bytesRead, BytesWritten: out.bytes,
		EntriesIn: entriesIn, EntriesOut: out.entries,
		TombstonesDropped: out.tombstonesDropped,
		NewEntries:        newEntries, OutputBlocks: out.blocks,
	})
	return nil
}

// retireTable closes the reader for a table that is leaving the live set,
// keeping its read counters in the totals.
func (db *DB) retireTable(num uint64) {
	r := db.tables[num]
	addReaderStats(&db.retired, r.Stats())
	r.Close()
	delete(db.tables, num)
}

func addReaderStats(dst *sstable.ReaderStats, s sstable.ReaderStats) {
	dst.BlocksRead += s.BlocksRead
	dst.BloomChecks += s.BloomChecks
	dst.BloomNegatives += s.BloomNegatives
}
