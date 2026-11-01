package cluster_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

// Failure paths: a fake node or store fails at one step, and the test checks
// what the error promises (message), where the data is, and whether
// membership changed.

// requireClusterError fails unless err is a cluster error that wraps cause
// and whose message contains promise.
func requireClusterError(t *testing.T, err, cause error, promise string) {
	t.Helper()
	e, ok := errs.Outermost(err)
	if !ok || e.Module != cluster.Module {
		t.Fatalf("not a well-formed cluster error: %s", errs.Describe(err))
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is lost the cause: %s", errs.Describe(err))
	}
	if !strings.Contains(e.Message, promise) {
		t.Fatalf("message %q should contain %q", e.Message, promise)
	}
}

// assertFilled checks that the n keys written by fill read back unchanged
// through the cluster.
func (f *fixture) assertFilled(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		k := fmt.Sprintf("k%d", i)
		if v, ok, err := f.c.Get(context.Background(), k); err != nil || !ok || v != fmt.Sprint(i) {
			t.Fatalf("Get(%q) = (%q, %v, %v)", k, v, ok, err)
		}
	}
}

// rebalanceFixture has a, b holding 200 keys, and c ready to join.
func rebalanceFixture(t *testing.T) (f *fixture, saves int) {
	t.Helper()
	f = newFixture(t, "a", "b", "c", "d")
	f.add(t, "a", "b")
	f.fill(t, 200)
	return f, f.store.Saves
}

// Phase 1, copy: nothing is committed and the old owners still hold
// everything, so a retry works.
func TestRebalanceCopyFailureChangesNothing(t *testing.T) {
	ctx := context.Background()
	f, saves := rebalanceFixture(t)
	f.nodes["c"].FailPuts = true

	_, err := f.c.AddNode(ctx, "c")
	requireClusterError(t, err, clustertest.ErrDown, "membership unchanged, safe to retry")
	if len(f.c.Nodes()) != 2 || f.store.Saves != saves {
		t.Fatalf("membership changed: nodes=%v saves=%d", f.c.Nodes(), f.store.Saves)
	}
	f.assertHealthy(t, 200)
	f.assertFilled(t, 200)

	f.nodes["c"].FailPuts = false
	f.add(t, "c")
	f.assertHealthy(t, 200)
}

// Phase 1, clear: a deleted key left a stale copy on the node about to own
// it. If the clear fails, membership stays unchanged, because committing
// would let reads see the stale copy.
func TestRebalanceClearFailureChangesNothing(t *testing.T) {
	ctx := context.Background()
	f, saves := rebalanceFixture(t)
	stale := findKey(t, func(k string) bool { return ownerAfter(t, k, "a", "b", "c") == "c" })
	f.nodes["c"].Store.Put(stale, "stale") // the key itself has no live copy
	f.nodes["c"].FailDeletes = true

	_, err := f.c.AddNode(ctx, "c")
	requireClusterError(t, err, clustertest.ErrDown, "membership unchanged, safe to retry")
	if len(f.c.Nodes()) != 2 || f.store.Saves != saves {
		t.Fatalf("membership changed: nodes=%v saves=%d", f.c.Nodes(), f.store.Saves)
	}
	f.assertHealthy(t, 200)

	f.nodes["c"].FailDeletes = false
	f.add(t, "c")
	if _, ok, _ := f.c.Get(ctx, stale); ok {
		t.Fatalf("stale copy of %q was served after the retry", stale)
	}
	f.assertHealthy(t, 200)
}

// Phase 2, commit: the keys are copied but membership is not saved, so the
// cluster keeps its old membership and every key is still readable.
func TestRebalanceSaveFailureCommitsNothing(t *testing.T) {
	ctx := context.Background()
	f, saves := rebalanceFixture(t)
	diskFull := errors.New("disk full")
	f.store.SaveErr = diskFull

	_, err := f.c.AddNode(ctx, "c")
	requireClusterError(t, err, diskFull, "nothing committed, safe to retry")
	if len(f.c.Nodes()) != 2 || f.store.Saves != saves {
		t.Fatalf("membership changed: nodes=%v saves=%d", f.c.Nodes(), f.store.Saves)
	}
	if f.nodes["c"].Store.Len() == 0 {
		t.Fatal("expected the copies to exist on the new node")
	}
	f.assertFilled(t, 200)

	f.store.SaveErr = nil
	f.add(t, "c")
	f.assertHealthy(t, 200)
}

// Phase 3, clean-up: membership is committed and every key is served by its
// new owner. Old copies remain, are reported as misplaced, and the next
// change deletes them.
func TestRebalanceCleanUpFailureKeepsDataSafe(t *testing.T) {
	ctx := context.Background()
	f, saves := rebalanceFixture(t)
	f.nodes["a"].FailDeletes, f.nodes["b"].FailDeletes = true, true

	_, err := f.c.AddNode(ctx, "c")
	requireClusterError(t, err, clustertest.ErrDown, "data is safe")
	if len(f.c.Nodes()) != 3 || f.store.Saves != saves+1 {
		t.Fatalf("membership not committed: nodes=%v saves=%d", f.c.Nodes(), f.store.Saves)
	}
	f.assertFilled(t, 200)
	misplaced := 0
	for _, s := range f.c.Stats(ctx) {
		misplaced += s.Misplaced
	}
	if misplaced == 0 {
		t.Fatal("expected stale copies to be reported as misplaced")
	}

	f.nodes["a"].FailDeletes, f.nodes["b"].FailDeletes = false, false
	f.add(t, "d")
	f.assertHealthy(t, 200)
}

// A node that errors during a key operation gives a cluster error that names
// the operation and keeps the cause.
func TestKeyOperationsReportNodeErrors(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a")
	f.add(t, "a")
	f.nodes["a"].Down = true

	_, putErr := f.c.Put(ctx, "k", "v")
	_, _, getErr := f.c.Get(ctx, "k")
	delErr := f.c.Delete(ctx, "k")
	requireClusterError(t, putErr, clustertest.ErrDown, "cannot write key")
	requireClusterError(t, getErr, clustertest.ErrDown, "cannot read key")
	requireClusterError(t, delErr, clustertest.ErrDown, "cannot delete key")
}

func TestOpenReportsLoadFailure(t *testing.T) {
	corrupt := errors.New("corrupt file")
	_, err := cluster.Open(&clustertest.MemStore{LoadErr: corrupt}, func(string) cluster.Node { return nil })
	requireClusterError(t, err, corrupt, "cannot load cluster membership")
}

// The zero Membership is not Valid, but reads treat it as empty. Apply
// requires a valid one, and rejects an op it can't replay.
func TestMembershipEdgeCases(t *testing.T) {
	var zero cluster.Membership
	if zero.Valid() || len(zero.Nodes()) != 0 {
		t.Fatal("zero Membership should be invalid and empty")
	}
	if n, ok := zero.Owner("k"); ok || n != "" {
		t.Fatalf("zero Membership owns %q", n)
	}
	requirePanic(t, "Apply pre", func() { zero.Apply(cluster.Op{Kind: cluster.OpAdd, Node: "a"}) })

	m, _ := cluster.NewMembership(nil)
	if _, err := m.Apply(cluster.Op{Kind: "rename", Node: "a"}); err == nil {
		t.Fatal("an unknown op kind should be an error")
	}
}
