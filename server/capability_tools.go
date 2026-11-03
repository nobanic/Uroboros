package server

import (
	"encoding/json"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"log"
)

type capabilityToolRegistry struct{ exchange capabilityRequest }

// Default lookup retains the ordered code catalogs; the chosen record crosses
// one force-write boundary without altering runtime handler or binding identity.
type capabilityToolDefaults struct {
	exchange capabilityRequest
	key      string
}
type capabilityToolDefault struct {
	key, description string
	schema, agents   json.RawMessage
}

func (controller capabilityToolRegistry) list() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	ts, err := pg.ListTools()
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	if ts == nil {
		ts = []*db.Tool{}
	}
	// Usage is best-effort decoration. Runtime tool resolution keeps using the plain
	// catalog query, so agent assembly never pays for this aggregate.
	counts, countErr := pg.ToolUsageCounts()
	if countErr != nil {
		log.Printf("[tools] failed to read call statistics: %v", countErr)
	} else {
		for _, tool := range ts {
			tool.Calls = counts[tool.Key]
		}
	}
	for _, tool := range ts {
		tool.Family = toolRuntimeFamily(tool.Key, tool.System)
	}
	return capabilityDocument{status: 200, value: map[string]any{"tools": ts}}
}

func (controller capabilityToolRegistry) update() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	key := controller.exchange.request.PathValue("key")
	cur, err := pg.GetTool(key)
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	if cur == nil {
		return capabilityFailure(404, "tool does not exist: "+key)
	}
	var body struct {
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Agents      []string        `json:"agents"`
		Enabled     bool            `json:"enabled"`
	}
	if err := decode(controller.exchange.request, &body); err != nil {
		return capabilityFailure(400, err.Error())
	}
	agents, _ := json.Marshal(body.Agents)
	if err := pg.UpdateTool(key, body.Description, body.Schema, agents, body.Enabled); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"ok": true}}
}

func (controller capabilityToolRegistry) reset() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	key := controller.exchange.request.PathValue("key")
	selected := (capabilityToolDefaults{exchange: controller.exchange, key: key}).resolve()
	if selected == nil {
		return capabilityFailure(404, "not a built-in tool or does not exist: "+key)
	}
	if err := pg.UpsertToolForce(selected.key, selected.description, selected.schema, selected.agents); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"ok": true}}
}

func (defaults capabilityToolDefaults) resolve() *capabilityToolDefault {
	for _, sd := range agent.BuiltinToolSeeds() {
		if sd.Key != defaults.key {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		agents, _ := json.Marshal(sd.Agents)
		return &capabilityToolDefault{key: sd.Key, description: sd.Desc, schema: schema, agents: agents}
	}
	// orchestration/platform tools (auto agent) — default-bind to "auto".
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range append(defaults.exchange.server.orchestrationTools(), defaults.exchange.server.platformTools()...) {
		if t.Name() != defaults.key {
			continue
		}
		schema, _ := json.Marshal(t.InputSchema())
		return &capabilityToolDefault{key: t.Name(), description: t.Description(), schema: schema, agents: autoAgents}
	}
	// Task-scoped discovery/evidence engines share the same default binding.
	// These tools were already seedable and editable, but historically the reset
	// endpoint omitted them, leaving a Katana row with a broken Reset action.
	httpAgents, _ := json.Marshal([]string{"planner", "worker", "pentest"})
	discoveryTools := append(defaults.exchange.server.katanaTools(), defaults.exchange.server.arsenalTools()...)
	discoveryTools = append(discoveryTools, defaults.exchange.server.httpRequestTool(), defaults.exchange.server.artifactTransformTool())
	for _, t := range discoveryTools {
		if t.Name() != defaults.key {
			continue
		}
		schema, _ := json.Marshal(t.InputSchema())
		return &capabilityToolDefault{key: t.Name(), description: t.Description(), schema: schema, agents: httpAgents}
	}
	plannerAgents, _ := json.Marshal([]string{"planner"})
	reviewAgents, _ := json.Marshal([]string{"planner", "mainagent"})
	for _, t := range defaults.exchange.server.discoveryReviewTools() {
		if t.Name() != defaults.key {
			continue
		}
		bindings := plannerAgents
		if t.Name() == "discovery_review" {
			bindings = reviewAgents
		}
		schema, _ := json.Marshal(t.InputSchema())
		return &capabilityToolDefault{key: t.Name(), description: t.Description(), schema: schema, agents: bindings}
	}
	return nil
}
