package db

import (
	"database/sql"
	"encoding/json"
	"slices"
)

// The two execution logs share text and token codecs. Task-only metadata,
// intent identity and main-segment identity never enter conversation storage.
// Read defaults and write normalization belong to the same authored field.
func journalText(name string, target *string) recordField {
	column := field(name, target).nullableText().defaultRead("''")
	column.value = func() any { return utf8Clean(*target) }
	return column
}

func journalContent(row *Activity) []recordField {
	return []recordField{
		journalText("worker", &row.Worker), journalText("kind", &row.Kind),
		journalText("tool", &row.Tool), journalText("tool_use_id", &row.ToolUseID),
		field("is_error", &row.IsError), journalText("summary", &row.Summary),
		journalText("detail", &row.Detail),
	}
}

func journalTokens(row *Activity) []recordField {
	return []recordField{
		field("input_tokens", &row.InputTokens), field("output_tokens", &row.OutputTokens),
		field("cache_read_tokens", &row.CacheReadTokens), field("cache_write_tokens", &row.CacheWriteTokens),
	}
}

func appendExecutionStep(database *DB, scopeID int64, taskLog bool, step Activity) (int64, error) {
	table, scope := "conversation_activities", "conversation_id"
	columns := []recordField{field(scope, &scopeID)}
	if taskLog {
		table, scope = "activity", "exploration_id"
		columns = []recordField{field(scope, &scopeID), field("node_id", &step.NodeID)}
	}
	columns = append(columns, journalContent(&step)...)
	if taskLog {
		if len(step.Metadata) == 0 {
			step.Metadata = json.RawMessage(`{}`)
		}
		// Retain the RawMessage driver value and its original rejection behavior.
		// jsonbClean is deliberately not a policy of this append API.
		columns = append(columns, field("metadata", &step.Metadata))
	}
	columns = append(columns, journalTokens(&step)...)
	if taskLog {
		columns = append(columns, field("main_seg", &step.MainSeg))
	}
	return persistRecord(database, table, 0, columns)
}

func conversationStepFields(row *Activity) []recordField {
	columns := []recordField{field("id", &row.ID)}
	for _, column := range journalContent(row) {
		if column.name != "detail" {
			columns = append(columns, column)
		}
	}
	columns = append(columns, field("created_at", &row.CreatedAt))
	return append(columns, journalTokens(row)...)
}

func conversationSteps() recordSelect[Activity] {
	return recordSelect[Activity]{from: "FROM conversation_activities", fields: conversationStepFields, nonNil: true, orderBy: "id"}
}

func conversationIncrement(database *DB, conversationID, since int64, limit int) ([]Activity, int64, error) {
	criteria := compileRecordCriteria("true", recordEquals("conversation_id", conversationID, true), recordPredicate{
		present: true, value: since, expression: func(binding string) string { return "id > " + binding },
	})
	window := recordPage{size: activityWindow(limit, 500)}
	batch := conversationSteps().collect(database, criteria, &window)
	if batch.failure != nil {
		return nil, since, batch.failure
	}
	cursor := since
	for _, row := range batch.items {
		if row.ID > cursor {
			cursor = row.ID
		}
	}
	return batch.items, cursor, batch.iterationError
}

func conversationHistory(database *DB, conversationID, before int64, limit int) ([]Activity, bool, error) {
	capacity := activityWindow(limit, 200)
	criteria := compileRecordCriteria("true", recordEquals("conversation_id", conversationID, true), recordPredicate{
		present: true, value: before, expression: func(binding string) string { return "(" + binding + " <= 0 OR id < " + binding + ")" },
	})
	selection := conversationSteps()
	selection.orderBy = "id DESC"
	window := recordPage{size: capacity + 1}
	batch := selection.collect(database, criteria, &window)
	if batch.failure != nil {
		return nil, false, batch.failure
	}
	if batch.iterationError != nil {
		return nil, false, batch.iterationError
	}
	more := len(batch.items) > capacity
	if more {
		batch.items = batch.items[:capacity]
	}
	slices.Reverse(batch.items)
	return batch.items, more, nil
}

func executionStepDetail(database *DB, taskLog bool, scopeID, id int64) (string, error) {
	table, scope := "conversation_activities", "conversation_id"
	if taskLog {
		table, scope = "activity", "exploration_id"
	}
	var text string
	column := readNullableText("detail", &text)
	err := decodeRecord(database.QueryRow("SELECT detail FROM "+table+" WHERE id=$1 AND "+scope+"=$2", id, scopeID), []recordField{column})
	if err == sql.ErrNoRows {
		return "", nil
	}
	return text, err
}

func mainSessionFields(row *MainSession) []recordField {
	return []recordField{field("seq", &row.Seq), field("created_at", &row.CreatedAt)}
}

func mainSessionHistory(store *ExplorationStore) ([]MainSession, error) {
	selection := recordSelect[MainSession]{from: "FROM main_sessions", fields: mainSessionFields, orderBy: "seq DESC", nonNil: true}
	rows, err := selection.all(store.db, compileRecordCriteria("true", recordEquals("exploration_id", store.expID, true)))
	if err != nil {
		return nil, err
	}
	return append(rows, MainSession{Seq: 0}), nil
}

func allocateMainSession(store *ExplorationStore) (MainSession, error) {
	var row MainSession
	err := decodeRecord(store.db.QueryRow(`
INSERT INTO main_sessions(exploration_id, seq)
VALUES ($1, COALESCE((SELECT MAX(seq) FROM main_sessions WHERE exploration_id=$1),0)+1)
RETURNING seq, created_at`, store.expID), mainSessionFields(&row))
	return row, err
}
