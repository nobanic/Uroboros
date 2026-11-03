# Uroboros 0.5 Operator Guide

This guide covers installation, first login, model and tool configuration,
authorized operations, graph navigation, traffic, findings and persistent state.
Return to the [main README](../README.md) for requirements and the source layout.

The screenshots were captured from the current compiled console at a
1600 × 1000 viewport with the blue dark theme. Main pages use built-in synthetic
demonstration data. Setup and login show the normal frontend with isolated
authentication-state fixtures. Domains, findings, provider status and usage in
these illustrations are examples, not live assessment results or credentials.
Your new installation starts with an empty database.

Use Uroboros only within your authorization. The console supports the evidence
and review process; the operator remains responsible for scope and conclusions.

## Contents

1. [Start the local stack](#1-start-the-local-stack)
2. [Initialize and sign in](#2-initialize-and-sign-in)
3. [Navigate Mission Control](#3-navigate-mission-control)
4. [Configure models and capabilities](#4-configure-models-and-capabilities)
5. [Define scope and guard rules](#5-define-scope-and-guard-rules)
6. [Create and monitor an operation](#6-create-and-monitor-an-operation)
7. [Read the evidence graph](#7-read-the-evidence-graph)
8. [Review traffic and findings](#8-review-traffic-and-findings)
9. [Use chat, records and workspace](#9-use-chat-records-and-workspace)
10. [Configure the system](#10-configure-the-system)
11. [Stop, restart and update](#11-stop-restart-and-update)
12. [Troubleshooting](#troubleshooting)

## 1. Start the local stack

Open a terminal in the package directory containing `clean/`, `web/` and
`go.mod`. Start Docker Desktop with Linux containers on Windows, or Docker Engine
on Linux. Both hosts require Compose v2 and an AMD64/x86-64 Linux engine.

Run the preflight first. It checks the source distribution, engine and exact
binary runtime images without building or starting application containers.

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\clean\start.ps1 -PreflightOnly
powershell -NoProfile -ExecutionPolicy Bypass -File .\clean\start.ps1
```

Linux:

```bash
bash ./clean/start.sh --preflight-only
bash ./install.sh
```

The first source build includes the frontend, browser runtime and pinned tool
dependencies. Wait for bootstrap and the authentication checks to finish, then
open <http://127.0.0.1:8805/>.

The repository includes the exact broker/worker archives under
`clean/runtime-images/` as regular Git files. A clone or GitHub-generated source
ZIP includes them; the launchers verify and load them on a fresh Docker host.
The [image manifest](../clean/runtime-images/MANIFEST.md)
defines the accepted identities. Do not replace a failed image check with an
unverified archive.

## 2. Initialize and sign in

A genuinely new database opens `/setup/`. There is no preset operator account,
password or model credential.

1. Enter a local passphrase with at least eight characters.
2. Confirm the passphrase.
3. Submit the initialization form.
4. Use that passphrase on `/login/` when signing in again.

![First operator initialization](../screenshots/0.5/01-setup.png)

![Operator sign-in](../screenshots/0.5/02-login.png)

The account is persistent runtime data. Do not put its password, provider keys
or signing key into the source directory. If setup appears unexpectedly, check
that you are using the intended Compose project and volumes before creating
another account.

## 3. Navigate Mission Control

**Mission Control** summarizes active operations, mapped assets, evidence,
pending human decisions and agent activity. Open an operation from the live
queue to inspect its details.

![Mission Control](../screenshots/0.5/03-mission-control.png)

The left navigation groups operational work separately from system controls.
The top bar identifies the current area, search and connection state. Contextual
decision rails connect related pages and describe the next useful action.

| Operational page | Purpose |
| --- | --- |
| Mission Control | Overall activity and posture. |
| Command Chat | Agent conversations and operation context. |
| Operations | Create, inspect, pause, resume and review operations. |
| Findings | Triage findings and their evidence. |
| Traffic | Search recorded HTTP exchanges. |
| Tool Runs | Audit tool inputs, execution state and outputs. |
| Model Ledger | Inspect model call records and usage. |
| Asset Graph | Manage organizations, assets, services and endpoints. |
| Workspace | Browse files and artifacts. |

`DEMO` in these screenshots identifies demonstration state. It is not evidence
that a real operation is running. A new normal installation has no seeded
assessment results.

## 4. Configure models and capabilities

### Create a model profile

Open **Model Fabric**. Review the current profile, health and failover order,
then choose **New profile**.

![Model Fabric](../screenshots/0.5/19-model-fabric.png)

Fill in the profile name, provider format, model, credential and connection
details. Use the endpoint and model identifier documented by your provider.
For an OpenAI-compatible Chat Completions service, choose the corresponding
format and its documented base URL. Select a native format only when the
provider supports it.

![New model profile](../screenshots/0.5/20-new-model-profile.png)

The editor also exposes streaming, rate and output limits, context size,
reasoning controls and retry behavior. Save the profile and explicitly make it
active when it is ready. Review pool priority and exclusions before relying on
failover.

**Load available models** and **Test connection** can contact the configured
provider. Confirm the intended account and limits before using them. A provider
balance error is separate from Uroboros research budgets.

### Review agent roles

Open **Agents** and inspect the model, tools, MCP servers and skills bound to
each role.

![Agent configuration](../screenshots/0.5/21-agents.png)

Bindings are deliberate boundaries. For example, planning and graph-management
tools need not be available to a worker executing one intent. A catalog entry
does not grant every agent permission to call it. Test a tool in the role that
owns it instead of repeatedly sending an unsupported worker intent.

### Review Toolchain, MCP and skills

**Toolchain** lists registered tools and their configuration. Check enabled
state and bindings when a tool is unavailable.

![Toolchain](../screenshots/0.5/23-toolchain.png)

**MCP Mesh** manages connected MCP servers and tool visibility. Deferred tools
are discovered on demand; discovery and invocation are separate from successful
execution of the requested feature.

![MCP Mesh](../screenshots/0.5/24-mcp-mesh.png)

**Skills** contains the product skill catalog and its bindings. These skills
are application capabilities, not developer-session instructions.

![Skills](../screenshots/0.5/25-skills.png)

## 5. Define scope and guard rules

Open **Asset Graph** to review organizations, assets, services and endpoints.
Use search and filters to inspect the relevant subset instead of treating every
known asset as an authorized target.

![Asset Graph](../screenshots/0.5/10-asset-graph.png)

Register the exact target scope supplied by the operator. Do not infer a wider
network or related domain from one authorized endpoint. Related-task evidence
and a globally visible asset are not automatically new testing authority.

Open **Asset Guard** to review asset-specific policy before running work.

![Asset Guard](../screenshots/0.5/11-asset-guard.png)

Some discovery is task infrastructure. The first eligible Katana crawl can
start automatically from the frozen task scope. The planner reviews the current
discovery snapshot and records run/skip decisions for typed families. A skip
with an empty scope is different from an attempted scanner run failing.

A discovery candidate, passive fingerprint or callback does not automatically
become a confirmed finding. Preserve the normal evidence and review lifecycle.

## 6. Create and monitor an operation

Open **Operations** and choose **New operation**. Give it a concrete goal,
authorized target context and useful constraints. Check the model profile and
scope before starting.

![Operation list](../screenshots/0.5/04-operations.png)

Before choosing **Create operation**, open **Automatic startup**:

The default keeps Katana enabled and lets the initial scout follow the workspace
setting:

![Automatic startup defaults](../screenshots/0.5/33-automatic-startup-defaults.png)

| Choice | Behavior |
| --- | --- |
| Initial Katana crawl | On by default. Turn it off to skip the infrastructure crawl for this operation. Automatic scheduling will continue to respect this choice after scope changes, resume and restart. |
| Initial reconnaissance scout | Use the workspace default, explicitly enable one bounded initial reconnaissance intent, or turn it off for this operation. The workspace setting is unchanged. |

![Choose initial discovery](../screenshots/0.5/32-automatic-startup.png)

These are startup choices, not tool bans. The planner and workers can still use
Katana and other tools later under the normal scope, discovery-review and
operator rules. A skipped infrastructure crawl is reported as
`skipped_by_operator`; the planner can review the available evidence without
waiting for a crawl that was intentionally omitted.

Goal decomposition, ordinary planning, the separately selected **Seed the first
intent**, and custom **on task create** agent triggers retain their own behavior.
Templates leave these runtime choices as currently entered. API clients can
send `skip_initial_katana: true` and `initial_scout: false`; omitted startup
options preserve the original defaults, and `initial_scout: null` follows the
workspace default.

Open an operation to inspect its **Overview**, exploration graph and associated
work. Review goals, active intents, tool results, captured evidence and the
main-agent conversation together.

![Operation workspace](../screenshots/0.5/06-operation-detail.png)

The living console provides a compact operational view with an operation
selector, activity and graph-focused navigation.

![Living console](../screenshots/0.5/05-living-console.png)

Use **Pause** when work needs review and **Resume** when the intended conditions
are ready. A goal is complete only when its acceptance condition has evidence.
Successful invocation alone is not proof of a useful result.

When asking the main agent to change direction, state the intended result and
the constraints clearly. A new intent may start model or tool work. A status
request and a request to launch new work are different actions.

## 7. Read the evidence graph

The graph relates goals, work, facts, findings and evidence. Node type, state,
edges and lineage explain where a conclusion came from and what can follow.

![Evidence graph](../screenshots/0.5/07-evidence-graph.png)

Use the available graph controls to:

- fit the graph to the workspace;
- zoom and pan while keeping readable node labels;
- search for a record by its visible text;
- filter the relevant record types or states;
- focus on a record and inspect its neighbors;
- open a record without treating its position or animation as finding authority.

The **graph record browser** gives a text-first view of graph contents. It is
useful when the graph is large or you want to find a specific record quickly.

![Graph record browser](../screenshots/0.5/08-graph-records.png)

Select or search for a record to open its inspector. Review its content, raw
data and lineage, then return to the graph context.

![Evidence inspector](../screenshots/0.5/09-evidence-inspector.png)

Animations communicate activity; they do not establish that an assessment or
verification succeeded. The interface respects reduced-motion preferences.
Use the underlying tool and traffic records when a conclusion is unclear.

## 8. Review traffic and findings

### Captured HTTP

Open **Traffic** to filter exchanges by host, path, method, status and other
available fields. Inspect the actual request and response associated with a
tool result or finding.

![Captured traffic](../screenshots/0.5/14-traffic.png)

Traffic is useful only when recording is enabled and the operation uses the
intended recording path. A screenshot of a tool response is not a substitute
for the saved exchange and its provenance. Preserve relevant capture IDs when
attaching evidence.

### Findings

Open **Findings** to review severity, state, affected asset and supporting
evidence. Follow a finding into its details and inspect the linked records.

![Finding triage](../screenshots/0.5/12-findings.png)

Separate an observation or static candidate from a verified runtime conclusion.
Review reproduction conditions and controls before confirming a finding. Export
only after checking the finding text, evidence and sensitive fields.

## 9. Use chat, records and workspace

### Command Chat

**Command Chat** provides a direct conversation with an agent, optionally in
an operation context. Check that context before asking it to perform work.

![Command Chat](../screenshots/0.5/17-command-chat.png)

### Tool Runs

**Tool Runs** records inputs, state and output. Inspect the actual result when
an agent reports a dependency error, missing job or rejected scope. These are
different outcomes and should not all be summarized as a successful tool test.

![Tool run audit](../screenshots/0.5/15-tool-runs.png)

### Model Ledger

**Model Ledger** shows recorded model activity and usage. Use it to understand
which profile handled a call and why a run stopped or changed provider.

![Model Ledger](../screenshots/0.5/16-model-ledger.png)

### Workspace

**Workspace** contains operation files and artifacts. Inspect their provenance
and contents before sharing them. Keep operator credentials and private reports
out of a public source export.

![Workspace](../screenshots/0.5/18-workspace.png)

## 10. Configure the system

### Intercept rules and approvals

**Intercept Rules** controls supported allow, deny and approval behavior. Review
the applicable rule and operation context before changing it.

![Intercept Rules](../screenshots/0.5/26-intercept-rules.png)

Open **Approvals** when an action is waiting for an operator decision. Check
the requested action and evidence before approving or rejecting it.

![Approvals](../screenshots/0.5/27-approvals.png)

### Signals and System Log

**Signals** configures notifications. Test the intended destination and avoid
including secrets or unnecessary target details in notification payloads.

![Signals](../screenshots/0.5/28-signals.png)

**System Log** helps explain connection, dependency and execution failures.
Correlate its timestamps with operation activity and tool records.

![System Log](../screenshots/0.5/29-system-log.png)

### Configuration domains

**Configuration** groups controls into Network & evidence, Agent runtime,
Search intelligence and Lifecycle & labs. Read each control's impact text;
some settings affect running agents or subsequent requests immediately.

![Configuration](../screenshots/0.5/30-configuration.png)

### Research budgets

Open **Search intelligence → Recorded failover & budgets**.

![Research providers and budgets](../screenshots/0.5/31-search-intelligence.png)

The audited broker has a shared research budget per operation. The default
40 cumulative attempts includes web searches, vulnerability-provider calls and
other audited research requests across its agents. Failed attempts, provider
fallbacks and retries count. Cache receipts do not.

**Attempts per minute** is a separate rolling 60-second admission limit.
Raising it does not replenish an exhausted cumulative budget. Raising
**Requests per task/session** allows later calls up to the new total; it does
not erase earlier attempts or automatically restart a stopped operation.

The current settings accept 1–100,000 cumulative requests and 1–10,000 attempts
per minute. Paid request units, provider quotas, model tokens and scanner
budgets remain separate. None of these settings widens target authorization.

### API and source experiments

API Experiment Lab tools are available through their configured MCP bindings.
Review schemas and the request catalog before starting an experiment. Inspect
status, results and artifacts; preserve source context when replaying an
exported recipe. Static matches are candidates, not runtime proof.

Remember that a container's `localhost` is that container. A service reachable
from the application or your Windows host is not automatically reachable from
API Lab. Use the intended service address and authorized scope.

## 11. Stop, restart and update

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\clean\status.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\clean\stop.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\clean\start.ps1
```

Linux:

```bash
bash ./clean/status.sh
bash ./clean/stop.sh
bash ./clean/start.sh
```

Stopping preserves PostgreSQL, settings, operation records, artifacts and the
signing keystore. Git rollback does not roll back those stores. Back up the
affected state before a migration, reset or behavior-changing experiment.

For an update, open **System settings → Version & Updates**. Choose
**Check for updates**, review the installed and available source commits, then
**Update from main**. Updates follow ordinary commits pushed to
[nobanic/Uroboros/main](https://github.com/nobanic/Uroboros/tree/main); no GitHub
Release is needed. Pause active and queued operations and finish running agent
conversations first.

![Update directly from main](../screenshots/0.5/34-console-updates-main.png)

For a private repository, expand **GitHub access** and save a repository token
with Contents: read-only. It is stored locally outside the checkout, and the
panel only reports whether one is configured. Once the repository is public,
updates also work without a token; **Clear token** switches to public access.

The progress panel follows source fetching, building, backup, restart and health
verification. The current console remains available during the build. Before
replacement, a private recovery snapshot is created; tasks, API profiles,
settings, artifacts and the signing key remain with the installation. If the
new stack cannot start, the updater attempts to restore the previous snapshot.
The console reconnects and reloads after the selected commit is verified.

![Update build progress](../screenshots/0.5/35-console-update-progress.png)

These two update screenshots use synthetic commits and a simulated update
response in the normal console. They illustrate the controls and progress;
they do not represent an update of an operator's installation.

An older installation needs one normal `clean/start` invocation from this
updated source to install the controller. Keep the same private state and
project. See [the update and recovery guide](../clean/updater/README.md).

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Docker cannot be reached | Start the intended Docker engine and verify Linux-container mode. |
| Required binary image is missing | Supply the exact archives from the matching release or preload their pinned image identities. |
| Archive checksum mismatch | Preserve the failed file and obtain the matching trusted archive; do not change the expected checksum. |
| Setup appears on an existing installation | Confirm project, private runtime file and volume names before initializing anything. |
| New dashboard is empty | A clean database has no demonstration operations; configure a model and create authorized work. |
| An agent reports `unknown tool` | Inspect its role bindings. Planner/main tools need not be in a worker registry. |
| Research reports `research_budget_exhausted` | Inspect the shared cumulative research count and configured task budget. |
| Provider returns a balance or quota error | Check that provider account and profile; this is separate from local research admission limits. |
| Main-agent activity is not updating | Check connection state and refresh once, then inspect tool activity and logs. Avoid resending paid work blindly. |
| A scanner has not started | Review current scope, discovery review and explicit run/skip decision before calling it a failure. |
| API Lab cannot reach localhost | Check container networking and use the intended scoped service address. |
| Traffic is empty | Check capture configuration and the operation's recording path. |
| A Git-free copy fails source provenance | Keep its valid `RELEASE_SOURCE` file; use the matching complete release. |

For installation details and source organization, return to the
[main README](../README.md) or [clean-stack guide](../clean/README.md).
