# syntax=docker/dockerfile:1
#
# Runtime image: builds the pinned Katana source with a reviewed security-only
# dependency refresh, installs common tools, and drops in the prebuilt Uroboros
# Linux single binary.
# The binary is cross-compiled by CI's binaries job (pure Go, no QEMU) and placed, per target architecture, at
# dist/<TARGETARCH>/uroboros in the build context. This way, multi-arch builds only emulate the apt layer for arm64,
# no longer emulating the Next/Go compilation, which is much faster.
#
# When building the image manually in a local environment, prepare the binary yourself first:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/uroboros ./cmd/uroboros
#   docker build -t uroboros:local .

# Optional source-built application target used by the clean local bundle. The
# ordinary/default image target below still consumes CI's prebuilt dist binary.
FROM node:22-bookworm@sha256:63538dfe497cc29dfafdda25ada09b46db68a3f4cc205abf1f5d6bf0913f3384 AS source-web-build
ARG NEXT_PUBLIC_AUTH_DISABLED=0
ENV NEXT_PUBLIC_AUTH_DISABLED=${NEXT_PUBLIC_AUTH_DISABLED}
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build:static

FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS source-go-build
ARG UROBOROS_VERSION=dev
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/norma/go.mod third_party/norma/go.sum ./third_party/norma/
RUN go mod download
COPY . .
RUN legacy_token_pattern='(?i)(?<![A-Za-z0-9])(?:aut''umn|art''ex|art''umn|scope''sentry)(?![A-Za-z0-9])' \
    && agent_context_pattern='(?i)(?<![A-Za-z0-9])(?:cod''ex|claude[- ]code)(?![A-Za-z0-9])' \
    && ! LC_ALL=C.UTF-8 grep -RInP --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=.next --exclude-dir=out --exclude-dir=dist "${legacy_token_pattern}" . \
    && ! LC_ALL=C.UTF-8 grep -RInP --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=.next --exclude-dir=out --exclude-dir=dist "${agent_context_pattern}" . \
    && ! LC_ALL=C.UTF-8 grep -RInP --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=.next --exclude-dir=out --exclude-dir=dist '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}\x{3000}-\x{303F}\x{FE30}-\x{FE4F}\x{FF01}-\x{FF60}]' .
RUN rm -rf server/webui/dist && mkdir -p server/webui/dist
COPY --from=source-web-build /src/web/out/ server/webui/dist/
RUN go build -tags embedui -ldflags "-s -w -buildid= -X main.version=${UROBOROS_VERSION}" -o /out/uroboros ./cmd/uroboros

FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS katana-build
ARG KATANA_VERSION=1.8.0
ARG KATANA_COMMIT=35267ac5c8ff1db9694a319d0eb466ed97b0969f
ARG KATANA_PGX_VERSION=v5.9.2
ARG KATANA_COMPRESS_VERSION=v1.18.7
ARG KATANA_X_NET_VERSION=v0.60.0
WORKDIR /src
RUN git init . \
    && git remote add origin https://github.com/projectdiscovery/katana.git \
    && git fetch --depth=1 origin "${KATANA_COMMIT}" \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "${KATANA_COMMIT}" \
    && rm -rf .git \
    && go get \
      "github.com/jackc/pgx/v5@${KATANA_PGX_VERSION}" \
      "github.com/klauspost/compress@${KATANA_COMPRESS_VERSION}" \
      "golang.org/x/net@${KATANA_X_NET_VERSION}" \
    && go mod tidy \
    && test "$(go list -m -f '{{.Version}}' github.com/jackc/pgx/v5)" = "${KATANA_PGX_VERSION}" \
    && test "$(go list -m -f '{{.Version}}' github.com/klauspost/compress)" = "${KATANA_COMPRESS_VERSION}" \
    && test "$(go list -m -f '{{.Version}}' golang.org/x/net)" = "${KATANA_X_NET_VERSION}" \
    && CGO_ENABLED=0 GOFLAGS='-trimpath -buildvcs=false' go build \
      -ldflags '-s -w -buildid=' -o /out/katana ./cmd/katana \
    && /out/katana -version 2>&1 | grep -F "v${KATANA_VERSION}"

FROM python:3.12-slim-bookworm@sha256:6dd8b6dab6b3adc7812bab69eca5dbdc1a5cc690ad28306d234ffb4ef8ac9753 AS runtime-base
ARG KATANA_VERSION=1.8.0
ARG NPM_VERSION=12.2.1
ARG NPM_UNDICI_VERSION=6.29.1
ARG DIT_MODEL_REVISION=9ffc04b123e664bf8fee958baa31072726c1479d
ARG DIT_MODEL_SHA256=eba238b3093ff7aa4772ce17536bc313cb955428a6aa87dae41695a2dede6e59
# Common tools: ripgrep / curl / vim, plus a set of recon staples (add/remove as needed).
# Node is installed from NodeSource as supported 22.x LTS: bookworm's bundled
# apt nodejs is 18 and Node 20 reached EOL before this production refresh.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Pinned task-scoped crawler. Keep the reviewed v1.8.0 source while replacing
# only vulnerable dependency/runtime versions in the build stage above.
COPY --from=katana-build /out/katana /usr/local/bin/katana
COPY --from=katana-build /src/LICENSE.md /usr/share/licenses/katana/LICENSE.md
RUN katana -version 2>&1 | grep -F "v${KATANA_VERSION}"
ENV UROBOROS_KATANA_BIN=/usr/local/bin/katana
# Katana's knowledge-base classifier otherwise downloads a mutable model on its
# first run. Fetch the reviewed Hugging Face revision during the image build,
# verify it, and expose the read-only path to the task-scoped runner.
RUN curl -fsSL "https://huggingface.co/datasets/happyhackingspace/dit/resolve/${DIT_MODEL_REVISION}/model.json" -o /tmp/dit-model.json \
    && echo "${DIT_MODEL_SHA256}  /tmp/dit-model.json" | sha256sum -c - \
    && install -D -m 0444 /tmp/dit-model.json /usr/local/share/uroboros/dit/model.json \
    && rm -f /tmp/dit-model.json
ENV UROBOROS_KATANA_MODEL=/usr/local/share/uroboros/dit/model.json
# Pre-install Playwright MCP and CLI (globally) so there is no npx network download at runtime.
# @playwright/mcp: the browser MCP runs directly via `npx @playwright/mcp` (already installed globally, no -y/@latest needed).
# @playwright/cli: provides playwright-cli; after install, run --help to verify it is executable.
# Then install playwright (provides browser management); after install, use --with-deps to preinstall chromium and its system dependencies,
# so the MCP/CLI inside the container works on first launch without downloading a browser over the network.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npm install -g "npm@${NPM_VERSION}" \
    # npm 12.2.1 bundles undici 6.29.0. Keep npm pinned while applying the
    # reviewed, semver-compatible fix for CVE-2026-109869 to its private copy.
    && npm install -g "undici@${NPM_UNDICI_VERSION}" \
    && rm -rf /usr/lib/node_modules/npm/node_modules/undici \
    && cp -a /usr/lib/node_modules/undici /usr/lib/node_modules/npm/node_modules/undici \
    && rm -rf /usr/lib/node_modules/undici \
    && test "$(jq -r .version /usr/lib/node_modules/npm/node_modules/undici/package.json)" = "${NPM_UNDICI_VERSION}" \
    && npm install -g @playwright/mcp@0.0.83 @playwright/cli@0.1.22 playwright@1.64.1 \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && chromium_path="$(find "${PLAYWRIGHT_BROWSERS_PATH}" -type f -name chrome -path '*/chromium-*/*' | sort | tail -n 1)" \
    && test -n "${chromium_path}" -a -x "${chromium_path}" \
    && chmod -R a+rX "${PLAYWRIGHT_BROWSERS_PATH}" \
    && ln -s "${chromium_path}" /usr/local/bin/uroboros-chromium \
    && rm -rf /var/lib/apt/lists/*
# The final runtime is non-root/capability-dropped with no-new-privileges. Its
# user-namespace sandbox is unavailable, so the host reconciler adds the MCP's
# explicit --no-sandbox only for this image-owned Chromium path.
ENV UROBOROS_CHROME_BIN=/usr/local/bin/uroboros-chromium \
    UROBOROS_BROWSER_MCP_NO_SANDBOX=true

FROM runtime-base AS source-app
WORKDIR /app
COPY --from=source-go-build /out/uroboros /app/uroboros
COPY start.sh /app/start.sh
RUN chmod +x /app/uroboros /app/start.sh
COPY skills/ /app/skills/
RUN find /app/skills -mindepth 1 -type d -empty -delete \
    && mkdir -p /app/keystore \
    && ln -s keystore/jwt.key /app/jwt.key
VOLUME ["/app/data", "/app/keystore"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]

FROM runtime-base AS prebuilt-app
ARG TARGETARCH
WORKDIR /app
# The prebuilt binary for the matching architecture (dist/amd64/uroboros or dist/arm64/uroboros)
COPY dist/${TARGETARCH}/uroboros /app/uroboros
# Supervisor start script: after the process exits, it decides from the exit code whether to relaunch, and the UI's one-click update relies on it to complete the swap.
# It is also responsible for forwarding SIGTERM to uroboros —— docker stop sends the signal only to PID 1,
# and without forwarding, uroboros never receives it, cannot shut down gracefully, and after 10 seconds is hard-killed by SIGKILL.
COPY start.sh /app/start.sh
RUN chmod +x /app/uroboros /app/start.sh
COPY skills/ /app/skills/
# data/ (SQLite + jwt.key) persistence point
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]

# Preserve the historical no-target Docker build behaviour.
FROM prebuilt-app AS default
