package db

import (
	"context"
	"database/sql"
)

// A deletion owns one existing transaction. Children are locked before their
// profile; a newly committed reference aborts this attempt for the outer retry.
type profileDeletion struct {
	ctx   context.Context
	tx    *sql.Tx
	id    int64
	locks map[string]map[int64]struct{}
}

func deleteConfiguredProfile(store *DB, ctx context.Context, id int64) (bool, error) {
	tx, err := store.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	deletion := profileDeletion{ctx: ctx, tx: tx, id: id, locks: make(map[string]map[int64]struct{})}
	if err := deletion.lockReferences(); err != nil {
		return false, err
	}
	if err := deletion.lockProfile(); err != nil {
		return false, err
	}
	if changed, err := deletion.referencesChanged(); err != nil || changed {
		return changed, err
	}
	tasks, err := deletion.affectedTasks()
	if err != nil {
		return false, err
	}
	if err := deletion.exec(`DELETE FROM llm_profiles WHERE id=$1`, id); err != nil {
		return false, err
	}
	for _, task := range tasks {
		if err := deletion.advance(task); err != nil {
			return false, err
		}
	}
	return false, tx.Commit()
}

func (deletion *profileDeletion) exec(query string, args ...any) error {
	_, err := deletion.tx.ExecContext(deletion.ctx, query, args...)
	return err
}

func (deletion *profileDeletion) lockProfile() error {
	var active bool
	err := deletion.tx.QueryRowContext(deletion.ctx, `SELECT is_default FROM llm_profiles WHERE id=$1 FOR UPDATE`, deletion.id).Scan(&active)
	if err == sql.ErrNoRows {
		return ErrLLMProfileNotFound
	}
	if err != nil {
		return err
	}
	if active {
		return ErrActiveLLMProfileDelete
	}
	return nil
}

// Keep error and early-stop closure semantics shared by the two cursor reads.
// Early stop intentionally discards Close errors, as the original retry did.
func visitProfileDeletionRows(rows *sql.Rows, visit func(*sql.Rows) (bool, error)) (bool, error) {
	for rows.Next() {
		stop, err := visit(rows)
		if err != nil || stop {
			rows.Close()
			return stop, err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	return false, rows.Close()
}
