//go:build experiments

package experiments

import (
	"math"
	"slices"
	"sort"
)

// The two baselines jump hash is compared with. Each one maps a key to a
// bucket in [0, n), like jumphash.Hash.
//
//   - modulo: key mod n, the naive scheme consistent hashing replaces. It
//     balances perfectly but moves almost every key when n changes.
//   - kargerRing: the classic consistent-hash ring of Karger et al.
//     (1997), as the paper describes it under "Related work".
//
// They are minimal, test-only baselines. kargerRing is NOT
// jumphash.Ring: that type is this repo's extension of jump hash that
// lets any node leave (holes); this one is the older algorithm the paper
// argues against.

// modulo is the naive baseline.
func modulo(key uint64, n int) int32 { return int32(key % uint64(n)) }

// kargerRing gives each bucket v points on a circle (here the uint64
// range, wrapping at 2^64), and maps a key to the bucket owning the first
// point at or clockwise after the key's position. A bucket's points
// depend only on its number, so growing from n to n+1 buckets adds the
// new bucket's points and changes nothing else: like jump hash, keys move
// only onto the new bucket. Balance needs many points per bucket, and
// the points are state that every client must hold.
type kargerRing struct {
	points  []uint64 // sorted positions on the circle
	buckets []int32  // buckets[i] owns points[i]
}

// ringPoint is bucket b's i-th point on the circle.
func ringPoint(b, i int) uint64 {
	return mix64(mix64(uint64(b)+0x5eed) + uint64(i))
}

// newKargerRing builds a ring of n buckets with v points each.
func newKargerRing(n, v int) *kargerRing {
	type point struct {
		pos    uint64
		bucket int32
	}
	pts := make([]point, 0, n*v)
	for b := range n {
		for i := range v {
			pts = append(pts, point{ringPoint(b, i), int32(b)})
		}
	}
	// Ties are vanishingly rare, but order them by bucket so the ring is
	// the same however it was built.
	sort.Slice(pts, func(i, j int) bool {
		if pts[i].pos != pts[j].pos {
			return pts[i].pos < pts[j].pos
		}
		return pts[i].bucket < pts[j].bucket
	})
	r := &kargerRing{points: make([]uint64, len(pts)), buckets: make([]int32, len(pts))}
	for i, p := range pts {
		r.points[i], r.buckets[i] = p.pos, p.bucket
	}
	return r
}

// without returns the ring with bucket b's points removed: for b = n-1,
// the same ring as newKargerRing(n-1, v), without sorting again.
func (r *kargerRing) without(b int32) *kargerRing {
	out := &kargerRing{}
	for i, pb := range r.buckets {
		if pb != b {
			out.points = append(out.points, r.points[i])
			out.buckets = append(out.buckets, pb)
		}
	}
	return out
}

// get returns key's bucket: the owner of the first point at or after the
// key's position, wrapping around to the first point.
func (r *kargerRing) get(key uint64) int32 {
	i, _ := slices.BinarySearch(r.points, mix64(key))
	if i == len(r.points) {
		i = 0
	}
	return r.buckets[i]
}

// keyspaceShares returns the exact fraction of the circle, and so of all
// possible keys, that each of the n buckets owns: point i owns the arc
// from the previous point (exclusive) up to itself. This is what the
// paper's Figure 2 measures, free of key-sampling noise.
func (r *kargerRing) keyspaceShares(n int) []float64 {
	shares := make([]float64, n)
	for i, p := range r.points {
		prev := r.points[(i+len(r.points)-1)%len(r.points)]
		arc := p - prev // wraps correctly for i == 0
		if len(r.points) == 1 {
			arc = math.MaxUint64
		}
		shares[r.buckets[i]] += float64(arc) / math.Exp2(64)
	}
	return shares
}

// stateBytes is the memory the ring needs: a 64-bit position and a 32-bit
// bucket per point, like the paper's "version B" but with full 64-bit
// positions. Jump hash and modulo need none.
func (r *kargerRing) stateBytes() int { return len(r.points) * 12 }
