//go:build experiments

// Package experiments measures the paper's claims about the hash itself,
// not the toy cluster around it:
//
//   - H1: growing from n to n+1 buckets moves about 1/(n+1) of the keys,
//     all onto the new bucket (the paper's monotonicity, its core claim).
//   - H2: keys spread evenly over the buckets, with no extra state, where
//     a Karger ring needs many points per bucket to get close.
//   - H3: a lookup takes about ln(n) loop iterations (the paper's
//     "Performance Analysis").
//
// Each Test is one experiment. Nothing is timed: the tests count keys and
// loop iterations over a fixed, seeded key sample, so the numbers are the
// same on every machine and every run. The baselines (modulo hashing and
// a classic Karger ring) are in baselines_test.go.
//
// The tests are behind the `experiments` build tag, so plain `go test
// ./...` skips them. Run them with `make experiments` (which runs
// shared/scripts/experiments.sh and then scripts/experiments.sql in
// DuckDB), or directly:
//
//	go test -tags experiments -count=1 -v ./experiments
//
// Each test prints its table with t.Log and asserts only a loose
// expected shape, so the suite also works as a regression check. When
// EXPERIMENT_RESULTS_DIR is set, the tables are also saved as CSV files
// there (see shared/experiment).
package experiments

import (
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/nickstrad/systems-learnings/shared/experiment"
)

func TestMain(m *testing.M) { os.Exit(experiment.Main(m)) }

// sampleKeys returns k pseudo-random 64-bit keys from a PCG seeded with
// seed. Each experiment uses its own seed, so changing one experiment
// does not move another's numbers.
func sampleKeys(k int, seed uint64) []uint64 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	out := make([]uint64, k)
	for i := range out {
		out[i] = rng.Uint64()
	}
	return out
}

// mix64 is the SplitMix64 finalizer: a cheap, well-mixed 64-bit hash.
// The ring baseline uses it to place points and keys on its circle.
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// spread summarises how many keys each bucket got.
type spread struct {
	mean, min, max, stddev float64 // stddev is the population one
}

func spreadOf(counts []int) spread {
	s := spread{min: math.Inf(1), max: math.Inf(-1)}
	for _, c := range counts {
		s.mean += float64(c)
		s.min = math.Min(s.min, float64(c))
		s.max = math.Max(s.max, float64(c))
	}
	s.mean /= float64(len(counts))
	for _, c := range counts {
		d := float64(c) - s.mean
		s.stddev += d * d
	}
	s.stddev = math.Sqrt(s.stddev / float64(len(counts)))
	return s
}

// relStddev returns the population standard deviation of xs over their
// mean, the paper's "standard error" σ/μ (Figure 2).
func relStddev(xs []float64) float64 {
	var mean, ss float64
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	return math.Sqrt(ss/float64(len(xs))) / mean
}

// optional turns 0 into nil, so a column that doesn't apply to a method
// (points per bucket for jump) is empty in the table and NULL in DuckDB.
func optional(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
