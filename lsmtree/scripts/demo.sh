#!/usr/bin/env bash
# Narrated walkthrough: fill a store until it has several levels, watch the
# rolling merge, then crash a synced load with kill -9 and recover.
# Usage: ./scripts/demo.sh   (from the lsmtree directory)
set -euo pipefail

BIN=$(mktemp -d)
WORK=$(mktemp -d)
LOADER=""

cleanup() {
  if [[ -n "$LOADER" ]]; then kill -9 "$LOADER" 2>/dev/null || true; wait "$LOADER" 2>/dev/null || true; fi
  rm -rf "$BIN" "$WORK"
}
trap cleanup EXIT

go build -o "$BIN/lsmctl" ./cmd/lsmctl

# Small sizes, so a few thousand keys build several levels: an 8 KiB
# memtable, 16 KiB tables, L1 = 32 KiB, and each level 4x the one above.
LSMCTL=("$BIN/lsmctl" -dir "$WORK/data" -memtable 8k -table 16k -base 32k -ratio 4)
lsmctl() { "${LSMCTL[@]}" "$@"; }
say() { echo; printf '\033[1;34m# %s\033[0m\n' "$@"; }  # one line per argument
step() { printf '\033[1m$ lsmctl %s\033[0m\n' "$*"; lsmctl "$@"; }

say "Each command is its own process: it opens the store, runs, and closes it."
step put greeting "hello, LSM"
step get greeting

say "Load 250 keys. Each time the memtable fills, it is flushed to a sorted table in L0."
step -v load 250
step dump

say "Load 2,000 more. L0 reaching 4 tables triggers a merge into L1, and each level that" \
  "outgrows its target merges one table into the next level down (the rolling merge)."
step load 2000 -seed 2
step dump

say "Overwrite the same 2,000 keys. Watch each level's cursor move on: the next merge" \
  "starts just after the last key the previous one took ('->' marks that table)."
step load 2000 -seed 3
step dump

say "Sizes against targets, and the cost of keeping the tree in shape."
step stats

say "Deletes write a tombstone; the key is gone at once, and merges drop it later."
step del greeting
step get greeting || true
echo "(the log ID finds the full error in lsmctl.log)"

say "Crash test: with -sync, load fsyncs every write and prints each key once it is durable." \
  "Start one in the background and kill -9 it after about a second."
printf '\033[1m$ lsmctl -sync load 100000 -seed 4 > acks.txt &\033[0m\n'
# Run the binary itself in the background (not the lsmctl function, which
# would run in a subshell), so $! is the process to kill.
"${LSMCTL[@]}" -sync load 100000 -seed 4 > "$WORK/acks.txt" &
LOADER=$!
sleep 1
printf '\033[1m$ kill -9 %s\033[0m\n' "$LOADER"
kill -9 "$LOADER"
wait "$LOADER" 2>/dev/null || true
LOADER=""
ACKED=$(grep -c '^key-' "$WORK/acks.txt" || true)
LAST=$(grep '^key-' "$WORK/acks.txt" | tail -n 1)
echo "acknowledged before the kill: $ACKED keys (last: $LAST)"

say "Reopen: the store replays its write-ahead log, and every acknowledged key is there."
step verify
step get "$LAST"
lsmctl scan > "$WORK/scan.txt"
awk -F'\t' -v seed=4 '
  NR == FNR { have[$1] = $2; next }
  index(have[$1], $1 "@" seed) == 1 { ok++ }
  END { printf "%d of %d acknowledged keys present with their seed-4 values\n", ok, FNR; exit ok != FNR }
' "$WORK/scan.txt" "$WORK/acks.txt"
