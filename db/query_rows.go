package db

import "database/sql"

// The issuing adapter retains its SQL, bindings, context and transaction. This
// value borrows only its result and takes ownership of a successful cursor.
type queryRowCursor struct {
	rows *sql.Rows
	err  error
}

func queriedRows(rows *sql.Rows, err error) queryRowCursor {
	return queryRowCursor{rows: rows, err: err}
}

// Initial state is constructed after a successful query. The existing row
// collector discards state on a scan failure and retains it on a late iterator
// error. Closing stays local to this query and never closes the connection.
func collectQueryState[State any](query queryRowCursor, initial func() State, take func(rowDecoder, *State) error) (State, error) {
	if query.err != nil {
		var zero State
		return zero, query.err
	}
	defer query.rows.Close()
	return collectRowState(query.rows, initial(), func(state *State) error { return take(query.rows, state) })
}
