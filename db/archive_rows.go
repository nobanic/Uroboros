package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type archiveJSONRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}
type archiveRowSource interface {
	Query(string, ...any) (*sql.Rows, error)
}

// A sink receives the original row bytes without reparsing or collecting the
// source table in PostgreSQL. On any failure the returned count is zero, even
// when a streaming writer has already accepted a prefix.
func consumeArchiveRows(rows archiveJSONRows, accept func([]byte) error) (int64, error) {
	var accepted int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		if err := accept(raw); err != nil {
			return 0, err
		}
		accepted++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return accepted, nil
}

type archiveRowQuery struct {
	source    archiveRowSource
	statement string
	values    []any
}

func (query archiveRowQuery) open() (*sql.Rows, error) {
	return query.source.Query("SELECT row_to_json(row_data)::text FROM ("+query.statement+") row_data", query.values...)
}
func (query archiveRowQuery) array() (json.RawMessage, int64, error) {
	rows, err := query.open()
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return archiveRowArray(rows)
}
func (query archiveRowQuery) sequence(writer io.Writer) (int64, error) {
	rows, err := query.open()
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	return archiveRowSequence(rows, writer)
}

func archiveRowArray(rows archiveJSONRows) (json.RawMessage, int64, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	first := true
	count, err := consumeArchiveRows(rows, func(raw []byte) error {
		if !first {
			output.WriteByte(',')
		}
		first = false
		output.Write(raw)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	output.WriteByte(']')
	return json.RawMessage(output.Bytes()), count, nil
}

func archiveWriteResult(written, expected int, err error) error {
	if err != nil {
		return err
	}
	if written != expected {
		return io.ErrShortWrite
	}
	return nil
}
func archiveRowSequence(rows archiveJSONRows, writer io.Writer) (int64, error) {
	return consumeArchiveRows(rows, func(raw []byte) error {
		written, err := writer.Write(raw)
		if err := archiveWriteResult(written, len(raw), err); err != nil {
			return err
		}
		written, err = io.WriteString(writer, "\n")
		return archiveWriteResult(written, 1, err)
	})
}

type archiveInput []byte

// Array inspection uses Unmarshal (including its trailing-data rejection).
// Object restoration keeps Decoder.UseNumber and its original first-value-only
// behavior. Combining those two policies would change legacy archive handling.
func (input archiveInput) array() []json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil
	}
	return items
}
func (input archiveInput) first() json.RawMessage {
	items := input.array()
	if len(items) == 0 {
		return json.RawMessage("{}")
	}
	return items[0]
}
func (input archiveInput) objects() ([]map[string]any, error) {
	if len(input) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var items []map[string]any
	if err := decoder.Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

func archiveRestoreDefaults(table string) map[string]json.RawMessage {
	switch table {
	case "side_question_sessions":
		return map[string]json.RawMessage{"memory": json.RawMessage("{}")}
	case "side_question_requests":
		return map[string]json.RawMessage{"context_info": json.RawMessage("{}")}
	case "katana_discoveries":
		return map[string]json.RawMessage{"technologies": json.RawMessage("[]"), "knowledge_base": json.RawMessage("{}")}
	}
	return nil
}
func (input archiveInput) compatible(table string) (archiveInput, error) {
	defaults := archiveRestoreDefaults(table)
	if len(input) == 0 || defaults == nil {
		return input, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		for field, fallback := range defaults {
			if len(item[field]) == 0 || string(item[field]) == "null" {
				item[field] = fallback
			}
		}
	}
	return json.Marshal(items)
}

// Only source-owned table names may enter the restore SQL. Keep compatibility
// decoding before the allowlist and empty-array check, as in older archives.
func restoreArchiveInput(tx *sql.Tx, table string, raw archiveInput) error {
	compatible, err := raw.compatible(table)
	if err != nil {
		return err
	}
	switch table {
	case "exploration_nodes", "exploration_edges", "exploration_anchors", "task_constraints", "activity", "task_asset_links", "findings",
		"llm_records", "llm_usage", "skill_usage", "tool_usage", "side_question_sessions", "side_question_requests",
		"finding_experiments", "finding_retests", "finding_experiment_replays", "katana_jobs", "katana_discoveries", "discovery_reviews",
		"arsenal_jobs", "arsenal_observations", "binary_artifacts", "binary_artifact_origins", "binary_triage_jobs", "binary_observations",
		"archive_provider_grants", "archive_provider_artifacts", "research_disclosure_grants", "research_egress_grants", "research_artifacts":
	default:
		return fmt.Errorf("archive restore table %q is not allowed", table)
	}
	if len(compatible.array()) == 0 {
		return nil
	}
	_, err = tx.Exec("INSERT INTO "+table+" SELECT * FROM json_populate_recordset(NULL::"+table+",$1::json)", string(compatible))
	return err
}

func restoreArchiveSequence(tx *sql.Tx, table string, reader io.Reader) (int64, error) {
	if table != "llm_records" {
		return 0, fmt.Errorf("archive streamed restore table %q is not allowed", table)
	}
	statement, err := tx.Prepare(`INSERT INTO llm_records SELECT * FROM json_populate_record(NULL::llm_records,$1::json)`)
	if err != nil {
		return 0, err
	}
	defer statement.Close()
	decoder := json.NewDecoder(reader)
	var restored int64
	for {
		var object json.RawMessage
		failure := decoder.Decode(&object)
		if errors.Is(failure, io.EOF) {
			return restored, nil
		}
		if failure != nil {
			return 0, failure
		}
		object = bytes.TrimSpace(object)
		if len(object) == 0 || object[0] != '{' {
			return 0, errors.New("streamed archive row must be a JSON object")
		}
		if _, err := statement.Exec(string(object)); err != nil {
			return 0, err
		}
		restored++
	}
}
