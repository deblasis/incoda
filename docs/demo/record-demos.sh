#!/usr/bin/env bash
# Re-record README demo GIFs/MP4s. Requires: vhs, incoda.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMG="$ROOT/docs/img"
DEMO="$ROOT/docs/demo"

command -v vhs >/dev/null || { echo "vhs not on PATH" >&2; exit 1; }
command -v incoda >/dev/null || { echo "incoda not on PATH" >&2; exit 1; }

record() {
	local prep=$1 tape=$2 state_dir=$3
	local name
	name="$(basename "$tape" .tape)"
	echo "recording $name..."
	export INCODA_DIR="$state_dir"
	"$prep"
	(cd "$IMG" && vhs "$(basename "$tape")")
	for ext in gif mp4; do
		if [ -f "$IMG/$name.$ext" ]; then
			echo "  $name.$ext ($(du -h "$IMG/$name.$ext" | cut -f1))"
		fi
	done
}

record "$DEMO/prep-queue-gif.sh" "$IMG/demo-queue.tape" "/tmp/incoda-cli-demo/state"
record "$DEMO/prep-watch-gif.sh" "$IMG/demo-watch.tape" "/tmp/incoda-watch-gif/state"

cp "$IMG/demo-queue.gif" "$IMG/demo.gif"

echo "done: demo-queue.{gif,mp4} demo-watch.{gif,mp4}"
