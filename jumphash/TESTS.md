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
| `ring_test.go` | `Ring`: add/remove errors, empty ring, keys move only where they should, remove then add restores the mapping, balance (and one assertion-tripping test) |
| `internal/errs/errs_test.go` | Error wrapping: what/when/where context, chains, stack capture |
| `internal/assert/assert_test.go` | The assertion helper itself |
| `internal/cluster/cluster_test.go` | Cluster rules over in-memory fakes: key movement, validation, empty key, dead source, empty cluster, plan purity |

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
| `cmd/kvctl/main_test.go` | The CLI's `realMain`: exit codes, messages with log IDs, bugs and panics reported as bugs, argument errors |

## 7. System / e2e tests (real binaries, `-tags e2e`)

| File | What it covers |
|------|----------------|
| `e2e/harness_test.go` | `TestMain` builds the binaries; helpers start nodes on free ports and wait for health |
| `e2e/e2e_test.go` | Main flows: CRUD, scale out/in, node replacement, lifecycle, unreachable node, source down mid-rebalance, guard rails, log IDs |
| `e2e/model_test.go` | A fixed-seed workload checked against a map through real processes (the deterministic counterpart of `FuzzClusterModel`) |
