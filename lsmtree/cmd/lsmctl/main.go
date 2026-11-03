// Command lsmctl reads and writes an lsmtree store on disk, and shows its
// level tree. Run `lsmctl -h` for the command list.
//
// Each run is one process: it opens the store, runs one command, and
// closes it. Opening replays the write-ahead log left by the last run, so
// a sequence of runs shows that data really persists.
//
// This package is the top level in Cox-Buday's error propagation model
// ("Concurrency in Go", chapter 5):
//
//   - A well-formed error (the outermost error is from module "lsmtree" or
//     "lsmctl") is a known edge case. Its message is shown as is.
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
	"strconv"
	"strings"
	"time"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/shared/cli"
	"github.com/nickstrad/systems-learnings/shared/errs"
)

const usage = `usage: lsmctl [flags] <command> [args]

commands:
  put <key> <value>    write a key
  get <key>            print a key's value (exit 1 if it is absent)
  del <key>            delete a key (a missing key is not an error)
  scan [start [end]]   print key<TAB>value for keys in [start, end)
  load <n> [-seed s] [-sequential] [-value-size b]
                       write keys key-00000000 .. key-<n-1>, in an order
                       shuffled by the seed (or in order with -sequential);
                       each value is derived from its key and the seed
  flush                write the memtable to L0 now
  compact              compact until every level is within its target
  stats                level sizes against targets, live data, amplification
  dump                 the level tree: tables, key ranges, merge cursors
  verify               read every table, checking checksums and key order

flags (before the command):
  -dir path       the store's directory (default "data")
  -log file       log file (default: lsmctl.log next to the store directory)
  -v              print each flush and compaction to stderr
  -sync           fsync every write; load then prints each acknowledged key
  -memtable size  memtable size that triggers a flush (default 64KiB)
  -block size     SSTable data block size (default 4KiB)
  -table size     target size of a compaction output table (default 64KiB)
  -bloom n        Bloom filter bits per key (default 10)
  -no-bloom       turn Bloom filters off
  -l0-trigger n   L0 file count that triggers a compaction (default 4)
  -base size      target size of L1 (default 256KiB)
  -ratio n        size ratio between adjacent levels (default 10)
Sizes accept a k, m, or g suffix (powers of 1024), e.g. -memtable 8k.
Tuning flags apply to the run they are given to; they are not saved.

Scan and dump print keys and values as is, or Go-quoted ("...") if they
are empty, start with a quote, or hold a tab, newline, or other
unprintable byte.

Errors print a log ID; the full details are in the log file.`

// Exit codes, shared by every command in the repo (see shared/cli).
const (
	exitOK    = cli.ExitOK
	exitError = cli.ExitError
	exitUsage = cli.ExitUsage
	exitBug   = cli.ExitBug
)

// module is this package's name in well-formed errors.
const module = "lsmctl"

func main() {
	// Ctrl-C cancels ctx, which stops a load between writes; the store is
	// then closed cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(realMain(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// realMain is main without the process-level parts, so tests can call it.
func realMain(ctx context.Context, argv []string, stdout, stderr io.Writer) (code int) {
	fs := flag.NewFlagSet("lsmctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := &session{stdout: stdout, stderr: stderr}
	fs.StringVar(&c.dir, "dir", "data", "the store's directory")
	logPath := fs.String("log", "", "log file")
	fs.BoolVar(&c.verbose, "v", false, "print each flush and compaction")
	fs.BoolVar(&c.opts.SyncWrites, "sync", false, "fsync every write")
	var blockSize int64
	fs.Var(sizeFlag{&c.opts.MemtableSize}, "memtable", "memtable size")
	fs.Var(sizeFlag{&blockSize}, "block", "block size")
	fs.Var(sizeFlag{&c.opts.TableSize}, "table", "table size")
	fs.IntVar(&c.opts.BloomBitsPerKey, "bloom", 0, "Bloom bits per key")
	fs.BoolVar(&c.opts.NoBloom, "no-bloom", false, "no Bloom filters")
	fs.IntVar(&c.opts.L0CompactionTrigger, "l0-trigger", 0, "L0 compaction trigger")
	fs.Var(sizeFlag{&c.opts.BaseLevelSize}, "base", "L1 target size")
	fs.IntVar(&c.opts.LevelSizeRatio, "ratio", 0, "level size ratio")
	fs.Usage = func() { fmt.Fprintln(stderr, usage) }
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return exitUsage
	}
	c.opts.BlockSize = int(blockSize)
	if *logPath == "" {
		*logPath = filepath.Join(filepath.Dir(filepath.Clean(c.dir)), "lsmctl.log")
	}
	logger, closeLog := cli.OpenLog(*logPath, stderr)
	defer closeLog()
	c.logger = logger.With("command", fs.Arg(0), "args", fs.Args()[1:], "dir", c.dir)
	r := cli.Reporter{Logger: c.logger, Stderr: stderr, LogPath: *logPath, Modules: []string{lsmtree.Module, module}}

	// A panic is a bug, often a failed assertion. Report it like any other
	// bug instead of letting Go print a raw goroutine dump.
	defer r.Recover(&code)

	start := time.Now()
	if err := runCommand(ctx, c, fs.Arg(0), fs.Args()[1:]); err != nil {
		return r.Report(err)
	}
	c.logger.Info("command ok", "duration_ms", time.Since(start).Milliseconds())
	return exitOK
}

// runCommand is run, swappable so tests can inject a panic.
var runCommand = run

// userError is a well-formed lsmctl error for bad command-line input.
func userError(format string, args ...any) error {
	return errs.Wrap(module, nil, format, args...)
}

// sizeFlag is a flag holding a byte count, such as 4096, 8k, or 1m.
type sizeFlag struct{ p *int64 }

func (s sizeFlag) String() string {
	if s.p == nil {
		return "0"
	}
	return strconv.FormatInt(*s.p, 10)
}

func (s sizeFlag) Set(v string) error {
	n, err := parseSize(v)
	if err != nil {
		return err
	}
	*s.p = n
	return nil
}

// parseSize reads a non-negative byte count with an optional k, m, or g
// suffix (powers of 1024; "kib" and "kb" are accepted too).
func parseSize(s string) (int64, error) {
	num := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(s), "b"), "i")
	mult := int64(1)
	if num != "" {
		switch num[len(num)-1] {
		case 'k':
			mult = 1 << 10
		case 'm':
			mult = 1 << 20
		case 'g':
			mult = 1 << 30
		}
		if mult > 1 {
			num = num[:len(num)-1]
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf("%q is not a size (want e.g. 4096, 8k, or 1m)", s)
	}
	return n * mult, nil
}
