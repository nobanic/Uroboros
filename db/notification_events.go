package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/notify"
)

// Journal append/read projections stay separate from the protected finding transaction producer.
type notificationJournal struct {
	database  *DB
	ctx       context.Context
	kind      string
	findingID int64
	snapshot  notify.Snapshot
	assetIDs  []int64
}

func (journal notificationJournal) append() (int64, error) {
	raw, err := json.Marshal(journal.snapshot)
	if err != nil {
		return 0, fmt.Errorf("failed to serialize notification event snapshot: %w", err)
	}
	var id int64
	err = journal.database.QueryRowContext(journal.ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		journal.kind, journal.findingID, string(raw)).Scan(&id)
	return id, err
}
func (journal notificationJournal) assetNames() ([]string, error) {
	if len(journal.assetIDs) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, journal.assetIDs)
	rows, err := journal.database.QueryContext(journal.ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(journal.assetIDs))
	seen := map[int64]bool{}
	for _, id := range journal.assetIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}
func (journal notificationJournal) stats() (*NotificationStats, error) {
	var s NotificationStats
	if err := journal.database.QueryRowContext(journal.ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}
