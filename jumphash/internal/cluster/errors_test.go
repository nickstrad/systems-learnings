package cluster_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

// The module-boundary rule: every error from a public cluster method is a
// well-formed cluster error whose message stands on its own, and sentinels
// still work with errors.Is.
func TestPublicErrorsAreWellFormed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "a", "b", "c")
	f.add(t, "a", "b")
	f.fill(t, 50)

	empty := newFixture(t)
	cases := []struct {
		name     string
		err      error
		sentinel error
		mention  string
	}{
		{"empty cluster", func() error { _, err := empty.c.Put(ctx, "k", "v"); return err }(), cluster.ErrEmpty, "no nodes"},
		{"empty key", func() error { _, err := f.c.Put(ctx, "", "v"); return err }(), cluster.ErrEmptyKey, "must not be empty"},
		{"duplicate add", func() error { _, err := f.c.AddNode(ctx, "a"); return err }(), jumphash.ErrNodeExists, "already in the cluster"},
		{"unknown remove", func() error { _, err := f.c.RemoveNode(ctx, "zzz"); return err }(), jumphash.ErrNodeNotFound, "not in the cluster"},
		{"empty node name", func() error { _, err := f.c.AddNode(ctx, ""); return err }(), jumphash.ErrEmptyName, "must not be empty"},
		{"new node down", func() error {
			f.nodes["c"].Down = true
			defer func() { f.nodes["c"].Down = false }()
			_, err := f.c.AddNode(ctx, "c")
			return err
		}(), clustertest.ErrDown, "nothing changed"},
		{"source node down", func() error {
			f.nodes["b"].Down = true
			defer func() { f.nodes["b"].Down = false }()
			_, err := f.c.AddNode(ctx, "c")
			return err
		}(), clustertest.ErrDown, "nothing changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := errs.Outermost(tc.err)
			if !ok || e.Module != "cluster" {
				t.Fatalf("not a well-formed cluster error: %s", errs.Describe(tc.err))
			}
			if !errors.Is(tc.err, tc.sentinel) {
				t.Fatalf("errors.Is lost the cause: %s", errs.Describe(tc.err))
			}
			if !strings.Contains(e.Message, tc.mention) {
				t.Fatalf("message %q should mention %q", e.Message, tc.mention)
			}
		})
	}
}

// The concurrent scan reports each node's error with its result: one down
// node doesn't hide the others.
func TestStatsReportsEachNodeIndependently(t *testing.T) {
	f := newFixture(t, "a", "b", "c")
	f.add(t, "a", "b", "c")
	f.fill(t, 300)
	f.nodes["b"].Down = true

	stats := f.c.Stats(context.Background())
	if len(stats) != 3 {
		t.Fatalf("got %d stats", len(stats))
	}
	for _, s := range stats {
		down := s.Node == "b"
		if (s.Unreachable != nil) != down {
			t.Fatalf("%s: unreachable=%v", s.Node, s.Unreachable)
		}
		if !down && s.Keys == 0 {
			t.Fatalf("%s: live node reported no keys", s.Node)
		}
	}
	requireClusterError(t, stats[1].Unreachable, clustertest.ErrDown, "did not answer")
}

// panicNode panics in All once armed, standing in for a bug inside a scan
// goroutine. It starts disarmed, because joining the cluster scans it too.
type panicNode struct {
	*clustertest.MemNode
	armed bool
}

func (n *panicNode) All(ctx context.Context) (map[string]string, error) {
	if n.armed {
		panic("boom in goroutine")
	}
	return n.MemNode.All(ctx)
}

// A panic in a scan goroutine must reach the caller's goroutine, where the
// top level can recover it, instead of crashing the process.
func TestScanPanicReachesCaller(t *testing.T) {
	b := &panicNode{MemNode: clustertest.NewMemNode()}
	nodes := map[string]cluster.Node{"a": clustertest.NewMemNode(), "b": b}
	c, _ := cluster.Open(&clustertest.MemStore{}, func(addr string) cluster.Node { return nodes[addr] })
	c.AddNode(context.Background(), "a")
	c.AddNode(context.Background(), "b")
	b.armed = true

	defer func() {
		p := recover()
		if p == nil || !strings.Contains(p.(string), "boom in goroutine") {
			t.Fatalf("recovered %v", p)
		}
	}()
	c.Stats(context.Background())
}

// When one node fails, rebalance cancels the other scans but still reads
// every result, so a panic (a bug) in another goroutine is never lost
// behind the first error.
func TestRebalanceNeverDropsAPanicBehindAnError(t *testing.T) {
	down := clustertest.NewMemNode()
	b := &panicNode{MemNode: clustertest.NewMemNode()}
	nodes := map[string]cluster.Node{"a": down, "b": b, "c": clustertest.NewMemNode()}
	c, _ := cluster.Open(&clustertest.MemStore{}, func(addr string) cluster.Node { return nodes[addr] })
	c.AddNode(context.Background(), "a")
	c.AddNode(context.Background(), "b")
	b.armed = true // only b's scan panics
	down.Down = true

	defer func() {
		if p := recover(); p == nil {
			t.Fatal("the panic was dropped behind node a's error")
		}
	}()
	c.AddNode(context.Background(), "c")
}
