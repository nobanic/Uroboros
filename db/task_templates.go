package db

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxTaskTemplateNameRunes = 120
	MaxTaskTemplateTextRunes = 16000
)

var (
	ErrTaskTemplateInvalid      = errors.New("invalid task template")
	ErrTaskTemplateNameConflict = errors.New("task template name already exists")
	ErrTaskTemplateNotFound     = errors.New("task template not found")
)

// TaskTemplate is a reusable task preset (description/goal + optional category
// and task-level intercept/allow rules).
type TaskTemplate struct {
	ID             int64                    `json:"id"`
	Name           string                   `json:"name"`
	NKey           string                   `json:"-"`
	Description    string                   `json:"description"`
	Goal           string                   `json:"goal"`
	CategoryID     *int64                   `json:"category_id"`
	InterceptRules []TaskInterceptRuleInput `json:"intercept_rules"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// TaskTemplateInput is the create/update payload after normalization.
type TaskTemplateInput struct {
	Name           string
	Description    string
	Goal           string
	CategoryID     *int64
	InterceptRules []TaskInterceptRuleInput
}

// TaskTemplatePatch changes only fields flagged as set. Name/Description/Goal use
// non-nil pointers; CategoryID/InterceptRules use explicit Set flags (so a nil
// CategoryID can mean "clear" when SetCategoryID is true).
type TaskTemplatePatch struct {
	Name              *string
	Description       *string
	Goal              *string
	CategoryID        *int64
	SetCategoryID     bool
	InterceptRules    []TaskInterceptRuleInput
	SetInterceptRules bool
}

const taskTemplateCols = `id, name, nkey, description, goal, category_id, intercept_rules, created_at, updated_at`

func scanTaskTemplate(row interface{ Scan(...any) error }) (TaskTemplate, error) {
	var t TaskTemplate
	var rulesRaw []byte
	if err := row.Scan(&t.ID, &t.Name, &t.NKey, &t.Description, &t.Goal, &t.CategoryID, &rulesRaw, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return t, err
	}
	t.InterceptRules = []TaskInterceptRuleInput{}
	if len(rulesRaw) > 0 {
		if err := json.Unmarshal(rulesRaw, &t.InterceptRules); err != nil {
			return t, err
		}
		if t.InterceptRules == nil {
			t.InterceptRules = []TaskInterceptRuleInput{}
		}
	}
	return t, nil
}

// marshalTemplateRules serializes a template's rule snapshot to JSONB text,
// always producing a JSON array (never null).
func marshalTemplateRules(rules []TaskInterceptRuleInput) ([]byte, error) {
	if rules == nil {
		rules = []TaskInterceptRuleInput{}
	}
	return json.Marshal(rules)
}

// taskTemplateName normalizes display whitespace while preserving the user's case.
func taskTemplateName(name string) string { return strings.Join(strings.Fields(name), " ") }

// taskTemplateNKey is the case-insensitive identity used by the unique index.
func taskTemplateNKey(name string) string { return strings.ToLower(taskTemplateName(name)) }

func normalizeTaskTemplateInput(in TaskTemplateInput) (TaskTemplateInput, string, error) {
	return taskTemplateInputFields(in)
}

func normalizeTaskTemplatePatch(patch TaskTemplatePatch) (TaskTemplatePatch, *string, error) {
	return taskTemplatePatchFields(patch)
}

func taskTemplateUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateTaskTemplate inserts one globally reusable preset.
func (d *DB) CreateTaskTemplate(in TaskTemplateInput) (*TaskTemplate, error) {
	return (taskTemplateStore{d}).create(in)
}

// ListTaskTemplates returns the most recently maintained templates first.
func (d *DB) ListTaskTemplates() ([]*TaskTemplate, error) {
	return (taskTemplateStore{d}).list()
}

// GetTaskTemplate returns nil when id does not exist.
func (d *DB) GetTaskTemplate(id int64) (*TaskTemplate, error) {
	return (taskTemplateStore{d}).get(id)
}

// UpdateTaskTemplate replaces the editable fields of one preset.
func (d *DB) UpdateTaskTemplate(id int64, in TaskTemplateInput) (*TaskTemplate, error) {
	return (taskTemplateStore{d}).replaceText(id, in)
}

// PatchTaskTemplate atomically changes only the supplied fields. Keeping the
// merge in one UPDATE prevents concurrent disjoint PATCH requests from losing
// each other's changes.
func (d *DB) PatchTaskTemplate(id int64, patch TaskTemplatePatch) (*TaskTemplate, error) {
	return (taskTemplateStore{d}).patch(id, patch)
}

// DeleteTaskTemplate deletes one preset and reports whether it existed.
func (d *DB) DeleteTaskTemplate(id int64) (bool, error) {
	return (taskTemplateStore{d}).remove(id)
}
