//go:build experiments

package experiments

import (
	"fmt"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/bloom"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// TestE4ReadAmp measures what finds cost in an LSM-tree (abstract, §3.4):
// a point lookup may have to look in every component. It counts tables
// probed and data blocks read per Get, for keys that are present and keys
// that are absent, with Bloom filters off and at 5, 10, and 20 bits per
// key.
//
// Without filters, an absent key costs one block read in every table
// whose key range covers it: about one per level plus the L0 files. A
// filter rules a table out without reading it, except for a false
// positive, which happens with probability about
//
//	p = (1 − e^(−k/b))^k,   b bits per key, k = round(b·ln 2) probes
//
// (bloom.FalsePositiveRate), so absent-key blocks per Get should fall to
// about p × tables probed.
// A present key costs one block for the table that holds it, plus false
// positives in the tables above it.
//
// The data: n random keys with even IDs, so absent keys (odd IDs) fall
// between them, inside every table's key range, the worst case for a
// lookup that has only the ranges to go on.
func TestE4ReadAmp(t *testing.T) {
	t.Parallel()
	const (
		n       = 60_000
		lookups = 20_000
	)
	present := shuffled(seq(0, 2, n), 4)
	absent := shuffled(seq(1, 2, n), 5)[:lookups]

	tb := &experiment.Table{Name: "e4_read_amp", Header: []string{
		"bloom_bits_per_key", "probes_k", "keys", "tables", "lookup", "gets",
		"tables_probed_per_get", "blocks_per_get", "bloom_checks", "bloom_negatives",
		"measured_fp_rate", "predicted_fp_rate", "predicted_blocks_per_get",
	}}
	type result struct {
		rows         [][]any
		absentBlocks float64 // blocks per Get for absent keys
	}
	bitsSweep := []int{0, 5, 10, 20}
	name := func(bits int) string { return fmt.Sprintf("bloom=%d", bits) }
	results, ok := experiment.Sweep(t, bitsSweep, name, func(t *testing.T, bits int) result {
		log := newEventLog("e4", name(bits))
		db := openDB(t, log, lsmtree.Options{
			MemtableSize:        32 << 10,
			BloomBitsPerKey:     bits,
			NoBloom:             bits == 0,
			L0CompactionTrigger: 4,
			BaseLevelSize:       128 << 10,
			LevelSizeRatio:      10,
			MaxLevels:           5,
			TableSize:           32 << 10,
		})
		db.put(t, present)
		db.compact(t)
		log.phase = "measure"
		var tables int
		for _, l := range db.Stats().Levels {
			tables += l.Files
		}

		k, fp := 0, 1.0
		if bits > 0 {
			k, fp = bloom.Probes(bits), bloom.FalsePositiveRate(bits)
		}
		var res result
		for _, lk := range []struct {
			name  string
			ids   []int
			found bool
		}{{"present", present[:lookups], true}, {"absent", absent, false}} {
			before := db.Stats()
			for _, id := range lk.ids {
				_, ok, err := db.Get(key(id))
				if err != nil {
					t.Fatalf("Get(%d): %v", id, err)
				}
				if ok != lk.found {
					t.Fatalf("Get(%d) found=%v, want %v", id, ok, lk.found)
				}
			}
			d := db.Stats().Sub(before)
			gets := float64(d.Gets)
			probed := float64(d.TablesProbed) / gets
			blocks := float64(d.BlocksRead) / gets
			checks, negatives := d.BloomChecks, d.BloomNegatives

			// For an absent key every "maybe" is a false positive. For a
			// present key, the table that holds it answers a true
			// "maybe"; every other table's "maybe" is false.
			var measuredFP, predictedFP any // empty without a filter
			var predictedBlocks float64
			if bits > 0 {
				predictedFP = fp
			}
			if checks > 0 {
				falseMaybes := float64(checks - negatives)
				tablesWithoutKey := float64(checks)
				if lk.found {
					falseMaybes -= gets
					tablesWithoutKey -= gets
				}
				measuredFP = falseMaybes / tablesWithoutKey
			}
			if lk.found {
				predictedBlocks = 1 + fp*(probed-1)
			} else {
				predictedBlocks = fp * probed
				res.absentBlocks = blocks
			}
			res.rows = append(res.rows, []any{bits, k, n, tables, lk.name, int(gets), probed, blocks,
				checks, negatives, measuredFP, predictedFP, predictedBlocks})

			if bits > 0 && !lk.found {
				// The measured rate comes from about 100k checks; allow
				// for sampling noise and for small tables' filters.
				if got, ok := measuredFP.(float64); ok && got > 2*fp+0.005 {
					t.Errorf("false-positive rate %.4f; want near the predicted %.4f", got, fp)
				}
			}
			if lk.found && blocks < 1 {
				t.Errorf("present keys read %.3g blocks per Get; want >= 1", blocks)
			}
		}
		db.close(t)
		return res
	})
	if !ok {
		return
	}
	absentBlocks := map[int]float64{}
	for i, res := range results {
		for _, row := range res.rows {
			tb.Add(row...)
		}
		absentBlocks[bitsSweep[i]] = res.absentBlocks
	}
	tb.Report(t)

	none := absentBlocks[0]
	if none < 2 {
		t.Errorf("without filters an absent key reads %.3g blocks per Get; want >= 2 (one per level)", none)
	}
	prev := none
	for _, bits := range bitsSweep[1:] {
		got := absentBlocks[bits]
		if got >= prev {
			t.Errorf("absent-key blocks per Get did not fall at %d bits per key: %.3g (before: %.3g)", bits, got, prev)
		}
		prev = got
	}
	if got := absentBlocks[10]; got > 0.1*none {
		t.Errorf("10 bits per key: absent keys read %.3g blocks per Get; want under 10%% of %.3g without filters", got, none)
	}
}
