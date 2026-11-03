# syntax=docker/dockerfile:1
#
# Hardened, build-from-source image for running Uroboros inside a sealed sandbox.
#
# We deliberately do NOT pull the maintainer's prebuilt Docker Hub image
# (nobanic/uroboros). Instead we compile the frontend and the Go binary ourselves
# from the checked-out source, so nothing but code we can read ends up running.
#
# The final runtime stages use Google "distroless" (static-debian12:nonroot):
#   - no shell, no package manager, no interactive tooling to abuse
#   - runs as a non-root user (uid 65532) by default
#   - minimal CVE / attack surface
# A fully static (CGO_ENABLED=0) Go binary needs nothing else.
#
# Two final targets are produced from this one file:
#   --target app      -> the Uroboros server
#   --target gateway  -> a tiny pure-Go TCP forwarder used to expose the UI
#
# Build-time network access is used here (npm/go module downloads). The RUNTIME
# containers get zero network egress — that is enforced by docker-compose networks.

# ---------------------------------------------------------------------------
# Stage 1: build the Next.js frontend as a static export (-> web/out)
# ---------------------------------------------------------------------------
FROM node:22-bookworm AS web
WORKDIR /src/web
# Install deps first for layer caching.
COPY web/package.json web/package-lock.json ./
RUN npm ci
# Then the rest of the frontend source.
COPY web/ ./
# next.config.mjs sets output:"export"; build:static produces ./out
RUN npm run build:static

# ---------------------------------------------------------------------------
# Stage 2: build the Go binaries (uroboros server + tcpfwd gateway), fully static
# ---------------------------------------------------------------------------
FROM golang:1.26-bookworm AS gobuild
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src
# Module graph first (cache).
COPY go.mod go.sum ./
COPY third_party/norma/go.mod third_party/norma/go.sum ./third_party/norma/
RUN go mod download
# Full source.
COPY . .
# Drop any stale embedded UI and replace with the freshly built static export,
# which is what `-tags embedui` bakes into the binary.
RUN rm -rf server/webui/dist && mkdir -p server/webui/dist
COPY --from=web /src/web/out/ server/webui/dist/
RUN go build -tags embedui -ldflags "-s -w -buildid=" -o /out/uroboros ./cmd/uroboros

# Tiny stdlib-only L4 TCP forwarder, built as its own throwaway module so it
# never touches the uroboros module graph. Used only to expose the UI to the host.
RUN mkdir -p /fwd && cat > /fwd/main.go <<'GO' && cd /fwd && go mod init tcpfwd >/dev/null 2>&1 && go build -ldflags "-s -w" -o /out/tcpfwd .
package main

import (
	"io"
	"log"
	"net"
	"os"
)

// Dumb layer-4 forwarder: accept on $LISTEN, dial $TARGET, splice both ways.
// No proxying of arbitrary destinations: TARGET is fixed at startup, so a
// client of the listener cannot steer it elsewhere. Pure stdlib, static.
func main() {
	listen := envOr("LISTEN", ":8787")
	target := envOr("TARGET", "uroboros:8787")
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("listen %s: %v", listen, err)
	}
	log.Printf("tcpfwd: %s -> %s", listen, target)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handle(c, target)
	}
}

func handle(client net.Conn, target string) {
	defer client.Close()
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		log.Printf("dial %s: %v", target, err)
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
GO

# Prepare empty, correctly-owned dirs to seed the named volumes with non-root
# ownership (distroless has no shell to mkdir/chown at runtime), plus a symlink
# so the app's jwt.key (which it anchors next to the binary, in read-only /app)
# actually lands in a writable, NON-browsable keystore volume — not in the
# browsable /app/data workspace. Write-through a baked symlink works even though
# /app itself is read-only.
RUN mkdir -p /seed/data /seed/approot/keystore \
    && chown -R 65532:65532 /seed/data /seed/approot/keystore \
    && ln -s keystore/jwt.key /seed/approot/jwt.key \
    && chown -h 65532:65532 /seed/approot/jwt.key

# ---------------------------------------------------------------------------
# Stage 3a: the Uroboros server, on distroless (no shell / no pkg manager)
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS app
WORKDIR /app
COPY --from=gobuild --chown=65532:65532 /out/uroboros /app/uroboros
COPY --chown=65532:65532 skills/ /app/skills/
COPY --from=gobuild --chown=65532:65532 /seed/data /app/data
# approot carries the keystore/ dir and the jwt.key -> keystore/jwt.key symlink;
# copied as a directory so BuildKit preserves the symlink instead of dereferencing it.
COPY --from=gobuild /seed/approot/ /app/
USER 65532:65532
VOLUME ["/app/data", "/app/keystore"]
EXPOSE 8787
# Run the binary directly (NOT start.sh): the start.sh restart loop exists only
# to orchestrate the self-updater, which is both pointless (no egress) and
# impossible (read-only root FS) in this sandbox. Loopback-only intercept proxy.
ENTRYPOINT ["/app/uroboros"]
CMD ["-addr", ":8787", "-proxy", "127.0.0.1:8788", "-data", "/app/data"]

# ---------------------------------------------------------------------------
# Stage 3b: the UI forwarder, same hardened distroless base
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS gateway
COPY --from=gobuild /out/tcpfwd /tcpfwd
USER 65532:65532
EXPOSE 8787
ENTRYPOINT ["/tcpfwd"]
