# lsmtree tests

Every test file in this project, grouped by the categories in the repo's
[testing approach](../docs/testing.md), plus a last group for the
experiments. A file that fits more than one category is listed under its
main purpose, with its other roles in parentheses. Files are listed in a
suggested reading order: the domain first, then outward to the real
binary. Paths are relative to this directory. The
[test walkthrough](../docs/walkthroughs/lsmtree/tests.md) explains each
group and gives a one-line command for every file.

Keep this file current: add, move, or remove entries in the same change
that adds, renames, deletes, or repurposes a test file.

## 1. Specification-based unit tests (domain logic, no real I/O)

| File | What it covers |
|------|----------------|
| `internal/keys/keys_test.go` | `Entry.Validate` key and value limits, `Compare` (user key ascending, newest seq first), `Clone` shares no memory |
| `internal/codec/codec_test.go` | The entry encoding byte for byte, round trip, decoded slices alias the input but are capped, malformed input rejected (non-minimal varints, over-limit lengths, bad kinds), limits accepted, CRC32-C |
| `internal/memtable/memtable_test.go` | Skip list: Get across versions and tombstones, iteration yields every version, Add copies its buffers, Len and Size, many random adds, same seed gives the same shape, plus `BenchmarkAdd` and `BenchmarkGet` (also contract: runs the iterator contract; also structural: `TestAddAssertions`, `TestCheckInvariantCatchesCorruption`) |
| `internal/bloom/bloom_test.go` | Probe positions, the false-positive formula, filter size and k, empty and damaged filters, no false negatives, measured FP rate matches the formula, the hash is pinned so old files stay readable (also structural: bits-per-key assertions trip) |
| `internal/vfs/vfstest/memfs_test.go` | `MemFS` crash model: unsynced data and directory entries are lost, renames and removes are undone until `SyncDir`, `SyncDir` is ordered across directories, `CrashTorn` keeps a prefix and renames stay atomic, crashes invalidate handles and locks, `FailNth` fails exactly one call with no effect (also contract: runs the FS contract) |
| `internal/wal/wal_test.go` | Round trip, the record layout from DESIGN §5, a torn tail at every byte offset keeps exactly the whole records before it, a flipped byte drops that record and the rest, invalid records dropped whole, writer size and use after close (also structural: `TestAppendAssertsPreconditions`) |
| `internal/wal/helpers_test.go` | Helper, not a test: builds logs, replays them, checks prefixes, and recomputes the CRC independently |
| `internal/sstable/sstable_test.go` | Round trip on and just past block boundaries, the summary, Add copies its input, size estimate, Get hits and every kind of miss, Bloom negatives read no block, blocks read lazily (also structural: `TestAddOutOfOrderTripsAssertion`) |
| `internal/sstable/helpers_test.go` | Helper, not a test: write, open, and build tables on a `MemFS` |
| `internal/iterator/merging_test.go` | Merging iterator: zero children, versions of one key spread over several sources come out newest first, `SeekGE` repositions exhausted children |
| `internal/iterator/dedup_test.go` | Newest-wins iterator: keeps one version per key, with and without tombstones; `SeekGE` onto a deleted key skips to the next live one |
| `internal/iterator/helpers_test.go` | Helper, not a test: `scriptIter`, a slice iterator that fails on demand (used by the failure tests below) |
| `internal/meta/meta_test.go` | Key-range overlap, `Version.Validate` partitions (L0 may overlap, L1+ may not, counters, cursors), `AddL0`, `Clone` independence, file name parsing (also structural: `TestCheckInvariantTrips`, `TestAddL0Trips`) |
| `internal/compaction/compaction_test.go` | The planner: which level compacts, the cursor picks the first file after it and wraps, the cursor sweeps every file once, overlaps below, L0 plans, the bottom-level flag, `Apply` and its cursor update. `NextInput` agrees with `Pick` |
| `internal/manifest/manifest_test.go` | Encode/decode round trip, the readable format (one JSON line, hex keys, CRC trailer), a manifest without cursors, corrupt inputs, a cut at every length is rejected (also structural: `TestEncodeAssertsInvariant`) |
| `db_test.go` | The `DB` API on `MemFS`: get/put/delete partitions, key and value limits (0, 1, max, max+1), scan bounds, data survives reopen with and without `SyncWrites`, deletes never resurrect, use after close, one opener at a time, bad options, corrupt tables, events and stats, `Stats.Next` is really the next compaction across reopens, compact after reopen |
| `helpers_test.go` | Helper, not a test: `tinyOptions` (flush and compact every few writes), put/get/del wrappers, and `checkModel`, used by the root tests (`assertWellFormed` lives in `db_test.go`) |

The `errs`, `assert`, `cli`, and `logx` helpers are tested in the shared
module; see [`../shared/TESTS.md`](../shared/TESTS.md).

## 2. Failure-path and regression unit tests (MemFS injects faults and crashes)

| File | What it covers |
|------|----------------|
| `internal/wal/failure_test.go` | Replay returns callback, read, and size errors instead of treating them as a torn tail; a failed write or sync makes the writer refuse further work; close errors |
| `internal/wal/crash_test.go` | The prefix property: after a crash that tears the unsynced tail at a random byte, replay returns a prefix of the appended entries at least as long as the synced part (also property-based) |
| `internal/sstable/sstable_failure_test.go` | Each failed write and the sync reach the caller as I/O errors, not corruption, and the file is closed; failed reads are I/O errors from Open, Get, and the iterator |
| `internal/sstable/sstable_corrupt_test.go` | Every byte flip is detected as `ErrCorrupt` (never a panic or a wrong answer), where each region's damage is reported, sticky iterator errors, truncated tables and trailing garbage, malformed blocks and indexes that pass their CRC |
| `internal/iterator/errors_test.go` | A child's error mid-stream or while positioning stops the merge; `Close` returns the first child error; dedup passes errors through |
| `internal/manifest/write_test.go` | `Write`/`Read` on `MemFS`: fail each step of the atomic install, then crash, and recover the old manifest or the new one, never a mix; fail the nth call for every n; torn crashes; read failures |
| `db_failure_test.go` | The fault sweep: `TestEveryFaultLeavesARecoverableStore` fails the nth call of each filesystem op during a workload; the store refuses further writes with a well-formed error, keeps serving reads, and after a reopen holds every acknowledged write. It found the ambiguous-commit bug (a failed directory sync after the manifest rename made compaction delete tables the new manifest still listed). `TestFaultDuringOpen` does the same for recovery. `-short` samples fewer faults |

## 3. Structural tests: proving the assertions can fail

| File | What it covers |
|------|----------------|
| `internal/iterator/assert_test.go` | Merging trips on two children with the same (key, seq) and on out-of-order output; dedup and the slice iterator trip on unsorted input |
| `internal/compaction/assert_test.go` | Each planner contract trips: a plan that misses or adds an overlapping file, a wrong bottom flag, no inputs, an input on the wrong level; an `Apply` that would corrupt the Version; bad options or too many levels passed to `Pick` |
| `internal/compaction/export_test.go` | Helper, not a test: exposes `checkPlan` as `CheckPlan` for the test above |
| `export_test.go` | Helper, not a test: exposes the DB's `checkInvariant` and a way to drop a live table's reader, for `TestDBInvariantTrips` in `db_test.go` |

More assertion-tripping tests live with the code they guard:
`TestAddAssertions` and `TestCheckInvariantCatchesCorruption` in
`internal/memtable/memtable_test.go`, the bits-per-key tests in
`internal/bloom/bloom_test.go`, `TestAppendAssertsPreconditions` in
`internal/wal/wal_test.go`, `TestAddOutOfOrderTripsAssertion` in
`internal/sstable/sstable_test.go` (and `TestWriterInvariantTrips`, the
bytes-written check), `TestDBInvariantTrips` in `db_test.go` (every live
table has a reader), `TestCheckInvariantTrips` and
`TestAddL0Trips` in `internal/meta/meta_test.go`, and
`TestEncodeAssertsInvariant` in `internal/manifest/manifest_test.go`.

## 4. Contract tests (one suite, every implementation)

| File | What it covers |
|------|----------------|
| `internal/vfs/vfstest/memfs.go` | `MemFS`: the in-memory `vfs.FS` with fault injection (`SetFault`, `FailNth`) and crash simulation (`Crash`, `CrashTorn`) that the failure and crash tests run on |
| `internal/vfs/vfstest/helpers.go` | `WriteFile` and `ReadFile` test helpers for any `vfs.FS` |
| `internal/vfs/vfstest/contract.go` | The shared `FSContract` suite: read and write, re-create, `ReadAt` boundaries, missing files, rename, list order, `MkdirAll`, readers surviving `Remove`, use after close, exclusive locks, the lock file in `List`. Run by `memfs_test.go`, `countfs_test.go`, and `osfs_test.go` |
| `internal/vfs/countfs/countfs_test.go` | The counting wrapper passes the FS contract, classifies files by kind, and counts bytes and calls per kind |
| `internal/iterator/itertest/contract.go` | The shared iterator `Contract`: not valid before positioning, `SeekGE` lands on the newest version, seek then next stays in order, reposition after exhausted, tombstones and empty values, `Close`. Also the `NewestOnly` reference model and entry helpers |
| `internal/iterator/contract_test.go` | Runs the contract against the slice, merging (inputs split several ways), and dedup iterators, and dedup over merging, the stack reads use. The memtable and SSTable run it in `memtable_test.go` and `sstable_contract_test.go` |
| `internal/sstable/sstable_contract_test.go` | Runs the iterator contract against an SSTable's iterator |

## 5. Property-based / fuzz tests

| File | Kind of property |
|------|------------------|
| `internal/codec/codec_fuzz_test.go` | `FuzzDecodeEntry`: arbitrary bytes never panic, and whatever decodes is a valid entry whose canonical encoding is exactly the bytes consumed (round trip) |
| `internal/memtable/memtable_fuzz_test.go` | `FuzzMemtable`: model-based; a script of puts, deletes, Get, and SeekGE checked against a sorted slice |
| `internal/bloom/bloom_fuzz_test.go` | `FuzzBloomNoFalseNegatives`: every added key is reported as maybe present, and the raw input probed as a filter never panics |
| `internal/wal/wal_fuzz_test.go` | `FuzzWALRoundTrip`: any valid entries round trip with nothing dropped. `FuzzWALReplay`: arbitrary bytes never panic or fail, every entry returned is valid, and the report accounts for every byte |
| `internal/sstable/sstable_fuzz_test.go` | `FuzzSSTableRoundTrip`: entries come back through the iterator, `SeekGE`, and Get with a tiny block size. `FuzzSSTableOpen`: arbitrary bytes give `ErrCorrupt`, never a panic. `FuzzDecoders`: the block and index decoders behind the CRCs |
| `internal/sstable/export_test.go` | Helper, not a test: exposes the block and index decoders for `FuzzDecoders` and the corruption tests, and the writer's invariant check for `TestWriterInvariantTrips` |
| `internal/iterator/merging_fuzz_test.go` | `FuzzMergeIterator`: the merge of 1–5 children equals a sort-and-dedupe reference |
| `internal/compaction/compaction_fuzz_test.go` | `FuzzPlan`: over random valid versions and several Pick/Apply rounds, the plan takes the first file after the cursor, overlaps and the bottom flag are exactly right by brute force, and `Apply` keeps the Version invariant and moves only that level's cursor |
| `internal/manifest/manifest_fuzz_test.go` | `FuzzManifestDecode`: arbitrary bytes decode to a valid Version or `ErrCorrupt`. `FuzzManifestBody`: the same with a valid checksum added, to reach the JSON and invariant checks |
| `internal/manifest/export_test.go` | Helper, not a test: exposes `seal` as `Seal`, so tests can wrap any body in a valid checksum |
| `db_model_fuzz_test.go` | `FuzzDBModel`: model-based (stateful), the main correctness test; a script of puts, deletes, gets, scans, flushes, compactions, reopens, and crashes, checked against a map. `FuzzCrashRecovery`: after a clean or torn crash without `SyncWrites`, the store holds exactly some prefix of the writes, including every write before the last Flush or Close. `TestModelLongRun`: the fixed-seed counterpart, long enough to fill four levels (`-short` runs a tenth) |

No fuzzer-found failures are saved under `testdata/fuzz/` yet; the seed
corpora are in the fuzz functions and run with plain `go test`.

## 6. Integration tests (one adapter against real infrastructure)

| File | What it covers |
|------|----------------|
| `internal/osfs/osfs_test.go` | The real filesystem in a temp dir (also runs the FS contract): `SyncDir` and `Lock` in a missing directory, `List` skipping symlinks |
| `internal/manifest/osfs_test.go` | The manifest written and read on the real filesystem; a byte flipped on disk is caught |
| `cmd/lsmctl/main_test.go` | The CLI's `realMain` in a temp dir: exit codes (1 error, 2 usage, 70 bug) with log IDs, raw errors and panics reported as bugs, argument errors, every command across separate calls, `load` with `-sync` printing acknowledged keys, an interrupted load, a locked store, a corrupt table, the log file next to the store, key quoting, `dump`'s `->` marker, size parsing. The generic `cli.Reporter` is tested in `shared/cli/cli_test.go` |

## 7. System / e2e tests (real binary, `-tags e2e`)

| File | What it covers |
|------|----------------|
| `e2e/harness_test.go` | `TestMain` builds `lsmctl` once; helpers run commands with the test's options as flags, expect friendly errors, inspect the store through the library, and stream and SIGKILL a `-sync load` |
| `e2e/e2e_test.go` | Data persists across processes; a load builds several levels within their targets; SIGKILL at three points loses no acknowledged write and `verify` passes; `compact` shrinks L0; a corrupt table is a friendly error, not a panic; a second opener fails cleanly |
| `e2e/model_test.go` | A fixed-seed 120-step workload of puts, deletes, flushes, and compactions, one process per step, checked against a map (the deterministic counterpart of `FuzzDBModel`) |

## 8. Measurement (experiments, `-tags experiments`)

Deterministic measurements of the paper's claims, counted in bytes and
blocks through `countfs` on an in-memory filesystem. Each logs a table
and asserts a loose expected shape, so the experiments double as
regression checks. Run them with `make experiments`; see
[`DESIGN.md` §8](DESIGN.md#8-experiments-measuring-the-papers-claims).

| File | What it measures |
|------|------------------|
| `experiments/helpers_test.go` | Helper, not a test: keys and values, fresh stores with an event log that copies every store `Event` to the run's `events.jsonl`, and `TestMain`, which hands the run to `shared/experiment` (tables as CSV, `runs.csv`, and the log go to `EXPERIMENT_RESULTS_DIR`) |
| `experiments/e1_batch_merge_test.go` | E1: measured batch-merge parameter M against formula 3.2 at C0/C1 ratios 1–64 (within 10%, growing with S0), and block I/O per insert against 2/M |
| `experiments/e2_write_amp_test.go` | E2: write amplification per level and in total at r = 2, 4, 10, 25, for random and sequential keys, with WAL and manifest bytes; checks `countfs` agrees with `Stats`, every config writes more than 2× the user bytes, sequential costs less than random and about one copy per level, and larger r uses no more levels but costs more on its costliest level |
| `experiments/e3_theorem31_test.go` | E3: Theorem 3.1; merge I/O as S1 varies with S0 and S2 fixed is U-shaped, lowest within 2× of √(S0·S2), and within 25% of formula 3.7 |
| `experiments/e4_read_amp_test.go` | E4: tables probed and blocks read per Get for present and absent keys, with Bloom filters off and at 5, 10, and 20 bits per key, against the predicted false-positive rate |
