#!/usr/bin/env bash
# =============================================================================
# Uroboros admin password reset script
#
# The login username is fixed as Uroboros; the password is stored as a bcrypt hash under the
# auth.password_hash key in the database settings table. After connecting to the database, this script uses
# pgcrypto to generate the bcrypt hash inside the database and write it back to that key—fully compatible with
# the backend login check (golang.org/x/crypto/bcrypt).
#
# Two deployment modes:
#   local (default) —— connect to the database directly with psql on the host. Connection info is resolved in this priority:
#                    command-line arguments > --dsn/$UROBOROS_PG_DSN > database.* in config.json
#   docker          —— run psql inside the postgres container via `docker compose exec` (or `docker exec`)
#                    (compose does not expose 5432 to the host by default, so it goes through the container).
#
# Usage examples:
#   ./reset-password.sh                          # local, auto-read config.json/env, enter the new password interactively
#   ./reset-password.sh -p 'NewPass!'            # local, provide the new password directly
#   ./reset-password.sh --dsn postgres://u:p@h:5432/uroboros
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d uroboros
#   ./reset-password.sh -m docker                # docker deployment (reads POSTGRES_* from .env)
#   ./reset-password.sh -m docker -c pg-container-name --exec docker
#
# Security: the new password is passed via an environment variable + psql \getenv (never entering the process argv), and :'var'
# auto-escapes it (preventing SQL injection); the database password is passed via PGPASSWORD, likewise never in argv.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker (empty = auto-detect)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # postgres service/container name in docker mode (default: postgres)
EXEC_KIND=""       # compose | docker (which exec to use in docker mode; empty = auto)
NEWPASS=""
ASSUME_YES=0

die() { echo "Error: $*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- Argument parsing ------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "unknown argument: $1 (-h for usage)" ;;
  esac
done

# ---- Read database.* from config.json (local mode only, and only when no connection is given explicitly) -----
# Prefer parsing with python3 (robust); fall back to grep when python3 is missing (config.json has tidy per-field entries).
read_config_json() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# Supports giving dsn directly, or per-field
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # Minimal fallback: grep key by key (values are strings or numbers)
    local k
    for k in host port user password dbname sslmode; do
      local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
  local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# ---- Auto-detect mode ------------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${UROBOROS_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "Deployment mode: $MODE"

# ---- Collect the new password ----------------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "Enter the new password (username is fixed as Uroboros): " NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "password must not be empty"
  read -r -s -p "Enter it again to confirm: " NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "the two entries do not match"
fi
[[ -n "$NEWPASS" ]] || die "password must not be empty"

# Hand the password to psql via an environment variable (read by \getenv, never entering argv/ps)
export UROBOROS_RESET_NEWPASS="$NEWPASS"

# Generate bcrypt inside the database and upsert; the password is auto-escaped via :'newpw'. CREATE EXTENSION is idempotent,
# and will error here if the database role lacks the create-extension privilege (see the failure branch of run below for the hint).
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw UROBOROS_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ---- Execute ---------------------------------------------------------------
if [[ "$MODE" == "local" ]]; then
  # Connection info priority: command line > --dsn/$UROBOROS_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${UROBOROS_PG_DSN:-}" ]] && DSN="$UROBOROS_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "Reading database config from $cfg"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "psql not found on this machine (install postgresql-client, or use -m docker instead)"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "missing database user (-U) or a valid config.json/DSN"
    [[ -n "$DBNAME" ]] || die "missing database name (-d) or a valid config.json/DSN"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "Target database: $target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Confirm resetting the Uroboros password in this database? [y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "cancelled"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "Write failed. If it reports a pgcrypto permission/missing error, use a role with the create-extension privilege, or run CREATE EXTENSION pgcrypto manually first."
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "docker not found"
  CONTAINER="${CONTAINER:-postgres}"

  # Choose the exec method: prefer docker compose exec (service name), otherwise docker exec (container name)
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # psql credentials inside the container: prefer the command line, then POSTGRES_* from .env, then fall back to the compose default (uroboros)
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-uroboros}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-uroboros}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "Target: psql -U $DUSER -d $DNAME inside container $CONTAINER (exec=$EXEC_KIND)"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Confirm resetting the Uroboros password in this container's database? [y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "cancelled"
  fi

  # -e with a name but no value → inherited from the current environment, so the password does not appear in the docker command argv.
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e UROBOROS_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e UROBOROS_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "Write failed. Check the container name (-c), the database account (POSTGRES_* in .env), and that the role has pgcrypto privileges."
  fi
fi

unset UROBOROS_RESET_NEWPASS
echo "✓ Uroboros admin password reset. Log in with username Uroboros + the new password (no service restart needed)."
