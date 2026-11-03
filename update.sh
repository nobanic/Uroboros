#!/usr/bin/env bash
# Uroboros update script: ① Docker update (pull a new image and rebuild)  ② local build update (rebuild the binary)
# Counterpart to install.sh: install handles the first-time setup, update handles upgrading to a new version.
# DB migrations need no manual step—uroboros idempotently re-runs schema.sql on every startup (including ADD COLUMN/CREATE
# INDEX IF NOT EXISTS), so "restart means migrate". Data (the pgdata volume, ./data, ./skills) is unaffected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: sync the repo to the latest code (compose/scripts/local build sources all update through this) ───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "not a git working copy; skipping git pull"; return; }
  [ "$(ask 'Pull the latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or a diverged branch)—resolve it manually and retry; using the current code for now"
  fi
}

# ── ① Docker update ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose not found; run ./install.sh to install and deploy first"
  [ -f .env ] || die ".env not found; run ./install.sh to complete the first-time deployment first"

  # Optional: upgrade to a specific version tag (leave blank to keep UROBOROS_TAG from .env, which defaults to latest)
  local tag; tag="$(ask 'Target image tag (Enter to keep .env / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^UROBOROS_TAG=' .env; then
      sed -i.bak "s|^UROBOROS_TAG=.*|UROBOROS_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nUROBOROS_TAG=%s\n' "$tag" >> .env
    fi
    ok "Set UROBOROS_TAG to ${tag}"
  fi

  # Only touch uroboros: postgres is pinned to 16-alpine and does not need to be upgraded alongside (pulling it just wastes bandwidth,
  # and a major version change carries compatibility risk). uroboros declares depends_on postgres, so when
  # doing up with the service name, pg is started automatically if it is not running, and left as-is (not rebuilt) if it already is.
  info "Pulling the new image (uroboros only)…"
  docker compose pull uroboros
  info "Rebuilding and starting (uroboros auto-migrates the schema on restart)…"
  docker compose up -d uroboros
  ok "Update complete → http://localhost:8787"
  info "View logs: docker compose logs -f uroboros"
  info "Clean up old images (optional): docker image prune -f"
}

# ── ② Local build update ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go (>=1.26) not found: https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found—for a first-time deployment use ./install.sh instead"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "Rebuilding the frontend static output…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Recompiling the embedded single binary…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o uroboros ./cmd/uroboros
  else
    warn "npm not found: compiling the backend **without an embedded frontend** (the frontend must be run separately via npm run dev)"
    CGO_ENABLED=0 go build -o uroboros ./cmd/uroboros
  fi
  ok "Build complete → ./uroboros"
  warn "Restart the running uroboros process to take effect (the schema is auto-migrated on restart)"
}

echo "=============================="
echo "  Uroboros update"
echo "  1) Docker update (pull a new image and rebuild)"
echo "  2) Local update (recompile with go)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "invalid choice" ;;
esac
