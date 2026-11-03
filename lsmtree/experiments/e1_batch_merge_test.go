//go:build experiments

package experiments

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// TestE1BatchMerge measures the batch-merge parameter M (Definition
// 3.2.1): the average number of C0 entries merged into each C1 page
// during a merge. Formula 3.2 predicts
//
//	M = (Sp/Se) · (S0/(S0+S1))
//
// where Sp/Se is entries per page and S0/(S0+S1) is the share of the
// tree's entries that sit in C0. The cost of an insert is then about 2/M
// page I/Os (formula 3.3): every C1 page is read and written once per
// merge, and that cost is shared by the M new entries it receives.
//
// Setup: a two-component tree. L1 is the last level, so it never
// compacts, and it is preloaded with a fixed n1 entries. Then n0 new
// entries go into the memtable and one Flush writes them out. Our design
// has an L0 between the memtable and L1 (DESIGN.md §1); with
// L0CompactionTrigger 1 every flushed L0 file is merged into L1 at once,
// so a flush followed by its L0→L1 compaction is the paper's C0→C1 merge
// with one extra sequential write and read of the C0 data. Keys are
// random, so the L0 file spans the whole key range and the merge reads
// and rewrites all of L1, as one full pass of the paper's rolling merge
// cursor does.
//
// Measured M is the L0→L1 compaction's new_entries / output_blocks.
// Predicted M uses the nominal Sp/Se = BlockSize / EntrySize. They differ
// only because real blocks are cut before the entry that would overflow
// them (so a block holds floor(Sp/Se) entries, not Sp/Se) and because the
// last block of each output table is partly empty.
func TestE1BatchMerge(t *testing.T) {
	t.Parallel()
	const n1 = 20_000 // entries in L1, fixed
	ids := shuffled(seq(0, 1, 2*n1), 1)
	preload, fresh := ids[:n1], ids[n1:]
	entriesPerPage := float64(blockSize) / float64(entrySize())

	tb := &experiment.Table{Name: "e1_batch_merge", Header: []string{
		"s1_over_s0", "n0", "n1", "s0_bytes", "s1_bytes", "entries_per_block",
		"measured_m", "predicted_m", "m_ratio",
		"measured_block_io_per_insert", "predicted_block_io_per_insert",
	}}
	type result struct {
		row      []any
		measured float64 // M
	}
	divs := []int{64, 32, 16, 8, 4, 2, 1}
	rows, ok := experiment.Sweep(t, divs, func(div int) string { return fmt.Sprintf("s1_s0=%d", div) },
		func(t *testing.T, div int) result {
			n0 := n1 / div
			log := newEventLog("e1", fmt.Sprintf("s1/s0=%d", div))
			db := openDB(t, log, lsmtree.Options{
				MemtableSize:        1 << 30, // flushes only when asked: C0 holds exactly n0 entries
				L0CompactionTrigger: 1,
				LevelTargets:        []int64{0, 1 << 40}, // L0 and L1; L1 is the last level
			})
			db.put(t, preload)
			db.compact(t)

			log.phase = "measure"
			before := db.Stats()
			db.put(t, fresh[:n0])
			db.compact(t)
			d := db.Stats().Sub(before)
			db.close(t)

			events := log.in("measure")
			flushes, merges := filterLevel(events, -1), filterLevel(events, 0)
			if len(flushes) != 1 || len(merges) != 1 {
				t.Fatalf("got %d flushes and %d L0→L1 compactions, want 1 of each", len(flushes), len(merges))
			}
			flush, m := flushes[0], merges[0]
			s0Bytes := d.FlushBytesWritten
			s1Bytes := m.BytesRead - s0Bytes
			measured := float64(m.NewEntries) / float64(m.OutputBlocks)
			predicted := entriesPerPage * float64(n0) / float64(n0+n1)
			// Block I/O per insert: the L1 blocks read and the blocks
			// written, over the new entries. Formula 3.3 counts the same in
			// pages: 2/M. The merge also reads back the L0 table's blocks,
			// which the paper's C0, kept in memory, never costs, so
			// subtract them.
			l1BlocksRead := d.BlocksRead - uint64(flush.OutputBlocks)
			ioPerInsert := float64(l1BlocksRead+uint64(m.OutputBlocks)) / float64(n0)

			if r := measured / predicted; math.Abs(r-1) > 0.10 {
				t.Errorf("measured M %.3g is %.2f× formula 3.2's %.3g; want within 10%%", measured, r, predicted)
			}
			return result{
				row: []any{div, n0, n1, s0Bytes, s1Bytes, entriesPerPage,
					measured, predicted, measured / predicted, ioPerInsert, 2 / predicted},
				measured: measured,
			}
		})
	if !ok {
		return
	}
	for _, r := range rows {
		tb.Add(r.row...)
	}
	tb.Report(t)

	for i := 1; i < len(rows); i++ {
		if got, prev := rows[i].measured, rows[i-1].measured; got <= prev {
			t.Errorf("s1/s0=%d: measured M %.3g did not grow with S0 (previous %.3g)", divs[i], got, prev)
		}
	}
}

// filterLevel returns the flushes (from -1) or the compactions out of
// level from.
func filterLevel(events []lsmtree.Event, from int) []lsmtree.Event {
	var out []lsmtree.Event
	for _, e := range events {
		if (e.Kind == lsmtree.EventFlush || e.Kind == lsmtree.EventCompaction) && e.FromLevel == from {
			out = append(out, e)
		}
	}
	return out
}
