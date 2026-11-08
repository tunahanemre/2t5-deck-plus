#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go build -o 2t5-deck-plus ./cmd/2t5-deck-plus
codesign --force --sign - ./2t5-deck-plus   # stable identity for Accessibility/Input-Monitoring
echo "built + signed ./2t5-deck-plus"
