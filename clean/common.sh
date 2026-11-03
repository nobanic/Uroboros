#!/usr/bin/env bash

# Shared host-side paths and helpers for the Linux launchers.
CLEAN_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
APP_DIR="$(CDPATH= cd -- "$CLEAN_DIR/.." && pwd -P)"
COMPOSE_FILE="$CLEAN_DIR/compose.yml"
IMAGE_DIR="$CLEAN_DIR/runtime-images"

if [[ -n "${ASTRA_RUNTIME_DIR:-}" ]]; then
    RUNTIME_ROOT="$ASTRA_RUNTIME_DIR"
else
    : "${HOME:?HOME must be set, or set ASTRA_RUNTIME_DIR explicitly}"
    RUNTIME_ROOT="${XDG_STATE_HOME:-$HOME/.local/state}/astra"
fi
RUNTIME_ENV="$RUNTIME_ROOT/runtime.env"

die() {
    printf 'Error: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "Required command is not available: $1"
}

compose() {
    docker compose --env-file "$RUNTIME_ENV" -f "$COMPOSE_FILE" "$@"
}

runtime_value() {
    local key="$1"
    awk -F= -v wanted="$key" '
        $1 == wanted {
            sub(/^[^=]*=/, "")
            sub(/\r$/, "")
            print
            exit
        }
    ' "$RUNTIME_ENV"
}

runtime_port() {
    local port
    port="$(runtime_value UROBOROS_PORT)"
    [[ "$port" =~ ^[0-9]+$ ]] || die "Invalid UROBOROS_PORT in $RUNTIME_ENV"
    (( port >= 1 && port <= 65535 )) || die "UROBOROS_PORT is outside 1-65535"
    printf '%s\n' "$port"
}
