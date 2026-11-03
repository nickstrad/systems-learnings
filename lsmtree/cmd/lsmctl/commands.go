package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickstrad/systems-learnings/lsmtree"
)

// session is one run's settings, parsed from the global flags.
type session struct {
	dir     string
	opts    lsmtree.Options
	verbose bool

	stdout, stderr io.Writer
	logger         *slog.Logger
}

// run executes one command. It returns errors from module "lsmtree", or
// from this module (userError) for bad command-line input. Any other error
// is a bug.
func run(ctx context.Context, c *session, cmd string, args []string) error {
	if cmd == "load" {
		return c.load(ctx, args) // load parses its own flags first
	}

	// Each case picks the command's argument count and its work on the
	// open store. The closures read args only after the count is checked.
	var fn func(*lsmtree.DB) error
	arity := 0
	switch cmd {
	case "put":
		arity, fn = 2, func(db *lsmtree.DB) error {
			if err := db.Put([]byte(args[0]), []byte(args[1])); err != nil {
				return err
			}
			fmt.Fprintln(c.stdout, "OK")
			return nil
		}

	case "get":
		arity, fn = 1, func(db *lsmtree.DB) error {
			v, ok, err := db.Get([]byte(args[0]))
			if err != nil {
				return err
			}
			if !ok {
				return userError("key %q not found", args[0])
			}
			fmt.Fprintf(c.stdout, "%s\n", v)
			return nil
		}

	case "del":
		arity, fn = 1, func(db *lsmtree.DB) error {
			if err := db.Delete([]byte(args[0])); err != nil {
				return err
			}
			fmt.Fprintln(c.stdout, "OK")
			return nil
		}

	case "scan":
		if len(args) > 2 {
			return userError("scan: want [start [end]], got %d arguments (see lsmctl -h)", len(args))
		}
		arity = len(args)
		var start, end []byte
		if len(args) > 0 {
			start = []byte(args[0])
		}
		if len(args) > 1 {
			end = []byte(args[1])
		}
		fn = func(db *lsmtree.DB) error {
			// A scan can print the whole store: buffer it. Like every
			// other command, lsmctl ignores errors writing to stdout.
			w := bufio.NewWriter(c.stdout)
			defer w.Flush()
			return db.Scan(start, end, func(k, v []byte) bool {
				fmt.Fprintf(w, "%s\t%s\n", show(k), show(v))
				return true
			})
		}

	case "flush", "compact":
		fn = func(db *lsmtree.DB) error {
			f := db.Flush
			if cmd == "compact" {
				f = db.Compact
			}
			if err := f(); err != nil {
				return err
			}
			fmt.Fprintf(c.stdout, "OK: %s\n", shape(db.Stats()))
			return nil
		}

	case "stats":
		fn = func(db *lsmtree.DB) error {
			// Stats knows table sizes but not how much of them is live
			// data (overwritten and deleted keys still take space until
			// compaction drops them), so stats scans the store on purpose
			// to count live keys and bytes for space amplification.
			var live, liveBytes int64
			err := db.Scan(nil, nil, func(k, v []byte) bool {
				live++
				liveBytes += int64(len(k) + len(v))
				return true
			})
			if err != nil {
				return err
			}
			printStats(c.stdout, db.Stats(), live, liveBytes)
			return nil
		}

	case "dump":
		fn = func(db *lsmtree.DB) error {
			printDump(c.stdout, db.Stats())
			return nil
		}

	case "verify":
		fn = func(db *lsmtree.DB) error {
			if err := db.Verify(); err != nil {
				return err
			}
			s := db.Stats()
			n, levels := 0, 0
			for _, l := range s.Levels {
				n += l.Files
				if l.Files > 0 {
					levels++
				}
			}
			fmt.Fprintf(c.stdout, "OK: %s in %d level(s) verified (checksums, key order, key ranges)\n", tables(n), levels)
			return nil
		}

	default:
		return userError("unknown command %q (see lsmctl -h)", cmd)
	}
	if len(args) != arity {
		return userError("%s: want %d argument(s), got %d (see lsmctl -h)", cmd, arity, len(args))
	}
	return c.withDB(fn)
}

// withDB opens the store, runs fn, and closes the store. fn's error wins;
// a Close error is returned only if fn succeeded.
func (c *session) withDB(fn func(*lsmtree.DB) error) error {
	opts := c.opts
	opts.Events = c.event
	db, err := lsmtree.Open(c.dir, opts)
	if err != nil {
		return err
	}
	err = fn(db)
	if cerr := db.Close(); err == nil {
		err = cerr
	} else if cerr != nil {
		c.logger.Warn("close failed after an earlier error", "error", cerr.Error())
	}
	return err
}

// event logs each flush, compaction, and recovery, the state changes
// worth an audit trail. With -v it also prints them.
func (c *session) event(e lsmtree.Event) {
	c.logger.Info(string(e.Kind), "from_level", e.FromLevel, "to_level", e.ToLevel,
		"input_files", e.InputFiles, "output_files", e.OutputFiles,
		"bytes_read", e.BytesRead, "bytes_written", e.BytesWritten,
		"entries_in", e.EntriesIn, "entries_out", e.EntriesOut,
		"tombstones_dropped", e.TombstonesDropped, "wal_records", e.WALRecords,
		"wal_dropped_bytes", e.WALDroppedBytes, "duration_ms", e.Duration.Milliseconds())
	if !c.verbose {
		return
	}
	switch e.Kind {
	case lsmtree.EventFlush:
		fmt.Fprintf(c.stderr, "flush: memtable -> L0, %d entries, %s\n", e.EntriesOut, human(e.BytesWritten))
	case lsmtree.EventCompaction:
		fmt.Fprintf(c.stderr, "compaction: L%d -> L%d, %d tables in, %d out, %s read, %s written\n",
			e.FromLevel, e.ToLevel, e.InputFiles, e.OutputFiles, human(e.BytesRead), human(e.BytesWritten))
	case lsmtree.EventRecovery:
		fmt.Fprintf(c.stderr, "recovery: replayed %d WAL records\n", e.WALRecords)
	}
}

// load writes n keys. Keys are key-00000000 .. key-<n-1>; the seed
// shuffles their order, and each value is loadValue(key, seed, size), so
// a test can check any key it was told was written. With -sync each
// acknowledged key is printed, one per line, as soon as Put returns.
func (c *session) load(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	seed := fs.Uint64("seed", 1, "")
	sequential := fs.Bool("sequential", false, "")
	valueSize := fs.Int("value-size", 100, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return userError("load: %v (see lsmctl -h)", err)
	}
	if len(pos) != 1 {
		return userError("load: want <n> [-seed s] [-sequential] [-value-size b] (see lsmctl -h)")
	}
	n, err := strconv.Atoi(pos[0])
	if err != nil || n < 0 {
		return userError("load: %q is not a non-negative number", pos[0])
	}
	if *valueSize < 0 {
		return userError("load: -value-size %d must not be negative", *valueSize)
	}

	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if !*sequential {
		rng := rand.New(rand.NewPCG(*seed, *seed))
		rng.Shuffle(n, func(i, j int) { order[i], order[j] = order[j], order[i] })
	}

	return c.withDB(func(db *lsmtree.DB) error {
		start := time.Now()
		for i, k := range order {
			if ctx.Err() != nil {
				return userError("load interrupted after %d of %d keys; those are saved", i, n)
			}
			key := loadKey(k)
			if err := db.Put([]byte(key), loadValue(key, *seed, *valueSize)); err != nil {
				return err
			}
			if c.opts.SyncWrites {
				fmt.Fprintln(c.stdout, key) // acknowledged: durable from here on
			}
		}
		s := db.Stats()
		fmt.Fprintf(c.stdout, "wrote %d keys (%s) in %s: %d flush(es), %d compaction(s), write amplification %.2fx\n",
			n, human(s.UserBytesWritten), time.Since(start).Round(time.Millisecond),
			s.Flushes, s.Compactions, s.WriteAmplification())
		fmt.Fprintf(c.stdout, "  %s\n", shape(s))
		return nil
	})
}

// loadKey is the i-th key load writes.
func loadKey(i int) string { return fmt.Sprintf("key-%08d", i) }

// loadValue is the value load writes for key with seed: "<key>@<seed>",
// padded with '.' to at least size bytes. The e2e tests rebuild it to
// check every acknowledged key.
func loadValue(key string, seed uint64, size int) []byte {
	v := fmt.Appendf(nil, "%s@%d", key, seed)
	if len(v) < size {
		v = append(v, bytes.Repeat([]byte{'.'}, size-len(v))...)
	}
	return v
}

// parseInterspersed parses flags that may come before or after the
// positional arguments, as in `load 1000 -seed 7`, and returns the
// positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// show prints b as is when that is unambiguous, and Go-quoted otherwise:
// when it is empty, starts with a quote, or holds anything unprintable
// (tabs and newlines included, so scan's lines stay one per key).
func show(b []byte) string {
	s := string(b)
	if s == "" || strings.HasPrefix(s, `"`) || strings.IndexFunc(s, func(r rune) bool {
		return !strconv.IsPrint(r) || r == utf8.RuneError
	}) >= 0 {
		return strconv.Quote(s)
	}
	return s
}
