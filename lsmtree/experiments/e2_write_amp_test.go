//go:build experiments

package experiments

import (
	"fmt"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/countfs"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// TestE2WriteAmp measures write amplification, the price of batching
// (§3.3): table bytes written per user byte, per level and in total, as
// the size ratio r between levels varies.
//
// The paper's merge-cost formula (3.7) says each byte that reaches a
// full component Ci is written (r_i + 1) times there: the merge from
// Ci-1 rewrites r_i bytes of Ci for every byte it brings in. So a larger
// r costs more per level, but needs fewer levels to hold the same data
// (about log_r of it). The "model" column below is that per-level term:
// 1 for the flush into L0, 1 + S1/(L0 batch) for L1, and 1 + r for each
// deeper level, times the table format's overhead per user byte (what
// the flush writes per user byte). It is a steady-state ceiling: levels
// hover between half full and full, and in a load from empty the
// deepest level is never full, so measured costs are lower.
//
// The paper's total, H ∝ K·(1+r) with K = log_r(SK/S0) levels (formula
// 3.6), is lowest at a small r (near e), not at the largest: fewer
// levels do not make up for the higher cost of each.
//
// Note the L0 batch is in table bytes: the memtable's size accounting
// includes its own overhead, so a 32 KiB memtable flushes a table of
// about 11 KiB.
//
// The same load with sequential keys is the contrast: each new file's key
// range lies above everything already on disk, so a merge overlaps
// nothing below it and every level copies each byte about once. (Our
// compaction always rewrites; it has no "trivial move" of a file to the
// next level, so the copy is not free.)
func TestE2WriteAmp(t *testing.T) {
	t.Parallel()
	const (
		n         = 100_000
		memtable  = 32 << 10
		l0Trigger = 4
		baseLevel = 128 << 10
	)
	sequential := seq(0, 1, n)
	patterns := []struct {
		name string
		ids  []int
	}{{"random", shuffled(sequential, 2)}, {"sequential", sequential}}
	ratios := []int{2, 4, 10, 25}

	perLevel := &experiment.Table{Name: "e2_write_amp", Header: []string{
		"pattern", "r", "level", "files", "level_bytes", "level_target",
		"compactions_into", "bytes_written", "write_amp", "model_write_amp",
	}}
	totals := &experiment.Table{Name: "e2_totals", Header: []string{
		"pattern", "r", "levels_used", "user_bytes", "table_bytes_written",
		"table_write_amp", "wal_write_amp", "manifest_write_amp", "disk_write_amp", "model_write_amp",
	}}

	type config struct {
		pattern string
		ids     []int
		r       int
	}
	type result struct {
		levelRows [][]any
		totalsRow []any
		levels    int
		total     float64
		maxDeep   float64 // the largest per-level amp below L1
	}
	var configs []config
	for _, p := range patterns {
		for _, r := range ratios {
			configs = append(configs, config{p.name, p.ids, r})
		}
	}
	name := func(c config) string { return fmt.Sprintf("%s r=%d", c.pattern, c.r) }
	results, ok := experiment.Sweep(t, configs, name, func(t *testing.T, c config) result {
		pattern, r := c.pattern, c.r
		log := newEventLog("e2", name(c))
		db := openDB(t, log, lsmtree.Options{
			MemtableSize:        memtable,
			L0CompactionTrigger: l0Trigger,
			BaseLevelSize:       baseLevel,
			LevelSizeRatio:      r,
			MaxLevels:           levelsFor(baseLevel, r, 64<<20),
			TableSize:           32 << 10,
		})
		db.put(t, c.ids)
		db.compact(t)
		s := db.Stats()
		// countfs sees every byte the store writes, by file kind: a check
		// on the store's own counters, and the WAL's share.
		counts, _ := db.fs.Snapshot()
		db.close(t)

		written := make([]int64, len(s.Levels))
		into := make([]int, len(s.Levels))
		for _, e := range log.in("load") {
			if e.Kind == lsmtree.EventFlush || e.Kind == lsmtree.EventCompaction {
				written[e.ToLevel] += e.BytesWritten
				into[e.ToLevel]++
			}
		}

		// The model counts copies of the data; one copy in table form
		// costs `overhead` table bytes per user byte (entry headers,
		// block checksums, index, filter), as the flush shows.
		overhead := float64(s.FlushBytesWritten) / float64(s.UserBytesWritten)
		l0Batch := float64(l0Trigger) * float64(s.FlushBytesWritten) / float64(s.Flushes)

		var res result
		var modelTotal float64
		for lvl, l := range s.Levels {
			if l.Bytes == 0 && written[lvl] == 0 {
				continue
			}
			res.levels = lvl
			amp := float64(written[lvl]) / float64(s.UserBytesWritten)
			model := overhead // the flush into L0
			switch {
			case lvl == 1:
				model *= 1 + float64(baseLevel)/l0Batch
			case lvl >= 2:
				model *= 1 + float64(r)
			}
			modelTotal += model
			if lvl >= 2 {
				res.maxDeep = max(res.maxDeep, amp)
			}
			res.levelRows = append(res.levelRows,
				[]any{pattern, r, lvl, l.Files, l.Bytes, l.Target, into[lvl], written[lvl], amp, model})
		}
		res.total = s.WriteAmplification()

		tableBytes := s.FlushBytesWritten + s.CompactionBytesWritten
		if got := counts[countfs.KindTable].BytesWritten; got != tableBytes {
			t.Errorf("countfs saw %d table bytes written, Stats says %d", got, tableBytes)
		}
		var disk int64
		for _, c := range counts {
			disk += c.BytesWritten
		}
		user := float64(s.UserBytesWritten)
		res.totalsRow = []any{pattern, r, res.levels, s.UserBytesWritten, tableBytes, res.total,
			float64(counts[countfs.KindWAL].BytesWritten) / user, float64(counts[countfs.KindManifest].BytesWritten) / user,
			float64(disk) / user, modelTotal}
		return res
	})
	if !ok {
		return
	}
	got := map[string]map[int]result{}
	for i, res := range results {
		c := configs[i]
		for _, row := range res.levelRows {
			perLevel.Add(row...)
		}
		totals.Add(res.totalsRow...)
		if got[c.pattern] == nil {
			got[c.pattern] = map[int]result{}
		}
		got[c.pattern][c.r] = res
	}
	perLevel.Report(t)
	totals.Report(t)

	// The expected shape, loosely.
	rnd, sq := got["random"], got["sequential"]
	for i, r := range ratios {
		if rnd[r].total <= 2 {
			t.Errorf("random r=%d: write amp %.3g; want > 2 (flush plus at least one merge)", r, rnd[r].total)
		}
		if sq[r].total >= rnd[r].total {
			t.Errorf("r=%d: sequential write amp %.3g is not below random's %.3g", r, sq[r].total, rnd[r].total)
		}
		// Sequential keys: about one copy per level. Allow 25% for the
		// L0 files that do overlap the newest L1 file.
		if limit := 1.25 * float64(sq[r].levels+1); sq[r].total > limit {
			t.Errorf("sequential r=%d: write amp %.3g over %d levels; want <= %.3g (one copy per level)",
				r, sq[r].total, sq[r].levels+1, limit)
		}
		if i == 0 {
			continue
		}
		prev := ratios[i-1]
		if rnd[r].levels > rnd[prev].levels {
			t.Errorf("random: r=%d uses %d levels, more than r=%d's %d", r, rnd[r].levels, prev, rnd[prev].levels)
		}
		if rnd[r].maxDeep <= rnd[prev].maxDeep {
			t.Errorf("random: the costliest level below L1 writes %.3g×user bytes at r=%d, not more than %.3g at r=%d",
				rnd[r].maxDeep, r, rnd[prev].maxDeep, prev)
		}
	}
}

// levelsFor returns a MaxLevels (L0 included) whose deepest level's
// target reaches capacity bytes, so the last level, which never compacts,
// is not forced to absorb data the load would have spread out. (A fixed
// large MaxLevels would overflow int64 targets at large r.)
func levelsFor(base int64, r int, capacity int64) int {
	levels := 2
	for target := base; target < capacity; target *= int64(r) {
		levels++
	}
	return levels
}
