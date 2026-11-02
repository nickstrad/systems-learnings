package jumphash

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

// FuzzRingOps reads the fuzz input as a script of ring changes. Each byte
// picks a node from a small pool and an op:
//
//	bits 0-2: node n0..n7
//	bit  3:   0 = add, 1 = remove
//	bit  4:   1 = after a remove, add the node straight back, to check the
//	          metamorphic rule "remove then re-add restores the mapping"
//
// (The reverse, add then remove, does not restore the mapping when the add
// appended a slot: the new trailing hole re-hashes some keys. Ring doesn't
// promise it, so it isn't checked.)
//
// The script runs against a Ring and a model set of live nodes. After every
// step:
//   - the ring invariant holds (Add and Remove assert checkInvariant)
//   - Add and Remove fail exactly when the model says they should
//   - Len and Nodes match the model
//   - Get always returns a live node for a sample of keys
//   - an add moves sampled keys only onto the new node, and a remove moves
//     only the removed node's keys
//   - removing a node and adding it straight back restores every sampled
//     key's owner
//   - when a key's first jump-hash slot is a live node, Get returns it
//     (with no holes, that makes Ring exactly plain Hash over its slots)
//
// The fuzzer also picks seed, which chooses the sampled keys, so the checks
// above run over the whole key space rather than one fixed sample.
func FuzzRingOps(f *testing.F) {
	const (
		add    = 0
		remove = 1 << 3
		undo   = 1 << 4
	)
	f.Add(uint64(0), []byte{add | 0, add | 1, add | 2, remove | 1, add | 3})    // fill a hole
	f.Add(uint64(1), []byte{add | 0, add | 1, add | 2, remove | 1 | undo})      // remove then re-add
	f.Add(uint64(2), []byte{add | 0, add | 0, remove | 5})                      // errors
	f.Add(uint64(3), []byte{add | 0, add | 1, remove | 0, remove | 1, add | 1}) // empty, then reuse
	f.Add(uint64(math.MaxUint64), []byte{add | 0, add | 1, add | 2, add | 3, remove | 0 | undo, remove | 2, add | 2})

	f.Fuzz(func(t *testing.T, seed uint64, script []byte) {
		if len(script) > 64 {
			t.Skip("long scripts add little and slow the fuzzer down")
		}
		keys := sampleKeys(seed)
		r, _ := NewRing()
		live := map[string]bool{}
		for step, b := range script {
			node := fmt.Sprintf("n%d", b&7)
			isRemove := b&remove != 0
			before := sampleOwners(r, keys)

			var err error
			if isRemove {
				err = r.Remove(node)
			} else {
				err = r.Add(node)
			}
			if wantErr := live[node] != isRemove; wantErr != (err != nil) {
				t.Fatalf("step %d: op %#x on %s: err = %v, model says error = %v", step, b, node, err, wantErr)
			}
			after := sampleOwners(r, keys)
			if err != nil {
				if !slices.Equal(before, after) {
					t.Fatalf("step %d: a failed op changed the mapping", step)
				}
				continue
			}
			live[node] = !isRemove
			checkRingMatches(t, r, live, keys, after)

			for k := range before {
				switch {
				case before[k] == after[k]:
				case isRemove && before[k] == node:
				case !isRemove && after[k] == node:
				default:
					t.Fatalf("step %d: %s %s moved sampled key %d %q -> %q", step, opName(isRemove), node, k, before[k], after[k])
				}
			}

			if isRemove && b&undo != 0 {
				if err := r.Add(node); err != nil {
					t.Fatalf("step %d: re-adding %s failed: %v", step, node, err)
				}
				live[node] = true
				restored := sampleOwners(r, keys)
				checkRingMatches(t, r, live, keys, restored)
				if !slices.Equal(before, restored) {
					t.Fatalf("step %d: removing and re-adding %s did not restore the mapping", step, node)
				}
			}
		}
	})
}

func opName(isRemove bool) string {
	if isRemove {
		return "remove"
	}
	return "add"
}

// sampleKeys returns the 256 keys sampled for seed.
func sampleKeys(seed uint64) []uint64 {
	keys := make([]uint64, 256)
	for k := range keys {
		keys[k] = mix64(seed + uint64(k))
	}
	return keys
}

// sampleOwners returns the owner of each key, "" if the ring is empty.
func sampleOwners(r *Ring, keys []uint64) []string {
	out := make([]string, len(keys))
	for k, key := range keys {
		out[k], _ = r.Get(key)
	}
	return out
}

// checkRingMatches fails if Len, Nodes, or the sampled owners disagree with
// the model, or if Get skips past a key's live first slot.
func checkRingMatches(t *testing.T, r *Ring, live map[string]bool, keys []uint64, owners []string) {
	t.Helper()
	var want []string
	for n, ok := range live {
		if ok {
			want = append(want, n)
		}
	}
	got := r.Nodes()
	if r.Len() != len(want) || !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("ring has %v (Len %d), model has %v", got, r.Len(), want)
	}
	for k, owner := range owners {
		if (owner == "") != (len(want) == 0) || (owner != "" && !live[owner]) {
			t.Fatalf("Get(sample %d) = %q, not a live node of %v", k, owner, want)
		}
		if len(r.slots) == 0 {
			continue
		}
		if first := r.slots[Hash(keys[k], int32(len(r.slots)))]; first != "" && owner != first {
			t.Fatalf("Get(%d) = %q, but its first slot holds live node %q", keys[k], owner, first)
		}
	}
}
