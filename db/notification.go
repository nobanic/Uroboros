package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"log"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/notify"
)

// This file is the channel-config and event layer of IM push. Claiming of delivery tasks and their
// status transitions live in db/notification_delivery.go.
//
// Two invariants to preserve whenever you change this file:
//
//  1. The write-finding transaction (RecordFindingTx) only calls InsertNotificationEventTx to do a
//     single blind insert; it reads no notification table and does no filter matching. Any read
//     introduced here could pollute or even abort the finding-write transaction via a misconfigured filter.
//  2. Filter matching never errors: malformed config is always treated as a "hit" (see notify.Match).
//     Better to over-push than under-push.

// ErrNotificationChannelNotFound: the channel does not exist.
var ErrNotificationChannelNotFound = errors.New("notification channel not found")

// Delivery states.
const (
	NotifyStatePending = "pending" // to send
	NotifyStateSending = "sending" // claimed by some dispatcher, lease not yet expired
	NotifyStateSent    = "sent"    // delivered
	NotifyStateFailed  = "failed"  // retries exhausted or permanently failed, can be manually resent
	NotifyStateSkipped = "skipped" // channel disabled, no longer sent
)

// Push modes.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode validates the push mode against a whitelist (same as findings.status: no DB CHECK,
// to ease future extension).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel is a single channel instance config. Config and Filter stay as raw JSON,
// with parsing left to the notify package — the db layer doesn't understand their field meanings.
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled is a pointer to distinguish "field not provided" from "explicitly false" —
	// the frontend toggle control only submits the fields that changed.
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled reports whether the channel is enabled; a nil Enabled (not loaded) is treated as enabled.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent is a single event fact.
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels returns all channel instances, enabled ones first, ties broken by id.
// The ordering is done in SQL so the UI and the dispatcher see the same stable order.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	return (notificationChannelStore{database: d, ctx: ctx}).list()
}

// NotificationChannelByID fetches a single channel.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	return (notificationChannelStore{database: d, ctx: ctx, id: id}).one()
}

// SaveNotificationChannel creates or updates a channel.
//
// On update it only overwrites the fields the caller explicitly provided (non-nil / non-empty), so
// the frontend can submit a partially edited drawer form without echoing back the config fields it
// didn't display — echoing them back would cause a "masked value overwrites the real secret" accident.
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	return (notificationChannelStore{database: d, ctx: ctx, channel: c}).save()
}

// SetNotificationChannelEnabled toggles enabled/disabled.
//
// When disabling a channel, also mark its not-yet-sent deliveries as skipped: otherwise, after
// re-enabling, a batch of old findings "backlogged while disabled" would suddenly arrive, stale and easily mistaken for new.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return (notificationChannelStore{database: d, ctx: ctx, id: id, enabled: enabled}).enable()
}

// DeleteNotificationChannel deletes a channel. Its delivery history is cascade-deleted via the
// foreign key (with the channel config gone, the history can't be interpreted anyway).
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	return (notificationChannelStore{database: d, ctx: ctx, id: id}).remove()
}

// RecordNotificationEventTx makes a **best-effort** attempt to write a push event inside the caller's transaction.
//
// This is the only notification-related change on the finding-write path: one INSERT, reading no table,
// knowing no channel, running no filter. Committing the transaction guarantees "finding stored" and
// "push task exists" are atomically consistent — there is no window where the commit succeeds but nothing
// was enqueued and the message is lost forever.
//
// Two key design points, neither casual:
//
//  1. **Why a SAVEPOINT**: in PostgreSQL, any statement error inside a transaction puts the whole
//     transaction into the aborted state, after which every statement (including COMMIT) fails. So
//     "ignore this INSERT's error and let the caller keep committing" is impossible in PG — unless a
//     savepoint isolates the error to this one statement. Without one, "roll back the whole thing" is the only option left.
//
//  2. **Why rolling back the whole thing is wrong**: push is a convenience feature, the finding record
//     is the product itself. A notification-table problem (old DB not migrated, a transient disk fault)
//     must not keep a high-severity finding out of the database. So we isolate the error, log it, and
//     return false, letting the finding write commit as usual — at the cost of dropping this one push.
//     Returning a bool rather than an error is deliberate: the caller shouldn't treat it as a write-affecting error.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] failed to serialize push event finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] failed to create savepoint finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] failed to write push event finding=%d (the finding record is unaffected): %v", findingID, err)
		// Roll back to the savepoint, rescuing the transaction from the aborted state.
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] failed to roll back to savepoint finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// Release the savepoint, to avoid accumulating useless savepoints in a long transaction.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent is the standalone-transaction version of InsertNotificationEventTx, for call
// sites not already in a transaction (e.g. a channel's "send test message", which has no real finding).
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	return (notificationJournal{database: d, ctx: ctx, kind: kind, findingID: findingID, snapshot: snap}).append()
}

// FanOutPendingEvents expands not-yet-dispatched finding events into delivery tasks per the currently
// enabled channels, returning the number of events processed and deliveries created this round.
//
// The whole round runs in one transaction: events are claimed with FOR UPDATE SKIP LOCKED, so multiple
// processes running at once each claim different rows (the project's archive queue uses the same
// technique for claiming, see completeNextArchiveJob in db/task_archives.go).
//
// Filter matching is deliberately on the Go side rather than in SQL: a channel's filter is a JSONB of
// optional fields, and expressing the six-combination match in SQL would make the query hard to
// maintain, whereas channels number "a handful configured by hand", so loading them all and comparing in memory is faster and more testable.
//
// Events that hit no channel are marked fanned_out too — otherwise one would stay in the pending-dispatch
// set forever, re-scanned on every tick.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	return (&notificationFanout{database: d, ctx: ctx, limit: limit}).run()
}

// listEnabledNotificationChannelsTx fetches the enabled channels inside the transaction. They are few,
// so no pagination and no cache — a cache would introduce the extra timing problem of "when does a config change take effect".
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	return collectRowValues(rows, out, func() (*NotificationChannel, error) {
		var c NotificationChannel
		err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin)
		return &c, err
	})
}

// NotificationAssetNames resolves asset ids into short display names for use in push messages.
//
// The return order matches the input, and its length may be shorter than the input (nonexistent ids are
// skipped). Preserving input order keeps the asset order stable across repeated deliveries of the same
// finding — otherwise the asset order would change in a retried message and be misread as "the assets changed".
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	return (notificationJournal{database: d, ctx: ctx, assetIDs: ids}).assetNames()
}

// assetDisplayName picks the most distinctive identifier for an asset by type.
// It falls back to an empty string, leaving the caller to decide how to present an "asset whose name
// can't be resolved" — this function invents no placeholder, otherwise noise like "asset#42" would
// slip into push messages and the reader would take it for a real domain.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify updates a finding's disposition status and, in the same transaction,
// records a status-change push event.
//
// Returns from=the status before the change; found=whether the finding exists; notified=whether the event was recorded successfully.
//
// Three deliberate behaviors:
//   - No event is recorded when the status didn't actually change. A frontend drawer resubmitting the
//     same value, or an automation script replaying idempotently, should not produce push noise.
//   - When the finding doesn't exist, it returns found=false and writes nothing, for the caller to translate into a 404.
//   - A failed event record doesn't affect the status update (see the savepoint note in RecordNotificationEventTx),
//     so when notified=false the status has already been changed successfully and the caller shouldn't error on it.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx updates a finding's status and records a status-change push event inside the **caller's transaction**.
//
// It was extracted into a transaction-level function so all status-changing paths share the same
// semantics — previously only patchFinding used the notifying version, while **when a retest verdict is
// "fixed"** (the `UPDATE findings SET status=...` in finding_retests) the DB was written directly, so
// channels configured with `on_status_change` received no push for such transitions at all: the status
// changed quietly in the UI and ops only noticed on opening the platform.
//
// Returns from=the status before the change, found=whether the finding exists, changed=whether the status
// actually changed, notified=whether the event was recorded (a failed record doesn't affect the status update, see RecordNotificationEventTx).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// If the status didn't really change, don't record an event: resubmitting the same value or an
		// idempotent replay should not produce push noise.
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats holds the overview counts at the top of the notifications page.
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // age in ms of the oldest pending delivery
}

// NotificationStatsSnapshot summarizes the health of the notification system.
// BacklogAgeMS is the most direct indicator of "is push stuck" — far more useful than the pending
// count, because the difference between a backlog of 3 and a backlog of 3 can be anywhere from 3 seconds to 3 hours.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	return (notificationJournal{database: d, ctx: ctx}).stats()
}
