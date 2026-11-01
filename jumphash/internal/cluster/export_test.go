package cluster

// Exposes internals to the external cluster_test package (the usual Go
// pattern: this file is compiled only during tests).

var CheckMinimalMovement = checkMinimalMovement

// The checked wrappers, so tests can drive them directly.
var (
	CheckedDialer = checkedDialer
	CheckedStore  = func(s MembershipStore) MembershipStore { return checkedStore{inner: s} }
	CheckedNode   = func(addr string, n Node) Node { return checkedNode{addr: addr, inner: n} }
)
