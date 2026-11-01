// Command kvctl manages a cluster of kvnodes and reads/writes through it.
// Run `kvctl -h` for the command list.
//
// This package is the top level in Cox-Buday's error propagation model
// ("Concurrency in Go", chapter 5):
//
//   - A well-formed error (the outermost error is from module "cluster" or
//     "kvctl") is a known edge case. Its message is shown as is.
//   - Anything else (a raw error, or a panic such as a failed assertion) is
//     a bug. The user gets a short "this is a bug" message.
//   - Either way, the full error (chain, stack, context, UTC time) goes to a
//     JSON log file, and the user sees a log ID to find it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
	"github.com/nickstrad/systems-learnings/jumphash/internal/logx"
)

const usage = `usage: kvctl [-cluster file] [-log file] [-v] <command> [args]

commands:
  add-node <addr>      add a running node and move keys onto it
  remove-node <addr>   move a node's keys elsewhere and drop it
  nodes                list nodes in ring order
  stats                keys per node, plus keys on the wrong node
  put <key> <value>    write a key to its owner
  get <key>            read a key from its owner
  del <key>            delete a key
  where <key>          print the node that owns a key
  fill <n> [prefix]    write n keys, prefix-0 .. prefix-(n-1), for demos

Errors print a log ID; the full details are in the log file
(default: kvctl.log next to the cluster file).`

// Exit codes.
const (
	exitOK    = 0
	exitError = 1  // a known edge case: bad input, node down, ...
	exitUsage = 2  // no command given
	exitBug   = 70 // EX_SOFTWARE: an unexpected error or a panic
)

// module is this package's name in well-formed errors.
const module = "kvctl"

func main() {
	// Ctrl-C cancels ctx, which stops in-flight requests and scans.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(realMain(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// realMain is main without the process-level parts, so tests can call it.
func realMain(ctx context.Context, argv []string, stdout, stderr io.Writer) (code int) {
	fs := flag.NewFlagSet("kvctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	clusterPath := fs.String("cluster", "cluster.json", "membership file")
	logPath := fs.String("log", "", "log file (default: kvctl.log next to the cluster file)")
	verbose := fs.Bool("v", false, "print every moved key")
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return exitUsage
	}
	if *logPath == "" {
		*logPath = filepath.Join(filepath.Dir(*clusterPath), "kvctl.log")
	}
	logger, closeLog := openLog(*logPath, stderr)
	defer closeLog()
	logger = logger.With("command", fs.Arg(0), "args", fs.Args()[1:])
	r := reporter{logger: logger, stderr: stderr, logPath: *logPath}

	// A panic is a bug, often a failed assertion. Report it like any other
	// bug instead of letting Go print a raw goroutine dump.
	defer func() {
		if p := recover(); p != nil {
			code = r.bug(fmt.Errorf("panic: %v", p), string(debug.Stack()))
		}
	}()

	start := time.Now()
	if err := runCommand(ctx, logger, stdout, *clusterPath, *verbose, fs.Arg(0), fs.Args()[1:]); err != nil {
		return r.report(err)
	}
	logger.Info("command ok", "duration_ms", time.Since(start).Milliseconds())
	return exitOK
}

// runCommand is run, swappable so tests can inject a panic.
var runCommand = run

// reporter turns errors into what the user sees and what the log keeps.
type reporter struct {
	logger  *slog.Logger
	stderr  io.Writer
	logPath string
}

// report handles an error that reached the top level.
func (r reporter) report(err error) int {
	if e, ok := errs.Outermost(err); ok && (e.Module == cluster.Module || e.Module == module) {
		id := r.log(slog.LevelWarn, "command failed", err, errs.DeepestStack(err))
		fmt.Fprintf(r.stderr, "error: %s [log ID %s]\n", e.Message, id)
		return exitError
	}
	return r.bug(err, errs.DeepestStack(err))
}

// bug reports an error that no module wrapped, or a panic.
func (r reporter) bug(err error, stack string) int {
	id := r.log(slog.LevelError, "unexpected error (bug)", err, stack)
	fmt.Fprintf(r.stderr, "error: something unexpected went wrong. This is a bug; "+
		"please report it with log ID %s (details in %s)\n", id, r.logPath)
	return exitBug
}

// log writes one line with everything known about err, and returns its ID.
func (r reporter) log(level slog.Level, msg string, err error, stack string) string {
	id := logx.NewID()
	attrs := []slog.Attr{
		slog.String("log_id", id),
		slog.String("error", err.Error()),
		slog.Any("chain", errs.Chain(err)),
	}
	if stack != "" {
		attrs = append(attrs, slog.String("stack", stack))
	}
	r.logger.LogAttrs(context.Background(), level, msg, attrs...)
	return id
}

// openLog opens the log file for appending. If that fails, the program
// still works, and logs go to stderr instead.
func openLog(path string, stderr io.Writer) (*slog.Logger, func()) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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

// userError is a well-formed kvctl error for bad command-line input.
func userError(format string, args ...any) error {
	return errs.Wrap(module, nil, format, args...)
}
