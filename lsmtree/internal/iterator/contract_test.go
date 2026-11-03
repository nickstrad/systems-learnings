package iterator_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

func TestSliceContract(t *testing.T) {
	itertest.Contract(t, func(t *testing.T, entries []keys.Entry) iterator.Iterator {
		return iterator.NewSlice(entries)
	})
}

// TestMergingContract splits the entries across children in several
// ways; the merge must give back exactly the original sequence.
func TestMergingContract(t *testing.T) {
	deals := map[string]func(i, n int, rng *rand.Rand) int{
		"roundrobin": func(i, n int, _ *rand.Rand) int { return i % n },
		"random":     func(_, n int, rng *rand.Rand) int { return rng.IntN(n) },
	}
	for name, deal := range deals {
		for _, n := range []int{1, 2, 5} {
			for _, empties := range []int{0, 2} {
				t.Run(fmt.Sprintf("%s/children=%d/empty=%d", name, n, empties), func(t *testing.T) {
					itertest.Contract(t, func(t *testing.T, entries []keys.Entry) iterator.Iterator {
						rng := rand.New(rand.NewPCG(uint64(n), uint64(len(entries))))
						parts := make([][]keys.Entry, n)
						for i, e := range entries {
							k := deal(i, n, rng)
							parts[k] = append(parts[k], e)
						}
						var children []iterator.Iterator
						for range empties {
							children = append(children, iterator.NewSlice(nil))
						}
						for _, p := range parts {
							children = append(children, iterator.NewSlice(p))
						}
						return iterator.NewMerging(children...)
					})
				})
			}
		}
	}
}

// skipVersions skips a contract case that holds several versions of one
// user key: dedup yields only the newest, by design, so it cannot be the
// identity the contract expects there.
func skipVersions(t *testing.T, entries []keys.Entry) {
	for i := 1; i < len(entries); i++ {
		if bytes.Equal(entries[i-1].Key, entries[i].Key) {
			t.Skip("dedup keeps one version per key")
		}
	}
}

// On inputs with distinct user keys, dedup must behave as the identity.
// dropTombstones is false because the contract walks a tombstone and
// expects to see it.
func TestDedupContract(t *testing.T) {
	itertest.Contract(t, func(t *testing.T, entries []keys.Entry) iterator.Iterator {
		skipVersions(t, entries)
		return iterator.NewDedup(iterator.NewSlice(entries), false)
	})
}

// TestDedupOverMergingContract runs the stack reads use: dedup over a
// merge.
func TestDedupOverMergingContract(t *testing.T) {
	itertest.Contract(t, func(t *testing.T, entries []keys.Entry) iterator.Iterator {
		skipVersions(t, entries)
		var even, odd []keys.Entry
		for i, e := range entries {
			if i%2 == 0 {
				even = append(even, e)
			} else {
				odd = append(odd, e)
			}
		}
		return iterator.NewDedup(iterator.NewMerging(iterator.NewSlice(even), iterator.NewSlice(odd)), false)
	})
}
