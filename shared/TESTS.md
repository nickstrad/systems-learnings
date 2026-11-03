# shared tests

Every test file in this module, grouped by the categories in the repo's
[testing approach](../docs/testing.md). Paths are relative to this
directory.

Keep this file current: add, move, or remove entries in the same change
that adds, renames, deletes, or repurposes a test file.

## 1. Specification-based unit tests (no I/O)

| File | What it covers |
|------|----------------|
| `errs/errs_test.go` | Error wrapping: what/when/where context, chains, stack capture |
| `assert/assert_test.go` | The assertion helper itself, in both assertion builds |
| `assert/asserttest/asserttest_test.go` | `asserttest.Panics` accepts a matching assertion failure; `assert.Fail` panics only when assertions are on |
| `cli/cli_test.go` | `Reporter`: known modules exit 1 with the message and a log ID that matches a JSON log line; everything else, including an error hidden by `errors.Join`, exits 70 as a bug; `Recover` turns a panic into a logged bug; `OpenLog` creates its directory and falls back to stderr |
| `experiment/experiment_test.go` | `Table` writes CSV with `run_id` first and a nil run writes nothing; a wrong-length row panics; `Log` writes JSON lines and returns one file per name; `Sweep` returns results in config order |
