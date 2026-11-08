#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
( cd sidecar && python3 sidecar.py ) &
SIDE=$!
trap "kill $SIDE 2>/dev/null" EXIT
# wait for sidecar socket (up to ~15s) instead of a fixed guess
for _ in $(seq 1 150); do [ -S /tmp/2t5-deck-plus.sock ] && break; sleep 0.1; done
./2t5-deck-plus
