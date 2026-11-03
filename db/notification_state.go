package db

import (
	"context"
	"fmt"
	"time"
)

// State transitions retain distinct attempt refund, reset and retry-time policies.
type notificationDeliveryWrite struct {
	database *DB
	ctx      context.Context
	ids      []int64
	id       int64
	delay    time.Duration
	message  string
}

func (write notificationDeliveryWrite) sent() error {
	ph, args := placeholders(2, write.ids)
	if len(args) == 0 {
		return nil
	}
	_, err := write.database.ExecContext(write.ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}
func (write notificationDeliveryWrite) reschedule() error {
	ph, args := placeholders(4, write.ids)
	if len(args) == 0 {
		return nil
	}
	_, err := write.database.ExecContext(write.ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, write.delay.Seconds(), truncateNotifyError(write.message)}, args...)...)
	return err
}
func (write notificationDeliveryWrite) deferBatch() error {
	ph, args := placeholders(3, write.ids)
	if len(args) == 0 {
		return nil
	}
	_, err := write.database.ExecContext(write.ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(write.message)}, args...)...)
	return err
}
func (write notificationDeliveryWrite) fail() error {
	// Placeholders start at $3: $1 is state, $2 is last_error.
	ph, args := placeholders(3, write.ids)
	if len(args) == 0 {
		return nil
	}
	_, err := write.database.ExecContext(write.ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(write.message)}, args...)...)
	return err
}
func (write notificationDeliveryWrite) retry() error {
	res, err := write.database.ExecContext(write.ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, write.id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delivery %d does not exist or its current state does not allow resending", write.id)
	}
	return nil
}
