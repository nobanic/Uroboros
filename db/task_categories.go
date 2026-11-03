package db

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const MaxTaskCategoryNameRunes = 80

// MaxTaskCategoryBatchSize bounds one batch move so a single request cannot lock
// an unbounded number of task rows.
const MaxTaskCategoryBatchSize = 100

var (
	ErrTaskCategoryInvalid      = errors.New("invalid task category")
	ErrTaskCategoryNameConflict = errors.New("task category name already exists")
	ErrTaskCategoryNotFound     = errors.New("task category not found")
	ErrTaskCategoryTaskNotFound = errors.New("task not found")
)

// TaskCategory is a globally reusable task grouping label.
type TaskCategory struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"-"`
	TaskCount int       `json:"task_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const taskCategoryCols = `category.id, category.name, category.nkey,
       count(task.id) FILTER (WHERE task.deleted_at IS NULL),
       category.created_at, category.updated_at`

func scanTaskCategory(row interface{ Scan(...any) error }) (TaskCategory, error) {
	var category TaskCategory
	err := row.Scan(&category.ID, &category.Name, &category.NKey, &category.TaskCount, &category.CreatedAt, &category.UpdatedAt)
	return category, err
}

func normalizeTaskCategoryName(name string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", "", fmt.Errorf("%w: name is required", ErrTaskCategoryInvalid)
	}
	if utf8.RuneCountInString(name) > MaxTaskCategoryNameRunes {
		return "", "", fmt.Errorf("%w: name exceeds %d characters", ErrTaskCategoryInvalid, MaxTaskCategoryNameRunes)
	}
	return name, strings.ToLower(name), nil
}

func taskCategoryUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (d *DB) CreateTaskCategory(name string) (*TaskCategory, error) {
	return (taskCategoryStore{d}).create(name)
}

func (d *DB) ListTaskCategories() ([]*TaskCategory, error) {
	return (taskCategoryStore{d}).list()
}

func (d *DB) GetTaskCategory(id int64) (*TaskCategory, error) {
	return (taskCategoryStore{d}).find(d, id, nil)
}

func (d *DB) RenameTaskCategory(id int64, name string) (*TaskCategory, error) {
	return (taskCategoryStore{d}).rename(id, name)
}

// DeleteTaskCategory moves affected tasks to the uncategorized bucket through
// the tasks.category_id ON DELETE SET NULL foreign key.
func (d *DB) DeleteTaskCategory(id int64) (bool, error) {
	return (taskCategoryStore{d}).remove(id)
}

// SetTaskCategory updates one live task. A nil category means uncategorized.
func (d *DB) SetTaskCategory(taskID int64, categoryID *int64) (*TaskCategory, error) {
	return (taskCategoryStore{d}).assign(taskID, categoryID)
}

// SetTasksCategory moves several tasks into one category (nil = uncategorized)
// inside a single transaction, so a half-applied batch is never observable.
// It returns the ids that were actually updated — ids missing from that slice
// were deleted between selection and submit — plus the refreshed category row
// whose task_count already reflects this move.
func (d *DB) SetTasksCategory(taskIDs []int64, categoryID *int64) ([]int64, *TaskCategory, error) {
	return (taskCategoryStore{d}).assignMany(taskIDs, categoryID)
}
