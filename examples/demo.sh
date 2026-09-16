#!/usr/bin/env bash
# 30-second incoda demo: one holder, one waiter, then watch.
set -euo pipefail

if ! command -v incoda >/dev/null 2>&1; then
	echo "incoda not found on PATH — install first (see README#install)" >&2
	exit 1
fi

export INCODA_QUEUE=demo

incoda run --reason "job A (holder)" -- sleep 15 &
sleep 0.5
incoda run --reason "job B (waiter)" -- sleep 3 &
sleep 0.5

echo "Opening incoda watch — q to quit"
exec incoda watch
