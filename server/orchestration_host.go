package server

import (
	"context"
	"encoding/json"
	"github.com/nobanic/UroborOS/agent"
	actool "github.com/nobanic/norma/tool"
	"log"
	"strings"
)

// orchestrationHost borrows the original manager, engine and task-bound tools.
type orchestrationHost struct{ server *Server }

func (host orchestrationHost) available() ([]actool.CoreTool, map[string][]string) {
	tools := append(host.server.m.HostTools(), host.server.orchestrationTools()...)
	tools = append(tools, host.server.discoveryReviewTools()...)
	tools = append(tools, host.server.katanaTools()...)
	tools = append(tools, host.server.arsenalTools()...)
	tools = append(tools, host.server.binaryTools()...)
	tools = append(tools, host.server.artifactTransformTool())
	// The structured request tool is evidence-mandatory, so do not advertise it
	// when the recording proxy is absent or capture is disabled.
	if host.server.m.Traffic() != nil && host.server.m.TrafficEnabled() {
		tools = append(tools, host.server.httpRequestTool())
	}
	tools = append(tools, host.server.findingRetestTools()...)
	tools = append(tools, host.server.findingExperimentTools()...)
	tools = append(tools, host.server.experimentRetestTools()...)
	tools = append(tools, host.server.trafficSchemaTool())
	tools = append(tools, host.server.platformTools()...) // platform operation tools (create/modify skills/tools/MCP, for Auto))
	custom, err := host.server.customTools()
	if err != nil {
		log.Printf("[custom-tool] load failed: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := host.server.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

func (host orchestrationHost) catalog() []actool.CoreTool {
	return []actool.CoreTool{
		host.server.toolListTasks(),
		host.server.toolListLLMProfiles(),
		host.server.toolSpawnTask(),
		host.server.toolPauseTask(),
		host.server.toolGetTaskGraph(),
		host.server.toolListTaskFindings(),
		host.server.toolAddHint(),
		host.server.toolGetWorkerTrace(),
		host.server.toolListWorkerTraces(),
		host.server.toolSearchWorkerTraces(),
		host.server.toolGetTaskNodeDetail(),
		host.server.toolUpdateFindingReport(),
		host.server.toolGetFindingTraffic(),
		host.server.toolBindFindingTraffic(),
	}
}

func (host orchestrationHost) delegate(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id is required"), nil
	}
	t, ok := host.server.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("task not found: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if host.server.m.Assets() != nil {
		tsx.SetAssetStore(host.server.m.Assets(), host.server.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // generic wake-up (write ops without a dedicated callback use it; read tools are no-op)
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → log 'a human added N strategic hints: ...', then trigger and wake the planner
	return pick(tsx).Call(ctx, inner, nil)
}

func (host orchestrationHost) status(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || host.server.engine.IsPaused(t.ID):
		return "paused"
	case host.server.engine.ReadyFor(t) && host.server.engine.Started(t.ID):
		return "running"
	}
	return "created"
}
