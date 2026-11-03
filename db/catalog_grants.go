package db

import (
	"database/sql"
	"fmt"
	"strconv"
)

const catalogSkillGrant = `INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled) VALUES ($1,$2,true)
ON CONFLICT (agent_id,skill_name) DO UPDATE SET enabled=true`
const catalogServerGrant = `INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,enabled) VALUES ($1,$2,$3,true)
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=true`

func (catalog agentCatalog) skills(agentID int64) ([]string, error) {
	return catalogColumn[string](catalog, `SELECT skill_name FROM agent_skill_visibility WHERE agent_id=$1 AND enabled ORDER BY skill_name`, agentID)
}
func (catalog agentCatalog) skillAgents(name string) ([]int64, error) {
	return catalogColumn[int64](catalog, `SELECT agent_id FROM agent_skill_visibility WHERE skill_name=$1 AND enabled`, name)
}
func (catalog agentCatalog) replaceSkills(agentID int64, names []string) error {
	return (catalogReplacement[string]{
		erase: `DELETE FROM agent_skill_visibility WHERE agent_id=$1`, scope: []any{agentID},
		insert: catalogSkillGrant, entries: names, bind: func(name string) []any { return []any{agentID, name} },
	}).apply(catalog)
}
func (catalog agentCatalog) toggleSkill(agentID int64, name string, enabled bool) error {
	if enabled {
		return catalog.write(catalogSkillGrant, agentID, name)
	}
	return catalog.write(`DELETE FROM agent_skill_visibility WHERE agent_id=$1 AND skill_name=$2`, agentID, name)
}
func (catalog agentCatalog) deleteSkill(name string) error {
	return catalog.write(`DELETE FROM agent_skill_visibility WHERE skill_name=$1`, name)
}

func (catalog agentCatalog) servers(agentID int64, kind string) ([]int64, error) {
	return catalogColumn[int64](catalog, `SELECT resource_id FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND mcp_tool_name='' AND enabled`, agentID, kind)
}
func (catalog agentCatalog) resourceAgents(kind string, id int64) ([]int64, error) {
	return catalogColumn[int64](catalog, `SELECT agent_id FROM agent_visibility WHERE resource_kind=$1 AND resource_id=$2 AND mcp_tool_name='' AND enabled`, kind, id)
}
func (catalog agentCatalog) replaceServers(agentID int64, kind string, ids []int64) error {
	return (catalogReplacement[int64]{
		erase: `DELETE FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND mcp_tool_name=''`, scope: []any{agentID, kind},
		insert: catalogServerGrant, entries: ids, bind: func(id int64) []any { return []any{agentID, kind, id} },
	}).apply(catalog)
}
func (catalog agentCatalog) toggleServer(agentID int64, kind string, id int64, enabled bool) error {
	if enabled {
		return catalog.write(catalogServerGrant, agentID, kind, id)
	}
	return catalog.write(`DELETE FROM agent_visibility WHERE agent_id=$1 AND resource_kind=$2 AND resource_id=$3 AND mcp_tool_name=''`, agentID, kind, id)
}

func (catalog agentCatalog) toolNames(serverID int64) ([]string, error) {
	return catalogColumn[string](catalog, `SELECT tool_name FROM mcp_tools_cache WHERE server_id=$1 ORDER BY tool_name`, serverID)
}
func (catalog agentCatalog) tools(serverID int64) ([]MCPTool, error) {
	return catalogRead(catalog, `SELECT tool_name, COALESCE(description,'') FROM mcp_tools_cache WHERE server_id=$1 ORDER BY tool_name`, []any{serverID}, []MCPTool(nil), func(values *[]MCPTool, row rowDecoder) error {
		var tool MCPTool
		if err := row.Scan(&tool.Name, &tool.Description); err != nil {
			return err
		}
		*values = append(*values, tool)
		return nil
	})
}
func (catalog agentCatalog) replaceTools(serverID int64, tools []MCPTool) error {
	return (catalogReplacement[MCPTool]{
		erase: `DELETE FROM mcp_tools_cache WHERE server_id=$1`, scope: []any{serverID},
		insert: `INSERT INTO mcp_tools_cache(server_id, tool_name, description) VALUES ($1,$2,$3)
ON CONFLICT (server_id, tool_name) DO UPDATE SET description=EXCLUDED.description`,
		entries: tools, bind: func(tool MCPTool) []any { return []any{serverID, tool.Name, tool.Description} },
	}).apply(catalog)
}
func (catalog agentCatalog) deleteServer(id int64) error {
	return catalog.transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM agent_visibility WHERE resource_kind='mcp' AND resource_id=$1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM mcp_servers WHERE id=$1`, id)
		return err
	})
}

func catalogOverrides[K comparable](catalog agentCatalog, statement string, id int64, key func(int64) K) (map[K]map[string]bool, error) {
	return catalogRead(catalog, statement, []any{id}, map[K]map[string]bool{}, func(groups *map[K]map[string]bool, row rowDecoder) error {
		var ownerID int64
		var name string
		var enabled bool
		if err := row.Scan(&ownerID, &name, &enabled); err != nil {
			return err
		}
		owner := key(ownerID)
		if (*groups)[owner] == nil {
			(*groups)[owner] = map[string]bool{}
		}
		(*groups)[owner][name] = enabled
		return nil
	})
}
func (catalog agentCatalog) agentOverrides(agentID int64) (map[int64]map[string]bool, error) {
	return catalogOverrides(catalog, `SELECT resource_id,mcp_tool_name,enabled FROM agent_visibility
WHERE agent_id=$1 AND resource_kind='mcp' AND mcp_tool_name<>''`, agentID, func(id int64) int64 { return id })
}
func (catalog agentCatalog) serverOverrides(serverID int64) (map[string]map[string]bool, error) {
	return catalogOverrides(catalog, `SELECT agent_id,mcp_tool_name,enabled FROM agent_visibility
WHERE resource_kind='mcp' AND resource_id=$1 AND mcp_tool_name<>''`, serverID, func(id int64) string { return strconv.FormatInt(id, 10) })
}
func (catalog agentCatalog) override(agentID, serverID int64, name string, enabled bool) error {
	if agentID <= 0 || serverID <= 0 || name == "" {
		return fmt.Errorf("agent, server and nonempty remote tool name are required")
	}
	return catalog.write(`INSERT INTO agent_visibility(agent_id,resource_kind,resource_id,mcp_tool_name,enabled)
VALUES ($1,'mcp',$2,$3,$4)
ON CONFLICT (agent_id,resource_kind,resource_id,mcp_tool_name) DO UPDATE SET enabled=EXCLUDED.enabled`, agentID, serverID, name, enabled)
}
