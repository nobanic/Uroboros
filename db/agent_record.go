package db

import "database/sql"

// List/detail and INSERT RETURNING share the complete persisted agent schema.
// Runtime binding changes and their lock order stay in SetAgentLLMProfile.
func agentFields(agent *Agent) []recordField {
	return []recordField{
		field("id", &agent.ID).immutable(), field("key", &agent.Key), field("name", &agent.Name),
		field("description", &agent.Description).defaultRead("''").nullableText(),
		field("role", &agent.Role), field("builtin", &agent.Builtin), field("enabled", &agent.Enabled),
		field("max_turns", &agent.MaxTurns).defaultRead("0"), field("run_seconds", &agent.RunSecs).defaultRead("600"),
		field("web_search", &agent.WebSearch).defaultRead("false"), field("interactive_shell", &agent.InteractiveShell).defaultRead("false"),
		field("wrapup_prompt", &agent.WrapupPrompt).defaultRead("''"), field("wrapup_max_turns", &agent.WrapupMaxTurns).defaultRead("0"),
		field("task_timeout_wrapup_prompt", &agent.TaskTimeoutWrapupPrompt).defaultRead("''"),
		field("task_timeout_wrapup_max_turns", &agent.TaskTimeoutWrapupMaxTurns).defaultRead("0"),
		field("trigger_run_mode", &agent.TriggerRunMode).defaultRead("'serial'"),
		field("trigger_merge_mode", &agent.TriggerMergeMode).defaultRead("'all'"),
		field("trigger_max_parallel", &agent.TriggerMaxParallel).defaultRead("5"), field("llm_profile_id", &agent.LLMProfileID),
	}
}

func decodeAgent(row rowDecoder) (*Agent, error) {
	agent := new(Agent)
	failure := decodeRecord(row, agentFields(agent))
	return agent, failure
}

func (database *DB) readAgents() ([]*Agent, error) {
	statement := "SELECT " + recordProjection(agentFields(new(Agent))) + " FROM agents ORDER BY id"
	return graphRows(database, statement, nil, decodeAgent)
}

func (database *DB) readAgent(key string) (*Agent, error) {
	statement := "SELECT " + recordProjection(agentFields(new(Agent))) + " FROM agents WHERE key = $1"
	agent, failure := decodeAgent(database.QueryRow(statement, key))
	if failure == sql.ErrNoRows {
		return nil, nil
	}
	if failure != nil {
		return nil, failure
	}
	return agent, nil
}

func (database *DB) insertAgent(key, name, description string) (*Agent, error) {
	// Preserve PostgreSQL defaults and the atomic inserted snapshot. A second
	// SELECT could race with a concurrent update or deletion.
	statement := "INSERT INTO agents (key, name, description, role, builtin, enabled) " +
		"VALUES ($1, $2, NULLIF($3, ''), 'assistant', false, true) RETURNING " + recordProjection(agentFields(new(Agent)))
	agent, failure := decodeAgent(database.QueryRow(statement, key, name, description))
	if failure != nil {
		return nil, failure
	}
	return agent, nil
}
