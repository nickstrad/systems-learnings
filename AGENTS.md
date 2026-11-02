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

## Root Makefile

The root Makefile discovers projects from top-level `go.mod` files.
Give each project a Makefile with `check`, `e2e`, and `demo` targets:
`check` runs its standard checks and tests, `e2e` runs its end-to-end
suite, and `demo` runs its walkthrough and cleans up any processes it
starts. Keep `make test-<project>` and `make demo-<project>` working as
projects are added, and document them in the root README.

`AGENTS.md` is the source of these instructions. `CLAUDE.md` is a relative
symlink to it; preserve the symlink and edit this file to change the rules.
