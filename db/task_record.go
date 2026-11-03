package db

import (
	"database/sql"
	"strings"
)

// The task projection binds storage names, fallbacks and decoding together.
// Context is loaded separately: metadata PATCH and creation intentionally return
// raw rows, while GetTask and ListTasks attach their existing ordered context.
func taskFields(task *Task) []recordField {
	pin := field("pinned_at", &task.PinnedAt)
	pin.afterRead = func() { task.Pinned = task.PinnedAt != nil }
	return []recordField{
		field("id", &task.ID), field("name", &task.Name).defaultRead("''"),
		field("category_id", &task.CategoryID),
		field("(SELECT category.name FROM task_categories category WHERE category.id=tasks.category_id)", &task.CategoryName).defaultRead("''"),
		field("description", &task.Description), field("goal", &task.Goal),
		field("exploration_id", &task.ExplorationID), field("status", &task.Status),
		field("paused", &task.Paused), field("queued", &task.Queued),
		field("queued_at", &task.QueuedAt), field("queue_mode", &task.QueueMode).defaultRead("''"),
		field("llm_profile_id", &task.LLMProfileID), field("active_llm_profile_id", &task.ActiveLLMProfileID),
		field("parent_ref", &task.ParentRef).defaultRead("''"), pin,
		field("created_at", &task.CreatedAt), field("completed_at", &task.CompletedAt),
		field("timeout_seconds", &task.TimeoutSeconds).defaultRead("0"),
		// Existing rows retain the legacy300 fallback; creation normalizes to600.
		field("plan_heartbeat_seconds", &task.PlanHeartbeatSeconds).defaultRead("300"),
		field("coverage_enabled", &task.CoverageEnabled).defaultRead("true"),
		field("strict_isolation", &task.StrictIsolation).defaultRead("false"),
		field("skip_initial_katana", &task.SkipInitialKatana).defaultRead("false"),
		field("initial_scout", &task.InitialScout),
		field("first_run_at", &task.FirstRunAt), field("deadline_at", &task.DeadlineAt),
	}
}

var taskCols = recordProjection(taskFields(new(Task)))

func scanTask(row rowDecoder) (*Task, error) {
	task := new(Task)
	if err := decodeRecord(row, taskFields(task)); err != nil {
		return nil, err
	}
	return task, nil
}

func insertTaskRow(tx *sql.Tx, task *Task) error {
	// Only these authored columns are written. Read fallbacks never alter input
	// values, and SQL defaults still own all other lifecycle/timing fields.
	writable := map[string]bool{
		"name": true, "category_id": true, "description": true, "goal": true,
		"exploration_id": true, "llm_profile_id": true, "active_llm_profile_id": true,
		"timeout_seconds": true, "plan_heartbeat_seconds": true,
		"coverage_enabled": true, "strict_isolation": true,
		"skip_initial_katana": true, "initial_scout": true,
	}
	var fields []recordField
	for _, column := range taskFields(task) {
		if writable[column.name] {
			fields = append(fields, column)
		}
	}
	changes := recordChanges(fields)
	statement := "INSERT INTO tasks (" + strings.Join(changes.names, ", ") + ") VALUES (" + strings.Join(changes.expressions, ", ") + ") RETURNING id, status, paused, created_at"
	return tx.QueryRow(statement, changes.values...).Scan(&task.ID, &task.Status, &task.Paused, &task.CreatedAt)
}

func (database *DB) taskRows() ([]*Task, error) {
	selection := recordSelect[Task]{
		from: "FROM tasks", fields: taskFields,
		orderBy: "(pinned_at IS NOT NULL) DESC, pinned_at DESC NULLS LAST, id DESC",
	}
	var tasks []*Task
	batch := selection.visit(database, recordCriteria{where: "WHERE deleted_at IS NULL"}, nil, func(task Task) { tasks = append(tasks, &task) })
	if batch.failure != nil {
		return nil, batch.failure
	}
	if batch.iterationError != nil {
		return nil, batch.iterationError
	}
	return tasks, nil
}

// A nullable LEFT JOIN row has one decoder for both single and bulk hydration.
// Missing profile entries are not an empty/default profile, and an absent active
// cursor is not repaired from an earlier ready entry.
type taskContextRow struct {
	taskID                            int64
	legacy, active, profile, position sql.NullInt64
	revision                          int64
	status, lastError                 sql.NullString
	exhaustedAt                       sql.NullTime
}

func taskContextFields(row *taskContextRow, withTaskID bool) []recordField {
	fields := []recordField{
		field("t.llm_profile_id", &row.legacy), field("t.active_llm_profile_id", &row.active),
		field("t.llm_chain_revision", &row.revision), field("p.profile_id", &row.profile),
		field("p.position", &row.position), field("p.status", &row.status),
		field("p.last_error", &row.lastError).defaultRead("''"), field("p.exhausted_at", &row.exhaustedAt),
	}
	if withTaskID {
		fields = append([]recordField{field("t.id", &row.taskID)}, fields...)
	}
	return fields
}

type taskBatchContext struct {
	legacyProfileID *int64
	activeProfileID *int64
	revision        int64
	chain           []TaskLLMProfile
	sourceIDs       []int64
	companyIDs      []int64
}

func (context *taskBatchContext) header(row taskContextRow) {
	context.revision = row.revision
	if row.legacy.Valid {
		id := row.legacy.Int64
		context.legacyProfileID = &id
	}
	if row.active.Valid {
		id := row.active.Int64
		context.activeProfileID = &id
	}
}
func (context *taskBatchContext) appendProfile(row taskContextRow) {
	if !row.profile.Valid {
		return
	}
	entry := TaskLLMProfile{ProfileID: row.profile.Int64, Position: int(row.position.Int64), Status: row.status.String, LastError: row.lastError.String}
	if row.exhaustedAt.Valid {
		stamp := row.exhaustedAt.Time
		entry.ExhaustedAt = &stamp
	}
	context.chain = append(context.chain, entry)
}

type taskContextRead struct {
	database *DB
	batch    bool
	ids      any
}

func (read taskContextRead) criterion(column string) recordCriteria {
	comparison := "=$1"
	if read.batch {
		comparison = "=ANY($1::bigint[])"
	}
	return recordCriteria{where: "WHERE " + column + comparison, values: []any{read.ids}}
}
func (read taskContextRead) profiles(accept func(taskContextRow)) error {
	order := "p.position NULLS LAST"
	if read.batch {
		order = "t.id, " + order
	}
	selection := recordSelect[taskContextRow]{
		from: "FROM tasks t LEFT JOIN task_llm_profiles p ON p.task_id=t.id", orderBy: order,
		fields: func(row *taskContextRow) []recordField { return taskContextFields(row, read.batch) },
	}
	batch := selection.visit(read.database, read.criterion("t.id"), nil, accept)
	if batch.failure != nil {
		return batch.failure
	}
	return batch.iterationError
}

type taskLinkKind uint8

const (
	taskSourceLink taskLinkKind = iota
	taskCompanyLink
)

type taskLinkRow struct{ taskID, id int64 }

func (read taskContextRead) links(kind taskLinkKind, accept func(taskLinkRow)) recordBatch[taskLinkRow] {
	table, column, order := "task_relations", "source_task_id", "created_at, source_task_id"
	criteria := read.criterion("task_id")
	if kind == taskCompanyLink {
		table, column, order = "task_scope", "company_id", "id"
		criteria.where += " AND kind='company' AND company_id IS NOT NULL"
	}
	if read.batch {
		order = "task_id, " + order
	}
	selection := recordSelect[taskLinkRow]{from: "FROM " + table, orderBy: order,
		fields: func(row *taskLinkRow) []recordField {
			fields := []recordField{field(column, &row.id)}
			if read.batch {
				fields = append([]recordField{field("task_id", &row.taskID)}, fields...)
			}
			return fields
		},
	}
	return selection.visit(read.database, criteria, nil, accept)
}

func (database *DB) taskLinkIDs(taskID int64, kind taskLinkKind) ([]int64, error) {
	var ids []int64
	batch := (taskContextRead{database: database, ids: taskID}).links(kind, func(row taskLinkRow) { ids = append(ids, row.id) })
	if batch.failure != nil {
		return nil, batch.failure
	}
	// Preserve partial items on an iterator error; query/scan errors return nil.
	return ids, batch.iterationError
}

func (database *DB) readTaskContext(taskID int64) (*taskBatchContext, error) {
	context := new(taskBatchContext)
	found := false
	err := (taskContextRead{database: database, ids: taskID}).profiles(func(row taskContextRow) {
		if !found {
			context.header(row)
			found = true
		}
		context.appendProfile(row)
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, sql.ErrNoRows
	}
	return context, nil
}

func (database *DB) attachTaskContexts(tasks []*Task) error {
	ids := make([]int64, 0, len(tasks))
	contexts := make(map[int64]*taskBatchContext, len(tasks))
	targets := make(map[int64]*Task, len(tasks))
	for _, task := range tasks {
		if task != nil {
			ids = append(ids, task.ID)
			contexts[task.ID], targets[task.ID] = new(taskBatchContext), task
		}
	}
	if len(ids) == 0 {
		return nil
	}
	read := taskContextRead{database: database, batch: true, ids: ids}
	err := read.profiles(func(row taskContextRow) {
		if context := contexts[row.taskID]; context != nil {
			context.header(row)
			context.appendProfile(row)
		}
	})
	if err != nil {
		return err
	}
	for _, kind := range []taskLinkKind{taskSourceLink, taskCompanyLink} {
		batch := read.links(kind, func(link taskLinkRow) {
			if context := contexts[link.taskID]; context != nil {
				if kind == taskSourceLink {
					context.sourceIDs = append(context.sourceIDs, link.id)
				} else {
					context.companyIDs = append(context.companyIDs, link.id)
				}
			}
		})
		if batch.failure != nil {
			return batch.failure
		}
		if batch.iterationError != nil {
			return batch.iterationError
		}
	}
	// No public task is modified until all three bulk queries have succeeded.
	for id, context := range contexts {
		task := targets[id]
		task.SourceTaskIDs, task.CompanyIDs = context.sourceIDs, context.companyIDs
		applyTaskLLMContext(task, context.legacyProfileID, context.activeProfileID, context.revision, context.chain)
	}
	return nil
}

func taskSourceFields(source *TaskSource) []recordField {
	return []recordField{field("t.id", &source.TaskID), field("t.exploration_id", &source.ExplorationID), field("t.description", &source.Description), field("t.goal", &source.Goal), field("t.status", &source.Status)}
}
func taskProfileFields(profile *TaskLLMProfile) []recordField {
	return []recordField{field("profile_id", &profile.ProfileID), field("position", &profile.Position), field("status", &profile.Status), field("last_error", &profile.LastError).defaultRead("''"), field("exhausted_at", &profile.ExhaustedAt)}
}
