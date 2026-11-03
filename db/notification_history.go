package db

import (
	"context"
	"fmt"
)

type notificationDeliveryHistory struct {
	database             *DB
	ctx                  context.Context
	filter               NotificationDeliveryFilter
	pageNumber, pageSize int
}

func (history notificationDeliveryHistory) page() ([]*NotificationDelivery, int, error) {
	if history.pageNumber < 1 {
		history.pageNumber = 1
	}
	if history.pageSize <= 0 || history.pageSize > 200 {
		history.pageSize = 50
	}
	where, args := history.filter.where()

	var total int
	if err := history.database.QueryRowContext(history.ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := history.database.QueryContext(history.ctx, q, append(args, history.pageSize, (history.pageNumber-1)*history.pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}
