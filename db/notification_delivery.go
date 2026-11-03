package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This file handles claiming delivery tasks and their status transitions.
//
// Claiming uses a "lease" rather than a long transaction: set the row to sending and push
// next_attempt_at into the future as the lease expiry, then commit the transaction before doing the
// network delivery. This way no database lock is held during delivery — a network request can take
// several seconds (client timeout 15s), and holding the row lock would stall other writes to the same DB.
//
// The cost is that if the process crashes mid-delivery, the row stays in sending. This is **self-healing**:
// once the lease expires, next_attempt_at falls into the past and the next claim round picks the same row
// up again (see state IN ('pending','sending') in the claim condition). The retry count is already +1 at
// claim time, so a crash can't cause infinite retries — once MaxNotifyAttempts chances are spent it lands in failed for manual handling.

// MaxNotifyAttempts is the maximum number of attempts for one delivery (including the first).
// Defined here rather than in the delivery engine: it is the state machine's own policy; the engine is just the executor.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize is the maximum number of deliveries one digest batch can merge at once.
//
// The reason it exists is resources: if a digest cycle turns up tens of thousands of findings (entirely
// possible — a single full scan can do it), with no upper bound the claim would read every row into
// memory, render one gigantic message, and then have most of it cut off by the channel's length limit —
// wasting memory and **silently losing** the findings that got cut. With an upper bound, the overflow
// stays in the DB as the next batch and goes out naturally next cycle, without loss.
//
// The basis for 500: it's the order of magnitude that, once rendered into a message, still has "readable
// content" within WeCom's 4096-byte limit; any larger just pushes the truncation point further back.
const MaxDigestBatchSize = 500

// NotificationDelivery is a single delivery task, including the channel config and event snapshot needed for rendering.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// Jointly loaded rendering context, not included in JSON (the server layer assembles the DTO).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind carried over from the event, so the history list can jump straight to the finding detail.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind are redundant fields for list display, sparing the frontend a second query.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery is the unified read shape for a delivery row: delivery + event snapshot + channel config.
// Rendering a message needs all three; querying them separately would mean three round-trips.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery describes one claim: first select candidates per sel and lock them, then set them to sending
// and extend the lease. The lease position in sel is a $n placeholder the caller fills in with its own arg.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries claims a batch of due realtime deliveries for a channel, up to limit rows.
//
// It deliberately claims **per single channel** rather than "claim a global batch then cherry-pick what
// to send": the rate-limit gate is maintained per channel in the delivery engine, and only by first
// knowing how many this channel can still send this round and then claiming exactly that many does the
// rate limit avoid consuming retries. Done the other way — claim first, discard later — rows blocked by
// the limit have already had attempts counted once, and the budget of 3 would be burned on pure waiting
// and finally land in failed.
//
// The condition includes "sending whose lease has expired" — that's the landing spot for crash self-healing.
// lease must be significantly larger than the worst-case time of a single delivery (channel HTTP client
// timeout 15s), otherwise the same row would be delivered by two dispatchers at once. It also blocks
// disabled channels: disabling already marked existing deliveries as skipped, and this is a second guard
// against leaks when disabling and claiming run concurrently.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	return (notificationChannelClaim{database: d, ctx: ctx, channelID: channelID, limit: limit, lease: lease}).realtime()
}

// DigestBatchDue reports whether the channel has accumulated a due batch: there is a pending delivery and
// **the oldest one** has reached the digest cycle age.
//
// The decision is based on the oldest delivery's age rather than the wall clock: this way a just-created
// channel won't immediately spit out a one-item "digest" because it aligned to the top of the hour, and a
// long-backlogged batch won't wait out another idle round.
//
// It's separate from ClaimDigestBatch because the semantics differ: this function only answers "should it
// send", whereas the claim must take away **all** of the channel's pending rows (including those not yet at
// age) — otherwise one cycle would be split into several messages and the digest would be pointless.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	return (notificationChannelClaim{database: d, ctx: ctx, channelID: channelID, minimumAge: minAge}).due()
}

// ClaimDigestBatch claims a channel's currently due pending deliveries as one digest batch, up to
// MaxDigestBatchSize rows per batch.
//
// All deliveries in the same batch share a batch_id, using the smallest id in the set as the batch number
// (stable, readable, no extra sequence needed). Retries use COALESCE to keep the original batch number, so
// "these N were sent together" still holds after multiple retries.
//
// Taking the first N by ascending id rather than at random: the earliest-produced deliveries go out first,
// so under backlog there's no "new findings sent first, old findings forever at the back" starvation.
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	return (notificationChannelClaim{database: d, ctx: ctx, channelID: channelID, limit: limit, lease: lease}).digest()
}

// claimDeliveries performs "select + set sending and extend lease + read full rows", all in one transaction.
// postClaim is an optional extra step (the digest batch uses it to write batch_id).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	return (notificationLeaseClaim{database: d, ctx: ctx, lease: lease, selection: cq, postClaim: postClaim}).run()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	return collectRowValues(rows, ids, func() (int64, error) {
		var id int64
		err := rows.Scan(&id)
		return id, err
	})
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	return collectRowValues(rows, out, func() (*NotificationDelivery, error) {
		return scanNotificationDelivery(rows)
	})
}

// MarkDeliveriesSent marks a batch of deliveries as delivered.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	return (notificationDeliveryWrite{database: d, ctx: ctx, ids: ids}).sent()
}

// RescheduleDeliveries returns a batch of deliveries to pending and pushes back the retry time.
//
// Returning to pending rather than introducing a new intermediate state keeps "how many chances remain"
// expressed in only one place (MaxNotifyAttempts), avoiding the state machine's branches ballooning with the retry policy.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	return (notificationDeliveryWrite{database: d, ctx: ctx, ids: ids, delay: delay, message: errMsg}).reschedule()
}

// DeferDeliveries returns a batch of deliveries to pending, immediately re-claimable, and **undoes the one attempt counted at claim time**.
//
// It has a single use: when a digest message is sent in segments bounded by the channel's length limit, the
// items that didn't fit in this one are held for the next batch. That isn't a failure, so it shouldn't
// consume the retry budget — attempts was optimistically +1 at claim time, and must be decremented back
// here. Otherwise a backlog of 500, cut into 25 segments of 20 each, would have tail items judged failed by
// MaxNotifyAttempts at the 3rd segment, though they never errored at all.
//
// GREATEST(...,0) guards the case of "someone manually resent, zeroing attempts, then reached here again",
// keeping the count from going negative.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	return (notificationDeliveryWrite{database: d, ctx: ctx, ids: ids, message: reason}).deferBatch()
}

// FailDeliveries marks a batch of deliveries as finally failed, awaiting a manual resend from the delivery history.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	return (notificationDeliveryWrite{database: d, ctx: ctx, ids: ids, message: errMsg}).fail()
}

// RetryNotificationDelivery manually resends one delivery: reset to pending, zero the retry count, due
// immediately. Zeroing the count is deliberate — a manual "resend" means the causes of the earlier
// failures have been handled, so limiting it by the old count makes no sense.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	return (notificationDeliveryWrite{database: d, ctx: ctx, id: id}).retry()
}

// NotificationDeliveryFilter is the query filter for delivery history.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries returns delivery history paginated, newest first.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	return (notificationDeliveryHistory{database: d, ctx: ctx, filter: f, pageNumber: page, pageSize: pageSize}).page()
}

// truncateNotifyError truncates the error message to a length the column can accept. A channel's response
// body can be very long (especially a generic Webhook hitting a self-hosted service), and not truncating would bloat the history list payload.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Back off to a character boundary, to avoid leaving half a UTF-8 character that the frontend shows as garbage.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders generates the $n placeholder string starting at start and the matching args, for use in IN (...).
// For example start=3, ids=[7,8] → "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
