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
if [[ "$RUNTIME_ROOT" != /* ]]; then RUNTIME_ROOT="$PWD/$RUNTIME_ROOT"; RUNTIME_ENV="$RUNTIME_ROOT/runtime.env"; fi

# The managed snapshot lives outside the checkout, so a source-only update can
# replace every container while keeping the same project and private state.
select_update_source() {
    local candidate='' revision='' seed active source selected
    candidate="$(git -C "$APP_DIR" rev-parse HEAD 2>/dev/null || true)"
    if [[ "$candidate" =~ ^[0-9a-f]{40}$ ]]; then
        revision="$candidate"
    elif [[ -f "$APP_DIR/RELEASE_SOURCE" ]]; then
        revision="$(tr -d '\r\n' < "$APP_DIR/RELEASE_SOURCE")"
    fi
    [[ "$revision" =~ ^[0-9a-f]{40}$ ]] || die 'The installation has no full source revision.'
    seed="$revision"
    UROBOROS_UPDATE_STATE_DIR="$RUNTIME_ROOT/updates"
    active="$UROBOROS_UPDATE_STATE_DIR/active.json"
    if [[ -f "$active" ]]; then
        local active_seed active_revision
        active_seed="$(awk -F '"' '$2 == "seed_revision" {print $4}' "$active")"
        active_revision="$(awk -F '"' '$2 == "revision" {print $4}' "$active")"
        source="$(awk -F '"' '$2 == "source" {print $4}' "$active")"
        if [[ "$active_seed" == "$seed" ]]; then
            [[ "$active_revision" =~ ^[0-9a-f]{40}$ && "$source" =~ ^releases/[0-9a-f]{40}/[a-z0-9][a-z0-9_.-]+$ && "$source" == "releases/$active_revision/"* ]] || die 'Invalid active update source pointer.'
            selected="$UROBOROS_UPDATE_STATE_DIR/$source"
            [[ -f "$selected/clean/compose.yml" ]] || die 'The last healthy update source is missing.'
            APP_DIR="$(CDPATH= cd -- "$selected" && pwd -P)"
            revision="$active_revision"
        fi
    fi
    CLEAN_DIR="$APP_DIR/clean"
    COMPOSE_FILE="$CLEAN_DIR/compose.yml"
    IMAGE_DIR="$CLEAN_DIR/runtime-images"
    UROBOROS_SOURCE_REVISION="$revision"
    UROBOROS_UPDATE_SEED="$seed"
    UROBOROS_UPDATE_INSTALL_DIR="$APP_DIR"
    UROBOROS_UPDATER_USER="$(id -u):$(id -g)"
    UROBOROS_DOCKER_GID="$(stat -c '%g' /var/run/docker.sock 2>/dev/null || printf '0')"
    export UROBOROS_SOURCE_REVISION UROBOROS_UPDATE_SEED UROBOROS_UPDATE_INSTALL_DIR UROBOROS_UPDATE_STATE_DIR UROBOROS_UPDATER_USER UROBOROS_DOCKER_GID
}

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

select_update_source
