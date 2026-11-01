package cluster_test

import (
	"context"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
)

// An assertion that has never failed proves nothing. These tests drive each
// documented pre- and postcondition of the interfaces (see contract.go)
// through the checked wrappers with an implementation that breaks it, and
// check that the assertion trips. They skip when assertions are compiled out.

// stubNode is a Node whose Get and All results the test chooses.
type stubNode struct {
	cluster.Node
	get func() (string, bool, error)
	all func() (map[string]string, error)
}

func (n stubNode) Get(context.Context, string) (string, bool, error) { return n.get() }
func (n stubNode) All(context.Context) (map[string]string, error)    { return n.all() }

// stubStore is a MembershipStore whose Load result the test chooses.
type stubStore struct {
	cluster.MembershipStore
	load func() (cluster.Membership, error)
}

func (s stubStore) Load() (cluster.Membership, error) { return s.load() }

func TestNodeContractIsAsserted(t *testing.T) {
	ctx := context.Background()
	inner := clustertest.NewMemNode()
	node := cluster.CheckedNode("a", inner)

	t.Run("pre: key must not be empty", func(t *testing.T) {
		requirePanic(t, "Node.Get pre", func() { node.Get(ctx, "") })
		requirePanic(t, "Node.Put pre", func() { node.Put(ctx, "", "v") })
		requirePanic(t, "Node.Delete pre", func() { node.Delete(ctx, "") })
	})

	getCases := []struct {
		name string
		get  func() (string, bool, error)
		want string
	}{
		{"post: a value alongside an error", func() (string, bool, error) { return "v", true, clustertest.ErrDown }, "value or ok set alongside an error"},
		{"post: ok alongside an error", func() (string, bool, error) { return "", true, clustertest.ErrDown }, "value or ok set alongside an error"},
		{"post: a value for a missing key", func() (string, bool, error) { return "v", false, nil }, "value returned for a missing key"},
	}
	for _, tc := range getCases {
		t.Run("Get "+tc.name, func(t *testing.T) {
			bad := cluster.CheckedNode("a", stubNode{get: tc.get})
			requirePanic(t, tc.want, func() { bad.Get(ctx, "k") })
		})
	}

	allCases := []struct {
		name    string
		entries map[string]string
		want    string
	}{
		{"post: nil map without an error", nil, "nil map"},
		{"post: an empty key", map[string]string{"": "v"}, "empty key"},
	}
	for _, tc := range allCases {
		t.Run("All "+tc.name, func(t *testing.T) {
			bad := cluster.CheckedNode("a", stubNode{all: func() (map[string]string, error) { return tc.entries, nil }})
			requirePanic(t, tc.want, func() { bad.All(ctx) })
		})
	}
}

func TestStoreContractIsAsserted(t *testing.T) {
	t.Run("Load post: invalid membership without an error", func(t *testing.T) {
		bad := cluster.CheckedStore(stubStore{load: func() (cluster.Membership, error) { return cluster.Membership{}, nil }})
		requirePanic(t, "MembershipStore.Load post", func() { bad.Load() })
	})
	t.Run("Save pre: invalid membership", func(t *testing.T) {
		store := cluster.CheckedStore(&clustertest.MemStore{})
		requirePanic(t, "MembershipStore.Save pre", func() { store.Save(cluster.Membership{}) })
	})
}

func TestDialerContractIsAsserted(t *testing.T) {
	t.Run("pre: addr must not be empty", func(t *testing.T) {
		dial := cluster.CheckedDialer(func(string) cluster.Node { return clustertest.NewMemNode() })
		requirePanic(t, "Dialer pre", func() { dial("") })
	})
	t.Run("post: Node must not be nil", func(t *testing.T) {
		dial := cluster.CheckedDialer(func(string) cluster.Node { return nil })
		requirePanic(t, "Dialer post", func() { dial("a") })
	})
}

func TestOpenRequiresStoreAndDialer(t *testing.T) {
	dial := func(string) cluster.Node { return nil }
	requirePanic(t, "Open pre", func() { cluster.Open(nil, dial) })
	requirePanic(t, "Open pre", func() { cluster.Open(&clustertest.MemStore{}, nil) })
}
