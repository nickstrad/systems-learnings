// Package cli is the top level of every command in this repo, following
// Cox-Buday's error propagation model ("Concurrency in Go", chapter 5):
//
//   - A well-formed error, whose outermost layer is from one of the
//     command's known modules, is a known edge case. Its message is shown
//     as is, and the command exits with ExitError.
//   - Anything else (a raw error, or a panic such as a failed assertion) is
//     a bug. The user gets a short "this is a bug" message and ExitBug.
//   - Either way, the full error (chain, stack, context, UTC time) goes to a
//     JSON log file, and the user sees a log ID to find it.
//
// A command's realMain opens the log with OpenLog, builds a Reporter, and
// defers Reporter.Recover; see jumphash/cmd/kvctl and lsmtree/cmd/lsmctl.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"

	"github.com/nickstrad/systems-learnings/shared/errs"
	"github.com/nickstrad/systems-learnings/shared/logx"
)

// Exit codes, the same for every command.
const (
	ExitOK    = 0
	ExitError = 1  // a known edge case: bad input, a server down, a missing key, ...
	ExitUsage = 2  // no command, or a bad flag
	ExitBug   = 70 // EX_SOFTWARE: an unexpected error or a panic
)

// OpenLog opens path for appending JSON log lines, creating its directory
// if needed. If that fails, the command still works and logs go to stderr
// instead. Call the returned function to close the log.
func OpenLog(path string, stderr io.Writer) (*slog.Logger, func()) {
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: cannot open log file %s (%v); logging to stderr\n", path, err)
		return logx.New(stderr, slog.LevelInfo), func() {}
	}
	return logx.New(f, slog.LevelInfo), func() {
		if err := f.Close(); err != nil {
			fmt.Fprintf(stderr, "warning: log file %s may be incomplete: %v\n", path, err)
		}
	}
}

// Reporter turns errors that reach the top level into what the user sees
// and what the log keeps.
type Reporter struct {
	Logger  *slog.Logger
	Stderr  io.Writer
	LogPath string   // shown to the user with a bug's log ID
	Modules []string // modules whose well-formed errors are known edge cases
}

// Report handles an error that reached the top level and returns the
// exit code.
func (r Reporter) Report(err error) int {
	if e, ok := errs.Outermost(err); ok && slices.Contains(r.Modules, e.Module) {
		id := r.log(slog.LevelWarn, "command failed", err, errs.DeepestStack(err))
		fmt.Fprintf(r.Stderr, "error: %s [log ID %s]\n", e.Message, id)
		return ExitError
	}
	return r.Bug(err, errs.DeepestStack(err))
}

// Bug reports an error that no known module wrapped, or a panic, and
// returns ExitBug.
func (r Reporter) Bug(err error, stack string) int {
	id := r.log(slog.LevelError, "unexpected error (bug)", err, stack)
	fmt.Fprintf(r.Stderr, "error: something unexpected went wrong. This is a bug; "+
		"please report it with log ID %s (details in %s)\n", id, r.LogPath)
	return ExitBug
}

// Recover reports a panic as a bug and sets *code to ExitBug, instead of
// letting Go print a raw goroutine dump. Use it directly with defer:
//
//	defer r.Recover(&code)
func (r Reporter) Recover(code *int) {
	if p := recover(); p != nil {
		*code = r.Bug(fmt.Errorf("panic: %v", p), string(debug.Stack()))
	}
}

// log writes one line with everything known about err, and returns its ID.
func (r Reporter) log(level slog.Level, msg string, err error, stack string) string {
	id := logx.NewID()
	attrs := []slog.Attr{
		slog.String("log_id", id),
		slog.String("error", err.Error()),
		slog.Any("chain", errs.Chain(err)),
	}
	if stack != "" {
		attrs = append(attrs, slog.String("stack", stack))
	}
	r.Logger.LogAttrs(context.Background(), level, msg, attrs...)
	return id
}
