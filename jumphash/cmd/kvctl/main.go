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
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/shared/cli"
	"github.com/nickstrad/systems-learnings/shared/errs"
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

// Exit codes, shared by every command in the repo (see shared/cli).
const (
	exitOK    = cli.ExitOK
	exitError = cli.ExitError
	exitUsage = cli.ExitUsage
	exitBug   = cli.ExitBug
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
	logger, closeLog := cli.OpenLog(*logPath, stderr)
	defer closeLog()
	logger = logger.With("command", fs.Arg(0), "args", fs.Args()[1:])
	r := cli.Reporter{Logger: logger, Stderr: stderr, LogPath: *logPath, Modules: []string{cluster.Module, module}}

	// A panic is a bug, often a failed assertion. Report it like any other
	// bug instead of letting Go print a raw goroutine dump.
	defer r.Recover(&code)

	start := time.Now()
	if err := runCommand(ctx, logger, stdout, *clusterPath, *verbose, fs.Arg(0), fs.Args()[1:]); err != nil {
		return r.Report(err)
	}
	logger.Info("command ok", "duration_ms", time.Since(start).Milliseconds())
	return exitOK
}

// runCommand is run, swappable so tests can inject a panic.
var runCommand = run

// userError is a well-formed kvctl error for bad command-line input.
func userError(format string, args ...any) error {
	return errs.Wrap(module, nil, format, args...)
}
