package jumphash

import (
	"math"
	"testing"
)

// FuzzHash checks Hash's documented guarantees for any key and any pair of
// bucket counts, including the extremes the table tests never reach (key 0,
// MaxUint64, bucket counts near MaxInt32).
//
// Properties, for any key k and bucket counts n and m:
//   - n <= 0 gives -1; otherwise the result is in [0, n)
//   - growing from n to n+1 buckets, k stays put or moves to the new bucket n
//   - for 0 < n < m, k stays put or moves to one of the new buckets [n, m).
//     This is the general form of the rule above: growing the cluster never
//     moves a key between two old buckets, however many buckets are added.
func FuzzHash(f *testing.F) {
	f.Add(uint64(0), int32(1), int32(2))
	f.Add(uint64(42), int32(0), int32(-5))
	f.Add(uint64(math.MaxUint64), int32(math.MaxInt32-1), int32(math.MaxInt32))
	f.Add(uint64(0x9E3779B97F4A7C15), int32(10), int32(1_000_000))
	f.Add(uint64(1), int32(math.MinInt32), int32(1))

	f.Fuzz(func(t *testing.T, key uint64, n, m int32) {
		got := Hash(key, n)
		if n <= 0 {
			if got != -1 {
				t.Fatalf("Hash(%d, %d) = %d, want -1", key, n, got)
			}
			return
		}
		if got < 0 || got >= n {
			t.Fatalf("Hash(%d, %d) = %d, out of range", key, n, got)
		}
		if n < math.MaxInt32 {
			if next := Hash(key, n+1); next != got && next != n {
				t.Fatalf("key %d: %d -> %d buckets moved %d -> %d, want stay or move to %d", key, n, n+1, got, next, n)
			}
		}

		if m > n {
			if later := Hash(key, m); later != got && later < n {
				t.Fatalf("key %d: %d -> %d buckets moved %d -> %d, an old bucket", key, n, m, got, later)
			}
		}
	})
}
