package server

import (
	"context"
	"encoding/json"
	"log"

	"github.com/nobanic/UroborOS/agent"
	actool "github.com/nobanic/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	return (toolResult{value: v}).json()
}

// This file implements P2 'cross-task orchestration toolset' (docs/benchmark-orchestration §2 P2). These are host tools -- they need
// access to the Manager (any task's Store), the Engine (pause), and the task-creation flow, so they live in the server layer.
// read tools redirect the 'existing per-task tools' to run against the target task's store (building a temporary ToolSet ToolSet
// and Calling the corresponding tool), thus reusing exactly the same logic; control tools (spawn/pause) call the Manager/Engine directly.
// like traffic tools, they are seeded into the tools table and bound per agent (only bound to the orchestration agent to be visible).

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	return (orchestrationHost{server: s}).available()
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return (orchestrationHost{server: s}).catalog()
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return (stringArgument{description: desc}).schema()
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	return (profileArgument{raw: raw}).id()
}

func objSchema(props map[string]any, required ...string) map[string]any {
	return (objectArguments{properties: props, required: required}).schema()
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return (orchestrationTool{name: name, description: desc, schema: schema, run: run}).build(true)
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return (orchestrationTool{name: name, description: desc, schema: schema, run: run}).build(false)
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	return (orchestrationHost{server: s}).delegate(ctx, in, pick)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return (orchestrationTasks{server: s}).list()
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return (orchestrationTasks{server: s}).profiles()
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return (orchestrationTasks{server: s}).spawn()
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return (orchestrationTasks{server: s}).pause()
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return (orchestrationTasks{server: s}).graph()
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return (orchestrationTasks{server: s}).findings()
}

func (s *Server) toolAddHint() actool.CoreTool {
	return (orchestrationTasks{server: s}).hint()
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return (orchestrationTasks{server: s}).workerTrace()
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return (orchestrationTasks{server: s}).workerTraces()
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return (orchestrationTasks{server: s}).searchTraces()
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return (orchestrationTasks{server: s}).node()
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return (orchestrationTasks{server: s}).report()
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	return (orchestrationHost{server: s}).status(t)
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	(orchestrationSeed{server: s}).all()
}

// refreshHypothesisRepeatAndFactSchemas upgrades existing installations without
// overwriting operator bindings or enabled flags. These tools are seeded with
// first-insert semantics, so newly required model-facing fields and descriptions
// need an explicit one-time schema refresh.
func (s *Server) refreshHypothesisRepeatAndFactSchemas(requestTool actool.CoreTool) {
	(orchestrationSeed{server: s}).closure(requestTool)
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. spawn_task's llm_profile) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	(orchestrationSeed{server: s}).defaults()
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	(orchestrationSeed{server: s}).removeGoalMet()
}

// reseedGoalsPrompt refreshes the goals decomposer's prompt to the [current code default] -- because the default body added
// the step 'extract operation constraints (set_constraints) first, then decompose goals'; but SeedPromptIfEmpty is first-insert only, so old DBs'
// existing version 1 would not get this step. Here we use version management to [append a new version] and switch to it (ResetPromptToDefault),(ResetPromptToDefault),
// the old version stays in history; if the user customized it, they can recover it from the version record. A settings flag guards it -> done only once;;
// if the default changes again later, bump this flag. Fresh DBs need no handling (SeedPromptIfEmpty already seeded the latest default).
func (s *Server) reseedGoalsPrompt() {
	(orchestrationPrompt{server: s, flag: "goals_prompt_constraint_step_v1", key: "goals", failure: func(err error) { log.Printf("[prompts] goals prompt refresh to new default failed: %v", err) }, success: func() {
		log.Printf("[prompts] goals prompt: appended a new default version (added the extract-operation-constraints step, one-time))")
	}}).refresh()
}

// reseedMainAgentPrompt refreshes the mainagent prompt to the [current code default] -- the default body added 'after all goals
// are met, when add_intent posts an intent directly, ask the human whether to register it as a formal goal' guidance; but SeedPromptIfEmpty is first-insert
// only,only, so old DBs' existing versions miss it. Use version management to [append a new version] and switch to it (ResetPromptToDefault); the old version still
// stays in history; if the user customized it, they can recover it from the version record. A settings flag guards it -> done only once. Fresh DBs need no handling
// (SeedPromptIfEmpty already seeded the latest default). Structurally identical to reseedGoalsPrompt.
func (s *Server) reseedMainAgentPrompt() {
	(orchestrationPrompt{server: s, flag: "mainagent_prompt_goalless_intent_v1", key: "mainagent", failure: func(err error) { log.Printf("[prompts] mainagent prompt refresh to new default failed: %v", err) }, success: func() {
		log.Printf("[prompts] mainagent prompt: appended a new default version (added the post-goal-completion prompt to create a goal, one-time))")
	}}).refresh()
}

// reseedPlannerPrompt refreshes the planner prompt to the [current code default] -- the default body was streamlined and refactored, and 'restraint' was downgraded to
// dedup only; added 'depth over coverage', 'hard floor: if goals are unmet and no intent is running, one must be produced', and an upper bound on negative-conclusion re-checks.
// whenever the default changes substantively, bump the flag below (currently v2) so existing old DBs refresh again. SeedPromptIfEmpty is first-insert only; old DBs' existing versions miss it, so we use version management
// to [append a new version] and switch to it (ResetPromptToDefault); the old version stays in history, and if the user customized it, from the version record
// they can recover it. A settings flag guards it -> done only once. Fresh DBs need no handling (SeedPromptIfEmpty already seeded the latest default). Like
// reseedGoalsPrompt reseedGoalsPrompt, it is structurally identical.
func (s *Server) reseedPlannerPrompt() {
	(orchestrationPrompt{server: s, flag: "planner_prompt_compact_realistic_v2", key: "planner", failure: func(err error) { log.Printf("[prompts] planner prompt refresh to new default failed: %v", err) }, success: func() {
		log.Printf("[prompts] planner prompt: appended a new default version (streamlined refactor + restraint downgraded to dedup + depth-first + negative-conclusion review upper bound, one-time))")
	}}).refresh()
}

// reseedWorkerPrompt refreshes the worker prompt to the [current code default] -- the default body's record_fact section removed the whole sentence 'for negative conclusions
// write an observation + a tentative reading', and decoupled confidence(observed/inferred) from 'whether this intent's means are exhausted' (these easily mislead the planner),),
// also tightened the facts array's per-item rule to the rare exceptions that are 'completely independent and cannot be merged'. Bump flag to v3 so existing old DBs refresh again.
// SeedPromptIfEmpty is first-insert only; old DBs' existing versions miss it, so we use version management to [append a new version] and switch to it; the old version stays in history and can be recovered.
// settings flag guards it -> done only once. Fresh DBs need no handling. Structurally identical to reseedGoalsPrompt.
func (s *Server) reseedWorkerPrompt() {
	(orchestrationPrompt{server: s, flag: "worker_prompt_compact_v4", key: "worker", failure: func(err error) { log.Printf("[prompts] worker prompt refresh to new default failed: %v", err) }, success: func() {
		log.Printf("[prompts] worker prompt: appended a new default version (the context-lookup section narrowed to list_assets/list_findings, dropped list_facts/node_detail/asset_neighbors, one-time))")
	}}).refresh()
}

// reporterToolCallMessage must unconditionally require reading get_finding_traffic once before writing the report.
// that tool is read-only and 'does not depend on the capture switch'; whether auto-binding is on or off, it can read manually-bound evidence. If here
// we wrote 'read only when auto-binding is enabled', then under the default-off configuration reporter would not pass evidence_version, evidence_version,
// SetFindingReportVersionByNodeID would then write -1 per legacy semantics, and the vulnerability details and Markdown export
// would permanently show 'evidence changed, report pending update', with no entry point in the UI to clear it.
const reporterToolCallMessage = "A vulnerability was just registered via report_finding. Read finding_id (the standalone vulnerability-record ID) and finding_node_id (the exploration-node ID) from the returned JSON, " +
	"first use get_finding_traffic(finding_id) to read the current evidence list and its version (an empty list is normal; write the report as usual); " +
	"if the run guidance enables auto-binding, verify and associate this vulnerability's traffic before reading. Use finding_node_id for node details. " +
	"finally call update_finding_report(finding_id=finding_node_id, report, evidence_version=the version you actually read) to save, " +
	"evidence_version must be passed; otherwise the report is permanently marked as pending update. Do not mix the two numbering schemes."

// seedReporterAgent pre-provisions a 'Report Writing' custom agent (builtin=false, editable/deletable in the UI):
// binds update_finding_report + task-query tools, and attaches a 'triggers when report_finding is called'
// trigger -- each registered vulnerability wakes it to write a detailed report. One-time (guarded by a settings flag): once the user deletes it, it is not recreated.
// dependency: the orchestration tools were already stored above in this function via SeedTool, so the bindings succeed.
func (s *Server) seedReporterAgent() {
	(orchestrationSeed{server: s}).reporter()
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	(orchestrationSeed{server: s}).autoFinding()
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	(orchestrationSeed{server: s}).plannerFinding()
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	(orchestrationSeed{server: s}).plannerAssets()
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	(orchestrationSeed{server: s}).companyScope()
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	(orchestrationSeed{server: s}).workerReads()
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	(orchestrationSeed{server: s}).workerReadback()
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	(orchestrationSeed{server: s}).autoDefaults()
}
