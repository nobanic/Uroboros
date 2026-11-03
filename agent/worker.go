package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/norma/agentcore"
	"github.com/nobanic/norma/harness"
	"github.com/nobanic/norma/llm"
	actool "github.com/nobanic/norma/tool"
	"github.com/nobanic/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts carries only host-owned audited research tools and their shared
// per-agent gate. Provider selection, credentials, proxy and budgets deliberately
// never enter an agent object or agentcore.Options.
type WebSearchOpts struct {
	Enabled bool
	// Tool is the host-owned audited broker. When present, agents receive this
	// exact tool before augmentation and the SDK's opaque auto-tool stays off.
	Tool actool.CoreTool
	// Tools are additional typed research capabilities governed by the same
	// operator and per-agent web-search gate (for example vulnerability lookup).
	Tools []actool.CoreTool
}

func appendAuditedResearchTools(base []actool.CoreTool, options WebSearchOpts) []actool.CoreTool {
	if !options.Enabled {
		return base
	}
	if options.Tool != nil {
		base = append(base, options.Tool)
	}
	return append(base, options.Tools...)
}

type Worker struct {
	findingRecorder FindingRecorder
	modelRuntime
	windowFn func() int // optional dynamic task-chain minimum
	maxTurns int        // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- UROBOROS_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.modelRuntime.setNonStreaming(fn) }

func (w *Worker) nonStreaming() bool { return w.modelRuntime.nonStreaming() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.modelRuntime.setNoaEnabled(fn) }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.modelRuntime.setMaxTokens(fn) }

func (w *Worker) maxTokens() int {
	return w.modelRuntime.maxTokens()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "You are about to be terminated because the budget is exhausted. Do not run any more commands/probes. Please, in order: (1) write back, one by one, everything you have already identified above but not yet written back — use insert_assets for new assets, record_fact for exploration conclusions/facts, report_finding for confirmed vulnerabilities; (2) **finally, in a single separate plain-text sentence**, summarize what you did and the key conclusions you reached (this sentence is shown as the result of this run, so be sure to output it)."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{modelRuntime: newModelRuntime(prov, model, workDir, tx, window), maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	return w.modelRuntime.compactionWindow(&w.windowFn)
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.modelRuntime.setProxy(addr, caCert) }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.modelRuntime.setWebSearch(o) }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (section [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the intermediate-artifact
// output rules are NOT here — they are code-owned and appended by workerSystem after
// rendering (sections [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `You are the "executor" (work agent) in an authorized penetration-testing system on a cybersecurity platform. You are handed [one intent] (a one-sentence exploration direction); your sole duty is: **complete this one intent, write the findings back into the knowledge graph, then stop and return.**

**Boundaries (hard lines)**:
1. **Do only the one intent you were handed**. **If, while exploring this intent, you glimpse a lead worth digging into beyond it** (a path leaked by an error, a point that may link up with other assets, a suspected entry to another exploitation chain), **note it in one line in the fact's summary and hand it to the planner**.
2. Being blocked on the first try (payload filtered / 404 / injection with no echo) does not mean you have explored it fully — exhaust every bypass for this intent before stating a conclusion;
3. Operate only within the authorized scope. If [operation constraints] are attached at the top of the system prompt, those are the highest-priority hard line: self-check before executing each command/probe, and if it violates them, do not do it (even if it falls within the intent you were handed).

**Write back as you discover** (only what is written into the graph counts; what is in your head or in text does not; write each result immediately — do not save it up until the step budget runs out and it is lost). Three kinds of write-back; do not mix up the graphs:
- **New asset/resource → insert_assets (asset graph)**: subdomains / services / endpoints / fingerprints / credentials and every asset [itself]. **Only register assets here; do not write exploration conclusions/judgments here — use record_fact.**
- **Exploration conclusion/fact → record_fact (exploration graph, pass intent_id)**: use it for all of them. **Aggregate multiple observations into [a single] fact** (summary = one-sentence summary + detail = an expansion of that summary, grounded in the actual execution); do not write one per attribute — usually just one per intent. Splitting it up makes the graph balloon endlessly — **by default write one, and fold everything that can go into detail into it**; only when there truly are [fully independent, un-mergeable] conclusions should you split them into the facts array, which is a rare exception, not the norm. **Write only the delta**: record only what is [newly obtained] this time; do not reword and re-record an existing fact (if you only confirm an existing one with nothing new, there is no need to record it). **Write only what you actually saw**: provide evidence (one line: the command + the one or two output lines that best prove it, concise, with the rest in detail) and mark confidence (observed = saw directly / inferred = inferred from a phenomenon).
- **Confirmed vulnerability → report_finding (exploration graph, with PoC, pass intent_id)**: **use it only for something you actually triggered this time and for which you obtained reproducible evidence (request/response or command output)**. Never treat "version/fingerprint matched a CVE", "the parameter looks injectable", or "inferred from an external vulnerability database / changelog / code diff" as confirmed, and do not substitute querying a CVE database or comparing patch versions for an actual trigger. Cannot trigger it but have a suspicion → record an inferred fact with record_fact (the suspected point + why it was not triggered) and hand it to the planner; do not force it into a finding.


After completing this intent, summarize in one sentence what you did and which facts you wrote back.`

// workerTrafficBlock is section [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**Traffic tools**:\n- traffic_search / traffic_get / traffic_blob: review responses and find resources already visited. **Check the traffic first; do not curl the same URL again**. traffic_search **must specify host**, and by default returns only 3 ultra-lightweight index entries (id/method/url/status/resp_len, with no response body); raise limit explicitly if you need more; use body_contains to full-text search within request/response bodies (at least 3 characters, supports substrings and Chinese, e.g. to find passwords/keys/errors/internal addresses); to see the raw text of one entry use traffic_get(id), where an oversized body is shown as @blob sha256:<hash>, and use traffic_blob(hash) to fetch the full text in chunks.\n- traffic_match: search one captured request/response or its full blob by literal/RE2 pattern and receive only bounded match context with immutable traffic id and byte offsets. Use this instead of reading a giant one-line JavaScript body page by page.\n- traffic_routes: extract and deduplicate /api and /webapi candidates from one captured HTML/JavaScript response, including escaped slashes, occurrence counts, offsets, contexts and body hash. Run it early on large frontend bundles before guessing endpoints.\n- traffic_contract_worklist: search route/symbol leads across the bounded captured HTML/JavaScript corpus in one deterministic pass and return same-target JavaScript references split into captured_references and pending_fetches. Fetch only useful pending chunks with http_request, then rerun the worklist; every caller match remains tied to traffic id, body hash and byte offsets.\n- **Client-contract recovery**: when an endpoint exists but guessed requests fail, stop payload roulette. Run traffic_contract_worklist first with the exact route plus any adjacent symbol leads. Follow its pending_fetches with http_request and rerun it until the caller is found or search_complete shows the bounded corpus was covered. Use traffic_routes/traffic_match for focused follow-up on the returned exchange ids. Recover the adjacent fetch/axios/XHR method, Content-Type, body encoding, JSON.stringify/form/text handling and preconditions, then test the evidence-derived shape before concluding anything about the endpoint."
}

// workerExecutionDisciplineBlock is code-owned so an old/custom DB prompt cannot
// silently drop the anti-convergence and repeated-failure contract.
func workerExecutionDisciplineBlock() string {
	return "\n\n**Hypothesis and repetition discipline (mandatory)**:\n" +
		"- The intent payload may contain hypothesis_key, attack_role, avoid_hypothesis_keys, and avoid_attack_roles. Stay on both your hypothesis and role lane; the avoid lists name sibling mechanisms and operating roles already assigned to other workers. You may make a small control request outside the lane when strictly necessary, but do not turn it into another worker's investigation. Hand a cross-lane lead back as a fact instead.\n" +
		"- http_request identifies semantically repeated transport/HTTP failures and recognized HTTP-200 application errors for the same method+URL+request-body class. When repeat_stop=true, do not send another materially equivalent request. Safe GET/HEAD/OPTIONS directions may receive repeat_warning instead of a hard stop; map them only while they produce new evidence. A client-code-derived method/body/content-type contract is a genuine new mechanism; state that evidence in repeat_override_reason if an older stopped shape must be crossed. Renaming guessed fields or replaying credentials is not a new mechanism.\n" +
		"- A 4xx/5xx response closes only the exact request shape that produced it, never the endpoint or vulnerability class. Before declaring an HTTP/API direction exhausted, recover the expected method, content type, body encoding and preconditions from captured client code, an API schema, or a known-good request.\n" +
		"- External source code is a lead, not target provenance. Never project the current/default branch onto the deployed target or use it to claim that a target secret/configuration is random, fixed, patched, or vulnerable. First establish a target version/revision from runtime evidence, or compare the relevant behavior across bounded historical tags/releases and keep the conclusion inferred until a target request confirms it.\n" +
		"- record_fact enforces that rule for direction closure. provenance_status=unversioned_external can only be an observation. version_matched requires target_version_evidence, source_revision_evidence, and runtime_validation; target_observed requires runtime_validation; not_applicable requires a concrete provenance_note. If you cannot supply the required target-bound proof, keep closure_status=observation and list the provenance gap in unresolved_leads.\n" +
		"- For a bounded weak/default-secret hypothesis, derive candidates from observed product/config identifiers and include exact observed capitalization plus simple case and separator variants before pivoting. Do not expand this into brute force, and never describe failure of a candidate list as proof that the deployed key has high entropy.\n" +
		"- For any rejection/inability/no-vulnerability fact, record_fact must set negative=true; it defaults closure_status=observation and becomes a planner recovery signal. Use closure_status=exhausted only after the tool's structured proof requirements are met. If callers/bundles/request shapes remain unchecked, list them in unresolved_leads and hand them back to the planner.\n" +
		"- Two identical failures are not evidence that the whole target is safe; they only stop the same request-shape branch."
}

// artifactSpec is section [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**Intermediate-artifact output rules**: scripts, payloads, captured response bodies, temporary data, and every other intermediate artifact **must all be written to this task's working directory " + dir + "** (a relative path lands here, or you may use this absolute path) — **do not write to /tmp, do not use any other absolute path**."
}

// workerArtifactSpec is the worker's section [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**Intermediate-artifact output rules**: scripts, payloads, captured response bodies, temporary data, and every other intermediate artifact **must all be written to this intent's dedicated working directory " + runDir + "** (already created for you — just write here with relative paths, no need to create the directory yourself) — **do not write to /tmp, do not use any other absolute path**."
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerExecutionDisciplineBlock() + workerArtifactSpec(runDir)
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n[The intent you were handed (your only task this time: do just this one, produce only facts, and stop when done)]:\n%s\nIntent id: %d (pass it when writing back via record_fact / report_finding)", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage is a signal for the planner to judge "which categories are under-tested /
	// whether to widen scope"; it conflicts with the worker's duty boundary of "do only
	// the one intent handed to you, don't chase uncovered points" → strip it from the
	// worker's view. data is a fresh map owned by this worker, so deleting the key does
	// not affect the planner.
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n[Global exploration overview (read-only, to help you see your own intent in the big picture)]:\n" +
		"Below is the current exploration snapshot of the whole task. It has two uses: first, to know what others have already found so you don't repeat it; second, so that while exploring your own intent you can relate it to the global picture.\n" +
		"**Thinking broadly is good**: while exploring this intent, think deeply and make plenty of associations. The only boundary is — do not actually go off and execute other intents (that is other workers' job, scheduled by the planner). Whenever you associate a valuable lead (a cross-asset linkage, a suspected entry to another exploitation chain, a globally suspicious point), **be sure to write it into a fact and hand it to the planner** — this is an important output of yours, not optional. Better to report one extra for the planner to judge than to swallow it yourself.\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notify func(), notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notify, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notify func(), notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notify, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notify func(), notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotify(notify)               // graph-change edge for newly inserted assets
	tsx.SetNotifyFinding(notifyFinding) // wake the planner on the spot when report_finding is persisted, carrying "which intent + finding"
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// The worker deliberately omits MultiEdit/Glob/Grep: precise file edits use Edit, and
	// search goes through Bash (grep/find), narrowing the tool surface and cutting
	// low-value calls. The other SDK default tools (Read/Write/Edit/LS/Bash/Sleep) stay.
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	base = withWebFetch(base, w.proxyAddr, w.proxyCACert)
	base = appendAuditedResearchTools(base, w.webSearch)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID, AgentKey: name})
	var writes WriteCounts
	outcome := runAgentTurn(ctx, "worker", base,
		func(ctx context.Context, tools []actool.CoreTool, def DeferredInfo) preparedAgentTurn {

			// The intent is the worker's [sole duty, an invariant that spans the whole run] → it
			// goes into the system prompt together with the launch instruction and the raw data of
			// the intent-anchored target assets: the system prompt is reassembled every run and is
			// never compacted away, so the intent is always present in a long run, and continuation
			// does not depend on whether the transcript history kept that first message. The cost is
			// that per-intent volatile data gets mixed into system, losing cross-intent cache reuse;
			// this is a deliberate trade-off (losing the intent is far worse than saving tokens).
			// The divergence from the planner's "put the overview block in the user turn" is
			// intentional: the planner is itself the one producing intents and has no single mandate,
			// whereas the worker does. Only the [global overview] stays in the launch user message —
			// it is degradable, tolerates staleness, and can be compacted away harmlessly.
			// This intent's dedicated working directory <workDir>/tasks/<taskID>/i<intentID> is
			// created on the engine side first.
			runDir := ensureRunDir(w.workDir, taskID, intent.ID)
			// The run-wide intent is not the current tool action. Do not forward it or
			// inherit a parent run's background into the action reviewer.
			ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
			overview := renderWorkerGraphOverview(tsx.graphOverviewData())
			sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
			if w.wantConstraints() {
				sysBody += constraintBlock(ts) // operation constraints (if any) injected into the system prompt; the worker follows them strictly
			}
			// intent block → intent-anchored asset block → launch instruction, appended to the end of system in order (same append approach as constraintBlock).
			sysBody += renderIntentTask(intent)
			if as != nil {
				if ids := intentAssetIDs(intent); len(ids) > 0 {
					if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
						if b, err := json.Marshal(assets); err == nil {
							sysBody += "\n\nTarget assets corresponding to this intent's asset_ids:\n" + string(b)
						}
						// These assets the intent explicitly targets → automatically fold their domain
						// authority into task scope. Network assets stay linked evidence and require an
						// explicit authorization edge, matching insertAssets.
						// upsertTaskScope's ON CONFLICT DO NOTHING + the uq_task_scope unique index
						// guarantee no duplicate inserts; a re-run/retry is likewise an idempotent no-op.
						// When asset coverage is disabled, the test scope (the denominator) is no longer accumulated.
						if coverageEnabled {
							for _, a := range assets {
								_ = as.AddAutoScopeForDiscoveredAsset(taskID, a.Type, a.Domain, a.URL, a.IP)
							}
						}
					}
				}
			}
			sysBody += "\n\nStart executing the intent above: do only it, produce only facts, assets, and findings, and stop when done."
			system, boundary := deferredSystem(sysBody, def)
			// The task-level deadline (injected via ctx) clamps this run's wall-clock budget and decides the wrap-up wording (see taskclock.go).
			tc := taskClockFrom(ctx)
			maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
			settle := wrapupSettlement("worker", []string{"Bash"})
			if tc.DeadlineUnix > 0 {
				settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
			}
			opts := commonAgentOptions(w.prov, system, boundary, tools, def)
			opts.BashEnv = proxyEnv(w.proxyAddr, w.proxyCACert)
			opts.WorkingDir = runDir
			opts.MaxTurns = w.maxTurns // 0 = unlimited (configurable in agent management)
			// Wall-clock budget, checked at turn boundaries, never interrupting mid-way; 0 =
			// unlimited. When a task-level deadline exists, it is clamped to min(own budget,
			// time left to deadline) so the run naturally enters wrap-up when the task is due (see taskclock.go).
			opts.MaxDuration = maxDur
			// Hitting the budget (turns OR duration) → the SDK runs one wrap-up turn (hiding Bash), writing back what was identified to avoid a half-finished run.
			// When clamped (bounded by the task deadline), use PromptByReason: timeout = task is due → task-timeout wording;
			// max-turns = steps ran out first within the clamped window → fall back to the per-run wording. When not clamped, keep pure per-run.
			opts.Settlement = settle
			// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
			// full output preserved on disk. The truncation cap uses the SDK default (30000 characters).
			opts.ToolOutputDir = cmdOutDir(runDir)
			opts.Compaction = compactionConfig(w.compactionWindow()) // long tool-heavy runs stay within the window
			opts.Todos = actool.NewTodoStore()                       // session-scoped scratch todos (TodoWrite), planning only, dropped on exit
			w.modelRuntime.bindModel(&opts)
			if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
				opts.Hooks = hooks
			}
			w.modelRuntime.bindTranscript(&opts, func() { // persist raw LLM conversation; one file per worked intent
				opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
			})
			intentID := intent.ID
			emitWrap := activityInLane(emit, name, &intentID)
			// The intent / launch instruction / intent-anchored assets are already delivered with
			// the system prompt (see the sysBody assembly above). This launch user message carries
			// only the [global overview] — degradable big-picture context that is harmless to
			// compact away. In the rare case the overview marshals to empty, fall back to a one-line
			// launch message to avoid an empty user message on the first turn.
			input := overview
			if strings.TrimSpace(input) == "" {
				input = "Start executing the intent handed to you in system: do only it, produce only facts, assets, and findings, and stop when done."
			}

			// Experimental feature: once enabled, noa takes over context compression (archives are centralized under <workDir>/noa/<SessionID>, persistent).
			noaSession := WorkerSessionID(ts.ID(), intent.ID)
			enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))

			return preparedAgentTurn{
				ctx:     ctx,
				options: opts,
				input:   input,
				emit:    emitWrap,
				resume: func(s *agentcore.Session) string {

					// Resume prior conversation if this intent was paused/blocked/exhausted and is
					// being re-run. The transcript ID is deterministic per intent, so if a prior
					// session exists the worker continues from where it left off instead of
					// restarting from scratch.
					alreadyRecorded := false
					if w.tx != nil {
						_ = s.Resume(opts.SessionID)
						alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
						if len(s.Messages()) > 0 && message == "" {
							seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
							input = "Continue executing."
						} else if len(s.Messages()) > 0 {
							seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
						}
					}
					if message != "" {
						if alreadyRecorded {
							input = "Continue executing the new intent entered in the previous manual conversation. Do not repeat actions already completed."
						} else if len(s.Messages()) > 0 {
							input = workerChatMarker(requestID) + "\n[New intent entered via manual conversation]\n" + message +
								"\n\nExecute this manual input immediately; once done, decide from context whether the original task needs to continue."
						} else {
							input += "\n\n" + workerChatMarker(requestID) + "\n[New intent entered via manual conversation]\n" + message +
								"\n\nGive this manual input priority."
						}
					}

					// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
					// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
					// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
					// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
					// still settles (no external hard-timeout backstop needed). ctx itself carries only
					// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.

					return input
				},
			}
		}, func() { writes = tsx.Writes() })
	return outcome.reason, writes, outcome.err
}
