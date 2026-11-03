package server

import (
	"github.com/nobanic/UroborOS/agent"
	"log"
	"strings"
)

type capabilityAgentRegistry struct{ exchange capabilityRequest }

func (controller capabilityAgentRegistry) list() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	ags, err := pg.ListAgents()
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	dtos := agentDTOs(ags)
	// overlay per-agent binding counts (mcp/skill by id, tools by key) — best-effort.
	if mcp, skill, tools, err := pg.AgentBindingCounts(); err == nil {
		for i := range dtos {
			dtos[i].McpCount = mcp[ags[i].ID]
			dtos[i].SkillCount = skill[ags[i].ID]
			dtos[i].ToolCount = tools[ags[i].Key]
		}
	}
	return capabilityDocument{status: 200, value: map[string]any{"agents": dtos}}
}

func (controller capabilityAgentRegistry) create() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	var req struct{ Key, Name, Description string }
	if err := decode(controller.exchange.request, &req); err != nil {
		return capabilityFailure(400, err.Error())
	}
	req.Key, req.Name = strings.TrimSpace(req.Key), strings.TrimSpace(req.Name)
	if !reAgentKey.MatchString(req.Key) {
		return capabilityFailure(400, "key must start with a lowercase letter and contain only lowercase letters/digits/underscores")
	}
	if req.Name == "" {
		return capabilityFailure(400, "Name cannot be empty")
	}
	if exist, _ := pg.GetAgentByKey(req.Key); exist != nil {
		return capabilityFailure(409, "this key already exists")
	}
	a, err := pg.CreateAgent(req.Key, req.Name, req.Description)
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	// starter prompt so the editor shows something editable from the start.
	if err := pg.SeedPromptIfEmpty(a.ID, agent.DefaultAssistantPrompt); err != nil {
		log.Printf("[agents] seed starter prompt for %s failed: %v", a.Key, err)
	}
	return capabilityDocument{status: 200, value: agentDTO(a)}
}

func (controller capabilityAgentRegistry) update() capabilityDocument {
	pg, a, ok := controller.exchange.server.agentByKey(controller.exchange.writer, controller.exchange.request)
	if !ok {
		return capabilityDocument{}
	}
	if a.Builtin {
		return capabilityFailure(400, "built-in agents cannot have their name/description modified")
	}
	var req struct{ Name, Description string }
	if err := decode(controller.exchange.request, &req); err != nil {
		return capabilityFailure(400, err.Error())
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return capabilityFailure(400, "Name cannot be empty")
	}
	if err := pg.UpdateAgentMeta(a.Key, req.Name, req.Description); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"ok": true}}
}

func (controller capabilityAgentRegistry) remove() capabilityDocument {
	pg, a, ok := controller.exchange.server.agentByKey(controller.exchange.writer, controller.exchange.request)
	if !ok {
		return capabilityDocument{}
	}
	if a.Builtin {
		return capabilityFailure(400, "built-in agents cannot be deleted")
	}
	if err := pg.DeleteAgent(a.Key); err != nil {
		return capabilityFailure(500, err.Error())
	}
	if err := pg.RemoveAgentFromToolBindings(a.Key); err != nil {
		log.Printf("[agents] failed to clean up %s tool bindings: %v", a.Key, err)
	}
	if err := pg.DeleteTriggersForAgent(a.Key); err != nil {
		log.Printf("[agents] failed to clean up %s triggers: %v", a.Key, err)
	}
	return capabilityDocument{status: 200, value: map[string]any{"deleted": a.Key}}
}
