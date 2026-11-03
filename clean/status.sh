#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=common.sh
source "$SCRIPT_DIR/common.sh"

if [[ ! -f "$RUNTIME_ENV" ]]; then
    printf '%s\n' 'Uroboros 0.5 has not been initialized yet.'
    exit 0
fi

require_command docker
compose ps

if ! command -v curl >/dev/null 2>&1; then
    printf '%s\n' 'Warning: curl is unavailable; HTTP health was not checked.' >&2
    exit 0
fi

port="$(runtime_port)"
if ! curl -fsS --max-time 5 "http://127.0.0.1:${port}/api/health"; then
    printf '\n%s\n' 'Warning: the local HTTP endpoint is not responding.' >&2
else
    printf '\n'
fi
