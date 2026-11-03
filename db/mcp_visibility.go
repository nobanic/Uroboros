package db

// Tool overrides narrow access granted by an MCP visibility row or a skill.
// No override preserves existing access. A tool override never grants access to
// its server. Store the remote tool name, independent of the server namespace.
func (d *DB) AgentMCPToolOverrides(agentID int64) (map[int64]map[string]bool, error) {
	return (agentCatalog{database: d}).agentOverrides(agentID)
}

func (d *DB) MCPToolOverrides(serverID int64) (map[string]map[string]bool, error) {
	return (agentCatalog{database: d}).serverOverrides(serverID)
}

func (d *DB) SetMCPToolOverride(agentID, serverID int64, name string, enabled bool) error {
	return (agentCatalog{database: d}).override(agentID, serverID, name, enabled)
}
