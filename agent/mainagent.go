package agent

import (
	"context"
	"fmt"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/norma/llm"
	actool "github.com/nobanic/norma/tool"
	"github.com/nobanic/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	modelRuntime
	windowFn  func() int                             // optional dynamic task-chain minimum
	maxTurns  int                                    // max agent turns per run (0 = unlimited)
	steerWork func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.modelRuntime.setNoaEnabled(fn) }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.modelRuntime.setNonStreaming(fn) }

func (m *MainAgent) nonStreaming() bool { return m.modelRuntime.nonStreaming() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.modelRuntime.setMaxTokens(fn) }

func (m *MainAgent) maxTokens() int {
	return m.modelRuntime.maxTokens()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{modelRuntime: newModelRuntime(prov, model, workDir, tx, window), maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	return m.modelRuntime.compactionWindow(&m.windowFn)
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.modelRuntime.setProxy(addr, caCert) }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.modelRuntime.setWebSearch(o) }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (section [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the
// intermediate-artifact output rules tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `You are the "main agent" of an authorized penetration-testing system — the interface to the human operator. You do not explore yourself, nor do you autonomously generate intents continuously (that is the planner's job). Your responsibilities:

1. Observe: use graph_overview / list_findings / list_facts / list_assets / get_worker_output to answer the human's questions about current progress.
2. Steer (turn the human's intent into system action):
   - The human wants to "change direction / emphasize a class of vulnerability / focus on a region" → use add_hint to write a hint (the planner will read it next time).
   - The human wants to "test a specific target right now" → use add_intent to inject a high-priority intent directly (priority 8-10). The system automatically pulls a completed task back into the running state, has a worker pick up this intent and execute it, and returns to the completed state once done.
     **When all task goals are already met** (goals are all met in graph_overview): before dispatching, judge whether this intent implies a "new result to be achieved". If it does, restate the goal you inferred to the human in one sentence and **ask back whether to register it as a formal goal** — the human says yes → use set_goals to register it (the task then enters normal planning and the planner drives it forward autonomously); the human says no / just wants a quick one-off probe → only add_intent this single one, and once the worker finishes the task returns to the completed state (it will not continue on its own). If this intent is clearly just a one-off check that implies no new goal, simply add_intent it — you need not ask every time.
   - The human wants to "course-correct a running intent (work) in real time (stop doing X, focus on Y)" → use steer_work (without interrupting or losing existing progress; it takes effect before the worker's next action); first use get_worker_output to see what it is doing. If the direction is entirely wrong, use add_intent to dispatch a new intent instead.
   - The human wants to "add a new final goal to achieve" → use set_goals to add the goal. The system writes the goal into the task graph and **automatically pulls completed/paused tasks back into the running state to keep going** (the planner then re-judges whether it is met based on this), with no need for a manual resume.
   - The human wants to "add/change a test constraint (allow/forbid a class of operations, e.g. 'test the current port only', 'no brute-forcing', 'passive reconnaissance only')" → use set_constraints to register it (type=allow / type=deny). The constraint is injected into the planner/worker prompt on the next planning round to frame the exploration boundary; it can also be added/edited/removed under "Constraint management" in the overview.
3. Reply concisely in plain language, explaining what you did.

Current task goal: {{.Goal}}

Do not fabricate findings; answer only from the real data the tools return.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // generic wake-up (write operations with no dedicated callback go through it, debounced)
	tsx.SetResumeTask(resume)     // set_goals adds a goal → pull completed/paused tasks back to running
	tsx.SetNotifyGoal(notifyGoal) // set_goals adds a goal → record a "human added a goal: …" trigger for the planner
	tsx.SetNotifyHint(notifyHint) // add_hint adds a hint → record a "human added N strategic hints: …" trigger for the planner
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// Domain tools + the base default tool set (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash).
	// When the asset-coverage feature is off, strip add_task_scope/list_untested_assets (not in the prompt).
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	base = withWebFetch(base, m.proxyAddr, m.proxyCACert)
	base = appendAuditedResearchTools(base, m.webSearch)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), AgentKey: "mainagent"})
	outcome := runAgentTurn(ctx, "mainagent", base,
		func(ctx context.Context, tools []actool.CoreTool, def DeferredInfo) preparedAgentTurn {

			// This task's working directory <workDir>/tasks/<taskID>; create it first.
			mainDir := ensureRunDir(m.workDir, taskID, 0)
			ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
			system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
			opts := commonAgentOptions(m.prov, system, boundary, tools, def)
			opts.BashEnv = proxyEnv(m.proxyAddr, m.proxyCACert) // Bash sub-commands route through the proxy and trust the CA by default
			opts.WorkingDir = mainDir                           // this task's working directory <workDir>/tasks/<taskID>
			opts.ToolOutputDir = cmdOutDir(mainDir)
			opts.MaxTurns = m.maxTurns                               // 0 = unlimited (configurable in agent management)
			opts.Compaction = compactionConfig(m.compactionWindow()) // long chats stay within the window
			opts.Todos = actool.NewTodoStore()                       // session-scoped temporary todos (TodoWrite), for planning only, discarded on exit
			// hitting the budget (step count) → the SDK runs the wrap-up: it emits a one-line progress summary to the user. Prompt and wrap-up turn count are admin-editable (default 10 turns).
			opts.Settlement = wrapupSettlement("mainagent", nil)
			m.modelRuntime.bindModel(&opts)
			m.modelRuntime.bindTranscript(&opts, func() { // persist raw human↔AI conversation; one accumulating file per segment
				// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
				// load; each new session (seg>=1) gets its own file for a clean context.
				opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
				if mainSeg > 0 {
					opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
				}
			})
			// Experimental feature: when on, noa takes over context compaction (archives collected under <workDir>/noa/<SessionID>, persistent).
			// The session id follows the same rule as the transcript (segment-aware), keeping archiving and restoration aligned.
			noaSession := fmt.Sprintf("exp%d-main", ts.ID())
			if mainSeg > 0 {
				noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
			}
			enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))

			return preparedAgentTurn{
				ctx:     ctx,
				options: opts,
				input:   message,
				emit:    activityInLane(emit, "mainagent", nil),
				resume:  conversationResume(m.tx != nil, opts.SessionID, def.UnlockSkill, message),
			}
		}, nil)
	return outcome.text, outcome.err
}
