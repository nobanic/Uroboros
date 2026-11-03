package server

import (
	"net/http"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

func (s *Server) mcpPolicyTarget(w http.ResponseWriter, r *http.Request) (*db.DB, *db.MCPServer) {
	pg := s.pg(w)
	if pg == nil {
		return nil, nil
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "invalid MCP server id")
		return nil, nil
	}
	servers, err := pg.ListMCP()
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, nil
	}
	for _, m := range servers {
		if m.ID == id {
			return pg, m
		}
	}
	writeErr(w, 404, "MCP server not found")
	return nil, nil
}

func (s *Server) pgMCPToolVisibility(w http.ResponseWriter, r *http.Request) {
	pg, m := s.mcpPolicyTarget(w, r)
	if pg == nil {
		return
	}
	policies, err := pg.MCPToolOverrides(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"policies": policies})
}

func (s *Server) pgSetMCPToolVisibility(w http.ResponseWriter, r *http.Request) {
	pg, m := s.mcpPolicyTarget(w, r)
	if pg == nil {
		return
	}
	var body struct {
		AgentID  int64  `json:"agent_id,string"`
		ToolName string `json:"tool_name"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if body.AgentID <= 0 || body.ToolName == "" || body.Enabled == nil {
		writeErr(w, 400, "agent_id, tool_name and enabled are required")
		return
	}
	// Only discovered tools can be configured; this also prevents the empty
	// server-wide visibility key from being written through the tool endpoint.
	tools, err := pg.MCPToolNames(m.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	known := false
	for _, name := range tools {
		if name == body.ToolName {
			known = true
			break
		}
	}
	if !known {
		writeErr(w, 400, "tool not found in MCP cache; refresh discovery first")
		return
	}
	name := strings.TrimPrefix(body.ToolName, "mcp__"+m.Name+"__")
	if name == "" {
		writeErr(w, 400, "empty tool name")
		return
	}
	if err := pg.SetMCPToolOverride(body.AgentID, m.ID, name, *body.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
