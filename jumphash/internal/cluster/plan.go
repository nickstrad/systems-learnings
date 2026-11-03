package cluster

import (
	"errors"
	"maps"
	"slices"

	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Move is one key that changes owner: copy Value to To, then delete it
// from From.
type Move struct {
	Key, From, To string
	Value         string
}

// Drop is a stale copy of a key: delete it, and never copy it anywhere.
type Drop struct {
	Key, Node string
}

// Plan is what a membership change must do to the data.
type Plan struct {
	Moves []Move // authoritative copies that go to a new owner
	// Clears are stale copies sitting on the key's owner under next. They
	// must be deleted before the commit: once routing switches, reads
	// would see them.
	Clears []Drop
	Drops  []Drop // other stale copies; reads never see them, so they are deleted after the commit
}

var errLastNode = errors.New("last node holds keys")

// PlanMoves decides what must happen to the data for the cluster to match
// next. contents maps each node to the entries it holds now. It is a pure
// function: no I/O, so it's easy to test and to reason about.
//
// Reads go only to a key's owner, so the owner under current is the single
// source of truth: its copy is the authoritative one, and if it has no copy
// the key does not exist. A copy anywhere else is stale. Failed clean-ups,
// copy phases that failed part-way, and a node that left with data and came
// back all leave such copies, and any of them may be older than the
// owner's value or may belong to a key that was deleted since.
//
// So the authoritative copy moves to the key's owner under next, if that's
// a different node, and every other copy is deleted. A key with no
// authoritative copy has every copy deleted: putting one on its new owner
// would resurrect a deleted key or return an old value (see
// TestDeletedKeyStaysDeletedAfterFailedCleanUp).
//
// After the moves and the clears, the owner under current and the owner
// under next both return what a read returns now, so committing changes no
// read. The drops never touch a key's owner under next.
//
// The jump hash properties still hold: an add moves keys only onto the new
// node, and a remove moves only the removed node's keys.
//
//	pre:  current.Valid() && next.Valid()
//	post: err == nil ⇒
//	      - every move goes from the key's owner under current, which holds
//	        it, to its owner under next, a different node; at most one per key
//	      - every clear is on the key's owner under next, which has no move in
//	        for the key; no drop is on it
//	      - no clear or drop deletes an authoritative copy
//	      err != nil ⇒ next has no nodes while some key has an
//	      authoritative copy
func PlanMoves(contents map[string]map[string]string, current, next Membership) (Plan, error) {
	assert.That(current.Valid() && next.Valid(), "PlanMoves pre: invalid membership")

	// Invert node -> entries into key -> node -> value.
	copies := map[string]map[string]string{}
	for node, entries := range contents {
		for k, v := range entries {
			if copies[k] == nil {
				copies[k] = map[string]string{}
			}
			copies[k][node] = v
		}
	}

	var p Plan
	for _, key := range slices.Sorted(maps.Keys(copies)) {
		holders := copies[key]
		auth, _ := current.Owner(key)
		value, live := holders[auth] // false if current has no nodes, too
		dst, hasDst := next.Owner(key)
		if live && !hasDst {
			return Plan{}, wrap(errLastNode, "cannot remove the last node while it still holds keys; add another node first")
		}

		if live && auth != dst {
			p.Moves = append(p.Moves, Move{Key: key, From: auth, To: dst, Value: value})
		}
		for _, n := range slices.Sorted(maps.Keys(holders)) {
			switch {
			case live && (n == auth || n == dst):
				// The authoritative copy is deleted by its move, if it has
				// one, and dst is about to be overwritten with it.
			case n == dst:
				p.Clears = append(p.Clears, Drop{Key: key, Node: n})
			default:
				p.Drops = append(p.Drops, Drop{Key: key, Node: n})
			}
		}
	}

	if assert.Enabled {
		checkPlan(p, copies, current, next)
	}
	return p, nil
}

// checkPlan asserts PlanMoves' postconditions.
func checkPlan(p Plan, copies map[string]map[string]string, current, next Membership) {
	authoritative := func(key, node string) bool {
		owner, _ := current.Owner(key)
		_, held := copies[key][node]
		return held && node == owner
	}
	moved := map[string]bool{}
	for _, m := range p.Moves {
		owner, _ := next.Owner(m.Key)
		assert.That(!moved[m.Key] && authoritative(m.Key, m.From) && m.To == owner && m.From != m.To,
			"PlanMoves post: move %q %s -> %s is a duplicate, not from the authoritative copy, or not to the owner %s", m.Key, m.From, m.To, owner)
		moved[m.Key] = true
	}
	for _, d := range p.Clears {
		dst, _ := next.Owner(d.Key)
		assert.That(d.Node == dst && !moved[d.Key] && !authoritative(d.Key, d.Node),
			"PlanMoves post: clear of %q on %s is not a stale copy on its next owner %s", d.Key, d.Node, dst)
	}
	for _, d := range p.Drops {
		dst, _ := next.Owner(d.Key)
		assert.That(d.Node != dst && !authoritative(d.Key, d.Node),
			"PlanMoves post: drop of %q on %s would delete live data", d.Key, d.Node)
	}
}
