# shared

Small helper packages used by every project in this repo. They are not a
paper project. Before this module existed, each project would have needed
its own copy, because Go's `internal/` packages can't be imported across
modules.

| Package | What it is | Rules it follows |
|---------|------------|------------------|
| `errs` | The well-formed error type: message, cause, UTC time, stack, context | [coding approach §3](../docs/coding.md#3-error-handling) |
| `assert` | `assert.That` and `assert.Fail` for preconditions, postconditions, and invariants. Compiled out with `-tags noassert`. `assert/asserttest.Panics` shows a check can trip | [coding approach §2](../docs/coding.md#2-validation-vs-contracts) |
| `logx` | JSON `slog` loggers with UTC timestamps, and short log IDs | [coding approach §4](../docs/coding.md#4-logging) |
| `experiment` | Plumbing for every project's experiments: `Table` (prints with `t.Log`, saves CSV with `run_id`), JSON-lines logs, the run directory and `runs.csv`, `Sweep` for parallel configurations, and `Main` for `TestMain` | [Experiments](#experiments) below |
| `cli` | The top level of every command: `OpenLog`, a `Reporter` that shows known errors with a log ID and reports everything else (and panics, via `Recover`) as a bug, and the exit codes 0/1/2/70. Used by `kvctl` and `lsmctl` | [coding approach §3](../docs/coding.md#3-error-handling) |

## Experiments

A project that measures its paper's claims puts them in an
`experiments/` package (Go tests behind the `experiments` build tag)
using `shared/experiment`, plus a `scripts/experiments.sql` of DuckDB
queries. [`scripts/experiments.sh`](scripts/experiments.sh) runs both:
it makes `<project>/results/<UTC time>-<commit>/`, runs the tests with
`EXPERIMENT_RESULTS_DIR` set so they write their CSV and JSON-lines
files there, then prints the queries' tables if `duckdb` is installed.
Every row carries `run_id`, so the queries can read all runs by glob
(`read_csv('results/*/h1_movement.csv')`) and compare them.

The SQL every project's queries share lives in
[`scripts/experiments_prelude.sql`](scripts/experiments_prelude.sql). A
project's `experiments.sql` names the tables its experiments write, then
loads it:

```sql
SET VARIABLE tables = ['h1_movement', 'h2_balance', 'h3_jumps', 'h3_fit'];
.read ../shared/scripts/experiments_prelude.sql
CREATE TEMP VIEW h1 AS FROM latest('h1_movement');
```

The prelude stops with "no results yet for …; run every experiment once:
make experiments (without RUN=)" if a named table has no run, and
otherwise defines `runs_of('<table>')` (every run's rows), `latest('<table>')`
(the rows of the newest run that wrote the table; run IDs start with a
UTC timestamp), the `runs` view, and `latest_runs` (each table's latest
run and its experiment, the table name up to the first `_`). It prints
the runs and the run each experiment's tables come from, so a `RUN=` run
of some experiments shows its new tables beside the others' last results.

From the repository root, `make experiments` runs every project that has
an `experiments/` package, and `make experiments-<project> RUN=H1` runs
one project's experiments whose test names match `RUN`.

## Using it from a project

Each project's `go.mod` requires the module and points it at this
directory, so no version is ever published:

```
require github.com/nickstrad/systems-learnings/shared v0.0.0

replace github.com/nickstrad/systems-learnings/shared => ../shared
```

Then import the packages, e.g. `github.com/nickstrad/systems-learnings/shared/errs`.

Keep these packages small and free of project logic. A change here
affects every project, so run `make test` from the repository root
afterwards, not just `make test-shared`.

## Tests

`make check` runs vet, formatting, and the tests with and without
assertions. There are no binaries, so `make e2e` and `make demo` do
nothing. See [`TESTS.md`](TESTS.md).
