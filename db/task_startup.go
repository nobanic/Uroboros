package db

import (
	"context"
	"errors"
)

// InitialKatanaSkipped is read at the shared automatic admission boundary,
// including pending follow-ups and restored managers. Errors fail closed.
// Explicit source="tool" calls deliberately do not consult this choice.
func (d *DB) InitialKatanaSkipped(ctx context.Context, taskID int64) (bool, error) {
	if d == nil {
		return false, errors.New("task startup store is unavailable")
	}
	var skipped bool
	err := d.QueryRowContext(ctx, `SELECT skip_initial_katana FROM tasks WHERE id=$1`, taskID).Scan(&skipped)
	return skipped, err
}
