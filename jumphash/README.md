# jumphash + a toy sharded KV store

A learning project, not production code. It follows the repo-wide
[coding](../docs/coding.md) and [testing](../docs/testing.md) approach.
[`TESTS.md`](TESTS.md) lists every test file by category. For a guided
tour, read the [code walkthrough](../docs/walkthroughs/jumphash/code.md)
and the [test walkthrough](../docs/walkthroughs/jumphash/tests.md).

## Layout: domain vs integration

Domain packages hold the logic and do no I/O. Integration packages connect
them to HTTP, files, and the command line. The `jumphash` library is the only
importable package. Everything app-specific lives under `internal/`, so other
modules can't depend on it. The error, assertion, and logging helpers,
and kvctl's top level (`shared/cli`: log file, error reporting, exit
codes), come from the repo's [`shared`](../shared/) module.

```
jumphash/
├── jumphash.go, ring.go     public library: package jumphash
├── cmd/
│   ├── kvnode/              binary: one node
│   └── kvctl/               binary: the CLI
├── internal/
│   ├── kv/                  domain: Store
│   ├── cluster/             domain: membership, PlanMoves, rebalance
│   │   └── clustertest/     test helpers: in-memory fakes and contract suites
│   ├── kvhttp/              integration: chi server + HTTP client
│   └── memberfile/          integration: JSON membership file
├── e2e/                     end-to-end tests (build tag: e2e)
├── experiments/             measurements of the paper's claims (build tag: experiments)
└── scripts/
    ├── demo.sh              manual walkthrough
    └── experiments.sql      DuckDB summary of the experiments' results
```

| Package | Kind | What it does |
|---------|------|--------------|
| `jumphash` (root) | domain | Jump Consistent Hash, plus `Ring`, which can remove any node by leaving a "hole" |
| `internal/kv` | domain | `Store`: one node's data, a `map` behind a lock |
| `internal/cluster` | domain | `Membership` (who owns each key), `PlanMoves` (which keys move, a pure function), and `Cluster` (add/remove in copy → commit → clean-up order) |
| `internal/kvhttp` | integration | chi server exposing a `kv.Store`, and an HTTP `Client` that satisfies `cluster.Node` |
| `internal/memberfile` | integration | JSON file that satisfies `cluster.MembershipStore` |
| `cmd/kvnode`, `cmd/kvctl` | integration | flags, output, and wiring the pieces together |

`cluster` reaches nodes and storage only through two interfaces:

```go
type Node interface { Health; Get; Put; Delete; All }   // kvhttp.Client
type MembershipStore interface { Load; Save }            // memberfile.File
```

Because of that, the cluster tests use in-memory fake nodes, with no network
and no disk. The `e2e/` suite runs the real binaries.

```
 kvctl ── cluster.Open(memberfile.File, dial → kvhttp.Client)
              │
   hash(key) → Membership (jumphash.Ring) → owner node
              │  HTTP
   ┌──────────┼──────────┐
 kvnode     kvnode     kvnode      (each: kvhttp server around a kv.Store)
```

## Contracts and assertions

Design by contract, following *Effective Software Testing* chapter 4:

- **Input validation returns errors.** User input, network responses, and
  files are untrusted. Examples:
  - an empty key gets `ErrEmptyKey`
  - an HTTP request with an empty key gets a 400
  - a corrupt `cluster.json` gets a parse error
- **Contracts are asserted.** Preconditions, postconditions, and invariants
  hold unless this program has a bug. They are checked with
  `shared/assert` and panic when they fail.
- **Assertions are on by default.** Build with `-tags noassert` to compile
  them out. `make check` tests both builds.

The two integration points, `cluster.Node` and `cluster.MembershipStore`
(see `internal/cluster/contract.go`), document a contract for each method:

- `cluster.Open` wraps every implementation in a checker that asserts its
  contract on every call.
- `internal/cluster/clustertest` has one contract suite per interface. The
  real adapters (`kvhttp.Client`, `memberfile.File`) and the in-memory
  fakes must all pass it.

The main invariants:

| Invariant | Where | Checked |
|-----------|-------|---------|
| Ring slots, index, and holes agree | `ring.go` | after every Add and Remove |
| Membership ring is the replay of its history | `cluster/membership.go` | on construction and Apply |
| Minimal movement: a well-placed key moves only onto an added node, or off a removed one | `cluster/rebalance.go` | before every copy phase |
| Every planned move goes to the key's owner | `cluster/plan.go`, `rebalance.go` | when planning and after commit |

## Errors and logging

Follows the error-propagation model in Cox-Buday's *Concurrency in Go*
(write-up: `~/AI/writeups/cox_buday_concurrency_in_go_errors_logging.md`).

- **Wrap at module boundaries.** Every error that leaves `kvhttp`,
  `memberfile`, `cluster`, or `kvctl` is an `*errs.Error` (`shared/errs`).
  It carries:
  - the module
  - a one-line friendly message
  - the cause
  - a UTC timestamp
  - a stack trace
  - extra context (URL, path, the node's request ID, ...)

  `errors.Is` still finds sentinels such as `cluster.ErrEmptyKey`.
- **The top level (`kvctl`) decides between edge case and bug:**
  - The outermost error is from `cluster` or `kvctl`: a known edge case.
    Shows the message and exits 1.
  - Anything else, including a panic or a failed assertion: a bug. Shows
    "this is a bug" and exits 70.
- **Log IDs.** Every error prints `[log ID 3f9a1c0b]`. The matching JSON
  line in `kvctl.log` (next to the cluster file, or set with `-log`) holds
  the full chain and the stack.
- **Nodes log as JSON** on stderr, one line per request, with a request ID.
  The ID is also sent back in `X-Request-Id`, and the client copies it into
  its errors, so a CLI log line can be matched to the node's log line.
- **Concurrency.** Rebalance and `stats` read all nodes at once, one
  goroutine per node. Each goroutine sends its result and its error
  together on one channel. The parent decides what to do:
  - rebalance cancels the rest and stops at the first failure
  - stats reports each node separately

  A panic in one of these goroutines is passed back to the parent, so it
  is still reported as a bug instead of crashing the program.

```
$ kvctl add-node localhost:1
error: cannot add http://localhost:1, nothing changed: cannot reach node http://localhost:1 (is it running?) [log ID 6aa56b22]
```

## Run it

```sh
make test     # unit tests
make e2e      # end-to-end: real processes, real HTTP, driven through the CLI
make demo     # scripted walkthrough with 4 nodes on :8081-8084
make build    # bin/kvnode, bin/kvctl
make experiments  # measure the paper's claims (see "Experiments")
```

By hand:

```sh
bin/kvnode -addr :8081 &
bin/kvnode -addr :8082 &
bin/kvctl add-node localhost:8081
bin/kvctl fill 1000
bin/kvctl add-node localhost:8082     # moves ~50% of keys onto :8082
bin/kvctl stats
bin/kvctl put user:42 hello && bin/kvctl where user:42
bin/kvctl remove-node localhost:8081  # moves all of :8081's keys to :8082
```

## Experiments: measuring the paper's claims

Three experiments in `experiments/` measure the hash itself, not the
cluster. They count keys and loop iterations over fixed, seeded key
samples, and never time anything, so every run on every machine gives the
same numbers. Each prints a table and asserts only a loose expected
shape, so the suite doubles as a regression check. The baselines live
in the experiments package: modulo hashing (`key % n`) and a classic
Karger consistent-hash ring (each bucket hashed to `v` points on a
64-bit circle; a key goes to the next point clockwise). That ring is a
textbook baseline, not `jumphash.Ring`.

| Test | Claim | Measured |
|------|-------|----------|
| `TestH1Movement` | Growing n → n+1 moves 1/(n+1) of the keys, all to the new bucket | 100,000 keys, n = 1..1000: fraction moved, keys moved between old buckets ("misplaced"), donor buckets; jump vs modulo vs the ring at v = 1, 10, 100, 1000 |
| `TestH2Balance` | Jump splits keys evenly with no state; a ring needs many points per bucket | 1,000,000 keys at n = 10, 100, 1000: min/mean, max/mean, stddev against the binomial √(K·p·(1-p)), the ring's exact key-space σ/μ against the paper's Figure 2, and bytes of state |
| `TestH3Jumps` | A lookup takes 1 + Σ_{i=2..n} 1/i = H_n iterations on average, which the paper bounds by ln(n) + 1 | 1,000,000 keys, n = 1..2^20: mean, stddev, and max iterations, counted by a copy of `Hash` that is checked against `Hash` for every key; plus a least-squares fit to a·ln(n) + b |

Run them from the repository root, or with `make experiments` in this
directory (both take about a second). `RUN=` picks some by `go test -run`
pattern:

```sh
make experiments-jumphash          # all three, then the DuckDB tables
make experiments-jumphash RUN=H1   # only TestH1Movement
go test -tags experiments -count=1 -v -run TestH2 ./experiments   # print only, saves nothing
```

Each run writes `results/<UTC time>-<commit>/`: `runs.csv` (run ID,
commit, Go version), `h1_movement.csv`, `h2_balance.csv`,
`h3_jumps.csv`, `h3_fit.csv`, and `test.log`. Every row starts with the
run ID. If [DuckDB](https://duckdb.org/docs/installation/) is installed,
the runner then prints the summary tables from `scripts/experiments.sql`,
which builds on the shared
[`experiments_prelude.sql`](../shared/scripts/experiments_prelude.sql).
Each table shows the latest run that measured it, so after `RUN=H1` the
H2 and H3 tables still show their last results. Run all three once first,
or the script stops and says so. The last table compares the headline
numbers across runs. Delete `results/` to reset.

### Results

From one run (the numbers are the same on every run).

**H1: minimal movement.** Fraction of keys that move when n grows by one:

| n | 1/(n+1) | jump | modulo | ring v=1 | ring v=10 | ring v=1000 |
|---|---------|------|--------|----------|-----------|-------------|
| 1 | 0.5 | 0.5001 | 0.5026 | 0.0480 | 0.5424 | 0.5090 |
| 2 | 0.3333 | 0.3323 | 0.6681 | 0.9382 | 0.5325 | 0.3332 |
| 10 | 0.0909 | 0.0904 | 0.9077 | 0.0078 | 0.0925 | 0.0954 |
| 100 | 0.00990 | 0.00977 | 0.98993 | 0.00617 | 0.00406 | 0.00969 |
| 1000 | 0.00100 | 0.00106 | 0.99907 | 0.00232 | 0.00134 | 0.00109 |

- Jump stays within 9% of 1/(n+1) at every n in the sweep, and within 3%
  up to n = 150. Beyond that only a few hundred of the 100,000 keys move,
  so sampling noise dominates. It never moved a key between old buckets.
- Modulo moves n/(n+1) of the keys, exactly as predicted: n times the
  minimum. Almost all of its moves land on an old bucket.
- The ring is monotone too (0 misplaced keys), and right on average, but
  any one step is not: at v = 1 it moved between 0.05× and 3.5× the
  minimum, and at v = 10 between 0.4× and 1.7×. Only v = 1000 comes
  close to jump. It also takes the keys from at most v donors: at
  v = 1 the new bucket splits a single old bucket, and at v = 10 and
  n = 100 one donor gave 31% of the moved keys. Jump takes an even slice
  from every bucket (the largest donor gave 2.3%, about 1/n).

**H2: balance.** Keys per bucket, 1,000,000 keys, n = 1000 (the paper's
Figure 2 values in the "paper σ/μ" column):

| method | min/mean | max/mean | stddev / binomial | key-space σ/μ | paper σ/μ | state |
|--------|----------|----------|-------------------|---------------|-----------|-------|
| jump | 0.902 | 1.096 | 0.97 | — | 7.6·10⁻⁹ | 0 B |
| modulo | 0.857 | 1.113 | 1.01 | — | — | 0 B |
| ring v=1 | 0.001 | 7.40 | 31.6 | 0.999 | 0.998 | 12 KB |
| ring v=10 | 0.313 | 2.35 | 9.96 | 0.313 | 0.315 | 120 KB |
| ring v=100 | 0.666 | 1.37 | 3.33 | 0.098 | 0.100 | 1.2 MB |
| ring v=1000 | 0.864 | 1.12 | 1.42 | 0.032 | 0.032 | 12 MB |

- Jump's spread is what perfect randomness gives (stddev ratio 0.64,
  0.94, and 0.97 at n = 10, 100, 1000; with only 10 buckets the estimate
  itself is noisy). Its min and max at n = 1000 are sampling noise: about
  1000 keys per bucket.
- The ring's exact key-space σ/μ is 1/√v, matching Figure 2 to within
  2% at n = 1000. Even at v = 1000 points per bucket (12 MB for 1000
  buckets), its 3.2% imbalance adds to the sampling noise: its stddev is
  1.4× the binomial, where jump's is 1.0×. With more keys per bucket,
  jump's spread keeps shrinking and the ring's stays at 3.2%.

**H3: cost in loop iterations.** Measured mean against H_n and the
paper's bound:

| n | measured | H_n | ln(n)+1 | log2(n) | max |
|---|----------|-----|---------|---------|-----|
| 2 | 1.500 | 1.500 | 1.693 | 1 | 2 |
| 16 | 3.381 | 3.381 | 3.773 | 4 | 11 |
| 1024 | 7.511 | 7.509 | 7.931 | 10 | 22 |
| 65536 | 11.667 | 11.668 | 12.090 | 16 | 29 |
| 1048576 | 14.439 | 14.440 | 14.863 | 20 | 34 |

- The mean matches H_n to within 0.05% at every n, and stays under the
  paper's ln(n) + 1. A least-squares fit gives 0.992·ln(n) + 0.655
  (R² = 0.9999); theory says 1·ln(n) + γ (0.577) plus 1/(2n), which
  pulls the fitted intercept up at small n.
- That is fewer steps than a binary search over n sorted keys (log2 n)
  by a factor of ln 2 ≈ 0.69, as the paper says, and far fewer than a
  ring's binary search over n·v points.

Not measured yet: wall-clock speed (the paper's "Execution Time" table).
That needs benchmarks rather than counts.

## Node HTTP API

| Method | Path        | Result                                                     |
|--------|-------------|------------------------------------------------------------|
| GET    | `/healthz`  | `ok`                                                       |
| GET    | `/kv`       | JSON object of every entry                                 |
| GET    | `/kv/{key}` | value, or 404                                              |
| PUT    | `/kv/{key}` | 204, request body is the value; 400 unless key and value are valid UTF-8 |
| DELETE | `/kv/{key}` | 204, or 404                                                |

Keys and values must be valid UTF-8 because `GET /kv` sends them as JSON,
which would turn invalid bytes into U+FFFD. A rebalance would then copy a
mangled key and value. `FuzzKVHTTPRoundTrip` found this.

## How data moves (`internal/cluster/rebalance.go`, `internal/cluster/plan.go`)

A membership change runs in three phases:

1. **Copy**: build the membership as it will be after the change. Scan every
   node, including one that is joining, and use `PlanMoves` to list the keys
   whose owner changes. PUT each one onto its new owner. Then delete any
   stale copy that sits on a key's new owner, so the commit can't expose
   it. Only stale copies are deleted, so a failure here changes no read and
   no membership.
2. **Commit**: write the new `cluster.json`. Routing switches now.
3. **Clean up**: DELETE the moved keys from their old nodes, and every
   other stale copy.

**The owner's copy is the only source of truth.** Reads go only to a key's
owner, so if the owner has no copy, the key doesn't exist, whatever other
nodes hold. A failed clean-up, a copy phase that failed part-way, or a node
that left with data and later came back all leave stale copies on nodes
that don't own them. Reads never see them. The next change deletes them
and never copies them, because without versions there is no way to tell a
stale copy from a live one:

- An old value must never overwrite a newer one
  (`TestStaleCopyNeverOverwritesNewerValue`).
- A deleted key must never come back. An earlier version "repaired" a key
  found only on the wrong node by copying it onto its owner. That
  resurrected keys deleted after a failed clean-up
  (`TestDeletedKeyStaysDeletedAfterFailedCleanUp`). A node rejoining with
  stale copies also served them, because a joining node wasn't scanned
  (`TestReAddedNodeDoesNotServeStaleCopies`). `FuzzClusterModel` found
  both.

This trades availability for correctness: a copy with no owner copy is
deleted, even in the rare case where it was the only one left.

Membership is stored as a **history of add/remove ops**, not a node list.
`Ring`'s mapping depends on which slots are holes, so the CLI replays the
history to rebuild the same ring every time.

## What the e2e suite proves (`e2e/`)

| Test | Property |
|------|----------|
| `TestSingleNodeCRUD` | put/get/del/where work through the CLI |
| `TestScaleOutMovesOnlyOntoNewNode` | 3→4 nodes moves ~25% of keys, and every moved key lands on the new node |
| `TestScaleInMovesOnlyRemovedNodesKeys` | removing a node moves exactly its keys, and no other key changes node |
| `TestReplacementNodeInheritsExactKeySet` | remove B, add E: E gets exactly B's old keys (the hole is reused) |
| `TestLifecycleGrowThenShrink` | 1→5→1 nodes, with data checked after every step; logs a moves table |
| `TestAddUnreachableNodeIsRejected` | a dead node can't join |
| `TestRebalanceAbortsWhenSourceNodeIsDown` | a failed scan leaves membership and data untouched |
| `TestMembershipGuardRails` | duplicate adds, unknown removes, and removing the last node while it holds data are all rejected |
| `TestFailureGivesLogIDWithFullDetails` | a failing command prints a log ID, and the log has the full error chain under that ID |
| `TestWorkloadMatchesModel` | 150 fixed-seed steps mixing puts, overwrites, deletes, odd keys, adds, removes, and a dead-node add, checked against a plain map: no lost writes, no stale reads, no resurrected deletes, and each key on exactly one member |

The assertions don't trust the CLI's own report. They ask each node directly
what it holds (`GET /kv`). Sample output from `TestLifecycleGrowThenShrink`:

```
step        nodes   moved       %
add n1          2     983   49.1%
add n2          3     663   33.1%
add n3          4     498   24.9%
add n4          5     417   20.9%
remove n2       4     399   19.9%
remove n0       3     503   25.1%
remove n4       2     703   35.1%
remove n1       1     979   49.0%
```

## Property-based tests (Go fuzzing)

Each target's seed corpus, plus every failure the fuzzer has found
(`testdata/fuzz/`), runs as a regression test with plain `go test`. To
search for new failures, fuzz one target at a time:

```sh
go test -run='^$' -fuzz=FuzzClusterModel -fuzztime=60s ./internal/cluster
```

| Target | Package | Property |
|--------|---------|----------|
| `FuzzClusterModel` | `internal/cluster` | A script of add/remove/put/delete and injected faults (down nodes, failing deletes, puts failing part-way through a copy phase) runs against the real `Cluster` and a map-plus-set model. Reads always match the model: no lost writes, stale reads, or resurrected deletes |
| `FuzzPlanMoves` | `internal/cluster` | Applying a plan changes no read before or at the commit, and leaves each live key only on its new owner |
| `FuzzHash` | root | For any key and bucket counts: result in range (or -1), and growing from n to m buckets never moves a key between two old buckets |
| `FuzzRingOps` | root | Ring invariant, minimal movement on every add and remove, remove then re-add restores the mapping, and `Get` returns a key's first slot whenever that slot is live, over fuzzer-chosen sample keys |
| `FuzzKVHTTPRoundTrip` | `internal/kvhttp` | Any key and value survive put, get, list, and delete over real HTTP |
| `FuzzMemberfileLoad` | `internal/memberfile` | Any file loads as a valid membership or a well-formed error, and save then load round-trips |

## Deliberately missing (good blog material)

- **Writes during a rebalance can be lost.** A write that reaches an old owner
  between copy and commit is deleted in cleanup. Fixes: dual writes, versioned
  values, or fencing the old owner.
- **One client owns membership.** Two `kvctl`s with different `cluster.json`
  files will disagree. Real systems use a coordinator (etcd, gossip, Raft).
- **No replication.** If a node dies, its keys are gone.
- **No persistence.** If a node restarts, its keys are gone.
- **Full scans.** Every rebalance reads every key on every node. Real systems
  track key ranges or keep per-bucket indexes.
- **Many holes slow lookups down.** See the `Ring` docs and MementoHash.
