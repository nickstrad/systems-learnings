package jumphash

import (
	"errors"
	"sync"

	"github.com/nickstrad/systems-learnings/shared/assert"
)

var (
	ErrNodeExists   = errors.New("jumphash: node already exists")
	ErrNodeNotFound = errors.New("jumphash: node not found")
	ErrEmptyName    = errors.New("jumphash: node name must not be empty")
)

// Ring maps keys to named nodes using jump hash and supports adding and
// removing any node, not just the last one.
//
// Plain jump hash only lets you add or remove the last bucket. Ring works
// around that by keeping each removed node's slot as an empty "hole" instead
// of shifting the other nodes down. A key that hashes to a hole is re-hashed
// with a new seed, and this repeats until it lands on a live node.
//
// This keeps key movement minimal:
//   - Remove: only keys on the removed node move. Other keys never touched
//     the hole, so their lookups don't change.
//   - Add: the new node fills the most recently emptied hole, or is appended
//     if there are no holes. Keys move only onto the new node. Removing a node
//     and then adding it back restores the old mapping exactly.
//
// Lookups take about slots/live tries on average, so they slow down if most
// slots are holes. The MementoHash paper handles that case better.
//
// A Ring is safe for concurrent use.
//
// Invariant (checked after every Add and Remove, see checkInvariant):
// every slot is either a live node, recorded in index at that slot, or a
// hole, recorded exactly once in holes. So len(index)+len(holes) == len(slots).
type Ring struct {
	mu    sync.RWMutex
	slots []string       // bucket index -> node name; "" marks a hole
	index map[string]int // node name -> bucket index
	holes []int          // empty slots, reused most-recent first
}

// NewRing returns a Ring holding the given nodes, in order.
func NewRing(nodes ...string) (*Ring, error) {
	r := &Ring{index: make(map[string]int)}
	for _, n := range nodes {
		if err := r.Add(n); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Add adds a node. It returns ErrNodeExists if the node is already present.
func (r *Ring) Add(node string) error {
	if node == "" {
		return ErrEmptyName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if assert.Enabled {
		defer r.checkInvariant() // runs before Unlock: defers are LIFO
	}
	if _, ok := r.index[node]; ok {
		return ErrNodeExists
	}
	if len(r.holes) > 0 {
		i := r.holes[len(r.holes)-1]
		r.holes = r.holes[:len(r.holes)-1]
		r.slots[i] = node
		r.index[node] = i
		return nil
	}
	r.index[node] = len(r.slots)
	r.slots = append(r.slots, node)
	return nil
}

// Remove removes a node. It returns ErrNodeNotFound if the node isn't present.
func (r *Ring) Remove(node string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if assert.Enabled {
		defer r.checkInvariant()
	}
	i, ok := r.index[node]
	if !ok {
		return ErrNodeNotFound
	}
	delete(r.index, node)
	r.slots[i] = ""
	r.holes = append(r.holes, i)
	return nil
}

// Get returns the node for key. It returns false if the ring has no nodes.
// The loop ends because at least one slot is live. The invariant
// guarantees that a non-empty slot is a live node, so no extra check is
// needed on this hot path.
func (r *Ring) Get(key uint64) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.index) == 0 {
		return "", false
	}
	n := int32(len(r.slots))
	for {
		if node := r.slots[Hash(key, n)]; node != "" {
			return node, true
		}
		key = mix64(key) // landed on a hole: try again with a new seed
	}
}

// Len returns the number of live nodes.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.index)
}

// Nodes returns the live nodes in bucket order.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.index))
	for _, s := range r.slots {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// checkInvariant panics if slots, index, and holes disagree. It is O(slots)
// and only runs after membership changes. The caller must hold r.mu.
func (r *Ring) checkInvariant() {
	assert.That(len(r.index)+len(r.holes) == len(r.slots),
		"ring: %d live + %d holes != %d slots", len(r.index), len(r.holes), len(r.slots))
	for name, i := range r.index {
		assert.That(i >= 0 && i < len(r.slots) && r.slots[i] == name,
			"ring: index says %q is in slot %d, but the slot doesn't hold it", name, i)
	}
	seen := make([]bool, len(r.slots))
	for _, h := range r.holes {
		ok := h >= 0 && h < len(r.slots) && r.slots[h] == "" && !seen[h]
		assert.That(ok, "ring: hole %d is invalid or listed twice", h)
		seen[h] = true
	}
}

// mix64 is the SplitMix64 finalizer. It turns a key into a new,
// well-scrambled key for the next lookup attempt.
func mix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}
