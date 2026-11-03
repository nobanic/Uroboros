package server

import (
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
)

func (endpoint interceptHTTP) tools() interceptDocument {
	tools, err := endpoint.server.m.interceptor.GetEnabledTools()
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	return interceptDocument{status: 200, value: map[string]any{"enabled_tools": tools}}
}
func (endpoint interceptHTTP) setTools() interceptDocument {
	var req interceptToolsInput

	if err := decode(endpoint.request, &req); err != nil {
		return interceptFailure(400, err.Error())
	}
	if req.EnabledTools == nil {
		req.EnabledTools = []string{}
	}
	if err := endpoint.server.m.interceptor.SetEnabledTools(req.EnabledTools); err != nil {
		return interceptFailure(500, err.Error())
	}
	return interceptDocument{status: 200, value: map[string]any{"ok": true}}
}
func (endpoint interceptHTTP) judge() interceptDocument {
	return interceptDocument{status: 200, value: endpoint.server.m.interceptor.GetJudgeConfig()}
}
func (endpoint interceptHTTP) setJudge() interceptDocument {
	var req intercept.JudgeConfig
	if err := decode(endpoint.request, &req); err != nil {
		return interceptFailure(400, err.Error())
	}
	switch req.FailAction {
	case "allow", "ask", "deny":
	default:
		return interceptFailure(400, "fail_action must be allow, ask, or deny")
	}
	switch req.AskTimeoutAction {
	case "allow", "deny":
	default:
		return interceptFailure(400, "ask_timeout_action must be allow or deny")
	}
	if err := endpoint.server.m.interceptor.SetJudgeConfig(req); err != nil {
		return interceptFailure(500, err.Error())
	}
	return interceptDocument{status: 200, value: map[string]any{"ok": true}}
}
func (endpoint interceptHTTP) usage() interceptDocument {
	pg := endpoint.server.m.PG()
	if pg == nil {
		return interceptDocument{status: 200, value: db.JudgeUsage{Daily: []db.JudgeDayUsage{}}}
	}
	days := atoiDefault(endpoint.request.URL.Query().Get("days"), 30)
	usage, err := pg.JudgeUsageStats(days)
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	return interceptDocument{status: 200, value: usage}
}
