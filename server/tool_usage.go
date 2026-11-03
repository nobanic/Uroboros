package server

import (
	"context"
	"encoding/json"
	"log"

	actool "github.com/nobanic/norma/tool"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

type toolUsageRecorder interface {
	InsertToolUsage(*db.ToolUsage) error
}

type taskExecutionChecker interface {
	TaskExecutionAllowed(int64, bool) (bool, error)
}

// meteredTool records one row immediately before a catalog tool is invoked. It
// embeds the resolved tool so schema overrides, defaults and permission behavior
// remain unchanged.
type meteredTool struct {
	actool.CoreTool
	recorder toolUsageRecorder
	toolKey  string
	agentKey string
	ri       agent.RunInfo
}

func meterTool(t actool.CoreTool, recorder toolUsageRecorder, toolKey, agentKey string, ri agent.RunInfo) actool.CoreTool {
	if recorder == nil {
		return t
	}
	return &meteredTool{CoreTool: t, recorder: recorder, toolKey: toolKey, agentKey: agentKey, ri: ri}
}

func (m *meteredTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	ri := mergeRunInfo(m.ri, agent.RunInfoFrom(ctx))
	if err := m.recorder.InsertToolUsage(&db.ToolUsage{
		ToolKey:       m.toolKey,
		AgentKey:      m.agentKey,
		TaskID:        ri.TaskID,
		ExplorationID: ri.ExplorationID,
		IntentID:      ri.IntentID,
		SessionID:     ri.SessionID,
	}); err != nil {
		log.Printf("[toolusage] insert %s: %v", m.toolKey, err)
	}
	return m.CoreTool.Call(ctx, input, tc)
}

func mergeRunInfo(captured, current agent.RunInfo) agent.RunInfo {
	if current.TaskID == 0 {
		current.TaskID = captured.TaskID
	}
	if current.ExplorationID == 0 {
		current.ExplorationID = captured.ExplorationID
	}
	if current.IntentID == 0 {
		current.IntentID = captured.IntentID
	}
	if current.SessionID == "" {
		current.SessionID = captured.SessionID
	}
	return current
}

// taskGuardedTool is the tool-side half of the pause barrier. Context
// cancellation closes the in-process race before pause persistence; the DB check
// prevents a stale/restored agent context from executing after pause committed.
type taskGuardedTool struct {
	actool.CoreTool
	checker taskExecutionChecker
	ri      agent.RunInfo
}

func guardTaskTool(t actool.CoreTool, checker taskExecutionChecker, ri agent.RunInfo) actool.CoreTool {
	if t == nil || checker == nil {
		return t
	}
	return &taskGuardedTool{CoreTool: t, checker: checker, ri: ri}
}

func (t *taskGuardedTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if cause := context.Cause(ctx); cause != nil {
		return actool.Result{}, cause
	}
	ri := mergeRunInfo(t.ri, agent.RunInfoFrom(ctx))
	if ri.TaskID > 0 {
		allowed, err := t.checker.TaskExecutionAllowed(ri.TaskID, agent.TaskClockFrom(ctx).Final)
		if err != nil {
			log.Printf("[toolgate] task %d execution check: %v", ri.TaskID, err)
			return actool.Errorf("task execution gate is unavailable"), nil
		}
		if !allowed {
			return actool.Errorf("task execution is paused or unavailable"), nil
		}
	}
	return t.CoreTool.Call(ctx, input, tc)
}
