package cluster

import (
	"errors"
	"fmt"
	"hash/fnv"
	"slices"

	"github.com/nickstrad/systems-learnings/jumphash"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Op is one membership change.
type Op struct {
	Kind string // OpAdd or OpRemove
	Node string
}

const (
	OpAdd    = "add"
	OpRemove = "remove"
)

// Membership is the set of nodes, stored as the history of add/remove ops.
//
// Why history and not a node list? jumphash.Ring's mapping depends on the
// order of changes: removed nodes leave holes that later adds fill.
// Replaying the history rebuilds exactly the same ring.
//
// A Membership is immutable. Apply returns a new one.
//
// Invariant: a Membership built by NewMembership or Apply is Valid, and its
// ring is exactly the replay of its history. The zero value is not Valid,
// but Nodes and Owner treat it as empty.
type Membership struct {
	history []Op
	ring    *jumphash.Ring
}

// NewMembership rebuilds membership from its history.
//
//	pre:  none. history may come from a file, so it is checked and bad
//	      history returns an error.
//	post: err == nil ⇒ Valid, and the node count is adds minus removes
func NewMembership(history []Op) (Membership, error) {
	r, _ := jumphash.NewRing()
	for _, op := range history {
		var err error
		switch op.Kind {
		case OpAdd:
			err = r.Add(op.Node)
		case OpRemove:
			err = r.Remove(op.Node)
		default:
			err = fmt.Errorf("unknown op %q", op.Kind)
		}
		if err != nil {
			return Membership{}, wrap(err, "invalid membership history at %q %q: %v", op.Kind, op.Node, err)
		}
	}
	m := Membership{history: slices.Clone(history), ring: r}
	if assert.Enabled {
		live := 0
		for _, op := range history {
			if op.Kind == OpAdd {
				live++
			} else {
				live--
			}
		}
		assert.That(m.ring.Len() == live, "NewMembership post: ring has %d nodes, history says %d", m.ring.Len(), live)
	}
	return m, nil
}

// Valid reports whether m was built by NewMembership or Apply.
func (m Membership) Valid() bool { return m.ring != nil }

// Apply returns the membership after op, leaving m unchanged.
//
//	pre:  m.Valid()
//	post: err == nil ⇒ next has exactly one more op than m; op.Node is
//	      present after an add and absent after a remove; the node count
//	      changed by exactly one
func (m Membership) Apply(op Op) (next Membership, err error) {
	assert.That(m.Valid(), "Apply pre: invalid membership")
	next, err = NewMembership(append(slices.Clone(m.history), op))
	switch {
	case errors.Is(err, jumphash.ErrNodeExists):
		return Membership{}, wrap(err, "%s is already in the cluster", op.Node)
	case errors.Is(err, jumphash.ErrEmptyName):
		return Membership{}, wrap(err, "node address must not be empty")
	case errors.Is(err, jumphash.ErrNodeNotFound):
		return Membership{}, wrap(err, "%s is not in the cluster", op.Node)
	case err != nil:
		return Membership{}, err
	}
	if assert.Enabled {
		added := op.Kind == OpAdd
		delta := -1
		if added {
			delta = 1
		}
		assert.That(len(next.history) == len(m.history)+1, "Apply post: must append exactly one op")
		assert.That(slices.Contains(next.Nodes(), op.Node) == added,
			"Apply post: after %s %s, node presence is wrong", op.Kind, op.Node)
		assert.That(len(next.Nodes()) == len(m.Nodes())+delta,
			"Apply post: %s changed node count from %d to %d", op.Kind, len(m.Nodes()), len(next.Nodes()))
	}
	return next, nil
}

// History returns a copy of the ops, for saving.
func (m Membership) History() []Op { return slices.Clone(m.history) }

// Nodes returns the live nodes in ring slot order.
func (m Membership) Nodes() []string {
	if m.ring == nil {
		return nil
	}
	return m.ring.Nodes()
}

// Owner returns the node responsible for key. ok is false if there are no nodes.
//
//	post: ok ⇒ node is one of m.Nodes() (guaranteed by the ring invariant)
func (m Membership) Owner(key string) (node string, ok bool) {
	if m.ring == nil {
		return "", false
	}
	return m.ring.Get(KeyHash(key))
}

// KeyHash maps a string key to the uint64 jump hash expects. It must be
// stable across processes, so no hash/maphash (it is randomly seeded).
func KeyHash(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return h.Sum64()
}
