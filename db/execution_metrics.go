package db

// Activity totals and always-on llm_usage totals intentionally use different
// sources. The shared projection owns conversion, not accounting authority.
func tokenSumFields(prefix string, input, output, read, write *int) []recordField {
	return []recordField{
		field("SUM("+prefix+"input_tokens)", input).defaultRead("0"),
		field("SUM("+prefix+"output_tokens)", output).defaultRead("0"),
		field("SUM("+prefix+"cache_read_tokens)", read).defaultRead("0"),
		field("SUM("+prefix+"cache_write_tokens)", write).defaultRead("0"),
	}
}

func workerTokenSums(row *TokenUsage) []recordField {
	return tokenSumFields("", &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens)
}

type executionAggregate[K comparable, V any] struct {
	key   K
	value V
}

func keyedExecutionMetrics[K comparable, V any](database *DB, from, key, group string, fields func(*V) []recordField, criteria recordCriteria) (map[K]V, error) {
	selection := recordSelect[executionAggregate[K, V]]{from: from, groupBy: group, fields: func(row *executionAggregate[K, V]) []recordField {
		return append([]recordField{field(key, &row.key)}, fields(&row.value)...)
	}}
	values := map[K]V{}
	batch := selection.visit(database, criteria, nil, func(row executionAggregate[K, V]) { values[row.key] = row.value })
	if batch.failure != nil {
		return nil, batch.failure
	}
	return values, batch.iterationError
}

func activityTokenTotal(store *ExplorationStore) (TokenUsage, error) {
	var total TokenUsage
	fields := workerTokenSums(&total)
	err := decodeRecord(store.db.QueryRow("SELECT "+recordProjection(fields)+" FROM activity WHERE exploration_id=$1 AND kind='result'", store.expID), fields)
	return total, err
}

func allActivityTokenTotals(database *DB) (map[int64]TokenUsage, error) {
	return keyedExecutionMetrics[int64](database, "FROM activity", "exploration_id", "exploration_id", workerTokenSums, compileRecordCriteria("kind='result'"))
}

func activityWorkerTotals(store *ExplorationStore) ([]TokenUsage, error) {
	selection := recordSelect[TokenUsage]{from: "FROM activity", groupBy: "worker", orderBy: "worker", nonNil: true, fields: func(row *TokenUsage) []recordField {
		return append([]recordField{field("worker", &row.Worker).defaultRead("''")}, workerTokenSums(row)...)
	}}
	return selection.all(store.db, compileRecordCriteria("kind='result'", recordEquals("exploration_id", store.expID, true)))
}

func activitySessionTotals(store *ExplorationStore) ([]SessionTokenUsage, error) {
	selection := recordSelect[SessionTokenUsage]{
		from: `FROM activity a LEFT JOIN exploration_nodes n
ON n.id=a.node_id AND n.exploration_id=a.exploration_id AND n.kind='intent'`,
		groupBy: "session_key", orderBy: "session_key", nonNil: true, fields: func(row *SessionTokenUsage) []recordField {
			identity := field(`CASE
WHEN COALESCE(a.worker,'')='mainagent' THEN 'main:' || COALESCE(a.main_seg,0)::text
WHEN COALESCE(a.worker,'')='planner' THEN 'plan'
WHEN n.id IS NOT NULL THEN 'intent:' || n.id::text
ELSE '' END AS session_key`, &row.Session)
			return append([]recordField{identity}, tokenSumFields("a.", &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens)...)
		},
	}
	return selection.all(store.db, compileRecordCriteria("a.kind='result' AND (COALESCE(a.worker,'') IN ('mainagent','planner') OR n.id IS NOT NULL)", recordEquals("a.exploration_id", store.expID, true)))
}

func dailyActivityTotals(database *DB, days int) ([]DailyTokenBucket, error) {
	selection := recordSelect[DailyTokenBucket]{from: "FROM activity", groupBy: "day", orderBy: "day", fields: func(row *DailyTokenBucket) []recordField {
		return []recordField{
			field("TO_CHAR(DATE_TRUNC('day', created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day", &row.Day),
			field("SUM(input_tokens)", &row.InputTokens).defaultRead("0"),
			field("SUM(output_tokens)", &row.OutputTokens).defaultRead("0"),
			field("SUM(cache_read_tokens)", &row.CacheReadTokens).defaultRead("0"),
		}
	}}
	criteria := compileRecordCriteria("kind='result'", recordPredicate{present: true, value: activityWindow(days, 30), expression: func(binding string) string {
		return "created_at >= NOW() - (" + binding + " * INTERVAL '1 day')"
	}})
	return selection.all(database, criteria)
}

func conversationTokenTotals(database *DB) ([]ConvTokenSummary, error) {
	selection := recordSelect[ConvTokenSummary]{from: `FROM conversations c
LEFT JOIN conversation_activities ca ON ca.conversation_id = c.id AND ca.kind = 'result'`, groupBy: "c.id", nonNil: true,
		fields: func(row *ConvTokenSummary) []recordField {
			columns := []recordField{field("c.llm_profile_id", &row.LLMProfileID), field("c.created_at::text", &row.CreatedAt)}
			return append(columns, tokenSumFields("ca.", &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens)...)
		},
	}
	return selection.all(database, compileRecordCriteria("true"))
}

func persistedActivityTimes(database *DB) (map[int64]int64, error) {
	return keyedExecutionMetrics[int64](database, "FROM activity", "exploration_id", "exploration_id", func(value *int64) []recordField {
		return []recordField{field("EXTRACT(EPOCH FROM MAX(created_at))::bigint", value)}
	}, compileRecordCriteria("true"))
}

func persistedGoalCounts(database *DB) (map[int64]GoalCounts, error) {
	return keyedExecutionMetrics[int64](database, "FROM exploration_nodes", "exploration_id", "exploration_id", func(row *GoalCounts) []recordField {
		return []recordField{field("COUNT(*)", &row.Total), field("COUNT(*) FILTER (WHERE state = 'met')", &row.Met)}
	}, compileRecordCriteria("kind='goal'"))
}

func taskMetricFields(row *TaskListMetrics) []recordField {
	return []recordField{
		field("token_metrics.input_tokens", &row.Tokens.InputTokens).defaultRead("0"),
		field("token_metrics.output_tokens", &row.Tokens.OutputTokens).defaultRead("0"),
		field("token_metrics.cache_read_tokens", &row.Tokens.CacheReadTokens).defaultRead("0"),
		field("token_metrics.cache_write_tokens", &row.Tokens.CacheWriteTokens).defaultRead("0"),
		field("latest_activity.created_at", &row.LastActivity).defaultRead("0"),
		field("goal_metrics.total", &row.Goals.Total).defaultRead("0"),
		field("goal_metrics.met", &row.Goals.Met).defaultRead("0"),
		field("intent_metrics.running", &row.RunningIntents).defaultRead("0"),
		field("finding_metrics.critical", &row.Findings.Critical).defaultRead("0"),
		field("finding_metrics.high", &row.Findings.High).defaultRead("0"),
		field("finding_metrics.medium", &row.Findings.Medium).defaultRead("0"),
		field("finding_metrics.low", &row.Findings.Low).defaultRead("0"),
	}
}

// Keep indexed lateral sources and the OR attribution rule unchanged.
const taskMetricSources = `FROM tasks task
		LEFT JOIN LATERAL (
			SELECT SUM(input_tokens) AS input_tokens,
			       SUM(output_tokens) AS output_tokens,
			       SUM(cache_read) AS cache_read_tokens,
			       SUM(cache_write) AS cache_write_tokens
			FROM llm_usage
			WHERE COALESCE(task_id,'')=task.id::text OR exploration_id=task.exploration_id
		) token_metrics ON true
		LEFT JOIN LATERAL (
			SELECT EXTRACT(EPOCH FROM created_at)::bigint AS created_at
			FROM activity
			WHERE exploration_id=task.exploration_id
			ORDER BY created_at DESC
			LIMIT 1
		) latest_activity ON true
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS total,
			       COUNT(*) FILTER (WHERE state='met') AS met
			FROM exploration_nodes
			WHERE exploration_id=task.exploration_id AND kind='goal'
		) goal_metrics ON true
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS running
			FROM exploration_nodes
			WHERE exploration_id=task.exploration_id AND kind='intent' AND state='running'
		) intent_metrics ON true
		LEFT JOIN LATERAL (
			SELECT COUNT(*) FILTER (WHERE severity='critical') AS critical,
			       COUNT(*) FILTER (WHERE severity='high')     AS high,
			       COUNT(*) FILTER (WHERE severity='medium')   AS medium,
			       COUNT(*) FILTER (WHERE severity='low')      AS low
			FROM findings
			WHERE task_id=task.id
		) finding_metrics ON true`
