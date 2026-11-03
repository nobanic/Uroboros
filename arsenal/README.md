# Arsenal MCP execution sidecar

Arsenal is an execution sidecar, not the task/scope authority. Uroboros exposes
the model-facing `arsenal_run`, `arsenal_discover`, `arjun_discover`,
`waf_fingerprint`, `arsenal_status`, `arsenal_results`, `arsenal_cancel`, and
archive/restore host tools. Those tools derive `task_id` from the current run,
freeze canonical task scope, and persist lifecycle/evidence in PostgreSQL.
Network-capable tools receive authenticated fail-closed leases. The model never
supplies scope snapshots, proxy addresses, credentials, hashes, response
headers, passive providers, profiles, or raw scanner flags.

The MCP server includes seven backend-only task tools:

- `arsenal_metadata` returns exact binary/template/wordlist provenance;
- `arsenal_task_scan` executes one synchronous scanner call under the frozen
  lease and requires matching scope/config hashes plus the per-run proxy;
- `arsenal_task_subdomains`, `arsenal_task_ports`, and
  `arsenal_task_services` are the three internally sequenced T2 discovery
  stages;
- `arsenal_task_arjun` performs the bounded, proxy-recorded parameter probe for
  one already admitted GET endpoint;
- `arsenal_task_waf` evaluates only the stored response headers of one admitted
  GET endpoint. It has no target URL, body, proxy, credential, flags, profile,
  or request-budget parameter and performs zero target requests.

These tools cannot be selected directly through the model-facing catalog.

The legacy scanner/job tools below remain available for direct operator
diagnostics, but Uroboros deliberately withholds every raw Arsenal MCP tool from
agents. `register.py` registers the backend and removes old direct visibility.

One Streamable HTTP MCP server provides `nuclei_scan`, `httpx_probe`,
`ffuf_fuzz`, and five Nuclei job tools. Each scanner owns one execution slot. The shared budget defaults
to two simultaneous scans (`ARSENAL_MAX_PARALLEL`); a second scan of the same
tool or a full budget returns `exit_state=busy` without waiting in a queue.

The inline MCP handlers await worker threads instead of blocking the request loop.
For inline scans, MCP `notifications/cancelled` cancels the corresponding invocation: its worker
kills and reaps the process tree before releasing its slots. Cancelling one
invocation does not cancel another scanner. Uroboros sends this notification
when its caller cancels or its HTTP deadline expires.
Background scans require explicit `nuclei_job_cancel`; cancelling a status
request or closing its session leaves the scan running.
The message follows the [MCP cancellation specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/utilities/cancellation).

## Configuration

```bash
docker compose -f arsenal/docker-compose.arsenal.yml --env-file bench/.env up -d --build
```

- `ARSENAL_TOKEN`: required bearer token, supplied to the MCP client through
  the `Authorization` header.
- `ARSENAL_ALLOWLIST`: optional guard for legacy operator calls. It is not used
  as task authority; backend task runs always fail closed on empty/mismatched
  frozen scope and route all HTTP(S) requests through Uroboros.
- `ARSENAL_MAX_PARALLEL`: shared execution budget, default `2`.
- `ARSENAL_WORDLIST`: ffuf wordlist path, default `/opt/arsenal/wordlist.txt`.
- `ARSENAL_JOB_DB`: SQLite job/evidence storage. Compose sets
  `/var/lib/arsenal/jobs.sqlite3` in the persistent `arsenal_jobs` volume.
- `ARSENAL_NUCLEI_JOB_TIMEOUT`: full background scan budget, default `3600`
  seconds, allowed range `10..7200`. Independent of MCP request deadlines.
- `ARSENAL_SOURCE_REVISION`: source commit embedded as an OCI label and returned
  in provenance. Production builds must set it to the committed source revision.

The image pins Alpine by digest, exact Python packages, Nuclei 3.3.0, httpx
1.6.9, ffuf 2.1.0, subfinder 2.17.0, naabu 2.6.1, nuclei-templates 10.5.0, and
the exact 172-plugin WAFW00F 2.4.2 corpus. Every downloaded archive is SHA-256
verified. WAFW00F itself is not installed and its CLI/network engine is not
available: a strict local interpreter accepts only the audited passive
header/cookie matcher subset from the vendored BSD-3-Clause corpus. Naabu's
official linux/amd64 artifact uses the glibc loader, so the Alpine image includes
`gcompat`; the artifact itself remains checksum-pinned. Runtime update
checks/downloads are disabled; an upgrade means a new image and a new
model-free gate.

## Task-bound contract

`arsenal_task_scan` accepts only typed configuration for `nuclei`, `httpx`, or
`ffuf`. It rejects unknown fields and verifies canonical scope/config hashes.
The recording proxy URL must carry an unguessable per-run credential. A fresh
runtime directory receives only a small environment allowlist and the Uroboros
CA; model/provider/cloud credentials are never inherited.

- httpx receives one explicit scoped URL, uses the recording proxy, follows
  redirects only through that proxy, and returns service candidates.
- ffuf receives one explicit scoped origin/path; calibration and every wordlist
  request use the proxy. Hits remain candidates until Uroboros matches them to a
  captured exchange and imports the verified endpoint.
- Nuclei loads only the pinned `http/` corpus (`-pt http`), keeps code templates
  disabled, disables Interactsh/OAST, and proxies internal HTTP requests. Its
  observations are hypotheses, never findings.

Uroboros owns durable run/status/results/cancel, task isolation, archive/restore,
Traffic IDs, asset provenance, and finding count. Sidecar SQLite remains only a
legacy operator-job store and is not the authoritative task record.

### Passive WAF fingerprinting

`waf_fingerprint(endpoint_id)` accepts only an endpoint ID; `task_id` comes from
the active run. Admission requires an existing same-task GET endpoint backed by
a proxy-verified Katana or Arsenal Traffic exchange. Uroboros loads only the
stored response head, rejects malformed or oversized fields instead of
truncating them, preserves repeated `Set-Cookie` values, and sends a normalized
bounded header map to the sidecar. The body is never read or transferred.

The fixed `wafw00f-passive-response-headers-v1` profile disables attack,
content, status, and reason matchers. It evaluates only WAFW00F's ordinary
header/cookie predicates, caps output at 32 deterministic candidates, and
persists only plugin ID, vendor/manufacturer, evidence kind, endpoint ID,
Traffic ID, and fixed provenance. Raw headers, cookies, regexes, bodies, and
redirect locations are not persisted. Results are candidate observations, not
assets, scope, graph facts, or findings. Repeated calls reuse the same completed
job only while the task and exact Traffic/header/config authority still exist.

### Task-bound discovery chain

`arsenal_discover` accepts one registrable domain that must exactly match a root
domain in the frozen task scope. Its only configurable fields are a bounded port
set, subdomain/address limits, one total workflow budget, and the existing HTTP
request budget. Uroboros—not the sidecar and not the model—sequences:

1. subfinder against the fixed no-key HTTP sources `anubis` and `rapiddns`;
2. host-side system DNS, whose bounded A/AAAA results are frozen for the run;
3. naabu in CONNECT mode against only those IPs and typed ports;
4. httpx against only hostname/port pairs that naabu reported open.

The provider calls use a GET-only recording proxy layered over an authenticated
SOCKS lease pinned to pre-resolved provider addresses. Both fixed providers must
produce a successful captured response; source errors or a missing provider are
an incomplete run, not a clean negative. The domain is therefore disclosed to
AnubisDB and RapidDNS, and to no configurable source. Wayback is not part of this
chain.

A completed composite discovery job may later supply one exact route to
`arjun_discover(endpoint_id)`. Uroboros reconstructs it only when the selected,
Traffic-backed httpx endpoint and the same job's DNS and naabu observations agree
on task, hostname, address, and effective port. This job-local authority does not
create IP/CIDR task scope. Missing, unrelated, ambiguous, cross-task, or
non-Traffic evidence fails before an Arjun job is created; the frozen route hash
and discovery job/Traffic provenance remain durable.

Naabu gets IP literals plus an exact-destination SOCKS lease. Its `-verify`
option is deliberately absent: naabu 2.6.1 performs that second verification
with a direct dial rather than its configured proxy. httpx uses the ordinary
Traffic recording proxy, whose run-specific upstream is another frozen SOCKS
lease; redirects cannot escape the discovered hostname/port set.

Subdomain, DNS, and port observations remain candidates without Traffic-backed
asset promotion. Only a final httpx record matched to a captured Traffic
exchange can create service/endpoint asset evidence. No stage creates a finding.

Set this server's **Request timeout** to at least **240 seconds** in Uroboros.
Inline Nuclei's execution budget is 180 seconds, ffuf's 120 seconds and httpx's 30
seconds. Transport time must cover cleanup and result delivery as well.
`register.py` sets 240 seconds for new registrations and raises a shorter
deadline on an existing Arsenal entry without changing its other settings.
HTTP MCP servers retain the legacy 120-second default unless configured.

In **MCP → server → Tools**, each tool has per-agent access switches. They
narrow access granted by server visibility or a skill; an enabled tool switch
alone does not grant server access. Filters apply to new runs, including tools
unlocked through skills. They survive server visibility changes and server
renaming. Existing configurations without filters keep all tools available.

## Result contract

Scanner calls and individual job responses return `tool`, `exit_state`,
`complete`, `records`, and `stats`. `nuclei_job_list` returns a `jobs` index.

| State | Meaning |
|---|---|
| `ok` | Completed scan with parsed observations |
| `ok_empty` | Completed scan with no observations; not proof the target is secure |
| `partial` | Some usable observations, but parse errors or a record limit |
| `parse_error` | No usable records from an invalid/incomplete report |
| `busy` | Scanner slot or shared execution budget occupied |
| `running`, `cancel_requested` | Background scan active; poll its job ID |
| `interrupted` | Server restarted during scan; stored evidence remains partial |
| `timeout`, `cancelled`, `tool_error`, `scope_rejected` | Check did not complete |

Only `ok` and `ok_empty` have `complete=true`. `records_limited`, parse counts
and output truncation are explicit; nonempty records do not imply completion.
An output-cap kill is a tool error even if the process exits with code zero.
httpx parses every JSONL observation, not only the first line.

### Nuclei scope

For a light/smoke test, explicitly pass `profile="light"` to `nuclei_scan`.
It selects four bundled HTTP templates (technology detection, Juice Shop
detection, Prometheus metrics, directory listing), uses a 60-second execution
budget, serial template execution, a 3-second per-request timeout and no request retries. This is a narrow
check, not full vulnerability coverage; exact selection is returned in `stats`.
All Nuclei profiles omit full HTTP request/response bodies to keep tool results
compact. Matcher metadata and extracted results remain available.

`profile="full"` remains the default scope. With default `execution="auto"`,
it now starts a **background job**, returning immediately with `job_id`,
`status="running"`, `complete=false`. The default scan budget is one hour,
independent of the initiating HTTP request, model turn or MCP session. This
changes the full scan response workflow: callers must follow the job to completion.
`template_ids` (comma-separated) selects particular IDs instead of the light template list;
`severity` and `tags` further filter that selection. Runtime automatic update
checks are disabled in the offline sidecar. Template updates remain build-time.

On timeout, completed Nuclei JSONL observations are preserved, `complete` stays
false, and the last available scanner statistics sample appears as
`stats.progress`. A partial scan with no records is not a clean result. Repeating
an identical broad scan does not reduce its scope; choose a relevant subset.

ffuf uses automatic calibration and noninteractive mode. Its findings are
discovery candidates that still require verification. ffuf 2.1.0 can put a
calibration word into `input.FUZZ` on a genuine result; normalized identity is
derived from the actual request URL instead. Original output remains in `raw`.

## Verification

Run the Python tests in the Arsenal image with the project mounted at `/src`:

```bash
docker run --rm -v "$PWD:/src:ro" -w /src uroboros-arsenal/sidecar:local \
  python3 -m unittest discover -s arsenal/tests -v
```

Tests include real MCP HTTP/auth/discovery/concurrency/cancellation, process-tree
cleanup, result completeness, real ffuf against a local SPA fixture, and passive
WAF evaluation against a redirect/cookie canary whose request counter must stay
at zero. No LLM or external target is used. MCP integration tests require the
runtime image and are skipped when its dependencies are absent.

For production promotion, set `UROBOROS_ARSENAL_IMAGE` to the exact validated
image tag and invoke Compose with `--no-build`. Leaving the variable unset keeps
the local-development tag `uroboros-arsenal/sidecar:local`.

### Following a Nuclei job

1. `nuclei_scan(target=..., profile="full")`: retain the returned job ID.
2. `nuclei_job_status(job_id=..., wait_seconds=10)`: wait/poll progress.
3. `nuclei_job_results(job_id=..., offset=0, limit=20)`: read observations;
   follow `next_offset` while `has_more=true`. Raw JSON is stored, but omitted
   by default; use `include_raw=true` for individual validation evidence.
4. `nuclei_job_cancel(job_id=...)`: request cancellation, then poll for the
   terminal state. `nuclei_job_list()` recovers IDs after reconnecting.

Background jobs keep running when an MCP session ends or a status request is
cancelled. Server restart preserves observations, marks active jobs interrupted,
and does not auto-resume or rescan. Compose retains the last 100 jobs in a named
volume; removing that volume deletes their evidence. One active Nuclei job is
allowed; duplicates return busy with the existing ID rather than queueing.

`execution="inline"` explicitly retains synchronous behavior and its 180s full
or 60s light limit; inline budgets cannot exceed those limits. Inline MCP
cancellation still kills the scanner. `execution="background"` also supports
light scans. `budget_seconds=10..7200` customizes a job's execution deadline.
The stdout/stderr caps and record limit still apply; exceeding them produces
an incomplete result. Scan completion describes the selected templates and
enabled engine capabilities, not proof of complete vulnerability coverage.

### Persisted diagnostic evidence

Background Nuclei jobs pass a unique `-elog` path to the native engine. A reader
archives JSONL request errors incrementally in the `diagnostics` table of
`/var/lib/arsenal/jobs.sqlite3`, together with full bounded stderr. Both are
linked to the same job ID as observations, in the existing persistent Compose
volume. Temporary error files live in `job-artifacts` next to the database and
are removed after capture; SQLite is the durable archive. Retention prunes
diagnostics together with the last-100-job history. Container recreation keeps
this volume; deleting the volume deletes the evidence.

`nuclei_job_status`, result pages and the job list include `stats.diagnostics`:
capture availability/completeness, error-log/stderr entry counts, error
categories, protocol counts and truncation. Counts describe archived data;
they are not interchangeable with the native engine's `errors` counter.
Categories are conservative hints, not confirmed causes of the historical
scan's errors or proof of a ban. No missing legacy log is reported as zero
errors: jobs created before this feature explicitly say `capture=unavailable`.

Use `nuclei_job_diagnostics(job_id=..., stream="errors", category="timeout",
offset=0, limit=20)` for details. `stream="stderr"` reads the complete archived
stderr as pages; `stream="all"` includes both. Follow `next_offset` while
`has_more`. Each entry exposes at most 2000 characters; for a long line use
`limit=1`, retain the same page offset/filter and follow `next_text_offset` as
`text_offset` until `text_has_more=false`. This makes the full archived line
readable without putting the entire log in a model's context at once.
Log contents are untrusted target/tool data, separate from finding records.

The native error-log cap is 4 MiB, monitored while the process runs, alongside
the existing stdout/stderr byte caps. Reaching it requests scanner cancellation
and yields an incomplete tool error with `diagnostics_truncated`; storage
failure is explicit too. File growth between monitor samples can slightly
overshoot the cap before the process stops; only capped data is archived.
`capture_complete` describes diagnostic retention separately from scan
completion: cancellation/timeout may still have completely archived the
produced log, while restart interruption may leave a partial archive.

Inline Nuclei still has its compact stderr preview; durable per-job capture is
provided for background jobs. Use background execution when archived
diagnostics are required. httpx/ffuf behavior is unchanged.
