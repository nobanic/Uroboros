package agent

import (
	"strings"

	"github.com/nobanic/norma/harness"
)

// Wrap-up / settlement prompt: when an agent is terminated because it ran out of
// turns (MaxTurns) or timed out (run_seconds/MaxDuration), the SDK's settlement
// phase injects this prompt so the agent first persists anything it identified but
// has not written back, then emits a one-line summary, avoiding a half-finished run.
//
// Each agent's wrap-up prompt can be overridden from the admin backend as needed
// (stored in agents.wrapup_prompt); empty falls back to the built-in default here.
// Only the prompt body is editable; which tools are disabled and how many turns the
// wrap-up itself gets are fixed, code-owned policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default wrap-up prompts, indexed by agent key. worker reuses the
// historically hard-coded settleWrapUpPrompt (defined in worker.go); planner and
// mainagent each have their own; an unmatched (custom) agent uses the generic fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: built-in default turn budget for each agent's own wrap-up
// phase (can be overridden by a backend value >0). All get 10 turns, enough to
// persist results during wrap-up. An unmatched agent uses genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "Your planning turns for this round are almost used up — note that only THIS round is ending; the system will wake you again later as the situation evolves to keep planning, so this is not the end of the task and you do not need to wrap up the whole plan here. Land the conclusions you have already thought through this round so it is not wasted, but also DO NOT force out intents just to wrap up (0 intents this round is still a perfectly normal result): (1) if you have identified exploration directions that SHOULD be dispatched right now, submit them in one batch with add_intent (do not sit on what you have settled on); (2) for goals already proven met by some finding/fact, call prove_goal to mark them met (do not miss any); (3) if you have identified a serial exploitation chain that must be done step by step, note it down with TodoWrite so the next wake-up can keep dispatching it. When done, just end this round; no summary text needed."

const mainAgentWrapUpDefault = "Your turns are almost used up and this interaction is about to end. Do not start any new exploration/action. Please summarize the current progress, the key conclusions, and a suggested next step for the user in **a single plain-text sentence on its own**."

const genericWrapUpDefault = "You are about to be terminated because your budget is exhausted. First write back any completed results that have not been persisted, then summarize what you did and the key conclusions you reached in **a single plain-text sentence on its own** (this sentence will be shown as the result of this run)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- task-level timeout wrap-up prompt (see docs/task-timeout-and-wrapup-design.md) ----------
//
// This is a SEPARATE set from the per-run wrap-up prompt: per-run means "the budget
// for this one run is used up"; task timeout means "the whole task has hit its
// deadline and is about to end". The semantics are often opposite (especially for
// planner: per-run says "don't stop, keep planning", task timeout says "the deadline
// is reached, stop planning and make the final determination"). Configured for
// worker/planner only.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB overrides for the task-timeout
// wrap-up prompt and turn budget (wired to agents.task_timeout_wrapup_prompt /
// _max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not the budget for this one run — the entire exploration has hit its deadline). This is your last chance: (1) persist EVERYTHING you have identified but not yet written back — new assets via insert_assets, exploration conclusions/facts via record_fact, confirmed vulnerabilities via report_finding; (2) do not start any new commands/probes; (3) **finally, in a single plain-text sentence on its own**, summarize your key conclusions on this intent."

const plannerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not this round — the entire task is terminating). Based on ALL current facts and findings, make a final goal determination: for goals proven met by evidence, call prove_goal to mark them met (do not miss any). **Do not generate any new intents** (dispatching an intent now would no longer be executed). Once the determination is done, wrap up; no summary text needed."

// TaskTimeoutWrapupDefault returns an agent's built-in default task-timeout wrap-up
// prompt (used by the admin UI as the placeholder / restore-default value).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // not configured (mainagent/chat) → returns empty string
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. An empty
// string means the agent has no task-timeout prompt (not worker/planner), in which
// case the caller should fall back to the per-run prompt.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // default: reuse the per-run turn count
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → this run is squeezed by the task deadline: wrapping up due to
//     Timeout = the task hit its deadline → use the task-timeout prompt; wrapping up
//     due to MaxTurns = turns ran out first within the squeeze window while the task
//     still has a few minutes left → fall back to the per-run prompt.
//   - clamped=false → the task deadline is still far off: both reasons use the per-run
//     prompt (i.e. it degenerates to wrapupSettlement).
//
// The PromptByReason handed to the harness picks by the ACTUAL reason at wrap-up time,
// so there is no build-time mismatch.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // fallback (and the value both reasons take when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // task hit its deadline
				harness.ReasonMaxTurns: perRun, // turns ran out first, task still has time left
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
