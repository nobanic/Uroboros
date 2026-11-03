package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/notify"
)

func newNotificationChannel(input notifyChannelRequest) (*db.NotificationChannel, error) {
	if input.Kind == nil || !notify.ValidKind(*input.Kind) {
		return nil, fmt.Errorf("invalid channel kind, options: %s", strings.Join(notify.Kinds(), " / "))
	}
	name := ""
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if name == "" {
		return nil, errors.New("channel name is required")
	}
	adapter, _ := notify.Get(*input.Kind)
	if err := adapter.Validate(input.Config); err != nil {
		return nil, err
	}
	channel := &db.NotificationChannel{Name: name, Kind: *input.Kind, Enabled: input.Enabled,
		Mode: db.NotifyModeRealtime, RatePerMin: adapter.DefaultRatePerMin()}
	if err := applyNotificationTuning(channel, input, adapter.DefaultRatePerMin); err != nil {
		return nil, err
	}
	channel.Config, _ = json.Marshal(input.Config)
	return channel, nil
}

func editNotificationChannel(current *db.NotificationChannel, input notifyChannelRequest) (*db.NotificationChannel, error) {
	kind := current.Kind
	if input.Kind != nil {
		if !notify.ValidKind(*input.Kind) {
			return nil, fmt.Errorf("invalid channel kind, options: %s", strings.Join(notify.Kinds(), " / "))
		}
		kind = *input.Kind
	}
	adapter, _ := notify.Get(kind)
	var stored map[string]any
	if kind == current.Kind && len(current.Config) > 0 {
		_ = json.Unmarshal(current.Config, &stored)
	}
	if stored == nil {
		stored = map[string]any{}
	}
	merged, err := notify.PrepareConfigUpdate(kind, stored, input.Config)
	if err != nil {
		return nil, err
	}
	if err := adapter.Validate(merged); err != nil {
		return nil, err
	}
	config, _ := json.Marshal(merged)
	channel := &db.NotificationChannel{ID: current.ID, Name: current.Name, Kind: kind,
		Enabled: current.Enabled, Mode: current.Mode, Config: config, Filter: current.Filter, RatePerMin: current.RatePerMin}
	if input.Name != nil {
		channel.Name = strings.TrimSpace(*input.Name)
		if channel.Name == "" {
			return nil, errors.New("channel name must not be empty")
		}
	}
	if input.Enabled != nil {
		channel.Enabled = input.Enabled
	}
	if err := applyNotificationTuning(channel, input, nil); err != nil {
		return nil, err
	}
	return channel, nil
}

// Keep omitted versus explicit zero/false and the original validation order.
// Only a new channel gets a default rate; PATCH retains stored tuning.
func applyNotificationTuning(channel *db.NotificationChannel, input notifyChannelRequest, defaultRate func() int) error {
	if input.Mode != nil {
		if !db.ValidNotifyMode(*input.Mode) {
			return errors.New("invalid push mode, options: realtime / digest")
		}
		channel.Mode = *input.Mode
	}
	if input.RatePerMin != nil {
		if *input.RatePerMin < 0 {
			return errors.New("rate limit must not be negative")
		}
		channel.RatePerMin = *input.RatePerMin
	} else if defaultRate != nil {
		channel.RatePerMin = defaultRate()
	}
	if input.Filter != nil {
		if err := input.Filter.Validate(); err != nil {
			return err
		}
		channel.Filter, _ = json.Marshal(input.Filter)
	}
	return nil
}

// Config save precedes the enable transition. These are deliberately separate
// original writes: toggle failure can leave the config committed. Disabling
// uses the store's skip-backlog transition, never a generic save alone.
func commitNotificationChannelUpdate(ctx context.Context, pg *db.DB, current, edited *db.NotificationChannel) error {
	changed := edited.Enabled != nil && current.Enabled != nil && *edited.Enabled != *current.Enabled
	if !changed {
		_, err := pg.SaveNotificationChannel(ctx, edited)
		return err
	}
	enabled := edited.Enabled
	edited.Enabled = current.Enabled
	if _, err := pg.SaveNotificationChannel(ctx, edited); err != nil {
		return err
	}
	return pg.SetNotificationChannelEnabled(ctx, edited.ID, *enabled)
}
