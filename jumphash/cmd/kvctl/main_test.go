package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
	"github.com/nickstrad/systems-learnings/jumphash/internal/kvhttp"
	"github.com/nickstrad/systems-learnings/shared/cli"
)

var logIDRE = regexp.MustCompile(`log ID ([0-9a-f]{8})`)

// kvctlIn runs realMain against the cluster file in dir.
func kvctlIn(dir string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = realMain(context.Background(), append([]string{"-cluster", filepath.Join(dir, "c.json")}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// kvctl runs realMain against a fresh temp cluster file and returns the exit
// code, stderr, and the log lines.
func kvctl(t *testing.T, args ...string) (code int, stderr string, logLines []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	code, _, stderr = kvctlIn(dir, args...)
	b, _ := os.ReadFile(filepath.Join(dir, "kvctl.log"))
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		logLines = append(logLines, m)
	}
	return code, stderr, logLines
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

func TestKnownEdgeCaseShowsMessageAndLogID(t *testing.T) {
	code, stderr, lines := kvctl(t, "put", "k", "v") // no nodes yet
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "the cluster has no nodes; add one first") {
		t.Fatalf("stderr: %q", stderr)
	}
	l := findLog(t, stderr, lines)
	if l["level"] != "WARN" || l["chain"] == nil || l["time"] == nil {
		t.Fatalf("log line missing details: %v", l)
	}
	if ts, _ := l["time"].(string); !strings.HasSuffix(ts, "Z") {
		t.Fatalf("log time %q is not UTC", ts)
	}
}

func TestBadInputIsWellFormed(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"put", "only-one-arg"}, {"fill", "x"}} {
		code, stderr, _ := kvctl(t, args...)
		if code != exitError || strings.Contains(stderr, "bug") {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestRawErrorIsReportedAsBug(t *testing.T) {
	var log bytes.Buffer
	r := cli.Reporter{Logger: slog.New(slog.NewJSONHandler(&log, nil)), Stderr: io.Discard, LogPath: "x.log", Modules: []string{module}}
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
	runCommand = func(context.Context, *slog.Logger, io.Writer, string, bool, string, []string) error {
		panic("assertion failed: something impossible")
	}

	code, stderr, lines := kvctl(t, "nodes")
	if code != exitBug {
		t.Fatalf("exit %d, want %d", code, exitBug)
	}
	if strings.Contains(stderr, "goroutine") || !strings.Contains(stderr, "This is a bug") {
		t.Fatalf("user should see a short bug message, got %q", stderr)
	}
	l := findLog(t, stderr, lines)
	if !strings.Contains(l["error"].(string), "assertion failed") || l["stack"] == nil {
		t.Fatalf("log should hold the panic and stack: %v", l)
	}
}

// liveCluster starts two real kvnode handlers in-process, adds them to a
// fresh cluster file, and returns a runner bound to that cluster.
func liveCluster(t *testing.T) func(args ...string) (code int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	for range 2 {
		srv := httptest.NewServer(kvhttp.NewHandler(kv.NewStore(), slog.New(slog.NewTextHandler(io.Discard, nil))))
		t.Cleanup(srv.Close)
		if code, _, stderr := kvctlIn(dir, "add-node", srv.URL); code != exitOK {
			t.Fatalf("add-node: exit %d, stderr %q", code, stderr)
		}
	}
	return func(args ...string) (int, string, string) { return kvctlIn(dir, args...) }
}

// step is one kvctl invocation and what it must do. wantOut and wantErr are
// substrings; "" means don't check.
type step struct {
	args    []string
	code    int
	wantOut string
	wantErr string
}

func runSteps(t *testing.T, steps []step) {
	t.Helper()
	run := liveCluster(t)
	for _, s := range steps {
		code, stdout, stderr := run(s.args...)
		if code != s.code {
			t.Fatalf("kvctl %q: exit %d, want %d (stdout %q, stderr %q)", s.args, code, s.code, stdout, stderr)
		}
		if !strings.Contains(stdout, s.wantOut) || !strings.Contains(stderr, s.wantErr) {
			t.Fatalf("kvctl %q: stdout %q, stderr %q; want them to contain %q and %q", s.args, stdout, stderr, s.wantOut, s.wantErr)
		}
	}
}

// Values: empty versus non-empty, and one write versus an overwrite.
func TestPutGetValues(t *testing.T) {
	tests := []struct {
		name  string
		steps []step
	}{
		{"empty value is found, not missing", []step{
			{[]string{"put", "k", ""}, exitOK, "OK", ""},
			{[]string{"get", "k"}, exitOK, "\n", ""},
			{[]string{"del", "k"}, exitOK, "OK", ""},
			{[]string{"get", "k"}, exitError, "", "not found"},
		}},
		{"overwrite: second put wins", []step{
			{[]string{"put", "k", "one"}, exitOK, "OK", ""},
			{[]string{"put", "k", "two"}, exitOK, "OK", ""},
			{[]string{"get", "k"}, exitOK, "two\n", ""},
		}},
		{"overwrite a value with an empty one", []step{
			{[]string{"put", "k", "one"}, exitOK, "OK", ""},
			{[]string{"put", "k", ""}, exitOK, "OK", ""},
			{[]string{"get", "k"}, exitOK, "\n", ""},
		}},
		{"missing key", []step{
			{[]string{"get", "nope"}, exitError, "", `key "nope" not found`},
		}},
		{"del of a missing key is not an error", []step{
			{[]string{"del", "nope"}, exitOK, "OK", ""},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runSteps(t, tt.steps) })
	}
}

// Keys with characters that need escaping must survive the CLI, the client,
// and the server unchanged.
func TestSpecialCharacterKeys(t *testing.T) {
	keys := []string{"a/b", "a//b", "/lead", "trail/", "with space", "50%", "a%2Fb", "ünï-键", "q?x=1", "frag#ment", "a+b", ".", ".."}
	run := liveCluster(t)
	for _, k := range keys {
		if code, _, stderr := run("put", k, "v:"+k); code != exitOK {
			t.Fatalf("put %q: exit %d, stderr %q", k, code, stderr)
		}
	}
	for _, k := range keys {
		code, stdout, stderr := run("get", k)
		if code != exitOK || stdout != "v:"+k+"\n" {
			t.Fatalf("get %q: exit %d, stdout %q, stderr %q", k, code, stdout, stderr)
		}
	}
	// Keys that differ only in escaping are different keys.
	if code, _, _ := run("get", "a b"); code != exitError {
		t.Fatalf(`"a b" must not alias another key, exit %d`, code)
	}
}

// Argument errors. Bad arguments are known edge cases: exit 1 with a clear
// message and no "bug" wording. Exit 2 is only for no command or a bad flag.
func TestArgumentErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		code    int
		wantErr string
	}{
		{"no command", nil, exitUsage, "usage: kvctl"},
		{"unknown flag", []string{"-bogus", "nodes"}, exitUsage, "usage: kvctl"},
		{"unknown command", []string{"bogus"}, exitError, `unknown command "bogus"`},

		{"add-node: none", []string{"add-node"}, exitError, "add-node: want 1 argument(s), got 0"},
		{"add-node: too many", []string{"add-node", "a", "b"}, exitError, "add-node: want 1 argument(s), got 2"},
		{"remove-node: none", []string{"remove-node"}, exitError, "remove-node: want 1 argument(s), got 0"},
		{"remove-node: too many", []string{"remove-node", "a", "b"}, exitError, "remove-node: want 1 argument(s), got 2"},
		{"put: none", []string{"put"}, exitError, "put: want 2 argument(s), got 0"},
		{"put: only key", []string{"put", "k"}, exitError, "put: want 2 argument(s), got 1"},
		{"put: too many", []string{"put", "k", "v", "x"}, exitError, "put: want 2 argument(s), got 3"},
		{"put: empty key", []string{"put", "", "v"}, exitError, "key must not be empty"},
		{"get: none", []string{"get"}, exitError, "get: want 1 argument(s), got 0"},
		{"get: too many", []string{"get", "a", "b"}, exitError, "get: want 1 argument(s), got 2"},
		{"get: empty key", []string{"get", ""}, exitError, "key must not be empty"},
		{"del: none", []string{"del"}, exitError, "del: want 1 argument(s), got 0"},
		{"del: too many", []string{"del", "a", "b"}, exitError, "del: want 1 argument(s), got 2"},
		{"del: empty key", []string{"del", ""}, exitError, "key must not be empty"},
		{"where: none", []string{"where"}, exitError, "where: want 1 argument(s), got 0"},
		{"where: too many", []string{"where", "a", "b"}, exitError, "where: want 1 argument(s), got 2"},
		{"where: empty key", []string{"where", ""}, exitError, "key must not be empty"},
		{"fill: none", []string{"fill"}, exitError, "fill: want <n> [prefix]"},
		{"fill: too many", []string{"fill", "1", "p", "x"}, exitError, "fill: want <n> [prefix]"},
		{"fill: not a number", []string{"fill", "x"}, exitError, `"x" is not a non-negative number`},
		{"fill: negative", []string{"fill", "-1"}, exitError, `"-1" is not a non-negative number`},
		{"nodes: extra args are ignored", []string{"nodes", "extra"}, exitOK, ""},
		{"stats: extra args are ignored", []string{"stats", "extra"}, exitOK, ""},
	}
	run := liveCluster(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := run(tt.args...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tt.code, stderr)
			}
			if !strings.Contains(stderr, tt.wantErr) || strings.Contains(stderr, "This is a bug") {
				t.Fatalf("stderr %q, want it to contain %q and not report a bug", stderr, tt.wantErr)
			}
			if tt.code == exitError && !logIDRE.MatchString(stderr) {
				t.Fatalf("stderr %q has no log ID", stderr)
			}
		})
	}
}
