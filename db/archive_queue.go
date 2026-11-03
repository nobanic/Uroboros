package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// archiveQueue owns persistent admission and job transitions. Each admission
// borrows one transaction; claim keeps the original context and SKIP LOCKED
// policy. No worker, polling loop, cache or new admission authority lives here.
type archiveQueue struct{ database *DB }

type archiveTaskAdmission struct {
	name, description, goal, status, categoryName string
	categoryID                                    *int64
	paused, queued                                bool
	deadline                                      *time.Time
}

func (q archiveQueue) admit(taskID int64) (*TaskArchive, error) {
	tx, err := q.database.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	input := archiveTaskAdmission{}
	err = tx.QueryRow(`SELECT COALESCE(t.name,''), t.description, t.goal, t.status,
 t.category_id, COALESCE(c.name,''), t.paused, t.queued, t.deadline_at
FROM tasks t LEFT JOIN task_categories c ON c.id=t.category_id
WHERE t.id=$1 AND t.deleted_at IS NULL FOR UPDATE OF t`, taskID).Scan(&input.name, &input.description, &input.goal, &input.status, &input.categoryID, &input.categoryName, &input.paused, &input.queued, &input.deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskArchiveNotFound
	}
	if err != nil {
		return nil, err
	}
	if input.queued {
		return nil, ErrTaskArchiveQueued
	}
	if !input.paused && !IsTerminal(input.status) {
		return nil, ErrTaskArchiveIneligible
	}
	if err := archiveAdmissionDependent(tx, `SELECT child.id FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
LEFT JOIN task_archives pending ON pending.task_id=child.id
WHERE relation.source_task_id=$1
  AND (pending.id IS NULL OR pending.state NOT IN ('archive_queued','archiving'))
LIMIT 1`, ErrTaskArchiveDependent, taskID); err != nil {
		return nil, err
	}
	sources, err := archiveAdmissionSources(tx, taskID)
	if err != nil {
		return nil, err
	}
	remaining := int64(0)
	if input.paused && input.deadline != nil {
		remaining = int64(time.Until(*input.deadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
	}
	_, err = tx.Exec(`INSERT INTO task_archives(
 task_id,state,phase,progress,error,warnings,format_version,task_name,
 task_description,task_goal,original_status,category_id_snapshot,
 category_name_snapshot,source_task_ids,remaining_timeout_seconds,requested_at)
VALUES ($1,$2,'queued',0,'','[]',$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
	ON CONFLICT (task_id) DO UPDATE SET
	 state=CASE WHEN task_archives.state IN ('archive_failed') THEN EXCLUDED.state ELSE task_archives.state END,
	 phase=CASE WHEN task_archives.state IN ('archive_failed') THEN 'queued' ELSE task_archives.phase END,
	 progress=CASE WHEN task_archives.state IN ('archive_failed') THEN 0 ELSE task_archives.progress END,
	 error=CASE WHEN task_archives.state IN ('archive_failed') THEN '' ELSE task_archives.error END,
	 format_version=CASE WHEN task_archives.state IN ('archive_failed') THEN EXCLUDED.format_version ELSE task_archives.format_version END,
	 requested_at=CASE WHEN task_archives.state IN ('archive_failed') THEN now() ELSE task_archives.requested_at END`, taskID, ArchiveQueued, TaskArchiveFormatVersion, input.name, input.description, input.goal, input.status, input.categoryID, input.categoryName, sources, remaining)
	if err != nil {
		return nil, err
	}
	item, err := scanTaskArchive(tx.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE task_id=$1`, taskID))
	if err != nil {
		return nil, err
	}
	if item.State != ArchiveQueued && item.State != ArchiveFailed {
		return nil, fmt.Errorf("%w: current state %s", ErrTaskArchiveState, item.State)
	}
	return item, tx.Commit()
}

func archiveAdmissionDependent(tx *sql.Tx, query string, sentinel error, args ...any) error {
	var dependent int64
	err := tx.QueryRow(query, args...).Scan(&dependent)
	if err == nil {
		return fmt.Errorf("%w: task %d", sentinel, dependent)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func archiveAdmissionSources(tx *sql.Tx, taskID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT source_task_id FROM task_relations WHERE task_id=$1 ORDER BY created_at, source_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	var sources []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, id)
	}
	// Retain the historical Close-only late-error handling, without introducing
	// a different error/rollback policy into this ownership change.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []int64{}
	}
	return sources, nil
}

func (q archiveQueue) restore(id int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(q.database.QueryRow(`UPDATE task_archives
SET state=$2, phase='queued', progress=0, error='', requested_at=now()
WHERE id=$1 AND state IN ('ready','restore_failed') RETURNING `+taskArchiveCols, id, RestoreQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskArchiveState
	}
	return item, err
}

func (q archiveQueue) remove(id int64) (*TaskArchive, error) {
	tx, err := q.database.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var taskID int64
	if err := tx.QueryRow(`SELECT task_id FROM task_archives WHERE id=$1 AND state IN ('ready','delete_failed') FOR UPDATE`, id).Scan(&taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskArchiveState
		}
		return nil, err
	}
	if err := archiveAdmissionDependent(tx, `SELECT task_id FROM task_archives
WHERE id<>$1 AND $2=ANY(source_task_ids) AND state NOT IN ('delete_queued','deleting') LIMIT 1`, ErrTaskArchiveDeleteBlocked, id, taskID); err != nil {
		return nil, err
	}
	item, err := scanTaskArchive(tx.QueryRow(`UPDATE task_archives
SET state=$2, phase='queued', progress=0, error='', requested_at=now()
WHERE id=$1 RETURNING `+taskArchiveCols, id, DeleteQueued))
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

func (q archiveQueue) recover() error {
	_, err := q.database.Exec(`UPDATE task_archives SET
	 state=CASE state WHEN 'archiving' THEN 'archive_failed'
	                  WHEN 'restoring' THEN 'restore_queued'
	                  WHEN 'deleting' THEN 'delete_queued' ELSE state END,
	 phase='interrupted',
	 error=CASE WHEN state='archiving' THEN 'the previous archiving process exited abnormally; please retry manually' ELSE '' END
	WHERE state IN ('archiving','restoring','deleting')`)
	return err
}

func (q archiveQueue) claim(ctx context.Context) (*TaskArchive, error) {
	tx, err := q.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var id int64
	var queued string
	err = tx.QueryRowContext(ctx, `SELECT id,state FROM task_archives
WHERE state IN ('archive_queued','restore_queued','delete_queued')
ORDER BY requested_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &queued)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	active := map[string]string{ArchiveQueued: Archiving, RestoreQueued: Restoring, DeleteQueued: Deleting}[queued]
	item, err := scanTaskArchive(tx.QueryRowContext(ctx, `UPDATE task_archives
SET state=$2,phase='starting',progress=1,error='' WHERE id=$1 RETURNING `+taskArchiveCols, id, active))
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

func (q archiveQueue) fail(id int64, active string, cause error) error {
	failed := map[string]string{Archiving: ArchiveFailed, Restoring: RestoreFailed, Deleting: DeleteFailed}[active]
	if failed == "" {
		return fmt.Errorf("unknown active archive state %q", active)
	}
	message := "unknown archive failure"
	if cause != nil {
		message = cause.Error()
	}
	_, err := q.database.Exec(`UPDATE task_archives SET state=$2,phase='failed',error=$3 WHERE id=$1`, id, failed, message)
	return err
}
