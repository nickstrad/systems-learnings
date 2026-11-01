//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const keys = 2000

// 1. The basics: one node, CRUD through the CLI.
func TestSingleNodeCRUD(t *testing.T) {
	e := newEnv(t)
	n := e.startNode()

	if out, err := e.tryKvctl("put", "a", "1"); err == nil {
		t.Fatalf("put on empty cluster should fail, got %q", out)
	}
	e.addNode(n)

	e.kvctl("put", "greeting", "hello world")
	if got := strings.TrimSpace(e.kvctl("get", "greeting")); got != "hello world" {
		t.Fatalf("get = %q", got)
	}
	if got := strings.TrimSpace(e.kvctl("where", "greeting")); got != n.URL {
		t.Fatalf("where = %q, want %s", got, n.URL)
	}
	e.kvctl("del", "greeting")
	if out, err := e.tryKvctl("get", "greeting"); err == nil {
		t.Fatalf("get after delete should fail, got %q", out)
	}
}

// 2. Scale out. Adding a 4th node to 3 should move about 1/4 of keys, and
// every moved key should go to the new node. No key moves between old nodes.
func TestScaleOutMovesOnlyOntoNewNode(t *testing.T) {
	e := newEnv(t)
	nodes := e.startNodes(4)
	for _, n := range nodes[:3] {
		e.addNode(n)
	}
	e.kvctl("fill", fmt.Sprint(keys))
	before := placement(t, nodes...)

	scanned, moved := e.addNode(nodes[3])
	after := placement(t, nodes...)

	if scanned != keys {
		t.Fatalf("scanned %d, want %d", scanned, keys)
	}
	if !near(moved, keys/4, 0.2) {
		t.Fatalf("moved %d keys, want about %d", moved, keys/4)
	}
	diff := 0
	for k, was := range before {
		if now := after[k]; now != was {
			diff++
			if now != nodes[3].URL {
				t.Fatalf("key %s moved %s -> %s; only moves to the new node are allowed", k, was, now)
			}
		}
	}
	if diff != moved {
		t.Fatalf("CLI reported %d moves, nodes show %d", moved, diff)
	}
	e.assertAllReadable("key", keys)
}

// 3. Scale in. Removing a node moves exactly that node's keys and nothing
// else. Afterwards the removed node is empty.
func TestScaleInMovesOnlyRemovedNodesKeys(t *testing.T) {
	e := newEnv(t)
	nodes := e.startNodes(4)
	for _, n := range nodes {
		e.addNode(n)
	}
	e.kvctl("fill", fmt.Sprint(keys))
	before := placement(t, nodes...)
	victim := nodes[1] // a middle node, so the ring gets a hole
	held := len(victim.Contents(t))

	_, moved := e.removeNode(victim)
	after := placement(t, nodes...)

	if moved != held {
		t.Fatalf("moved %d, but victim held %d", moved, held)
	}
	if n := len(victim.Contents(t)); n != 0 {
		t.Fatalf("removed node still holds %d keys", n)
	}
	for k, was := range before {
		if was != victim.URL && after[k] != was {
			t.Fatalf("key %s moved %s -> %s, but only the removed node's keys should move", k, was, after[k])
		}
	}
	if strings.Contains(e.kvctl("nodes"), victim.URL) {
		t.Fatal("removed node still listed")
	}
	e.assertAllReadable("key", keys)
}

// 4. Removing a node leaves a hole in the jump hash ring. The next add fills
// it, so the new node should receive exactly the removed node's old key set.
func TestReplacementNodeInheritsExactKeySet(t *testing.T) {
	e := newEnv(t)
	nodes := e.startNodes(3)
	for _, n := range nodes {
		e.addNode(n)
	}
	e.kvctl("fill", fmt.Sprint(keys))
	victim := nodes[1]
	victimKeys := slices.Sorted(maps.Keys(victim.Contents(t)))

	e.removeNode(victim)
	replacement := e.startNode()
	_, moved := e.addNode(replacement)

	got := slices.Sorted(maps.Keys(replacement.Contents(t)))
	if !slices.Equal(got, victimKeys) {
		t.Fatalf("replacement got %d keys, victim had %d; key sets differ", len(got), len(victimKeys))
	}
	if moved != len(victimKeys) {
		t.Fatalf("moved %d, want %d", moved, len(victimKeys))
	}
	e.assertAllReadable("key", keys)
}

// 5. The full story: grow from 1 to 5 nodes, then shrink back to 1, checking
// data after every step. The log output doubles as a table for the blog post.
func TestLifecycleGrowThenShrink(t *testing.T) {
	e := newEnv(t)
	nodes := e.startNodes(5)
	e.addNode(nodes[0])
	e.kvctl("fill", fmt.Sprint(keys))

	type row struct {
		step         string
		nodes, moved int
	}
	var table []row

	for i := 1; i < len(nodes); i++ {
		_, moved := e.addNode(nodes[i])
		n := i + 1
		// Going from n-1 to n nodes should move about 1/n of the keys.
		if !near(moved, keys/n, 0.25) {
			t.Fatalf("grow to %d: moved %d, want about %d", n, moved, keys/n)
		}
		table = append(table, row{"add " + label(i), n, moved})
		e.assertAllReadable("key", keys)
	}
	// Remove in a mixed order, not just LIFO, to exercise holes.
	live := len(nodes)
	for _, i := range []int{2, 0, 4, 1} {
		held := len(nodes[i].Contents(t))
		_, moved := e.removeNode(nodes[i])
		if moved != held {
			t.Fatalf("remove %s: moved %d, held %d", label(i), moved, held)
		}
		live--
		table = append(table, row{"remove " + label(i), live, moved})
		e.assertAllReadable("key", keys)
	}
	if got := len(nodes[3].Contents(t)); got != keys {
		t.Fatalf("last node holds %d, want all %d", got, keys)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n%-10s %6s %7s %7s\n", "step", "nodes", "moved", "%")
	for _, r := range table {
		fmt.Fprintf(&b, "%-10s %6d %7d %6.1f%%\n", r.step, r.nodes, r.moved, 100*float64(r.moved)/keys)
	}
	t.Log(b.String())
}

// 6. A node that isn't running can't join, and membership is unchanged.
func TestAddUnreachableNodeIsRejected(t *testing.T) {
	e := newEnv(t)
	n := e.startNode()
	e.addNode(n)

	ghost := e.startNode()
	ghost.Kill()
	if out, err := e.tryKvctl("add-node", ghost.URL); err == nil {
		t.Fatalf("expected failure, got %q", out)
	}
	if strings.Contains(e.kvctl("nodes"), ghost.URL) {
		t.Fatal("dead node was added")
	}
}

// 7. If an existing node is down, add-node fails during the scan, before it
// changes anything. Membership stays the same and the live data is untouched.
func TestRebalanceAbortsWhenSourceNodeIsDown(t *testing.T) {
	e := newEnv(t)
	nodes := e.startNodes(3)
	e.addNode(nodes[0])
	e.addNode(nodes[1])
	e.kvctl("fill", "200")
	nodesBefore := e.kvctl("nodes")
	survivorBefore := nodes[0].Contents(t)

	nodes[1].Kill()
	out, err := e.tryKvctl("add-node", nodes[2].URL)
	if err == nil {
		t.Fatalf("expected failure, got %q", out)
	}
	t.Logf("add-node with a dead source failed as expected: %s", strings.TrimSpace(out))

	if got := e.kvctl("nodes"); got != nodesBefore {
		t.Fatalf("membership changed:\nbefore:\n%s\nafter:\n%s", nodesBefore, got)
	}
	if !maps.Equal(nodes[0].Contents(t), survivorBefore) {
		t.Fatal("surviving node's data changed")
	}
}

// 8. CLI guard rails.
func TestMembershipGuardRails(t *testing.T) {
	e := newEnv(t)
	n := e.startNode()
	e.addNode(n)

	if _, err := e.tryKvctl("add-node", n.URL); err == nil {
		t.Fatal("duplicate add should fail")
	}
	// The same node written differently is still a duplicate.
	if _, err := e.tryKvctl("add-node", strings.TrimPrefix(n.URL, "http://")+"/"); err == nil {
		t.Fatal("duplicate add (normalized) should fail")
	}
	if _, err := e.tryKvctl("remove-node", "http://127.0.0.1:1"); err == nil {
		t.Fatal("removing unknown node should fail")
	}
	e.kvctl("put", "k", "v")
	if _, err := e.tryKvctl("remove-node", n.URL); err == nil {
		t.Fatal("removing the last node while it has data should fail")
	}
	e.kvctl("del", "k")
	e.removeNode(n) // empty last node: allowed
}

func label(i int) string { return fmt.Sprintf("n%d", i) }

// 9. Error propagation (Cox-Buday): a failing command prints a one-line
// message with a log ID, and kvctl.log has that ID with the full chain:
// the cluster's message, the kvhttp layer, and the raw network error.
func TestFailureGivesLogIDWithFullDetails(t *testing.T) {
	e := newEnv(t)
	n := e.startNode()
	e.addNode(n)
	ghost := e.startNode()
	ghost.Kill()

	out, err := e.tryKvctl("add-node", ghost.URL)
	if err == nil {
		t.Fatalf("expected failure, got %q", out)
	}
	m := regexp.MustCompile(`log ID ([0-9a-f]{8})`).FindStringSubmatch(out)
	if m == nil || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("want one line with a log ID, got %q", out)
	}

	entry := logEntry(t, filepath.Join(filepath.Dir(e.cluster), "kvctl.log"), m[1])
	var modules []string
	for _, layer := range entry["chain"].([]any) {
		if mod, ok := layer.(map[string]any)["module"].(string); ok {
			modules = append(modules, mod)
		}
	}
	if !slices.Equal(modules, []string{"cluster", "kvhttp"}) || entry["stack"] == nil ||
		!strings.Contains(fmt.Sprint(entry["chain"]), "connection refused") {
		t.Fatalf("log entry for %s should show cluster -> kvhttp -> the network error, with a stack:\n%v", m[1], entry)
	}
}

// logEntry returns the JSON log line in path whose log_id is id.
func logEntry(t *testing.T, path, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if m["log_id"] == id {
			return m
		}
	}
	t.Fatalf("no log line with log_id %s in %s", id, path)
	return nil
}
