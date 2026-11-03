#!/usr/bin/env bash
set -euo pipefail

# Linux host entrypoint for the complete Uroboros 0.5 stack. The container's
# own supervisor remains start.sh; host lifecycle commands live under clean/.
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
exec bash "$ROOT_DIR/clean/start.sh" "$@"
