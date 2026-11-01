package clustertest

import (
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
)

// The fakes must honor the same contracts as the real adapters. Otherwise
// tests that pass with fakes would prove nothing about the real system.

func TestMemNodeContract(t *testing.T) {
	NodeContract(t, func(*testing.T) cluster.Node { return NewMemNode() })
}

func TestMemStoreContract(t *testing.T) {
	StoreContract(t, func(*testing.T) cluster.MembershipStore { return &MemStore{} })
}
