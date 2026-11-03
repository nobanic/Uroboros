package db

// Metadata columns are declared once for list/detail so their defaults and scan
// order cannot diverge. Bodies remain excluded from the list projection.
func executionLLMColumns(record *LLMRecord) []recordField {
	return []recordField{
		field("id", &record.ID), field("ts", &record.Ts),
		field("model", &record.Model).defaultRead("''"), field("profile_name", &record.ProfileName).defaultRead("''"),
		field("session_id", &record.SessionID).defaultRead("''"), field("task_id", &record.TaskID).defaultRead("''"), field("worker", &record.Worker).defaultRead("''"),
		field("latency_ms", &record.LatencyMs).defaultRead("0"), field("input_tokens", &record.InputTokens).defaultRead("0"), field("output_tokens", &record.OutputTokens).defaultRead("0"),
		field("cache_read", &record.CacheRead).defaultRead("0"), field("cache_write", &record.CacheWrite).defaultRead("0"),
		field("status", &record.Status).defaultRead("''"), field("error", &record.Error).defaultRead("''"),
	}
}
func executionLLMBodyColumns(record *LLMRecord) []recordField {
	return append(executionLLMColumns(record), readNullableText("request_body", &record.RequestBody), readNullableText("response_body", &record.ResponseBody), readNullableText("raw_request", &record.RawRequest), readNullableText("raw_response", &record.RawResponse))
}
func executionLLMCriteria(model, session, task string) recordCriteria {
	return compileRecordCriteria("true", recordEquals("model", model, model != ""), recordLikeAny([]string{"session_id"}, "%"+session+"%", session != ""), recordEquals("COALESCE(task_id,'')", task, task != ""))
}

var executionLLMSummary = recordSelect[LLMRecord]{from: "FROM llm_records", orderBy: "id DESC", fields: executionLLMColumns, nonNil: true}
var executionLLMDetail = recordSelect[LLMRecord]{from: "FROM llm_records", fields: executionLLMBodyColumns}
var executionLLMTasks = recordSelect[LLMTask]{from: "FROM llm_records", groupBy: "task_id", orderBy: "MAX(id) DESC", fields: func(task *LLMTask) []recordField {
	return []recordField{field("task_id", &task.TaskID), field("COUNT(*) AS n", &task.Count)}
}}

// This deliberately retains the original tool_use_id-only join, including its
// repeated-ID multiplicity and count/data distinction. Pairing repair is separate.
const executionCommandJoin = "FROM activity u LEFT JOIN activity r ON r.tool_use_id = u.tool_use_id AND r.kind = 'tool_result'"

func executionCommandCriteria(exploration *int64, keyword string) recordCriteria {
	var id any
	if exploration != nil {
		id = *exploration
	}
	return compileRecordCriteria("u.kind = 'tool_use'", recordEquals("u.exploration_id", id, exploration != nil), recordLikeAny([]string{"u.tool", "u.detail"}, "%"+keyword+"%", keyword != ""))
}

var executionCommandRows = recordSelect[CommandRecord]{from: executionCommandJoin, countFrom: "FROM activity u", orderBy: "u.id DESC", nonNil: true, fields: func(record *CommandRecord) []recordField {
	return []recordField{
		field("u.id", &record.ID), field("u.exploration_id", &record.ExpID), field("u.worker", &record.Worker).defaultRead("''"), field("u.tool", &record.Tool).defaultRead("''"), field("u.detail", &record.Command).defaultRead("''"),
		field("r.detail", &record.Output).defaultRead("''"), field("r.is_error", &record.IsError).defaultRead("false"), field("u.created_at", &record.CreatedAt),
	}
}}
var executionCommandStats = recordSelect[ToolStat]{from: executionCommandJoin, groupBy: "1", orderBy: "total DESC, tool ASC", nonNil: true, fields: func(stat *ToolStat) []recordField {
	return []recordField{field("COALESCE(NULLIF(u.tool,''),'-') AS tool", &stat.Tool), field("COUNT(*) AS total", &stat.Total), field("COUNT(*) FILTER (WHERE COALESCE(r.is_error,false)) AS errors", &stat.Errors)}
}}
