package clustertest

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
)

// NodeContract checks that an implementation honors the cluster.Node
// contract (see cluster/contract.go). newNode must return a fresh, empty,
// healthy node each time it is called.
func NodeContract(t *testing.T, newNode func(t *testing.T) cluster.Node) {
	ctx := context.Background()

	t.Run("healthy", func(t *testing.T) {
		if err := newNode(t).Health(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("get missing returns empty, not ok, no error", func(t *testing.T) {
		v, ok, err := newNode(t).Get(ctx, "missing")
		if v != "" || ok || err != nil {
			t.Fatalf("got (%q, %v, %v)", v, ok, err)
		}
	})

	t.Run("put then get", func(t *testing.T) {
		n := newNode(t)
		mustPut(t, n, "k", "v1")
		mustGet(t, n, "k", "v1")
		mustPut(t, n, "k", "v2") // overwrite
		mustGet(t, n, "k", "v2")
	})

	t.Run("empty value is stored", func(t *testing.T) {
		n := newNode(t)
		mustPut(t, n, "k", "")
		mustGet(t, n, "k", "")
	})

	t.Run("keys round-trip unchanged", func(t *testing.T) {
		n := newNode(t)
		for _, k := range []string{"plain", "with/slash", "with space", "pct%25", "100%", "ünï", "?q=1#f"} {
			mustPut(t, n, k, "v-"+k)
			mustGet(t, n, k, "v-"+k)
		}
	})

	t.Run("delete is idempotent", func(t *testing.T) {
		n := newNode(t)
		mustPut(t, n, "k", "v")
		for range 2 {
			if err := n.Delete(ctx, "k"); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok, _ := n.Get(ctx, "k"); ok {
			t.Fatal("key present after delete")
		}
	})

	t.Run("all on empty node is an empty, non-nil map", func(t *testing.T) {
		m, err := newNode(t).All(ctx)
		if err != nil || m == nil || len(m) != 0 {
			t.Fatalf("got (%v, %v)", m, err)
		}
	})

	t.Run("all returns a copy of every entry", func(t *testing.T) {
		n := newNode(t)
		want := map[string]string{"a": "1", "b": "2", "c": ""}
		for k, v := range want {
			mustPut(t, n, k, v)
		}
		got, err := n.All(ctx)
		if err != nil || !maps.Equal(got, want) {
			t.Fatalf("got (%v, %v), want %v", got, err, want)
		}
		got["a"] = "changed"
		mustGet(t, n, "a", "1")
	})
}

// StoreContract checks that an implementation honors the
// cluster.MembershipStore contract. newStore must return a fresh store with
// nothing saved.
func StoreContract(t *testing.T, newStore func(t *testing.T) cluster.MembershipStore) {
	t.Run("load before any save is empty and valid", func(t *testing.T) {
		m, err := newStore(t).Load()
		if err != nil || !m.Valid() || len(m.Nodes()) != 0 {
			t.Fatalf("got (%v, valid=%v, %v)", m.Nodes(), m.Valid(), err)
		}
	})

	t.Run("save then load returns the same history and ring", func(t *testing.T) {
		s := newStore(t)
		m, _ := cluster.NewMembership([]cluster.Op{
			{Kind: cluster.OpAdd, Node: "a"},
			{Kind: cluster.OpAdd, Node: "b"},
			{Kind: cluster.OpRemove, Node: "a"},
			{Kind: cluster.OpAdd, Node: "c"}, // fills a's hole
		})
		if err := s.Save(m); err != nil {
			t.Fatal(err)
		}
		got, err := s.Load()
		if err != nil || !got.Valid() {
			t.Fatalf("load: valid=%v err=%v", got.Valid(), err)
		}
		if !slices.Equal(got.History(), m.History()) || !slices.Equal(got.Nodes(), m.Nodes()) {
			t.Fatalf("got %v, want %v", got.Nodes(), m.Nodes())
		}
	})

	t.Run("last save wins", func(t *testing.T) {
		s := newStore(t)
		one, _ := cluster.NewMembership([]cluster.Op{{Kind: cluster.OpAdd, Node: "a"}})
		two, _ := one.Apply(cluster.Op{Kind: cluster.OpAdd, Node: "b"})
		s.Save(one)
		s.Save(two)
		got, _ := s.Load()
		if !slices.Equal(got.Nodes(), []string{"a", "b"}) {
			t.Fatalf("got %v", got.Nodes())
		}
	})
}

func mustPut(t *testing.T, n cluster.Node, k, v string) {
	t.Helper()
	if err := n.Put(context.Background(), k, v); err != nil {
		t.Fatalf("put %q: %v", k, err)
	}
}

func mustGet(t *testing.T, n cluster.Node, k, want string) {
	t.Helper()
	v, ok, err := n.Get(context.Background(), k)
	if err != nil || !ok || v != want {
		t.Fatalf("get %q: got (%q, %v, %v), want %q", k, v, ok, err, want)
	}
}
