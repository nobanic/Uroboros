package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// archiveMutation is one borrowed database transaction, with the archive/task
// identity locked together after schema coordination. Callers own rollback and
// commit; no filesystem or worker operation happens inside this owner.
type archiveMutation struct {
	tx          *sql.Tx
	archiveID   int64
	taskID      int64
	exploration int64
	state       string
}

func (d *DB) beginArchiveMutation(archiveID int64) (*archiveMutation, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	mutation := &archiveMutation{tx: tx, archiveID: archiveID}
	if err := coordinateWithSchemaMigration(tx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	err = tx.QueryRow(`SELECT archive.task_id,task.exploration_id,archive.state
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id
WHERE archive.id=$1 FOR UPDATE OF archive,task`, archiveID).Scan(&mutation.taskID, &mutation.exploration, &mutation.state)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return mutation, nil
}

func (m *archiveMutation) requireSnapshot(snapshot *TaskArchiveSnapshot, state, operation string) error {
	if m.state != state || m.taskID != snapshot.TaskID || m.exploration != snapshot.ExplorationID {
		return fmt.Errorf("%w: %s snapshot identity/state mismatch", ErrTaskArchiveState, operation)
	}
	return nil
}

func (m *archiveMutation) exec(query string, args ...any) error {
	_, err := m.tx.Exec(query, args...)
	return err
}

type archiveStatement struct {
	query string
	args  []any
}

func (m *archiveMutation) execute(statements ...archiveStatement) error {
	for _, statement := range statements {
		if err := m.exec(statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func (m *archiveMutation) rejectDependent(query string, sentinel error, args ...any) error {
	var dependent int64
	err := m.tx.QueryRow(query, args...).Scan(&dependent)
	if err == nil {
		return fmt.Errorf("%w: task %d", sentinel, dependent)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
