package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/shared/cli"
)

var logIDRE = regexp.MustCompile(`log ID ([0-9a-f]{8})`)

// tiny makes flushes and compactions happen after a few hundred keys.
var tiny = []string{"-memtable", "2k", "-block", "512", "-table", "4k", "-base", "8k", "-ratio", "4"}

// lsmctlIn runs realMain against the store <root>/data, logging to
// <root>/lsmctl.log (the default: next to the store directory).
func lsmctlIn(ctx context.Context, root string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = realMain(ctx, append([]string{"-dir", filepath.Join(root, "data")}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// logLines reads the JSON log in root.
func logLines(t *testing.T, root string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(root, "lsmctl.log"))
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		lines = append(lines, m)
	}
	return lines
}

// findLog returns the log line carrying the ID shown to the user.
func findLog(t *testing.T, stderr string, lines []map[string]any) map[string]any {
	t.Helper()
	m := logIDRE.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("no log ID in stderr: %q", stderr)
	}
	for _, l := range lines {
		if l["log_id"] == m[1] {
			return l
		}
	}
	t.Fatalf("log ID %s not found in log", m[1])
	return nil
}

// store returns a runner bound to a fresh store, failing the test on
// any exit code other than want.
func store(t *testing.T) (root string, run func(want int, args ...string) (stdout, stderr string)) {
	t.Helper()
	root = t.TempDir()
	return root, func(want int, args ...string) (string, string) {
		t.Helper()
		code, stdout, stderr := lsmctlIn(context.Background(), root, args...)
		if code != want {
			t.Fatalf("lsmctl %q: exit %d, want %d\nstdout: %s\nstderr: %s", args, code, want, stdout, stderr)
		}
		return stdout, stderr
	}
}

func TestKnownEdgeCaseShowsMessageAndLogID(t *testing.T) {
	root, run := store(t)
	_, stderr := run(exitError, "get", "nope")
	if !strings.Contains(stderr, `key "nope" not found`) || strings.Contains(stderr, "bug") {
		t.Fatalf("stderr: %q", stderr)
	}
	l := findLog(t, stderr, logLines(t, root))
	if l["level"] != "WARN" || l["chain"] == nil || l["time"] == nil || l["command"] != "get" {
		t.Fatalf("log line missing details: %v", l)
	}
	if ts, _ := l["time"].(string); !strings.HasSuffix(ts, "Z") {
		t.Fatalf("log time %q is not UTC", ts)
	}
}

func TestRawErrorIsReportedAsBug(t *testing.T) {
	var log bytes.Buffer
	r := cli.Reporter{Logger: slog.New(slog.NewJSONHandler(&log, nil)), Stderr: io.Discard, LogPath: "x.log", Modules: []string{"lsmtree", module}}
	if code := r.Report(io.ErrUnexpectedEOF); code != exitBug {
		t.Fatalf("exit %d, want %d", code, exitBug)
	}
	if !strings.Contains(log.String(), `"level":"ERROR"`) || !strings.Contains(log.String(), "unexpected EOF") {
		t.Fatalf("log: %s", log.String())
	}
}

func TestPanicIsReportedAsBug(t *testing.T) {
	orig := runCommand
	t.Cleanup(func() { runCommand = orig })
	runCommand = func(context.Context, *session, string, []string) error {
		panic("assertion failed: something impossible")
	}

	root, run := store(t)
	_, stderr := run(exitBug, "stats")
	if strings.Contains(stderr, "goroutine") || !strings.Contains(stderr, "This is a bug") {
		t.Fatalf("user should see a short bug message, got %q", stderr)
	}
	l := findLog(t, stderr, logLines(t, root))
	if !strings.Contains(l["error"].(string), "assertion failed") || l["stack"] == nil {
		t.Fatalf("log should hold the panic and stack: %v", l)
	}
}

// Argument errors. Bad arguments to a command are known edge cases: exit 1
// with a clear message, a log ID, and no "bug" wording. Exit 2 is only for
// no command or a bad flag before the command.
func TestArgumentErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		code    int
		wantErr string
	}{
		{"no command", nil, exitUsage, "usage: lsmctl"},
		{"unknown flag", []string{"-bogus", "stats"}, exitUsage, "usage: lsmctl"},
		{"bad size", []string{"-memtable", "lots", "stats"}, exitUsage, `"lots" is not a size`},
		{"negative size", []string{"-memtable", "-1", "stats"}, exitUsage, `"-1" is not a size`},
		{"unknown command", []string{"bogus"}, exitError, `unknown command "bogus"`},
		{"bad options", []string{"-ratio", "1", "stats"}, exitError, "LevelSizeRatio is 1"},

		{"put: none", []string{"put"}, exitError, "put: want 2 argument(s), got 0"},
		{"put: only key", []string{"put", "k"}, exitError, "put: want 2 argument(s), got 1"},
		{"put: too many", []string{"put", "k", "v", "x"}, exitError, "put: want 2 argument(s), got 3"},
		{"put: empty key", []string{"put", "", "v"}, exitError, "0-byte key"},
		{"put: key one over the limit", []string{"put", strings.Repeat("k", 4097), "v"}, exitError, "4097-byte key"},
		{"get: none", []string{"get"}, exitError, "get: want 1 argument(s), got 0"},
		{"get: too many", []string{"get", "a", "b"}, exitError, "get: want 1 argument(s), got 2"},
		{"get: empty key", []string{"get", ""}, exitError, "0-byte key"},
		{"del: none", []string{"del"}, exitError, "del: want 1 argument(s), got 0"},
		{"del: empty key", []string{"del", ""}, exitError, "0-byte key"},
		{"scan: too many", []string{"scan", "a", "b", "c"}, exitError, "scan: want [start [end]], got 3"},
		{"flush: extra", []string{"flush", "x"}, exitError, "flush: want 0 argument(s), got 1"},
		{"compact: extra", []string{"compact", "x"}, exitError, "compact: want 0 argument(s), got 1"},
		{"stats: extra", []string{"stats", "x"}, exitError, "stats: want 0 argument(s), got 1"},
		{"dump: extra", []string{"dump", "x"}, exitError, "dump: want 0 argument(s), got 1"},
		{"verify: extra", []string{"verify", "x"}, exitError, "verify: want 0 argument(s), got 1"},
		{"load: none", []string{"load"}, exitError, "load: want <n>"},
		{"load: two counts", []string{"load", "1", "2"}, exitError, "load: want <n>"},
		{"load: not a number", []string{"load", "x"}, exitError, `"x" is not a non-negative number`},
		{"load: negative", []string{"load", "--", "-1"}, exitError, `"-1" is not a non-negative number`},
		{"load: unknown flag", []string{"load", "1", "-bogus"}, exitError, "load: flag provided but not defined: -bogus"},
		{"load: negative value size", []string{"load", "1", "-value-size", "-1"}, exitError, "-value-size -1 must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, run := store(t)
			_, stderr := run(tt.code, tt.args...)
			if !strings.Contains(stderr, tt.wantErr) || strings.Contains(stderr, "This is a bug") {
				t.Fatalf("stderr %q, want it to contain %q and not report a bug", stderr, tt.wantErr)
			}
			if tt.code == exitError && !logIDRE.MatchString(stderr) {
				t.Fatalf("stderr %q has no log ID", stderr)
			}
		})
	}
}

// Each command, run as separate realMain calls against one store, the way
// separate processes would use it.
func TestCommands(t *testing.T) {
	_, run := store(t)
	steps := []struct {
		args    []string
		wantOut string
	}{
		{[]string{"put", "b", "two"}, "OK\n"},
		{[]string{"put", "a", "one"}, "OK\n"},
		{[]string{"put", "c", ""}, "OK\n"},
		{[]string{"get", "a"}, "one\n"},
		{[]string{"get", "c"}, "\n"}, // empty value: found, not missing
		{[]string{"put", "a", "uno"}, "OK\n"},
		{[]string{"get", "a"}, "uno\n"}, // overwrite: the second put wins
		{[]string{"scan"}, "a\tuno\nb\ttwo\nc\t\"\"\n"},
		{[]string{"scan", "b"}, "b\ttwo\nc\t\"\"\n"},
		{[]string{"scan", "a", "c"}, "a\tuno\nb\ttwo\n"}, // end is exclusive
		{[]string{"scan", "x"}, ""},
		{[]string{"del", "b"}, "OK\n"},
		{[]string{"del", "never-written"}, "OK\n"}, // not an error
		{[]string{"scan"}, "a\tuno\nc\t\"\"\n"},
		{[]string{"flush"}, "OK: L0 "},
		{[]string{"compact"}, "OK: L0 0 | L1 1 tables\n"},
		{[]string{"get", "a"}, "uno\n"},
		{[]string{"verify"}, "OK: 1 table in 1 level(s) verified"},
		{[]string{"stats"}, "live data:           2 keys"},
		{[]string{"dump"}, "L1  1 table,"},
	}
	for _, s := range steps {
		if out, _ := run(exitOK, s.args...); !strings.Contains(out, s.wantOut) {
			t.Fatalf("lsmctl %q: stdout %q, want it to contain %q", s.args, out, s.wantOut)
		}
	}
	run(exitError, "get", "b") // deleted, and stays deleted after compaction
}

func TestLoad(t *testing.T) {
	_, run := store(t)
	run(exitOK, append(tiny, "load", "300", "-value-size", "20")...)
	out, _ := run(exitOK, "scan")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 300 {
		t.Fatalf("scan has %d keys, want 300", len(lines))
	}
	for i, line := range lines {
		key := loadKey(i)
		if want := key + "\t" + string(loadValue(key, 1, 20)); line != want {
			t.Fatalf("line %d = %q, want %q", i, line, want)
		}
	}
	// Overwriting with another seed changes every value.
	run(exitOK, append(tiny, "load", "-seed", "9", "300")...)
	if out, _ := run(exitOK, "get", loadKey(7)); out != string(loadValue(loadKey(7), 9, 100))+"\n" {
		t.Fatalf("after reload: get = %q", out)
	}
	if out, _ := run(exitOK, append(tiny, "dump")...); !strings.Contains(out, "cursor") || !strings.Contains(out, "->") {
		t.Fatalf("dump after a load should show a merge cursor:\n%s", out)
	}
}

// With -sync, load prints each key once it is acknowledged; the order
// comes from the seed unless -sequential.
func TestLoadSyncPrintsAcknowledgedKeys(t *testing.T) {
	acked := func(args ...string) []string {
		_, run := store(t)
		out, _ := run(exitOK, append([]string{"-sync", "load", "20"}, args...)...)
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		return slices.DeleteFunc(lines, func(l string) bool { return !strings.HasPrefix(l, "key-") })
	}
	seq := acked("-sequential")
	for i, k := range seq {
		if k != loadKey(i) {
			t.Fatalf("-sequential: key %d is %q", i, k)
		}
	}
	a, b, c := acked("-seed", "3"), acked("-seed", "3"), acked("-seed", "4")
	if !slices.Equal(a, b) || slices.Equal(a, c) || slices.Equal(a, seq) {
		t.Fatalf("same seed must give the same order, different seeds a different one:\n%v\n%v\n%v", a, b, c)
	}
	if !slices.Equal(slices.Sorted(slices.Values(a)), seq) {
		t.Fatalf("a shuffled load must write the same keys: %v", a)
	}
}

// An interrupted load (Ctrl-C) stops between writes and closes the store
// cleanly.
func TestLoadInterrupted(t *testing.T) {
	root, _ := store(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, _, stderr := lsmctlIn(ctx, root, "load", "10")
	if code != exitError || !strings.Contains(stderr, "load interrupted after 0 of 10 keys") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// A store in use by another opener is a known edge case, not a bug.
func TestLockedStoreIsFriendly(t *testing.T) {
	root, run := store(t)
	db, err := lsmtree.Open(filepath.Join(root, "data"), lsmtree.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, stderr := run(exitError, "get", "k"); !strings.Contains(stderr, "another process is using it") {
		t.Fatalf("stderr %q", stderr)
	}
}

// A damaged table is reported by verify as corruption, with a log ID.
func TestCorruptTableIsFriendly(t *testing.T) {
	root, run := store(t)
	run(exitOK, "load", "2000")
	tables, _ := filepath.Glob(filepath.Join(root, "data", "*.sst"))
	if len(tables) == 0 {
		t.Fatal("no tables after load")
	}
	b, err := os.ReadFile(tables[0])
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/4] ^= 0xff // inside a data block
	if err := os.WriteFile(tables[0], b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr := run(exitError, "verify")
	if !strings.Contains(stderr, "corrupt") || !logIDRE.MatchString(stderr) {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestLogFileNextToStore(t *testing.T) {
	root := t.TempDir()
	var stderr bytes.Buffer
	if code := realMain(context.Background(), []string{"-dir", filepath.Join(root, "a", "data"), "stats"}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a", "lsmctl.log")); err != nil {
		t.Fatalf("log not next to the store: %v", err)
	}
}

func TestShow(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"with space", "with space"},
		{"ünï-键", "ünï-键"},
		{"", `""`},
		{`"quoted"`, `"\"quoted\""`},
		{`mid"quote`, `mid"quote`},
		{"tab\there", `"tab\there"`},
		{"new\nline", `"new\nline"`},
		{"\x00", `"\x00"`},
		{"\xff", `"\xff"`},
	}
	for _, tt := range tests {
		if got := show([]byte(tt.in)); got != tt.want {
			t.Errorf("show(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestDumpMarksNext checks that dump marks the table Stats names in
// LevelStats.Next, and draws L0 against Stats.L0Trigger. Whether Next is
// really the next compaction is the lsmtree package's test.
func TestDumpMarksNext(t *testing.T) {
	table := func(num uint64, lo, hi string) lsmtree.TableInfo {
		return lsmtree.TableInfo{Num: num, Size: 100, Entries: 1, Smallest: []byte(lo), Largest: []byte(hi)}
	}
	s := lsmtree.Stats{L0Trigger: 3, Levels: []lsmtree.LevelStats{
		{Files: 1, Next: -1, Tables: []lsmtree.TableInfo{table(9, "a", "z")}},
		{Files: 3, Target: 1000, Next: 1, Cursor: []byte("c"),
			Tables: []lsmtree.TableInfo{table(4, "a", "c"), table(5, "d", "f"), table(6, "g", "i")}},
		{Next: -1},
	}}
	var b strings.Builder
	printDump(&b, s)
	out := b.String()
	for _, want := range []string{"compacts at 3 tables", "next merge into L2 takes #5", "  -> #5 "} {
		if !strings.Contains(out, want) {
			t.Errorf("dump has no %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "->"); n != 1 {
		t.Errorf("dump marks %d tables, want 1:\n%s", n, out)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"4096", 4096, true},
		{"8k", 8 << 10, true},
		{"8K", 8 << 10, true},
		{"8KiB", 8 << 10, true},
		{"8kb", 8 << 10, true},
		{"8b", 8, true},
		{"8kbb", 0, false}, // one "b" or "ib" suffix, no more
		{"1m", 1 << 20, true},
		{"2g", 2 << 30, true},
		{"", 0, false},
		{"k", 0, false},
		{"-1", 0, false},
		{"1.5k", 0, false},
		{"lots", 0, false},
		{"9999999999g", 0, false}, // overflows
	}
	for _, tt := range tests {
		got, err := parseSize(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("parseSize(%q) = %d, %v; want %d, ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestHuman(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"}, {5 << 30, "5.0 GiB"},
	}
	for _, tt := range tests {
		if got := human(tt.in); got != tt.want {
			t.Errorf("human(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
