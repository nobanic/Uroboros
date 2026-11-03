package db

// Callers retain query, transaction and cursor ownership. Decode failures
// discard the returned state; terminal iteration errors preserve partial state.
func collectRowState[State any](cursor interface {
	Next() bool
	Err() error
}, initial State, take func(*State) error) (State, error) {
	finished, err := visitRows(cursor, func() error { return take(&initial) })
	if !finished {
		var zero State
		return zero, err
	}
	return initial, err
}

// The initial slice retains each reader's nil, allocated and capacity policy.
func collectRowValues[Value any](cursor interface {
	Next() bool
	Err() error
}, initial []Value, decode func() (Value, error)) ([]Value, error) {
	return collectRowState(cursor, initial, func(values *[]Value) error {
		value, err := decode()
		if err != nil {
			return err
		}
		*values = append(*values, value)
		return nil
	})
}

// A completed iteration can still carry a terminal cursor error. A decoder
// failure stops immediately and never reads the cursor's terminal error.
func visitRows(cursor interface {
	Next() bool
	Err() error
}, take func() error) (bool, error) {
	for cursor.Next() {
		if err := take(); err != nil {
			return false, err
		}
	}
	return true, cursor.Err()
}
