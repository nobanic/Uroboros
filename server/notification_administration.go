package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/notify"
)

// notificationAdministration borrows HTTP state and the existing DB handle for
// one request. Persistent delivery/credential authority stays with existing stores.
type notificationAdministration struct {
	server  *Server
	pg      *db.DB
	writer  http.ResponseWriter
	request *http.Request
}

func newNotificationAdministration(s *Server, w http.ResponseWriter, r *http.Request) *notificationAdministration {
	pg := s.pg(w)
	if pg == nil {
		return nil
	}
	return &notificationAdministration{server: s, pg: pg, writer: w, request: r}
}

func (a *notificationAdministration) metadata() {
	kinds := make([]map[string]any, 0, len(notify.Kinds()))
	for _, kind := range notify.Kinds() {
		channel, _ := notify.Get(kind)
		kinds = append(kinds, map[string]any{"kind": kind, "default_rate_per_min": channel.DefaultRatePerMin(), "secret_keys": channel.SecretKeys()})
	}
	baseURL, _, _ := a.pg.GetSetting(settingNotifyPublicBaseURL)
	digest, _, _ := a.pg.GetSetting(settingNotifyDigestMinutes)
	stats, err := a.pg.NotificationStatsSnapshot(a.request.Context())
	if err != nil {
		writeErr(a.writer, 500, err.Error())
		return
	}
	writeJSON(a.writer, 200, map[string]any{"kinds": kinds, "enabled": a.pg.GetBool(settingNotifyEnabled, true),
		"public_base_url": baseURL, "digest_interval_min": digest,
		"defaults": map[string]any{"digest_interval_min": notifyDefaultDigestMinutes}, "stats": stats})
}

func (a *notificationAdministration) channels() {
	rows, err := a.pg.ListNotificationChannels(a.request.Context())
	if err != nil {
		writeErr(a.writer, 500, err.Error())
		return
	}
	document := make([]notifyChannelDTO, 0, len(rows))
	for _, row := range rows {
		document = append(document, toNotifyChannelDTO(row))
	}
	writeJSON(a.writer, 200, map[string]any{"channels": document})
}

func (a *notificationAdministration) decodeChannel() (*notifyChannelRequest, bool) {
	var input notifyChannelRequest
	if err := json.NewDecoder(a.request.Body).Decode(&input); err != nil {
		writeErr(a.writer, 400, "request body is not valid JSON: "+err.Error())
		return nil, false
	}
	return &input, true
}

func (a *notificationAdministration) identifier(message string) (int64, bool) {
	id, ok := pathInt(a.request, "id")
	if !ok {
		writeErr(a.writer, 400, message)
	}
	return id, ok
}

func (a *notificationAdministration) channel(id int64) (*db.NotificationChannel, bool) {
	row, err := a.pg.NotificationChannelByID(a.request.Context(), id)
	if err != nil {
		notifyChannelLookupErr(a.writer, err)
		return nil, false
	}
	return row, true
}

func (a *notificationAdministration) createChannel() {
	input, ok := a.decodeChannel()
	if !ok {
		return
	}
	channel, err := newNotificationChannel(*input)
	if err != nil {
		writeErr(a.writer, 400, err.Error())
		return
	}
	id, err := a.pg.SaveNotificationChannel(a.request.Context(), channel)
	if err != nil {
		writeErr(a.writer, 500, err.Error())
		return
	}
	writeJSON(a.writer, 200, map[string]any{"id": id})
}

func (a *notificationAdministration) updateChannel() {
	id, ok := a.identifier("invalid channel id")
	if !ok {
		return
	}
	current, ok := a.channel(id)
	if !ok {
		return
	}
	input, ok := a.decodeChannel()
	if !ok {
		return
	}
	edited, err := editNotificationChannel(current, *input)
	if err != nil {
		writeErr(a.writer, 400, err.Error())
		return
	}
	if err := commitNotificationChannelUpdate(a.request.Context(), a.pg, current, edited); err != nil {
		writeErr(a.writer, 500, err.Error())
		return
	}
	writeJSON(a.writer, 200, map[string]any{"id": id})
}

func (a *notificationAdministration) removeChannel() {
	id, ok := a.identifier("invalid channel id")
	if !ok {
		return
	}
	if err := a.pg.DeleteNotificationChannel(a.request.Context(), id); err != nil {
		notifyChannelLookupErr(a.writer, err)
		return
	}
	writeJSON(a.writer, 200, map[string]any{"ok": true})
}

func (a *notificationAdministration) testChannel() {
	id, ok := a.identifier("invalid channel id")
	if !ok {
		return
	}
	row, ok := a.channel(id)
	if !ok {
		return
	}
	channel, ok := notify.Get(row.Kind)
	if !ok {
		writeErr(a.writer, 400, fmt.Sprintf("channel kind %q is not registered", row.Kind))
		return
	}
	var config map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &config)
	}
	if err := channel.Validate(config); err != nil {
		writeErr(a.writer, 400, err.Error())
		return
	}
	message := notifyTestMessage(a.server.notifierBaseURL(a.pg))
	start := time.Now()
	if _, err := channel.Send(a.request.Context(), config, message); err != nil {
		writeErr(a.writer, 502, err.Error())
		return
	}
	writeJSON(a.writer, 200, map[string]any{"ok": true, "latency_ms": time.Since(start).Milliseconds()})
}

type notificationDeliveryPage struct {
	filter     db.NotificationDeliveryFilter
	page, size int
}

func notificationHistoryPage(request *http.Request) notificationDeliveryPage {
	query := request.URL.Query()
	p := notificationDeliveryPage{filter: db.NotificationDeliveryFilter{State: query.Get("state"), EventKind: query.Get("event_kind")},
		page: atoiDefault(query.Get("page"), 1), size: atoiDefault(query.Get("page_size"), 50)}
	if id := query.Get("channel_id"); id != "" {
		p.filter.ChannelID = int64(atoiDefault(id, 0))
	}
	if p.page <= 0 {
		p.page = 1
	}
	if p.size <= 0 {
		p.size = 50
	}
	return p
}

func (a *notificationAdministration) deliveries() {
	selection := notificationHistoryPage(a.request)
	rows, total, err := a.pg.ListNotificationDeliveries(a.request.Context(), selection.filter, selection.page, selection.size)
	if err != nil {
		writeErr(a.writer, 500, err.Error())
		return
	}
	document := make([]notifyDeliveryDTO, 0, len(rows))
	for _, row := range rows {
		document = append(document, toNotifyDeliveryDTO(row))
	}
	writeJSON(a.writer, 200, map[string]any{"deliveries": document, "total": total, "page": selection.page, "page_size": selection.size})
}

func (a *notificationAdministration) retryDelivery() {
	id, ok := a.identifier("invalid delivery id")
	if !ok {
		return
	}
	if err := a.pg.RetryNotificationDelivery(a.request.Context(), id); err != nil {
		writeErr(a.writer, 400, err.Error())
		return
	}
	writeJSON(a.writer, 200, map[string]any{"ok": true})
}
