# B0 LIEF + capa + FLOSS worker

This image statically analyzes exactly one broker-provided artifact and exits.
The operator does not provide analyzer flags, rule paths, workspace paths, or a
second upload: the task-owned intake stages the artifact in the private CAS and
the broker streams only that artifact to the worker.

The runtime contract requires `network_mode: none`, a read-only root
filesystem, all Linux capabilities dropped, `no-new-privileges`, a bounded
`/work` tmpfs mounted `noexec,nosuid,nodev`, and only the worker Unix socket.
The worker must never receive the CAS, application workspace, PostgreSQL, task
data, Docker socket, or model-visible source mounts.

## Reproducible inputs

From the repository root:

1. Run `binaryworker/fetch-wheel.ps1` for the exact LIEF 1.0.0 wheel.
2. Run `binaryworker/fetch-capa.ps1` for the exact hash-locked capa 9.4.0
   static-Vivisect wheel closure, rules, and signatures.
3. Run `binaryworker/fetch-floss.ps1` for the separate exact hash-locked FLOSS
   3.2.0 wheel closure. Two reviewed wheels built from exact PyPI sdists are
   committed under `floss-local-wheels`; all other wheels come from PyPI by
   their locked digest.
4. Build `binaryworker/Dockerfile` with networking disabled.

The generated wheelhouses and corpora are ignored by Git. The Docker build is
offline, uses `--require-hashes --no-deps`, and fails unless it sees exactly one
LIEF distribution, the 34 expected capa distributions, the 34 expected FLOSS
distributions, 1,045 rule files, and three signature files. The pinned upstream
identities are:

- capa 9.4.0 source commit
  `7a79f799a70f052e8382cdbeaec8115daba14a5b`;
- capa-rules 9.4.0 commit
  `2af9fbfc1c9b4634dbeb76b5d34fca9389fa7f80`;
- ordinal rule-corpus manifest SHA-256
  `1910582fdcc7fd0bff198cfe07f4f1e3ceaf3acbd0c840d52d6507009d2bb5e2`;
- signature-corpus manifest SHA-256
  `1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e`.
- FLOSS 3.2.0 source commit
  `69e8ea21ca78deb2418abb68f86c838d1d2672c7`.

IDA-only packages and their network/update client chain are intentionally not
installed. The fixed runner can select only capa's static backend, supplies the
rules and signatures itself, and forcibly disables Vivisect workspace
persistence.
FLOSS runs through a second fixed runner with only decoded strings enabled,
PE/ELF admission capped at 32 MiB, the same reviewed signature corpus, and
`FLOSS_LOAD_VIV_WORKSPACE=0` plus `FLOSS_SAVE_WORKSPACE=0`. Its dependencies
remain in a separate site-packages tree from both LIEF and capa.

## Output boundary

LIEF emits bounded structural metadata. capa's raw JSON, rule source, match
trees, command line, timestamps, source paths, and captures never cross the
worker boundary. The worker emits only a bounded summary and normalized
capability candidates. FLOSS likewise emits only a bounded summary and decoded
string candidates; raw paths, timings, function scores, static strings, and
other upstream result fields are dropped. Every capa/FLOSS record is marked
`authority=candidate` and `verified=false`. The broker independently validates
schema, artifact binding, stage hashes, status consistency, limits, stable keys,
and candidate-only authority before the intake can persist observations.

The final image is a shell-less scratch runtime containing only the required
CPython runtime, locked Python distributions, rule/signature corpora, and their
native library closure. OpenSSL, pip, package managers, and general-purpose
command-line tools are absent. Image activation remains prohibited until its
digest, SBOM, license inventory, CVE scan, adversarial Linux runtime gate, and
fresh PostgreSQL lifecycle have been recorded.
