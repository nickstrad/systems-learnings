// Package jumphash implements Jump Consistent Hash from
// Lamping & Veach, "A Fast, Minimal Memory, Consistent Hash Algorithm" (2014).
//
// Given a 64-bit key and a bucket count n, Hash returns a bucket in [0, n).
// When n grows to n+1, only about 1/(n+1) of keys move, and they all move
// to the new bucket n.
package jumphash

import "github.com/nickstrad/systems-learnings/jumphash/internal/assert"

// Hash maps key to a bucket in [0, numBuckets). It returns -1 if numBuckets <= 0.
//
// Postcondition: for numBuckets > 0, the result is in [0, numBuckets).
func Hash(key uint64, numBuckets int32) int32 {
	if numBuckets <= 0 {
		return -1
	}
	var b, j int64 = -1, 0
	for j < int64(numBuckets) {
		b = j
		// 64-bit linear congruential step: a cheap pseudo-random generator
		// seeded by the key, so every key follows its own fixed jump sequence.
		key = key*2862933555777941757 + 1
		// Jump forward: the next bucket this key would move to as n grows.
		j = int64(float64(b+1) * (float64(int64(1)<<31) / float64((key>>33)+1)))
	}
	assert.That(b >= 0 && b < int64(numBuckets), "jumphash: Hash result out of range")
	return int32(b)
}
