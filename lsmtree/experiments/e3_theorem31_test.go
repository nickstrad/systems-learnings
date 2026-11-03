//go:build experiments

package experiments

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// TestE3Theorem31 tests Theorem 3.1: with the memory component S0 and the
// largest component S2 fixed, the total merge I/O of a three-component
// LSM-tree is lowest when the size ratios are equal, S1/S0 = S2/S1, that
// is at S1 = √(S0·S2).
//
// For K = 2 disk components, formula 3.7 gives the merge I/O per byte
// inserted (pages read plus written, times the page size, over R) as
//
//	H·Sp/R = (2·r1 + 2) + (2·r2 + 1),   r1 = S1/S0, r2 = S2/S1
//
// which, with r1·r2 = S2/S0 fixed, is smallest at r1 = r2.
//
// Setup: three levels with explicit targets, L0, L1 = S1, and L2, the
// last level, which never compacts. L0CompactionTrigger 1 merges each
// flush straight into L1, so S0 is one flushed memtable. The tree is
// preloaded with S2 + S1's worth of even-numbered keys in order (sequential
// keys merge without overlap, so the preload is cheap). Then the measured
// phase inserts odd-numbered keys in random order: new entries spread
// over the whole key range, all of which flow down to L2, as the proof
// assumes. A warm-up phase first brings L1 to its steady size.
//
// Measured: (compaction bytes read + written − the L0 bytes the merges
// re-read) / bytes flushed. Subtracting the L0 re-read leaves exactly the
// terms of formula 3.7, which keeps C0 in memory and never reads it.
// Predicted: formula 3.7 with S0 = the average flushed table, S1 = L1's
// target, and S2 = L2's average size during the measured phase.
//
// Where our design differs from the paper's: a leveled L1→L2 compaction
// takes one whole file of L1 and also rewrites the partial L2 files at
// both ends of its range, so each step costs about two extra L2 tables;
// L1 hovers just under its target rather than at it; and L2 grows by the
// inserted data during the phase (about 25%) instead of being held fixed
// by deletes, so its size during the phase is reported, not assumed.
// These shift the curve but should not move its minimum far.
func TestE3Theorem31(t *testing.T) {
	t.Parallel()
	const (
		memtable  = 16 << 10
		tableSize = 16 << 10
		preloadN  = 70_000 // about 4 MiB of tables: S2
		warmupN   = 8_000
		measureN  = 17_000 // about 25% of S2
	)
	// Table bytes per entry, to turn S1 into a number of entries: the
	// entry inside its data block, plus about one byte of index, filter,
	// and block trailer.
	tableBytesPerEntry := entrySize() + 1

	tb := &experiment.Table{Name: "e3_theorem31", Header: []string{
		"s1_target", "s0_bytes", "s2_bytes", "geo_mean_s1", "s1_over_geo_mean", "r1", "r2",
		"flush_bytes", "compaction_bytes_read", "compaction_bytes_written",
		"measured_merge_io", "predicted_merge_io", "measured_write_amp", "predicted_write_amp",
	}}
	type point struct {
		row         []any
		s1, geo, io float64
	}
	s1s := []int64{32 << 10, 64 << 10, 128 << 10, 256 << 10, 512 << 10, 1 << 20, 2 << 20}
	name := func(s1 int64) string { return fmt.Sprintf("s1=%dKiB", s1>>10) }
	points, ok := experiment.Sweep(t, s1s, name, func(t *testing.T, s1 int64) point {
		log := newEventLog("e3", name(s1))
		db := openDB(t, log, lsmtree.Options{
			MemtableSize:        memtable,
			L0CompactionTrigger: 1,
			LevelTargets:        []int64{0, s1, 1 << 40},
			TableSize:           tableSize,
		})
		// Preload S2 plus what L1 will hold, so that L2 starts at about
		// the same size whatever S1 is.
		n := preloadN + int(s1)/tableBytesPerEntry
		db.put(t, seq(0, 2, n))
		// The inserts must spread over the whole preloaded range, or the
		// merges would miss the part of L1 the preload left there.
		inserts := shuffled(seq(1, 2, n), 3)
		warmup, measured := inserts[:warmupN], inserts[warmupN:warmupN+measureN]
		log.phase = "warmup"
		db.put(t, warmup)

		log.phase = "measure"
		before := db.Stats()
		var l2Sum float64
		var l2Samples int
		for i, id := range measured {
			db.put(t, []int{id})
			if i%1000 == 0 {
				l2Sum += float64(db.Stats().Levels[2].Bytes)
				l2Samples++
			}
		}
		d := db.Stats().Sub(before)
		db.close(t)

		flushed := float64(d.FlushBytesWritten)
		read := float64(d.CompactionBytesRead)
		written := float64(d.CompactionBytesWritten)
		s0 := flushed / float64(d.Flushes)
		s2 := l2Sum / float64(l2Samples)
		r1, r2 := float64(s1)/s0, s2/float64(s1)
		geo := math.Sqrt(s0 * s2)
		// With L0CompactionTrigger 1 every flushed table is read back
		// once, by the L0→L1 merge that follows it at once.
		measuredIO := (read + written - flushed) / flushed
		predictedIO := (2*r1 + 2) + (2*r2 + 1)
		if r := measuredIO / predictedIO; r < 0.75 || r > 1.25 {
			t.Errorf("merge I/O %.3g is %.2f× formula 3.7's %.3g; want within 25%%", measuredIO, r, predictedIO)
		}
		return point{
			row: []any{s1, s0, s2, geo, float64(s1) / geo, r1, r2,
				int64(flushed), int64(read), int64(written),
				measuredIO, predictedIO, written / flushed, (r1 + 1) + (r2 + 1)},
			s1: float64(s1), geo: geo, io: measuredIO,
		}
	})
	if !ok {
		return
	}
	for _, p := range points {
		tb.Add(p.row...)
	}
	tb.Report(t)

	// The expected shape: a U. The minimum is within a factor of 2 of the
	// geometric mean (one step of the sweep either side), and both ends
	// cost clearly more than the minimum.
	best := 0
	for i, p := range points {
		if p.io < points[best].io {
			best = i
		}
	}
	b := points[best]
	if ratio := b.s1 / b.geo; ratio < 0.5 || ratio > 2 {
		t.Errorf("merge I/O is lowest at S1 = %.0f, %.2f× the geometric mean %.0f; want within 2×", b.s1, ratio, b.geo)
	}
	for _, end := range []point{points[0], points[len(points)-1]} {
		if end.io < 1.5*b.io {
			t.Errorf("merge I/O at S1 = %.0f is %.3g, not clearly above the minimum %.3g: no U shape", end.s1, end.io, b.io)
		}
	}
}
