package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
)

// The caller lends its existing repeatable-read transaction. This owner neither
// starts nor commits a transaction and retains snapshot query/error order.
type archiveUsageSnapshot struct {
	transaction       *sql.Tx
	task, exploration int64
}
type archiveUsageQuery struct {
	name, statement string
	values          []any
}

const archiveUsageLLMScope = `FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2`

func archiveSkillUsageSQL(missing bool) string {
	tasks, filter := 1, ""
	if missing {
		tasks, filter = 0, "NOT "
	}
	return fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT skill,count(*) calls,%d tasks,
       COALESCE(array_agg(DISTINCT agent_key) FILTER (WHERE agent_key IS NOT NULL),ARRAY[]::text[]) agents,
       max(ts) last_used
FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND %sfound GROUP BY skill) x`, tasks, filter)
}

func (snapshot archiveUsageSnapshot) queries() []archiveUsageQuery {
	llm := []any{strconv.FormatInt(snapshot.task, 10), snapshot.exploration}
	execution := []any{snapshot.task, snapshot.exploration}
	task := []any{snapshot.task}
	return []archiveUsageQuery{
		{"token_profiles", `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT COALESCE(profile_name,'') profile_name,count(*) calls,1 tasks,
       COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
       COALESCE(sum(cache_read),0) cache_read_tokens,COALESCE(sum(cache_write),0) cache_write_tokens
` + archiveUsageLLMScope + `
GROUP BY profile_name ORDER BY sum(input_tokens)+sum(output_tokens) DESC) x`, llm},
		{"token_daily", `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT COALESCE(profile_name,'') profile_name,to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD') date,
       COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
       COALESCE(sum(cache_read),0) cache_read_tokens
` + archiveUsageLLMScope + `
GROUP BY profile_name,date ORDER BY date) x`, llm},
		{"skills", `SELECT COALESCE(jsonb_object_agg(name,n),'{}'::jsonb) FROM (SELECT skill name,count(*) n FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND found GROUP BY skill) x`, execution},
		{"skill_stats", archiveSkillUsageSQL(false), execution},
		{"missing_skill_stats", archiveSkillUsageSQL(true), execution},
		{"tools", `SELECT COALESCE(jsonb_object_agg(name,n),'{}'::jsonb) FROM (SELECT tool_key name,count(*) n FROM tool_usage WHERE task_id=$1 OR exploration_id=$2 GROUP BY tool_key) x`, execution},
		{"findings", `SELECT COALESCE(jsonb_object_agg(name,n),'{}'::jsonb) FROM (SELECT COALESCE(NULLIF(severity,''),'unknown') name,count(*) n FROM findings WHERE task_id=$1 GROUP BY severity) x`, task},
		{"finding_stats", `SELECT jsonb_build_object(
 'total',count(*),'pending',count(*) FILTER (WHERE status='pending'),
 'critical',count(*) FILTER (WHERE severity='critical'),'high',count(*) FILTER (WHERE severity='high'),
 'medium',count(*) FILTER (WHERE severity='medium'),'low',count(*) FILTER (WHERE severity='low'),
 'vulnclasses',COALESCE(jsonb_agg(DISTINCT vulnclass) FILTER (WHERE vulnclass<>''),'[]'::jsonb))
FROM findings WHERE task_id=$1`, task},
	}
}

func (snapshot archiveUsageSnapshot) readJSON(query archiveUsageQuery) (any, error) {
	var encoded []byte
	if err := snapshot.transaction.QueryRow(query.statement, query.values...).Scan(&encoded); err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (snapshot archiveUsageSnapshot) read() (map[string]any, error) {
	var counts [5]int64
	if err := snapshot.transaction.QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),
COALESCE(sum(cache_read),0),COALESCE(sum(cache_write),0)
`+archiveUsageLLMScope, strconv.FormatInt(snapshot.task, 10), snapshot.exploration).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4]); err != nil {
		return nil, err
	}
	result := map[string]any{"tokens": map[string]int64{"calls": counts[0], "input_tokens": counts[1], "output_tokens": counts[2], "cache_read_tokens": counts[3], "cache_write_tokens": counts[4]}}
	for _, query := range snapshot.queries() {
		value, err := snapshot.readJSON(query)
		if err != nil {
			return nil, err
		}
		result[query.name] = value
	}
	return result, nil
}
