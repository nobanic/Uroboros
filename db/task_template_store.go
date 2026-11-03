package db

import "database/sql"

// Storage keeps the original atomic CASE-based patch and exact bound values.
// Rule decoding and nil-array normalization remain in the original codec.
type taskTemplateStore struct{ database *DB }

func taskTemplateRow(row rowDecoder, missing error, conflict bool) (*TaskTemplate, error) {
	value, failure := scanTaskTemplate(row)
	switch {
	case failure == sql.ErrNoRows:
		return nil, missing
	case conflict && taskTemplateUniqueViolation(failure):
		return nil, ErrTaskTemplateNameConflict
	case failure != nil:
		return nil, failure
	default:
		return &value, nil
	}
}
func (store taskTemplateStore) create(input TaskTemplateInput) (*TaskTemplate, error) {
	input, identity, failure := normalizeTaskTemplateInput(input)
	if failure != nil {
		return nil, failure
	}
	rules, failure := marshalTemplateRules(input.InterceptRules)
	if failure != nil {
		return nil, failure
	}
	return taskTemplateRow(store.database.QueryRow(`
INSERT INTO task_templates(name, nkey, description, goal, category_id, intercept_rules)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (nkey) DO NOTHING
RETURNING `+taskTemplateCols, input.Name, identity, input.Description, input.Goal, input.CategoryID, rules), ErrTaskTemplateNameConflict, false)
}
func (store taskTemplateStore) list() ([]*TaskTemplate, error) {
	cursor, failure := store.database.Query(`SELECT ` + taskTemplateCols + ` FROM task_templates ORDER BY updated_at DESC, id DESC`)
	if failure != nil {
		return nil, failure
	}
	defer cursor.Close()
	values := []*TaskTemplate{}
	return collectRowValues(cursor, values, func() (*TaskTemplate, error) {
		return taskTemplateRow(cursor, sql.ErrNoRows, false)
	})
}
func (store taskTemplateStore) get(id int64) (*TaskTemplate, error) {
	return taskTemplateRow(store.database.QueryRow(`SELECT `+taskTemplateCols+` FROM task_templates WHERE id=$1`, id), nil, false)
}
func (store taskTemplateStore) replaceText(id int64, input TaskTemplateInput) (*TaskTemplate, error) {
	input, _, failure := normalizeTaskTemplateInput(input)
	if failure != nil {
		return nil, failure
	}
	// Preserve the original full-update port: category and rules are not replaced.
	return store.database.PatchTaskTemplate(id, TaskTemplatePatch{Name: &input.Name, Description: &input.Description, Goal: &input.Goal})
}
func (store taskTemplateStore) patch(id int64, patch TaskTemplatePatch) (*TaskTemplate, error) {
	patch, identity, failure := normalizeTaskTemplatePatch(patch)
	if failure != nil {
		return nil, failure
	}
	rules, failure := marshalTemplateRules(patch.InterceptRules)
	if failure != nil {
		return nil, failure
	}
	return taskTemplateRow(store.database.QueryRow(`UPDATE task_templates
SET name=CASE WHEN $2 THEN $3::text ELSE name END,
    nkey=CASE WHEN $2 THEN $4::text ELSE nkey END,
    description=CASE WHEN $5 THEN $6::text ELSE description END,
    goal=CASE WHEN $7 THEN $8::text ELSE goal END,
    category_id=CASE WHEN $9 THEN $10::bigint ELSE category_id END,
    intercept_rules=CASE WHEN $11 THEN $12::jsonb ELSE intercept_rules END
WHERE id=$1
RETURNING `+taskTemplateCols,
		id, patch.Name != nil, patch.Name, identity,
		patch.Description != nil, patch.Description, patch.Goal != nil, patch.Goal,
		patch.SetCategoryID, patch.CategoryID, patch.SetInterceptRules, rules), ErrTaskTemplateNotFound, true)
}
func (store taskTemplateStore) remove(id int64) (bool, error) {
	result, failure := store.database.Exec(`DELETE FROM task_templates WHERE id=$1`, id)
	if failure != nil {
		return false, failure
	}
	affected, failure := result.RowsAffected()
	return affected > 0, failure
}
