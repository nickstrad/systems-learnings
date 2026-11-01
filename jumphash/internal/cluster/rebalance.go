package cluster

import (
	"context"
	"slices"

	"github.com/nickstrad/systems-learnings/jumphash/internal/assert"
)

// Report says what a membership change did.
type Report struct {
	Node    string // the node added or removed
	Scanned int    // copies looked at across all nodes
	Moves   []Move // keys that changed owner
	Drops   []Drop // stale copies deleted (left behind by earlier failures)
}

// AddNode adds a running node to the cluster and moves onto it the keys it
// now owns. With jump hash, every moved key goes to the new node, and about
// 1/N of all keys move.
//
//	pre:  none. node comes from the user, and bad values return errors.
//	post: err == nil ⇒ node is in c.Nodes()
func (c *Cluster) AddNode(ctx context.Context, node string) (Report, error) {
	op := Op{Kind: OpAdd, Node: node}
	next, err := c.members.Apply(op)
	if err != nil {
		return Report{}, err
	}
	if err := c.dial(node).Health(ctx); err != nil {
		return Report{}, wrap(err, "cannot add %s, nothing changed: %v", node, err).With("node", node)
	}
	return c.rebalance(ctx, op, next)
}

// RemoveNode moves every key off node and then drops it from the cluster.
// Only that node's keys move. The node itself keeps running, and it is empty afterwards.
//
//	pre:  none. node comes from the user, and bad values return errors.
//	post: err == nil ⇒ node is not in c.Nodes()
func (c *Cluster) RemoveNode(ctx context.Context, node string) (Report, error) {
	op := Op{Kind: OpRemove, Node: node}
	next, err := c.members.Apply(op)
	if err != nil {
		return Report{}, err
	}
	return c.rebalance(ctx, op, next)
}

// rebalance moves data to match next, in three phases:
//
//  1. Copy: scan every current node and the node being added, plan the
//     moves (PlanMoves), and PUT each moved key's authoritative copy onto
//     its new owner. Then DELETE stale copies that sit on a key's new owner
//     (Plan.Clears), so the commit can't expose them. Only stale copies
//     are deleted, and no current owner is touched, so a failure here
//     changes no read and membership is unchanged.
//  2. Commit: save the new membership. From now on, reads and writes go to
//     the new owners.
//  3. Clean up: DELETE the moved keys from their old nodes, plus any other
//     stale copies (Plan.Drops). None of these is on a key's owner, so a
//     failure here leaves copies that no read sees and the next change
//     deletes.
//
// This is NOT safe under concurrent writes: a write that reaches an old
// owner between phase 1 and phase 2 is lost in phase 3. Real systems fix
// this with things like dual writes during migration, versioned values, or
// a coordinator that fences the old owner. For a learning project, stop
// writes while you rebalance.
//
// The scan runs one goroutine per node (see scan). The first node that
// fails cancels the rest, and nothing changes. Each move is one request,
// and the moves run one after another. That's the bottleneck on a real
// network. A real system would batch them per destination.
//
// Errors: every returned error is a cluster error whose message says
// whether anything changed and whether it is safe to retry.
//
//	pre:  next == c.members.Apply(op)
//	post: membership is committed only if every copy and clear succeeded.
//	      On success, c.members == next, and every moved key's new owner is its To node.
//	      The minimal-movement invariant holds (see checkMinimalMovement).
func (c *Cluster) rebalance(ctx context.Context, op Op, next Membership) (Report, error) {
	rep := Report{Node: op.Node}

	// Scan the nodes that hold data right now, including the node being
	// removed. Also scan a node being added: it may hold stale copies from
	// before it left the cluster, or from a copy phase that failed, and
	// those must be cleared before it starts serving reads.
	// The parent's policy (Cox-Buday, chapter 4): the first failure cancels
	// the other scans, but every result is still read. That way a panic in
	// another goroutine is never silently dropped, and nothing is left
	// running when we return.
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	contents := map[string]map[string]string{}
	var firstErr error
	scanned := c.Nodes()
	if op.Kind == OpAdd {
		scanned = append(scanned, op.Node)
	}
	for r := range c.scan(scanCtx, scanned) {
		r.repanic()
		if r.err != nil {
			if firstErr == nil {
				firstErr = wrap(r.err, "cannot read node %s, nothing changed: %v", r.node, r.err).With("node", r.node)
				cancel()
			}
			continue
		}
		contents[r.node] = r.entries
		rep.Scanned += len(r.entries)
	}
	if firstErr != nil {
		return Report{}, firstErr
	}

	plan, err := PlanMoves(contents, c.members, next)
	if err != nil {
		return Report{}, err // already a cluster error
	}
	moves := plan.Moves
	rep.Moves, rep.Drops = plan.Moves, append(slices.Clone(plan.Clears), plan.Drops...)
	assert.That(len(moves) <= rep.Scanned, "rebalance: %d moves from %d scanned keys", len(moves), rep.Scanned)
	if assert.Enabled {
		checkMinimalMovement(op, moves)
	}

	// Phase 1: copy.
	for _, m := range moves {
		if err := c.dial(m.To).Put(ctx, m.Key, m.Value); err != nil {
			return rep, wrap(err, "cannot copy key %q to %s; membership unchanged, safe to retry: %v", m.Key, m.To, err).
				With("key", m.Key).With("from", m.From).With("to", m.To)
		}
	}
	// Still phase 1: delete stale copies on their key's new owner, which no
	// read sees yet but every read would see after the commit.
	for _, d := range plan.Clears {
		if err := c.dial(d.Node).Delete(ctx, d.Key); err != nil {
			return rep, wrap(err, "cannot delete stale copy of %q on %s; membership unchanged, safe to retry: %v", d.Key, d.Node, err).
				With("key", d.Key).With("node", d.Node)
		}
	}

	// Phase 2: commit.
	if err := c.store.Save(next); err != nil {
		return rep, wrap(err, "keys were copied but membership could not be saved; nothing committed, safe to retry: %v", err)
	}
	c.members = next
	if assert.Enabled {
		for _, m := range moves {
			owner, _ := c.members.Owner(m.Key)
			assert.That(owner == m.To, "rebalance post: %q committed to %s but owner is %s", m.Key, m.To, owner)
		}
	}

	// Phase 3: clean up the moved keys' old copies and the other stale
	// copies. A failure here leaves a stale copy behind. Reads never see it
	// (they go to the owner), and the next rebalance drops it instead of
	// copying it.
	stale := plan.Drops
	for _, m := range moves {
		stale = append(stale, Drop{Key: m.Key, Node: m.From})
	}
	for _, d := range stale {
		if err := c.dial(d.Node).Delete(ctx, d.Key); err != nil {
			return rep, wrap(err, "membership changed, but the old copy of %q on %s could not be deleted (data is safe; the next change removes it): %v", d.Key, d.Node, err).
				With("key", d.Key).With("node", d.Node)
		}
	}
	return rep, nil
}

// checkMinimalMovement asserts the system invariant this whole design rests
// on: jump hash moves as few keys as possible. A key that was on its correct
// owner before the change may only move onto an added node, or off a
// removed node. If any other well-placed key moves, the ring or the plan is
// broken.
//
// PlanMoves only moves a key from its owner, so every move is checked.
// Stale copies are never moved at all; PlanMoves deletes them.
func checkMinimalMovement(op Op, moves []Move) {
	for _, m := range moves {
		switch op.Kind {
		case OpAdd:
			assert.That(m.To == op.Node, "rebalance: add %s moved well-placed %q %s -> %s", op.Node, m.Key, m.From, m.To)
		case OpRemove:
			assert.That(m.From == op.Node, "rebalance: remove %s moved well-placed %q %s -> %s", op.Node, m.Key, m.From, m.To)
		}
	}
}
