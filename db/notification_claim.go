package db

import (
	"context"
	"database/sql"
	"time"
)

// A channel selection binds quota-independent row limits and lease duration.
type notificationChannelClaim struct {
	database          *DB
	ctx               context.Context
	channelID         int64
	limit             int
	lease, minimumAge time.Duration
}

// A lease claim owns select/lock/attempt/extension/postClaim/read/commit ordering.
type notificationLeaseClaim struct {
	database  *DB
	ctx       context.Context
	lease     time.Duration
	selection claimQuery
	postClaim func(*sql.Tx, []int64) error
}

func (claim notificationChannelClaim) realtime() ([]*NotificationDelivery, error) {
	if claim.limit <= 0 {
		return nil, nil
	}
	return claim.database.claimDeliveries(claim.ctx, claim.lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{claim.channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, claim.limit},
	}, nil)
}
func (claim notificationChannelClaim) due() (bool, error) {
	var due bool
	err := claim.database.QueryRowContext(claim.ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, claim.channelID, NotifyStatePending, NotifyStateSending, int64(claim.minimumAge.Seconds())).Scan(&due)
	return due, err
}
func (claim notificationChannelClaim) digest() ([]*NotificationDelivery, error) {
	if claim.limit <= 0 {
		return nil, nil
	}
	// limit is the **memory upper bound**; the caller passes MaxDigestBatchSize. We clamp it again here
	// to guard against a caller passing in a larger value.
	//
	// It deliberately does not accept a "rate-limit quota" as the batch size: the unit of rate limiting is
	// message count — one batch sends one message and consumes one token, deducted by the server layer's
	// takeTokens — a different dimension from "how many findings a batch holds". Once, to make rate_per_min
	// apply to digest, the per-round request budget was passed in as the batch size, and a rate=20/min
	// channel ended up holding just 1 finding per batch, degrading digest into realtime push with digest
	// wording. To change rate limiting, change takeTokens' want, not this.
	if claim.limit > MaxDigestBatchSize {
		claim.limit = MaxDigestBatchSize
	}
	out, err := claim.database.claimDeliveries(claim.ctx, claim.lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{claim.channelID, NotifyStatePending, NotifyStateSending, claim.limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(claim.ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}
func (claim notificationLeaseClaim) run() ([]*NotificationDelivery, error) {
	tx, err := claim.database.BeginTx(claim.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once the commit succeeds

	ids, err := selectForClaim(claim.ctx, tx, claim.selection.sql, claim.selection.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Set sending and push next_attempt_at into the future: that future moment is the lease expiry, so
	// "lease not expired" and "retry time not reached" share the same conditional expression, needing no new column.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(claim.ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, claim.lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if claim.postClaim != nil {
		if err := claim.postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(claim.ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
