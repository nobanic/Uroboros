package db

import (
	"database/sql"
	"fmt"
)

// A batch keeps its result private until all rows and the refreshed category
// have been read and the original transaction commits. No cache publication.
type taskCategoryMove struct {
	requested   []int64
	destination *int64
	updated     []int64
	category    *TaskCategory
}

func fmtCategoryIDInvalid() error {
	return fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
}
func (move *taskCategoryMove) validate() error {
	switch {
	case len(move.requested) == 0:
		return fmt.Errorf("%w: task ids are required", ErrTaskCategoryInvalid)
	case len(move.requested) > MaxTaskCategoryBatchSize:
		return fmt.Errorf("%w: at most %d tasks per request", ErrTaskCategoryInvalid, MaxTaskCategoryBatchSize)
	}
	for _, id := range move.requested {
		if id <= 0 {
			return fmt.Errorf("%w: task id must be positive", ErrTaskCategoryInvalid)
		}
	}
	if move.destination != nil && *move.destination <= 0 {
		return fmtCategoryIDInvalid()
	}
	return nil
}
func (move *taskCategoryMove) commit(store taskCategoryStore) error {
	if failure := move.validate(); failure != nil {
		return failure
	}
	transaction, failure := store.database.Begin()
	if failure != nil {
		return failure
	}
	defer transaction.Rollback()
	if failure := move.apply(store, transaction); failure != nil {
		return failure
	}
	return transaction.Commit()
}
func (move *taskCategoryMove) apply(store taskCategoryStore, transaction *sql.Tx) error {
	if move.destination != nil {
		var exists bool
		if failure := transaction.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_categories WHERE id=$1)`, *move.destination).Scan(&exists); failure != nil {
			return failure
		}
		if !exists {
			return ErrTaskCategoryNotFound
		}
	}
	cursor, failure := transaction.Query(`
UPDATE tasks SET category_id=$2
WHERE id=ANY($1::bigint[]) AND deleted_at IS NULL
RETURNING id`, move.requested, move.destination)
	if failure != nil {
		return failure
	}
	move.updated = make([]int64, 0, len(move.requested))
	for cursor.Next() {
		var id int64
		if failure := cursor.Scan(&id); failure != nil {
			cursor.Close()
			return failure
		}
		move.updated = append(move.updated, id)
	}
	cursor.Close()
	if failure := cursor.Err(); failure != nil {
		return failure
	}
	if move.destination != nil {
		move.category, failure = store.find(transaction, *move.destination, sql.ErrNoRows)
	}
	return failure
}
