package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/guard"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/norma/llm"
	actool "github.com/nobanic/norma/tool"
	"github.com/nobanic/norma/transcript"
)

// ChatAgent is the generic, task-independent conversational runner behind the chat
// page. It generalizes MainAgent.Chat: any agent (built-in OR a custom one, by
// key) can be chatted with, multi-turn history resumed from the transcript. It is
// a PURE ASSISTANT — base tools are the SDK DefaultTools (Bash/Read/Write/Edit/
// LS/Glob/Grep) plus whatever skills/MCP the key is made visible; NO pentest
// graph/task context is injected (that stays exclusive to MainAgent).
type ChatAgent struct {
	modelRuntime
	guard *guard.Guard // optional; nil disables intercept hooks for chat
}

func NewChatAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window int) *ChatAgent {
	return &ChatAgent{modelRuntime: newModelRuntime(prov, model, workDir, tx, window)}
}

// SetNonStreaming wires a resolver deciding whether chat runs use the
// non-streaming model path (true = non-streaming). nil/unset = streaming.
func (c *ChatAgent) SetNonStreaming(fn func() bool) { c.modelRuntime.setNonStreaming(fn) }

func (c *ChatAgent) nonStreaming() bool { return c.modelRuntime.nonStreaming() }

// SetNoaEnabled wires a resolver deciding whether chat runs use the experimental
// noa context-compression mechanism. nil/unset = off (built-in compaction). Read
// per run so the settings toggle takes effect without rebuilding the agent.
func (c *ChatAgent) SetNoaEnabled(fn func() bool) { c.modelRuntime.setNoaEnabled(fn) }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (c *ChatAgent) SetMaxTokens(fn func() int) { c.modelRuntime.setMaxTokens(fn) }

func (c *ChatAgent) maxTokens() int {
	return c.modelRuntime.maxTokens()
}

// SetProxy points the chat agent's WebFetch/Bash at the recording proxy plus the
// CA cert it trusts (empty addr = direct). Kept for parity with the other agents.
func (c *ChatAgent) SetProxy(addr, caCert string) { c.modelRuntime.setProxy(addr, caCert) }

// SetWebSearch selects the web_search backend for the chat agent (off by default).
func (c *ChatAgent) SetWebSearch(o WebSearchOpts) { c.modelRuntime.setWebSearch(o) }

// SetGuard attaches a guard (with user-configured intercept rules) to this chat
// agent. Must be called before Chat; safe to call multiple times.
func (c *ChatAgent) SetGuard(g *guard.Guard) { c.guard = g }

// chatWorkDirSpec returns a working-directory notice appended to every chat
// agent's system prompt. Mirrors artifactSpec but without pentest-specific
// wording ("payload", "capture the response body") that would be odd in a general assistant.
func chatWorkDirSpec(workDir string) string {
	return "\n\n**File output convention**: when you need to write a file, always write it to the working directory " + workDir + " (this is the default CWD, so a relative path lands here; this absolute path works too) — do not write to /tmp or other absolute paths."
}

// chatSystem renders the DB-managed prompt body for agentKey. Custom agents have
// no per-key in-code default, so DefaultAssistantPrompt is the render fallback.
func chatSystem(agentKey, dataDir, workDir string) string {
	return renderSystem(agentKey, DefaultAssistantPrompt, chatVars{DataDir: dataDir, Now: nowStr()}) + chatWorkDirSpec(workDir)
}

// chatVars carries the runtime variables a custom agent's prompt may reference.
// DataDir (server data root) + Now (server wall-clock, refreshed each turn) are the
// universal ones; any other {{.X}} fails to render and falls back to
// DefaultAssistantPrompt.
type chatVars struct{ DataDir, Now string }

// Chat runs ONE turn of a conversation with the agent identified by agentKey,
// resuming prior history keyed by sessionID. maxTurns is the per-turn agent step
// budget (0 = unlimited). maxDuration is the wall-clock run budget per turn
// (0 = unlimited); the timer resets each time Chat is called, so a new user
// message always starts a fresh countdown. webSearch gates network search for
// THIS agent (the global backend/key still come from the chat agent's config,
// but each agent decides on/off). emit receives each execution step (thinking /
// tool_use / tool_result / text / result), tagged with the agent key as the
// worker lane.
func (c *ChatAgent) Chat(ctx context.Context, agentKey, sessionID, message string, maxTurns int, maxDuration time.Duration, webSearch bool, emit func(db.Activity)) (string, error) {
	// gate the global web-search opts by this agent's own flag.
	ws := c.webSearch
	if !webSearch {
		ws.Enabled = false
	}

	// Per-session working directory: <workDir>/sessions/<sessionID>/
	// Isolates file writes across conversations, mirroring how workers use i<intentID>/.
	sessionWorkDir := filepath.Join(c.workDir, "sessions", sessionID)
	_ = os.MkdirAll(sessionWorkDir, 0o755)
	ctx = intercept.WithReviewWorkingDirectory(ctx, sessionWorkDir)

	// Pure assistant: DefaultTools as the base; AugmentTools layers in the key's
	// visible skills/MCP and lets the DB tools table filter/override. DefaultTools
	// have no tools-table rows, so they always pass through.
	base := withWebFetch(actool.DefaultTools(), c.proxyAddr, c.proxyCACert)
	base = appendAuditedResearchTools(base, ws)
	ctx = WithRunInfo(ctx, RunInfo{SessionID: sessionID, AgentKey: agentKey})
	outcome := runAgentTurn(ctx, agentKey, base,
		func(ctx context.Context, tools []actool.CoreTool, def DeferredInfo) preparedAgentTurn {

			system, boundary := deferredSystem(chatSystem(agentKey, c.workDir, sessionWorkDir), def)
			opts := commonAgentOptions(c.prov, system, boundary, tools, def)
			opts.BashEnv = proxyEnv(c.proxyAddr, c.proxyCACert) // Bash sub-commands route through the proxy and trust the CA by default
			opts.WorkingDir = sessionWorkDir
			opts.MaxTurns = maxTurns
			opts.MaxDuration = maxDuration
			opts.Compaction = compactionConfig(c.window)
			opts.Todos = actool.NewTodoStore()
			// large tool output spills to cmd-output/ under the session dir.
			// truncation cap uses the SDK default (tool.Capture's 30000 characters).
			opts.ToolOutputDir = filepath.Join(sessionWorkDir, "cmd-output")
			// hitting the budget (step count) → the SDK runs the wrap-up: it emits a one-line summary. Prompt and wrap-up turn count are admin-editable per agent key
			// (each custom agent has its own; empty/0 uses the generic default: 10 turns).
			opts.Settlement = wrapupSettlement(agentKey, nil)
			c.modelRuntime.bindModel(&opts)
			if c.guard != nil {
				opts.Hooks = c.guard.Hooks()
			}
			c.modelRuntime.bindTranscript(&opts, func() { // persist raw human↔AI conversation; one accumulating file per thread
				opts.SessionID = sessionID
			})
			// Experimental feature: when on, noa takes over context compaction (archives collected under <workDir>/noa/<SessionID>, persistent).
			enableNoa(&opts, c.noaEnabledFn, c.workDir, "chat-"+sessionID, noaWarn("chat-"+sessionID))

			return preparedAgentTurn{
				ctx:     ctx,
				options: opts,
				input:   message,
				emit:    activityInLane(emit, agentKey, nil),
				resume:  conversationResume(c.tx != nil, sessionID, def.UnlockSkill, message),
			}
		}, nil)
	return outcome.text, outcome.err
}
