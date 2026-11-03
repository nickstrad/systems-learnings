# lsmtree: design

Paper 3: O'Neil, Cheng, Gawlick & O'Neil, *The Log-Structured Merge-Tree*
(1996), [`docs/papers/03-oneil-lsm-tree-1996.pdf`](../docs/papers/03-oneil-lsm-tree-1996.pdf).

This is the design spec for the project. It is written before the code
and is meant to be argued with. The goal is a small key-value store that
I can read in an afternoon, test hard, measure, and blog about. It is not
a production database.

## 1. What the paper says, and what we will build

The paper's idea in one line: **don't update the index in place; buffer
changes in memory, then merge them into larger sorted runs on disk in big
sequential batches.**

| Paper term | This project | Notes |
|------------|--------------|-------|
| C0 tree (memory component) | `memtable`: a skip list | Pugh's skip list (paper 16 in the candidates list) |
| Recovery log + checkpoint (§4.2) | `wal` + `MANIFEST` | The manifest is our checkpoint: it says which files are live |
| C1 … CK (disk components) | Levels L1 … Ln of sorted, immutable files (SSTables) | Modern LSMs split each component into many files |
| Multi-page block | SSTable data block (~4 KiB) | Our "page" for counting I/O |
| Rolling merge (§2) with a cursor | Leveled compaction with a **round-robin compaction cursor per level** | One file from Li merged into the overlapping files of Li+1. The cursor walks through the key space and wraps, as in the paper's Figure 2.2 |
| Delete node entry (§2.3) | Tombstone | Removes the older value when compaction merges past it. Dropped at the bottom level |
| Component size ratio r (§3.3, Thm 3.1) | `LevelSizeRatio`, or explicit `LevelTargets` | Explicit targets let us test Theorem 3.1 |
| Batch-merge parameter M (Def. 3.2.1) | Measured: new entries merged per output block | Compared with formula 3.2 |

**One deliberate difference from the paper:** we add an L0 level whose
files may overlap. A memtable flush writes one L0 file sequentially and
never reads disk. L0 is then merged into L1. The paper merges C0 straight
into C1. L0 is what LevelDB and RocksDB do, and it keeps flush and merge
separate, which makes both easier to test. The blog post should say why
the two designs differ.

## 2. Scope

### In scope (the MVP)

- `Put`, `Get`, `Delete`, and an ordered range `Scan`, with byte-slice keys
  and values.
- Write-ahead log with checksums. Recovery after a crash, including a
  torn last record.
- Memtable flush to L0 SSTables, with data blocks, a sparse index, and a
  Bloom filter per file.
- Leveled compaction with configurable size ratios, a rolling cursor, and
  tombstone garbage collection.
- A manifest written atomically (write temp, fsync, rename, fsync dir).
  Orphan files are cleaned up on open.
- A filesystem interface, so tests can inject faults and simulate crashes
  that lose unsynced data.
- Instrumentation: bytes and blocks read and written, by cause (WAL,
  flush, compaction, get). These give the paper's I/O counts without
  depending on wall-clock time.
- `lsmctl` CLI over a data directory, an e2e suite, a demo script, and an
  experiments suite that prints tables for the blog.

### Out of scope (say so in the README)

Transactions, snapshots/MVCC reads, compression, a block cache, range
deletes, prefix compression, a network server, and multiple processes
sharing one directory (an OS file lock prevents this). Each is a good
"what would come next" paragraph, not MVP work.

### Concurrency stance

**MVP: one `sync.Mutex` guards the DB, and compaction runs synchronously
inside the write that triggers it.** That makes every run deterministic,
which the model-based fuzz test needs. Reads are safe from many
goroutines; they just take the lock.

**Stage 2 (optional, after the MVP is green):** background flush and
compaction goroutines with an immutable memtable. This is the paper's §4.1
claim that "all index values are continuously accessible" during merges.
It follows the `docs/coding.md` concurrency rules: Result channels, no
leaks, and recovered panics. The deterministic model test stays the
oracle, and the background mode adds `-race` stress tests.

## 3. Public API (root package `lsmtree`)

```go
func Open(dir string, opts Options) (*DB, error)

func (db *DB) Put(key, value []byte) error
func (db *DB) Get(key []byte) (value []byte, ok bool, err error) // ok=false if absent or deleted
func (db *DB) Delete(key []byte) error                 // deleting a missing key is not an error
func (db *DB) Scan(start, end []byte, fn func(k, v []byte) bool) error // [start, end); nil = unbounded
func (db *DB) Flush() error                            // memtable → L0 now
func (db *DB) Compact() error                          // same as Flush: every flush compacts to target
func (db *DB) Verify() error                           // read every table, check checksums and ranges
func (db *DB) Stats() Stats                            // counters, levels, tables, rolling-merge cursors
func (db *DB) Close() error
```

Every public error is an `errs.Error` from module `"lsmtree"`. It says
whether anything changed and whether a retry is safe. Sentinels
(`ErrClosed`, `ErrInvalidKey`, `ErrValueTooBig`, `ErrCorrupt`,
`ErrBadOptions`, `ErrWritesFailed`) still work with `errors.Is`.

`Stats` reports each level's tables and rolling-merge cursor.
`LevelStats.Next` is the index in `Tables` of the table the level's next
compaction takes, computed with the planner's own rule
(`compaction.NextInput`: the first table starting after the cursor,
wrapping to the first); it is -1 for L0, an empty level, and the last
level. `Stats.L0Trigger` is the L0 file-count trigger in effect, since L0
has no byte target. `lsmctl dump` marks `Next` with `->`.

A missing key is not an error: `Get` returns `ok == false`. (An early
draft returned `ErrNotFound`, but a well-formed error captures a stack
trace, which would make every miss expensive and distort the read
experiments.)

**After a failure,** if a WAL write, flush, or compaction fails, the DB
stops taking writes and returns `ErrWritesFailed` from then on. Reads
keep working. Reopening recovers every acknowledged write.

**Validation, at the API edge only:** the key must be 1 B–4 KiB. The
value may be empty and is at most 1 MiB. A closed DB returns `ErrClosed`.
Everything inside trusts these limits and asserts them instead.

**Guarantees:**
- Read your writes: a `Get` after a successful `Put` sees the value.
- With `SyncWrites: true`, a write that returned nil survives a crash.
- With `SyncWrites: false`, a crash may lose a suffix of recent writes.
  It never loses an older write while keeping a newer one, and it never
  corrupts the store. **Recovered state = the state after some prefix of
  the operations.** This prefix property is what the crash tests check.
- `Scan` yields keys in strictly increasing order, each at its newest
  value, with deleted keys omitted.

### Options

| Option | Default | Why the default |
|--------|---------|-----------------|
| `MemtableSize` | 64 KiB | Small, so flushes and compactions happen in demos and tests |
| `BlockSize` | 4 KiB | The paper's page size |
| `BloomBitsPerKey`, `NoBloom` | 10, false | About a 1% false-positive rate. 0 means the default; set `NoBloom` to turn filters off and measure the difference |
| `L0CompactionTrigger` | 4 files | |
| `LevelSizeRatio` | 10 | The common modern choice. The paper's examples use about 25–40 |
| `BaseLevelSize` | 256 KiB | Target size of L1 |
| `MaxLevels` | 7 | Number of levels, L0 included. Reopening a store needs at least as many as it has |
| `LevelTargets` | nil | Explicit per-level sizes, overriding the ratio (for Theorem 3.1) |
| `TableSize` | 64 KiB | Compaction starts a new output table at this size |
| `SyncWrites` | false | |
| `FS` | the OS filesystem | Tests pass `memfs` |
| `Events` | nil | `func(Event)` called after each flush, compaction, and WAL recovery. The domain reports what happened; the caller decides whether to log it. The experiments write these events as JSON lines for DuckDB |

## 4. Layout

Follows `docs/coding.md`: domain packages do no I/O except through the
`vfs.FS` interface they depend on. Wiring lives in `cmd/`.

```
lsmtree/
├── DESIGN.md  README.md  TESTS.md  Makefile  go.mod
├── db.go  options.go  stats.go     public API: Open/recover, Put/Get/Scan, failure policy, Stats
├── flush.go  compact.go  event.go  orchestration: flush, compaction (copy, commit, clean up), Events
├── internal/
│   ├── keys/                       entry = user key + seq + kind (put|delete); ordering; Validate
│   ├── codec/                      the one entry encoding (WAL and SSTable), CRC32-C
│   ├── meta/                       FileMeta, Version (levels, cursors) and its invariant; file names
│   ├── memtable/                   skip list, p = 1/4, seeded RNG
│   ├── bloom/                      Bloom filter, double hashing
│   ├── wal/                        record format, writer, reader that stops at a torn tail
│   ├── sstable/                    writer, reader, block format, footer
│   ├── iterator/                   Iterator interface, merging iterator, newest-wins, tombstone filter
│   ├── compaction/                 PURE planner: (version, options) → Plan; Apply; NextInput
│   ├── manifest/                   encode/decode the manifest (one JSON line + CRC line)
│   ├── vfs/                        FS interface; vfstest/ has memfs (crash simulation) and the contract suite;
│   │                               countfs/ wraps any FS and counts I/O by file kind
│   └── osfs/                       real FS adapter: fsync, dir fsync, rename, flock
├── cmd/lsmctl/                     CLI
├── experiments/                    measurement tests (build tag `experiments`); write CSV + JSONL for DuckDB
├── e2e/                            real binary tests (build tag `e2e`)
└── scripts/demo.sh, experiments.sql
```

The error, assertion, logging, and CLI top-level helpers (`errs`,
`assert`, `logx`, `cli`) come from the repo's [`shared`](../shared/)
module.

`compaction.Plan` is this project's version of jumphash's `PlanMoves`.
It decides what to merge with no I/O, so it can be tested with plain
structs.

## 5. On-disk formats

All integers are little-endian. Every record and block carries a CRC32-C.

**One entry encoding** (package `codec`), shared by WAL records and
SSTable data blocks: `keylen uvarint | key | seq u64 | kind u8 | vallen
uvarint | val`. The decoder rejects non-minimal varints, lengths over the
key and value limits, and anything `keys.Entry.Validate` rejects, so
every valid entry has exactly one encoding.

**WAL record:** `crc u32 | len u32 | entry`, with the CRC over `len` and
the entry. The reader stops at the first short, bad-CRC, or malformed
record and reports how many bytes it dropped. A bad record in the middle
and a torn tail look the same, so everything after the first bad record
is dropped; that keeps the recovered writes a prefix of the written
ones. One WAL per memtable: `NNNNNN.log`.

**SSTable (`NNNNNN.sst`):**
```
[data block 0] … [data block n]   entries: keylen uvarint | key | seq u64 | kind u8 | vallen uvarint | val ; crc
[bloom block]                      bits + k ; crc        (empty when filters are off)
[index block]                      per data block: keylen uvarint | last key | offset uvarint | length uvarint ; crc
[footer, fixed 40 B]               index off u64 | index len u32 | bloom off u64 | bloom len u32 | entries u64 | crc u32 | magic "LSM1"
```
The regions cover the file exactly, with no gaps; `Open` checks this,
the footer CRC, and the bloom and index CRCs. Each data block is checked
when it is read.
Entries are sorted by (user key asc, seq desc). A file never holds two
versions of the same key, because compaction keeps only the newest.

**MANIFEST:** one line of compact JSON with next file number, last sequence
number, live WAL number, per level the list of `{num, size, smallest,
largest, minSeq, maxSeq, entries}`, and the rolling-merge cursor of each
level. Keys are hex. A trailing line `crc32c <hex>` protects it;
`head -n 1 MANIFEST | jq .` pretty-prints the JSON. (It was indented at
first, but indenting took about 45% of the CPU in the experiments.) It is
replaced whole and atomically: write `MANIFEST.tmp`, fsync, rename, fsync
the directory. Because a compaction's new files and its cursor are in one
manifest, the rolling merge resumes where it stopped after a restart, as
the paper's checkpoint (§4.2) does. Rewriting the whole file costs
O(files) per flush or compaction; LevelDB appends version edits instead.
That is fine at this scale.

**Write ordering**, following coding.md §6 (copy, commit, clean up):
- **Flush:** write the SST, fsync it, write the new manifest (adds the
  table, points at the new WAL), then delete the old WAL.
- **Compaction:** write the outputs, fsync them, write the new manifest
  (inputs out, outputs in), then delete the inputs.
- **Open:** read the manifest, replay the WAL into a memtable, then
  delete any `.sst` or `.log` file the manifest doesn't name.

A crash between any two steps leaves either the old state or the new
state, never a mix. The crash tests check this at every step.

**The ambiguous commit.** If the final directory fsync after the
manifest rename fails, the new manifest may or may not survive: the
outcome is unknown. The first version of compaction deleted its outputs
on any manifest error, so a store whose new manifest had in fact landed
then pointed at deleted tables. The fault-injection test found this.
Now a manifest error leaves every file in place, and `Open` deletes the
ones the surviving manifest doesn't name.

**A regression the e2e suite caught.** A cleanup made `Flush` return
early on an empty memtable, which also skipped compaction. In-process
tests passed, but across processes the flush at `Open` (which never
compacts) leaves L0 tables behind, and `lsmctl compact` then did
nothing. `TestCompactShrinksL0` in `e2e/` caught it; `Flush` now skips
only the empty flush and always compacts, and `TestCompactAfterReopen`
pins the rule in-process.

## 6. Invariants and contracts (asserted, `-tags noassert` removes them)

- **Memtable:** entries strictly ordered by (key asc, seq desc). Each skip
  list level is a subsequence of the level below it.
- **Memtable:** sequence numbers strictly increase across `Add`s (the DB
  hands out one per write).
- **SSTable writer:** keys are added in strictly increasing order, and
  the bytes written end exactly where the last indexed block ends.
- **Version:** within L1+, files are sorted by smallest key and their
  ranges don't overlap. L0 files are ordered by file number. Every file
  number is below `nextFileNum`, and every file's maxSeq is at most
  `lastSeq`.
- **Merging iterator:** emits every version in strictly increasing
  `keys.Compare` order (two children never hold the same entry).
- **Dedup iterator:** emits strictly increasing user keys, each at its
  newest version.
- **DB:** every live table has an open reader, and nothing else does.
- **Compaction plan:** the inputs from Li+1 are exactly the files that
  overlap the chosen Li input's range. Tombstones are dropped only if no
  deeper level overlaps the key.

Each one gets a test that proves it can trip (structural tests,
`docs/testing.md` §2).

## 7. Test plan (maps to `TESTS.md` categories)

1. **Specification-based unit tests:** keys ordering; the skip list
   (empty, one, many, duplicate key with a higher seq); Bloom filter (no
   false negatives, and the FP rate matches `(1-e^{-kn/m})^k` within
   tolerance); SSTable round trip at the 0-, 1-, and many-block
   boundaries and a key exactly the block size; the compaction planner
   (empty levels, a level exactly at target and one byte over, cursor
   wrap-around, no overlap below, overlap with every file below); DB API
   partitions (key size 0/1/max/max+1, empty value, overwrite, delete a
   missing key, delete then put).
2. **Failure-path tests:** memfs fails each write, fsync, rename, and
   remove in flush, compaction, and manifest install, one at a time. The
   DB stays readable, the error says what state it is in, and a reopen
   sees a consistent state.
3. **Structural tests:** a broken stub for each invariant above shows
   that it trips. Coverage is reviewed for uncovered branches.
4. **Contract tests:** one `FSContract` suite runs against `osfs` (temp
   dir), `memfs`, and `countfs`. One iterator contract
   (`itertest.Contract`: seek, next, order, exhausted) runs against the
   slice, memtable, SSTable, merging, and dedup iterators.
5. **Property/fuzz tests:**
   - `FuzzDBModel`, the main one: bytes become a script of Put, Delete,
     Get, Scan, Flush, Compact, and Reopen, run against the DB and a
     `map`. Tiny memtable and block sizes force many levels.
   - `FuzzCrashRecovery`: the same script, plus a crash at a
     fuzzer-chosen step. memfs drops unsynced data, then the DB reopens.
     The state must equal some prefix of the model, at least as long as
     the last synced write.
   - `FuzzSSTableRoundTrip`, `FuzzWALRoundTrip`: encode, then decode
     back to the same entries.
   - `FuzzSSTableOpen`, `FuzzWALReplay`, `FuzzManifestDecode`: arbitrary
     bytes never panic; they return `ErrCorrupt` or a valid prefix.
     `FuzzDecodeEntry`, `FuzzDecoders`, and `FuzzManifestBody` aim past
     the checksums, which random bytes almost never pass.
   - `FuzzMemtable`, `FuzzBloomNoFalseNegatives`, `FuzzPlan`: the
     memtable against a sorted slice, no false negatives, and every
     compaction plan checked by brute force.
   - `FuzzMergeIterator`: the merged output equals a sort-and-dedupe
     reference.
6. **Integration tests:** `osfs` in a temp dir (fsync, rename,
   directory sync, lock held twice fails); SSTable and WAL files on real
   disk, with a flipped byte detected; `cmd/lsmctl` `realMain` exit codes
   (1 error, 2 usage, 70 bug) and log IDs.
7. **e2e tests (real binary):**
   - put, get, scan, and delete across separate process runs (so the data
     is truly on disk)
   - **SIGKILL crash test:** `lsmctl load -sync` prints each acknowledged
     key. Kill it with `-9` mid-run, reopen, and every acknowledged key
     must be present.
   - compaction through the CLI shrinks L0
   - a corrupted SST gives a friendly error with a log ID, not a panic
   - a fixed-seed model workload

## 8. Experiments: measuring the paper's claims

These live in `experiments/` and run with `make experiments` (the
shared runner `../shared/scripts/experiments.sh`; from the repository
root, `make experiments-lsmtree [RUN=pattern]`). The plumbing (tables
printed and saved as CSV, the run directory, JSON-lines logs, parallel
configuration sweeps) is the shared `experiment` package
([`shared/experiment`](../shared/experiment/)). They count bytes and blocks through an
instrumented `vfs.FS` rather than timing, so results are deterministic
and machine-independent. Each test asserts a loose expected shape and
prints a table with `t.Log`.

**Output for DuckDB.** Each run writes to `results/<run-id>/` (ignored
by git):

| File | Format | One row per |
|------|--------|-------------|
| `runs.csv` | CSV | the run: run ID, UTC time, git commit, Go version |
| `e1_batch_merge.csv`, `e2_write_amp.csv`, … | CSV, one per experiment | parameter setting, with measured and predicted values side by side |
| `events.jsonl` | JSON lines (the `Events` hook) | flush or compaction: experiment, config, level from → to, input/output files, bytes read and written, entries in and out, tombstones dropped |

DuckDB reads these without a schema: `read_csv_auto('results/*/e2_*.csv')`
and `read_json_auto('results/*/events.jsonl')`. Globbing across run
directories compares runs. The runner runs the experiments and finishes
by running `scripts/experiments.sql` with `duckdb`. The queries print
the blog tables, each from the latest run that measured that experiment
(so a `RUN=` run of some experiments shows them beside the others' last
results): measured M vs formula 3.2, write amplification per level and
size ratio, the Theorem 3.1 curve, and bytes per compaction by level
from the event log of those same runs. The shared
`../shared/scripts/experiments_prelude.sql` provides the
`latest('<table>')` macro and stops with a clear message if a table has
no run yet. If `duckdb` isn't
installed, the script says so and stops after the Go tests.

| # | Claim (paper section) | Experiment | Expected shape |
|---|-----------------------|------------|----------------|
| E1 | Batching makes inserts cheap: M = (Sp/Se)·(S0/(S0+S1)) (formula 3.2) | Vary memtable size against a fixed L1. Measure new entries merged per output block | Measured M follows the formula, within a tolerance |
| E2 | Write amplification is the price (§3.3) | Load N random keys at r = 4, 10, 25. Report bytes written to disk ÷ user bytes, per level and total | Grows roughly with r per level times the level count |
| E3 | Geometric sizes minimise I/O (Theorem 3.1) | 3 disk levels, S0 and S2 fixed, vary S1. Total merge I/O is lowest near S1 = √(S0·S2) | A U-shaped curve with its minimum near the geometric mean |
| E4 | Finds cost more (abstract, §3.4) | Blocks read per `Get` for present and absent keys, with Bloom filters on and off | Absent-key reads drop to about 1 block per level with filters, at the predicted FP rate |
| E5 | LSM vs B-tree inserts (formula 3.1 vs 3.3) | Stretch goal: the same random-insert load into `go.etcd.io/bbolt` (a B+tree). Compare bytes written and throughput. Kept in `experiments/` so the library has no dependency | The LSM writes fewer bytes per insert at random keys, but not for sequential keys (the paper's "insert-on-the-right" caveat) |

**What the runs show** (`make experiments`, about 5 seconds; each
experiment's configurations run in parallel, each with its own in-memory
store, and the numbers are counts, so every run gives the same ones):

- **E1.** Measured M is 0.97× formula 3.2 at every C0/C1 ratio (blocks
  hold whole entries, and each table's last block is partly empty).
  Block I/O per insert counts the L1 blocks read plus the blocks written,
  leaving out the merge's re-read of the L0 table, which the paper's
  in-memory C0 never costs. It sits slightly above 2/M when S0 ≪ S1 and
  below it as S0 approaches S1: a merge reads only the old S1 pages but
  writes S0 + S1, while formula 3.3 counts both as M's pages.
- **E2.** Write amplification per level grows with r, but the number of
  levels falls, and the total is lowest near r = 4, as formula 3.6
  predicts. Sequential keys cost about one copy per level (there is no
  trivial move). Rewriting the whole manifest costs about 3–6× the user
  bytes, more than the WAL: the price of a manifest rewritten whole on
  every change (§5).
- **E3.** Merge I/O over S1 is U-shaped, with its minimum between 0.7×
  and 1.4× √(S0·S2), and stays within 11% of formula 3.7. To match the
  theorem's setup, the test preloads S2 + S1, spreads new inserts over
  the whole key range, and subtracts the L0 re-read.
- **E4.** Absent-key blocks per `Get` fall from 5.6 (no filter) to
  p × tables probed, with p = `bloom.FalsePositiveRate(b)`. Measured p
  matches at 5 and 10 bits per key; at 20 bits it is about 3.5× the
  formula (27 false positives against 7.5 expected), the known weakness
  of plain double hashing at large k.

The paper reasons about 1995 disks, where random I/O cost about 10× as
much as sequential. On SSDs that gap is smaller, so wall-clock results
will differ from the paper's numbers. Counting I/O in the paper's own
units (pages, multi-page blocks) is how we test the argument itself.
That difference is a good thread for the blog post.

## 9. Milestones

Each milestone ends green on `make check`, with `TESTS.md` updated.

| # | Milestone | Done when |
|---|-----------|-----------|
| 0 | Scaffold: `go.mod`, Makefile (`check`/`e2e`/`demo`), helpers moved to the `shared` module, `vfs` and memfs with the FS contract. **Done** | `make test-lsmtree` passes. **Adding `go.mod` registers the project in the root Makefile, so check, e2e, and demo must exist from the first commit** |
| 1 | keys, memtable, in-memory DB (Put/Get/Delete/Scan) | `FuzzDBModel` passes without Flush or Reopen ops. **Done** |
| 2 | WAL and recovery | Reopen op added; `FuzzCrashRecovery` passes for WAL-only state. **Done** |
| 3 | Bloom, SSTable, flush to L0, manifest, orphan cleanup | Flush op added; crash points in flush tested. **Done** |
| 4 | Compaction: planner, merging iterator, tombstone GC | Compact op added; every invariant asserted; crash points in compaction tested. **Done** |
| 5 | `lsmctl`, e2e, demo, README sections (root and project) | Root README marks the project runnable. **Done** |
| 6 | Experiments E1–E4 (and E5 if time allows), CSV/JSONL output, DuckDB queries | `make experiments` ends with the DuckDB tables; results in the README; root table updated. **Done** (E1–E4; E5 open) |
| 7 | Optional: background compaction (Stage 2) | `-race` stress tests plus the unchanged model test |
| — | Blog post | Paper marked complete |

## 10. `lsmctl` sketch

```
lsmctl [-dir data] [-v] <command>
  put <key> <value>       get <key>          del <key>
  scan [start [end]]      load <n> [-seed s] [-sync] [-sequential]
  flush                   compact            stats   # per-level sizes, write amp, read amp
  dump                    verify             # dump: level tree with key ranges; verify: checksums + invariants
```

`dump` is the demo's centrepiece and the blog's diagram source. It shows
files per level, their key ranges, and the compaction cursor, so you can
watch a rolling merge happen.

## Open questions

- **Memtable:** a skip list (p = 1/4, as in Pugh). *Decided.*
- **Manifest:** JSON, rewritten whole each time. *Decided;* see §5 for
  the cost.
- **E5 (bbolt as a B-tree baseline):** still open. It would be the only
  experiment that tests the paper's headline claim directly, at the cost
  of a dependency in the experiments package.
