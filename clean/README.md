# Uroboros 0.5 — clean local bundle

This bundle starts a fresh Uroboros control plane with authentication enabled,
no preconfigured operator password, no imported database and no LLM/provider
API key. The first visit opens the setup page; after the operator creates a
password, the normal login page protects the console. The UI is published only
on `127.0.0.1:8805` and must not be exposed to a LAN or the public internet.

## Supported hosts

The Windows Docker Desktop stack has been started and checked locally.
Linux launcher checks are separate from a full cold installation on a Linux host.

- Windows x86-64 with Docker Desktop using Linux containers and Compose v2;
- Linux x86-64 with Docker Engine, Compose v2, Bash, curl, GNU grep with PCRE,
  and standard core utilities.

The audited B0.5 broker/worker images are Linux amd64 images. ARM64 hosts are
not currently a native supported target.

## Start on Windows

From this directory in PowerShell:

```powershell
.\start.ps1
```

## Start on Linux

From the repository root:

```bash
bash ./install.sh
```

Or from this directory:

```bash
bash ./start.sh
```

Open <http://127.0.0.1:8805/setup/> for a fresh database, create the local
operator password, and use <http://127.0.0.1:8805/login/> on later visits. The
first build is large because
the source-built app includes the pinned Katana runtime, Playwright MCP and
Chromium, and the source-built Arsenal/API Lab sidecars include their exact tool
closures.

For a screen-by-screen walkthrough, see the
[English operator guide](../guide/README.md). It covers first-time setup,
the normal operation workflow, evidence review, safety, troubleshooting, and
every item in the 0.5 sidebar.

The launchers create only internal Docker credentials outside the source tree:

- Windows: `%LOCALAPPDATA%\Astra\runtime.env`;
- Linux: `${XDG_STATE_HOME:-$HOME/.local/state}/astra/runtime.env`.

The Linux directory is mode `0700` and the environment file is mode `0600`.
Neither file contains an Anthropic/OpenAI/Brave/Tavily or other provider API
key. If you later configure an LLM in the UI, that is your new local runtime
data, not part of the clean distribution.

Stop without deleting state:

```powershell
.\stop.ps1
```

```bash
bash ./stop.sh
```

Inspect status:

```powershell
.\status.ps1
```

```bash
bash ./status.sh
```

## Included runtime surfaces

- all built-in host tools and safety/evidence lifecycle implementations;
- classic keyless DDGS web research and fixed vulnerability-intelligence
  providers;
- isolated Tor `research_fetch` egress;
- Playwright Browser MCP with bundled Chromium, enabled for the agents by the
  idempotent bootstrap;
- pinned Katana core discovery;
- Arsenal scanners/discovery/Arjun/passive WAF/archive integration, kept
  backend-only behind Uroboros' task/scope authority;
- API Experiment Lab MCP, including source-analysis tools, enabled for agents;
- accepted B0.5 offline binary triage with LIEF, capa and FLOSS through the
  exact no-network broker/worker images.

The clean bundle intentionally does not expose unimplemented or disabled
future integrations. VVAH remains disabled, and B1+ Ghidra/radare2 layers are
not current implementations.

## Binary runtime archives

The B0.5 worker build consumes hash-locked generated corpora that are not stored
as source files in Git. This repository includes their accepted runtime images
as the following tracked archives:

- `runtime-images/binary-broker-b05.tar`
- `runtime-images/binary-worker-b05.tar`

`start.ps1` and `start.sh` first accept the exact audited local image digests and
otherwise check and load these archives. A missing local image is an expected
cold-cache condition. Both launchers refuse a changed archive or a different
broker/worker identity. Exact checksums are recorded in
`runtime-images/MANIFEST.md`.

A regular repository clone or GitHub-generated source ZIP includes both archives
and the manifest. On a fresh Docker host the launchers verify and load these
local files automatically. No separate runtime archive download is required.

To validate source, Docker availability, and the exact B0.5 payload without
building or starting the Compose stack:

```powershell
.\start.ps1 -PreflightOnly
```

```bash
bash ./start.sh --preflight-only
```

## Updates

The application release source is `nobanic/UroborOS` on GitHub. Release lookup
and binary downloads require a published platform archive and `SHA256SUMS`;
authenticated downloads from a private repository are not implemented.

This clean stack uses read-only container filesystems. Update it through a
qualified container-image rebuild/replacement that preserves its project,
volumes and private runtime credentials. The root `update.sh` targets the old
root Compose topology and is not a clean-stack updater. A dedicated clean-stack
update workflow remains to be qualified.

## Isolation and persistence

PostgreSQL, app data, sidecar jobs and the private binary CAS use Docker named
volumes scoped to the `uroboros` project. The JWT signing key has its own
non-browsable `uroboros_keystore` volume. Tool registration runs first in a
one-shot, non-published auth-disabled container; only after it exits does the
host-published auth-enabled application start. Arsenal and API Lab have no
direct internet network. The binary broker/worker use `network_mode: none`.
Tor has a dedicated app-to-SOCKS control network and a separate outbound network.

Deleting the named volumes is intentionally not automated because it destroys
tasks, settings and evidence. If a truly fresh reset is required, stop the stack,
review the exact `uroboros_*` volume names, and remove only those volumes.

Docker image names also use the `uroboros/` namespace. Docker project and image
identifiers use lowercase; the console remains **Uroboros**. Existing `astra`
installations require a data-preserving migration when changing the project
name. Their original volumes and private runtime file must be retained until
the migration is verified. The established private state directory and
`ASTRA_RUNTIME_DIR` override remain compatible; they are outside the source tree.
