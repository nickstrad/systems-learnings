package jumphash

import (
	"fmt"
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
func FuzzRingOps(f *testing.F) {
	const (
		add    = 0
		remove = 1 << 3
		undo   = 1 << 4
	)
	f.Add([]byte{add | 0, add | 1, add | 2, remove | 1, add | 3})    // fill a hole
	f.Add([]byte{add | 0, add | 1, add | 2, remove | 1 | undo})      // remove then re-add
	f.Add([]byte{add | 0, add | 0, remove | 5})                      // errors
	f.Add([]byte{add | 0, add | 1, remove | 0, remove | 1, add | 1}) // empty, then reuse
	f.Add([]byte{add | 0, add | 1, add | 2, add | 3, remove | 0 | undo, remove | 2, add | 2})

	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > 64 {
			t.Skip("long scripts add little and slow the fuzzer down")
		}
		r, _ := NewRing()
		live := map[string]bool{}
		for step, b := range script {
			node := fmt.Sprintf("n%d", b&7)
			isRemove := b&remove != 0
			before := sampleOwners(r)

			var err error
			if isRemove {
				err = r.Remove(node)
			} else {
				err = r.Add(node)
			}
			if wantErr := live[node] != isRemove; wantErr != (err != nil) {
				t.Fatalf("step %d: op %#x on %s: err = %v, model says error = %v", step, b, node, err, wantErr)
			}
			after := sampleOwners(r)
			if err != nil {
				if !slices.Equal(before, after) {
					t.Fatalf("step %d: a failed op changed the mapping", step)
				}
				continue
			}
			live[node] = !isRemove
			checkRingMatches(t, r, live, after)

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
				restored := sampleOwners(r)
				checkRingMatches(t, r, live, restored)
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

// sampleOwners returns the owner of a fixed sample of keys, "" if the ring
// is empty.
func sampleOwners(r *Ring) []string {
	out := make([]string, 256)
	for k := range out {
		out[k], _ = r.Get(mix64(uint64(k)))
	}
	return out
}

// checkRingMatches fails if Len, Nodes, or the sampled owners disagree with
// the model.
func checkRingMatches(t *testing.T, r *Ring, live map[string]bool, owners []string) {
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
	}
}
