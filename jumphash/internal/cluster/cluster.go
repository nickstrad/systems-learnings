package cluster

import (
	"context"
	"errors"

	"github.com/nickstrad/systems-learnings/shared/assert"
	"github.com/nickstrad/systems-learnings/shared/errs"
)

// Sentinels. Public methods return them wrapped in a cluster error, so
// check them with errors.Is.
var (
	ErrEmpty    = errors.New("cluster has no nodes")
	ErrEmptyKey = errors.New("key must not be empty")
)

// Module is this package's name in well-formed errors.
//
// Error handling follows Cox-Buday's error propagation: every error that
// leaves a public method or function of this package is an *errs.Error
// from module "cluster". Its message stands on its own (it includes the
// cause's message), because the CLI shows only the outermost message.
const Module = "cluster"

func wrap(err error, format string, args ...any) *errs.Error {
	return errs.Wrap(Module, err, format, args...)
}

// Cluster routes keys to nodes and changes membership.
//
// Input checks: public methods return errors for bad input from callers,
// such as an empty key. Everything past that point is a contract and is
// asserted.
//
// Invariant: c.members is valid, and it is what c.store last saved or loaded.
type Cluster struct {
	members Membership
	store   MembershipStore
	dial    Dialer
}

// Open loads membership from store. dial is used to reach nodes.
//
//	pre:  store != nil, dial != nil
//	post: err == nil ⇒ the cluster's membership is what store holds
func Open(store MembershipStore, dial Dialer) (*Cluster, error) {
	assert.That(store != nil && dial != nil, "Open pre: nil store or dialer")
	store = checkedStore{inner: store}
	m, err := store.Load()
	if err != nil {
		return nil, wrap(err, "cannot load cluster membership: %v", err)
	}
	return &Cluster{members: m, store: store, dial: checkedDialer(dial)}, nil
}

// Nodes returns the live nodes in ring slot order.
func (c *Cluster) Nodes() []string { return c.members.Nodes() }

// Owner returns the node responsible for key. It is where every key
// operation starts, so a Node never receives an empty key.
//
//	post: err == nil ⇒ the node is one of c.Nodes() (guaranteed by the ring invariant)
func (c *Cluster) Owner(key string) (string, error) {
	if key == "" {
		return "", wrap(ErrEmptyKey, "key must not be empty")
	}
	n, ok := c.members.Owner(key)
	if !ok {
		return "", wrap(ErrEmpty, "the cluster has no nodes; add one first")
	}
	return n, nil
}

// Put writes key to its owner and returns that node.
func (c *Cluster) Put(ctx context.Context, key, value string) (string, error) {
	node, err := c.Owner(key)
	if err != nil {
		return "", err
	}
	if err := c.dial(node).Put(ctx, key, value); err != nil {
		return "", wrap(err, "cannot write key %q: %v", key, err).With("node", node)
	}
	return node, nil
}

func (c *Cluster) Get(ctx context.Context, key string) (value string, ok bool, err error) {
	node, err := c.Owner(key)
	if err != nil {
		return "", false, err
	}
	value, ok, err = c.dial(node).Get(ctx, key)
	if err != nil {
		return "", false, wrap(err, "cannot read key %q: %v", key, err).With("node", node)
	}
	return value, ok, nil
}

func (c *Cluster) Delete(ctx context.Context, key string) error {
	node, err := c.Owner(key)
	if err != nil {
		return err
	}
	if err := c.dial(node).Delete(ctx, key); err != nil {
		return wrap(err, "cannot delete key %q: %v", key, err).With("node", node)
	}
	return nil
}

// NodeStat is what one node holds, and whether its keys are on the right node.
type NodeStat struct {
	Node        string
	Keys        int
	Misplaced   int // keys this node holds but the ring says belong elsewhere
	Unreachable error
}

// Stats asks every node what it holds, all at once (see scan). A node that
// fails is reported as Unreachable, and the others are still reported.
//
//	post: one stat per node, in c.Nodes() order, with 0 <= Misplaced <= Keys
func (c *Cluster) Stats(ctx context.Context) []NodeStat {
	nodes := c.Nodes()
	slot := make(map[string]int, len(nodes))
	out := make([]NodeStat, len(nodes))
	for i, n := range nodes {
		slot[n], out[i] = i, NodeStat{Node: n}
	}
	for r := range c.scan(ctx, nodes) {
		r.repanic()
		s := &out[slot[r.node]]
		if r.err != nil {
			s.Unreachable = wrap(r.err, "node %s did not answer: %v", r.node, r.err)
			continue
		}
		s.Keys = len(r.entries)
		for k := range r.entries {
			if o, _ := c.members.Owner(k); o != r.node {
				s.Misplaced++
			}
		}
		assert.That(s.Misplaced <= s.Keys, "Stats post: %d misplaced of %d keys", s.Misplaced, s.Keys)
	}
	return out
}
