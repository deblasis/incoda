#!/usr/bin/env bash
# Seed a rich incoda watch demo: holder + waiter on builds, exclusive gui-tests, closed legacy.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
STATE_DIR="${INCODA_DEMO_STATE:-$ROOT/docs/demo/.state}"
export INCODA_DIR="$STATE_DIR"

mkdir -p "$INCODA_DIR"

# Reset any prior demo tickets.
incoda force-release --queue builds --live >/dev/null 2>&1 || true
incoda force-release --queue gui-tests --live >/dev/null 2>&1 || true
incoda force-release --queue legacy --live >/dev/null 2>&1 || true

incoda config builds --slots 2 --description "CPU-heavy builds and link steps"
incoda config gui-tests --description "GUI and E2E runs that need the desktop"
incoda config legacy --close "retired: use builds or gui-tests"

# Holder uses one slot on a two-slot queue so a second job waits FIFO.
incoda run --queue builds --slots 1 --owner feature-x --reason "zig build (LLVM)" -- sleep 3600 &
holder_pid=$!
sleep 0.8

incoda run --queue builds --owner main --reason "dotnet test suite" -- sleep 3600 &
waiter_pid=$!
sleep 0.8

incoda run --queue gui-tests --exclusive --owner desktop --reason "playwright e2e" -- sleep 3600 &
gui_pid=$!
sleep 0.5

echo "seeded INCODA_DIR=$INCODA_DIR holder=$holder_pid waiter=$waiter_pid gui=$gui_pid"

cleanup() {
	for pid in "$holder_pid" "$waiter_pid" "$gui_pid"; do
		kill "$pid" 2>/dev/null || true
	done
}
trap cleanup EXIT INT TERM

# Hold the lane open until the recorder kills us.
wait
