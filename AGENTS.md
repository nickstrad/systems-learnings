# Repository instructions

This repository contains Go systems projects exploring the ten papers in
`docs/papers/`. Each paper's project lives in its own Go module.

## Keep project documentation current

When a paper project becomes runnable or is completed, update the root
`README.md` in the same change:

- Add or update its entry under **Run the projects**. Use the `jumphash`
  section as the example: include prerequisites, the working directory,
  build and run commands, useful interactions, expected results, and how
  to stop or reset the project.
- Check the commands against the implementation and verify the walkthrough.
- Update the paper table's project link and status, plus any progress or
  remaining-work notes. Mark a paper complete only when its stated work
  is finished.
- Link to the project's own README for detailed documentation, and keep
  that README consistent with the root instructions.

When a project's commands, flags, setup, or behavior change, update its
existing usage section even if the paper project is still in progress.

## Keep each project's TESTS.md current

Every project has a `TESTS.md` at its root (for example
`jumphash/TESTS.md`) that lists all of its test files, grouped by the
testing categories in `docs/testing.md`. Use `jumphash/TESTS.md` as the
template.

- When a project gets its first test, create its `TESTS.md` and link it
  from the project's README.
- When you add, rename, delete, or change the purpose of a test file
  (including fuzz targets, fakes, contract suites, and saved fuzz
  corpora), update that project's `TESTS.md` in the same change.
- Put each file under its main category, note other roles in
  parentheses, and order files from the domain outward to the real
  binaries.

## Keep each project's walkthroughs current

Every project has two walkthroughs in `docs/walkthroughs/<project>/`:

- `code.md`: the non-test code in suggested reading order, from the
  domain outward to the binaries, with what to read at each stop and
  how it connects to the paper.
- `tests.md`: every test file, grouped by the categories in
  `docs/testing.md`, saying what each category buys us, with a one-line
  command to run each file or test from the repository root.

When a change adds, moves, renames, or removes code or tests, or changes
behaviour a walkthrough describes, update the walkthrough in the same
change. Check that every file reference, excerpt, and one-line command
still matches the code; run the one-liners. When a project gets its first
runnable code, create its walkthroughs and link them from its README.

### Diagrams

Walkthroughs use C4 architecture diagrams (context, container,
component, and the test pyramid mapped onto components) where they help
a reader. Each diagram exists in two forms, next to each other:

- **GitHub:** a fenced `mermaid` block in C4 syntax (`C4Context`,
  `C4Container`, `C4Component`), which GitHub renders.
- **Terminal:** Unicode text drawn with the `draw-visual` skill (in
  `~/.claude/skills/draw-visual`, sourced from the `personal-config`
  repo). The C4 syntax doesn't render as text, so its source is an
  equivalent Mermaid `graph` in `docs/walkthroughs/<project>/diagrams/`,
  embedded with a `<!-- draw-visual: diagrams/... -->` marker and
  regenerated with `render.py --update`. Never edit the rendered text by
  hand.

When the architecture changes, update both forms of every affected
diagram in the same change, re-render, and check the width (80 columns).

## Experiments

A project measures its paper's claims in an `experiments/` package: Go
tests behind the `experiments` build tag that use `shared/experiment`
for tables, logs, and parallel sweeps, and assert loose shapes so they
double as regression checks. Results must be deterministic (counts, not
timings) where possible. Each project also has `scripts/experiments.sql`,
DuckDB queries over `results/*/` that print the tables for the latest run
and compare runs; it lists its tables and loads
`shared/scripts/experiments_prelude.sql` for the shared guard, `runs_of`
and `latest` macros, and run listings. `shared/scripts/experiments.sh` runs both; the
project's Makefile `experiments` target calls it, and the root Makefile
finds the project automatically (`make experiments`,
`make experiments-<project> RUN=<pattern>`). Document each experiment's
claim, command, output files, and results in the project's README and
walkthroughs, and list the files under a "Measurement (experiments)"
group in `TESTS.md`.

## Root Makefile

The root Makefile discovers projects from top-level `go.mod` files.
Give each project a Makefile with `check`, `e2e`, and `demo` targets:
`check` runs its standard checks and tests, `e2e` runs its end-to-end
suite, and `demo` runs its walkthrough and cleans up any processes it
starts. Keep `make test-<project>` and `make demo-<project>` working as
projects are added, and document them in the root README.

`AGENTS.md` is the source of these instructions. `CLAUDE.md` is a relative
symlink to it; preserve the symlink and edit this file to change the rules.
