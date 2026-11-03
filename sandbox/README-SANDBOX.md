# Uroboros — hardened study sandbox

This directory runs Uroboros in a **sealed** container environment meant for reading the code and
clicking around the UI safely — not for launching real engagements. The design goal is strict:

> The untrusted app can neither reach the host nor the internet, and cannot leak the host's
> IP / DNS / network identity. The only thing exposed is the web UI, on `127.0.0.1` only.

Everything here is additive — the upstream `Dockerfile`, `docker-compose.yml`, and scripts in the
repo root are left untouched.

## Quick start

```bash
cd sandbox
# a .env with a random Postgres password is already generated; otherwise:
#   cp .env.sandbox.example .env  && edit POSTGRES_PASSWORD
docker compose -f docker-compose.sandbox.yml up -d --build   # first build compiles from source
open http://127.0.0.1:8787                                   # first visit -> /setup (set admin pw)
./verify-isolation.sh                                        # prove the seal against the live stack
```

Tear down (keep data) / wipe everything:

```bash
docker compose -f docker-compose.sandbox.yml down            # stop
docker compose -f docker-compose.sandbox.yml down -v         # stop + delete the DB & data volumes
```

## Why this is built the way it is

**Built from source, not pulled.** The upstream compose pulls a prebuilt image
(`nobanic/uroboros`). We instead compile the frontend and the Go binary ourselves in a multi-stage
build (`sandbox/Dockerfile`), so only code you can read runs. Building needs the network; the
**running** containers do not get any.

**Distroless base (no shell, no package manager).** The app and the UI forwarder run on
`gcr.io/distroless/static-debian12:nonroot`: a minimal, hardened, non-root (uid 65532) image with
no shell or tooling for an attacker to pivot through. A static (CGO-disabled) Go binary needs
nothing more. The only container with a shell is Postgres (it needs one for `initdb`), and it sits
on the sealed network with no exposed ports.

**Two networks, sealed topology.**

```
host 127.0.0.1:8787 ─► [gateway] ─► [uroboros] ⇄ [ monixa.local ]   [postgres]
                       edge + lab    lab        lab (your target)   lab
                       forwarder     └─────── SEALED LAB ───────────┘
                                     no internet · no host · intra-lab OK
```

- `lab` is a Docker network created with `internal: true` — it has **no route to the host or the
  internet**, but containers **on it can talk to each other**. `uroboros` and `postgres` live only
  here. From this network, outbound TCP is "Network unreachable", external DNS returns `SERVFAIL`,
  `host.docker.internal` does not resolve, and there is no default route at all. (All verified —
  see `verify-isolation.sh`.) Its fixed name is `uroboros_sandbox_lab`.
- The web UI is reachable only because a ~30-line pure-Go L4 forwarder (`tcpfwd`) straddles both
  networks and splices inbound `127.0.0.1:8787` to `uroboros:8787`. Its target is fixed at startup,
  so a client cannot steer it elsewhere, and the app **cannot use it as a pivot**: the app has no
  default route and no IP-forwarding, so it has no path onto the edge network.
- The published port is bound to `127.0.0.1` — the UI is not exposed to your LAN.

**Defense in depth on the app container.** `read_only` root filesystem (also disables the
self-updater — it can't overwrite its own binary), all Linux capabilities dropped, `no-new-privileges`,
non-root, a `tmpfs` `/tmp` with `nosuid,nodev,noexec`, a single writable **named volume**
(`/app/data`) so no host filesystem path is ever mounted, plus memory / CPU / PID limits so a
runaway can't exhaust the host. `host.docker.internal` is additionally pinned to loopback.

**No host paths, no host identity.** Data and DB live in Docker **named volumes**, not host bind
mounts, so nothing from your filesystem is visible inside. The container has its own network
namespace and a fixed hostname; it never sees your macOS interfaces or real IP. (On Docker Desktop
all of this already runs inside a LinuxKit VM, which is a second boundary beneath these controls.)

## What works / what doesn't, sealed

Works: the full web UI, first-run `/setup`, login, dashboards, settings, browsing the code's
behavior, Postgres persistence, and **scanning a target you attach to the lab** (`monixa.local`).
This is everything you need to study and exercise the system.

Does not work unless you add a local LLM: the *autonomous* reasoning. The agent calls an LLM API,
which the seal blocks from the internet. Attach a local LLM to the lab network (see below) to drive
it fully offline — or just use the UI/manual features, which need no LLM. Either way, nothing can
reach the internet or your host.

## Testing against your own target container (`monixa.local`)

The whole point of the sealed **lab** network is that you can drop a target you own onto it and let
Uroboros scan it, with neither side able to reach the internet or your Mac.

1. Start your target container however you normally do (its own `docker run`/compose is fine).
2. Attach it to the lab network with a friendly name:

   ```bash
   ./attach-target.sh <your-target-container-name>        # default alias: monixa.local
   ```

   The script attaches the container, then verifies from inside the lab that `http://monixa.local`
   responds **and** that the internet is still unreachable. (Detach later with
   `./detach-target.sh <name>`.)
3. In the Uroboros UI, point a task / scope at `http://monixa.local`.

Prefer to wire it in your target's own compose instead of the script? Add:

```yaml
networks:
  uroboros_lab:
    external: true
    name: uroboros_sandbox_lab
services:
  monixa:                       # your target service
    networks:
      uroboros_lab:
        aliases: [monixa.local]
```

**Keep the lab hermetic:** for a true air-gap, the target should be on the `uroboros_sandbox_lab`
network *only*. If it also sits on an internet-capable network, Uroboros could pivot out through it —
`attach-target.sh` warns you when it detects this. Only ever point Uroboros at targets you own.

## Note on the AI agent with no internet

You asked for no internet egress, which is fully honored. Be aware the **autonomous** part of Uroboros
calls an LLM API to reason — with the lab sealed it cannot reach Anthropic/OpenAI. If you want the
agent to actually drive scans of `monixa.local` offline, run a **local** LLM (e.g. an Ollama
container) and attach it to `uroboros_sandbox_lab` too, then set Uroboros's LLM base URL to it in the UI.
Everything stays on the sealed network; still no internet. (Browsing the UI, configuring, and
manual features work without any LLM.)

## Variant B: secured, WITH internet (`docker-compose.internet.yml`)

Same hardened app, but egress to the **public internet** is allowed — so the agent can reach LLM
APIs and authorized external targets — while the **host and your LAN stay blocked** and the
**database still has no internet**.

```bash
cd sandbox
docker compose -f docker-compose.internet.yml up -d --build
open http://127.0.0.1:8787
./verify-internet.sh
```

How it's enforced: a tiny Alpine+iptables **firewall** container holds the egress policy, and the
Uroboros app shares its network namespace (`network_mode: service:firewall`), so every packet the app
sends is filtered: loopback + DNS + the DB subnet + the public internet are allowed; `10/8`,
`172.16/12`, `192.168/16`, `169.254/16`, `100.64/10` (your Mac, your LAN, cloud metadata) are
dropped; IPv6 egress is killed so it can't bypass the rules. Postgres stays on the sealed `dbnet`
with no internet, and the UI is still published on `127.0.0.1` only. (All verified by
`verify-internet.sh`: public IP/DNS/`api.anthropic.com` reachable; host + private ranges blocked.)

To run explorations, put your LLM key in the UI or in `sandbox/.env`
(`ANTHROPIC_API_KEY=` / `OPENAI_API_KEY=`). Only ever point Uroboros at targets you are authorized to
test.

### Which variant to use

| | Sealed (`docker-compose.sandbox.yml`) | Internet (`docker-compose.internet.yml`) |
| --- | --- | --- |
| Internet egress | none | public only (host/LAN blocked) |
| Reach a local target (`monixa.local`) | yes, on the sealed lab | yes (attach to `dbnet`) + public targets |
| LLM / autonomous agent | only via a local LLM on the lab | yes (real LLM APIs) |
| Database internet | none | none |
| UI exposure | `127.0.0.1` only | `127.0.0.1` only |
| Best for | reading code, offline study, air-gapped target testing | real runs you control, online targets, cloud LLMs |

Both are the same distroless, read-only, non-root, capability-dropped images.

### Optional Tor research profile

The internet variant can add the bounded `research_fetch` egress profile with
the Tor overlay:

```bash
docker compose \
  -f docker-compose.internet.yml \
  -f docker-compose.tor-research.yml \
  up -d --no-build
```

Build and audit the pinned Tor image before a production promotion, then set
`UROBOROS_TOR_IMAGE` to that exact accepted tag. The overlay does not route the
normal browser, WebFetch, LLM, scanner, or DDGS traffic through Tor. It adds a
client-only Tor sidecar with no host port and two dedicated networks: an
internal SOCKS control segment shared only with the firewall/app network
namespace, and a Tor-only egress segment. The application receives only the
fixed deployment-owned SOCKS endpoint; per-task isolation credentials and
origin grants remain host-owned and `research_fetch` has no direct fallback.

Removing the overlay and recreating `firewall` plus `uroboros` disables the Tor
profile without changing PostgreSQL or the application image.

### Katana-enabled production promotion

The default `sandbox/Dockerfile` remains the minimal distroless study image and
does not contain Katana, Chromium, or the pinned DIT model. A production runtime
that enables task-scoped Katana must therefore promote an exact committed app
binary with `Dockerfile.katana-runtime`. That narrow wrapper:

- inherits the already validated K2R dependency image;
- verifies the supplied binary, Katana 1.8.0, and DIT model hashes at build time;
- preserves writable non-root `/app/data` and `/app/keystore/jwt.key`
  persistence boundaries;
- drops to UID/GID 65532 before runtime; and
- remains subject to this Compose file's read-only root, capability drop,
  no-new-privileges, resource limits, and firewall network namespace.

Headless Chromium cannot start when its own working directory is mounted
`noexec`. General `/tmp` therefore remains `noexec`, while Compose supplies the
non-root process with a separate, non-persistent `/app/katana-tmp` tmpfs. It is
private (`0700`), size-limited, and executable solely for Katana/Chromium scratch
files; `TMPDIR` points there and the directory is discarded with the container.

Set `UROBOROS_APP_IMAGE` to the exact immutable tag and start Compose with
`--no-build`. Do not retag or silently rebuild the default distroless image as a
Katana deployment. The source commit, runtime-packaging commit, base image ID,
binary SHA-256, final image ID, database/volume backup, and rollback tag must be
recorded for every promotion.

The internet stack pins its sealed DB and egress bridges to `10.89.7.0/24` and
`10.89.8.0/24`. This avoids Docker choosing a different subnet on each restore
or exhausting its default address pools; verify both ranges are unused on the
deployment host before starting the stack.

## Files

| file | purpose |
| --- | --- |
| `Dockerfile` | multi-stage build-from-source; targets `app` and `gateway` on distroless |
| `Dockerfile.katana-runtime` | verifies and promotes one exact app binary into the pinned K2R runtime |
| `docker-compose.sandbox.yml` | the sealed lab + edge stack + all hardening |
| `docker-compose.internet.yml` | secured variant WITH public internet (host/LAN still blocked) |
| `docker-compose.tor-research.yml` | optional client-only Tor profile for bounded grant-scoped research |
| `firewall/` | minimal Alpine+iptables egress firewall for the internet variant |
| `verify-internet.sh` | proves the internet variant: public reachable, host/LAN blocked, DB sealed |
| `.env` | local-only random Postgres password (gitignored; never leaves the sealed net) |
| `attach-target.sh` / `detach-target.sh` | add/remove your target container (`monixa.local`) to the sealed lab |
| `verify-isolation.sh` | proves the seal against the running stack (structural + behavioral) |
| `README-SANDBOX.md` | this file |
