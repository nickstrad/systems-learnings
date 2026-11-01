# jumphash + a toy sharded KV store

A learning project, not production code. It follows the repo-wide
[coding](../docs/coding.md) and [testing](../docs/testing.md) approach.

## Layout: domain vs integration

Domain packages hold the logic and do no I/O. Integration packages connect
them to HTTP, files, and the command line. The `jumphash` library is the only
importable package. Everything app-specific lives under `internal/`, so other
modules can't depend on it.

```
jumphash/
├── jumphash.go, ring.go     public library: package jumphash
├── cmd/
│   ├── kvnode/              binary: one node
│   └── kvctl/               binary: the CLI
├── internal/
│   ├── kv/                  domain: Store
│   ├── cluster/             domain: membership, PlanMoves, rebalance
│   ├── kvhttp/              integration: chi server + HTTP client
│   └── memberfile/          integration: JSON membership file
├── e2e/                     end-to-end tests (build tag: e2e)
└── scripts/demo.sh          manual walkthrough
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
  `internal/assert` and panic when they fail.
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
  `memberfile`, `cluster`, or `kvctl` is an `*errs.Error` (`internal/errs`).
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
| `FuzzRingOps` | root | Ring invariant, minimal movement on every add and remove, and remove then re-add restores the mapping |
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
