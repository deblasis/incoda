#!/usr/bin/env bash
# Re-record all README demo GIFs. See record-demos.sh.
exec "$(cd "$(dirname "$0")" && pwd)/record-demos.sh"
