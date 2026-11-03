package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InterceptRule is one row of intercept_rules.
type InterceptRule struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Priority       int       `json:"priority"`
	MatchTarget    string    `json:"match_target"`
	MatchType      string    `json:"match_type"`
	Pattern        string    `json:"pattern"`
	Action         string    `json:"action"`
	Message        string    `json:"message"`
	TimeoutEnabled bool      `json:"timeout_enabled"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	TimeoutAction  string    `json:"timeout_action"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// InterceptPending is one row of intercept_pending.
type InterceptPending struct {
	ID             int64           `json:"id"`
	RuleID         *int64          `json:"rule_id"`
	ConversationID *int64          `json:"conversation_id"`
	TaskID         *string         `json:"task_id"`
	AgentName      string          `json:"agent_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Status         string          `json:"status"`
	DecisionSource string          `json:"decision_source"`
	Reason         string          `json:"reason"` // the rule message, or the model's decision reason (prefixed [model])
	DecidedAt      *time.Time      `json:"decided_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

const interceptRuleCols = `id, name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, created_at, updated_at`

func scanInterceptRule(row interface{ Scan(...any) error }) (InterceptRule, error) {
	var r InterceptRule
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.Priority,
		&r.MatchTarget, &r.MatchType, &r.Pattern, &r.Action, &r.Message,
		&r.TimeoutEnabled, &r.TimeoutSeconds, &r.TimeoutAction,
		&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListInterceptRules returns all rules ordered by priority DESC then id.
func (d *DB) ListInterceptRules() ([]InterceptRule, error) {
	return queryInterceptRows(d, `SELECT `+interceptRuleCols+` FROM intercept_rules ORDER BY priority DESC, id`, nil, []InterceptRule(nil), scanInterceptRule).list()
}

// CreateInterceptRule inserts a new rule.
func (d *DB) CreateInterceptRule(name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string) (InterceptRule, error) {
	row := d.QueryRow(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING `+interceptRuleCols,
		name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction)
	return scanInterceptRule(row)
}

// UpdateInterceptRule replaces all editable fields of an existing rule.
func (d *DB) UpdateInterceptRule(id int64, name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string) (InterceptRule, error) {
	row := d.QueryRow(`
UPDATE intercept_rules
   SET name=$2, enabled=$3, priority=$4, match_target=$5,
       match_type=$6, pattern=$7, action=$8, message=$9,
       timeout_enabled=$10, timeout_seconds=$11, timeout_action=$12
WHERE id=$1
RETURNING `+interceptRuleCols,
		id, name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction)
	return scanInterceptRule(row)
}

// DeleteInterceptRule removes a rule.
func (d *DB) DeleteInterceptRule(id int64) error {
	_, err := d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, id)
	return err
}

// ToggleInterceptRule flips the enabled state of a rule.
func (d *DB) ToggleInterceptRule(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE intercept_rules SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}

// CreateInterceptPending inserts a pending approval record and returns its ID.
// convID == 0 → conversation_id stored as NULL (background task).
// taskID == "" → task_id stored as NULL.
func (d *DB) CreateInterceptPending(ruleID, convID int64, taskID, agentName, toolName string, input []byte, reason string, audits ...*InterceptAudit) (int64, error) {
	return newInterceptRecord(ruleID, convID, taskID, agentName, toolName, input, reason, audits).insert(d, false, "")
}

// DecideInterceptPending updates a pending record's status (allowed/denied/timeout).
func (d *DB) DecideInterceptPending(id int64, status string) error {
	_, err := d.Exec(`UPDATE intercept_pending SET status=$2, decided_at=NOW() WHERE id=$1`, id, status)
	return err
}

// CreateDecidedIntercept inserts an intercept_pending row ALREADY in a final state
// (status = 'allowed' | 'denied'), decided_at stamped now. Used to log allow/deny
// rule matches for observability — they don't block and need no user action, so unlike
// CreateInterceptPending (which starts 'pending') this records the outcome directly.
func (d *DB) CreateDecidedIntercept(ruleID, convID int64, taskID, agentName, toolName string, input []byte, status, reason string, audits ...*InterceptAudit) (int64, error) {
	return newInterceptRecord(ruleID, convID, taskID, agentName, toolName, input, reason, audits).insert(d, true, status)
}

const interceptPendingCols = `id, rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, reason, decided_at, created_at, decision_source`

func scanInterceptPending(s interface{ Scan(...any) error }, p *InterceptPending) error {
	return s.Scan(&p.ID, &p.RuleID, &p.ConversationID, &p.TaskID, &p.AgentName,
		&p.ToolName, &p.ToolInput, &p.Status, &p.Reason, &p.DecidedAt, &p.CreatedAt, &p.DecisionSource)
}

// ListPendingIntercepts returns all unresolved approval requests, newest first.
func (d *DB) ListPendingIntercepts() ([]InterceptPending, error) {
	return queryInterceptRows(d, `SELECT `+interceptPendingCols+` FROM intercept_pending WHERE status='pending' ORDER BY created_at DESC`, nil, []InterceptPending(nil), scanInterceptPendingValue).list()
}

// GetInterceptPending returns one pending record (nil if absent).
func (d *DB) GetInterceptPending(id int64) (*InterceptPending, error) {
	var p InterceptPending
	err := scanInterceptPending(
		d.QueryRow(`SELECT `+interceptPendingCols+` FROM intercept_pending WHERE id=$1`, id),
		&p,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// InterceptApprovalRow is intercept_pending enriched with conversation and rule info.
type InterceptApprovalRow struct {
	InterceptPending
	ConvTitle    string `json:"conv_title"`
	ConvAgentKey string `json:"conv_agent_key"`
	RuleName     string `json:"rule_name"`
}

func scanInterceptApprovalRow(rows interface{ Scan(...any) error }, r *InterceptApprovalRow) error {
	return rows.Scan(interceptApprovalDestinations(r)...)
}

// Keep legacy rows without decision_source consistent with their displayed source.
const approvalDecisionSource = `COALESCE(NULLIF(ip.decision_source,''), CASE
 WHEN ip.rule_id IS NOT NULL THEN 'rule'
 WHEN ip.reason LIKE '[model]%' THEN 'model' ELSE 'unknown' END)`

const approvalRowColumns = `ip.id, ip.rule_id, ip.conversation_id, ip.task_id, ip.agent_name,
       ip.tool_name, ip.tool_input, ip.status, ip.reason, ip.decided_at, ip.created_at, ` + approvalDecisionSource + `,
       COALESCE(c.title,'') AS conv_title,
       COALESCE(c.agent_key,'') AS conv_agent_key,
       COALESCE(ir.name,'') AS rule_name`

const approvalRowJoins = `
FROM intercept_pending ip
LEFT JOIN conversations c ON c.id = ip.conversation_id
LEFT JOIN intercept_rules ir ON ir.id = ip.rule_id`

const approvalRowSelect = `SELECT ` + approvalRowColumns + approvalRowJoins
const approvalRowSelectWithAudit = `SELECT ` + approvalRowColumns + `, ip.audit` + approvalRowJoins

func interceptSource(ruleID int64, reason string) string {
	if ruleID != 0 {
		return "rule"
	}
	if strings.HasPrefix(reason, "[model]") {
		return "model"
	}
	return "unknown"
}

func firstAudit(audits []*InterceptAudit) any {
	if len(audits) == 0 || audits[0] == nil {
		return nil
	}
	raw, err := json.Marshal(audits[0])
	if err != nil {
		return nil
	}
	return raw
}

// ListAllIntercepts returns up to limit intercept_pending rows (newest first)
// joined with conversation and rule info.
func (d *DB) ListAllIntercepts(limit int) ([]InterceptApprovalRow, error) {
	return queryInterceptRows(d, approvalRowSelect+` ORDER BY ip.created_at DESC LIMIT $1`, []any{limit}, []InterceptApprovalRow(nil), scanInterceptApprovalValue).list()
}

// InterceptApprovalFilter combines exact status and decision-source filters.
// Empty fields include all values.
type InterceptApprovalFilter struct {
	Status         string
	DecisionSource string
}

// ListAllInterceptsPage returns one 1-based page and the total matching count.
func (d *DB) ListAllInterceptsPage(page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	return d.listInterceptsPage("", page, size, filter)
}

// ListTaskIntercepts returns all intercept_pending rows for a specific task (newest first).
func (d *DB) ListTaskIntercepts(taskID string) ([]InterceptApprovalRow, error) {
	return queryInterceptRows(d, approvalRowSelect+` WHERE ip.task_id=$1 ORDER BY ip.created_at DESC`, []any{taskID}, []InterceptApprovalRow(nil), scanInterceptApprovalValue).list()
}

// ListTaskInterceptsPage is the paginated variant of ListTaskIntercepts.
func (d *DB) ListTaskInterceptsPage(taskID string, page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	return d.listInterceptsPage(taskID, page, size, filter)
}

func (d *DB) listInterceptsPage(taskID string, page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	offset := (page - 1) * size

	conditions := []string{}
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			args = append(args, value)
			conditions = append(conditions, column+"=$"+fmt.Sprint(len(args)))
		}
	}
	add("ip.task_id", taskID)
	add("ip.status", filter.Status)
	add(approvalDecisionSource, filter.DecisionSource)
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := d.QueryRow("SELECT COUNT(*) FROM intercept_pending ip"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := len(args) + 1
	offsetArg := limitArg + 1
	dataQ := approvalRowSelect + where +
		" ORDER BY ip.created_at DESC, ip.id DESC LIMIT $" + fmt.Sprint(limitArg) +
		" OFFSET $" + fmt.Sprint(offsetArg)
	args = append(args, size, offset)
	result := queryInterceptRows(d, dataQ, args, []InterceptApprovalRow{}, scanInterceptApprovalValue)
	if !result.finished {
		return nil, 0, result.err
	}
	return result.items, total, result.err
}
