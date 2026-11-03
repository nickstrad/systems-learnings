//go:build e2e

package e2e

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
)

// 7. A model-checked workload. A fixed-seed sequence of puts, overwrites,
// deletes, flushes, and compactions runs through the binary, one process
// per step, with a tiny memtable so data moves through every level. A
// plain map records what the store should hold, and the store is checked
// against it as the test goes.
//
// It checks the same rule as the domain's FuzzDBModel (no lost writes, no
// stale reads, no resurrected deletes), but through real files, process
// restarts, and the CLI's argument handling. It is deliberately not
// random: the seed is fixed, so it runs the same steps every time. The
// fuzz test does the random exploring.
func TestWorkloadMatchesModel(t *testing.T) {
	const (
		seed  = 20261102
		steps = 120
	)
	e := newEnv(t, lsmtree.Options{MemtableSize: 100, BlockSize: 256, TableSize: 512, BaseLevelSize: 512, LevelSizeRatio: 2})
	w := &workload{e: e, model: map[string]string{}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("steps so far:\n  %s", strings.Join(w.trace, "\n  "))
		}
	})

	// Keys that sort in interesting places or need quoting in scan output
	// are left to the unit tests; these all print as is.
	keys := []string{"a", "aa", "ab", "b", "user:1", "user:2", "with space", "ünï", "z", "zz"}
	for i := range 10 {
		keys = append(keys, fmt.Sprintf("k%02d", i))
	}

	rng := rand.New(rand.NewPCG(seed, seed))
	for step := range steps {
		key := keys[rng.IntN(len(keys))]
		switch r := rng.IntN(100); {
		case r < 50:
			// Most values overflow the tiny memtable, so most puts flush
			// and compact within their own process.
			value := fmt.Sprintf("v%d-%s", step, strings.Repeat("x", rng.IntN(200)))
			if rng.IntN(10) == 0 {
				value = "" // empty values are allowed
			}
			w.put(key, value)
		case r < 70:
			w.del(key)
		case r < 75:
			w.do("flush")
		case r < 80:
			w.do("compact")
		case r < 85:
			w.checkScan()
		default:
			w.get(key)
		}
	}
	w.checkScan()
	for _, k := range keys {
		w.get(k)
	}
	w.do("verify")
	t.Logf("ran %d steps; %d live keys\n$ lsmctl dump\n%s", steps, len(w.model), e.lsmctl("dump"))
}

// workload drives the store through lsmctl and keeps the model in step.
type workload struct {
	e     *env
	model map[string]string // what the store should hold
	trace []string          // steps so far, logged if the test fails
}

func (w *workload) log(format string, args ...any) {
	w.trace = append(w.trace, fmt.Sprintf(format, args...))
}

func (w *workload) put(key, value string) {
	w.e.t.Helper()
	w.log("put %q=%.12q", key, value)
	w.e.lsmctl("put", key, value)
	w.model[key] = value
	w.get(key)
}

func (w *workload) del(key string) {
	w.e.t.Helper()
	w.log("del %q", key)
	w.e.lsmctl("del", key)
	delete(w.model, key)
	w.get(key)
}

func (w *workload) do(cmd string) {
	w.e.t.Helper()
	w.log("%s", cmd)
	w.e.lsmctl(cmd)
}

// get reads key through lsmctl and compares it with the model.
func (w *workload) get(key string) {
	w.e.t.Helper()
	stdout, stderr, code := w.e.run("get", key)
	want, exists := w.model[key]
	switch {
	case !exists && code == 0:
		w.e.t.Fatalf("get %q = %q, but the key was deleted or never written", key, stdout)
	case !exists && !strings.Contains(stderr, "not found"):
		w.e.t.Fatalf("get %q: want not found, got exit %d, %q", key, code, stderr)
	case exists && code != 0:
		w.e.t.Fatalf("get %q: exit %d, %q", key, code, stderr)
	case exists && strings.TrimSuffix(stdout, "\n") != want:
		w.e.t.Fatalf("get %q = %.20q, want %.20q", key, stdout, want)
	}
}

// checkScan reads the whole store and compares it with the model, in key
// order. Empty values print as "" in scan output.
func (w *workload) checkScan() {
	w.e.t.Helper()
	w.log("scan")
	var want []string
	for _, k := range slices.Sorted(maps.Keys(w.model)) {
		v := w.model[k]
		if v == "" {
			v = `""`
		}
		want = append(want, k+"\t"+v)
	}
	if got := lines(w.e.lsmctl("scan")); !slices.Equal(got, want) {
		w.e.t.Fatalf("scan:\n got %d keys %.300q\nwant %d keys %.300q", len(got), got, len(want), want)
	}
}
