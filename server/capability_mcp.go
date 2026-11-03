package server

import (
	"context"
	"github.com/nobanic/UroborOS/db"
	"log"
	"time"
)

type capabilityMCPRegistry struct{ exchange capabilityRequest }

func (controller capabilityMCPRegistry) list() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	ms, err := pg.ListMCP()
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	if ms == nil {
		ms = []*db.MCPServer{}
	}
	return capabilityDocument{status: 200, value: map[string]any{"servers": ms}}
}

func (controller capabilityMCPRegistry) save() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	var m db.MCPServer
	if err := decode(controller.exchange.request, &m); err != nil {
		return capabilityFailure(400, err.Error())
	}
	if m.RequestTimeoutSeconds < 0 || m.RequestTimeoutSeconds > 3600 {
		return capabilityFailure(400, "request_timeout_seconds must be between 1 and 3600 (0 preserves the default)")
	}
	isNew := m.ID == 0
	id, err := pg.SaveMCP(&m)
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	// On initial add, auto-discover + cache the tool list so the UI shows it right
	// away (bounded so a slow/broken server can't hang the request). Skipped on
	// plain updates (e.g. enable toggles) to avoid re-spawning the server each time.
	if isNew && m.Enabled {
		m.ID = id
		ctx, cancel := context.WithTimeout(controller.exchange.request.Context(), 45*time.Second)
		if derr := controller.exchange.server.discoverAndCacheMCP(ctx, &m); derr != nil {
			log.Printf("[mcp] %s tool discovery failed after adding: %v", m.Name, derr)
		}
		cancel()
	}
	return capabilityDocument{status: 200, value: map[string]any{"id": id}}
}

func (controller capabilityMCPRegistry) remove() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	id, _ := pathInt(controller.exchange.request, "id")
	if err := pg.DeleteMCP(id); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"deleted": id}}
}

func (controller capabilityMCPRegistry) refresh() {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return
	}
	id, _ := pathInt(controller.exchange.request, "id")
	all, err := pg.ListMCP()
	if err != nil {
		writeErr(controller.exchange.writer, 500, err.Error())
		return
	}
	var target *db.MCPServer
	for _, m := range all {
		if m.ID == id {
			target = m
			break
		}
	}
	if target == nil {
		writeErr(controller.exchange.writer, 404, "MCP does not exist")
		return
	}
	ctx, cancel := context.WithTimeout(controller.exchange.request.Context(), 45*time.Second)
	defer cancel()
	if err := controller.exchange.server.discoverAndCacheMCP(ctx, target); err != nil {
		writeErr(controller.exchange.writer, 502, "tool discovery failed: "+err.Error())
		return
	}
	tools, _ := pg.MCPToolsDetailed(id)
	writeJSON(controller.exchange.writer, 200, map[string]any{"tools": tools})
}

func (controller capabilityMCPRegistry) tools() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	id, _ := pathInt(controller.exchange.request, "id")
	tools, err := pg.MCPToolsDetailed(id)
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	if tools == nil {
		tools = []db.MCPTool{}
	}
	return capabilityDocument{status: 200, value: map[string]any{"tools": tools}}
}
