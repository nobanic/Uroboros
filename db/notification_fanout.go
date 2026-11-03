package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/notify"
	"strings"
)

// One fanout round owns its transaction and decoded input collections. Matching
// and queue writes follow the original order after all input rows are closed.
type notificationFanout struct {
	database  *DB
	ctx       context.Context
	limit     int
	tx        *sql.Tx
	channels  []*NotificationChannel
	events    []NotificationEvent
	snapshots []notify.Snapshot
}

func (fanout *notificationFanout) run() (eventCount, deliveryCount int, err error) {
	if fanout.limit <= 0 {
		fanout.limit = 200
	}
	fanout.tx, err = fanout.database.BeginTx(fanout.ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer fanout.tx.Rollback()
	if err := fanout.load(); err != nil {
		return 0, 0, err
	}
	if len(fanout.events) == 0 {
		return 0, 0, fanout.tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range fanout.snapshots {
		for _, ch := range fanout.channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: fanout.events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := fanout.tx.ExecContext(fanout.ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// Mark this round's events as dispatched. Events hitting no channel are marked too (see the function comment).
	ids := make([]string, 0, len(fanout.events))
	markArgs := make([]any, 0, len(fanout.events))
	for _, ev := range fanout.events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := fanout.tx.ExecContext(fanout.ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(fanout.events), len(toInsert), fanout.tx.Commit()
}
func (fanout *notificationFanout) load() error {
	var err error
	fanout.channels, err = listEnabledNotificationChannelsTx(fanout.ctx, fanout.tx)
	if err != nil {
		return err
	}
	rows, err := fanout.tx.QueryContext(fanout.ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, fanout.limit)
	if err != nil {
		return err
	}

	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return err
		}
		var snap notify.Snapshot
		// The snapshot is written by us, so in theory it always parses; a parse failure doesn't block the
		// delivery flow, but this event, with all fields empty, gets skipped by every filtered channel —
		// better to miss one push than to let a bad row jam the whole queue.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind is authoritative from the row value: the one in the snapshot is a copy for rendering, possibly written by an older version.
		snap.Kind = ev.Kind
		fanout.events = append(fanout.events, ev)
		fanout.snapshots = append(fanout.snapshots, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}
