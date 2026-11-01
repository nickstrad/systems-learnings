package cluster_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
)

// ownerAfter returns the owner of key after replaying ops.
func ownerAfter(t *testing.T, key string, nodes ...string) string {
	t.Helper()
	var ops []cluster.Op
	for _, n := range nodes {
		ops = append(ops, cluster.Op{Kind: cluster.OpAdd, Node: n})
	}
	m, err := cluster.NewMembership(ops)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := m.Owner(key)
	return o
}

// Regression test for the stale-copy bug.
//
//  1. Adding c moves key k from x to c, but the clean-up delete on x fails.
//     The membership change is committed; x keeps a stale copy of k.
//  2. A user writes a newer value of k. It goes to c, the owner.
//  3. The next rebalance (adding d) scans every node, sees the stale copy
//     on x, and must NOT copy it over c's newer value.
func TestStaleCopyNeverOverwritesNewerValue(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b", "c", "d")
	f.add(t, "a", "b")

	// A key that moves to c when c joins and stays on c when d joins.
	key := findKey(t, func(k string) bool {
		return ownerAfter(t, k, "a", "b", "c") == "c" && ownerAfter(t, k, "a", "b", "c", "d") == "c"
	})
	x := ownerAfter(t, key, "a", "b")
	if _, err := f.c.Put(ctx, key, "old"); err != nil {
		t.Fatal(err)
	}

	f.nodes[x].FailDeletes = true
	if _, err := f.c.AddNode(ctx, "c"); err == nil {
		t.Fatal("expected the clean-up step to fail")
	}
	f.nodes[x].FailDeletes = false
	if _, ok := f.nodes[x].Store.Get(key); !ok {
		t.Fatal("setup: expected a stale copy on " + x)
	}

	if _, err := f.c.Put(ctx, key, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.AddNode(ctx, "d"); err != nil {
		t.Fatal(err)
	}

	if v, _, _ := f.c.Get(ctx, key); v != "new" {
		t.Fatalf("stale copy overwrote the newer value: got %q, want %q", v, "new")
	}
	if _, ok := f.nodes[x].Store.Get(key); ok {
		t.Fatalf("stale copy on %s was not cleaned up", x)
	}
	f.assertHealthy(t, 1)
}

// Regression test for a resurrected delete, found by FuzzClusterModel.
//
//  1. Adding c moves key k from x to c, but the clean-up delete on x fails,
//     so x keeps a stale copy of k.
//  2. A user deletes k. The delete goes to c, the owner.
//  3. The next rebalance found k only on x, with no copy on its owner, and
//     "repaired" it back onto the owner: the deleted key came back.
//
// A key with no copy on its owner doesn't exist, so the stale copy must be
// deleted, never copied.
func TestDeletedKeyStaysDeletedAfterFailedCleanUp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b", "c", "d")
	f.add(t, "a", "b")

	key := findKey(t, func(k string) bool { return ownerAfter(t, k, "a", "b", "c") == "c" })
	x := ownerAfter(t, key, "a", "b")
	if _, err := f.c.Put(ctx, key, "v"); err != nil {
		t.Fatal(err)
	}
	f.nodes[x].FailDeletes = true
	if _, err := f.c.AddNode(ctx, "c"); err == nil {
		t.Fatal("expected the clean-up step to fail")
	}
	f.nodes[x].FailDeletes = false

	if err := f.c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.AddNode(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := f.c.Get(ctx, key); ok {
		t.Fatalf("deleted key %q came back with value %q", key, v)
	}
	f.assertHealthy(t, 0)
}

// Regression test for a resurrected delete, found by FuzzClusterModel.
//
//  1. Removing b moves its key k to a, but the clean-up delete on b fails.
//     b leaves the cluster still holding a stale copy of k.
//  2. A user deletes k. The delete goes to a, the owner.
//  3. b is added back and owns k again. The rebalance didn't scan the
//     joining node, so b served its stale copy: the deleted key came back.
//
// The joining node is now scanned, and stale copies on a key's new owner
// are deleted before the commit.
func TestReAddedNodeDoesNotServeStaleCopies(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b")
	f.add(t, "a", "b")
	key := keyOwnedBy(t, f.c, "b")
	if _, err := f.c.Put(ctx, key, "old"); err != nil {
		t.Fatal(err)
	}

	f.nodes["b"].FailDeletes = true
	if _, err := f.c.RemoveNode(ctx, "b"); err == nil {
		t.Fatal("expected the clean-up step to fail")
	}
	f.nodes["b"].FailDeletes = false
	if err := f.c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}

	f.add(t, "b")
	if owner, _ := f.c.Owner(key); owner != "b" {
		t.Fatalf("setup: %q should be owned by b again, got %s", key, owner)
	}
	if v, ok, _ := f.c.Get(ctx, key); ok {
		t.Fatalf("deleted key %q came back with value %q", key, v)
	}
	f.assertHealthy(t, 0)
}

// The planner's rule for keys held by more than one node.
func TestPlanPicksTheAuthoritativeCopy(t *testing.T) {
	ab, _ := cluster.NewMembership([]cluster.Op{{Kind: cluster.OpAdd, Node: "a"}, {Kind: cluster.OpAdd, Node: "b"}})
	abc, _ := ab.Apply(cluster.Op{Kind: cluster.OpAdd, Node: "c"})

	// A key owned by a now that stays on a after c joins.
	key := ""
	for i := 0; key == ""; i++ {
		k := fmt.Sprint(i)
		if o, _ := ab.Owner(k); o == "a" {
			if o2, _ := abc.Owner(k); o2 == "a" {
				key = k
			}
		}
	}

	t.Run("owner's copy wins, stale copy is dropped not copied", func(t *testing.T) {
		p, _ := cluster.PlanMoves(map[string]map[string]string{
			"a": {key: "new"}, "b": {key: "old"},
		}, ab, abc)
		if len(p.Moves) != 0 || len(p.Drops) != 1 || p.Drops[0] != (cluster.Drop{Key: key, Node: "b"}) {
			t.Fatalf("got %+v", p)
		}
	})

	t.Run("a key with no copy on its owner is dropped, not repaired", func(t *testing.T) {
		// Reads go to the owner, so this key doesn't exist; b's copy is
		// stale (e.g. the key was deleted after a failed clean-up).
		p, _ := cluster.PlanMoves(map[string]map[string]string{"b": {key: "v"}}, ab, abc)
		if len(p.Moves) != 0 || len(p.Clears) != 0 || len(p.Drops) != 1 || p.Drops[0] != (cluster.Drop{Key: key, Node: "b"}) {
			t.Fatalf("got %+v", p)
		}
	})

	t.Run("a stale copy on the next owner is cleared before the commit", func(t *testing.T) {
		// c is joining and holds a copy of a key it will own, from before
		// it left. The current owner, a, has no copy, so the key doesn't exist.
		key := ""
		for i := 0; key == ""; i++ {
			k := fmt.Sprint(i)
			if o, _ := abc.Owner(k); o == "c" {
				key = k
			}
		}
		p, _ := cluster.PlanMoves(map[string]map[string]string{"c": {key: "old"}}, ab, abc)
		if len(p.Moves) != 0 || len(p.Drops) != 0 || len(p.Clears) != 1 || p.Clears[0] != (cluster.Drop{Key: key, Node: "c"}) {
			t.Fatalf("got %+v", p)
		}
	})
}
