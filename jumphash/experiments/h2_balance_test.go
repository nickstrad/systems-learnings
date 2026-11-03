//go:build experiments

package experiments

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// TestH2Balance measures balance: how evenly the keys spread over n
// buckets, for n = 10, 100, and 1000.
//
// A fixed sample of 1,000,000 keys is hashed, and the test reports each
// bucket's count against the mean: the smallest and largest bucket, and
// the standard deviation. Even a perfectly random assignment varies: each
// bucket's count is binomial, with standard deviation √(K·p·(1-p)) for K
// keys and p = 1/n. stddev_ratio divides the measured standard deviation
// by that, so 1 means "as even as perfect randomness" and anything well
// above 1 is imbalance from the method itself.
//
// The paper's Figure 2 measures the same thing over the whole key space
// rather than a sample: the standard error σ/μ of each bucket's share.
// For the ring that share is exact, so keyspace_std_err is the length of
// circle each bucket owns, with no sampling noise; it should come out
// near 1/√v, as in the paper's table (paper_std_err). For jump hash the
// paper reports 7.6·10⁻⁹: only sampling noise is left, so std_err falls
// to the binomial floor.
//
// The comparison: the ring at v = 1, 10, 100, and 1000 points per bucket
// (the paper's settings), jump hash, and modulo, which balances perfectly
// but fails H1. state_bytes is the memory each method needs (the paper's
// "Space Requirements"): the ring's points, and nothing for the others.
func TestH2Balance(t *testing.T) {
	t.Parallel()
	const numKeys = 1_000_000
	keys := sampleKeys(numKeys, 2)
	type method struct {
		name string
		v    int // points per bucket, for the ring
	}
	methods := []method{{name: "jump"}, {name: "modulo"},
		{name: "ring", v: 1}, {name: "ring", v: 10}, {name: "ring", v: 100}, {name: "ring", v: 1000}}
	// Figure 2's standard errors, by points per bucket; jump's is v = 0.
	paperStdErr := map[int]float64{0: 0.00000000764, 1: 0.9979060, 10: 0.3151810, 100: 0.0996996, 1000: 0.0315723}

	type config struct {
		n int
		m method
	}
	var configs []config
	for _, n := range []int{10, 100, 1000} {
		for _, m := range methods {
			configs = append(configs, config{n, m})
		}
	}
	type result struct {
		row            []any
		stddevRatio    float64
		keyspaceStdErr float64 // ring only
	}
	results, ok := experiment.Sweep(t, configs,
		func(c config) string {
			if c.m.v > 0 {
				return fmt.Sprintf("n=%d/%s/v=%d", c.n, c.m.name, c.m.v)
			}
			return fmt.Sprintf("n=%d/%s", c.n, c.m.name)
		},
		func(t *testing.T, c config) result {
			counts := make([]int, c.n)
			var keyspaceStdErr, paper any
			var stateBytes int
			switch c.m.name {
			case "jump":
				for _, k := range keys {
					counts[jumphash.Hash(k, int32(c.n))]++
				}
				paper = paperStdErr[0]
			case "modulo":
				for _, k := range keys {
					counts[modulo(k, c.n)]++
				}
			case "ring":
				r := newKargerRing(c.n, c.m.v)
				for _, k := range keys {
					counts[r.get(k)]++
				}
				keyspaceStdErr = relStddev(r.keyspaceShares(c.n))
				paper = paperStdErr[c.m.v]
				stateBytes = r.stateBytes()
			}
			s := spreadOf(counts)
			p := 1 / float64(c.n)
			binomial := math.Sqrt(numKeys * p * (1 - p))
			res := result{
				row: []any{c.n, c.m.name, optional(c.m.v), numKeys, s.mean,
					s.min / s.mean, s.max / s.mean, s.stddev, binomial, s.stddev / binomial,
					s.stddev / s.mean, keyspaceStdErr, paper, stateBytes},
				stddevRatio: s.stddev / binomial,
			}
			if f, ok := keyspaceStdErr.(float64); ok {
				res.keyspaceStdErr = f
			}
			return res
		})
	if !ok {
		return
	}
	tb := &experiment.Table{Name: "h2_balance", Header: []string{
		"n", "method", "points_per_bucket", "keys", "mean_keys",
		"min_over_mean", "max_over_mean", "stddev", "binomial_stddev", "stddev_ratio",
		"std_err", "keyspace_std_err", "paper_std_err", "state_bytes",
	}}
	for _, r := range results {
		tb.Add(r.row...)
	}
	tb.Report(t)

	for i := 0; i < len(configs); i += len(methods) {
		n := configs[i].n
		// The sample standard deviation of n bucket counts is itself
		// noisy: its relative error is about 1/√(2(n-1)).
		tol := 3 / math.Sqrt(2*float64(n-1))
		if got := results[i].stddevRatio; math.Abs(got-1) > tol {
			t.Errorf("n=%d: jump's stddev is %.2f× the binomial; want within %.2f of 1 (as even as perfect randomness)", n, got, tol)
		}
		if got := results[i+2].stddevRatio; got < 10 {
			t.Errorf("n=%d: the ring with 1 point per bucket has stddev %.2f× the binomial; want at least 10× (far worse than jump)", n, got)
		}
		for j := i + 3; j < i+len(methods); j++ {
			if got, prev := results[j].keyspaceStdErr, results[j-1].keyspaceStdErr; got >= prev {
				t.Errorf("n=%d: ring std err %.4f at v=%d is no better than %.4f at v=%d; more points should balance better",
					n, got, configs[j].m.v, prev, configs[j-1].m.v)
			}
		}
		// With 1000 buckets the ring's std err is well estimated, and a
		// bucket's share is a sum of v arcs, so σ/μ ≈ 1/√v.
		if n == 1000 {
			for j := i + 2; j < i+len(methods); j++ {
				v := configs[j].m.v
				if got, want := results[j].keyspaceStdErr, 1/math.Sqrt(float64(v)); math.Abs(got/want-1) > 0.15 {
					t.Errorf("n=1000: ring std err %.4f at v=%d; want within 15%% of 1/√v = %.4f", got, v, want)
				}
			}
		}
	}
}
