package db

import (
	"database/sql"
	"encoding/json"
)

func toolFields(tool *Tool, agents *json.RawMessage) []recordField {
	return []recordField{
		field("key", &tool.Key), field("system", &tool.System), field("description", &tool.Description),
		field("schema", &tool.Schema), field("agents", agents), field("enabled", &tool.Enabled),
		field("kind", &tool.Kind), field("exec", &tool.Exec), field("deferred", &tool.Deferred),
	}
}

func decodeTool(row rowDecoder) (*Tool, error) {
	tool, agents := new(Tool), json.RawMessage(nil)
	if failure := decodeRecord(row, toolFields(tool, &agents)); failure != nil {
		return nil, failure
	}
	if len(agents) > 0 {
		_ = json.Unmarshal(agents, &tool.Agents)
	}
	if tool.Agents == nil {
		tool.Agents = []string{}
	}
	return tool, nil
}

func toolProjection() string {
	var agents json.RawMessage
	return recordProjection(toolFields(new(Tool), &agents))
}

func (database *DB) readTools(customOnly bool) ([]*Tool, error) {
	statement := "SELECT " + toolProjection() + " FROM tools"
	if customOnly {
		statement += " WHERE NOT system"
	}
	return graphRows(database, statement+" ORDER BY key", nil, decodeTool)
}

func (database *DB) readTool(key string) (*Tool, error) {
	tool, failure := decodeTool(database.QueryRow("SELECT "+toolProjection()+" FROM tools WHERE key = $1", key))
	if failure == sql.ErrNoRows {
		return nil, nil
	}
	return tool, failure
}

func toolJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("{}")
	}
	return value
}

func (database *DB) writeCustomTool(input *Tool, create bool) error {
	tool := *input
	tool.System, tool.Schema, tool.Exec = false, toolJSON(tool.Schema), toolJSON(tool.Exec)
	agents, _ := json.Marshal(tool.Agents)
	encoded := json.RawMessage(agents) // nil slices intentionally retain the historic JSON null write.
	fields := toolFields(&tool, &encoded)
	if !create {
		fields = fields[2:]
	} // stable key and system ownership never change.
	return persistKeyedRecord(database, "tools", field("key", &tool.Key), fields, create, "NOT system")
}

func (database *DB) writeToolMetadata(key, description string, schema, agents json.RawMessage, enabled bool) error {
	if len(agents) == 0 {
		agents = json.RawMessage("[]")
	}
	tool := Tool{Key: key, Description: description, Schema: toolJSON(schema), Enabled: enabled}
	fields := toolFields(&tool, &agents)[2:6]
	return persistKeyedRecord(database, "tools", field("key", &tool.Key), fields, false, "")
}
