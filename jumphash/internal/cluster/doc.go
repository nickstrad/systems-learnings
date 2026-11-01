// Package cluster is the domain logic for turning independent kv nodes into
// one sharded store: who owns each key, which keys move when membership
// changes, and the order to move them in.
//
// It does no I/O itself. It reaches nodes through the Node interface and
// saves membership through MembershipStore. kvhttp and memberfile provide
// the real implementations, and tests use in-memory fakes.
package cluster
