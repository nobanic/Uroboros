package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/internal/toolinput"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/db"
	acperm "github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if key, ok := p["hypothesis_key"]; ok && key != nil && key != "" {
			m["hypothesis_key"] = key
		}
		if stop, ok := p["stop_condition"]; ok && stop != nil && stop != "" {
			m["stop_condition"] = stop
		}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // upstream: which nodes this intent derived from (several facts may jointly produce one intent)
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // downstream: which facts/findings this intent produced
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet   bool
	Reason    string
	writes    WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "a human added N goals: …" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "a human added N strategic hints: …"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "facts:1 assets:25 findings:0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("facts:%d assets:%d findings:%d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that own the scope definition keep it either way — in lockstep with
// insertAssets' auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts != nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/custom agent/side-channel question). Refusing
// there keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " requires a task context (exploration graph): the current agent is not running inside a task, so the task's exploration graph is unavailable and this tool cannot be used. Use it inside a task, or switch to a cross-task read tool that takes task_id (get_task_node_detail / list_task_findings / get_task_graph, etc.)."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(Exploration graph) Distilled situational summary of the exploration: asset counts, sites with no endpoints, the frontier, findings, and hints (strategic hints from the human / main agent that must be taken into account when generating intents). Call this first when planning.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	return (graphOverviewRead{tools: t, store: t.ts}).document()
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	return (graphSourceReads{tools: t}).documents()
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "List the [confirmed vulnerabilities] of this task and its direct related tasks (compact: id+task_id+intent_id+vulnclass+severity+summary+state). Related-task entries carry source_task_id/inherited=true and are read-only. This contains vulnerabilities only; use list_facts for ordinary exploration facts and node_detail(id) for details.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> the intent id that produced it
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "Paginate the [exploration facts/conclusions] of this task and its direct related tasks, newest first (compact: id+summary+state; an over-long summary is truncated, full text via node_detail(id)). All parameters optional: limit (default 20, max 100), before (cursor — pass the next_before returned by the previous page to get an older page; omit/0 = newest page), q (filter by summary keyword). Returns {facts, total, has_more, next_before}: total is the filtered total; when has_more=true, continue paging with next_before. Related-task entries carry source_task_id/inherited=true and are read-only. For vulnerabilities see list_findings.",
		obj(map[string]any{
			"limit":  intp("Number of entries to return, default 20, max 100"),
			"before": intp("Pagination cursor: return only facts with an id smaller than this value (older); omit or 0 = newest page"),
			"q":      str("Filter by fact-summary keyword (case-insensitive); omit = no filter"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(a struct {
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
				Q      string `json:"q"`
			}) (actool.Result, error) {
				limit := a.Limit
				if limit <= 0 {
					limit = factsPageSize
				}
				if limit > 100 {
					limit = 100
				}
				f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
				if err != nil {
					return actool.Result{}, err
				}
				out := make([]map[string]any, 0, len(f))
				for _, n := range f {
					out = append(out, compactFact(n))
				}
				res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
				if hasMore && len(f) > 0 {
					res["next_before"] = f[len(f)-1].ID // pass it back to get the next (older) page
				}
				return jsonResult(res)

			})
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	var payload map[string]any
	if json.Unmarshal(n.Payload, &payload) == nil {
		addFactDecisionMetadata(m, payload)
	}
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

// addFactDecisionMetadata keeps the planner's compact views honest about what a
// fact can close. Legacy facts and new facts without an explicit status are
// observations, never proof that a direction was exhausted.
func addFactDecisionMetadata(dst, payload map[string]any) {
	if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
		dst["confidence"] = confidence
	}
	closure, _ := payload["closure_status"].(string)
	closure = strings.ToLower(strings.TrimSpace(closure))
	if closure != factClosureExhausted {
		closure = factClosureObservation
	}
	dst["closure_status"] = closure
	if negative, _ := payload["negative"].(bool); negative {
		dst["negative"] = true
	}
	if variants, ok := payload["tested_variants"].([]any); ok && len(variants) > 0 {
		dst["tested_variants_count"] = len(variants)
	}
	if leads, ok := payload["unresolved_leads"].([]any); ok && len(leads) > 0 {
		dst["unresolved_leads_count"] = len(leads)
	}
	if evidence, ok := payload["contract_evidence"].(string); ok && strings.TrimSpace(evidence) != "" {
		dst["contract_evidence_present"] = true
	}
	if provenance, ok := payload["provenance_status"].(string); ok && strings.TrimSpace(provenance) != "" {
		dst["provenance_status"] = strings.ToLower(strings.TrimSpace(provenance))
	}
	if version, ok := payload["target_version_evidence"].(string); ok && strings.TrimSpace(version) != "" {
		dst["target_version_evidence_present"] = true
	}
	if revision, ok := payload["source_revision_evidence"].(string); ok && strings.TrimSpace(revision) != "" {
		dst["source_revision_evidence_present"] = true
	}
	if validation, ok := payload["runtime_validation"].(string); ok && strings.TrimSpace(validation) != "" {
		dst["runtime_validation_present"] = true
	}
}

func factNeedsRecovery(payload map[string]any, intentState string) bool {
	switch intentState {
	case "done", "blocked", "exhausted", "stopped":
	default:
		return false
	}
	negative, _ := payload["negative"].(bool)
	closure, _ := payload["closure_status"].(string)
	return negative && !strings.EqualFold(strings.TrimSpace(closure), factClosureExhausted)
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "Fetch by id the full content of an [exploration-graph node] of this task or a direct related task. Inherited nodes carry source_task_id/inherited=true and are read-only. Only for exploration-node ids returned by list_facts/list_findings/graph_overview; for assets use list_assets/asset_neighbors.",
		obj(map[string]any{"id": idp("exploration-graph node id (not an asset id)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(a struct {
				ID json.RawMessage `json:"id"`
			}) (actool.Result, error) {
				id := pid(a.ID)
				if id <= 0 {
					return actool.Errorf("id is required"), nil
				}
				n, err := t.ts.GetNodeWithSources(id)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if n == nil {
					return actool.Errorf(fmt.Sprintf("Exploration node %d not found. If you meant an asset, use list_assets / asset_neighbors (assets and exploration nodes are different id spaces; an asset id cannot be passed to node_detail).", id)), nil
				}
				if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs

			})
		})
}

// --- planner write tools ---

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "List this task's goal nodes and their state (open/met), used to judge whether the goal is achieved.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "Terminate one running intent (work). Use it to stop a derailed/pointless exploration; the terminated intent is marked stopped and is not auto-reclaimed. First use get_worker_output to see what it is doing before deciding.",
		obj(map[string]any{"intent_id": idp("the id of the intent to terminate (= the work handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work is currently unavailable"), nil
			}
			return toolinput.Partial(in, func(a intentHandleInput) (actool.Result, error) {
				id := pid(a.IntentID)
				if id <= 0 {
					return actool.Errorf("intent_id is required"), nil
				}
				node, err := t.ts.GetNode(id)
				if err != nil || node == nil || node.Kind != db.KindIntent {
					return actool.Errorf("intent_id must be an intent of this task (related-task intents are read-only)"), nil
				}
				if err := t.killWork(id); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text(fmt.Sprintf("sent a termination signal to the work of intent %d", id)), nil

			})
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("stop doing X, focus on Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "Inject a real-time course-correction into a running intent (work) without interrupting it or losing existing progress: the worker receives your instruction before its next action and adjusts accordingly. Use it for [within-intent] corrections like 'stop going down X, focus on Y'; if the whole direction is wrong, use kill_work and then issue a new intent. It is recommended to use get_worker_output first to see what it is doing.",
		obj(map[string]any{
			"intent_id": idp("intent id to course-correct (= the work handle)"),
			"message":   str("the course-correction instruction for the worker, clearly telling it what to stop and what to switch to"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work is currently unavailable"), nil
			}
			return toolinput.Partial(in, func(a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}) (actool.Result, error) {
				id := pid(a.IntentID)
				if id <= 0 {
					return actool.Errorf("intent_id must be positive"), nil
				}
				if strings.TrimSpace(a.Message) == "" {
					return actool.Errorf("message is required"), nil
				}
				if err := t.steerWork(id, a.Message); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text(fmt.Sprintf("injected a course-correction into the work of intent %d (takes effect on the next step)", id)), nil

			})
		})
}

func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "Fetch the final output conclusion of one intent (work) of this task or a directly related task. Related-task results carry source_task_id/inherited=true and are read-only. On normal completion it returns its summary; a terminated (stopped)/abnormal work returns its last output as of when it aborted.",
		obj(map[string]any{"intent_id": idp("intent id (= the work handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(a intentHandleInput) (actool.Result, error) {
				id := pid(a.IntentID)
				if id <= 0 {
					return actool.Errorf("intent_id is required"), nil
				}
				intentNode, err := t.ts.GetNodeWithSources(id)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if intentNode == nil || intentNode.Kind != db.KindIntent {
					return actool.Errorf("intent_id does not belong to this task or its directly related tasks"), nil
				}
				acts, _, err := t.ts.ActivityListWithSources(id, 0, 1000)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				var chosen, fallback *db.Activity
				for i := range acts {
					switch acts[i].Kind {
					case "result":
						chosen = &acts[i]
						fallback = &acts[i]
					case "text":
						fallback = &acts[i]
					}
				}
				pick := chosen
				if pick == nil {
					pick = fallback
				}
				if pick == nil {
					if intentNode.Inherited {
						return jsonResult(inheritedMap(map[string]any{
							"intent_id": id, "final_text": "(this work has produced no output yet)",
						}, intentNode.SourceTaskID))
					}
					return actool.Text("(this work has produced no output yet)"), nil
				}
				detail, _ := t.ts.ActivityDetailWithSources(pick.ID)
				if detail == "" {
					detail = pick.Summary
				}
				result := map[string]any{
					"intent_id": id, "final_text": detail,
					"summary": pick.Summary, "is_error": pick.IsError,
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)

			})
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"View the [execution process] of one intent (work) (as opposed to get_worker_output, which gives only the final conclusion). Three usages:\n"+
			"① Pass only intent_id → returns the summary stream of every step of this work (summary ≤100 chars, includes step_id; just the action outline, no full output);\n"+
			"② intent_id + q → returns only the step summaries matching the keyword (searches both summaries and full output; still only summary — to see content use ③);\n"+
			"③ intent_id + step_ids → returns the full content (detail) of those steps; at most 5 at a time, beyond which only the first 5 are returned and the untaken ones are noted in notice/omitted_step_ids.\n"+
			"Typical flow: first use ①/② to locate the step_id of a suspicious step, then use ③ to fetch its full output. Thinking steps are excluded. Historical traces of directly related tasks are supported; their results carry source_task_id/inherited=true and are read-only.",
		obj(map[string]any{
			"intent_id": idp("intent id (= the work handle)"),
			"q":         str("keyword: return only steps whose summary/full output matches it (optional; mutually exclusive with step_ids)"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "the step_ids to fetch full content for (from the ①/② return; at most 5 at a time; if more are passed only the first 5 are returned and the rest are listed in omitted_step_ids)"},
			"limit":     intp("return cap for the summary stream / search (optional)"),
		}, "intent_id"),
		(workerTraceRead{tools: t}).trace)
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"[Usually not recommended, because the system already provides most of the information] Search by keyword (q) within [the execution process of other works in this task] — to recover something a worker saw but never wrote into a fact (a path/token/error, etc.)."+
			"Steps of your own intent are automatically excluded (those are already in your context)."+
			"Returns only the summaries of matching steps (summary ≤100 chars), each with its intent_id; then use get_worker_trace(intent_id, step_ids=[...]) to fetch the full content.",
		obj(map[string]any{
			"q":     str("keyword (searched in all works' step summaries + full output)"),
			"limit": intp("return cap, default 100 (optional)"),
		}, "q"),
		(workerTraceRead{tools: t}).search)
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"[Usually not recommended, because the system already provides most of the information] List an index of [the works (intents) that have already run] in this task: intent_id + one-sentence direction (summary) + state."+
			"You (the worker) cannot see the exploration graph, so use it to discover which works are worth reviewing — then use get_worker_trace(intent_id) to see their steps and get_worker_trace(intent_id, step_ids=[...]) to fetch details."+
			"Lists only those that have run (running/done/exhausted/blocked/stopped), not the open ones not yet run. Note: your task boundary is still the one intent you were handed; looking at other works is only to reuse observations / avoid duplicate work.",
		obj(map[string]any{
			"q":     str("filter by summary keyword (optional)"),
			"limit": intp("return cap, default 50 (optional)"),
		}),
		(workerTraceRead{tools: t}).index)
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding: when the planner confirms a vulnerability itself during situational assessment, it can record it directly (the same tool as the worker).
		t.addFinding(),
		// list_companies: view the company list + scope + asset count (to get company_id / understand ownership scope).
		t.listCompanies(),
		// list_assets: during planning, search the whole asset store by DSL (complementing list_untested_assets' "untested within scope" view,
		// adding the ability to "query the whole store by domain/fingerprint/port/status-code conditions").
		t.listAssets(),
		// add_company_scope: during planning, add domains/IPs/CIDRs/ICPs/keywords into a company's asset scope (auto-claiming matching assets).
		t.addCompanyScope(),
		// add_task_scope: proactively add a whole root domain / whole company / a subdomain / IP into this task's test scope (the coverage denominator).
		t.addTaskScope(),
		// list_untested_assets: query untested assets within this task's scope on demand (by type + paginated), and decide for yourself what to test.
		t.listUntestedAssets(),
	}
}
