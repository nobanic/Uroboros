package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"time"
)

// DBFinding is a row in the standalone findings table. It persists across task
// deletion unless the caller explicitly requests related finding cleanup.
type DBFinding struct {
	TrafficCount          int
	EvidenceVersion       int64
	ReportEvidenceVersion int64
	TrafficBindings       []FindingTrafficBinding // populated only for export

	ID              int64
	TaskID          *int64
	NodeID          *int64
	VulnClass       string
	Name            string // vulnerability name (human-readable title); when empty the frontend falls back to VulnClass
	Severity        string
	Summary         string
	Evidence        string
	Worker          string
	AssetIDs        []int64
	Status          string
	Report          string // detailed report (Markdown); populated only by GetFinding, not by list queries
	CreatedAt       time.Time
	TaskDescription string // populated via LEFT JOIN on tasks
}

// Finding triage states (findings.status).
const (
	FindingPending       = "pending"        // pending
	FindingInProgress    = "in_progress"    // in progress
	FindingConfirmed     = "confirmed"      // confirmed (real vulnerability, not yet fixed)
	FindingResolved      = "resolved"       // resolved
	FindingFixed         = "fixed"          // fixed
	FindingFalsePositive = "false_positive" // false positive
	FindingIgnored       = "ignored"        // ignored
	FindingDuplicate     = "duplicate"      // duplicate
	FindingRiskAccepted  = "risk_accepted"  // risk accepted
)

// ValidFindingStatus reports whether s is a known triage state.
func ValidFindingStatus(s string) bool {
	switch s {
	case FindingPending, FindingInProgress, FindingConfirmed, FindingResolved, FindingFixed,
		FindingFalsePositive, FindingIgnored, FindingDuplicate, FindingRiskAccepted:
		return true
	}
	return false
}

// Finding severity levels (findings.severity).
const (
	SeverityCritical = "critical" // critical
	SeverityHigh     = "high"     // high
	SeverityMedium   = "medium"   // medium
	SeverityLow      = "low"      // low
)

// ValidSeverity reports whether s is a known severity level.
func ValidSeverity(s string) bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	}
	return false
}

// FindingFilter narrows a paginated findings query. Empty-string fields mean "no
// filter on that column". Sort is "severity" (severity desc, then newest) or
// anything else (newest first).
type FindingFilter struct {
	Severity  string // high | medium | low
	Status    string // pending | false_positive | ignored | resolved
	VulnClass string
	TaskID    string // task id (string form; empty/invalid = don't filter by task)
	Query     string // fuzzy-search keyword over name/class/summary/evidence/report body
	Sort      string // "severity" | "time"
	// AssetScope is a node key in the asset tree (a:<id> / c:<id> / r:<domain> / __none__);
	// selecting a node is equivalent to selecting its whole subtree. Empty = don't filter by asset.
	AssetScope string

	// The three below are parsed from AssetScope by applyAssetScope; callers don't set them.
	assetIDs  []int64 // all asset ids in the subtree
	assetNone bool    // only findings with "no associated asset"
	assetMiss bool    // the selected node doesn't exist under the current filter → result always empty
}

// FindingUnassignedTask is the task filter sentinel for findings whose task is
// absent. That includes rows created without a task and rows retained after their
// originating task was deleted (the findings FK is ON DELETE SET NULL).
const FindingUnassignedTask = "__unassigned__"

// FindingGroup is one task-level bucket in the global findings view. TaskID is
// nil for both findings that never had a task and findings retained after task
// deletion; those records intentionally share one "unassigned/deleted" bucket.
type FindingGroup struct {
	TaskID          *int64    `json:"task_id"`
	TaskName        string    `json:"task_name"` // optional task name; empty = unnamed
	TaskDescription string    `json:"task_description"`
	TaskStatus      string    `json:"task_status"`
	Count           int       `json:"count"`
	Critical        int       `json:"critical"`
	High            int       `json:"high"`
	Medium          int       `json:"medium"`
	Low             int       `json:"low"`
	LastFoundAt     time.Time `json:"last_found_at"`
}

// ErrFindingOriginUnavailable means a retained finding no longer has a live
// owning task and finding node from which a follow-up intent can be derived.
var ErrFindingOriginUnavailable = errors.New("finding origin is no longer available")

// AddFindingFollowUpIntent atomically creates a priority-10 human intent from a
// live finding node, copies that finding's asset anchors, records the
// finding --derived_from--> intent lineage edge, and persists its audit activity.
// The returned activity is the committed row and can be broadcast as-is without
// calling AppendActivity again.
func (s *ExplorationStore) AddFindingFollowUpIntent(findingID, findingNodeID int64, description string, audit Activity) (int64, Activity, error) {
	return (findingFollowup{store: s, findingID: findingID, findingNodeID: findingNodeID, description: description, audit: audit}).run()
}

// FindingStats is the whole-table aggregate powering the findings page's stat cards
// and vuln-class filter — computed server-side so it stays exact regardless of
// pagination.
type FindingStats struct {
	Total       int                 `json:"total"`
	Pending     int                 `json:"pending"`
	Critical    int                 `json:"critical"`
	High        int                 `json:"high"`
	Medium      int                 `json:"medium"`
	Low         int                 `json:"low"`
	VulnClasses []string            `json:"vulnclasses"`
	Tasks       []FindingTaskOption `json:"tasks"` // tasks that have findings (for the "by task" dropdown)
}

// FindingTaskOption is one entry in the findings page's task filter: a task that has at
// least one finding, with its description and finding count. Description is empty when
// the task has since been deleted (finding rows persist), so the frontend falls back to
// the id.
type FindingTaskOption struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"` // optional task name; empty = unnamed
	Description string `json:"description"`
	Count       int    `json:"count"`
}

// DeleteFinding removes a finding entirely: the standalone findings row and its
// originating exploration node (kind='finding'), so it disappears from the findings
// list, the per-task findings Tab, and the exploration graph alike. Deleting the node
// cascades its edges + node_assets and nulls any activity referencing it. Returns
// rows affected (0 = no finding with that id).
func (d *DB) DeleteFinding(id int64) (n int64, err error) {
	err = d.WithEvidenceTx(context.Background(), func(tx *sql.Tx) error {
		if err := LockFindingEvidenceTx(tx, id, nil); err != nil {
			if errors.Is(err, ErrFindingNotFound) {
				return nil
			}
			return err
		}
		var nodeID sql.NullInt64
		if err := tx.QueryRow(`DELETE FROM findings WHERE id=$1 RETURNING node_id`, id).Scan(&nodeID); err != nil {
			return err
		}
		if nodeID.Valid {
			if _, err := tx.Exec(`DELETE FROM exploration_nodes WHERE id=$1 AND kind='finding'`, nodeID.Int64); err != nil {
				return err
			}
		}
		n = 1
		return nil
	})
	return
}

// DeleteFindingsByTask removes all findings rows of a task. The originating
// exploration finding nodes are cascade-deleted separately when the task's
// exploration subgraph is dropped. Returns rows deleted.
func (d *DB) DeleteFindingsByTask(taskID int64) (int64, error) {
	res, err := d.Exec(`DELETE FROM findings WHERE task_id=$1`, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetFindingStatus updates one finding's triage state. Returns rows affected.
//
// Low-level setter: changes only the status, records no push event. Production code that changes
// status should go through SetFindingStatusWithNotify — calling this directly silently disables the
// "status-change push". It's kept so cases that don't care about notifications (parameter validation, retest flow) can drive the status on their own.
func (d *DB) SetFindingStatus(id int64, status string) (int64, error) {
	res, err := d.Exec(`UPDATE findings SET status=$1 WHERE id=$2`, status, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetFindingReportByNodeID sets the Markdown report on the standalone finding row
// whose node_id matches — report_finding returns that node id, so an agent tool
// can address the finding it just created. Returns rows affected (0 when no row).
func (d *DB) SetFindingReportByNodeID(nodeID int64, report string) (int64, error) {
	return d.SetFindingReportVersionByNodeID(context.Background(), nodeID, report, nil)
}

// setFindingCol updates one text column on the standalone finding row AND mirrors
// the new value into the originating exploration node's payload under jsonKey, so the
// per-task findings Tab (which reads the node payload, not this table) stays in sync.
// Returns rows affected (0 when no finding has that id); the node sync is best-effort.
// col and jsonKey MUST be trusted constants (they are interpolated into SQL) — never
// pass user input.
func (d *DB) setFindingCol(id int64, col, jsonKey, val string) (int64, error) {
	var nodeID *int64
	err := d.QueryRow(`UPDATE findings SET `+col+`=$1 WHERE id=$2 RETURNING node_id`, val, id).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if nodeID != nil {
		_, _ = d.Exec(`UPDATE exploration_nodes
			SET payload = jsonb_set(payload, '{`+jsonKey+`}', to_jsonb($1::text))
			WHERE id = $2`, val, *nodeID)
	}
	return 1, nil
}

// SetFindingSeverity updates one finding's severity (+ node payload sync). Returns
// rows affected (0 when no finding has that id).
func (d *DB) SetFindingSeverity(id int64, severity string) (int64, error) {
	return d.setFindingCol(id, "severity", "severity", severity)
}

// SetFindingName updates one finding's name (+ node payload sync). Empty name is
// allowed — the frontend falls back to the vuln class for display.
func (d *DB) SetFindingName(id int64, name string) (int64, error) {
	return d.setFindingCol(id, "name", "name", name)
}

// SetFindingVulnClass updates one finding's vuln class (+ node payload sync).
func (d *DB) SetFindingVulnClass(id int64, vulnclass string) (int64, error) {
	return d.setFindingCol(id, "vulnclass", "vulnclass", vulnclass)
}

// FindingMeta is the standalone-row data (id, triage state, anchored assets) the
// per-task view grafts onto its exploration-node findings.
type FindingMeta struct {
	TrafficCount int

	ID       int64
	Status   string
	AssetIDs []int64
}

// FindingMetaByNodeID maps a task's finding node ids to their standalone-row
// metadata (status + anchored asset ids) via the asset store, so callers holding
// only an AssetStore (e.g. the agent ToolSet) can reach it without a raw *DB.
func (a *AssetStore) FindingMetaByNodeID(taskID int64) (map[int64]FindingMeta, error) {
	return a.db.FindingMetaByNodeID(taskID)
}

// FindingMetaByNodeID maps a task's finding node ids to their standalone-row
// metadata, so the per-task view (which reads exploration nodes) can show and
// edit the same status — and the same anchored assets — as the global findings page.
func (d *DB) FindingMetaByNodeID(taskID int64) (map[int64]FindingMeta, error) {
	out := map[int64]FindingMeta{}
	if taskID <= 0 {
		return out, nil
	}
	rows, err := d.Query(`SELECT node_id, id, COALESCE(status,'pending'), asset_ids, (SELECT count(*) FROM finding_traffic_bindings b WHERE b.finding_id=findings.id) FROM findings
		WHERE task_id=$1 AND node_id IS NOT NULL`, taskID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid int64
		var m FindingMeta
		var aidsJSON string
		if err := rows.Scan(&nid, &m.ID, &m.Status, &aidsJSON, &m.TrafficCount); err != nil {
			return out, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &m.AssetIDs)
		out[nid] = m
	}
	return out, rows.Err()
}
