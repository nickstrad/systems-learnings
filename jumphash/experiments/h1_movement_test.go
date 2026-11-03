//go:build experiments

package experiments

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// TestH1Movement measures the paper's core claim, monotonicity: when the
// number of buckets grows from n to n+1, only about 1/(n+1) of the keys
// move, and every key that moves goes to the new bucket n. Nothing moves
// between old buckets.
//
// For each n, a fixed sample of 100,000 keys is hashed with n and with
// n+1 buckets, and the test counts:
//
//   - moved: keys whose bucket changed. Jump hash should move 1/(n+1) of
//     them, the least any balanced scheme can move. Modulo hashing moves
//     n/(n+1): a key stays only if key mod n = key mod (n+1).
//   - misplaced: moved keys that did NOT land in the new bucket. Zero for
//     jump hash and for a Karger ring, which are both monotone; most of
//     modulo's moves are misplaced.
//   - donors: how many old buckets gave up keys, and max_donor_share, the
//     share of the moved keys that came from the biggest donor. The paper
//     ("Key Distribution") points out that jump hash takes an equal slice
//     from every old bucket, while a ring's new bucket takes keys only
//     from the owners of the arcs its v points land in: at most v donors,
//     with uneven shares. With 100,000 keys and large n only about
//     100,000/(n+1) keys move, so donors is limited by the sample too.
//
// The ring is the Karger baseline (baselines_test.go) at v = 1, 10, 100,
// and 1000 points per bucket. In expectation it also moves 1/(n+1), but
// any one step can move much more or much less.
func TestH1Movement(t *testing.T) {
	t.Parallel()
	const numKeys = 100_000
	keys := sampleKeys(numKeys, 1)
	ns := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 20, 30, 50, 70, 100, 150, 200, 300, 500, 700, 1000}
	vs := []int{1, 10, 100, 1000}

	tb := &experiment.Table{Name: "h1_movement", Header: []string{
		"n", "method", "points_per_bucket", "keys", "moved", "moved_fraction",
		"minimal_fraction", "predicted_fraction", "moved_over_minimal",
		"misplaced", "donors", "max_donor_share",
	}}
	type row struct {
		method   string
		v        int
		movement movement
	}
	results, ok := experiment.Sweep(t, ns, func(n int) string { return fmt.Sprintf("n=%d", n) },
		func(t *testing.T, n int) []row {
			newBucket := int32(n)
			rows := []row{
				{method: "jump", movement: measureMovement(keys, newBucket,
					func(k uint64) int32 { return jumphash.Hash(k, int32(n)) },
					func(k uint64) int32 { return jumphash.Hash(k, int32(n+1)) })},
				{method: "modulo", movement: measureMovement(keys, newBucket,
					func(k uint64) int32 { return modulo(k, n) },
					func(k uint64) int32 { return modulo(k, n+1) })},
			}
			for _, v := range vs {
				after := newKargerRing(n+1, v)
				before := after.without(newBucket)
				rows = append(rows, row{method: "ring", v: v,
					movement: measureMovement(keys, newBucket, before.get, after.get)})
			}

			minimal := 1 / float64(n+1)
			sigma := func(p float64) float64 { return math.Sqrt(p * (1 - p) / numKeys) }
			for _, r := range rows {
				m := r.movement
				if m.misplaced != 0 && r.method != "modulo" {
					t.Errorf("%s v=%d: %d keys moved between old buckets; a monotone hash moves keys only onto the new bucket",
						r.method, r.v, m.misplaced)
				}
				if r.method == "ring" && m.donors > r.v {
					t.Errorf("ring v=%d: %d donor buckets; the new bucket's %d points can split at most %d arcs",
						r.v, m.donors, r.v, r.v)
				}
			}
			jump, mod := rows[0].movement, rows[1].movement
			if got := jump.fraction(); math.Abs(got-minimal) > 5*sigma(minimal)+1e-9 {
				t.Errorf("jump moved %.5f of keys; want 1/(n+1) = %.5f within 5σ (%.5f)", got, minimal, 5*sigma(minimal))
			}
			if want := float64(n) / float64(n+1); math.Abs(mod.fraction()-want) > 5*sigma(want)+1e-9 {
				t.Errorf("modulo moved %.5f of keys; want n/(n+1) = %.5f within 5σ", mod.fraction(), want)
			}
			if n >= 2 && mod.fraction() < float64(n)/2*jump.fraction() {
				t.Errorf("modulo moved %.5f of keys, jump %.5f; want modulo at least n/2 = %d times as many",
					mod.fraction(), jump.fraction(), n/2)
			}
			return rows
		})
	if !ok {
		return
	}
	for i, n := range ns {
		minimal := 1 / float64(n+1)
		for _, r := range results[i] {
			m := r.movement
			predicted := minimal // jump, and the ring in expectation
			if r.method == "modulo" {
				predicted = float64(n) / float64(n+1)
			}
			tb.Add(n, r.method, optional(r.v), numKeys, m.moved, m.fraction(),
				minimal, predicted, m.fraction()/minimal,
				m.misplaced, m.donors, m.maxDonorShare())
		}
	}
	tb.Report(t)
}

// movement is what changed for a key sample when buckets grew by one.
type movement struct {
	keys, moved, misplaced int
	donors                 int // old buckets that lost at least one key
	maxDonor               int // keys lost by the biggest donor
}

func (m movement) fraction() float64 { return float64(m.moved) / float64(m.keys) }

// maxDonorShare is the share of the moved keys that the biggest donor
// gave up, or nil if nothing moved.
func (m movement) maxDonorShare() any {
	if m.moved == 0 {
		return nil
	}
	return float64(m.maxDonor) / float64(m.moved)
}

// measureMovement hashes keys before and after adding bucket newBucket.
func measureMovement(keys []uint64, newBucket int32, before, after func(uint64) int32) movement {
	m := movement{keys: len(keys)}
	lost := map[int32]int{}
	for _, k := range keys {
		b, a := before(k), after(k)
		if a == b {
			continue
		}
		m.moved++
		if a != newBucket {
			m.misplaced++
		}
		lost[b]++
	}
	m.donors = len(lost)
	for _, c := range lost {
		m.maxDonor = max(m.maxDonor, c)
	}
	return m
}
