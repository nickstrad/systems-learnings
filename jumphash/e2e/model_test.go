//go:build e2e

package e2e

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// 10. A model-checked workload. A fixed-seed sequence of puts, overwrites,
// deletes, and membership changes runs against real processes. A plain
// map records what the cluster should hold, and the cluster is checked
// against it as the test goes.
//
// It checks the same rule as the domain's FuzzClusterModel (no lost
// writes, no stale reads, no resurrected deletes), but through real HTTP,
// key escaping, and the membership file. It is deliberately not random:
// the seed is fixed, so it runs the same steps every time. The domain fuzz
// test does the random exploring.
func TestWorkloadMatchesModel(t *testing.T) {
	const (
		seed  = 20261101
		steps = 150
	)
	e := newEnv(t)
	pool := e.startNodes(5)
	w := &workload{e: e, model: map[string]string{}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("steps so far:\n  %s", strings.Join(w.trace, "\n  "))
		}
	})
	w.add(pool[0])
	w.add(pool[1])

	// Keys that need escaping, or that exercise the router.
	keys := []string{"user:1", "user:2", "with/slash", "two//slashes", "sp ace", "100%", "%2F", "ünï", "?q=1#f", "a+b", "dots..", "-dash"}
	for i := range 8 {
		keys = append(keys, fmt.Sprintf("k%d", i))
	}

	rng := rand.New(rand.NewPCG(seed, seed))
	for step := range steps {
		key := keys[rng.IntN(len(keys))]
		switch r := rng.IntN(100); {
		case r < 45:
			value := fmt.Sprintf("v%d", step)
			if rng.IntN(10) == 0 {
				value = "" // empty values are allowed
			}
			w.put(key, value)
		case r < 65:
			w.del(key)
		case r < 75:
			if spare := w.spare(pool); spare != nil {
				w.add(spare)
			}
		case r < 85:
			if len(w.members) > 2 {
				w.remove(w.members[rng.IntN(len(w.members))])
			}
		case r < 88:
			w.addDeadNode()
		default:
			w.get(key)
		}
	}
	w.checkAll(keys)
	t.Logf("ran %d steps; final membership %d nodes, %d live keys", steps, len(w.members), len(w.model))
}

// workload drives the cluster through kvctl and keeps the model in step.
type workload struct {
	e       *env
	model   map[string]string // what the cluster should hold
	members []*node
	trace   []string // steps so far, logged if the test fails
}

func (w *workload) log(format string, args ...any) {
	w.trace = append(w.trace, fmt.Sprintf(format, args...))
}

func (w *workload) fail(format string, args ...any) {
	w.e.t.Helper()
	w.e.t.Fatalf(format, args...)
}

func (w *workload) put(key, value string) {
	w.log("put %q=%q", key, value)
	if out, err := w.e.tryKvctl("put", key, value); err != nil {
		w.fail("put %q: %v\n%s", key, err, out)
	}
	w.model[key] = value
	w.get(key)
}

func (w *workload) del(key string) {
	w.log("del %q", key)
	if out, err := w.e.tryKvctl("del", key); err != nil {
		w.fail("del %q: %v\n%s", key, err, out)
	}
	delete(w.model, key)
	w.get(key)
}

// get reads key through kvctl and compares it with the model.
func (w *workload) get(key string) {
	w.e.t.Helper()
	out, err := w.e.tryKvctl("get", key)
	want, exists := w.model[key]
	switch {
	case !exists && err == nil:
		w.fail("get %q = %q, but the key was deleted or never written", key, out)
	case !exists && !strings.Contains(out, "not found"):
		w.fail("get %q: want not found, got %q", key, out)
	case exists && err != nil:
		w.fail("get %q: %v\n%s", key, err, out)
	case exists && strings.TrimSuffix(out, "\n") != want:
		w.fail("get %q = %q, want %q", key, strings.TrimSuffix(out, "\n"), want)
	}
}

func (w *workload) add(n *node) {
	w.log("add-node %s", n.URL)
	w.e.addNode(n)
	w.members = append(w.members, n)
	w.checkPlacement()
}

func (w *workload) remove(n *node) {
	w.log("remove-node %s", n.URL)
	w.e.removeNode(n)
	w.members = slices.DeleteFunc(w.members, func(m *node) bool { return m == n })
	if left := n.Contents(w.e.t); len(left) != 0 {
		w.fail("removed node %s still holds %d keys", n.URL, len(left))
	}
	w.checkPlacement()
}

// addDeadNode tries to add a node that isn't running. It must fail and
// change nothing.
func (w *workload) addDeadNode() {
	w.log("add-node <dead>")
	if out, err := w.e.tryKvctl("add-node", "http://127.0.0.1:1"); err == nil {
		w.fail("adding a dead node succeeded: %q", out)
	}
	w.checkPlacement()
}

func (w *workload) spare(pool []*node) *node {
	for _, n := range pool {
		if !slices.Contains(w.members, n) {
			return n
		}
	}
	return nil
}

// checkPlacement asks every member node directly what it holds. That's the
// ground truth, independent of kvctl's routing. Every live key must be on
// exactly one member, with the model's value, and nothing else may be
// stored anywhere.
func (w *workload) checkPlacement() {
	w.e.t.Helper()
	have := holdings(w.e.t, w.members...)
	for k, h := range have {
		if want, ok := w.model[k]; !ok {
			w.fail("node %s holds %q=%q, but the model says it was deleted", h.node, k, h.value)
		} else if h.value != want {
			w.fail("node %s holds %q=%q, want %q", h.node, k, h.value, want)
		}
	}
	for k, v := range w.model {
		if _, ok := have[k]; !ok {
			w.fail("key %q=%q is missing from every node", k, v)
		}
	}
}

// checkAll checks placement and reads every key through kvctl, including
// keys that should be absent.
func (w *workload) checkAll(keys []string) {
	w.checkPlacement()
	for _, k := range keys {
		w.get(k)
	}
}
