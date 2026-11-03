package db

import (
	"database/sql"
	"encoding/json"
)

// Row iteration preserves scan failure discard and late error partial results.
type interceptRead[T any] struct {
	items    []T
	finished bool
	err      error
}

func (result interceptRead[T]) list() ([]T, error) { return result.items, result.err }
func collectInterceptRows[T any](rows *sql.Rows, initial []T, scan func(interface{ Scan(...any) error }) (T, error)) interceptRead[T] {
	result := interceptRead[T]{items: initial}
	finished, err := visitRows(rows, func() error {
		item, err := scan(rows)
		if err != nil {
			return err
		}
		result.items = append(result.items, item)
		return nil
	})
	if !finished {
		return interceptRead[T]{err: err}
	}
	result.finished, result.err = finished, err
	return result
}
func queryInterceptRows[T any](d *DB, query string, args []any, initial []T, scan func(interface{ Scan(...any) error }) (T, error)) interceptRead[T] {
	rows, err := d.Query(query, args...)
	if err != nil {
		return interceptRead[T]{err: err}
	}
	defer rows.Close()
	return collectInterceptRows(rows, initial, scan)
}
func scanInterceptPendingValue(row interface{ Scan(...any) error }) (InterceptPending, error) {
	var value InterceptPending
	err := scanInterceptPending(row, &value)
	return value, err
}
func scanInterceptApprovalValue(row interface{ Scan(...any) error }) (InterceptApprovalRow, error) {
	var value InterceptApprovalRow
	err := scanInterceptApprovalRow(row, &value)
	return value, err
}
func interceptApprovalDestinations(r *InterceptApprovalRow) []any {
	return []any{
		&r.ID, &r.RuleID, &r.ConversationID, &r.TaskID, &r.AgentName,
		&r.ToolName, &r.ToolInput, &r.Status, &r.Reason, &r.DecidedAt, &r.CreatedAt,
		&r.DecisionSource, &r.ConvTitle, &r.ConvAgentKey, &r.RuleName,
	}
}
func scanInterceptExecutionActivity(row interface{ Scan(...any) error }) (Activity, error) {
	var a Activity
	err := row.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.CreatedAt, &a.MainSeg)
	return a, err
}

// One record binding preserves zero-to-NULL and the caller's borrowed input.
type interceptRecord struct {
	prefix         []any
	reason, source string
	audit          any
}

func newInterceptRecord(ruleID, convID int64, taskID, agentName, toolName string, input []byte, reason string, audits []*InterceptAudit) interceptRecord {
	raw := json.RawMessage(input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var conversation *int64
	if convID != 0 {
		conversation = &convID
	}
	var task *string
	if taskID != "" {
		task = &taskID
	}
	var rule *int64
	if ruleID != 0 {
		rule = &ruleID
	}
	return interceptRecord{prefix: []any{rule, conversation, task, agentName, toolName, raw}, reason: reason, source: interceptSource(ruleID, reason), audit: firstAudit(audits)}
}
func (record interceptRecord) insert(d *DB, decided bool, status string) (int64, error) {
	query := `
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, reason, decision_source, audit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`
	args := record.prefix
	if decided {
		query = `
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, reason, decided_at, decision_source, audit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), $9, $10) RETURNING id`
		args = append(args, status)
	}
	args = append(args, record.reason, record.source, record.audit)
	var id int64
	err := d.QueryRow(query, args...).Scan(&id)
	return id, err
}
