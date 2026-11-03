// Package experiment is the plumbing every project's experiments share:
// tables that print with t.Log and save as CSV, JSON-lines logs, the run
// directory with its runs.csv, and a runner for parallel sweeps.
//
// Experiments are Go tests behind the `experiments` build tag, run by
// shared/scripts/experiments.sh (or `make experiments` at the root). The
// script makes a run directory and sets three variables:
//
//	EXPERIMENT_RESULTS_DIR  where to write; unset means print only
//	EXPERIMENT_RUN_ID       the run's ID (default: the directory's name)
//	EXPERIMENT_GIT_COMMIT   the commit measured (default: "unknown")
//
// Every CSV row and JSON line carries run_id as its first field, so DuckDB
// can read all runs by glob (read_csv('results/*/x.csv')) and compare them.
//
// A project's experiments package wires it up in TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(experiment.Main(m)) }
package experiment

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"
)

// Run is the current run's output directory. A nil *Run writes nothing,
// so experiments behave the same with or without EXPERIMENT_RESULTS_DIR.
type Run struct {
	dir, runID string

	mu   sync.Mutex // guards logs: configurations run in parallel
	logs map[string]*Log
}

// current is set by Main.
var current *Run

// Main opens the run (if EXPERIMENT_RESULTS_DIR is set), runs the tests,
// closes every log, and returns the exit code for os.Exit.
func Main(m *testing.M) int {
	r, err := open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "experiments:", err)
		return 2
	}
	current = r
	code := m.Run()
	if err := r.close(); err != nil {
		fmt.Fprintln(os.Stderr, "experiments:", err)
		code = 1
	}
	return code
}

// Current returns the run Main opened, or nil when results aren't saved.
func Current() *Run { return current }

// open creates the run directory and writes runs.csv.
func open() (*Run, error) {
	dir := os.Getenv("EXPERIMENT_RESULTS_DIR")
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &Run{dir: dir, runID: os.Getenv("EXPERIMENT_RUN_ID"), logs: map[string]*Log{}}
	if r.runID == "" {
		r.runID = filepath.Base(dir)
	}
	commit := os.Getenv("EXPERIMENT_GIT_COMMIT")
	if commit == "" {
		commit = "unknown"
	}
	err := writeCSV(filepath.Join(dir, "runs.csv"),
		[]string{"run_id", "started_utc", "git_commit", "go_version", "goos", "goarch"},
		[][]string{{r.runID, time.Now().UTC().Format(time.RFC3339), commit, runtime.Version(), runtime.GOOS, runtime.GOARCH}})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Run) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, l := range r.logs {
		errs = append(errs, l.close())
	}
	return errors.Join(errs...)
}

// Log is a JSON-lines file in the run directory, such as events.jsonl.
// It is safe for concurrent use. A nil *Log writes nothing.
type Log struct {
	runID string

	mu   sync.Mutex
	file *os.File
	buf  *bufio.Writer
	enc  *json.Encoder
	err  error // the first failed write, reported when the run closes
}

// Log returns the run's JSON-lines file name (e.g. "events.jsonl"),
// creating it on first use. It returns nil for a nil run, or if the file
// can't be created (the error fails the run when it closes).
func (r *Run) Log(name string) *Log {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.logs[name]; ok {
		return l
	}
	l := &Log{runID: r.runID}
	f, err := os.Create(filepath.Join(r.dir, name))
	if err != nil {
		l.err = err
	} else {
		l.file, l.buf = f, bufio.NewWriter(f)
		l.enc = json.NewEncoder(l.buf)
	}
	r.logs[name] = l
	return l
}

// Write appends v as one JSON line. v should be a struct whose first
// field is `RunID string `json:"run_id"“; set it from Log.RunID. Fields
// should be flat and snake_case so read_json_auto gives clean columns.
func (l *Log) Write(v any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.enc == nil {
		return
	}
	if err := l.enc.Encode(v); err != nil && l.err == nil {
		l.err = err
	}
}

// RunID returns the run ID to put in each line, or "" for a nil log.
func (l *Log) RunID() string {
	if l == nil {
		return ""
	}
	return l.runID
}

func (l *Log) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return l.err
	}
	return errors.Join(l.err, l.buf.Flush(), l.file.Close())
}

// Table is one experiment's results: a header and rows of values. Report
// prints it with t.Log and, when results are saved, writes <Name>.csv
// with run_id as the first column.
type Table struct {
	Name   string // CSV file name without extension, e.g. "h1_movement"
	Header []string
	rows   [][]any
}

// Add appends a row. It panics if the row's length doesn't match Header,
// which is a bug in the experiment.
func (tb *Table) Add(row ...any) {
	if len(row) != len(tb.Header) {
		panic(fmt.Sprintf("%s: row has %d values, header has %d", tb.Name, len(row), len(tb.Header)))
	}
	tb.rows = append(tb.rows, row)
}

// Report logs the table and, if results are being saved, writes it.
func (tb *Table) Report(t testing.TB) {
	t.Helper()
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, strings.Join(tb.Header, "\t")+"\t")
	for _, row := range tb.rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = format(v, 4)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t")+"\t")
	}
	w.Flush()
	t.Logf("%s:\n%s", tb.Name, b.String())
	if err := current.writeTable(tb); err != nil {
		t.Fatalf("saving %s: %v", tb.Name, err)
	}
}

// writeTable writes tb as <Name>.csv, with run_id as the first column.
func (r *Run) writeTable(tb *Table) error {
	if r == nil {
		return nil
	}
	rows := make([][]string, len(tb.rows))
	for i, row := range tb.rows {
		rows[i] = []string{r.runID}
		for _, v := range row {
			rows[i] = append(rows[i], format(v, -1))
		}
	}
	return writeCSV(filepath.Join(r.dir, tb.Name+".csv"), append([]string{"run_id"}, tb.Header...), rows)
}

// format renders a cell: nil is empty (NULL in DuckDB), and floats get
// digits significant figures (-1: as many as needed to round-trip).
func format(v any, digits int) string {
	switch v := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(v, 'g', digits, 64)
	default:
		return fmt.Sprint(v)
	}
}

func writeCSV(path string, header []string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write(header)
	w.WriteAll(rows) // flushes
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Sweep runs one configuration per subtest, all in parallel, and returns
// their results in the order of configs once every one has finished.
// Configurations must not share mutable state. ok is false if any
// configuration stopped early (t.Fatal); its result is then the zero R,
// and the caller should skip checks that compare configurations.
func Sweep[C, R any](t *testing.T, configs []C, name func(C) string, run func(t *testing.T, c C) R) (out []R, ok bool) {
	t.Helper()
	out = make([]R, len(configs))
	done := make([]bool, len(configs))
	t.Run("configs", func(t *testing.T) {
		for i, c := range configs {
			t.Run(name(c), func(t *testing.T) {
				t.Parallel()
				out[i] = run(t, c)
				done[i] = true
			})
		}
	})
	for _, d := range done {
		if !d {
			return out, false
		}
	}
	return out, true
}
