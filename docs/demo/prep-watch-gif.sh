#!/usr/bin/env bash
# Seed state for demo-watch.{gif,mp4}.
set -euo pipefail

export INCODA_DIR="${INCODA_DIR:-/tmp/incoda-watch-gif/state}"
export CLICOLOR_FORCE=1
rm -rf "$INCODA_DIR"
mkdir -p "$INCODA_DIR"

incoda config builds --slots 2 --description "CPU-heavy builds and link steps"
incoda config gui-tests --description "GUI and E2E runs that need the desktop"
incoda config legacy --close "retired: use builds or gui-tests"

incoda run --queue builds --slots 1 --owner feature-x --reason "zig build (LLVM)" -- sleep 3600 &>/dev/null &
sleep 0.6
incoda run --queue builds --owner main --reason "dotnet test suite" -- sleep 3600 &>/dev/null &
sleep 0.6
incoda run --queue gui-tests --exclusive --owner desktop --reason "playwright e2e" -- sleep 3600 &>/dev/null &
sleep 1

disown -a 2>/dev/null || true
echo "seeded $INCODA_DIR"
