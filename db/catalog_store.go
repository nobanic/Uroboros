package db

import "database/sql"

// This catalog borrows the existing connection for one operation. It contains
// persisted configuration only; assembly retains all runtime access decisions.
type agentCatalog struct{ database *DB }

// A failed query/scan returns the type's zero value. A terminal rows error keeps
// the accumulated result, including each caller's intentional empty container.
func catalogRead[T any](catalog agentCatalog, statement string, args []any, initial T, take func(*T, rowDecoder) error) (T, error) {
	rows, err := catalog.database.Query(statement, args...)
	var zero T
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	return collectRowState(rows, initial, func(value *T) error { return take(value, rows) })
}

func catalogColumn[T any](catalog agentCatalog, statement string, args ...any) ([]T, error) {
	return catalogRead(catalog, statement, args, []T(nil), func(values *[]T, row rowDecoder) error {
		var value T
		if err := row.Scan(&value); err != nil {
			return err
		}
		*values = append(*values, value)
		return nil
	})
}

func (catalog agentCatalog) write(statement string, args ...any) error {
	_, err := catalog.database.Exec(statement, args...)
	return err
}

func (catalog agentCatalog) transaction(change func(*sql.Tx) error) error {
	tx, err := catalog.database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Closed repository statements own the replacement boundary and upsert policy.
// Values remain bound parameters; an empty collection still clears its scope.
type catalogReplacement[T any] struct {
	erase, insert string
	scope         []any
	entries       []T
	bind          func(T) []any
}

func (change catalogReplacement[T]) apply(catalog agentCatalog) error {
	return catalog.transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(change.erase, change.scope...); err != nil {
			return err
		}
		for _, entry := range change.entries {
			if _, err := tx.Exec(change.insert, change.bind(entry)...); err != nil {
				return err
			}
		}
		return nil
	})
}
