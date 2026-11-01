package jumphash

import (
	"hash/fnv"
	"math"
	"testing"
)

func TestInvalidBuckets(t *testing.T) {
	if got := Hash(42, 0); got != -1 {
		t.Fatalf("Hash(42, 0) = %d, want -1", got)
	}
	if got := Hash(42, -5); got != -1 {
		t.Fatalf("Hash(42, -5) = %d, want -1", got)
	}
}

func TestSingleBucket(t *testing.T) {
	for k := uint64(0); k < 1000; k++ {
		if got := Hash(k, 1); got != 0 {
			t.Fatalf("Hash(%d, 1) = %d, want 0", k, got)
		}
	}
}

// The same key and bucket count must always give the same bucket.
func TestDeterministic(t *testing.T) {
	for k := uint64(0); k < 1000; k++ {
		if Hash(k, 100) != Hash(k, 100) {
			t.Fatalf("non-deterministic for key %d", k)
		}
	}
}

func TestRange(t *testing.T) {
	for n := int32(1); n <= 200; n++ {
		for k := uint64(0); k < 500; k++ {
			b := Hash(k*0x9E3779B97F4A7C15, n)
			if b < 0 || b >= n {
				t.Fatalf("Hash(%d, %d) = %d out of range", k, n, b)
			}
		}
	}
}

// Growing from n to n+1 buckets: a key either stays put or moves to bucket n.
// About 1/(n+1) of keys should move.
func TestMonotoneAndMinimalMovement(t *testing.T) {
	const keys = 100000
	for n := int32(1); n < 50; n++ {
		moved := 0
		for k := uint64(0); k < keys; k++ {
			key := k * 0x9E3779B97F4A7C15
			before, after := Hash(key, n), Hash(key, n+1)
			if before != after {
				if after != n {
					t.Fatalf("key %d moved %d -> %d, want move only to new bucket %d", k, before, after, n)
				}
				moved++
			}
		}
		want := float64(keys) / float64(n+1)
		if math.Abs(float64(moved)-want) > 0.05*want+100 {
			t.Errorf("n=%d->%d: moved %d keys, want ~%.0f", n, n+1, moved, want)
		}
	}
}

func TestUniformDistribution(t *testing.T) {
	const n, keys = 10, 200000
	counts := make([]int, n)
	for k := uint64(0); k < keys; k++ {
		h := fnv.New64a()
		h.Write([]byte{byte(k), byte(k >> 8), byte(k >> 16), byte(k >> 24)})
		counts[Hash(h.Sum64(), n)]++
	}
	want := float64(keys) / n
	for b, c := range counts {
		if math.Abs(float64(c)-want) > 0.03*want {
			t.Errorf("bucket %d has %d keys, want ~%.0f", b, c, want)
		}
	}
}

func BenchmarkHash(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Hash(uint64(i), 1000)
	}
}
