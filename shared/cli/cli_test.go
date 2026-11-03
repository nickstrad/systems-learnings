package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/shared/cli"
	"github.com/nickstrad/systems-learnings/shared/errs"
)

func newReporter() (cli.Reporter, *bytes.Buffer, *bytes.Buffer) {
	var log, stderr bytes.Buffer
	return cli.Reporter{
		Logger:  slog.New(slog.NewJSONHandler(&log, nil)),
		Stderr:  &stderr,
		LogPath: "x.log",
		Modules: []string{"store", "ctl"},
	}, &log, &stderr
}

var logID = regexp.MustCompile(`log ID ([0-9a-f]{8})`)

// logged returns the JSON log line whose log_id matches the one shown on
// stderr, failing if there isn't exactly one.
func logged(t *testing.T, log, stderr *bytes.Buffer) map[string]any {
	t.Helper()
	m := logID.FindStringSubmatch(stderr.String())
	if m == nil {
		t.Fatalf("stderr shows no log ID: %q", stderr)
	}
	var found map[string]any
	for line := range strings.Lines(log.String()) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if entry["log_id"] == m[1] {
			if found != nil {
				t.Fatalf("two log lines have ID %s", m[1])
			}
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("no log line has the ID %s shown to the user", m[1])
	}
	return found
}

func TestReportKnownModulesAreEdgeCases(t *testing.T) {
	for _, module := range []string{"store", "ctl"} {
		r, log, stderr := newReporter()
		err := errs.Wrap(module, errors.New("disk full"), "cannot save")
		if code := r.Report(err); code != cli.ExitError {
			t.Fatalf("%s error: exit %d, want ExitError", module, code)
		}
		if !strings.HasPrefix(stderr.String(), "error: cannot save [log ID ") {
			t.Fatalf("stderr = %q; want the message and a log ID", stderr)
		}
		entry := logged(t, log, stderr)
		if entry["level"] != "WARN" || entry["chain"] == nil || entry["stack"] == nil {
			t.Fatalf("log line lacks level, chain, or stack: %v", entry)
		}
	}
}

func TestReportEverythingElseIsABug(t *testing.T) {
	cases := map[string]error{
		"raw error":        errors.New("boom"),
		"unknown module":   errs.Wrap("elsewhere", nil, "not ours"),
		"hidden by a join": errors.Join(errs.Wrap("store", nil, "hidden inside")),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			r, log, stderr := newReporter()
			if code := r.Report(err); code != cli.ExitBug {
				t.Fatalf("exit %d, want ExitBug", code)
			}
			if !strings.Contains(stderr.String(), "This is a bug") || !strings.Contains(stderr.String(), "x.log") {
				t.Fatalf("stderr = %q", stderr)
			}
			if entry := logged(t, log, stderr); entry["level"] != "ERROR" {
				t.Fatalf("bug logged at %v", entry["level"])
			}
		})
	}
}

func TestRecoverTurnsAPanicIntoABug(t *testing.T) {
	r, log, stderr := newReporter()
	code := func() (code int) {
		defer r.Recover(&code)
		panic("assertion failed: x=7")
	}()
	if code != cli.ExitBug {
		t.Fatalf("exit %d, want ExitBug", code)
	}
	entry := logged(t, log, stderr)
	if !strings.Contains(entry["error"].(string), "x=7") || !strings.Contains(entry["stack"].(string), "TestRecover") {
		t.Fatalf("log line lacks the panic or its stack: %v", entry)
	}
}

func TestRecoverWithoutAPanicLeavesTheCode(t *testing.T) {
	r, _, _ := newReporter()
	code := func() (code int) {
		defer r.Recover(&code)
		return cli.ExitError
	}()
	if code != cli.ExitError {
		t.Fatalf("exit %d, want the function's own ExitError", code)
	}
}

func TestOpenLog(t *testing.T) {
	t.Run("creates the directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sub", "dir", "app.log")
		var stderr bytes.Buffer
		logger, closeLog := cli.OpenLog(path, &stderr)
		logger.Info("hello")
		closeLog()
		b, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(b), `"msg":"hello"`) || stderr.Len() != 0 {
			t.Fatalf("log file = %q, %v; stderr = %q", b, err, stderr.String())
		}
	})
	t.Run("falls back to stderr", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		os.WriteFile(blocker, nil, 0o644)
		var stderr bytes.Buffer
		logger, closeLog := cli.OpenLog(filepath.Join(blocker, "app.log"), &stderr)
		logger.Info("still logged")
		closeLog()
		if !strings.Contains(stderr.String(), "warning: cannot open log file") || !strings.Contains(stderr.String(), "still logged") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})
}
