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
[testing approach](docs/testing.md).

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
| 3 | **Log-Structured Merge Trees**, O'Neil et al. | 1996 | 4/5 | A tiny KV store: memtable, sorted immutable files, Bloom filters, compaction | Higher write throughput, at the cost of write and read amplification | | Not started |
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

Still to do for this paper:
- benchmarks against modulo hashing and a classic hash ring
- the blog post

See [`jumphash/README.md`](jumphash/README.md).

## Run the projects

### Jump Consistent Hash: `jumphash`

Requires Go 1.26.4 or later and `make`, with ports 8081–8084 available
for the demo (8081–8082 for the manual walkthrough).

From the repository root, run the scripted walkthrough. It starts four
nodes, writes keys, adds and removes nodes, reports how many keys move,
and stops the nodes when it finishes:

```sh
cd jumphash
make demo
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

Run `make test` for unit tests or `make e2e` for tests against real node
processes. See the [project README](jumphash/README.md) for the HTTP API
and implementation details.

## Layout

```
systems-learnings/
├── README.md          this file
├── docs/
│   ├── coding.md      how projects are written: domain vs integration, contracts, errors, logging, concurrency
│   ├── testing.md     how projects are tested: specification-based, structural, property/fuzz, integration, e2e
│   ├── paper-candidates.md  the wider list the 10 were picked from
│   └── papers/        local copies of the papers, with their sources
└── <project>/         one Go module per paper, e.g. jumphash/
```

## Reading notes

Notes on the books behind the coding and testing approach are kept outside
the repo, in `~/AI/writeups/`:
- *Effective Software Testing* (Aniche)
- *Concurrency in Go* (Cox-Buday)
