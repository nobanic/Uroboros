package db

import "database/sql"

// Category storage owns row projection and missing/conflict policies. Manager
// commands retain task-state locks and publish live mirrors only after success.
type taskCategoryStore struct{ database *DB }
type taskCategoryConnection interface{ QueryRow(string, ...any) *sql.Row }

const taskCategoryReadFrom = `
SELECT ` + taskCategoryCols + `
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id`
const taskCategoryReadID = taskCategoryReadFrom + `
WHERE category.id=$1
GROUP BY category.id`

func taskCategoryRow(row rowDecoder, missing error, conflict bool) (*TaskCategory, error) {
	value, failure := scanTaskCategory(row)
	switch {
	case failure == sql.ErrNoRows:
		return nil, missing
	case conflict && taskCategoryUniqueViolation(failure):
		return nil, ErrTaskCategoryNameConflict
	case failure != nil:
		return nil, failure
	default:
		return &value, nil
	}
}
func (store taskCategoryStore) find(connection taskCategoryConnection, id int64, missing error) (*TaskCategory, error) {
	return taskCategoryRow(connection.QueryRow(taskCategoryReadID, id), missing, false)
}
func (store taskCategoryStore) write(statement string, values ...any) (int64, error) {
	result, failure := store.database.Exec(statement, values...)
	if failure != nil {
		return 0, failure
	}
	return result.RowsAffected()
}
func (store taskCategoryStore) create(name string) (*TaskCategory, error) {
	label, key, failure := normalizeTaskCategoryName(name)
	if failure != nil {
		return nil, failure
	}
	return taskCategoryRow(store.database.QueryRow(`
WITH inserted AS (
    INSERT INTO task_categories(name, nkey)
    VALUES ($1,$2)
    ON CONFLICT (nkey) DO NOTHING
    RETURNING *
)
SELECT inserted.id, inserted.name, inserted.nkey, 0, inserted.created_at, inserted.updated_at
FROM inserted`, label, key), ErrTaskCategoryNameConflict, false)
}
func (store taskCategoryStore) list() ([]*TaskCategory, error) {
	cursor, failure := store.database.Query(taskCategoryReadFrom + `
GROUP BY category.id
ORDER BY category.name, category.id`)
	if failure != nil {
		return nil, failure
	}
	defer cursor.Close()
	values := []*TaskCategory{}
	return collectRowValues(cursor, values, func() (*TaskCategory, error) {
		return taskCategoryRow(cursor, sql.ErrNoRows, false)
	})
}
func (store taskCategoryStore) rename(id int64, name string) (*TaskCategory, error) {
	label, key, failure := normalizeTaskCategoryName(name)
	if failure != nil {
		return nil, failure
	}
	return taskCategoryRow(store.database.QueryRow(`
WITH updated AS (
    UPDATE task_categories SET name=$2, nkey=$3 WHERE id=$1 RETURNING *
)
SELECT updated.id, updated.name, updated.nkey,
       (SELECT count(*) FROM tasks WHERE category_id=updated.id AND deleted_at IS NULL),
       updated.created_at, updated.updated_at
FROM updated`, id, label, key), ErrTaskCategoryNotFound, true)
}
func (store taskCategoryStore) remove(id int64) (bool, error) {
	affected, failure := store.write(`DELETE FROM task_categories WHERE id=$1`, id)
	return affected > 0, failure
}
func (store taskCategoryStore) assign(taskID int64, categoryID *int64) (*TaskCategory, error) {
	if categoryID == nil {
		affected, failure := store.write(`UPDATE tasks SET category_id=NULL WHERE id=$1 AND deleted_at IS NULL`, taskID)
		if failure == nil && affected == 0 {
			failure = ErrTaskCategoryTaskNotFound
		}
		return nil, failure
	}
	if *categoryID <= 0 {
		return nil, fmtCategoryIDInvalid()
	}
	category, failure := taskCategoryRow(store.database.QueryRow(`
WITH selected AS (
    SELECT * FROM task_categories WHERE id=$2
), updated AS (
    UPDATE tasks SET category_id=$2
    WHERE id=$1 AND deleted_at IS NULL AND EXISTS (SELECT 1 FROM selected)
    RETURNING id
)
SELECT selected.id, selected.name, selected.nkey,
       (SELECT count(*) FROM tasks WHERE category_id=selected.id AND deleted_at IS NULL),
       selected.created_at, selected.updated_at
FROM selected, updated`, taskID, *categoryID), sql.ErrNoRows, false)
	if failure == sql.ErrNoRows {
		var exists bool
		failure = store.database.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&exists)
		if failure == nil {
			failure = ErrTaskCategoryNotFound
			if !exists {
				failure = ErrTaskCategoryTaskNotFound
			}
		}
	}
	return category, failure
}
func (store taskCategoryStore) assignMany(ids []int64, destination *int64) ([]int64, *TaskCategory, error) {
	move := taskCategoryMove{requested: ids, destination: destination}
	if failure := move.commit(store); failure != nil {
		return nil, nil, failure
	}
	return move.updated, move.category, nil
}
