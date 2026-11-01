package jumphash

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/assert"
)

const testKeys = 100000

func nodeNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("node-%d", i)
	}
	return out
}

func snapshot(t *testing.T, r *Ring) []string {
	t.Helper()
	m := make([]string, testKeys)
	for k := range m {
		node, ok := r.Get(mix64(uint64(k)))
		if !ok {
			t.Fatal("Get returned no node on a non-empty ring")
		}
		m[k] = node
	}
	return m
}

// checkMoves fails if any key moved somewhere other than allowed, and
// returns how many keys moved.
func checkMoves(t *testing.T, before, after []string, allowed func(from, to string) bool) int {
	t.Helper()
	moved := 0
	for k := range before {
		if before[k] != after[k] {
			if !allowed(before[k], after[k]) {
				t.Fatalf("key %d moved %s -> %s, which is not allowed", k, before[k], after[k])
			}
			moved++
		}
	}
	return moved
}

func near(got, want int) bool {
	return math.Abs(float64(got-want)) <= 0.05*float64(want)+100
}

func TestRingAddRemoveErrors(t *testing.T) {
	r, err := NewRing("a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add("a"); err != ErrNodeExists {
		t.Fatalf("Add duplicate: got %v, want ErrNodeExists", err)
	}
	if err := r.Remove("zzz"); err != ErrNodeNotFound {
		t.Fatalf("Remove missing: got %v, want ErrNodeNotFound", err)
	}
	if err := r.Add(""); err == nil {
		t.Fatal("Add empty name: want error")
	}
	if _, err := NewRing("x", "x"); err != ErrNodeExists {
		t.Fatalf("NewRing duplicate: got %v, want ErrNodeExists", err)
	}
}

func TestRingEmpty(t *testing.T) {
	r, _ := NewRing("a")
	if err := r.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(1); ok {
		t.Fatal("Get on empty ring: want ok=false")
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
}

func TestRingRemoveMiddleMovesOnlyItsKeys(t *testing.T) {
	r, _ := NewRing(nodeNames(10)...)
	before := snapshot(t, r)
	if err := r.Remove("node-3"); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, r)
	moved := checkMoves(t, before, after, func(from, to string) bool { return from == "node-3" })
	if !near(moved, testKeys/10) {
		t.Errorf("moved %d keys, want ~%d", moved, testKeys/10)
	}
}

func TestRingAddMovesKeysOnlyToNewNode(t *testing.T) {
	r, _ := NewRing(nodeNames(10)...)
	before := snapshot(t, r)
	r.Add("new")
	after := snapshot(t, r)
	moved := checkMoves(t, before, after, func(from, to string) bool { return to == "new" })
	if !near(moved, testKeys/11) {
		t.Errorf("moved %d keys, want ~%d", moved, testKeys/11)
	}
}

func TestRingAddIntoHoleMovesKeysOnlyToNewNode(t *testing.T) {
	r, _ := NewRing(nodeNames(10)...)
	r.Remove("node-2")
	r.Remove("node-7")
	before := snapshot(t, r)
	r.Add("new") // fills node-7's slot
	after := snapshot(t, r)
	moved := checkMoves(t, before, after, func(from, to string) bool { return to == "new" })
	if !near(moved, testKeys/9) {
		t.Errorf("moved %d keys, want ~%d", moved, testKeys/9)
	}
}

func TestRingRemoveThenAddRestoresMapping(t *testing.T) {
	r, _ := NewRing(nodeNames(8)...)
	before := snapshot(t, r)
	r.Remove("node-5")
	r.Add("node-5")
	after := snapshot(t, r)
	if moved := checkMoves(t, before, after, func(string, string) bool { return false }); moved != 0 {
		t.Fatalf("%d keys moved after remove+add", moved)
	}
}

func TestRingBalancedAfterRemovals(t *testing.T) {
	r, _ := NewRing(nodeNames(20)...)
	for _, n := range []int{1, 4, 9, 13, 19} {
		r.Remove(fmt.Sprintf("node-%d", n))
	}
	counts := map[string]int{}
	for _, node := range snapshot(t, r) {
		counts[node]++
	}
	if len(counts) != 15 {
		t.Fatalf("keys spread over %d nodes, want 15", len(counts))
	}
	want := testKeys / 15
	for node, c := range counts {
		if !near(c, want) {
			t.Errorf("%s has %d keys, want ~%d", node, c, want)
		}
	}
}

func BenchmarkRingGet(b *testing.B) {
	r, _ := NewRing(nodeNames(100)...)
	for i := 0; i < 100; i += 4 {
		r.Remove(fmt.Sprintf("node-%d", i)) // 25% holes
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Get(uint64(i))
	}
}

// The invariant check must catch a corrupted ring. These tests corrupt the
// fields directly, which only a bug inside Ring could do.
func TestRingInvariantCatchesCorruption(t *testing.T) {
	if !assert.Enabled {
		t.Skip("assertions disabled (noassert)")
	}
	cases := map[string]func(r *Ring){
		"slot and index disagree": func(r *Ring) { r.slots[0] = "imposter" },
		"hole listed twice":       func(r *Ring) { r.holes = append(r.holes, r.holes[0]) },
		"hole points at live slot": func(r *Ring) {
			r.holes[0] = r.index["c"]
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := NewRing("a", "b", "c")
			r.Remove("b")
			corrupt(r)
			defer func() {
				if recover() == nil {
					t.Fatal("corruption was not detected")
				}
			}()
			r.checkInvariant()
		})
	}
}
