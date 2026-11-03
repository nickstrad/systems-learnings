//go:build experiments

package experiments

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

// eulerGamma is the Euler–Mascheroni constant: H_n - ln(n) → γ.
const eulerGamma = 0.5772156649015329

// TestH3Jumps measures the cost of a lookup in loop iterations, for n
// from 1 to 2^20 buckets, without timing anything.
//
// The paper's "Performance Analysis": the loop visits successive jump
// destinations, all below n except the last, so the number of iterations
// is one more than the number of jumps below n. A jump lands at bucket
// count i with probability 1/i, so the expected number of jumps below n
// is the sum of 1/i for i = 2..n, "which is less than ln(n)". The
// expected number of iterations is therefore exactly the harmonic number
//
//	H_n = 1 + 1/2 + ... + 1/n ≈ ln(n) + γ + 1/(2n),  γ ≈ 0.5772,
//
// which the paper bounds by ln(n) + 1 (paper_bound). Each jump happens
// independently, so the iterations' variance is the sum of (1/i)(1-1/i).
// The paper also notes this beats the log2(n) comparisons of a binary
// search, which is what a ring lookup costs (log2 of its points).
//
// To count iterations, hashCounting is a copy of jumphash.Hash with a
// counter. For every key and n it measures, the test checks that the copy
// returns exactly jumphash.Hash's bucket, so the copy can't drift from
// the real algorithm. A second table, h3_fit, fits mean iterations to
// a·ln(n) + b by least squares; theory says a = 1 and b ≈ γ.
func TestH3Jumps(t *testing.T) {
	t.Parallel()
	const numKeys = 1_000_000
	keys := sampleKeys(numKeys, 3)
	var ns []int // 1, 2, 3, 4, 6, 8, 11, ..., 2^20: two per doubling
	for e := 0; e <= 40; e++ {
		n := int(math.Round(math.Exp2(float64(e) / 2)))
		if len(ns) == 0 || n != ns[len(ns)-1] {
			ns = append(ns, n)
		}
	}

	type result struct {
		mean, stddev float64
		max          int
	}
	results, ok := experiment.Sweep(t, ns, func(n int) string { return fmt.Sprintf("n=%d", n) },
		func(t *testing.T, n int) result {
			var sum, sumSq float64
			var r result
			for _, k := range keys {
				b, iters := hashCounting(k, int32(n))
				if want := jumphash.Hash(k, int32(n)); b != want {
					t.Fatalf("hashCounting(%d, %d) = %d, jumphash.Hash = %d; the instrumented copy has drifted", k, n, b, want)
				}
				sum += float64(iters)
				sumSq += float64(iters) * float64(iters)
				r.max = max(r.max, iters)
			}
			r.mean = sum / numKeys
			r.stddev = math.Sqrt(sumSq/numKeys - r.mean*r.mean)
			return r
		})
	if !ok {
		return
	}

	tb := &experiment.Table{Name: "h3_jumps", Header: []string{
		"n", "keys", "mean_iterations", "expected_iterations", "mean_over_expected",
		"paper_bound", "log2_n", "stddev_iterations", "expected_stddev", "max_iterations",
	}}
	var xs, ys []float64 // for the fit: ln(n), mean iterations, n >= 2
	for i, n := range ns {
		r := results[i]
		hn, variance := harmonic(n)
		bound := math.Log(float64(n)) + 1
		tb.Add(n, numKeys, r.mean, hn, r.mean/hn, bound, math.Log2(float64(n)),
			r.stddev, math.Sqrt(variance), r.max)

		if se := math.Sqrt(variance / numKeys); math.Abs(r.mean-hn) > 5*se+1e-9 {
			t.Errorf("n=%d: %.4f iterations per lookup; want H_n = %.4f within 5 standard errors (%.4f)", n, r.mean, hn, 5*se)
		}
		if r.mean > bound+1e-9 {
			t.Errorf("n=%d: %.4f iterations per lookup; the paper's bound is ln(n)+1 = %.4f", n, r.mean, bound)
		}
		if n >= 2 {
			xs = append(xs, math.Log(float64(n)))
			ys = append(ys, r.mean)
		}
	}
	tb.Report(t)

	a, b, r2 := fitLine(xs, ys)
	fit := &experiment.Table{Name: "h3_fit", Header: []string{
		"model", "slope_a", "intercept_b", "r_squared", "theory_a", "theory_b", "points",
	}}
	fit.Add("iterations = a·ln(n) + b", a, b, r2, 1.0, eulerGamma, len(xs))
	fit.Report(t)
	if math.Abs(a-1) > 0.05 || math.Abs(b-eulerGamma) > 0.15 {
		t.Errorf("fit: iterations = %.3f·ln(n) + %.3f; want about 1·ln(n) + γ (%.3f)", a, b, eulerGamma)
	}
}

// hashCounting is jumphash.Hash (jumphash.go) with a counter: it returns
// the bucket and the number of loop iterations. Keep the loop identical
// to Hash's; TestH3Jumps checks every result against Hash.
func hashCounting(key uint64, numBuckets int32) (bucket int32, iterations int) {
	var b, j int64 = -1, 0
	for j < int64(numBuckets) {
		iterations++
		b = j
		key = key*2862933555777941757 + 1
		j = int64(float64(b+1) * (float64(int64(1)<<31) / float64((key>>33)+1)))
	}
	return int32(b), iterations
}

// harmonic returns the expected number of iterations for n buckets, the
// harmonic number H_n, and its variance: one iteration always, plus one
// for each bucket count i = 2..n at which the key jumps, an independent
// event with probability 1/i.
func harmonic(n int) (mean, variance float64) {
	mean = 1
	for i := 2; i <= n; i++ {
		p := 1 / float64(i)
		mean += p
		variance += p * (1 - p)
	}
	return mean, variance
}

// fitLine fits y = a·x + b by least squares and returns R².
func fitLine(xs, ys []float64) (a, b, r2 float64) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	a = (n*sxy - sx*sy) / (n*sxx - sx*sx)
	b = (sy - a*sx) / n
	var ssRes, ssTot float64
	for i := range xs {
		ssRes += math.Pow(ys[i]-(a*xs[i]+b), 2)
		ssTot += math.Pow(ys[i]-sy/n, 2)
	}
	return a, b, 1 - ssRes/ssTot
}
