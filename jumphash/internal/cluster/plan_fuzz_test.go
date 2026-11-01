package cluster_test

import (
	"fmt"
	"maps"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
)

// FuzzPlanMoves checks PlanMoves on its own, with random contents and
// random valid memberships:
//
//   - history and change are add/remove scripts over a pool of nodes (byte
//     b: node b%5, remove if b&8 != 0; invalid ops are skipped, so both
//     memberships are valid). current replays history; next replays
//     change on top of current, so it may differ by several ops or none.
//   - data is (node, key, value) triples for the contents, on any pool
//     node, member or not.
//
// Properties, applying the plan in memory in rebalance's order:
//   - It fails exactly when next has no nodes and some key has a copy on
//     its current owner (the only kind of copy that is data).
//   - At most one move per key.
//   - After the copy phase (moves, then clears), a read under current and
//     a read under next both return what a read under current returned
//     before. So the commit changes no read.
//   - The clean-up deletes never touch a key's owner under next, so a
//     clean-up that fails part-way changes no read either.
//   - After the clean-up, every key that existed is only on its owner under
//     next, with the same value, and every other copy is gone.
func FuzzPlanMoves(f *testing.F) {
	f.Add([]byte{0, 1}, []byte{2}, []byte{0, 0, 0, 1, 1, 1, 0, 2, 2, 1, 3, 3})
	f.Add([]byte{0, 1, 2}, []byte{8 | 1}, []byte{1, 0, 0, 1, 1, 1, 0, 1, 2})              // remove: stale copy too
	f.Add([]byte{0, 1, 8 | 1}, []byte{1}, []byte{1, 0, 0, 1, 1, 1, 1, 2, 2})              // re-add with stale copies
	f.Add([]byte{0}, []byte{8 | 0}, []byte{0, 0, 0})                                      // last node with data
	f.Add([]byte{0}, []byte{8 | 0}, []byte{1, 0, 0})                                      // last node, only a stray copy
	f.Add([]byte{}, []byte{0}, []byte{0, 0, 0})                                           // empty cluster, stale joining node
	f.Add([]byte{0, 1, 2, 3}, []byte{8 | 1, 8 | 2, 4}, []byte{0, 0, 0, 1, 0, 1, 2, 0, 2}) // several ops at once

	f.Fuzz(func(t *testing.T, history, change, data []byte) {
		if len(history)+len(change) > 32 || len(data) > 3*64 {
			t.Skip("big inputs add little and slow the fuzzer down")
		}
		empty, _ := cluster.NewMembership(nil)
		current := replay(empty, history)
		next := replay(current, change)

		contents := map[string]map[string]string{}
		for i := 0; i+2 < len(data); i += 3 {
			node := nodeName(int(data[i]))
			if contents[node] == nil {
				contents[node] = map[string]string{}
			}
			contents[node][keyName(int(data[i+1]))] = fmt.Sprintf("v%d", data[i+2]%4)
		}

		// What a read returns now. Only the current owner's copy counts.
		before := reads(contents, current)
		p, err := cluster.PlanMoves(contents, current, next)
		if wantErr := len(next.Nodes()) == 0 && len(before) > 0; wantErr != (err != nil) {
			t.Fatalf("err = %v, want an error: %v (live keys %v)", err, wantErr, before)
		}
		if err != nil {
			return
		}

		state := map[string]map[string]string{}
		for n, entries := range contents {
			state[n] = maps.Clone(entries)
		}
		del := func(key, node string) { delete(state[node], key) }

		// Phase 1: copy, then clear.
		moved := map[string]bool{}
		for _, m := range p.Moves {
			if moved[m.Key] {
				t.Fatalf("two moves for %q", m.Key)
			}
			moved[m.Key] = true
			if state[m.To] == nil {
				state[m.To] = map[string]string{}
			}
			state[m.To][m.Key] = m.Value
		}
		for _, d := range p.Clears {
			del(d.Key, d.Node)
		}
		if got := reads(state, current); !maps.Equal(got, before) {
			t.Fatalf("copy phase changed reads under current: %v -> %v", before, got)
		}
		if got := reads(state, next); !maps.Equal(got, before) {
			t.Fatalf("after the copy phase, reads under next = %v, want %v", got, before)
		}

		// Phase 3: clean up. None of it may touch a key's next owner.
		cleanup := p.Drops
		for _, m := range p.Moves {
			cleanup = append(cleanup, cluster.Drop{Key: m.Key, Node: m.From})
		}
		for _, d := range cleanup {
			if owner, _ := next.Owner(d.Key); owner == d.Node {
				t.Fatalf("clean-up deletes %q from its next owner %s", d.Key, d.Node)
			}
			del(d.Key, d.Node)
		}

		for n, entries := range state {
			for k, v := range entries {
				if owner, _ := next.Owner(k); owner != n || before[k] != v {
					t.Fatalf("after the plan, %s holds %s=%q; owner is %s and the live value is %q", n, k, v, owner, before[k])
				}
			}
		}
	})
}

// replay applies a script of membership ops to m, skipping invalid ones.
func replay(m cluster.Membership, script []byte) cluster.Membership {
	for _, b := range script {
		op := cluster.Op{Kind: cluster.OpAdd, Node: nodeName(int(b))}
		if b&8 != 0 {
			op.Kind = cluster.OpRemove
		}
		if next, err := m.Apply(op); err == nil {
			m = next
		}
	}
	return m
}

// reads returns what a read of each key returns under m: the copy on its
// owner, if the owner has one.
func reads(state map[string]map[string]string, m cluster.Membership) map[string]string {
	out := map[string]string{}
	for _, entries := range state {
		for k := range entries {
			if owner, ok := m.Owner(k); ok {
				if v, held := state[owner][k]; held {
					out[k] = v
				}
			}
		}
	}
	return out
}
