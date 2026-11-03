#!/usr/bin/env bash
# Uroboros cross-platform release builder.
#
# The default mode builds one target and embeds the already-exported frontend.
# `./build.sh --release` builds and packages all supported desktop/server targets.
#
# Environment variables:
#   UROBOROS_TARGET_OS=linux             One target OS in single-target mode.
#   UROBOROS_TARGET_ARCH=amd64           One target arch in single-target mode.
#   UROBOROS_TARGETS=linux/amd64,...     Comma-separated targets for multi-target mode.
#   UROBOROS_BUILD_VERSION=v0.3.3        Version embedded in the binary and archive name.
#   UROBOROS_OUTPUT=/path/to/uroboros       Explicit binary path in single-target mode.
#   UROBOROS_OUTPUT_DIR=dist             Directory for default binary paths.
#   UROBOROS_PACKAGE=1                   Create a zip archive for each target.
#   UROBOROS_PACKAGE_DIR=dist            Directory for release archives.
#   UROBOROS_COMPRESS=off                UPX mode: off, auto, or required.
#   UROBOROS_UPX_ARGS="--best --lzma"    Arguments passed to UPX.
#   UROBOROS_SKIP_FRONTEND=1             Reuse server/webui/dist (for CI artifact builds).
#   UROBOROS_SKIP_NPM_CI=1               Skip npm ci while rebuilding the frontend.
#   UROBOROS_GOSUMDB=sum.golang.org      Go checksum database.
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok() { printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage:
  ./build.sh                         Build for the current OS and architecture
  ./build.sh --target linux/amd64   Build a single specified target
  ./build.sh --release               Build and package all supported targets

Options:
  --release              Build the amd64/arm64 targets for Linux, macOS, and Windows and produce zips
  --target OS/ARCH       Set a single target, e.g. windows/amd64
  --upx                  Force UPX compression of the binary (may affect compatibility on some Linux environments)
  --no-compress          Skip UPX; use only Go linker stripping and zip compression
  --help                 Show help

The multi-target list can be overridden via UROBOROS_TARGETS, e.g.:
  UROBOROS_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
EOF
}

RELEASE_TARGETS_DEFAULT="linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64"
UROBOROS_RELEASE="${UROBOROS_RELEASE:-0}"
UROBOROS_COMPRESS="${UROBOROS_COMPRESS:-off}"
UROBOROS_PACKAGE="${UROBOROS_PACKAGE:-0}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --release)
      UROBOROS_RELEASE=1
      UROBOROS_PACKAGE=1
      shift
      ;;
    --target)
      [ "$#" -ge 2 ] || die "--target requires an OS/ARCH argument"
      target_arg="$2"
      case "$target_arg" in
        */*)
          UROBOROS_TARGET_OS="${target_arg%%/*}"
          UROBOROS_TARGET_ARCH="${target_arg##*/}"
          UROBOROS_TARGETS="$target_arg"
          ;;
        *) die "target must be OS/ARCH, e.g. linux/amd64" ;;
      esac
      shift 2
      ;;
    --no-compress)
      UROBOROS_COMPRESS=0
      shift
      ;;
    --upx)
      UROBOROS_COMPRESS=required
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1 (use --help for usage)" ;;
  esac
done

command -v go >/dev/null 2>&1 || die "Go not found (the project requires Go 1.26 or newer)"

UROBOROS_GOSUMDB="${UROBOROS_GOSUMDB:-sum.golang.org}"
if [ -z "${UROBOROS_BUILD_VERSION:-}" ]; then
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    UROBOROS_BUILD_VERSION="$(git describe --tags --always --dirty)"
  else
    UROBOROS_BUILD_VERSION="dev"
  fi
fi
# Release tags are commonly passed as v0.3.3; keep the binary version consistent.
UROBOROS_BUILD_VERSION="${UROBOROS_BUILD_VERSION#v}"
UROBOROS_OUTPUT_DIR="${UROBOROS_OUTPUT_DIR:-dist}"
UROBOROS_PACKAGE_DIR="${UROBOROS_PACKAGE_DIR:-$UROBOROS_OUTPUT_DIR}"
UROBOROS_UPX_ARGS="${UROBOROS_UPX_ARGS:---best --lzma}"

if [ "${UROBOROS_RELEASE}" = "1" ]; then
  UROBOROS_TARGETS="${UROBOROS_TARGETS:-$RELEASE_TARGETS_DEFAULT}"
else
  UROBOROS_TARGET_OS="${UROBOROS_TARGET_OS:-$(GOSUMDB="$UROBOROS_GOSUMDB" go env GOOS)}"
  UROBOROS_TARGET_ARCH="${UROBOROS_TARGET_ARCH:-$(GOSUMDB="$UROBOROS_GOSUMDB" go env GOARCH)}"
  UROBOROS_TARGETS="${UROBOROS_TARGETS:-${UROBOROS_TARGET_OS}/${UROBOROS_TARGET_ARCH}}"
fi

if [ "${UROBOROS_SKIP_FRONTEND:-0}" = "1" ]; then
  [ -d server/webui/dist ] || die "UROBOROS_SKIP_FRONTEND=1 but server/webui/dist does not exist"
else
  command -v npm >/dev/null 2>&1 || die "npm not found (the frontend static build requires Node.js/npm)"
  command -v rsync >/dev/null 2>&1 || die "rsync not found"
  info "Building frontend static assets"
  if [ "${UROBOROS_SKIP_NPM_CI:-0}" != "1" ]; then
    (cd web && npm ci)
  fi
  (cd web && npm run build:static)
  info "Syncing frontend assets to server/webui/dist"
  mkdir -p server/webui/dist
  rsync -a --delete web/out/ server/webui/dist/
fi

compress_binary() {
  binary="$1"
  goos="$2"
  case "$UROBOROS_COMPRESS" in
    0|off|false|none)
      info "Skipping UPX: $binary"
      return 0
      ;;
    auto|required|true|1) ;;
    *) die "UROBOROS_COMPRESS must be off, auto, or required" ;;
  esac

  if ! command -v upx >/dev/null 2>&1; then
    if [ "$UROBOROS_COMPRESS" = "required" ]; then
      die "UROBOROS_COMPRESS=required but upx not found"
    fi
    warn "upx not found; keeping the linker-stripped result: $binary"
    return 0
  fi

  before=$(wc -c < "$binary" | tr -d ' ')
  upx_args="$UROBOROS_UPX_ARGS"
  [ "$goos" = "darwin" ] && upx_args="$upx_args --force-macos"
  # shellcheck disable=SC2086
  if ! upx $upx_args -- "$binary"; then
    if [ "$UROBOROS_COMPRESS" = "required" ]; then
      die "UPX compression failed: $binary"
    fi
    warn "UPX does not support this target format; keeping the uncompressed binary: $binary"
    return 0
  fi
  after=$(wc -c < "$binary" | tr -d ' ')
  ok "UPX compression done: $binary (${before} -> ${after} bytes)"
}

package_binary() {
  binary="$1"
  goos="$2"
  goarch="$3"
  package_name="uroboros-${UROBOROS_BUILD_VERSION}-${goos}-${goarch}"
  package_root="${UROBOROS_PACKAGE_DIR}/${package_name}"
  archive="${UROBOROS_PACKAGE_DIR}/${package_name}.zip"

  command -v zip >/dev/null 2>&1 || die "packaging requires zip"
  rm -rf "$package_root" "$archive"
  mkdir -p "$package_root"
  cp "$binary" "$package_root/"
  # The supervisor start script is the real entry point: the one-click update in the UI relies on it to relaunch the process after it exits;
  # running the uroboros binary directly means it can never come back up after an update. Ship only the one matching the target OS.
  if [ "$goos" = "windows" ]; then
    cp start.bat "$package_root/"
  else
    cp start.sh "$package_root/"
    chmod +x "$package_root/start.sh"
  fi
  cp -R skills "$package_root/"
  cp config.example.json "$package_root/"
  if [ -f README.md ]; then cp README.md "$package_root/"; fi
  (cd "$UROBOROS_PACKAGE_DIR" && zip -q -r -9 "$(basename "$archive")" "$(basename "$package_root")")
  rm -rf "$package_root"
  ok "Release archive: $archive"
}

build_target() {
  target="$1"
  case "$target" in
    */*) ;;
    *) die "invalid target: $target (must be OS/ARCH)" ;;
  esac
  goos="${target%%/*}"
  goarch="${target##*/}"
  case "$goos" in
    linux|darwin|windows) ;;
    *) die "unsupported OS: $goos (supported: linux, darwin, windows)" ;;
  esac

  binary_name="uroboros"
  [ "$goos" = "windows" ] && binary_name="uroboros.exe"
  if [ -n "${UROBOROS_OUTPUT:-}" ] && [ "$UROBOROS_RELEASE" != "1" ]; then
    output="$UROBOROS_OUTPUT"
  else
    output="${UROBOROS_OUTPUT_DIR}/uroboros-${goos}-${goarch}/${binary_name}"
  fi
  mkdir -p "$(dirname "$output")"

  info "Building ${goos}/${goarch}, version ${UROBOROS_BUILD_VERSION}"
  GOSUMDB="$UROBOROS_GOSUMDB" \
  CGO_ENABLED=0 \
  GOOS="$goos" \
  GOARCH="$goarch" \
  go build \
    -tags embedui \
    -trimpath \
    -ldflags "-s -w -buildid= -X main.version=${UROBOROS_BUILD_VERSION}" \
    -o "$output" \
    ./cmd/uroboros

  compress_binary "$output" "$goos"
  if command -v file >/dev/null 2>&1; then file "$output"; fi
  if [ "$UROBOROS_PACKAGE" = "1" ]; then package_binary "$output" "$goos" "$goarch"; fi
  ok "Build done: $output"
}

write_checksums() {
  [ "$UROBOROS_PACKAGE" = "1" ] || return 0
  checksum_file="$UROBOROS_PACKAGE_DIR/SHA256SUMS"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$UROBOROS_PACKAGE_DIR" && for archive in *.zip; do sha256sum "$archive"; done > "$(basename "$checksum_file")")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$UROBOROS_PACKAGE_DIR" && for archive in *.zip; do shasum -a 256 "$archive"; done > "$(basename "$checksum_file")")
  else
    warn "sha256sum or shasum not found; skipping SHA256SUMS"
    return 0
  fi
  ok "Checksum file: $checksum_file"
}

mkdir -p "$UROBOROS_OUTPUT_DIR"
if [ "$UROBOROS_PACKAGE" = "1" ]; then mkdir -p "$UROBOROS_PACKAGE_DIR"; fi

old_ifs="$IFS"
IFS=','
read -r -a targets <<< "$UROBOROS_TARGETS"
IFS="$old_ifs"
[ "${#targets[@]}" -gt 0 ] || die "UROBOROS_TARGETS must not be empty"
for target in "${targets[@]}"; do
  target="${target//[[:space:]]/}"
  [ -n "$target" ] || continue
  build_target "$target"
done

if [ "$UROBOROS_PACKAGE" = "1" ]; then
  write_checksums
  info "Release packages generated in: $UROBOROS_PACKAGE_DIR"
fi
