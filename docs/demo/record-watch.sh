#!/usr/bin/env bash
# Record incoda watch screenshots with ttyrig. Requires: incoda, ttyrig.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
STATE_DIR="$ROOT/docs/demo/.state"
OUT_DIR="$ROOT/docs/demo/shots"
RIG="$ROOT/docs/demo/incoda-watch.rig"
SEED="$ROOT/docs/demo/seed-watch-state.sh"

command -v incoda >/dev/null || { echo "incoda not on PATH" >&2; exit 1; }
command -v ttyrig >/dev/null || { echo "ttyrig not on PATH" >&2; exit 1; }

rm -rf "$OUT_DIR" "$STATE_DIR"
mkdir -p "$OUT_DIR"

export INCODA_DEMO_STATE="$STATE_DIR"
"$SEED" &
seed_pid=$!

cleanup() {
	kill "$seed_pid" 2>/dev/null || true
	wait "$seed_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Give enrollments time to show up in watch.
for _ in $(seq 1 30); do
	if incoda status --queue builds 2>/dev/null | grep -q "WAITING"; then
		break
	fi
	sleep 0.2
done

export INCODA_DIR="$STATE_DIR"
ttyrig run --size 120x40 --out "$OUT_DIR" --script "$RIG" -- incoda watch

# Promote the frames README uses.
install -m 644 "$OUT_DIR/07-overview-help.png" "$ROOT/docs/img/watch-overview.png"
install -m 644 "$OUT_DIR/03-queue.png" "$ROOT/docs/img/watch-queue.png"
install -m 644 "$OUT_DIR/04-prompt-empty.png" "$ROOT/docs/img/watch-kill-prompt.png"
install -m 644 "$OUT_DIR/06-after-kill.png" "$ROOT/docs/img/watch-after-kill.png"

echo "watch screenshots -> docs/img/watch-*.png"
