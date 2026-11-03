# lsmtree: a small log-structured merge-tree store

A small key-value store built to explore O'Neil, Cheng, Gawlick & O'Neil,
*The Log-Structured Merge-Tree* (1996),
[`docs/papers/03-oneil-lsm-tree-1996.pdf`](../docs/papers/03-oneil-lsm-tree-1996.pdf).
The paper's idea: don't update an index in place. Buffer writes in
memory (the C0 tree), then merge them into larger sorted runs on disk
(C1 … CK) in big sequential batches, with a rolling merge that sweeps
each component's key range. This project builds that: a skip-list
memtable, a write-ahead log, SSTables with Bloom filters, and leveled
compaction with a per-level cursor that is the paper's rolling merge.
It then measures the paper's claims in its own units, pages and bytes,
rather than wall-clock time.

A learning project, not production code. It follows the repo-wide
[coding](../docs/coding.md) and [testing](../docs/testing.md) approach.
[`DESIGN.md`](DESIGN.md) is the spec: API, options, file formats,
invariants, test plan, and experiments. [`TESTS.md`](TESTS.md) lists
every test file by category. For a guided tour, read the
[code walkthrough](../docs/walkthroughs/lsmtree/code.md) and the
[test walkthrough](../docs/walkthroughs/lsmtree/tests.md). The root
[README](../README.md#paper-3-log-structured-merge-trees-lsmtree) has
the short version and the
[run instructions](../README.md#the-log-structured-merge-tree-lsmtree).

## Status

Runnable. Milestones 0–6 of [`DESIGN.md` §9](DESIGN.md#9-milestones) are
done: the store, crash recovery, compaction, `lsmctl`, the e2e suite,
the demo, and experiments E1–E4.

Still to do:
- **E5:** compare against a B+tree (`go.etcd.io/bbolt`), the paper's
  headline comparison
- optional: background flush and compaction (milestone 7)
- the blog post

## Layout: domain vs integration

Domain packages hold the logic. The pure ones do no I/O at all. The
storage ones (WAL, SSTable, manifest, and the `DB` itself) reach the disk
only through the `vfs.FS` interface, so tests run them on an in-memory
filesystem that injects faults and simulates crashes. Integration code
connects them to the real filesystem and the command line. The library
is the root package `lsmtree`; everything else is under `internal/`. The
error, assertion, and logging helpers, and lsmctl's top level
(`shared/cli`: log file, error reporting, exit codes), come from the
repo's [`shared`](../shared/) module.

```
lsmtree/
├── db.go, flush.go, compact.go   public library: Open, Put/Get/Delete/Scan, flush, compaction, recovery
├── options.go, stats.go, event.go  Options, Stats, and the Events hook
├── internal/
│   ├── keys/          domain: entries and their order
│   ├── codec/         domain: the one entry encoding, plus CRC32-C
│   ├── memtable/      domain: skip list (the paper's C0)
│   ├── bloom/         domain: Bloom filter
│   ├── iterator/      domain: merging and newest-wins iterators
│   │   └── itertest/  test helper: the iterator contract suite
│   ├── meta/          domain: Version (live tables per level, cursors), file names
│   ├── compaction/    domain: the pure planner (the rolling merge)
│   ├── wal/           storage: write-ahead log, replay
│   ├── sstable/       storage: table writer and reader
│   ├── manifest/      storage: the checkpoint (one JSON line + CRC)
│   ├── vfs/           the FS interface and its durability model
│   │   ├── vfstest/   test helper: MemFS (faults, crashes) and the FS contract suite
│   │   └── countfs/   FS wrapper that counts I/O by file kind (experiments)
│   └── osfs/          integration: the real filesystem
├── cmd/lsmctl/        binary: the CLI
├── e2e/               end-to-end tests (build tag: e2e)
├── experiments/       measurements of the paper's claims (build tag: experiments)
└── scripts/           demo.sh, experiments.sql
```

| Package | Kind | What it does |
|---------|------|--------------|
| `internal/keys` | domain | `Entry` (user key, sequence number, put or delete, value) and the one sort order: key ascending, newest first |
| `internal/codec` | domain | The entry encoding shared by WAL records and SSTable blocks; every valid entry has exactly one encoding |
| `internal/memtable` | domain | Skip list with p = 1/4 and a seeded RNG, so the same writes build the same list |
| `internal/bloom` | domain | Filter with double hashing and a fixed hash, so files read the same in every process |
| `internal/iterator` | domain | `Iterator`, a k-way merging iterator, and a newest-wins iterator that can drop tombstones |
| `internal/meta` | domain | `Version`, the live tables per level and each level's cursor, with its invariant; file naming |
| `internal/compaction` | domain | `Pick` and `Apply`: given a Version and options, which files merge next. Pure, like jumphash's `PlanMoves` |
| `internal/wal` | storage (via `vfs.FS`) | Log writer, and a replay that stops at the first bad record |
| `internal/sstable` | storage (via `vfs.FS`) | Data blocks, Bloom block, index, footer; lazy block reads with read counters |
| `internal/manifest` | storage (via `vfs.FS`) | Encodes a Version and installs it atomically |
| `lsmtree` (root) | domain orchestration | `DB`: writes, reads, flush, compaction, recovery, `Stats`, `Events` |
| `internal/vfs` | interface | `FS`, `WritableFile`, `ReadableFile` |
| `internal/vfs/countfs` | adapter | Counts bytes and calls per file kind (WAL, table, manifest) |
| `internal/osfs` | integration | `os` calls, directory fsync, and `flock` for the lock file |
| `cmd/lsmctl` | integration | Flags, output, and wiring; one store open per process |

```
 lsmctl ── lsmtree.Open(dir, Options{FS: osfs})
              │
   Put ─→ WAL ─→ memtable (C0) ──flush──→ L0 tables (may overlap)
                                              │ compaction (cursor per level)
   Get ─→ memtable → L0 newest first → L1 → L2 … (Bloom filter skips tables)
```

## Durability and write ordering

The full rules are in [`DESIGN.md` §5](DESIGN.md#5-on-disk-formats). In
brief:

- **What is durable.** As on a POSIX filesystem, a file's bytes survive
  a crash only after `Sync`, and a create, rename, or remove only after
  `SyncDir` on its directory (`internal/vfs`). `MemFS` simulates exactly
  this: `Crash` keeps only what was made durable, and `CrashTorn` also
  keeps a random prefix of the rest.
- **Copy, commit, clean up.** A flush writes and syncs the table, then
  commits a new manifest that lists it and names the new WAL, then
  deletes the old WAL. A compaction writes and syncs its outputs, then
  commits a manifest with inputs out, outputs in, and the level's new
  cursor, then deletes the inputs. `Open` reads the manifest, replays
  the WAL, and deletes any table or WAL the manifest doesn't name. A
  crash between any two steps leaves the old state or the new one.
- **The manifest is the checkpoint** (§4.2 of the paper). It's written
  to `MANIFEST.tmp`, synced, renamed, and the directory synced. Because
  it holds the cursors, a restart resumes the rolling merge where it
  stopped.
- **Guarantees.** With `SyncWrites` (`lsmctl -sync`), a write that
  returned survives a crash. Without it, a crash may lose a suffix of
  recent writes, never an older write while keeping a newer one: the
  recovered state is the state after some prefix of the writes.
- **After a failure,** if a WAL write, flush, or compaction fails, the
  DB returns `ErrWritesFailed` for every later write. Reads keep
  working. Reopening recovers every acknowledged write.

## Contracts and assertions

Design by contract, following *Effective Software Testing* chapter 4:

- **Input validation returns errors,** at the public API only. A key
  must be 1 B–4 KiB (`ErrInvalidKey`), a value at most 1 MiB
  (`ErrValueTooBig`). Bad options give `ErrBadOptions`. Corrupt files
  give `ErrCorrupt`: every block, record, footer, and the manifest carry
  a CRC32-C, and the decoders reject anything malformed behind it.
- **Contracts are asserted** with `shared/assert` and panic when they
  fail. Everything inside the API trusts the validated limits.
- **Assertions are on by default.** Build with `-tags noassert` to
  compile them out. `make check` tests both builds.

The main invariants ([`DESIGN.md` §6](DESIGN.md#6-invariants-and-contracts-asserted--tags-noassert-removes-them)):

| Invariant | Where | Checked |
|-----------|-------|---------|
| Entries strictly ordered (key asc, seq desc); each skip-list level is a subsequence of the one below; sequence numbers increase | `internal/memtable` | neighbours on every Add; a full walk at powers of two and every 1024 adds |
| Keys added in strictly increasing order; blocks cover the bytes written | `internal/sstable` (writer) | on every Add and after each block |
| L1+ files sorted and non-overlapping; L0 ordered by file number; file numbers and seqs within the counters | `internal/meta` (`Version`) | after every flush and compaction, and before the manifest is written |
| Merged output strictly increasing, each key at its newest version | `internal/iterator` | as entries are emitted |
| A plan's lower inputs are exactly the files overlapping the upper input; tombstones dropped only when no deeper level overlaps | `internal/compaction` | on every Pick; Apply checks the resulting Version |
| Every live table has an open reader, and no other table does | `db.go` | after every flush and compaction |

`vfstest.FSContract` holds every `vfs.FS` (`MemFS`, `osfs`, `countfs`) to
the same behaviour, and `itertest.Contract` does the same for every
iterator (memtable, SSTable, slice, merging, newest-wins).

## Errors and logging

Follows the error-propagation model in Cox-Buday's *Concurrency in Go*.

- **Wrap at the module boundary.** Every error from the `lsmtree` API is
  an `*errs.Error` (`shared/errs`) from module `lsmtree`, with a
  friendly message that says whether anything changed, the cause, a UTC
  timestamp, and a stack. `errors.Is` still finds the sentinels
  (`ErrClosed`, `ErrInvalidKey`, `ErrValueTooBig`, `ErrCorrupt`,
  `ErrBadOptions`, `ErrWritesFailed`, and `vfs.ErrLocked` for a store
  another process holds).
- **A missing key is not an error:** `Get` returns `ok == false`, so a
  miss doesn't pay for a stack trace.
- **The top level (`lsmctl`) decides between edge case and bug**
  (`shared/cli`):
  - outermost error from `lsmtree` or `lsmctl`: a known edge case. Shows
    the message and exits 1
  - no command or a bad flag before it: usage, exit 2
  - anything else, including a panic or a failed assertion: a bug.
    Shows "this is a bug" and exits 70
- **Log IDs.** Every error prints `[log ID 3f9a1c0b]`. The matching JSON
  line in `lsmctl.log` (next to the store directory, or set with `-log`)
  holds the full chain and the stack.
- **The domain doesn't log.** It reports each flush, compaction, and WAL
  recovery through `Options.Events`. `lsmctl -v` prints them; the
  experiments write them to `events.jsonl`.

```
$ lsmctl get user:42
error: key "user:42" not found [log ID 967cd842]
```

## Run it

Requires Go 1.26.4 or later and `make`; no ports or services. The
experiments' summary tables need [DuckDB](https://duckdb.org/docs/installation/)
(optional). From `lsmtree/`:

```sh
make check        # vet, gofmt, tests with -race (and -short), then the full tests with -tags noassert
make e2e          # end-to-end: the real lsmctl binary, one process per command, with a SIGKILL crash test
make demo         # scripted walkthrough in a temp store, including a kill -9; cleans up after itself
make experiments  # measure the paper's claims (about 5 seconds); see below
make build        # bin/lsmctl
make clean        # remove bin/, coverage.out, and *.log
```

`make check` runs the race pass with `-short`. That shrinks the two
long root tests, `TestEveryFaultLeavesARecoverableStore` and
`TestModelLongRun`, which are about 10× slower under the race detector.
The `noassert` pass runs their full versions, so `check` still covers
every fault and step. From the repository root, `make test-lsmtree`
runs `check` and `e2e`, and `make demo-lsmtree` runs the demo. `make
test`, `make bench`, `make cover`, and `make fmt` are also available.

### Using lsmctl

Each command is its own process: it opens the store (in `data/` by
default), runs, and closes it. Small sizes make flushes and compactions
visible after a few thousand keys. Tuning flags are not saved, so pass
the same ones to every command:

```sh
alias lsm='bin/lsmctl -memtable 8k -table 16k -base 32k -ratio 4'
lsm put user:42 hello
lsm get user:42            # hello
lsm -v load 2000           # prints each flush and compaction
lsm dump                   # tables per level, key ranges, merge cursors
lsm load 2000 -seed 2      # overwrites every key; prints write amplification
lsm dump                   # the cursors have moved on
lsm stats                  # sizes vs targets, space and read amplification
lsm scan key-00000010 key-00000013
lsm del user:42
lsm get user:42            # error: key "user:42" not found [log ID …]
lsm verify                 # checksums, key order, key ranges
```

`dump` draws the level tree. Each level shows its tables and key
ranges, its fill against the target, and its rolling-merge cursor; `->`
marks the table the level's next compaction takes:

```
L1  2 tables, 25.2 KiB of 32.0 KiB  [#######---] 78%
    cursor key-00001668; next merge into L2 takes #187
     #185   key-00000014 .. key-00000900     131 keys   16.0 KiB
  -> #187   key-00001669 .. key-00001996      75 keys    9.2 KiB
```

To see crash safety, run `lsm -sync load 100000 > acks.txt`. It fsyncs
every write and prints each key once it is durable. Stop it with
`kill -9` from another terminal, then run `lsm verify` and `lsm get` on
the last key in `acks.txt`.

Errors print a log ID; the details are in `lsmctl.log` next to the store
directory. `lsmctl -h` lists all commands and flags; global flags go
before the command. Ctrl-C stops a `load` cleanly. To start over, run
`rm -rf data lsmctl.log` (`make clean` removes `bin/` and logs).

## The experiments

`make experiments` (or `make experiments-lsmtree` from the repository
root) runs the shared runner
[`../shared/scripts/experiments.sh`](../shared/scripts/experiments.sh),
which runs the tests in `experiments/` (build tag `experiments`). Their
tables, run directory, and event log use the shared
[`experiment`](../shared/experiment/) package. To run some experiments
only, pass a `go test -run` pattern from the root:
`make experiments-lsmtree RUN=E3`. They count bytes and blocks
through `countfs` on an in-memory filesystem instead of timing anything,
so every run gives the same numbers on any machine. Each experiment
logs its table and checks a loose expected shape, so a change that
breaks a result fails the run. Details and the expected shapes are in
[`DESIGN.md` §8](DESIGN.md#8-experiments-measuring-the-papers-claims).

Each run writes `results/<UTC time>-<commit>/` (ignored by git):

| File | One row per |
|------|-------------|
| `runs.csv` | the run: run ID, UTC time, git commit, Go version |
| `e1_batch_merge.csv` | C0/C1 size ratio: measured and predicted M, block I/O per insert |
| `e2_write_amp.csv` | key pattern, size ratio r, and level: bytes written per user byte |
| `e2_totals.csv` | key pattern and r: table, WAL, and manifest write amplification, levels used |
| `e3_theorem31.csv` | S1 target: measured and predicted merge I/O per byte |
| `e4_read_amp.csv` | Bloom setting and lookup kind: tables probed and blocks read per Get, false-positive rate |
| `events.jsonl` | flush or compaction (the `Events` hook): levels, files, bytes, entries, tombstones dropped |
| `test.log` | the `go test -v` output, with each experiment's table |

If `duckdb` is installed, the script then runs
[`scripts/experiments.sql`](scripts/experiments.sql) (on top of the shared
[`experiments_prelude.sql`](../shared/scripts/experiments_prelude.sql)), which globs every
run under `results/` and prints each experiment's tables and event-log
summaries from the latest run that measured it, and one row of headline
numbers per run to spot regressions. After `RUN=E3` you see the new E3
beside the other experiments' last results (a table at the top names
the run each comes from). The queries need every experiment to have run
once: in an empty `results/`, a `RUN=` run stops the summary with a
message saying to run `make experiments` first. Without DuckDB it stops after the Go tests and says so. To
rerun the queries by hand:

```sh
duckdb -c '.read scripts/experiments.sql'
```

Remove `results/` to reset.

### What the runs show

| # | Claim (paper) | Result |
|---|---------------|--------|
| E1 | Batch-merge parameter M, formula 3.2 | Measured M is 0.97× the formula at every C0/C1 ratio (blocks hold whole entries, and each table's last block is partly empty) |
| E2 | Write amplification grows with the size ratio r (§3.3) | Per-level cost grows with r, but fewer levels are needed; the total is lowest near r = 4 (formula 3.6). Sequential keys cost about one copy per level. Rewriting the manifest costs about 3–6× the user bytes, more than the WAL's 1.4× |
| E3 | Theorem 3.1: geometric sizes minimise merge I/O | A U-shaped curve with its minimum next to √(S0·S2), within 11% of formula 3.7 |
| E4 | Finds cost more; Bloom filters cut absent-key reads | Absent-key blocks per Get fall from 5.6 with no filter to 0.046 at 10 bits per key, as predicted. At 20 bits the measured false-positive rate is about 3.5× the formula, a known weakness of plain double hashing at large k |

The paper reasons about 1995 disks, where a random page cost about 10×
a sequential one. Counting I/O in the paper's own units tests the
argument itself, independent of today's SSDs.

## What the tests prove

[`TESTS.md`](TESTS.md) lists every file. The tests that carry the most
weight:

- **Model-based fuzzing** (`FuzzDBModel`, `db_model_fuzz_test.go`).
  Fuzz bytes become a script of puts, deletes, gets, scans, flushes,
  compactions, reopens, and crashes, run against the real `DB` on
  `MemFS` and a plain map. Tiny sizes push data through every level in
  a few dozen steps. Any disagreement is a lost write, a stale read, or
  a resurrected delete. `TestModelLongRun` is its fixed-seed version,
  long enough to fill four levels, and `e2e/model_test.go` runs a
  fixed-seed workload through the real binary.
- **The crash prefix property** (`FuzzCrashRecovery`). Without
  `SyncWrites`, after a clean or torn crash at a fuzzer-chosen step, the
  store must hold exactly the state after some prefix of the writes,
  including every write before the last explicit Flush or Close. The WAL
  has the same test on its own (`internal/wal/crash_test.go`), and the
  e2e suite kills a synced `lsmctl load` with SIGKILL and checks every
  acknowledged key.
- **The fault sweep** (`TestEveryFaultLeavesARecoverableStore`). It
  fails the nth call of each filesystem operation (create, write, sync,
  rename, remove, directory sync, open, read, stat), for every n in the
  first 100 and a sample after. The store must refuse further writes
  with a well-formed error, keep answering reads, and after a reopen
  hold every acknowledged write. It found a real bug, the **ambiguous
  commit**: when the directory sync after the manifest rename failed,
  compaction deleted its new tables, but the new manifest could still
  have landed, leaving a store that pointed at deleted tables. Now a
  manifest error leaves every file in place, and `Open` removes the ones
  the surviving manifest doesn't name.
- **Every byte checked.** Flipping each byte of an SSTable in turn
  always surfaces as `ErrCorrupt`, never a panic or a wrong answer; a
  WAL cut at every offset recovers exactly the records before the cut;
  a manifest cut at every length is rejected.

## Deliberately missing

From [`DESIGN.md` §2](DESIGN.md#2-scope). Each is a "what would come
next" paragraph for the blog:

- **No transactions, snapshots, or MVCC reads.** A `Scan` holds the DB
  lock for its whole run.
- **No compression, block cache, range deletes, or prefix
  compression.** Every read that needs a block reads it from the file.
- **No server, and one process per store.** An OS file lock (`LOCK`)
  stops a second opener; there is no network API.
- **Synchronous compaction.** One mutex guards the DB, and flushes and
  compactions run inside the write that triggers them. That keeps every
  run deterministic for the model test, but writes stall during a merge.
  The paper's §4.1 point that the index stays readable during merges is
  milestone 7.
- **Tuning options are not persisted.** The manifest records the files,
  not the options, so every open must pass the same sizes (lsmctl's
  flags are per run). Opening with fewer levels than the store has is
  rejected with `ErrBadOptions`; other changes just change the shape
  the store compacts toward.
- **The manifest is rewritten whole** on every flush and compaction,
  O(files) each time; E2 shows it writing more bytes than the WAL.
  LevelDB appends version edits instead.
- **No trivial move.** Compaction always rewrites a file, even when it
  overlaps nothing below, so sequential loads still pay one copy per
  level.
