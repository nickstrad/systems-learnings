package lsmtree_test

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

var errInjected = errors.New("injected fault")

// faultOps are the filesystem calls a write path makes.
var faultOps = []vfstest.Op{
	vfstest.OpCreate, vfstest.OpWrite, vfstest.OpSync, vfstest.OpRename,
	vfstest.OpRemove, vfstest.OpSyncDir, vfstest.OpOpen, vfstest.OpRead, vfstest.OpStat,
}

// workload is enough writes, with tiny options, to flush and compact
// several times. Every fifth write is a delete.
func workload(i int) (key, value string, isDelete bool) {
	key = fmt.Sprintf("k%03d", (i*37)%120)
	return key, fmt.Sprintf("v%d-%s", i, strings.Repeat("x", i%30)), i%5 == 4
}

const workloadSize = 250

// doWrite performs write i of the workload.
func doWrite(db *lsmtree.DB, i int) error {
	k, v, isDel := workload(i)
	if isDel {
		return db.Delete([]byte(k))
	}
	return db.Put([]byte(k), []byte(v))
}

// applyWrite performs write i of the workload on the model.
func applyWrite(model map[string]string, i int) {
	k, v, isDel := workload(i)
	if isDel {
		delete(model, k)
	} else {
		model[k] = v
	}
}

// TestEveryFaultLeavesARecoverableStore fails the nth call of each kind of
// filesystem operation, for every n the workload reaches. After the
// fault, the store must refuse further writes with a well-formed error,
// keep answering reads, and, once reopened without faults, hold every
// acknowledged write. The one write whose call failed may or may not
// have landed.
//
// The workload makes thousands of writes, and each run replays it, so
// the test fails every one of the first 100 calls of each op and then an
// even sample of about 100 more, ending with the very last call. With
// -short (make race) it fails the first 10 and a sample of about 10.
func TestEveryFaultLeavesARecoverableStore(t *testing.T) {
	first, sampled := 100, 100
	if testing.Short() {
		first, sampled = 10, 10
	}
	calls := countCalls(t)
	for _, op := range faultOps {
		total := calls[op]
		if total == 0 {
			continue
		}
		stride := max(1, (total-first)/sampled)
		for n := 1; n <= total; n++ {
			if n > first && n != total && (n-first)%stride != 0 {
				continue
			}
			if !runWithFault(t, op, n) {
				t.Fatalf("%s #%d: the fault never fired, but a dry run made %d calls", op, n, total)
			}
		}
		t.Logf("%-8s %5d calls in the workload", op, total)
	}
}

// countCalls runs the workload with no faults and counts each op.
func countCalls(t *testing.T) map[vfstest.Op]int {
	t.Helper()
	fs := vfstest.NewMemFS()
	db := open(t, tinyOptions(fs))
	calls := map[vfstest.Op]int{}
	fs.SetFault(func(o vfstest.Op, _ string) error { calls[o]++; return nil })
	for i := range workloadSize {
		if err := doWrite(db, i); err != nil {
			t.Fatal(err)
		}
	}
	fs.SetFault(nil)
	db.Close()
	return calls
}

// runWithFault runs the workload with the nth op failing and reports
// whether the fault fired.
func runWithFault(t *testing.T, op vfstest.Op, n int) (fired bool) {
	t.Helper()
	fs := vfstest.NewMemFS()
	opts := tinyOptions(fs)
	db := open(t, opts)

	fault := vfstest.FailNth(op, n, errInjected)
	fs.SetFault(func(o vfstest.Op, path string) error {
		err := fault(o, path)
		if err != nil {
			fired = true
		}
		return err
	})

	model := map[string]string{}
	uncertain := ""
	for i := range workloadSize {
		err := doWrite(db, i)
		if err == nil {
			applyWrite(model, i)
			continue
		}
		if !errors.Is(err, errInjected) {
			t.Fatalf("%s #%d: write %d failed with %v, not the injected fault", op, n, i, err)
		}
		assertWellFormed(t, err)
		uncertain, _, _ = workload(i)
		before := maps.Clone(model)
		applyWrite(model, i)
		// The store refuses more writes but still answers reads.
		if err := db.Put([]byte("after"), []byte("x")); !errors.Is(err, lsmtree.ErrWritesFailed) {
			t.Fatalf("%s #%d: Put after a failure = %v, want ErrWritesFailed", op, n, err)
		}
		for key := range before {
			if key == uncertain {
				continue
			}
			v, ok, err := db.Get([]byte(key))
			if err != nil && !errors.Is(err, errInjected) {
				t.Fatalf("%s #%d: Get(%q) after the failure: %v", op, n, key, err)
			}
			if err == nil && (!ok || string(v) != before[key]) {
				t.Fatalf("%s #%d: Get(%q) after the failure = %q, %v; want %q", op, n, key, v, ok, before[key])
			}
		}
		break
	}
	db.Close()

	fs.SetFault(nil)
	db, err := lsmtree.Open(dir, opts)
	if err != nil {
		t.Fatalf("%s #%d: reopening after the fault failed: %v", op, n, err)
	}
	defer db.Close()
	got := dump(t, db)
	if uncertain != "" {
		// Accept the uncertain key in either state.
		if v, ok := got[uncertain]; ok {
			model[uncertain] = v
		} else {
			delete(model, uncertain)
		}
	}
	if !maps.Equal(got, model) {
		t.Fatalf("%s #%d: after reopening, the store disagrees with the acknowledged writes:\n%s", op, n, diff(got, model))
	}
	return fired
}

// TestFaultDuringOpen fails each call Open makes while recovering a
// store with data in its WAL and tables. Open must fail cleanly, and a
// later Open without faults must recover everything.
func TestFaultDuringOpen(t *testing.T) {
	fs := vfstest.NewMemFS()
	opts := tinyOptions(fs)
	db := open(t, opts)
	model := map[string]string{}
	for i := range 120 {
		k, v, _ := workload(i)
		put(t, db, k, v)
		model[k] = v
	}
	fs.Crash() // leave a WAL to replay

	for _, op := range faultOps {
		for n := 1; n < 500; n++ {
			fired := false
			fault := vfstest.FailNth(op, n, errInjected)
			fs.SetFault(func(o vfstest.Op, path string) error {
				err := fault(o, path)
				fired = fired || err != nil
				return err
			})
			db, err := lsmtree.Open(dir, opts)
			fs.SetFault(nil)
			if !fired {
				if err != nil {
					t.Fatalf("Open with no fault fired: %v", err)
				}
				db.Close()
				break
			}
			if err == nil {
				// The fault hit a step whose failure Open tolerates (none
				// today), or after Open's last check; the store must still
				// be right.
				checkModel(t, db, model, nil)
				db.Close()
				continue
			}
			if !errors.Is(err, errInjected) {
				t.Fatalf("%s #%d: Open failed with %v, not the injected fault", op, n, err)
			}
			assertWellFormed(t, err)
			fs.Crash() // drop the half-open store's handles and lock
		}
	}
	db = open(t, opts)
	defer db.Close()
	checkModel(t, db, model, nil)
}
