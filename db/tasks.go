package db

import (
	"database/sql"

	"errors"
	"fmt"

	"time"
)

// Task is a row in the task registry (1:1 with an exploration).
type Task struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"` // optional task name; empty = unnamed
	CategoryID    *int64     `json:"category_id,omitempty"`
	CategoryName  string     `json:"category_name,omitempty"`
	Pinned        bool       `json:"pinned"`
	PinnedAt      *time.Time `json:"pinned_at,omitempty"`
	Description   string     `json:"description"`
	Goal          string     `json:"goal"`
	ExplorationID int64      `json:"exploration_id"`
	Status        string     `json:"status"`
	Paused        bool       `json:"paused"`
	Queued        bool       `json:"queued"`
	QueuedAt      *time.Time `json:"queued_at,omitempty"`
	QueueMode     string     `json:"queue_mode,omitempty"`
	LLMProfileID  *int64     `json:"llm_profile_id,omitempty"`
	// Task-level ordered LLM chain. LLMProfileID remains the compatibility alias
	// for ActiveLLMProfileID while older API clients still send one profile id.
	LLMProfileIDs      []int64    `json:"llm_profile_ids,omitempty"`
	ActiveLLMProfileID *int64     `json:"active_llm_profile_id,omitempty"`
	LLMChainRevision   int64      `json:"-"`
	LLMFailoverState   string     `json:"llm_failover_state,omitempty"`
	LLMFailoverReason  string     `json:"llm_failover_reason,omitempty"`
	SourceTaskIDs      []int64    `json:"source_task_ids,omitempty"`
	CompanyIDs         []int64    `json:"company_ids,omitempty"`
	ParentRef          string     `json:"parent_ref,omitempty"` // parent task id (orchestration spawn record; empty = top-level)
	CreatedAt          time.Time  `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"` // moment of entering a terminal state (done/failed/timeout); nil when non-terminal
	// Task-level timeout (see docs/task-timeout-and-wrapup-design.md).
	TimeoutSeconds int        `json:"timeout_seconds"`        // 0 = no time limit
	FirstRunAt     *time.Time `json:"first_run_at,omitempty"` // moment of the first provider call (not created_at); nil = not yet run
	DeadlineAt     *time.Time `json:"deadline_at,omitempty"`  // = first_run_at + timeout_seconds; nil = unlimited or not run
	// planner heartbeat trigger interval (seconds): once this value has elapsed since the last plan ended / the task started with no trigger in between → fire a round.
	// Floor = default = 600 (10min); lower creation values are raised to 600. Legacy nullable rows retain their separate read fallback.
	PlanHeartbeatSeconds int `json:"plan_heartbeat_seconds"`
	// CoverageEnabled is the master switch for the "asset coverage" feature (default true). When false:
	// don't compute/display test coverage, don't auto-accumulate task_scope(source=auto), don't expose
	// add_task_scope/list_untested_assets to the agent, don't inject the coverage block into the situation
	// (the scope field is still kept). Company association (task_scope kind=company) is independent of this switch and never affected. See db/task_scope.go.
	CoverageEnabled bool `json:"coverage_enabled"`
	// StrictIsolation disables implicit reuse of assets discovered by unrelated
	// tasks. Explicit source-task inheritance is incompatible with this mode.
	StrictIsolation bool `json:"strict_isolation"`
	// Startup choices affect infrastructure only; explicit tool calls stay available.
	SkipInitialKatana bool  `json:"skip_initial_katana"`
	InitialScout      *bool `json:"initial_scout"` // nil follows the workspace default
}

// TaskDeleteResult reports optional related-data cleanup performed in the same
// transaction as the task/exploration delete.
type TaskDeleteResult struct {
	AssetsDeleted     int64
	AssetsDetached    int64
	FindingsDeleted   int64
	LLMRecordsDeleted int64
}

// TaskDeletePreparation is produced inside the PostgreSQL deletion transaction
// after asset and anchor writers have been excluded. Prepare callbacks may use
// TrafficHosts to stage an external traffic deletion before PostgreSQL commits.
type TaskDeletePreparation struct {
	ExplorationID int64
	TrafficHosts  []string
}

// IsTerminal reports whether a task status is a terminal (finished) state.
// Single source of truth, replacing the done/failed hardcoded checks scattered everywhere.
func IsTerminal(status string) bool {
	return status == "done" || status == "failed" || status == "timeout"
}

// CreateTask creates an exploration + task in one transaction and returns the task.
// timeoutSeconds is the task-level wall-clock budget (0 = no time limit); deadline_at is
// stamped later at first real run (see engine), not here.
// MinPlanHeartbeatSeconds is the floor of the planner heartbeat interval = default = 10min.
// Anything below it (including the default 0 / negatives / misconfigured small values) is raised to 10min, to keep the planner from being overwhelmed.
const MinPlanHeartbeatSeconds = 600

// MaxTaskSourceCount bounds the amount of live inherited context one task can
// pull into every planner/main-agent prompt. Inheritance is intentionally direct
// only; keeping the fan-in bounded also prevents a single create request from
// multiplying graph and asset-context queries without limit.
const MaxTaskSourceCount = 8

// MaxTaskCompanyCount bounds the number of company asset scopes attached to a
// task. Company scopes are prompt context, and their currently attributed
// assets are snapshotted into the task at creation time.
const MaxTaskCompanyCount = 32

const taskCompanyAssetSource = "company"

var (
	ErrTaskCompanyIDsInvalid = errors.New("invalid task company ids")
	ErrTaskCompanyNotFound   = errors.New("task company not found")
)

// NormalizeTaskCompanyIDs validates IDs and removes duplicates while retaining
// the user's first-seen order.
func NormalizeTaskCompanyIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	seen := make(map[int64]struct{}, min(len(ids), MaxTaskCompanyCount))
	normalized := make([]int64, 0, min(len(ids), MaxTaskCompanyCount))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: company id must be positive", ErrTaskCompanyIDsInvalid)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
		if len(normalized) > MaxTaskCompanyCount {
			return nil, fmt.Errorf("%w: got more than %d unique companies", ErrTaskCompanyIDsInvalid, MaxTaskCompanyCount)
		}
	}
	return normalized, nil
}

func normalizeHeartbeat(sec int) int {
	if sec < MinPlanHeartbeatSeconds {
		return MinPlanHeartbeatSeconds
	}
	return sec
}

func (d *DB) CreateTask(description, goal string, llmProfileID *int64, timeoutSeconds, planHeartbeatSeconds int) (*Task, error) {
	var ids []int64
	if llmProfileID != nil {
		ids = []int64{*llmProfileID}
	}
	return d.CreateTaskWithOptions(description, goal, TaskCreateOptions{
		LLMProfileIDs: ids, TimeoutSeconds: timeoutSeconds, PlanHeartbeatSeconds: planHeartbeatSeconds,
	})
}

// TaskCreateOptions contains the task data that must be committed atomically
// with the task/exploration row.
type TaskCreateOptions struct {
	Name                 string // optional task name; empty = unnamed
	CategoryID           *int64
	SourceTaskIDs        []int64
	CompanyIDs           []int64
	LLMProfileIDs        []int64
	TimeoutSeconds       int
	PlanHeartbeatSeconds int
	// CoverageEnabled is the "asset coverage" feature switch; nil = default on (true), so creation paths
	// that don't care about it (orchestration spawn, old API) keep the original behavior. Only web task creation can explicitly pass false to turn it off.
	CoverageEnabled *bool
	// StrictIsolation is intended for clean evaluations and other tasks that
	// must not consume the global asset graph as hidden prior-task evidence.
	StrictIsolation   bool
	SkipInitialKatana bool
	InitialScout      *bool
	// InterceptRules are task-level asset intercept rules, written into task_intercept_rules within the same transaction as the task at creation.
	InterceptRules []TaskInterceptRuleInput
}

// CreateTaskWithOptions creates an exploration, task, direct source relations,
// and the ordered task LLM chain in one transaction.
func (d *DB) CreateTaskWithOptions(description, goal string, opts TaskCreateOptions) (*Task, error) {
	return (&taskRegistryCreation{database: d, description: description, goal: goal, options: opts}).run()
}

func insertTaskCompanies(tx *sql.Tx, taskID int64, companyIDs []int64) error {
	return (taskRegistryContext{transaction: tx, taskID: taskID}).companies(companyIDs)
}

func insertTaskRelations(tx *sql.Tx, taskID int64, sourceIDs []int64) error {
	return (taskRegistryContext{transaction: tx, taskID: taskID}).sources(sourceIDs)
}

func insertTaskLLMProfiles(tx *sql.Tx, taskID int64, profileIDs []int64) error {
	return (taskRegistryContext{transaction: tx, taskID: taskID}).profiles(profileIDs)
}

// SetParentRef records a task's parent task id (orchestration agent spawn_task association).
func (d *DB) SetParentRef(id int64, parentRef string) error {
	_, err := d.Exec(`UPDATE tasks SET parent_ref=NULLIF($2,'') WHERE id=$1`, id, parentRef)
	return err
}

// ListTasks returns alive tasks with pinned tasks first, then newest ids.
func (d *DB) ListTasks() ([]*Task, error) {
	out, err := d.taskRows()
	if err != nil {
		return nil, err
	}
	if err := d.hydrateTasksContext(out); err != nil {
		return nil, err
	}
	return out, nil
}

// TaskPatch updates task list metadata without changing execution state.
type TaskPatch struct {
	Name   *string
	Pinned *bool
}

// UpdateTask applies a partial task name/pin mutation and returns the updated row.
// Re-pinning an already pinned task preserves its original position.
func (d *DB) UpdateTask(id int64, patch TaskPatch) (*Task, error) {
	return (taskRegistryRecords{database: d, id: id}).update(patch)
}

// GetTask returns one alive task (nil if not found/deleted).
func (d *DB) GetTask(id int64) (*Task, error) {
	return (taskRegistryRecords{database: d, id: id}).get()
}

// SetPaused persists a task's paused flag.
func (d *DB) SetPaused(id int64, paused bool) error {
	_, err := d.Exec(`UPDATE tasks SET paused=$1 WHERE id=$2`, paused, id)
	return err
}

// Enqueue places a task at the tail of the persistent admission queue. Repeating
// the operation while it is already queued keeps its original FIFO position.
func (d *DB) Enqueue(id int64, mode string) error {
	if mode != "bootstrap" && mode != "resume" {
		return fmt.Errorf("invalid queue mode %q", mode)
	}
	_, err := d.Exec(`UPDATE tasks
SET queued=true,
    queued_at=CASE WHEN queued THEN COALESCE(queued_at, now()) ELSE now() END,
    queue_mode=CASE
        WHEN queue_mode='bootstrap' OR $2='bootstrap' THEN 'bootstrap'
        ELSE 'resume'
    END
WHERE id=$1`, id, mode)
	return err
}

// Dequeue removes the concurrency hold. clearMode is false when a user pauses a
// queued task, preserving whether its next admission must bootstrap or resume.
func (d *DB) Dequeue(id int64, clearMode bool) error {
	_, err := d.Exec(`UPDATE tasks
SET queued=false,
    queued_at=NULL,
    queue_mode=CASE WHEN $2 THEN '' ELSE queue_mode END
WHERE id=$1`, id, clearMode)
	return err
}

// SetQueued is the compatibility helper used by older callers and tests. New
// scheduling code should use Enqueue/Dequeue so FIFO metadata is explicit.
func (d *DB) SetQueued(id int64, queued bool) error {
	if queued {
		return d.Enqueue(id, "bootstrap")
	}
	return d.Dequeue(id, true)
}

// SetStatus updates a task's lifecycle status. Entering a terminal state
// (done/failed/timeout) stamps completed_at once (COALESCE keeps the first stamp
// stable); moving back to a non-terminal state clears it, so a re-run has no stale
// finish time.
func (d *DB) SetStatus(id int64, status string) error {
	_, err := d.Exec(`
UPDATE tasks
   SET status = $1,
       completed_at = CASE WHEN $1 IN ('done','failed','timeout') THEN COALESCE(completed_at, now()) ELSE NULL END
 WHERE id = $2`, status, id)
	return err
}

// SetTerminalStatusGuarded sets a terminal status only when the task is neither
// paused nor already terminal, so pause remains a lifecycle barrier and a
// completed↔timeout race resolves to the first writer (won=true). won=false asks
// the caller to inspect the current lifecycle. Non-terminal transitions / re-run
// still use SetStatus.
func (d *DB) SetTerminalStatusGuarded(id int64, status string) (won bool, err error) {
	res, err := d.Exec(`
UPDATE tasks
   SET status = $1,
       completed_at = COALESCE(completed_at, now())
 WHERE id = $2 AND NOT paused AND status NOT IN ('done','failed','timeout')`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// BeginTaskRun is the authoritative first-provider-call transition. It stamps
// first_run_at for both limited and unlimited tasks, derives deadline_at only for
// a positive timeout, and moves a newly-created task to running in the same SQL
// statement. Paused, queued, deleted, archived and terminal tasks are refused so
// a provider request cannot cross a committed lifecycle barrier.
func (d *DB) BeginTaskRun(id int64, timeoutSeconds int) (firstRun time.Time, deadline *time.Time, status string, err error) {
	err = d.QueryRow(`
UPDATE tasks
   SET first_run_at = COALESCE(first_run_at, now()),
       deadline_at  = CASE
           WHEN first_run_at IS NOT NULL THEN deadline_at
           WHEN $2 > 0 THEN now() + make_interval(secs => $2)
           ELSE NULL END,
       status = CASE WHEN status='created' THEN 'running' ELSE status END
 WHERE id=$1
   AND deleted_at IS NULL
   AND archived_at IS NULL
   AND NOT paused
   AND NOT queued
   AND status NOT IN ('done','failed','timeout')
 RETURNING first_run_at, deadline_at, status`, id, timeoutSeconds).
		Scan(&firstRun, &deadline, &status)
	return firstRun, deadline, status, err
}

// TaskExecutionAllowed is the persistent half of the tool-call pause barrier.
// The call context supplies the in-process cancellation half; this query also
// covers restored/stale contexts after a pause has committed in PostgreSQL.
func (d *DB) TaskExecutionAllowed(id int64, allowExpiredDeadline bool) (bool, error) {
	var allowed bool
	err := d.QueryRow(`
SELECT NOT paused
   AND NOT queued
   AND deleted_at IS NULL
   AND archived_at IS NULL
   AND status NOT IN ('done','failed','timeout')
   AND ($2 OR deadline_at IS NULL OR deadline_at > now())
FROM tasks
WHERE id=$1`, id, allowExpiredDeadline).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return allowed, err
}

// DeleteTask preserves the historical behavior: remove the task and its
// exploration subgraph while retaining global assets.
func (d *DB) DeleteTask(id int64) error {
	_, err := d.DeleteTaskCascade(id, false, false)
	return err
}

// DeleteTaskCascade hard-deletes a task and its exploration subgraph
// (nodes/edges/activity via ON DELETE CASCADE). Optional standalone findings and
// assets owned only by this task are deleted in the same transaction; shared
// assets are retained and only have this task id detached. deleteLLMRecords is
// optional to preserve callers of the original two-option API.
func (d *DB) DeleteTaskCascade(id int64, deleteAssets, deleteFindings bool, deleteLLMRecords ...bool) (TaskDeleteResult, error) {
	deleteRecords := len(deleteLLMRecords) > 0 && deleteLLMRecords[0]
	return d.DeleteTaskCascadePrepared(id, deleteAssets, deleteFindings, deleteRecords, nil)
}

// DeleteTaskCascadePrepared coordinates reversible external deletion with the
// PostgreSQL cascade. When prepare is non-nil, host ownership is resolved and
// prepare is invoked inside this transaction while asset and anchor writers are
// excluded. The locks remain held through commit, closing the window where a
// host could become shared after its traffic had already been staged.
//
// prepare must only stage reversible work. Any returned error or later database
// error rolls PostgreSQL back; the caller remains responsible for rolling back
// external work that its callback staged successfully.
func (d *DB) DeleteTaskCascadePrepared(
	id int64,
	deleteAssets, deleteFindings, deleteLLMRecords bool,
	prepare func(TaskDeletePreparation) error,
) (TaskDeleteResult, error) {
	return (taskRegistryDeletion{database: d, id: id, assets: deleteAssets, findings: deleteFindings, records: deleteLLMRecords, prepare: prepare}).run()
}
