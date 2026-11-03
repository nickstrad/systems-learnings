# jumphash tests

Every test file in this project, grouped by the categories in the repo's
[testing approach](../docs/testing.md). A file that fits more than one
category is listed under its main purpose, with its other roles in
parentheses. Files are listed in a suggested reading order: the domain
first, then outward to the real binaries. Paths are relative to this
directory.

Keep this file current: add, move, or remove entries in the same change
that adds, renames, deletes, or repurposes a test file.

## 1. Specification-based unit tests (domain logic, no I/O)

| File | What it covers |
|------|----------------|
| `jumphash_test.go` | `Hash`: invalid bucket counts, one bucket, determinism, range, minimal movement, uniform spread, plus a benchmark |
| `ring_test.go` | `Ring`: add/remove errors, empty ring, keys move only where they should, remove then add restores the mapping, balance, plus `BenchmarkRingGet` (also structural: `TestRingInvariantCatchesCorruption`) |
| `internal/cluster/cluster_test.go` | Cluster rules over in-memory fakes: key movement, validation, empty key, dead source, empty cluster, plan purity |

The `errs`, `assert`, `cli`, and `logx` helpers are tested in the shared
module; see [`../shared/TESTS.md`](../shared/TESTS.md).

## 2. Failure-path and regression unit tests (fakes inject faults)

| File | What it covers |
|------|----------------|
| `internal/cluster/failure_test.go` | Each rebalance step failing (copy, clear, save, clean-up) leaves data safe and membership unchanged |
| `internal/cluster/stale_test.go` | Stale-copy regressions: an older copy never overwrites a newer value, and deleted keys stay deleted |
| `internal/cluster/errors_test.go` | Every public error is a well-formed cluster error; panics are never hidden behind an error |

## 3. Structural tests: proving the assertions can fail

| File | What it covers |
|------|----------------|
| `internal/cluster/contract_test.go` | Broken stubs show that each interface pre/postcondition assertion trips |
| `internal/cluster/export_test.go` | Helper, not a test: exposes internal checked wrappers to the tests above |

Two more assertion-tripping tests live with the code they guard:
`TestRingInvariantCatchesCorruption` in `ring_test.go` and
`TestMinimalMovementInvariantCatchesBadPlan` in
`internal/cluster/cluster_test.go`.

## 4. Contract tests (one suite, every implementation)

| File | What it covers |
|------|----------------|
| `internal/cluster/clustertest/fakes.go` | In-memory fakes (`MemNode`, `MemStore`) with switches for being down or failing writes |
| `internal/cluster/clustertest/contract.go` | The shared `NodeContract` and `StoreContract` suites |
| `internal/cluster/clustertest/clustertest_test.go` | Runs the contracts against the fakes. The real adapters run them in `kvhttp_test.go` and `memberfile_test.go` |

## 5. Property-based / fuzz tests

| File | Kind of property |
|------|------------------|
| `jumphash_fuzz_test.go` | `FuzzHash`: for any key and bucket counts, the result is in range (or -1), and growing from n to n+1 or to any m > n never moves a key between two old buckets |
| `ring_fuzz_test.go` | `FuzzRingOps`: ring invariant, minimal movement, remove then re-add restores the mapping (metamorphic), and `Get` returns a key's first slot when it is live, over fuzzer-chosen sample keys |
| `internal/cluster/plan_fuzz_test.go` | `FuzzPlanMoves`: properties of `PlanMoves` over random memberships and data |
| `internal/cluster/model_fuzz_test.go` | `FuzzClusterModel`: model-based (stateful); a script of operations and injected faults, checked against a plain map |
| `internal/cluster/testdata/fuzz/FuzzClusterModel/` | Saved failing inputs, rerun as regression tests by plain `go test` |
| `internal/kvhttp/kvhttp_fuzz_test.go` | `FuzzKVHTTPRoundTrip`: put, get, list, delete round trip over real HTTP, mostly testing key escaping |
| `internal/memberfile/memberfile_fuzz_test.go` | `FuzzMemberfileLoad`: arbitrary bytes never cause a panic, and save then load round-trips |

## 6. Integration tests (one adapter against real infrastructure)

| File | What it covers |
|------|----------------|
| `internal/kvhttp/kvhttp_test.go` | HTTP client and server through `httptest` (also runs the Node contract), size limits, empty key |
| `internal/kvhttp/errors_test.go` | Client errors are well-formed, bad replies, request logging with IDs, panic recovery to a 500 |
| `internal/memberfile/memberfile_test.go` | File store in a temp dir (also runs the Store contract): format, corrupt files, bad history, unusable paths |
| `cmd/kvctl/main_test.go` | The CLI's `realMain`: exit codes, messages with log IDs, bugs and panics reported as bugs, argument errors. The generic `cli.Reporter` is tested in `shared/cli/cli_test.go` |

## 7. System / e2e tests (real binaries, `-tags e2e`)

| File | What it covers |
|------|----------------|
| `e2e/harness_test.go` | `TestMain` builds the binaries; helpers start nodes on free ports and wait for health |
| `e2e/e2e_test.go` | Main flows: CRUD, scale out/in, node replacement, lifecycle, unreachable node, source down mid-rebalance, guard rails, log IDs |
| `e2e/model_test.go` | A fixed-seed workload checked against a map through real processes (the deterministic counterpart of `FuzzClusterModel`) |

## 8. Measurement (experiments, `-tags experiments`)

Deterministic measurements of the paper's claims about the hash itself,
counted in keys and loop iterations over seeded key samples, never
timed. Each logs a table and asserts a loose expected shape, so the
experiments double as regression checks. Run them with `make
experiments`; see the README's
[Experiments](README.md#experiments-measuring-the-papers-claims) section.

| File | What it measures |
|------|------------------|
| `experiments/helpers_test.go` | Helper, not a test: package doc, `TestMain` (`experiment.Main`, which saves the tables as CSV under `EXPERIMENT_RESULTS_DIR`), seeded key samples, `mix64`, and spread statistics |
| `experiments/baselines_test.go` | Helper, not a test: the baselines, modulo hashing and a minimal Karger ring (v points per bucket on a 64-bit circle, with its exact key-space shares); not `jumphash.Ring` |
| `experiments/h1_movement_test.go` | H1: growing n → n+1 for n = 1..1000; jump moves 1/(n+1) of the keys (within 5σ) and never between old buckets, modulo moves n/(n+1), and a ring at v = 1..1000 is monotone with at most v donor buckets |
| `experiments/h2_balance_test.go` | H2: keys per bucket at n = 10, 100, 1000; jump's stddev is near the binomial, the ring at v = 1 is at least 10× worse, and the ring's key-space σ/μ falls with v and is within 15% of 1/√v at n = 1000 (the paper's Figure 2) |
| `experiments/h3_jumps_test.go` | H3: loop iterations per lookup for n = 1..2^20, within 5 standard errors of H_n and under the paper's ln(n)+1, with a log fit; its instrumented copy of `Hash` is checked against `Hash` for every key |
