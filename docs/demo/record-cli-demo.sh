#!/usr/bin/env bash
# Re-record docs/img/demo.gif from docs/img/demo.tape. Requires: vhs, incoda on PATH.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TAPE="$ROOT/docs/img/demo.tape"

command -v vhs >/dev/null || { echo "vhs not on PATH" >&2; exit 1; }
command -v incoda >/dev/null || { echo "incoda not on PATH" >&2; exit 1; }

rm -rf /tmp/incoda-cli-demo
cd "$ROOT/docs/img"
vhs demo.tape

# vhs writes beside the tape file.
if [ -f demo.gif ]; then
	ls -la demo.gif
	echo "CLI demo -> docs/img/demo.gif ($(du -h demo.gif | cut -f1))"
else
	echo "vhs did not produce demo.gif" >&2
	exit 1
fi
