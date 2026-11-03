// Package lsmtree is a small log-structured merge-tree key-value store,
// built to explore O'Neil et al., "The Log-Structured Merge-Tree" (1996).
//
// Writes go to a write-ahead log and an in-memory memtable (the paper's
// C0). A full memtable is flushed to an immutable sorted file (an SSTable)
// in level 0. Compaction merges files down through levels L1, L2, ... of
// growing size (the paper's C1 … CK), using a rolling cursor per level.
// Reads check the memtable, then L0 newest first, then each level.
//
// It is a learning project, not production code. See DESIGN.md.
//
// # Concurrency
//
// One mutex guards the DB, and flushes and compactions run inside the
// write that triggers them. Every method is safe to call from several
// goroutines, but they take turns. This keeps every run deterministic,
// which the model-based fuzz tests rely on.
//
// # Failures
//
// If writing the WAL, a flush, or a compaction fails, the DB stops
// accepting writes: every later Put, Delete, Flush, or Compact returns
// the original error. Reads keep working. Close and reopen to recover;
// every acknowledged write is replayed from the WAL or found in a table.
package lsmtree

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/compaction"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/memtable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/wal"
	"github.com/nickstrad/systems-learnings/shared/assert"
	"github.com/nickstrad/systems-learnings/shared/errs"
)

// Module is the errs module name of every error this package returns.
// A caller can compare errs.Outermost(err).Module against it to tell this
// store's well-formed errors from bugs.
const Module = "lsmtree"

// Sentinel errors. Every error the DB returns is an *errs.Error from
// module "lsmtree"; errors.Is finds these through the wrapping.
var (
	ErrClosed       = errors.New("the database is closed")
	ErrInvalidKey   = fmt.Errorf("a key must be 1 to %d bytes", keys.MaxKeySize)
	ErrValueTooBig  = fmt.Errorf("a value must be at most %d bytes", keys.MaxValueSize)
	ErrCorrupt      = errors.New("the database files are corrupt")
	ErrBadOptions   = errors.New("invalid options")
	ErrWritesFailed = errors.New("an earlier write failed; reopen the database")
)

// DB is an open store. Create one with Open and release it with Close.
type DB struct {
	mu   sync.Mutex
	dir  string
	fs   vfs.FS
	opts Options
	lock io.Closer

	v      meta.Version               // the live files; mirrors the manifest on disk
	tables map[uint64]*sstable.Reader // an open reader for every file in v
	mem    *memtable.Memtable
	log    *wal.Writer // the WAL for mem, file v.LogNum

	failed  error // set when a write path fails; see "Failures" above
	closed  bool
	stats   Stats
	retired sstable.ReaderStats // read counters of readers already closed
}

// Open opens the store in dir, creating it if needed. It replays the WAL
// left by the last run, flushes what it recovered to an L0 table, and
// removes files no longer in use.
func Open(dir string, opts Options) (*DB, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return nil, errs.Wrap(Module, fmt.Errorf("%w: %w", ErrBadOptions, err), "cannot open %s: %v", dir, err)
	}
	db := &DB{dir: dir, fs: opts.FS, opts: opts, tables: map[uint64]*sstable.Reader{}}
	if err := db.open(); err != nil {
		db.closeFiles()
		return nil, errs.Wrap(Module, err, "cannot open %s: %v", dir, err)
	}
	return db, nil
}

func (db *DB) open() error {
	if err := db.fs.MkdirAll(db.dir); err != nil {
		return err
	}
	lock, err := db.fs.Lock(db.path("LOCK"))
	if err != nil {
		if errors.Is(err, vfs.ErrLocked) {
			return fmt.Errorf("another process is using it: %w", err)
		}
		return err
	}
	db.lock = lock

	v, err := manifest.Read(db.fs, db.dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		v = meta.Version{NextFileNum: 1}
	case errors.Is(err, manifest.ErrCorrupt):
		return fmt.Errorf("%w: %w", ErrCorrupt, err)
	case err != nil:
		return err
	}
	if len(v.Levels) > db.opts.MaxLevels {
		return fmt.Errorf("%w: the store has %d levels but the options allow %d", ErrBadOptions, len(v.Levels), db.opts.MaxLevels)
	}
	for len(v.Levels) < db.opts.MaxLevels {
		v.Levels = append(v.Levels, nil)
	}
	db.v = v

	for _, level := range v.Levels {
		for _, f := range level {
			if err := db.openTable(f.Num); err != nil {
				return err
			}
		}
	}
	return db.recover()
}

// recover replays the WAL into a memtable, writes it out as an L0 table,
// and starts a fresh WAL. Starting fresh keeps the WAL simple: it is only
// ever created and appended to, never reopened.
func (db *DB) recover() error {
	start := time.Now()
	db.mem = memtable.New(db.v.LastSeq + 1)
	var report wal.Report
	if db.v.LogNum != 0 {
		f, err := db.fs.Open(db.logPath(db.v.LogNum))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Created but never made durable before a crash: nothing in it
			// was acknowledged with SyncWrites, so it is empty.
		case err != nil:
			return err
		default:
			report, err = wal.Replay(f, func(e keys.Entry) error {
				if e.Seq <= db.v.LastSeq {
					return fmt.Errorf("%w: WAL record seq %d is not above the manifest's last seq %d", ErrCorrupt, e.Seq, db.v.LastSeq)
				}
				db.mem.Add(e)
				db.v.LastSeq = e.Seq
				return nil
			})
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	if err := db.flush(); err != nil {
		return err
	}
	if err := db.removeObsolete(); err != nil {
		return err
	}
	db.emit(Event{Kind: EventRecovery, FromLevel: -1, ToLevel: -1, Duration: time.Since(start),
		WALRecords: report.Records, WALDroppedBytes: report.DroppedBytes})
	return nil
}

// Put sets key to value.
func (db *DB) Put(key, value []byte) error {
	return db.write(keys.KindPut, key, value)
}

// Delete removes key. Deleting a key that is not there is not an error.
func (db *DB) Delete(key []byte) error {
	return db.write(keys.KindDelete, key, nil)
}

func (db *DB) write(kind keys.Kind, key, value []byte) error {
	if len(key) == 0 || len(key) > keys.MaxKeySize {
		return errs.Wrap(Module, ErrInvalidKey, "cannot %s a %d-byte key, nothing changed: %v", kind, len(key), ErrInvalidKey)
	}
	if len(value) > keys.MaxValueSize {
		return errs.Wrap(Module, ErrValueTooBig, "cannot put a %d-byte value, nothing changed: %v", len(value), ErrValueTooBig)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.writable(); err != nil {
		return err
	}

	e := keys.Entry{Key: key, Seq: db.v.LastSeq + 1, Kind: kind, Value: value}
	if err := db.log.Append(e); err != nil {
		return db.fail(err, "cannot %s %q: the WAL write failed, so the write is lost", kind, key)
	}
	if db.opts.SyncWrites {
		if err := db.log.Sync(); err != nil {
			return db.fail(err, "cannot %s %q: the WAL sync failed, so the write may be lost", kind, key)
		}
	}
	db.mem.Add(e)
	db.v.LastSeq = e.Seq
	db.stats.UserBytesWritten += int64(len(key) + len(value))
	db.stats.Writes++

	if db.mem.Size() < db.opts.MemtableSize {
		return nil
	}
	if err := db.flush(); err != nil {
		return db.fail(err, "wrote %q, but flushing the memtable failed", key)
	}
	if err := db.compact(); err != nil {
		return db.fail(err, "wrote %q, but compaction failed", key)
	}
	return nil
}

// Get returns the value of key. ok is false if the key is absent or
// deleted; that is not an error. The value is a copy the caller owns.
func (db *DB) Get(key []byte) (value []byte, ok bool, err error) {
	if len(key) == 0 || len(key) > keys.MaxKeySize {
		return nil, false, errs.Wrap(Module, ErrInvalidKey, "cannot get a %d-byte key: %v", len(key), ErrInvalidKey)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil, false, errs.Wrap(Module, ErrClosed, "cannot get %q: %v", key, ErrClosed)
	}
	db.stats.Gets++

	e, found := db.mem.Get(key)
	if !found {
		e, found, err = db.getFromTables(key)
		if err != nil {
			return nil, false, errs.Wrap(Module, tableErr(err), "cannot get %q: %v", key, err)
		}
	}
	if !found || e.Kind == keys.KindDelete {
		return nil, false, nil
	}
	return bytes.Clone(e.Value), true, nil
}

// getFromTables searches L0 newest first, then one file per deeper level.
// The first version found is the newest, because every level holds only
// data older than the level above it.
func (db *DB) getFromTables(key []byte) (keys.Entry, bool, error) {
	probe := func(f meta.FileMeta) (keys.Entry, bool, error) {
		r, err := db.reader(f.Num)
		if err != nil {
			return keys.Entry{}, false, err
		}
		db.stats.TablesProbed++
		return r.Get(key)
	}
	l0 := db.v.Levels[0]
	for i := len(l0) - 1; i >= 0; i-- {
		if !l0[i].Overlaps(key, key) {
			continue
		}
		if e, found, err := probe(l0[i]); found || err != nil {
			return e, found, err
		}
	}
	for _, files := range db.v.Levels[1:] {
		// Files in L1+ are sorted and disjoint: binary search for the one
		// file whose range could hold key.
		i, _ := slices.BinarySearchFunc(files, key, func(f meta.FileMeta, k []byte) int {
			return bytes.Compare(f.Largest, k)
		})
		if i == len(files) || !files[i].Overlaps(key, key) {
			continue
		}
		if e, found, err := probe(files[i]); found || err != nil {
			return e, found, err
		}
	}
	return keys.Entry{}, false, nil
}

// Scan calls fn for each live key in [start, end), in increasing order,
// with its newest value. A nil start or end means unbounded. Scan stops
// early if fn returns false.
//
// fn's slices are valid only during the call; copy them to keep them. fn
// must not call the DB, which is locked for the whole scan.
func (db *DB) Scan(start, end []byte, fn func(key, value []byte) bool) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return errs.Wrap(Module, ErrClosed, "cannot scan: %v", ErrClosed)
	}
	// Only tables whose key range meets [start, end) take part.
	var readers []*sstable.Reader
	for _, level := range db.v.Levels {
		for _, f := range level {
			if (end != nil && bytes.Compare(f.Smallest, end) >= 0) || (start != nil && bytes.Compare(f.Largest, start) < 0) {
				continue
			}
			r, err := db.reader(f.Num)
			if err != nil {
				return errs.Wrap(Module, err, "cannot scan: %v", err)
			}
			readers = append(readers, r)
		}
	}
	its := []iterator.Iterator{db.mem.NewIterator()}
	for _, r := range readers {
		its = append(its, r.NewIterator())
	}
	it := iterator.NewDedup(iterator.NewMerging(its...), true)
	if start == nil {
		it.First()
	} else {
		it.SeekGE(start)
	}
	for ; it.Valid(); it.Next() {
		e := it.Entry()
		if end != nil && bytes.Compare(e.Key, end) >= 0 {
			break
		}
		if !fn(e.Key, e.Value) {
			break
		}
	}
	if err := it.Close(); err != nil {
		return errs.Wrap(Module, tableErr(err), "scan stopped early: %v", err)
	}
	return nil
}

// Flush writes the memtable to a new L0 table now, then runs any
// compaction that makes necessary.
func (db *DB) Flush() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.writable(); err != nil {
		return err
	}
	// Skip an empty flush (it would only rotate the WAL), but always
	// compact: the flush at Open never compacts, so L0 can be over its
	// trigger even when the memtable is empty.
	if db.mem.Len() > 0 {
		if err := db.flush(); err != nil {
			return db.fail(err, "cannot flush")
		}
	}
	if err := db.compact(); err != nil {
		return db.fail(err, "flushed, but compaction failed")
	}
	return nil
}

// Compact flushes the memtable, then compacts until every level is
// within its target. Every flush already compacts that far, so Compact
// is Flush under a name that says what a caller wants. It does not merge
// everything into the last level.
func (db *DB) Compact() error {
	return db.Flush()
}

// Verify reads every live table in full, checking every block checksum
// and that each table's keys are in order and match the manifest's
// record of its range. It returns the first problem found, wrapping
// ErrCorrupt for damaged data.
func (db *DB) Verify() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return errs.Wrap(Module, ErrClosed, "cannot verify: %v", ErrClosed)
	}
	for level, files := range db.v.Levels {
		for _, f := range files {
			if err := db.verifyTable(f); err != nil {
				return errs.Wrap(Module, tableErr(err), "L%d table %d failed verification: %v", level, f.Num, err)
			}
		}
	}
	return nil
}

func (db *DB) verifyTable(f meta.FileMeta) error {
	r, err := db.reader(f.Num)
	if err != nil {
		return err
	}
	it := r.NewIterator()
	var n int64
	var first, last []byte
	for it.First(); it.Valid(); it.Next() {
		if n == 0 {
			first = bytes.Clone(it.Entry().Key)
		}
		last = append(last[:0], it.Entry().Key...)
		n++
	}
	if err := it.Close(); err != nil {
		return err
	}
	if n != f.Entries || !bytes.Equal(first, f.Smallest) || !bytes.Equal(last, f.Largest) {
		return fmt.Errorf("%w: holds %d entries in [%q, %q], manifest says %d in [%q, %q]",
			ErrCorrupt, n, first, last, f.Entries, f.Smallest, f.Largest)
	}
	return nil
}

// Close syncs the WAL, closes every file, and releases the lock. A clean
// Close loses no writes, even without SyncWrites.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return errs.Wrap(Module, ErrClosed, "cannot close: %v", ErrClosed)
	}
	db.closed = true
	var err error
	if db.failed == nil && db.log != nil {
		err = db.log.Sync()
	}
	if cerr := db.closeFiles(); err == nil {
		err = cerr
	}
	if err != nil {
		return errs.Wrap(Module, err, "closed, but not cleanly; reopen to recover: %v", err)
	}
	return nil
}

// closeFiles closes the WAL, every table, and the lock, returning the
// first error.
func (db *DB) closeFiles() error {
	var first error
	keep := func(err error) {
		// A WAL that already failed reports ErrFailed on Close; the
		// original failure was returned when it happened.
		if first == nil && err != nil && !errors.Is(err, wal.ErrFailed) {
			first = err
		}
	}
	if db.log != nil {
		keep(db.log.Close())
		db.log = nil
	}
	for _, r := range db.tables {
		keep(r.Close())
	}
	clear(db.tables)
	if db.lock != nil {
		keep(db.lock.Close())
		db.lock = nil
	}
	return first
}

// writable reports why the DB cannot take writes, if it cannot.
func (db *DB) writable() error {
	if db.closed {
		return errs.Wrap(Module, ErrClosed, "cannot write: %v", ErrClosed)
	}
	if db.failed != nil {
		return errs.Wrap(Module, fmt.Errorf("%w: %w", ErrWritesFailed, db.failed),
			"cannot write: %v (first failure: %v)", ErrWritesFailed, db.failed)
	}
	return nil
}

// fail records err as the reason the DB takes no more writes, and wraps
// it for the caller.
func (db *DB) fail(err error, format string, args ...any) error {
	db.failed = err
	return errs.Wrap(Module, tableErr(err), format+": %v; the database takes no more writes until it is reopened",
		append(args, err)...)
}

// tableErr marks corruption from a table so errors.Is(err, ErrCorrupt)
// works for callers.
func tableErr(err error) error {
	if errors.Is(err, sstable.ErrCorrupt) && !errors.Is(err, ErrCorrupt) {
		return fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return err
}

func (db *DB) emit(e Event) {
	if db.opts.Events != nil {
		db.opts.Events(e)
	}
}

// compactionOptions converts Options for the planner.
func (db *DB) compactionOptions() compaction.Options {
	return compaction.Options{L0Trigger: db.opts.L0CompactionTrigger, LevelTargets: db.opts.LevelTargets}
}

func (db *DB) path(name string) string { return filepath.Join(db.dir, name) }

func (db *DB) tablePath(num uint64) string { return db.path(meta.TableName(num)) }

func (db *DB) logPath(num uint64) string { return db.path(meta.LogName(num)) }

// openTable opens the reader for table num and remembers it.
func (db *DB) openTable(num uint64) error {
	f, err := db.fs.Open(db.tablePath(num))
	if err != nil {
		return fmt.Errorf("cannot open table %d listed in the manifest: %w", num, err)
	}
	r, err := sstable.Open(f)
	if err != nil {
		return fmt.Errorf("table %d: %w", num, tableErr(err))
	}
	db.tables[num] = r
	return nil
}

// reader returns the open reader for a live table. A reader is missing
// only if opening it failed right after the table was committed (the DB
// has stopped taking writes then); reader tries again, so reads recover
// as soon as the filesystem does.
func (db *DB) reader(num uint64) (*sstable.Reader, error) {
	if r := db.tables[num]; r != nil {
		return r, nil
	}
	if err := db.openTable(num); err != nil {
		return nil, err
	}
	return db.tables[num], nil
}

// checkInvariant asserts that the open readers match the live files.
func (db *DB) checkInvariant() {
	if !assert.Enabled {
		return
	}
	// db.v's own invariant is asserted by AddL0 and compaction.Apply.
	n := 0
	for _, level := range db.v.Levels {
		for _, f := range level {
			assert.That(db.tables[f.Num] != nil, "db: no open reader for live table %d", f.Num)
			n++
		}
	}
	assert.That(n == len(db.tables), "db: %d open readers for %d live tables", len(db.tables), n)
}
