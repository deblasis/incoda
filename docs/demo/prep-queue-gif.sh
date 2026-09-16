#!/usr/bin/env bash
# Seed state for demo-queue.{gif,mp4}: 2 holders + 1 waiter on a 2-slot queue.
set -euo pipefail

export INCODA_DIR="${INCODA_DIR:-/tmp/incoda-cli-demo/state}"
export CLICOLOR_FORCE=1
rm -rf "$INCODA_DIR"
mkdir -p "$INCODA_DIR"

incoda config builds --slots 2 --description "CPU-heavy builds"

incoda run --queue builds --owner feature-x --reason "zig build (LLVM)" -- sleep 3600 &>/dev/null &
holder_pid=$!
incoda run --queue builds --owner main --reason "cargo build" -- sleep 3600 &>/dev/null &
sleep 0.8
incoda run --queue builds --owner agents --reason "dotnet test suite" -- sleep 3600 &>/dev/null &
sleep 1

echo "$holder_pid" >"$INCODA_DIR/.holder.pid"
disown -a 2>/dev/null || true
echo "seeded $INCODA_DIR holder=$holder_pid"
