package experiment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withRun points the package at a fresh run directory for one test.
func withRun(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run-1")
	t.Setenv("EXPERIMENT_RESULTS_DIR", dir)
	t.Setenv("EXPERIMENT_RUN_ID", "")
	t.Setenv("EXPERIMENT_GIT_COMMIT", "abc123")
	r, err := open()
	if err != nil {
		t.Fatal(err)
	}
	current = r
	t.Cleanup(func() { current = nil })
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTableWritesCSVWithRunID(t *testing.T) {
	dir := withRun(t)
	tb := &Table{Name: "h1", Header: []string{"n", "moved", "note"}}
	tb.Add(10, 0.0909090909, nil)
	tb.Add(100, 0.0099, "x")
	tb.Report(t)

	want := "run_id,n,moved,note\nrun-1,10,0.0909090909,\nrun-1,100,0.0099,x\n"
	if got := read(t, filepath.Join(dir, "h1.csv")); got != want {
		t.Fatalf("h1.csv =\n%s\nwant\n%s", got, want)
	}
	runs := read(t, filepath.Join(dir, "runs.csv"))
	if !strings.HasPrefix(runs, "run_id,started_utc,git_commit,go_version,goos,goarch\nrun-1,") ||
		!strings.Contains(runs, ",abc123,") {
		t.Fatalf("runs.csv = %q", runs)
	}
}

func TestNilRunWritesNothing(t *testing.T) {
	current = nil
	tb := &Table{Name: "h1", Header: []string{"n"}}
	tb.Add(1)
	tb.Report(t) // must not fail or write
	var r *Run
	if r.Log("x.jsonl") != nil {
		t.Fatal("a nil run must have no ID and no logs")
	}
	var l *Log
	l.Write(struct{}{}) // no panic
	if r.close() != nil {
		t.Fatal("closing a nil run must succeed")
	}
}

func TestAddRejectsAWrongLengthRow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Add accepted a row of the wrong length")
		}
	}()
	(&Table{Name: "t", Header: []string{"a", "b"}}).Add(1)
}

func TestLogWritesJSONLines(t *testing.T) {
	dir := withRun(t)
	type line struct {
		RunID string `json:"run_id"`
		N     int    `json:"n"`
	}
	l := current.Log("events.jsonl")
	if current.Log("events.jsonl") != l {
		t.Fatal("Log must return the same file for the same name")
	}
	l.Write(line{RunID: l.RunID(), N: 1})
	l.Write(line{RunID: l.RunID(), N: 2})
	if err := current.close(); err != nil {
		t.Fatal(err)
	}
	want := `{"run_id":"run-1","n":1}` + "\n" + `{"run_id":"run-1","n":2}` + "\n"
	if got := read(t, filepath.Join(dir, "events.jsonl")); got != want {
		t.Fatalf("events.jsonl = %q, want %q", got, want)
	}
}

func TestSweepKeepsConfigOrder(t *testing.T) {
	configs := []int{5, 1, 4, 2, 3}
	out, ok := Sweep(t, configs, func(c int) string { return strings.Repeat("x", c) },
		func(t *testing.T, c int) int { return c * 10 })
	if !ok {
		t.Fatal("Sweep reported a failed configuration")
	}
	for i, c := range configs {
		if out[i] != c*10 {
			t.Fatalf("out = %v, want each config ×10 in config order", out)
		}
	}
}
