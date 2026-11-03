// Package bloom is a Bloom filter over user keys, stored with each
// SSTable so a point lookup can skip a file without reading a data block.
//
// A filter answers "might this file hold key?". It never says no to a key
// that was added (no false negatives), and says yes to a key that was not
// added with probability about
//
//	(1 - e^(-k·n/m))^k
//
// for n keys, m bits, and k hash functions. With m/n bits per key the
// best k is (m/n)·ln 2, which is what Builder picks.
//
// # Format
//
// A Filter is a plain byte slice, ready to write to disk:
//
//	+------------------------------+-------+
//	| bit array (m/8 bytes)        | k u8  |
//	+------------------------------+-------+
//
// Bit i lives in byte i/8, at bit i%8. The k probes come from one 64-bit
// hash by double hashing (Kirsch and Mitzenmacher): probe j is
// h1 + j·h2 mod m. The hash is FNV-1a 64 followed by a murmur3 finalizer,
// fixed forever, so a filter written by one process is read correctly by
// another (hash/maphash is seeded per process, so it can't be used).
package bloom

import (
	"hash/fnv"
	"math"

	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Limits on k, the number of probes per key.
const (
	minProbes = 1
	maxProbes = 30
)

// minBits keeps tiny filters (few keys) from having a uselessly high
// false-positive rate.
const minBits = 64

// Filter is an encoded Bloom filter: the bit array followed by one byte
// holding k. It may come from disk, so every method accepts any bytes.
type Filter []byte

// Builder collects keys one at a time, so the SSTable writer can stream
// entries without keeping them. It keeps one 64-bit hash per key.
type Builder struct {
	bitsPerKey int
	hashes     []uint64
}

// NewBuilder returns a Builder that will spend about bitsPerKey bits per
// key.
//
//	pre: bitsPerKey >= 1
func NewBuilder(bitsPerKey int) *Builder {
	assert.That(bitsPerKey >= 1, "bloom: bitsPerKey %d < 1", bitsPerKey)
	return &Builder{bitsPerKey: bitsPerKey}
}

// Add records key. Adding the same key twice is harmless.
func (b *Builder) Add(key []byte) {
	b.hashes = append(b.hashes, hash(key))
}

// Len returns the number of keys added so far.
func (b *Builder) Len() int { return len(b.hashes) }

// Size returns the length in bytes of the filter Build would return now.
// The SSTable writer uses it to estimate its output size.
//
//	post: Size() == len(Build())
func (b *Builder) Size() int { return b.bitBytes() + 1 }

// bitBytes is the length of the bit array: n·bitsPerKey bits, at least
// minBits, rounded up to a whole byte.
func (b *Builder) bitBytes() int {
	return (max(len(b.hashes)*b.bitsPerKey, minBits) + 7) / 8
}

// Probes returns k for bitsPerKey bits per key: round(bitsPerKey·ln 2),
// clamped to [minProbes, maxProbes].
func Probes(bitsPerKey int) int {
	k := int(math.Round(float64(bitsPerKey) * math.Ln2))
	return min(max(k, minProbes), maxProbes)
}

// FalsePositiveRate returns the expected false-positive rate of a filter
// built with bitsPerKey bits per key: (1 - e^(-k/b))^k, with b =
// bitsPerKey and k = Probes(b). It ignores the rounding of the bit array
// to whole bytes and the minBits floor, which matter only for tiny
// filters.
//
//	pre: bitsPerKey >= 1
func FalsePositiveRate(bitsPerKey int) float64 {
	assert.That(bitsPerKey >= 1, "bloom: bitsPerKey %d < 1", bitsPerKey)
	k := float64(Probes(bitsPerKey))
	return math.Pow(1-math.Exp(-k/float64(bitsPerKey)), k)
}

// Build encodes the filter over every key added. The Builder can keep
// collecting keys afterwards.
//
//	post: f.MayContain(key) for every added key
//	post: len(f) == m/8 + 1 with m = max(n·bitsPerKey, 64) rounded up to
//	      a whole byte
func (b *Builder) Build() Filter {
	nbytes := b.bitBytes()
	f := make(Filter, nbytes+1)
	k := Probes(b.bitsPerKey)
	f[nbytes] = byte(k)
	m := uint64(nbytes * 8)
	for _, h := range b.hashes {
		h1, h2 := split(h)
		for j := range uint64(k) {
			bit := (h1 + j*h2) % m
			f[bit/8] |= 1 << (bit % 8)
		}
	}
	return f
}

// New builds a filter over keys in one call.
//
//	pre: bitsPerKey >= 1
func New(bitsPerKey int, keys [][]byte) Filter {
	b := NewBuilder(bitsPerKey)
	for _, k := range keys {
		b.Add(k)
	}
	return b.Build()
}

// MayContain reports whether key might have been added. False means
// definitely not. A filter too short to hold any bits, or whose k byte is
// out of range, cannot rule anything out, so it answers true: a damaged
// filter costs a block read, never a wrong answer.
func (f Filter) MayContain(key []byte) bool {
	if len(f) < 2 {
		return true
	}
	k := uint64(f[len(f)-1])
	if k < minProbes || k > maxProbes {
		return true
	}
	m := uint64(len(f)-1) * 8
	h1, h2 := split(hash(key))
	for j := range k {
		bit := (h1 + j*h2) % m
		if f[bit/8]&(1<<(bit%8)) == 0 {
			return false
		}
	}
	return true
}

// hash is the fixed key hash. FNV-1a alone mixes its high bits poorly for
// short keys, so a murmur3 finalizer spreads every input bit across the
// whole word before split takes it apart.
func hash(key []byte) uint64 {
	h := fnv.New64a()
	h.Write(key) // never fails
	x := h.Sum64()
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

// split turns one hash into the two double-hashing values. h2 is forced
// odd so that it is never zero, which would make every probe the same bit.
func split(h uint64) (h1, h2 uint64) {
	return h & 0xffffffff, (h >> 32) | 1
}
