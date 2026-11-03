# systems-learnings

A Go repository of systems projects exploring the ten papers in
[`docs/papers/`](docs/papers/), covering storage, concurrency, and
distributed systems. Each project turns a paper's ideas into working Go
code, tests, and experiments.

For each paper:

1. Read it. The papers are in [`docs/papers/`](docs/papers/).
2. Implement the idea in Go, small and readable, with tests that check the
   paper's claims.
3. Measure the claims the paper makes.
4. Write a blog post about what I learned.

These are learning projects, not production code. Every project follows
the same [coding approach](docs/coding.md) and
[testing approach](docs/testing.md). Each project has a code walkthrough
and a test walkthrough in [`docs/walkthroughs/`](docs/walkthroughs/).

## The papers

Numbered in a suggested build order:
- **Storage:** papers 1–3.
- **Concurrency:** 4–5. The linearizability checker from 5 tests the queue
  from 4.
- **Distributed systems:** 6–10. One deterministic network simulator is
  built in 6 and reused through 10.

Replication ends with consensus (VR), which reuses the simulator, the
quorum ideas from Dynamo, and the linearizability checker.

| # | Paper | Year | Difficulty | Go experiment | Measured claim | Project | Status |
|---|-------|------|-----------|---------------|----------------|---------|--------|
| 1 | **Jump Consistent Hash**, Lamping & Veach | 2014 | 1/5 | The algorithm, a ring with holes, and a sharded KV store that moves data when nodes join or leave | Growing to n+1 buckets moves about 1/(n+1) of the keys, all of them to the new bucket | [`jumphash/`](jumphash/) | **In progress** |
| 2 | **Reed–Solomon Codes**, Reed & Solomon (implement from Plank's tutorial) | 1960 | 4/5 | GF(2^8) arithmetic and a 4+2 erasure encoder: delete shards, reconstruct, benchmark encode and recovery | Any k of the k+m shards rebuild the data, at (k+m)/k storage overhead | | Not started |
| 3 | **Log-Structured Merge Trees**, O'Neil et al. | 1996 | 4/5 | A tiny KV store: memtable, sorted immutable files, Bloom filters, compaction | Higher write throughput, at the cost of write and read amplification | [`lsmtree/`](lsmtree/) | **In progress** (runnable) |
| 4 | **Concurrent Queues**, Michael & Scott | 1996 | 3/5 | Lock-free queue vs the paper's two-lock queue vs a mutex queue vs a channel | The non-blocking queue scales under contention | | Not started |
| 5 | **Linearizability**, Herlihy & Wing | 1990 | 4/5 | A linearizability checker for histories from paper 4 and later papers | Locality: a history is linearizable iff each object's history is; checking cost grows with concurrency | | Not started |
| 6 | **Distributed Snapshots**, Chandy & Lamport | 1985 | 3/5 | A deterministic network simulator plus the marker algorithm over a conserved quantity | Snapshots are consistent and capture in-flight messages | | Not started |
| 7 | **CRDTs**, Shapiro et al. | 2011 | 3/5 | Counters, registers, and an OR-set, replicated under any delivery order | Strong eventual consistency without coordination | | Not started |
| 8 | **Dynamo**, DeCandia et al. | 2007 | 4/5 | N/R/W quorum reads and writes, vector clocks, read repair; jumphash places the keys | R+W>N means reads see the latest acknowledged write; smaller quorums trade consistency for latency | | Not started |
| 9 | **Congestion Avoidance and Control**, Jacobson | 1988 | 4/5 | Senders sharing a bottleneck link, with slow start, AIMD, and the retransmit-timer estimator | Fair, efficient sharing with no congestion collapse | | Not started |
| 10 | **Viewstamped Replication Revisited**, Liskov & Cowling | 2012 | 5/5 | Normal operation, view change, and recovery over the simulator, with a replicated KV store on top | Safety under crashes and partitions; the system makes progress with f+1 of 2f+1 replicas | | Not started |

## Paper 1: Jump Consistent Hash (`jumphash/`)

Paper 1 grew past the minimal experiment into a small sharded system:

- **The algorithm.** `jumphash.Hash` is the paper's few lines, plus a `Ring`
  that can remove any node, not just the last one, by leaving holes that
  later adds fill.
- **A sharded in-memory KV store.** `kvnode` processes behind chi HTTP
  servers, and a `kvctl` CLI that adds and removes nodes and moves data
  between them.
- **Tests that measure the paper's claims.** For example, growing from n to
  n+1 nodes moves about 1/(n+1) of the keys, and every moved key goes to
  the new node. The tests also cover the data-movement logic, all the way
  up to end-to-end tests against real processes.
- **Experiments that measure the paper's claims.** `make experiments-jumphash`
  counts, without timing anything, (H1) keys moved when n grows to n+1
  (jump hits 1/(n+1) with zero keys moved between old buckets; modulo
  moves n/(n+1); a Karger ring is right only on average), (H2) balance
  (jump is as even as perfect randomness with no state; the ring's σ/μ
  is 1/√v, matching the paper's Figure 2), and (H3) loop iterations per
  lookup (exactly the harmonic number H_n, under the paper's ln(n)+1
  bound, up to 2^20 buckets).

Still to do for this paper:
- wall-clock benchmarks (the paper's Execution Time table)
- the blog post

See [`jumphash/README.md`](jumphash/README.md).

## Paper 3: Log-Structured Merge-Trees (`lsmtree/`)

A small LSM key-value store, built to test the paper's argument:

- **The store.** A skip-list memtable (the paper's C0), a write-ahead log,
  SSTables with Bloom filters, and leveled compaction whose per-level
  cursor is the paper's rolling merge. The manifest records the cursors,
  so a restart resumes the sweep.
- **An `lsmctl` CLI.** Every command is its own process, so the demo
  shows data surviving restarts and a `kill -9`.
- **Tests that hunt for lost writes.** A model-based fuzz test checks
  any sequence of writes, flushes, compactions, reopens, and crashes
  against a map. A crash test checks that recovery always lands on a
  prefix of the writes. A fault-injection sweep fails every filesystem
  call in turn; it found a real bug where a failed directory sync after
  a manifest rename made compaction delete tables the new manifest
  still listed.
- **Experiments that measure the paper's claims** in its own units
  (pages and bytes, not time). Results go to CSV and JSON lines and are
  summarised with DuckDB:

  | Claim | Result |
  |-------|--------|
  | Batch-merge parameter M, formula 3.2 | measured M is 0.97× the formula at every C0/C1 ratio |
  | Write amplification grows with the size ratio r (§3.3) | per-level cost grows with r; total is lowest near r = 4 (formula 3.6) |
  | Theorem 3.1: geometric component sizes minimise merge I/O | a U-shaped curve with its minimum next to √(S0·S2), within 11% of formula 3.7 |
  | Finds cost more; Bloom filters cut absent-key reads | absent-key blocks per Get fall from 5.6 to 0.046 at 10 bits per key, as predicted |

Still to do for this paper:
- E5: compare against a B+tree (bbolt), the paper's headline comparison
- optional: background compaction (DESIGN.md milestone 7)
- the blog post

See [`lsmtree/README.md`](lsmtree/README.md) and
[`lsmtree/DESIGN.md`](lsmtree/DESIGN.md).

## Run the projects

Use the root Makefile to run every project or choose one:

```sh
make test           # all project test suites: check + e2e
make test-jumphash  # jumphash only: vet, formatting, race, noassert, and e2e
make demo           # all project demos, one at a time
make demo-jumphash  # jumphash's scripted walkthrough
make test-lsmtree   # lsmtree only: vet, formatting, race, noassert, and e2e
make demo-lsmtree   # lsmtree's scripted walkthrough, including a kill -9
make experiments    # every project's experiments, then their DuckDB tables
make experiments-lsmtree RUN=E3  # one project's experiments; RUN picks some
make experiments-jumphash RUN=H1
make test-shared    # the shared helper module
make help           # available commands and discovered projects
```

Projects are discovered from top-level directories containing `go.mod`.
Each project's Makefile provides `check`, `e2e`, and `demo` targets.
`shared/` is discovered too. It is a library of helpers, not a paper
project, so its `e2e` and `demo` targets do nothing. Projects with an
`experiments/` package get `experiments-<project>` targets; each run
writes `<project>/results/<run>/` and prints the project's DuckDB queries
(see [`shared/README.md`](shared/README.md#experiments)).

### Jump Consistent Hash: `jumphash`

Requires Go 1.26.4 or later and `make`, with ports 8081–8084 available
for the demo (8081–8082 for the manual walkthrough), and optionally
DuckDB for the experiments' summary tables.

From the repository root, run the scripted walkthrough. It starts four
nodes, writes keys, adds and removes nodes, reports how many keys move,
and stops the nodes when it finishes:

```sh
make demo-jumphash
```

To interact with the cluster yourself, build the CLI and node server:

```sh
cd jumphash  # from the repository root
make build
```

In two separate terminals, each in `jumphash/`, start one node:

```sh
# Terminal 1
bin/kvnode -addr 127.0.0.1:8081
```

```sh
# Terminal 2
bin/kvnode -addr 127.0.0.1:8082
```

In a third terminal in `jumphash/`, use `kvctl` to write and read values,
find their owner, and watch data move as membership changes:

```sh
bin/kvctl add-node localhost:8081
bin/kvctl fill 1000
bin/kvctl put user:42 hello
bin/kvctl get user:42                 # hello
bin/kvctl where user:42               # the node that owns this key
bin/kvctl add-node localhost:8082     # moves about half the keys
bin/kvctl nodes
bin/kvctl stats
bin/kvctl remove-node localhost:8081  # moves its keys to the remaining node
bin/kvctl get user:42                 # still hello
bin/kvctl del user:42
```

Use `bin/kvctl -h` for all commands; global flags such as `-v` go before
the command. The CLI stores membership in `cluster.json` and logs in
`kvctl.log` in the current directory. All commands for a cluster must use
the same membership file (`-cluster <path>` overrides the default).
Nodes hold data in memory, so stopping them loses their keys. Stop each
node with Ctrl-C and use a fresh membership file for a new walkthrough.

To measure the paper's claims, run `make experiments-jumphash` from the
root (or `make experiments` in `jumphash/`; about a second). Pass
`RUN=H1` to run only matching experiments; each summary table then shows
that experiment's latest run, so run them all once first. It writes
`jumphash/results/<UTC time>-<commit>/` with one CSV per table, then
prints the summary tables with DuckDB if it is installed. Remove
`results/` to reset. See the
[project README](jumphash/README.md#experiments-measuring-the-papers-claims)
for the results.

Run `make test` for unit tests or `make e2e` for tests against real node
processes. See the [project README](jumphash/README.md) for the HTTP API
and implementation details.

### The Log-Structured Merge-Tree: `lsmtree`

Requires Go 1.26.4 or later and `make`. No ports or services. The
experiments' summary tables need [DuckDB](https://duckdb.org/docs/installation/)
(optional).

From the repository root, run the scripted walkthrough. It fills a
temporary store until it has several levels, shows the level tree and
the rolling-merge cursors, then kills a synced load with `kill -9` and
shows that every acknowledged write survived. It removes the store and
any processes it started when it finishes:

```sh
make demo-lsmtree
```

To use the store yourself, build the CLI:

```sh
cd lsmtree  # from the repository root
make build
```

Each `lsmctl` command is its own process: it opens the store (in `data/`
by default), runs, and closes it. Small sizes make flushes and
compactions visible after a few thousand keys. Tuning flags are not
saved, so pass the same ones to every command:

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

To see crash safety, run `lsm -sync load 100000 > acks.txt`. It fsyncs
every write and prints each key once it is durable. Stop it with
`kill -9` from another terminal, then run `lsm verify` and `lsm get` on
the last key in `acks.txt`.

Errors print a log ID; the details are in `lsmctl.log` next to the store
directory. `lsmctl -h` lists all commands and flags; global flags go
before the command. Ctrl-C stops a `load` cleanly. To start over, run
`rm -rf data lsmctl.log` (`make clean` removes `bin/` and logs).

To measure the paper's claims, run `make experiments-lsmtree` from the
root (or `make experiments` in `lsmtree/`; about 5 seconds). Pass
`RUN=E3` to run only matching experiments; the summary then shows each
experiment from its latest run, so run them all once first. It writes
`lsmtree/results/<UTC time>-<commit>/` with one CSV per experiment and `events.jsonl` (every flush and compaction), then
prints the summary tables with DuckDB if it is installed. Remove
`results/` to reset. `make e2e` in `lsmtree/` runs the end-to-end tests
against the real binary, including the SIGKILL crash test. See the
[project README](lsmtree/README.md) for the design and the results.

## Layout

```
systems-learnings/
├── README.md          this file
├── Makefile           run all projects' test suites and demos, or select one
├── docs/
│   ├── coding.md      how projects are written: domain vs integration, contracts, errors, logging, concurrency
│   ├── testing.md     how projects are tested: specification-based, structural, property/fuzz, integration, e2e
│   ├── walkthroughs/  per project: the code in reading order, and every test by category, with C4 diagrams
│   ├── paper-candidates.md  the wider list the 10 were picked from
│   └── papers/        local copies of the papers, with their sources
├── shared/            Go module with helpers every project imports: errs, assert, logx, cli
└── <project>/         one Go module per paper, e.g. jumphash/, lsmtree/
```

## Reading notes

Notes on the books behind the coding and testing approach are kept outside
the repo, in `~/AI/writeups/`:
- *Effective Software Testing* (Aniche)
- *Concurrency in Go* (Cox-Buday)
