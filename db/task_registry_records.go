package db

import "database/sql"

// Metadata updates remain raw rows; a get separately hydrates ordered context.
type taskRegistryRecords struct {
	database *DB
	id       int64
}

func (record taskRegistryRecords) get() (*Task, error) {
	t, err := scanTask(record.database.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=$1 AND deleted_at IS NULL`, record.id))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if err := record.database.hydrateTaskContext(t); err != nil {
		return nil, err
	}
	return t, nil
}

func (record taskRegistryRecords) update(patch TaskPatch) (*Task, error) {
	task, err := scanTask(record.database.QueryRow(`UPDATE tasks SET
	name = CASE WHEN $2::boolean THEN $3 ELSE name END,
	pinned_at = CASE
		WHEN $4::boolean IS NULL THEN pinned_at
		WHEN $4::boolean THEN COALESCE(pinned_at, now())
		ELSE NULL
	END
WHERE id=$1 AND deleted_at IS NULL
RETURNING `+taskCols, record.id, patch.Name != nil, patch.Name, patch.Pinned))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}
