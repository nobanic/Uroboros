package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Channel commands retain caller pointer defaults and atomic disable/skip behavior.
type notificationChannelStore struct {
	database *DB
	ctx      context.Context
	id       int64
	channel  *NotificationChannel
	enabled  bool
}

func (channels notificationChannelStore) list() ([]*NotificationChannel, error) {
	rows, err := channels.database.QueryContext(channels.ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	return collectRowValues(rows, out, func() (*NotificationChannel, error) {
		return scanNotificationChannel(rows)
	})
}
func (channels notificationChannelStore) one() (*NotificationChannel, error) {
	row := channels.database.QueryRowContext(channels.ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, channels.id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}
func (channels notificationChannelStore) save() (int64, error) {
	if channels.channel.Mode == "" {
		channels.channel.Mode = NotifyModeRealtime
	}
	// Deliberately do **not** massage 0 here: 0 is a valid config meaning "no rate limit".
	//
	// This was once written as `if c.RatePerMin <= 0 { c.RatePerMin = default }`, meaning "give a safe
	// default when unspecified", but it swallowed "explicitly set to 0" too — docs, UI hints and takeTokens
	// all read 0 as unlimited, while only here it was silently bumped to 20 (DingTalk/WeCom/Telegram) or 100
	// (Feishu), so the operator thought the limit was lifted but was actually capped at 20/min with no hint.
	//
	// Only the caller knows "unspecified" vs. "explicit 0" (field absent in the request body vs. explicitly 0),
	// so the default is filled by the server layer when the field is absent, see notifyCreateChannel.
	if channels.channel.RatePerMin < 0 {
		return 0, errors.New("rate limit value cannot be negative")
	}
	if channels.channel.Config == nil {
		channels.channel.Config = json.RawMessage(`{}`)
	}
	if channels.channel.Filter == nil {
		channels.channel.Filter = json.RawMessage(`{}`)
	}
	enabled := channels.channel.IsEnabled()

	if channels.channel.ID == 0 {
		var id int64
		err := channels.database.QueryRowContext(channels.ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			channels.channel.Name, channels.channel.Kind, enabled, string(channels.channel.Config), channels.channel.Mode, string(channels.channel.Filter), channels.channel.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := channels.database.ExecContext(channels.ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		channels.channel.ID, channels.channel.Name, channels.channel.Kind, enabled, string(channels.channel.Config), channels.channel.Mode, string(channels.channel.Filter), channels.channel.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return channels.channel.ID, nil
}
func (channels notificationChannelStore) enable() error {
	return channels.database.WithEvidenceTx(channels.ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(channels.ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, channels.id, channels.enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !channels.enabled {
			if _, err := tx.ExecContext(channels.ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				channels.id, NotifyStateSkipped, "channel disabled", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}
func (channels notificationChannelStore) remove() error {
	res, err := channels.database.ExecContext(channels.ctx, `DELETE FROM notification_channels WHERE id=$1`, channels.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}
