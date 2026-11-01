package cluster_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

// FuzzClusterModel is a model-based (stateful) property test. It reads the
// fuzz input as a script of operations, runs each one against a real
// Cluster over in-memory fake nodes, and against a trivial model: a map for
// the data and a set for the membership. After every step the two must
// agree.
//
// Each op is two bytes, (kind, arg), over a pool of 5 nodes and 8 keys:
//
//	kind%8  op
//	0       AddNode(n)
//	1       RemoveNode(n)
//	2, 3    Put(k, a value unique to this step)
//	4       Delete(k)
//	5       toggle n.Down (a crashed node)
//	6       toggle n.FailDeletes (clean-up deletes fail)
//	7       let n accept only arg/5%5 more puts (4 = no limit), so a
//	        rebalance can fail part-way through its copy phase
//
// Properties checked:
//   - Membership ops fail exactly when the model says they must (duplicate
//     add, unknown remove, a down node, removing the last node while the
//     cluster holds data). With no faults injected, they always succeed.
//   - An error that doesn't say "membership changed" changed nothing: the
//     membership is the same as before. One that does committed the change.
//   - For every key whose owner is up, Get matches the model: no lost
//     writes, no stale reads, no resurrected deletes. This holds even after
//     failed copy phases and failed clean-ups, which leave stray copies on
//     nodes that don't own them.
//   - Every error is a well-formed cluster error.
//   - Every move in a report goes to the key's owner.
//   - No assertion fires (the code's own contracts are oracles too).
//
// Not modelled: writes concurrent with a rebalance. The design documents
// them as unsafe (see rebalance), so ops run one at a time.
func FuzzClusterModel(f *testing.F) {
	const (
		add, remove, put, del, down, failDel, putBudget = 0, 1, 2, 4, 5, 6, 7
	)
	op := func(kind, arg int) []byte { return []byte{byte(kind), byte(arg)} }
	script := func(ops ...[]byte) []byte { return slices.Concat(ops...) }
	var fill, delAll []byte // every key in the key space
	for k := range keySpace {
		fill = append(fill, op(put, k)...)
		delAll = append(delAll, op(del, k)...)
	}
	budget := func(node, puts int) []byte { return op(putBudget, puts*poolSize+node) }

	f.Add(script(op(add, 0), fill, op(add, 1), op(add, 2), op(remove, 0), op(remove, 1)))
	f.Add(script(op(add, 0), op(add, 0), op(remove, 3), fill, op(remove, 0), op(del, 1))) // guard rails
	f.Add(script(op(add, 0), fill, op(down, 0), op(add, 1), op(down, 1), op(down, 0), op(add, 1)))
	// A failed clean-up leaves stale copies; then delete and re-add.
	f.Add(script(op(add, 0), op(add, 1), fill, op(failDel, 0), op(failDel, 1), op(add, 2),
		op(failDel, 0), op(failDel, 1), delAll, op(add, 3)))
	// Removing a node whose clean-up fails, deleting, then adding it back.
	f.Add(script(op(add, 0), op(add, 1), fill, op(failDel, 1), op(remove, 1), op(failDel, 1),
		fill, delAll, op(add, 1)))
	// The same, but the returning node can't delete its stale copies, so
	// joining must fail before the commit.
	f.Add(script(op(add, 0), op(add, 1), fill, op(failDel, 1), op(remove, 1), delAll, op(add, 1)))
	// A copy phase that fails part-way, then deletes and a retry.
	f.Add(script(op(add, 0), op(add, 1), fill, budget(0, 1), op(remove, 1),
		delAll, budget(0, 4), op(add, 2), op(remove, 1)))

	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > 120 {
			t.Skip("long scripts add little and slow the fuzzer down")
		}
		m := newModelRun(t)
		for i := 0; i+1 < len(script); i += 2 {
			m.step(i/2, script[i], script[i+1])
			m.check()
		}
	})
}

const poolSize, keySpace = 5, 8

// faultyNode is a MemNode that can also run out of puts: once putsLeft
// reaches 0, Put fails. A negative putsLeft means no limit.
type faultyNode struct {
	*clustertest.MemNode
	putsLeft int
}

var errNoPuts = errors.New("node refuses puts")

func (n *faultyNode) Put(ctx context.Context, k, v string) error {
	switch {
	case n.Down: // MemNode reports it
	case n.putsLeft == 0:
		return errNoPuts
	case n.putsLeft > 0:
		n.putsLeft--
	}
	return n.MemNode.Put(ctx, k, v)
}

// modelRun is one script: the real cluster, its fake nodes, and the model.
type modelRun struct {
	t       *testing.T
	ctx     context.Context
	c       *cluster.Cluster
	store   *clustertest.MemStore
	nodes   map[string]*faultyNode
	members map[string]bool   // model: the live nodes
	data    map[string]string // model: what Get should return
	at      int               // the step running now, for failure messages
}

func newModelRun(t *testing.T) *modelRun {
	m := &modelRun{t: t, ctx: context.Background(), store: &clustertest.MemStore{},
		nodes: map[string]*faultyNode{}, members: map[string]bool{}, data: map[string]string{}}
	for i := range poolSize {
		m.nodes[nodeName(i)] = &faultyNode{MemNode: clustertest.NewMemNode(), putsLeft: -1}
	}
	c, err := cluster.Open(m.store, func(addr string) cluster.Node { return m.nodes[addr] })
	if err != nil {
		t.Fatal(err)
	}
	m.c = c
	return m
}

func nodeName(i int) string { return fmt.Sprintf("n%d", i%poolSize) }
func keyName(i int) string  { return fmt.Sprintf("k%d", i%keySpace) }

// step runs one op on both the cluster and the model.
func (m *modelRun) step(i int, kind, arg byte) {
	t := m.t
	m.at = i
	node, key := nodeName(int(arg)), keyName(int(arg))
	switch kind % 8 {
	case 0, 1:
		m.changeMembership(kind%8 == 0, node)
	case 2, 3:
		value := fmt.Sprintf("v%d", i)
		owner, _ := m.c.Owner(key)
		wantErr := len(m.members) == 0 || m.nodes[owner].Down || m.nodes[owner].putsLeft == 0
		_, err := m.c.Put(m.ctx, key, value)
		m.checkErr(err)
		if (err != nil) != wantErr {
			t.Fatalf("step %d: put %s: err = %v, model expected error = %v", i, key, err, wantErr)
		}
		if err == nil {
			m.data[key] = value
		}
	case 4:
		owner, _ := m.c.Owner(key)
		wantErr := len(m.members) == 0 || m.nodes[owner].Down || m.nodes[owner].FailDeletes
		err := m.c.Delete(m.ctx, key)
		m.checkErr(err)
		if (err != nil) != wantErr {
			t.Fatalf("step %d: delete %s: err = %v, model expected error = %v", i, key, err, wantErr)
		}
		if err == nil {
			delete(m.data, key)
		}
	case 5:
		m.nodes[node].Down = !m.nodes[node].Down
	case 6:
		m.nodes[node].FailDeletes = !m.nodes[node].FailDeletes
	case 7:
		m.nodes[node].putsLeft = int(arg) / poolSize % 5
		if m.nodes[node].putsLeft == 4 {
			m.nodes[node].putsLeft = -1 // no limit
		}
	}
}

// changeMembership adds or removes node and checks the outcome against the
// model.
func (m *modelRun) changeMembership(isAdd bool, node string) {
	t := m.t
	before := m.c.Nodes()

	// What the model says must happen.
	anyMemberDown := false
	for n := range m.members {
		anyMemberDown = anyMemberDown || m.nodes[n].Down
	}
	var mustFail bool
	var cause error // the sentinel the error must wrap, if it's exported
	switch {
	case isAdd && m.members[node]:
		mustFail, cause = true, jumphash.ErrNodeExists
	case !isAdd && !m.members[node]:
		mustFail, cause = true, jumphash.ErrNodeNotFound
	case isAdd && m.nodes[node].Down, anyMemberDown:
		mustFail, cause = true, clustertest.ErrDown
	case !isAdd && len(m.members) == 1 && len(m.data) > 0:
		mustFail = true // removing the last node while it holds data
	}
	faults := false
	for _, n := range m.nodes {
		faults = faults || n.FailDeletes || n.putsLeft >= 0
	}

	var rep cluster.Report
	var err error
	if isAdd {
		rep, err = m.c.AddNode(m.ctx, node)
	} else {
		rep, err = m.c.RemoveNode(m.ctx, node)
	}
	m.checkErr(err)

	what := "remove"
	if isAdd {
		what = "add"
	}
	switch {
	case mustFail && (err == nil || cause != nil && !errors.Is(err, cause)):
		t.Fatalf("step %d: %s %s: got %v, model expected an error (cause %v)", m.at, what, node, err, cause)
	case !mustFail && err != nil && !faults:
		t.Fatalf("step %d: %s %s failed with no faults injected: %v", m.at, what, node, err)
	}

	committed := err == nil || strings.Contains(err.Error(), "membership changed")
	if committed && isAdd {
		m.members[node] = true
	} else if committed {
		delete(m.members, node)
	} else if !slices.Equal(m.c.Nodes(), before) {
		t.Fatalf("step %d: %s %s failed without saying membership changed, but nodes went %v -> %v: %v",
			m.at, what, node, before, m.c.Nodes(), err)
	}

	if committed {
		for _, mv := range rep.Moves {
			if owner, _ := m.c.Owner(mv.Key); owner != mv.To {
				t.Fatalf("step %d: %q moved to %s, but its owner is %s", m.at, mv.Key, mv.To, owner)
			}
		}
	}
}

// checkErr fails unless err is nil or a well-formed cluster error.
func (m *modelRun) checkErr(err error) {
	if err == nil {
		return
	}
	if e, ok := errs.Outermost(err); !ok || e.Module != "cluster" {
		m.t.Fatalf("step %d: not a well-formed cluster error: %s", m.at, errs.Describe(err))
	}
}

// check compares the cluster with the model.
func (m *modelRun) check() {
	t := m.t
	nodes := m.c.Nodes()
	if want := slices.Sorted(maps.Keys(m.members)); !slices.Equal(slices.Sorted(slices.Values(nodes)), want) {
		t.Fatalf("step %d: cluster has nodes %v, model has %v", m.at, nodes, want)
	}
	if saved, _ := m.store.Load(); !slices.Equal(saved.Nodes(), nodes) {
		t.Fatalf("step %d: saved membership %v differs from the cluster's %v", m.at, saved.Nodes(), nodes)
	}
	for i := range keySpace {
		key := keyName(i)
		want, wantOK := m.data[key]
		owner, err := m.c.Owner(key)
		if err != nil {
			if wantOK {
				t.Fatalf("step %d: cluster is empty but the model holds %s=%q", m.at, key, want)
			}
			continue
		}
		if m.nodes[owner].Down {
			continue // can't read it; checked again once the node is back
		}
		got, ok, err := m.c.Get(m.ctx, key)
		if err != nil || got != want || ok != wantOK {
			t.Fatalf("step %d: Get(%s) on %s = (%q, %v, %v), model says (%q, %v)\nnode contents: %s",
				m.at, key, owner, got, ok, err, want, wantOK, m.dump())
		}
	}
}

// dump describes every node, for failure messages.
func (m *modelRun) dump() string {
	var b strings.Builder
	for i := range poolSize {
		n := m.nodes[nodeName(i)]
		fmt.Fprintf(&b, "\n  %s member=%v down=%v failDeletes=%v putsLeft=%d %v",
			nodeName(i), m.members[nodeName(i)], n.Down, n.FailDeletes, n.putsLeft, n.Store.Snapshot())
	}
	return b.String()
}
