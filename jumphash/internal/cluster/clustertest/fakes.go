// Package clustertest has in-memory fakes for the cluster interfaces, plus
// contract suites that every implementation must pass, both the real
// adapters and these fakes.
package clustertest

import (
	"context"
	"errors"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
)

// ErrDown is returned by a MemNode with Down set.
var ErrDown = errors.New("node is down")

// MemNode is an in-memory cluster.Node backed by a kv.Store. Set Down to
// simulate a crashed node, or FailPuts / FailDeletes to make only that write
// fail (e.g. a node that goes away during a rebalance's copy or clean-up phase).
type MemNode struct {
	Store       *kv.Store
	Down        bool
	FailPuts    bool
	FailDeletes bool
}

func NewMemNode() *MemNode { return &MemNode{Store: kv.NewStore()} }

func (n *MemNode) Health(context.Context) error {
	if n.Down {
		return ErrDown
	}
	return nil
}

func (n *MemNode) Get(_ context.Context, k string) (string, bool, error) {
	if n.Down {
		return "", false, ErrDown
	}
	v, ok := n.Store.Get(k)
	return v, ok, nil
}

func (n *MemNode) Put(_ context.Context, k, v string) error {
	if n.Down || n.FailPuts {
		return ErrDown
	}
	n.Store.Put(k, v)
	return nil
}

func (n *MemNode) Delete(_ context.Context, k string) error {
	if n.Down || n.FailDeletes {
		return ErrDown
	}
	n.Store.Delete(k)
	return nil
}

func (n *MemNode) All(context.Context) (map[string]string, error) {
	if n.Down {
		return nil, ErrDown
	}
	return n.Store.Snapshot(), nil
}

// MemStore keeps membership in memory and counts successful saves. Set
// LoadErr or SaveErr to make that call fail.
type MemStore struct {
	m       cluster.Membership
	Saves   int
	LoadErr error
	SaveErr error
}

func (s *MemStore) Load() (cluster.Membership, error) {
	if s.LoadErr != nil {
		return cluster.Membership{}, s.LoadErr
	}
	if !s.m.Valid() {
		return cluster.NewMembership(nil) // nothing saved yet
	}
	return s.m, nil
}

func (s *MemStore) Save(m cluster.Membership) error {
	if s.SaveErr != nil {
		return s.SaveErr
	}
	s.m = m
	s.Saves++
	return nil
}
