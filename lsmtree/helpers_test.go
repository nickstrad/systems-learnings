package lsmtree_test

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

const dir = "/db"

// tinyOptions makes every mechanism run often: a few writes fill the
// memtable, a few flushes trigger compaction, and data reaches the
// deepest level quickly.
func tinyOptions(fs *vfstest.MemFS) lsmtree.Options {
	return lsmtree.Options{
		FS:                  fs,
		MemtableSize:        512,
		BlockSize:           64,
		TableSize:           256,
		BaseLevelSize:       512,
		LevelSizeRatio:      2,
		MaxLevels:           4,
		L0CompactionTrigger: 2,
		SyncWrites:          true,
	}
}

func open(t testing.TB, opts lsmtree.Options) *lsmtree.DB {
	t.Helper()
	db, err := lsmtree.Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func put(t testing.TB, db *lsmtree.DB, key, value string) {
	t.Helper()
	if err := db.Put([]byte(key), []byte(value)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

func del(t testing.TB, db *lsmtree.DB, key string) {
	t.Helper()
	if err := db.Delete([]byte(key)); err != nil {
		t.Fatalf("Delete(%q): %v", key, err)
	}
}

// get returns the value, or "<absent>".
func get(t testing.TB, db *lsmtree.DB, key string) string {
	t.Helper()
	v, ok, err := db.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if !ok {
		return "<absent>"
	}
	return string(v)
}

// dump returns every live key and value, via Scan.
func dump(t testing.TB, db *lsmtree.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	var last []byte
	err := db.Scan(nil, nil, func(k, v []byte) bool {
		if last != nil && bytes.Compare(last, k) >= 0 {
			t.Fatalf("Scan out of order: %q then %q", last, k)
		}
		last = bytes.Clone(k)
		out[string(k)] = string(v)
		return true
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return out
}

// checkModel fails unless the DB holds exactly model, checked both by
// Scan and by a Get of every key in keySpace.
func checkModel(t testing.TB, db *lsmtree.DB, model map[string]string, keySpace []string) {
	t.Helper()
	if got := dump(t, db); !maps.Equal(got, model) {
		t.Fatalf("Scan disagrees with the model:\n%s", diff(got, model))
	}
	for _, k := range keySpace {
		checkGet(t, db, model, k)
	}
}

// modelGet is the model's answer to Get: the value, or "<absent>".
func modelGet(model map[string]string, k string) string {
	if v, ok := model[k]; ok {
		return v
	}
	return "<absent>"
}

// checkGet fails unless Get(k) agrees with the model.
func checkGet(t testing.TB, db *lsmtree.DB, model map[string]string, k string) {
	t.Helper()
	if got, want := get(t, db, k), modelGet(model, k); got != want {
		t.Fatalf("Get(%q) = %q, model has %q", k, got, want)
	}
}

func diff(got, want map[string]string) string {
	var b bytes.Buffer
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if g, ok := got[k]; !ok || g != want[k] {
			fmt.Fprintf(&b, "  %q: got %q (present=%v), want %q\n", k, g, ok, want[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[k]; !ok {
			fmt.Fprintf(&b, "  %q: got %q, want absent (resurrected or never written)\n", k, got[k])
		}
	}
	return b.String()
}
