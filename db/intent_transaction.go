package db

import "database/sql"

// transactIntent owns Begin/rollback/commit. Callback errors retain the callback's
// result; commit errors retain the successful result. Each port projects its
// original public policy, including zeroed follow-up results on commit failure.
func transactIntent[T any](store *ExplorationStore, work func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	transaction, err := store.db.Begin()
	if err != nil {
		return zero, err
	}
	defer transaction.Rollback()
	value, err := work(transaction)
	if err != nil {
		return value, err
	}
	return value, transaction.Commit()
}
