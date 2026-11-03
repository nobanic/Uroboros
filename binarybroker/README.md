# Binary artifact broker and isolated worker boundary (B0.4 source)

This package is the raw-byte boundary for deterministic offline binary triage.
B0.1 provides the broker, CAS, durable receipts/references, database contract,
and reconciliation logic. B0.2 adds content-gated task intake, a fixed LIEF
profile, a separate worker-only socket, and bounded metadata/candidate
projections. B0.3 adds exact capa 9.4.0 plus exact rules/signatures without
changing the byte, execution, or finding-authority boundary. The source is not
runtime-accepted until the image/SBOM/CVE and adversarial Linux gates pass.
B0.4 adds exact FLOSS 3.2.0 decoded-string candidates behind a separate
32 MiB PE/ELF admission gate and a hard-disabled Vivisect workspace boundary.

## Boundary

- The CAS root is a private volume. It must not be mounted into the Uroboros
  application, task workspaces, agent containers, or future analyzer workers.
- Outside the explicit `stage` import stream, the application receives only
  receipts, opaque reference/artifact IDs, hashes, sizes, lifecycle state,
  provenance metadata, and bounded projections.
- The protocol has no raw-byte read/download operation and accepts no caller
  path. Stage is the only byte-bearing app operation.
- `uroboros-binary-broker` listens on an absolute app Unix socket, accepts
  exactly one bootstrap connection, closes and unlinks that listener, and never
  reconnects. A separate worker-only listener exposes one artifact per
  authenticated analysis lease. Go-created descriptors are close-on-exec.
- The CAS root and bootstrap socket directory must be disjoint mounts. The
  broker refuses root execution and a nonblocking process lock prevents two
  broker instances from owning the same CAS. Deployment must additionally use
  `network_mode: none`, a read-only root filesystem, `cap_drop: ALL`, and
  `no-new-privileges`. The broker keeps UID/GID `65531:65531` and receives only
  supplemental worker-socket GID `65533`; this is required to retain setgid on
  the `65531:65533` worker directory without widening its permissions.

The runtime command reads four host-owned settings:

```text
UROBOROS_BINARY_BROKER_ROOT=/var/lib/uroboros-binary-cas
UROBOROS_BINARY_BROKER_SOCKET=/run/uroboros-binary-bootstrap/broker.sock
UROBOROS_BINARY_WORKER_SOCKET=/run/uroboros-binary-worker/worker.sock
UROBOROS_BINARY_WORKER_IMAGE_DIGEST=sha256:<audited-image-digest>
```

The host must connect the application before agent loops or general-purpose
shell tools start. Loss of that persistent connection is fail-closed and
requires a new host bootstrap; the client contains no retry/dial loop after its
first successful connection. The worker socket directory is setgid and shared
only with the worker GID. Its socket is changed to mode `000` while an accepted
worker owns a sample and restored only after that slot completes, preventing a
parser that has seen bytes from reconnecting for another artifact.

## Crash-safe lifecycle

1. `stage` streams at most 512 MiB, hashes it, durably installs the CAS blob,
   and writes a receipt.
2. PostgreSQL registers a task-owned artifact, origin, and desired hot
   reference carrying that receipt ID.
3. Reconciliation commits the receipt to a broker reference and marks the DB
   reference active.
4. Archive and restore create the destination desired reference first. The
   reconciler clones it from a same-hash source and removes obsolete references
   only after every destination exists.
5. A blob with no receipt or reference gets an unreferenced marker. It can be
   deleted only after the hard minimum 24-hour grace. Abandoned receipts live
   for 48 hours by default before entering that grace.

Reconciliation fails closed on ledger corruption, missing/tampered blobs,
duplicate desired IDs, or a hash mismatch. PostgreSQL never contains raw bytes,
raw analyzer output, or host paths. Task archive packages contain metadata;
PostgreSQL retains the cold reference, while the private CAS needs its own
consistent backup/restore plan.

## Authority

The companion `binaryartifact` package owns immutable effect manifests for the
conditionally registered typed tools. Both normal permission checks and direct
`Call` execution are guarded. The database can change visibility and bindings,
but cannot grant
`analyze_static`, `execute_sample`, network, filesystem, or any other effect.
B0 observations are restricted to `metadata` or `candidate`, and
`execution_allowed` is constrained to false.

The fixed `standard-v1` policy allows only LIEF 1.0.0 structural projection,
capa 9.4.0 static capability candidates, and FLOSS 3.2.0 decoded-string
candidates with exact source, rules/signature, and normalization pins. It caps
analyzable artifacts at 128 MiB, FLOSS admission at 32 MiB, and the aggregate
canonical projection at 2 MiB, with stricter per-stage/output/count/time
limits. The broker validates artifact binding, stage hashes, status consistency
and `candidate`/`verified=false` capa and FLOSS records before persistence. The job lease,
broker reference, artifact fingerprint, analyzer identity, tool pins, and
resource profile are all bound together. The worker receives no CAS,
application, workspace, task, PostgreSQL, or Docker mount and runs without
networking.
