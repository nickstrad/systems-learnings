package cluster

import (
	"context"

	"github.com/nickstrad/systems-learnings/shared/assert"
)

// This file defines the contracts at the domain's two integration points.
// Each interface method lists its preconditions (what the caller, i.e. this
// package, promises) and postconditions (what the implementation promises).
//
// The contracts are enforced in two places:
//   - At runtime: Open wraps every Node and the MembershipStore in a
//     checker that asserts both sides on every call.
//   - In tests: package clustertest has one contract suite per interface,
//     which every implementation must pass, both the real adapters and the
//     fakes. This is the Liskov substitution idea: any implementation must
//     be usable wherever the interface is expected.
//
// A precondition failure is a bug in this package. A postcondition failure
// is a bug in the adapter. Network and disk failures are not contract
// violations; they come back as errors.

// Node is one kv node as the cluster sees it. kvhttp.Client implements it
// over HTTP.
type Node interface {
	// Health returns nil if the node can serve requests.
	Health(ctx context.Context) error

	// Get returns the value stored under key.
	//   pre:  key != ""
	//   post: err != nil  ⇒ value == "" && !ok
	//         !ok         ⇒ value == ""   (missing key is not an error)
	Get(ctx context.Context, key string) (value string, ok bool, err error)

	// Put stores value under key, replacing any previous value. Empty
	// values are allowed.
	//   pre:  key != ""
	//   post: err == nil ⇒ a later Get returns (value, true)
	Put(ctx context.Context, key, value string) error

	// Delete removes key.
	//   pre:  key != ""
	//   post: err == nil ⇒ a later Get returns ok == false
	//         deleting a missing key succeeds (idempotent)
	Delete(ctx context.Context, key string) error

	// All returns every entry on the node.
	//   post: err == nil ⇒ entries != nil, and every key is non-empty
	//         the map is a copy: changing it doesn't change the node
	All(ctx context.Context) (entries map[string]string, err error)
}

// Dialer returns the Node at addr.
//
//	pre:  addr != ""
//	post: result != nil
type Dialer func(addr string) Node

// MembershipStore saves membership between runs. memberfile implements it
// with a JSON file.
type MembershipStore interface {
	// Load returns the last saved membership, or an empty one if nothing
	// was ever saved.
	//   post: err == nil ⇒ m.Valid()
	Load() (m Membership, err error)

	// Save stores m, replacing what was there.
	//   pre:  m.Valid()
	//   post: err == nil ⇒ Load returns a membership with the same History
	Save(m Membership) error
}

// checkedNode asserts the Node contract around a real implementation.
type checkedNode struct {
	addr  string
	inner Node
}

func (n checkedNode) Health(ctx context.Context) error { return n.inner.Health(ctx) }

func (n checkedNode) Get(ctx context.Context, key string) (string, bool, error) {
	assert.That(key != "", "Node.Get pre: empty key")
	v, ok, err := n.inner.Get(ctx, key)
	assert.That(err == nil || (v == "" && !ok), "Node.Get post (%s): value or ok set alongside an error", n.addr)
	assert.That(ok || v == "", "Node.Get post (%s): value returned for a missing key", n.addr)
	return v, ok, err
}

func (n checkedNode) Put(ctx context.Context, key, value string) error {
	assert.That(key != "", "Node.Put pre: empty key")
	return n.inner.Put(ctx, key, value)
}

func (n checkedNode) Delete(ctx context.Context, key string) error {
	assert.That(key != "", "Node.Delete pre: empty key")
	return n.inner.Delete(ctx, key)
}

func (n checkedNode) All(ctx context.Context) (map[string]string, error) {
	entries, err := n.inner.All(ctx)
	if err == nil && assert.Enabled {
		assert.That(entries != nil, "Node.All post (%s): nil map without an error", n.addr)
		_, hasEmpty := entries[""]
		assert.That(!hasEmpty, "Node.All post (%s): empty key returned", n.addr)
	}
	return entries, err
}

// checkedStore asserts the MembershipStore contract.
type checkedStore struct{ inner MembershipStore }

func (s checkedStore) Load() (Membership, error) {
	m, err := s.inner.Load()
	assert.That(err != nil || m.Valid(), "MembershipStore.Load post: invalid membership without an error")
	return m, err
}

func (s checkedStore) Save(m Membership) error {
	assert.That(m.Valid(), "MembershipStore.Save pre: invalid membership")
	return s.inner.Save(m)
}

// checkedDialer wraps every Node that dial returns.
func checkedDialer(dial Dialer) Dialer {
	return func(addr string) Node {
		assert.That(addr != "", "Dialer pre: empty addr")
		n := dial(addr)
		assert.That(n != nil, "Dialer post: nil Node for %s", addr)
		return checkedNode{addr: addr, inner: n}
	}
}
