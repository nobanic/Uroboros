#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=common.sh
source "$SCRIPT_DIR/common.sh"

[[ -f "$RUNTIME_ENV" ]] || die "Runtime environment does not exist: $RUNTIME_ENV"
require_command docker

compose down
printf '%s\n' 'Uroboros 0.5 stopped. Its named volumes were preserved.'
