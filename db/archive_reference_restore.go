package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type archiveReferencePolicy struct {
	field, table, warning string
	required, live        bool
}

func (policy archiveReferencePolicy) missing(tx *sql.Tx, row map[string]any) (int64, bool) {
	id, ok := jsonInt64(row[policy.field])
	if !ok {
		return id, policy.required
	}
	if !policy.required && id <= 0 {
		return id, false
	}
	var exists bool
	if policy.live {
		exists = liveTaskExists(tx, id)
	} else {
		exists = rowExists(tx, policy.table, id)
	}
	return id, !exists
}

type archiveReferenceRows struct {
	tx       *sql.Tx
	rows     []map[string]any
	warnings []string
}

func readArchiveReferences(tx *sql.Tx, raw json.RawMessage) (archiveReferenceRows, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return archiveReferenceRows{}, err
	}
	return archiveReferenceRows{tx: tx, rows: rows, warnings: []string{}}, nil
}
func (rows *archiveReferenceRows) visit(policy archiveReferencePolicy, accept func(map[string]any, int64) error) error {
	for _, row := range rows.rows {
		id, missing := policy.missing(rows.tx, row)
		if missing {
			rows.warnings = append(rows.warnings, fmt.Sprintf(policy.warning, id))
			continue
		}
		if err := accept(row, id); err != nil {
			return err
		}
	}
	return nil
}
func executeArchiveReferenceRows(tx *sql.Tx, statement string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(rows)
	_, err := tx.Exec(statement, string(encoded))
	return err
}
func (rows archiveReferenceRows) relations(taskID int64) ([]string, error) {
	policy := archiveReferencePolicy{field: "source_task_id", live: true, required: true, warning: "source task %d is unavailable; inheritance relation skipped"}
	err := rows.visit(policy, func(row map[string]any, sourceID int64) error {
		_, err := rows.tx.Exec(`INSERT INTO task_relations(task_id,source_task_id,created_at) VALUES($1,$2,COALESCE($3::timestamptz,now())) ON CONFLICT DO NOTHING`, taskID, sourceID, nilIfEmptyString(archiveRowString(row, "created_at")))
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows.warnings, nil
}
func (rows archiveReferenceRows) scopes() ([]string, error) {
	kept := make([]map[string]any, 0, len(rows.rows))
	policy := archiveReferencePolicy{field: "company_id", table: "companies", warning: "company %d was deleted; associated scope skipped"}
	if err := rows.visit(policy, func(row map[string]any, _ int64) error { kept = append(kept, row); return nil }); err != nil {
		return nil, err
	}
	err := executeArchiveReferenceRows(rows.tx, `INSERT INTO task_scope SELECT * FROM json_populate_recordset(NULL::task_scope,$1::json) ON CONFLICT DO NOTHING`, kept)
	return rows.warnings, err
}
func (rows archiveReferenceRows) profiles() ([]string, error) {
	position := 0
	policy := archiveReferencePolicy{field: "profile_id", table: "llm_profiles", required: true, warning: "LLM profile %d was deleted; chain entry skipped"}
	err := rows.visit(policy, func(row map[string]any, profileID int64) error {
		taskID, _ := jsonInt64(row["task_id"])
		_, err := rows.tx.Exec(`INSERT INTO task_llm_profiles(task_id,profile_id,position,status,last_error,exhausted_at,created_at,updated_at)
	VALUES($1,$2,$3,$4,NULLIF($5,''),$6::timestamptz,COALESCE($7::timestamptz,now()),COALESCE($8::timestamptz,now()))
	ON CONFLICT DO NOTHING`, taskID, profileID, position, archiveRowString(row, "status"), archiveRowString(row, "last_error"), nilIfEmptyString(archiveRowString(row, "exhausted_at")), nilIfEmptyString(archiveRowString(row, "created_at")), nilIfEmptyString(archiveRowString(row, "updated_at")))
		if err != nil {
			return err
		}
		position++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows.warnings, nil
}
func patchArchiveApproval(row map[string]any) {
	if source := archiveRowString(row, "decision_source"); source == "" {
		source = "unknown"
		if row["rule_id"] != nil {
			source = "rule"
		} else if strings.HasPrefix(archiveRowString(row, "reason"), "[model]") {
			source = "model"
		}
		row["decision_source"] = source
	}
	if archiveRowString(row, "status") == "pending" {
		row["status"] = "timeout"
		row["reason"] = "approval timed out during task archiving"
		row["decided_at"] = time.Now().UTC()
		if audit, ok := row["audit"].(map[string]any); ok {
			audit["effective_action"] = "deny"
			audit["decision_reason"] = "approval timed out during task archiving"
			audit["execution_status"] = "not_executed"
		}
	} else if audit, ok := row["audit"].(map[string]any); ok && audit["execution_status"] == "awaiting_result" {
		audit["execution_status"] = "unknown"
	}
}
func (rows archiveReferenceRows) approvals() error {
	for _, row := range rows.rows {
		patchArchiveApproval(row)
	}
	return executeArchiveReferenceRows(rows.tx, `INSERT INTO intercept_pending SELECT * FROM json_populate_recordset(NULL::intercept_pending,$1::json)`, rows.rows)
}
