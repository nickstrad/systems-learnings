//go:build experiments

// Package experiments measures the paper's claims against this store
// (DESIGN.md §8). Each Test is one experiment. It counts bytes, blocks,
// and entries instead of timing anything, and it runs the store on an
// in-memory filesystem, so the numbers are the same on every machine and
// on every run.
//
// The tests are behind the `experiments` build tag, so plain `go test
// ./...` skips them. Run them with `make experiments` (which runs
// ../shared/scripts/experiments.sh), or directly:
//
//	go test -tags experiments -count=1 -v ./experiments
//
// Each test prints a readable table with t.Log and asserts only a loose
// expected shape, so the suite also works as a regression check. The
// plumbing (tables, the run directory, JSON-lines logs, parallel sweeps)
// is shared/experiment: when EXPERIMENT_RESULTS_DIR is set, the tests
// also write their tables as CSV files and every store Event as a JSON
// line into that directory, for DuckDB (scripts/experiments.sql).
package experiments

import (
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/countfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/shared/experiment"
)

func TestMain(m *testing.M) { os.Exit(experiment.Main(m)) }

// Every experiment uses fixed-size entries, so that "size in bytes" and
// "number of entries" are proportional, as the paper's formulas assume.
const (
	keySize   = 16 // decimal digits, so keys sort numerically
	valueSize = 32
	blockSize = 4 << 10 // the paper's page, Sp
	dbDir     = "/db"
)

// key returns the 16-byte key for id. Keys sort in id order.
func key(id int) []byte { return fmt.Appendf(nil, "%0*d", keySize, id) }

// value returns a deterministic 32-byte value for id.
func value(id int) []byte {
	v := fmt.Appendf(nil, "value-%0*d", valueSize-6, id)
	return v[:valueSize]
}

// entrySize is Se: the encoded size of one entry inside a data block.
func entrySize() int {
	return codec.EntrySize(keys.Entry{Key: key(0), Seq: 1, Kind: keys.KindPut, Value: value(0)})
}

// shuffled returns ids in a fixed pseudo-random order. Every experiment
// uses its own seed, so changing one experiment does not move another's
// numbers.
func shuffled(ids []int, seed uint64) []int {
	out := append([]int(nil), ids...)
	rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)).Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// seq returns ids start, start+step, ... (n of them).
func seq(start, step, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = start + i*step
	}
	return out
}

// testDB is a store on an in-memory filesystem with I/O counting.
type testDB struct {
	*lsmtree.DB
	fs *countfs.FS
}

// openDB opens a fresh store on its own in-memory filesystem, sending its
// Events to log. It sets FS and Events, and BlockSize to blockSize if
// opts leaves it zero; the caller sets the rest. The store is closed by
// close or, failing that, when t ends.
func openDB(t testing.TB, log *eventLog, opts lsmtree.Options) *testDB {
	t.Helper()
	cfs := countfs.New(vfstest.NewMemFS())
	opts.FS = cfs
	opts.Events = log.hook
	if opts.BlockSize == 0 {
		opts.BlockSize = blockSize
	}
	db, err := lsmtree.Open(dbDir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() }) // ErrClosed if close already ran
	return &testDB{DB: db, fs: cfs}
}

// close closes the store, so a configuration frees its in-memory files as
// soon as it is done rather than when the whole test ends.
func (db *testDB) close(t testing.TB) {
	t.Helper()
	if err := db.DB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// put writes each id's key and value.
func (db *testDB) put(t testing.TB, ids []int) {
	t.Helper()
	for _, id := range ids {
		if err := db.Put(key(id), value(id)); err != nil {
			t.Fatalf("Put(%d): %v", id, err)
		}
	}
}

// compact flushes the memtable and compacts until every level is within
// its target.
func (db *testDB) compact(t testing.TB) {
	t.Helper()
	if err := db.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

// eventLog records the store's Events for one configuration, tagged with
// the phase the experiment was in (for example "load" or "measure"), and
// copies them to the run's events.jsonl when results are being saved.
type eventLog struct {
	experiment, config string
	phase              string
	events             []phasedEvent
	out                *experiment.Log // nil, writing nothing, if results aren't saved
}

type phasedEvent struct {
	phase string
	lsmtree.Event
}

func newEventLog(name, config string) *eventLog {
	return &eventLog{experiment: name, config: config, phase: "load",
		out: experiment.Current().Log("events.jsonl")}
}

// hook is the Options.Events callback.
func (l *eventLog) hook(e lsmtree.Event) {
	l.events = append(l.events, phasedEvent{l.phase, e})
	l.out.Write(eventLine{RunID: l.out.RunID(), Experiment: l.experiment, Config: l.config, Phase: l.phase, Event: e})
}

// in returns the events recorded during phase.
func (l *eventLog) in(phase string) []lsmtree.Event {
	var out []lsmtree.Event
	for _, e := range l.events {
		if e.phase == phase {
			out = append(out, e.Event)
		}
	}
	return out
}

// eventLine is one line of events.jsonl: the Event's own fields, flat,
// plus where it came from.
type eventLine struct {
	RunID      string `json:"run_id"`
	Experiment string `json:"experiment"`
	Config     string `json:"config"`
	Phase      string `json:"phase"`
	lsmtree.Event
}
