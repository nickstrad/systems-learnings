#!/usr/bin/env bash
# Manual walkthrough: start 4 nodes, grow the cluster, then shrink it.
# Usage: ./scripts/demo.sh   (from the jumphash directory)
set -euo pipefail

BIN=$(mktemp -d)
WORK=$(mktemp -d)
CLUSTER="$WORK/cluster.json"
PIDS=()

cleanup() { kill "${PIDS[@]}" 2>/dev/null || true; wait 2>/dev/null || true; rm -rf "$BIN" "$WORK"; }
trap cleanup EXIT

go build -o "$BIN/kvnode" ./cmd/kvnode
go build -o "$BIN/kvctl" ./cmd/kvctl
kvctl() { "$BIN/kvctl" -cluster "$CLUSTER" "$@"; }
step() { printf '\n\033[1m$ kvctl %s\033[0m\n' "$*"; kvctl "$@"; }

for port in 8081 8082 8083 8084; do
  "$BIN/kvnode" -addr "127.0.0.1:$port" -quiet &
  PIDS+=($!)
done
sleep 0.5

step add-node localhost:8081
step fill 1000
step stats
step add-node localhost:8082
step add-node localhost:8083
step add-node localhost:8084
step stats
step put user:42 '{"name":"ada"}'
step where user:42
step remove-node localhost:8082
step stats
step get user:42
printf '\nmembership file:\n'; cat "$CLUSTER"
