#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=common.sh
source "$SCRIPT_DIR/common.sh"

NO_BUILD=0
NO_WAIT=0
PREFLIGHT_ONLY=0

usage() {
    cat <<'EOF'
Usage: start.sh [--no-build] [--no-wait] [--preflight-only]

  --no-build        Reuse existing locally built application images.
  --no-wait         Do not wait for the one-shot bootstrap container.
  --preflight-only  Verify source, Docker, architecture and B0.5 images only.
EOF
}

while (( $# > 0 )); do
    case "$1" in
        --no-build) NO_BUILD=1 ;;
        --no-wait) NO_WAIT=1 ;;
        --preflight-only) PREFLIGHT_ONLY=1 ;;
        -h|--help) usage; exit 0 ;;
        *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

random_hex() {
    local byte_count="$1" value
    value="$(od -An -N "$byte_count" -tx1 /dev/urandom | tr -d ' \n')"
    (( ${#value} == byte_count * 2 )) || die 'Failed to generate a secure random value.'
    printf '%s\n' "$value"
}

source_revision() {
    local revision='local-clean-0.5' candidate=''
    if command -v git >/dev/null 2>&1; then
        candidate="$(git -C "$APP_DIR" rev-parse HEAD 2>/dev/null || true)"
    fi
    if [[ "$candidate" =~ ^[0-9a-f]{40}$ ]]; then
        printf '%s\n' "$candidate"
        return
    fi
    if [[ -f "$APP_DIR/RELEASE_SOURCE" ]]; then
        candidate="$(tr -d '\r\n' < "$APP_DIR/RELEASE_SOURCE")"
        if [[ "$candidate" =~ ^[0-9a-f]{40}$ ]]; then
            revision="$candidate"
        fi
    fi
    printf '%s\n' "$revision"
}

update_source_revision() {
    local revision="$1" temporary
    temporary="$(mktemp "$RUNTIME_ROOT/.runtime.env.XXXXXX")"
    awk -v revision="$revision" '
        BEGIN { updated = 0 }
        {
            sub(/\r$/, "")
            if (!updated && index($0, "UROBOROS_SOURCE_REVISION=") == 1) {
                print "UROBOROS_SOURCE_REVISION=" revision
                updated = 1
            } else {
                print
            }
        }
        END {
            if (!updated) {
                print "UROBOROS_SOURCE_REVISION=" revision
            }
        }
    ' "$RUNTIME_ENV" >"$temporary"
    chmod 600 "$temporary"
    mv -f -- "$temporary" "$RUNTIME_ENV"
}

archive_sha256() {
    local archive="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -- "$archive" | awk '{ print tolower($1) }'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 -- "$archive" | awk '{ print tolower($1) }'
    else
        die 'sha256sum or shasum is required to verify offline image archives.'
    fi
}

ensure_image() {
    local reference="$1" archive_name="$2" expected_sha256="$3"
    if docker image inspect "$reference" >/dev/null 2>&1; then
        return
    fi

    local archive="$IMAGE_DIR/$archive_name" actual_sha256
    [[ -f "$archive" ]] || die "Required accepted binary runtime image is missing: $reference. Expected offline archive: $archive"
    actual_sha256="$(archive_sha256 "$archive")"
    [[ "$actual_sha256" == "$expected_sha256" ]] || die "Offline image archive checksum mismatch: $archive"

    printf 'Loading accepted binary runtime image from %s ...\n' "$archive"
    docker image load --input "$archive"
    docker image inspect "$reference" >/dev/null 2>&1 || die "The archive did not provide the exact required image: $reference"
}

json_true() {
    local json="$1" key="$2"
    printf '%s' "$json" | grep -Eq "\"${key}\"[[:space:]]*:[[:space:]]*true"
}

bash "$CLEAN_DIR/verify-language.sh" --root "$APP_DIR"

require_command docker
require_command awk
require_command curl
require_command grep
require_command od
require_command tr

docker version >/dev/null 2>&1 || die 'Docker Engine is not available.'
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is not available.'

engine="$(docker info --format '{{.OSType}}|{{.Architecture}}' 2>/dev/null || true)"
case "$engine" in
    linux\|amd64|linux\|x86_64) ;;
    linux\|*) die "The clean B0.5 runtime currently requires an amd64/x86_64 Docker engine; detected: $engine" ;;
    *) die "The clean bundle requires a Linux-container Docker engine; detected: ${engine:-unknown}" ;;
esac

mkdir -p -- "$RUNTIME_ROOT"
chmod 700 "$RUNTIME_ROOT"
revision="$(source_revision)"
if [[ ! -f "$RUNTIME_ENV" ]]; then
    umask 077
    {
        printf '%s\n' '# Generated locally. Contains only internal Docker service credentials; no LLM/provider API key.'
        printf 'POSTGRES_PASSWORD=%s\n' "$(random_hex 32)"
        printf 'ARSENAL_TOKEN=%s\n' "$(random_hex 32)"
        printf 'API_LAB_TOKEN=%s\n' "$(random_hex 32)"
        printf '%s\n' 'UROBOROS_PORT=8805'
        printf 'UROBOROS_SOURCE_REVISION=%s\n' "$revision"
    } >"$RUNTIME_ENV"
    chmod 600 "$RUNTIME_ENV"
    printf 'Created private runtime state: %s\n' "$RUNTIME_ENV"
else
    update_source_revision "$revision"
fi

ensure_image \
    'uroboros-b0/binary-broker@sha256:0fc4e1bfe1c398f2371aa7b0f18c8b6a2fdbf3e3544b6ea3c3576fc734f58173' \
    'binary-broker-b05.tar' \
    'f12520c790e04e3cddb0766ab8cc93213563c78c61a28f5874b61eeb6513ef6f'
ensure_image \
    'uroboros-b0/binary-worker@sha256:142bdcf03aa22e6a27fead6dedf89391155a46210375f007871bd09726315514' \
    'binary-worker-b05.tar' \
    '5cc2a489c15b2a85f8a65acaede4fc087040539840f330aa6617191dbb060123'

if (( PREFLIGHT_ONLY )); then
    printf '%s\n' 'Cold-start preflight passed; exact B0.5 runtime images are available.'
    exit 0
fi

compose config --quiet
up_args=(up -d)
if (( ! NO_BUILD )); then
    up_args+=(--build)
fi
compose "${up_args[@]}"

if (( ! NO_WAIT )); then
    printf '%s\n' 'Waiting for tool bootstrap ...'
    bootstrap_ready=0
    for (( attempt = 0; attempt < 180; attempt++ )); do
        container_id="$(compose ps -a -q bootstrap)"
        if [[ -n "$container_id" ]]; then
            state="$(docker inspect --format '{{.State.Status}}|{{.State.ExitCode}}' "$container_id")"
            if [[ "$state" == 'exited|0' ]]; then
                bootstrap_ready=1
                break
            fi
            if [[ "$state" == exited\|* ]]; then
                compose logs --no-color bootstrap || true
                die "Tool bootstrap failed ($state)."
            fi
        fi
        sleep 2
    done
    if (( ! bootstrap_ready )); then
        compose logs --no-color bootstrap || true
        die 'Tool bootstrap did not finish within six minutes.'
    fi
fi

port="$(runtime_port)"
base_url="http://127.0.0.1:${port}"
control_ready=0
health_json=''
auth_json=''
for (( attempt = 0; attempt < 120; attempt++ )); do
    health_json="$(curl -fsS --max-time 5 "$base_url/api/health" 2>/dev/null || true)"
    auth_json="$(curl -fsS --max-time 5 "$base_url/api/auth/status" 2>/dev/null || true)"
    if json_true "$health_json" ok && ! json_true "$health_json" auth_disabled && ! json_true "$auth_json" disabled; then
        control_ready=1
        break
    fi
    sleep 2
done

if (( ! control_ready )); then
    compose logs --no-color uroboros || true
    die 'The control plane did not confirm auth-enabled mode within four minutes.'
fi

if json_true "$auth_json" initialized; then
    entry_url="$base_url/login/"
    auth_message='Authentication is enabled. Sign in with the local operator account.'
else
    entry_url="$base_url/setup/"
    auth_message='Authentication is enabled. Create the first local operator password on the setup page.'
fi

printf '\nUroboros 0.5 is ready: %s\n' "$entry_url"
printf '%s\n' "$auth_message"
printf '%s\n' 'No LLM/provider API key is configured.'
