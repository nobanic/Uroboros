package db

import (
	"database/sql"
	"fmt"
)

// These writes share SQL execution, while their callers retain the original
// distinctions between ignored affected-row errors, counts and goal existence.
type recordMutation struct {
	database  recordConnection
	statement string
	values    []any
}

func (change recordMutation) execute() error {
	_, err := change.database.Exec(change.statement, change.values...)
	return err
}
func (change recordMutation) affected() (int64, error) {
	result, err := change.database.Exec(change.statement, change.values...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (change recordMutation) ignoredCount() (int64, error) {
	result, err := change.database.Exec(change.statement, change.values...)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return count, nil
}
func (change recordMutation) requireGoal() error {
	count, err := change.ignoredCount()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("goal does not exist")
	}
	return nil
}

// A provisional graph ID is returned even if COMMIT fails; scoped asset writes
// return zero on the same failure. Both keep the original deferred rollback.
type mutationTransaction struct {
	database       *DB
	retainIdentity bool
}

func (transaction mutationTransaction) run(write func(*sql.Tx) (int64, error)) (int64, error) {
	tx, err := transaction.database.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := write(tx)
	if err != nil {
		return 0, err
	}
	err = tx.Commit()
	if err != nil && !transaction.retainIdentity {
		return 0, err
	}
	return id, err
}
