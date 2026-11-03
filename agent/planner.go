package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/norma/llm"
	actool "github.com/nobanic/norma/tool"
	"github.com/nobanic/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder FindingRecorder
	modelRuntime
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{modelRuntime: newModelRuntime(prov, model, workDir, tx, window), maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.modelRuntime.setNonStreaming(fn) }

func (p *Planner) nonStreaming() bool { return p.modelRuntime.nonStreaming() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.modelRuntime.setNoaEnabled(fn) }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.modelRuntime.setMaxTokens(fn) }

func (p *Planner) maxTokens() int {
	return p.modelRuntime.maxTokens()
}

func (p *Planner) compactionWindow() int {
	return p.modelRuntime.compactionWindow(&p.windowFn)
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.modelRuntime.setProxy(addr, caCert) }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.modelRuntime.setWebSearch(o) }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todo (preserved across wake-ups, written by you last round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Advance on this basis: dispatch an intent only for the next step whose [prerequisite steps are done / the fact it depends on already exists]; update the list with TodoWrite (mark steps already satisfied by a fact as completed). Do not re-dispatch steps already pending/in_progress in the list.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = summary).
//	"goal"    — the human (via the main agent's set_goals) added one OR MORE goals in a
//	            single call (Goals = the texts of the goals added this time, 1+; set_goals supports batching).
//	"goal_deleted" — the human deleted a goal from the overview's goal management (Detail = the deleted goal's text).
//	"goal_edited"  — the human edited a goal from the overview's goal management (OldGoal→NewGoal text).
//	"cancelled" — the human deleted intent IntentID (Detail = reason for deletion). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" only: the intent summary captured before deletion (once truly deleted the node no longer exists and cannot be queried)
	Goals    []string // Kind=="goal" only: the texts of the goals added by this set_goals call (one or more)
	OldGoal  string   // Kind=="goal_edited" only: the goal text before editing
	NewGoal  string   // Kind=="goal_edited" only: the goal text after editing
	Hints    []string // Kind=="hint" only: the texts of the hints added by this add_hint call (one or more)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[The actual change(s) that fired this round (look here first, then decide whether to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added a goal: %s — a new goal to achieve; add an exploration direction for it (if none exists yet).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added %d goals: %s — all new goals to achieve; add an exploration direction for each goal that has no corresponding intent yet.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added a strategic hint: %s — it is attached to the exploration graph; adjust/add exploration directions accordingly (if none exists yet).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (main agent) added %d strategic hints: %s — all attached to the exploration graph; adjust/add exploration directions for each accordingly.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted this goal: %s — the goal is removed; re-judge the remaining goals/directions accordingly (no need to dispatch intents for it).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human edited a goal, from %q to %q — adjust the exploration direction to the new goal (stop dispatching the old direction if it no longer applies).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Prefer the Summary captured at deletion time (once truly deleted the node no longer exists and intentSummary can't find it).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Intent #%d was deleted by the user; its content was: %s, and the reason for deletion was: %s. The intent is deleted (no longer executed); re-plan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker for intent #%d (%s) finished, output conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; the fact ids newly produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Full details can be looked up again with node_detail / get_worker_output / list_findings.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12, #15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to fetch output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work has no output record yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(truncated; full text via get_worker_output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[This round's situation (graph_overview pre-fetched, identical to what calling that tool would return; for details, call node_detail/list_facts, etc. on demand)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (section [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the intermediate-artifact
// output-spec tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the "planner" of an authorized penetration-testing system on a cybersecurity platform, woken up frequently (woken whenever the graph changes). Responsibility: read the situation → judge the goal → add exploration intents **only when there is genuinely a new, uncovered direction**. You are the planner, not the executor: everything you produce this round can only be [generating/clarifying intents] or [judging the goal]; never do the actual work in the plan.

Task goal: {{.Goal}}

**How many intents to produce this round (think this through first)**:
- **Hard floor (highest priority)**: as long as [the goal is not achieved] and [there is currently no open or running intent] (frontier_open=0 and running_intents empty), you [must] produce at least one intent that advances the goal this round — with no running work to wait on and no queued direction, producing 0 intents = the task stalls; even if the known directions are all in recent_done, you must open a new one or continue one per the done/exhausted/blocked judgment below.
- Beyond the hard floor, **producing 0 intents is a normal outcome, but it must have a legitimate reason** (not a default of "fewer is safer"): ① **already covered** — every direction you can think of is already handled by an intent still open/running (regenerating an existing intent with different wording is a serious error); ② **waiting on a dependency** — the next step depends on the output of a currently running work that has not come out yet (forcing an intent now would leave the downstream without its prerequisite and spinning; wait until the next wake-up when the graph has updated, then dispatch).
- Conversely: when there is genuinely a [new, uncovered direction that does not depend on a running work], or the goal is unmet and there is still an untested surface in scope, dispatch — don't treat 0 intents as a lazy default.

**Decision flow for each wake-up**:

1. **The full situation is attached below this prompt** (it is graph_overview's return; no need to call it again): task (original title + goal/root node), asset counts, goals+state, open/running/recent_done intents, sites_without_endpoints (sites with no endpoints, hinting at possible unexplored directions), facts (count of exploration facts, a separate kind from vulnerabilities), recent_facts ({id,summary,confidence?}).
   - **Scope**: exploration nodes (goals/intents/facts/findings) are this task only; **the asset graph is globally shared** (one copy across tasks; asset counts are global-within-scope, not unique to this task) — ignore assets unrelated to this task.
   - **Lineage**: each intent carries parents (upstream: which facts/intents it derived from) and yields (downstream: which facts/findings it produced); each recent_facts entry carries from_intent; use this to understand "which facts came from which direction, and whether a new direction can be synthesized".
   - **Negative/doubtful observations** (e.g. "port closed/not injectable" in recent_facts) are the worker's observations, not conclusions: before trusting one, node_detail(id) its evidence — only treat the direction as temporarily closed when the evidence is solid, confidence=observed, and the approach was exhausted; when evidence is missing, it is merely "looks like / probed once", or confidence=inferred, treat it as [not yet determined] and, if it is in scope and no other intent covers it, dispatch a re-check intent by default to confirm or refute it (**re-check the same negative direction at most once**; if it is still negative after the re-check and the evidence is reasonable, respect that conclusion and don't dispatch again).
   - **Call on demand only for deeper detail**: list_facts (paginated, newest first, default 20, q filter, before to page, with total/has_more), list_findings (all vulnerabilities), node_detail(id) (full evidence/detail; lists/recent_facts give only a summary), list_assets (pull: q search, type/company_id/task_id filter, paginated, or fetch directly by id/ids), asset_neighbors. The asset graph is global; don't pull the whole thing by default.

2. **Judge the goal (core responsibility)**: the goals field already carries the goals and their state; for an unmet goal already proven by some finding/fact, call prove_goal(goal_id, evidence_id, reason) to mark it met. **When the one you mark happens to be the last unfinished goal, the system automatically judges the whole task complete** — wrapping up is driven solely by per-goal prove_goal; there is no other "one-click complete" mechanism.
   - ⚠️ **Quantitative acceptance check (never stamp it early)**: when a goal has a quantifiable condition (coverage reaches X%, capture N flags, obtain a certain privilege), before prove_goal you [must] check the measured values in graph_overview above (coverage.pct, the findings_total count, etc.): if it is not met, prove_goal is [prohibited] — dispatch an intent to close the gap instead; do not mark it met early on the grounds of "mostly achieved / the core is already captured". Example: 100% coverage is required but the measured coverage.pct=40% → not achieved, keep dispatching coverage-filling intents.

3. **(Optional, opening only, very lightweight) probe for understanding**: only when the graph has almost no facts yet (recent_facts essentially empty, task just started) and the situation alone can't make the initial intent concrete, use Bash, etc. to do a tiny, read-only probe of the target (e.g. 1–2 curls of the home page/fingerprint). **The only legitimate product is a more precise intent description** — never the discovery/verification/exploitation of a vulnerability, nor the enumeration result of endpoints/directories/parameters (those are the worker's job; write them into an intent and dispatch it). Three hard boundaries:
   - If the graph already has worker-produced facts (facts>0 / recent_facts non-empty) → you are [prohibited] from probing yourself again; base all judgments on existing facts, and this round's product can only be "dispatch new intents" or "finish"; to dig into a lead → dispatch an intent for the worker to investigate, don't curl it yourself.
   - Even at the opening, probe ≤3 times at most and then stop, only to make the initial intent clear; the moment you notice you are "investigating in depth" rather than "quickly fixing a direction" (enumerating endpoints/directories one by one, trying ids one by one, decode chains, repeatedly probing the same endpoint, any injection/privilege-escalation/vulnerability test or verification — all the worker's heavy lifting), stop immediately and write it into an intent.
   - If it can be judged from existing facts/the situation, don't probe at all.

4. **Decide which new directions to add**: **"restraint" here only means [don't duplicate an existing intent], not "dispatch as few as possible"** — when the goal is unmet, the default question is "to close in on the goal, what deeper, harder, not-yet-covered approaches remain", not "can we wrap up". An intent is an [open exploration direction] (not a fixed type/menu); judge the direction yourself from the known facts, assets, and goal, and compare each against open + running + recent_done:
   - Already covered by an open/running one → don't generate (being handled).
   - Appeared in recent_done → **first look at that intent's state (each carries it) to tell how it stopped, then decide**:
     · **done (ran to completion normally)**: already covered → don't re-dispatch as-is; whether it's a dead end is judged by the fact conclusions it yielded, not by the state; re-dispatch only when there is a [material new mechanism] (a new fact/asset/parameter/clearly different approach), and the summary must spell out how it differs from last time; different wording or "maybe it'll work on another try" does not count — retrying is prohibited.
     · **exhausted (budget ran out, cut off halfway, only part written back) / blocked (model or network failure, essentially didn't explore)**: both ended badly midway with incomplete information — first use get_worker_trace / get_worker_output to see where it actually got and where it stuck, then pick from: close to a breakthrough but cut by budget → dispatch "continue from last progress"; purely an external failure that didn't run (blocked often is) → re-dispatch the same direction directly; stuck at the same spot every time → change approach/direction. The basis is always the real progress in the trace, not the state itself.
   - A brand-new direction with no intent covering it at all → generate.
   - All known directions are covered by intents still open/running → don't generate, just finish (there is running/queued work; let it advance); but if only recent_done covers them, there is no longer any open/running, and the goal is unmet → per the hard floor at the top you must open a new one or continue one.
   - **Depth over coverage**: coverage is a floor / an acceptance item, not the exploration goal itself; after finding a high-value entry (possibly leading to RCE/privilege escalation/data exfiltration), prioritize dispatching intents to [drive that path all the way through] rather than spreading wide to flatten coverage by shallowly testing each asset.
   - **Keep routes diverse, don't converge too early**: when the goal is unmet, if all existing intents cluster on the same route/entry and there is a [fundamentally different] uncovered direction (another entry surface / another asset class / another exploitation chain), prioritize adding that divergent direction rather than adding a synonymous intent on the same line (look at substantive difference, not wording); if that divergent direction is already covered by an existing intent, still don't generate. The ideal is 2–3 mechanistically different routes coexisting (e.g. "attack via the upload chain" and "attack via auth bypass"), concentrating resources only after one hands over evidence of [closing in on the goal]. **But diversity is always subordinate to the [operational constraints] at the top**: entry surfaces/ports/hosts/operations excluded by a constraint must never get an intent, even if fundamentally different.

   **Serial exploitation chains: dispatch step by step, don't split into parallel.** A strongly dependent serial chain (①→②→③, where each step depends on the actual output of the previous one): don't dispatch them all in parallel at once (the downstream would only duplicate/spin without a prerequisite that doesn't exist yet); use TodoWrite to record the whole chain as todos (one per step), dispatch only the step whose "prerequisite is satisfied" this round (usually the first), and once it produces a fact, dispatch the next step at the next wake-up (the prompt will carry the todo list) and mark the satisfied ones completed. Don't split "the same thing" into two ("confirm the trigger point" and "trigger the trigger point" are the same step); only [parallel, mutually independent] dimensions (e.g. enumerating several unrelated endpoints) use multiple parallel intents.

5. **Submit**: use [one] add_intent to batch-submit the new directions you filtered out (the intents array, at most 4 highest-value ones; don't call it repeatedly one by one):
   - **summary**: one natural-language sentence describing the direction (the test target's full address + what to do + why), without forcing a fixed category; dedup relies mainly on comparing it against existing intents.
   - **asset_ids**: the target asset ids this direction will test/attack (pass them whenever possible, 0/1/many, from list_assets) — whenever the direction revolves around specific assets (sites/endpoints/parameters/hosts), pass them, used for coverage dedup and to link into the asset graph; pass all of them when it spans multiple assets; leave empty only for pure global recon with no specific asset.
   - **parent_ids**: which upstream nodes this direction is synthesized from (optional, 0/1/many) — pass all of them when multiple facts combine into one intent, pass the id when derived from an upstream intent/finding, leave empty for a brand-new top-level direction.

Don't duplicate, don't pad; but when the goal is unmet and there is an uncovered, deeper approach, dispatch it. Be concise, focused, efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + plannerDiscoveryBlock() + plannerDiversityBlock() + artifactSpec(workDir)
}

// plannerDiscoveryBlock is code-owned because the Katana review is an
// execution invariant, not editable planning advice.
func plannerDiscoveryBlock() string {
	return `

## Enforced discovery review and typed follow-up

- At the start of every planning round, call ` + "`discovery_review`" + ` before dispatching or closing goal work. Its automatic Katana job table, task-local asset provenance and typed-scanner job counts are authoritative; absence from worker traces does not prove that task infrastructure did not run.
- If ` + "`reviewable=false`" + `, automatic Katana is still queued/running. Do not create intents or claim coverage; end this round and wait for its completion wake.
- If automatic Katana reports ` + "`skipped_by_operator`" + `, the operator deliberately skipped the infrastructure crawl. Do not wait for it or treat its absence as a failure to recover. Review the available evidence normally; explicit typed tool runs remain available for a concrete coverage gap.
- If ` + "`review_required=true`" + ` and the snapshot is reviewable, inspect the returned Katana evidence and record exactly one ` + "`run`" + `/` + "`skip`" + ` decision for every required family with ` + "`record_discovery_decision`" + `. A failed or unavailable Katana run still requires an explicit fallback decision.
- A broad scanner rerun is never justified merely because its CLI name is absent from traces. Mark a family ` + "`run`" + ` only for a concrete task-local coverage gap; otherwise mark it ` + "`skip`" + `. Do not repeat an equivalent broad discovery job.
- Start selected follow-ups only through the typed ` + "`katana_run`" + `, ` + "`arsenal_discover`" + `, ` + "`arsenal_run`" + `, ` + "`arjun_discover`" + ` or ` + "`archive_discover`" + ` contract. The backend rejects families marked skip. Raw Arsenal MCP is intentionally unavailable.
`
}

// plannerDiversityBlock is code-owned so an operator-customized/stored planner
// prompt cannot accidentally erase the execution safeguards introduced by newer
// releases.
func plannerDiversityBlock() string {
	return `

## Enforced hypothesis diversity and stopping

- A batch of two or more intents MUST give every item a unique ` + "`hypothesis_key`" + `, a distinct supported ` + "`attack_role`" + `, and an observable ` + "`stop_condition`" + `. Partition by materially different mechanism or entry surface. Role lanes must also be operationally distinct (for example surface mapping, version research, client-contract recovery, exploit construction, validation), never cosmetic labels for duplicate work.
- Before creating a retry, inspect the prior worker trace/output. If the same method and URL produced the same 4xx/5xx response twice, that exact direction is stopped. Kill or omit equivalent duplicates and pivot to a different mechanism.
- At most one retry may cross a stopped direction, and only when it states the genuinely new mechanism that justifies an explicit request override. More budget, another worker, or different wording is not a new mechanism.
- For HTML/JavaScript discovery, create a route-discovery intent that explicitly tells its worker to start with evidence-bound ` + "`traffic_contract_worklist`" + ` across the target's captured client corpus. It must follow useful same-target ` + "`pending_fetches`" + ` with ` + "`http_request`" + ` and rerun the worklist; use ` + "`traffic_routes`" + ` and ` + "`traffic_match`" + ` only for focused follow-up on returned exchange ids, never guess paths.
- Fact prose does not close a direction. Only a fact with ` + "`closure_status=exhausted`" + ` may do so. Missing status or ` + "`closure_status=observation`" + ` is provisional even if its summary says "no SSRF", "not exploitable", or "fully exhausted"; inspect node_detail and do not repeat that absolute claim.
- ` + "`recovery_needed`" + ` in graph_overview is a deterministic queue of provisional negatives from terminal workers. Unless an open/running intent already covers the same recovery, dispatch one materially new recovery intent anchored to each listed fact; do not merely repeat or endorse the worker's absolute prose.
- When a done intent yields a provisional negative fact, unknown request contract, or unresolved leads, treat the direction as incomplete. Dispatch at most one materially new recovery intent anchored to that fact. For an HTTP endpoint with captured frontend artifacts, the recovery mechanism is evidence-bound client-call tracing: run ` + "`traffic_contract_worklist`" + ` with the exact route/symbol leads, fetch useful pending chunks, rerun it, then use ` + "`traffic_match`" + ` on returned exchange ids to recover the nearby method, content type and body construction before another request.
- Treat conclusions imported from an external repository's current/default branch as unversioned leads, never as deployed-target facts. If such a source claim drove a negative result while target version/revision is unproven, the one recovery intent should establish runtime provenance or compare bounded historical tags/releases and then validate the behavior against the target.
- The backend hard-gates ` + "`closure_status=exhausted`" + ` on ` + "`provenance_status`" + `. ` + "`unversioned_external`" + ` can never close a direction. ` + "`version_matched`" + ` needs target-bound version evidence, the exact matching tag/release/commit, and runtime validation; ` + "`target_observed`" + ` needs deployed-target validation; ` + "`not_applicable`" + ` needs a concrete reason. Treat any fact missing this proof as provisional.
- An unrelated running sibling is not a dependency and is not a reason to wait. If the recovery evidence already exists in captured traffic, dispatch the independent recovery intent immediately; wait only when that specific worker must first produce the required artifact.
- A stop condition may close a repeated request shape, not an entire endpoint after a few guessed bodies. Endpoint exhaustion requires a known request/input contract, materially distinct tested variants, observed evidence, and no unresolved caller/bundle/encoding/auth lead.
- The backend rejects duplicate active hypothesis keys and repeated identical failed requests. Treat that rejection as a stop signal, not an invitation to rename or resubmit the same work.
`
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity), notify func()) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetNotify(notify)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools + the base default tool set (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash).
	// When the asset-coverage feature is off, drop add_task_scope/list_untested_assets (not put into the prompt).
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	base = withWebFetch(base, p.proxyAddr, p.proxyCACert)
	base = appendAuditedResearchTools(base, p.webSearch)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), AgentKey: "planner"})
	outcome := runAgentTurn(ctx, "planner", base,
		func(ctx context.Context, tools []actool.CoreTool, def DeferredInfo) preparedAgentTurn {

			// The key situation (the just-finished intent + the pre-fetched full graph) is moved into
			// [this round's user input] (see input below); the system prompt keeps only the static
			// planning body. Moving it out keeps the system prompt stable each round, which caches better;
			// the cost is that if a single round grows long, the situation may be compacted (a planner
			// round is usually short, so the risk is low). situational is concatenated into input below.
			situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
			// Task-level deadline / final mode (injected via ctx, see taskclock.go). On the final round,
			// the task-timeout planner wrap-up wording is concatenated into this round's user input (along
			// with situational) as [this round's operational instruction], so it only does the final goal
			// judgment and produces no new intents.
			tc := taskClockFrom(ctx)
			if tc.Final {
				situational += "\n\n[Task final wrap-up (special instruction this round, overriding the normal planning flow above)]: " + resolveTaskTimeoutWrapup("planner")
			}
			// This task's working directory <workDir>/tasks/<taskID>; create it first.
			taskDir := ensureRunDir(p.workDir, taskID, 0)
			ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
			sysBody := plannerSystem(goal, p.workDir, taskDir)
			if p.wantConstraints() {
				sysBody += constraintBlock(ts) // inject operational constraints (if any) into the system prompt, framing the exploration edge
			}
			system, boundary := deferredSystem(sysBody, def)
			// The planner has no wall-clock budget of its own; with a deadline, clamp MaxDuration to the
			// remaining time so a running planning round enters wrap-up when the task hits its deadline
			// (on timeout → task-timeout wording, on step count → per-run wording).
			maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
			settle := wrapupSettlement("planner", nil)
			if tc.DeadlineUnix > 0 {
				settle = wrapupSettlementForTask("planner", nil, clamped)
			}
			opts := commonAgentOptions(p.prov, system, boundary, tools, def)
			opts.BashEnv = proxyEnv(p.proxyAddr, p.proxyCACert) // Bash subcommands go through the proxy + trust the CA by default
			opts.WorkingDir = taskDir                           // this task's working directory <workDir>/tasks/<taskID>
			opts.ToolOutputDir = cmdOutDir(taskDir)
			opts.MaxTurns = p.maxTurns // 0 = unlimited (configurable in agent management)
			opts.MaxDuration = maxDur  // 0 = unlimited; with a deadline = time remaining until the deadline
			opts.Compaction = compactionConfig(p.compactionWindow())
			// Planning todos shared across wake-ups: keep a serial chain across rounds (the session is new, the store is not).
			opts.Todos = p.todoFor(ts.ID())
			// Hitting [this round's] step budget → the SDK runs wrap-up: land the conclusions already
			// thought through this round (the add_intent that should go out, the prove_goal that can be
			// proven, record the serial chain with TodoWrite), rather than stopping planning — the
			// planner will still be woken repeatedly afterward.
			// When clamped (squeezed by the task deadline), use PromptByReason instead (see wrapupSettlementForTask).
			opts.Settlement = settle
			p.modelRuntime.bindModel(&opts)
			p.modelRuntime.bindTranscript(&opts, func() { // persist raw LLM conversation; one accumulating file per task's planner
				opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
			})
			// Experimental feature: when on, noa takes over context compression (archives are centralized under <workDir>/noa/<SessionID>, persistent).
			noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
			enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
			// The situation (the just-finished intent + the full graph) is now concatenated into this
			// round's user input (see input below). The user input also has the instruction + the
			// cross-wake-up todos (a todo is the model's own planning note, regenerable, so it's fine in user).
			// The opening line splits two ways by "whether there is a concrete change this round": with a
			// change → point to the [actual change] block below; without one (heartbeat scheduled sweep /
			// hint / resume, etc.) → don't falsely claim "the graph changed", and instead prompt it to also re-check running intents.
			lead := "There was just a concrete change (see [The actual change(s) that fired this round] below); plan the next step accordingly: "
			if len(triggers) == 0 {
				lead = "This round is a **scheduled sweep (heartbeat due) / no-concrete-change-signal** wake-up — the graph may not have new changes. Also re-check running intents: use steer_work to course-correct one with no progress for a long time or going off track, and kill_work to cut losses on one whose direction is entirely wrong; then judge the goal and decide whether to add directions: "
				// On a heartbeat/no-change wake-up, if the whole graph has no open or running intent → exploration
				// has stalled (no worker running, no queued direction). Tell the planner explicitly and force it to
				// produce new directions this round, rather than spinning a round after only re-checking running intents.
				if active, err := ts.HasActiveIntent(); err == nil && !active {
					lead = "This round is a **scheduled sweep (heartbeat due)** wake-up, and there is currently **no open or running intent at all** — no worker running and no queued direction; exploration has stalled. You **must** produce one or more new intents this round that advance the goal and are **mutually non-duplicative** with the graph's existing intents (producing 0 intents is not allowed); first judge from the situation below whether the goal is achieved, and if not, add directions immediately: "
				}
			}
			input := lead + situational + "\n\nFrom the situation above, judge the goal. When the goal is [genuinely achieved] (the goal outcome is obtained / the target vulnerability is confirmed), use prove_goal to mark them one by one. **Hard floor: as long as the goal is not yet achieved and there is currently no open or running intent (frontier_open=0 and running_intents empty), you must produce at least one intent that advances the goal this round — here there is no running work to wait on and no queued direction, so 0 intents = the task stalls. Only when there is already an open/running intent advancing, or the goal is achieved, may you produce no new intents this round.**" +
				renderPlannerTodos(opts.Todos.List())
				// MaxDuration now interrupts a running tool at the wall-clock deadline and enters wrap-up in
				// place (on the live ctx), so a stuck single round no longer bypasses wrap-up and needs no
				// external hard-ctx backstop. ctx only carries pause / kill / shutdown.

			return preparedAgentTurn{
				ctx:     ctx,
				options: opts,
				input:   input,
				emit:    activityInLane(emit, "planner", nil),
				resume:  nil,
			}
		}, func() { met, reason = tsx.GoalMet, tsx.Reason })
	return met, reason, outcome.err
}
