package cluster_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Domain tests: nodes and membership are in-memory fakes from clustertest,
// so there is no network and no disk. The fakes pass the same contract
// suites as the real adapters.

type fixture struct {
	c     *cluster.Cluster
	store *clustertest.MemStore
	nodes map[string]*clustertest.MemNode
}

func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	f := &fixture{store: &clustertest.MemStore{}, nodes: map[string]*clustertest.MemNode{}}
	for _, n := range names {
		f.nodes[n] = clustertest.NewMemNode()
	}
	c, err := cluster.Open(f.store, func(addr string) cluster.Node { return f.nodes[addr] })
	if err != nil {
		t.Fatal(err)
	}
	f.c = c
	return f
}

func (f *fixture) add(t *testing.T, nodes ...string) {
	t.Helper()
	for _, n := range nodes {
		if _, err := f.c.AddNode(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) fill(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		if _, err := f.c.Put(context.Background(), fmt.Sprintf("k%d", i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) assertHealthy(t *testing.T, want int) {
	t.Helper()
	total := 0
	for _, s := range f.c.Stats(context.Background()) {
		if s.Misplaced != 0 || s.Unreachable != nil {
			t.Fatalf("bad stat: %+v", s)
		}
		total += s.Keys
	}
	if total != want {
		t.Fatalf("cluster holds %d keys, want %d", total, want)
	}
}

func TestAddAndRemoveMoveTheRightKeys(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b", "c", "d")
	f.add(t, "a", "b", "c")
	const n = 3000
	f.fill(t, n)

	rep, err := f.c.AddNode(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rep.Moves {
		if m.To != "d" {
			t.Fatalf("add moved %q to %s, expected only moves to the new node", m.Key, m.To)
		}
	}
	if got := len(rep.Moves); got < n/4-200 || got > n/4+200 {
		t.Fatalf("add moved %d keys, expected about %d", got, n/4)
	}

	held := f.nodes["b"].Store.Len()
	rep, err = f.c.RemoveNode(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Moves) != held || f.nodes["b"].Store.Len() != 0 {
		t.Fatalf("remove moved %d of %d keys, %d left behind", len(rep.Moves), held, f.nodes["b"].Store.Len())
	}
	f.assertHealthy(t, n)
}

func TestMembershipChangesAreValidated(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a")
	f.add(t, "a")
	if _, err := f.c.AddNode(ctx, "a"); err == nil {
		t.Fatal("duplicate add should fail")
	}
	if _, err := f.c.RemoveNode(ctx, "zzz"); err == nil {
		t.Fatal("removing unknown node should fail")
	}
	if _, err := f.c.AddNode(ctx, ""); err == nil {
		t.Fatal("adding an empty node name should fail")
	}
	f.fill(t, 1)
	if _, err := f.c.RemoveNode(ctx, "a"); err == nil {
		t.Fatal("removing last node with data should fail")
	}
	if len(f.c.Nodes()) != 1 || f.store.Saves != 1 {
		t.Fatalf("failed changes must not save membership (saves=%d)", f.store.Saves)
	}
}

// User input is checked with errors at the domain boundary, so it can never
// reach a Node and trip the Node.Put precondition.
func TestEmptyKeyIsAnErrorNotAPanic(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a")
	f.add(t, "a")
	if _, err := f.c.Put(ctx, "", "v"); !errors.Is(err, cluster.ErrEmptyKey) {
		t.Fatalf("Put: got %v", err)
	}
	if _, _, err := f.c.Get(ctx, ""); !errors.Is(err, cluster.ErrEmptyKey) {
		t.Fatalf("Get: got %v", err)
	}
	if err := f.c.Delete(ctx, ""); !errors.Is(err, cluster.ErrEmptyKey) {
		t.Fatalf("Delete: got %v", err)
	}
}

// A failure while scanning happens before any copy or commit, so nothing changes.
func TestDeadSourceAbortsBeforeCommit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b", "c")
	f.add(t, "a", "b")
	f.fill(t, 200)
	saves := f.store.Saves

	f.nodes["b"].Down = true
	if _, err := f.c.AddNode(ctx, "c"); !errors.Is(err, clustertest.ErrDown) {
		t.Fatalf("got %v, want ErrDown", err)
	}
	if f.store.Saves != saves || f.nodes["c"].Store.Len() != 0 {
		t.Fatal("membership or data changed after a failed scan")
	}
}

func TestEmptyCluster(t *testing.T) {
	f := newFixture(t)
	if _, err := f.c.Put(context.Background(), "k", "v"); !errors.Is(err, cluster.ErrEmpty) {
		t.Fatalf("got %v, want ErrEmpty", err)
	}
}

// PlanMoves is pure, so it can be tested with plain maps.
func TestPlanMovesIsPure(t *testing.T) {
	one, _ := cluster.NewMembership([]cluster.Op{{Kind: cluster.OpAdd, Node: "a"}})
	two, _ := one.Apply(cluster.Op{Kind: cluster.OpAdd, Node: "b"})

	contents := map[string]map[string]string{"a": {}}
	for i := range 1000 {
		contents["a"][fmt.Sprint(i)] = "v"
	}
	plan, err := cluster.PlanMoves(contents, one, two)
	if err != nil {
		t.Fatal(err)
	}
	moves := plan.Moves
	for _, m := range moves {
		if m.From != "a" || m.To != "b" {
			t.Fatalf("unexpected move %+v", m)
		}
	}
	if len(moves) < 400 || len(moves) > 600 {
		t.Fatalf("moved %d of 1000, want about half", len(moves))
	}
	if len(one.Nodes()) != 1 {
		t.Fatal("Apply mutated the original membership")
	}
}

// A key on the wrong node (e.g. left by a failed clean-up) shows up in
// Stats as misplaced. Reads go to the owner, which has no copy, so the key
// doesn't exist; the next membership change deletes the stray copy rather
// than "repairing" it, which could resurrect a deleted key.
func TestMisplacedKeyIsReportedAndDropped(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b", "c")
	f.add(t, "a", "b")

	key := keyOwnedBy(t, f.c, "a")
	f.nodes["b"].Store.Put(key, "v") // plant it on the wrong node
	misplaced := 0
	for _, s := range f.c.Stats(ctx) {
		misplaced += s.Misplaced
	}
	if misplaced != 1 {
		t.Fatalf("Stats reported %d misplaced keys, want 1", misplaced)
	}
	if _, ok, _ := f.c.Get(ctx, key); ok {
		t.Fatal("a read must only see the owner's copy")
	}

	f.add(t, "c")
	if _, ok, _ := f.c.Get(ctx, key); ok {
		t.Fatal("the stray copy was copied onto the owner")
	}
	f.assertHealthy(t, 0)
}

// The minimal-movement invariant must reject a plan that moves a
// well-placed key between two old nodes during an add.
func TestMinimalMovementInvariantCatchesBadPlan(t *testing.T) {
	f := newFixture(t, "a", "b")
	f.add(t, "a", "b")
	key := keyOwnedBy(t, f.c, "a")

	requirePanic(t, "moved well-placed", func() {
		cluster.CheckMinimalMovement(cluster.Op{Kind: cluster.OpAdd, Node: "c"}, []cluster.Move{{Key: key, From: "a", To: "b"}})
	})
}

// requirePanic runs f and fails unless it panics with an assertion message
// containing want. It skips the test when assertions are compiled out.
func requirePanic(t *testing.T, want string, f func()) {
	t.Helper()
	if !assert.Enabled {
		t.Skip("assertions disabled (noassert)")
	}
	defer func() {
		t.Helper()
		p := recover()
		if msg, _ := p.(string); !strings.Contains(msg, "assertion failed: ") || !strings.Contains(msg, want) {
			t.Fatalf("want an assertion failure mentioning %q, recovered %v", want, p)
		}
	}()
	f()
}

// findKey returns the first key "key-0", "key-1", ... that matches.
func findKey(t *testing.T, match func(key string) bool) string {
	t.Helper()
	for i := range 10000 {
		if k := fmt.Sprintf("key-%d", i); match(k) {
			return k
		}
	}
	t.Fatal("no key matches")
	return ""
}

func keyOwnedBy(t *testing.T, c *cluster.Cluster, node string) string {
	t.Helper()
	return findKey(t, func(k string) bool { o, _ := c.Owner(k); return o == node })
}
