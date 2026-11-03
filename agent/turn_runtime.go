package agent

import (
	"context"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/agentcore"
	"github.com/nobanic/norma/harness"
	"github.com/nobanic/norma/llm"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

// preparedAgentTurn retains each role's prompt, option evaluation order and
// history policy. A nil resume means a fresh planner round, without side capture.
type preparedAgentTurn struct {
	ctx     context.Context
	options agentcore.Options
	input   string
	emit    func(db.Activity)
	resume  func(*agentcore.Session) string
}

type agentTurnResult struct {
	text   string
	reason harness.TerminalReason
	err    error
}

// runAgentTurn owns augmentation and the SDK session for one whole turn.
// Project return values at the original role boundary: resumable roles snapshot
// before session Close; planner snapshots after captureRun has closed its session.
// In every role augmentation cleanup follows SDK Close, including on panic.
func runAgentTurn(ctx context.Context, key string, base []actool.CoreTool,
	prepare func(context.Context, []actool.CoreTool, DeferredInfo) preparedAgentTurn,
	project func()) agentTurnResult {
	tools, def, cleanup := AugmentTools(ctx, key, base)
	defer cleanup()
	turn := prepare(ctx, tools, def)
	var result agentTurnResult
	if turn.resume == nil {
		result.text, result.reason, result.err = captureRun(turn.ctx, turn.options, turn.input, turn.emit)
	} else {
		turn.ctx = attachSideCapture(turn.ctx, &turn.options)
		session := agentcore.NewSession(turn.options)
		defer session.Close()
		input := turn.resume(session)
		result.text, result.reason, result.err = captureRunSession(turn.ctx, session, input, turn.emit)
	}
	if project != nil {
		project()
	}
	return result
}

// The leading eight options are shared. Role-specific fields are assigned in
// their original order by prepare, including dynamic resolvers and todo stores.
func commonAgentOptions(provider llm.Provider, system []string, boundary int, tools []actool.CoreTool, def DeferredInfo) agentcore.Options {
	return agentcore.Options{
		Provider:        provider,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebSearch: false,
	}
}

func activityInLane(emit func(db.Activity), worker string, nodeID *int64) func(db.Activity) {
	return func(row db.Activity) {
		if emit != nil {
			if nodeID != nil {
				row.NodeID = nodeID
			}
			row.Worker = worker
			emit(row)
		}
	}
}

func conversationResume(persistent bool, sessionID string, unlock func(string), input string) func(*agentcore.Session) string {
	return func(session *agentcore.Session) string {
		if persistent {
			_ = session.Resume(sessionID)
		}
		seedUnlockFromHistory(session.Messages(), unlock)
		return input
	}
}
