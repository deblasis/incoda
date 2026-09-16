#!/usr/bin/env bash
# Re-record README demo GIFs from docs/img/demo-*.tape. Requires: vhs, incoda.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMG="$ROOT/docs/img"

command -v vhs >/dev/null || { echo "vhs not on PATH" >&2; exit 1; }
command -v incoda >/dev/null || { echo "incoda not on PATH" >&2; exit 1; }

optimize_gif() {
	local file=$1
	local colors=${2:-128}
	if ! command -v ffmpeg >/dev/null; then
		return
	fi
	mv "$file" "$file.raw"
	ffmpeg -y -loglevel error -i "$file.raw" \
		-vf "fps=10,scale=900:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=${colors}:stats_mode=diff[p];[s1][p]paletteuse=dither=bayer:bayer_scale=3" \
		"$file"
	rm -f "$file.raw"
}

record() {
	local tape=$1
	local colors=$2
	local name
	name="$(basename "$tape" .tape)"
	echo "recording $name..."
	rm -rf /tmp/incoda-cli-demo /tmp/incoda-watch-gif
	(cd "$IMG" && vhs "$(basename "$tape")")
	optimize_gif "$IMG/$name.gif" "$colors"
	echo "$IMG/$name.gif ($(du -h "$IMG/$name.gif" | cut -f1))"
}

record "$IMG/demo-queue.tape" 96
record "$IMG/demo-watch.tape" 192

cp "$IMG/demo-queue.gif" "$IMG/demo.gif"

echo "done: demo-queue.gif, demo-watch.gif, demo.gif (copy of queue)"
