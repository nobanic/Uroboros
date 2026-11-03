package server

import (
	"encoding/json"
	"errors"

	"net/http"

	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/notify"
)

// This file is the HTTP API for notifications. All routes are mounted behind requireAuth (see Handler()),
// consistent with the other management APIs.

// notifyChannelDTO is the channel's external representation.
//
// Config is the **masked** config: credential fields are replaced with values prefixed by notify.MaskedPrefix.
// The frontend submitting a masked value back unchanged means "this field was not changed", and the server keeps the stored value
// accordingly (see notify.MergeConfig).
type notifyChannelDTO struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Enabled    bool           `json:"enabled"`
	Mode       string         `json:"mode"`
	Config     map[string]any `json:"config"`
	Filter     notify.Filter  `json:"filter"`
	RatePerMin int            `json:"rate_per_min"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	// SecretKeys tells the frontend which fields are credentials, so it renders password inputs and the "leave empty to keep" hint.
	// Declared by the channel itself (notify.Channel.SecretKeys); the frontend hardcodes no channel knowledge.
	SecretKeys []string `json:"secret_keys"`
}

// notifyDeliveryDTO is the external representation of delivery history.
type notifyDeliveryDTO struct {
	ID          int64      `json:"id"`
	FindingID   int64      `json:"finding_id,string"`
	EventKind   string     `json:"event_kind"`
	ChannelID   int64      `json:"channel_id"`
	ChannelName string     `json:"channel_name"`
	ChannelKind string     `json:"channel_kind"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error"`
	BatchID     *int64     `json:"batch_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	NextAttempt time.Time  `json:"next_attempt_at"`
	// Message title summary, so the history list shows what was pushed without expanding.
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

func toNotifyChannelDTO(ch *db.NotificationChannel) notifyChannelDTO {
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	secrets := []string{}
	if c, ok := notify.Get(ch.Kind); ok {
		secrets = c.SecretKeys()
	}
	return notifyChannelDTO{
		ID:         ch.ID,
		Name:       ch.Name,
		Kind:       ch.Kind,
		Enabled:    ch.IsEnabled(),
		Mode:       ch.Mode,
		Config:     notify.MaskConfig(ch.Kind, cfg),
		Filter:     notify.ParseFilter(ch.Filter),
		RatePerMin: ch.RatePerMin,
		CreatedAt:  ch.CreatedAt,
		UpdatedAt:  ch.UpdatedAt,
		SecretKeys: secrets,
	}
}

func toNotifyDeliveryDTO(dl *db.NotificationDelivery) notifyDeliveryDTO {
	snap, _ := parseSnapshot(dl)
	dto := notifyDeliveryDTO{
		ID:          dl.ID,
		FindingID:   dl.FindingID,
		EventKind:   dl.EventKind,
		ChannelID:   dl.ChannelID,
		ChannelName: dl.ChannelName,
		ChannelKind: dl.ChannelKind,
		State:       dl.State,
		Attempts:    dl.Attempts,
		LastError:   dl.LastError,
		BatchID:     dl.BatchID,
		CreatedAt:   dl.CreatedAt,
		SentAt:      dl.SentAt,
		NextAttempt: dl.NextAttemptAt,
		Severity:    snap.Severity,
	}
	if snap.Name != "" {
		dto.Title = snap.Name
	} else {
		dto.Title = snap.VulnClass
	}
	return dto
}

// notifyMeta returns the static metadata and global settings the notifications page needs, all in one request,
// so the frontend does not make three requests to render one dropdown.
func (s *Server) notifyMeta(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.metadata()
	}
}

func (s *Server) notifyListChannels(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.channels()
	}
}

// notifyChannelRequest is the request body for creating/updating a channel.
//
// All business fields are pointers so "not sent" can be distinguished from "sent as zero value": under PATCH semantics,
// a field not sent must keep its stored value.
type notifyChannelRequest struct {
	Name       *string        `json:"name"`
	Kind       *string        `json:"kind"`
	Enabled    *bool          `json:"enabled"`
	Mode       *string        `json:"mode"`
	Config     map[string]any `json:"config"`
	Filter     *notify.Filter `json:"filter"`
	RatePerMin *int           `json:"rate_per_min"`
}

func (s *Server) notifyCreateChannel(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.createChannel()
	}
}

func (s *Server) notifyUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.updateChannel()
	}
}

func (s *Server) notifyDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.removeChannel()
	}
}

// notifyTestChannel sends a test message using the currently saved config.
//
// It calls the channel's Send directly, bypassing the delivery queue: the point of a test is to tell the user immediately
// "whether this config can send", whereas the queue would bury the result in delivery history, forcing them to go check again.
// So this API is **synchronous**, with the timeout bounded by the notify package's HTTP client (15s).
func (s *Server) notifyTestChannel(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.testChannel()
	}
}

// notifyTestMessage builds the test message. It deliberately uses content that is obviously a test:
// the recipient should not mistake it for a real finding.
func notifyTestMessage(baseURL string) notify.Message {
	return notify.Message{
		Items: []notify.Item{{
			FindingID: 0,
			Name:      "Test message · channel configured correctly",
			VulnClass: "Connectivity test",
			Severity:  "low",
			Summary:   "This is a test message from the Uroboros notification channel; receiving it means the channel is configured and working.",
			Assets:    []string{"uroboros.example.com"},
			DetailURL: baseURL,
		}},
		HomeURL: baseURL,
	}
}

// notifierBaseURL reads the external back-link address.
func (s *Server) notifierBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return trimTrailingSlash(v)
}

func (s *Server) notifyListDeliveries(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.deliveries()
	}
}

func (s *Server) notifyRetryDelivery(w http.ResponseWriter, r *http.Request) {
	if request := newNotificationAdministration(s, w, r); request != nil {
		request.retryDelivery()
	}
}

// notifyChannelLookupErr maps "channel not found" to 404, and other errors to 500.
func notifyChannelLookupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotificationChannelNotFound) {
		writeErr(w, 404, "notification channel not found")
		return
	}
	writeErr(w, 500, err.Error())
}
