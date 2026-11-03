#!/usr/bin/env bash
# Runs one project's experiments and prints its DuckDB summary tables.
#
#   shared/scripts/experiments.sh <project-dir> [go test -run pattern]
#
# From the repository root, `make experiments-<project>` runs this, and
# `make experiments-<project> RUN=H1` passes a -run pattern.
#
# Each run writes to <project>/results/<UTC time>-<short commit>/:
# runs.csv, one CSV per table, any JSON-lines logs, and test.log (see
# shared/experiment). If duckdb is installed, the script then runs
# <project>/scripts/experiments.sql from the project directory, so the
# queries can read every run with globs like 'results/*/h1_movement.csv'.
set -euo pipefail

project=${1:?usage: experiments.sh <project-dir> [run pattern]}
pattern=${2:-}
cd "$project"
name=$(basename "$PWD")

commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
if [ "$commit" != unknown ] && [ -n "$(git status --porcelain -- . ../shared 2>/dev/null)" ]; then
	commit="$commit-dirty"
fi
run_id="$(date -u +%Y%m%dT%H%M%SZ)-$commit"
run_dir="results/$run_id"
mkdir -p "$run_dir"

export EXPERIMENT_RESULTS_DIR="$PWD/$run_dir"
export EXPERIMENT_RUN_ID="$run_id"
export EXPERIMENT_GIT_COMMIT="$commit"

echo "experiments: $name run $run_id"
go test -tags experiments -count=1 -v ${pattern:+-run "$pattern"} ./experiments 2>&1 | tee "$run_dir/test.log"
echo "experiments: results in $name/$run_dir"

if ! command -v duckdb >/dev/null; then
	echo "experiments: duckdb not found, so no summary tables."
	echo "  Install it (https://duckdb.org/docs/installation, or: brew install duckdb),"
	echo "  then run, from $name/: duckdb -c '.read scripts/experiments.sql'"
	exit 0
fi

# -no-agent: print ordinary tables even when an AI agent runs this script
# (DuckDB otherwise switches to a compact agent format). Older releases
# don't have the flag and never switch, so leave it out for them.
no_agent=
if duckdb -no-agent -c 'select 1' >/dev/null 2>&1; then
	no_agent=-no-agent
fi
duckdb $no_agent -c ".read scripts/experiments.sql"
