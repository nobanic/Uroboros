package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type managedUpdateGate struct {
	mu      sync.RWMutex
	blocked atomic.Bool
}

func (s *Server) updateBlocked() bool {
	if !managedUpdatesEnabled() || os.Getenv("UROBOROS_UPDATE_STATUS_FILE") != "/app/update-status/status.json" {
		return s.updateGate.blocked.Load()
	}
	file, err := os.Open("/app/update-status/status.json")
	if err != nil {
		return s.updateGate.blocked.Load() || !os.IsNotExist(err)
	}
	defer file.Close()
	var status struct {
		RecoveryRequired bool   `json:"recovery_required"`
		Running          bool   `json:"running"`
		Phase            string `json:"phase"`
	}
	if json.NewDecoder(io.LimitReader(file, 8192)).Decode(&status) != nil {
		return true
	}
	if status.RecoveryRequired {
		return true
	}
	if !status.Running && status.Phase == "failed" {
		s.updateGate.blocked.Store(false)
	}
	return s.updateGate.blocked.Load() || (status.Running && (status.Phase == "backing-up" || status.Phase == "restarting" || status.Phase == "checking" || status.Phase == "recovering"))
}

func managedUpdatesEnabled() bool { return os.Getenv("UROBOROS_UPDATER_URL") == "http://updater:8790" }

func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request, endpoint string) {
	token := os.Getenv("UROBOROS_UPDATE_TOKEN")
	if !managedUpdatesEnabled() || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		writeErr(w, 503, "The clean update controller is not configured; start this source with clean/start first.")
		return
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if err != nil {
			writeErr(w, 400, "Invalid update request")
			return
		}
		body = bytes.NewReader(raw)
	}
	address := "http://updater:8790/" + endpoint
	if endpoint == "check" {
		values := url.Values{"current": {os.Getenv("UROBOROS_SOURCE_REVISION")}}
		if r.URL.Query().Get("force") != "" {
			values.Set("force", "1")
		}
		address += "?" + values.Encode()
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, address, body)
	if err != nil {
		writeErr(w, 500, "Cannot prepare update request")
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		if endpoint == "check" {
			writeJSON(w, 200, map[string]any{"mode": "docker-source", "current": BuildVersion, "current_revision": os.Getenv("UROBOROS_SOURCE_REVISION"), "repo": "nobanic/Uroboros", "branch": "main", "has_update": false, "error": "The update controller is unavailable. Check the clean stack status."})
			return
		}
		writeErr(w, 502, "The update controller is unavailable")
		return
	}
	defer response.Body.Close()
	var result map[string]any
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil {
		writeErr(w, 502, "Invalid update controller response")
		return
	}
	if endpoint == "check" {
		result["current"], result["os"], result["arch"] = BuildVersion, "linux", "amd64"
		reason := s.updateBusyReason()
		result["can_update"] = reason == ""
		if reason != "" {
			result["blocked_reason"] = reason
		}
	}
	writeJSON(w, response.StatusCode, result)
}

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if managedUpdatesEnabled() {
		s.updateProxy(w, r, "status")
		return
	}
	status, running := updHub.snapshot()
	writeJSON(w, 200, map[string]any{"phase": status.Phase, "message": status.Message, "running": running, "error": status.Error})
}

func (s *Server) updateCredentials(w http.ResponseWriter, r *http.Request) {
	if !managedUpdatesEnabled() {
		writeErr(w, 409, "GitHub access is configured by the clean update controller.")
		return
	}
	s.updateProxy(w, r, "credentials")
}

// This one route uses the private controller credential, independently of the
// browser session. It is not an exemption for any other API or tool endpoint.
func (s *Server) updateMaintenance(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("UROBOROS_UPDATE_TOKEN")
	if r.Method != http.MethodPost {
		writeErr(w, 405, "Method not allowed")
		return
	}
	if !managedUpdatesEnabled() || len(token) != 64 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Uroboros-Updater")), []byte(token)) != 1 {
		writeErr(w, 401, "Unauthorized")
		return
	}
	var body struct {
		Prepare bool `json:"prepare"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil {
		writeErr(w, 400, "Invalid maintenance request")
		return
	}
	s.updateGate.mu.Lock()
	defer s.updateGate.mu.Unlock()
	if !body.Prepare {
		s.updateGate.blocked.Store(false)
		s.queueMu.Lock()
		for key := range s.triggerQ {
			s.pumpLocked(key)
		}
		s.queueMu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	s.updateGate.blocked.Store(true)
	if reason := s.updateBusyReason(); reason != "" {
		s.updateGate.blocked.Store(false)
		s.queueMu.Lock()
		for key := range s.triggerQ {
			s.pumpLocked(key)
		}
		s.queueMu.Unlock()
		writeErr(w, 409, reason)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) updateBusyReason() string {
	if s.m == nil {
		return "The operation registry is unavailable."
	}
	for _, task := range s.m.List() {
		state := task.lifecycleSnapshot()
		if !isTerminalStatus(state.Status) && !state.Paused {
			return "Pause active and queued operations before updating."
		}
		if s.engine != nil && s.engine.ActiveLLMCalls(task.ID) > 0 {
			return "Wait for the current model calls to finish."
		}
	}
	s.chatMu.Lock()
	chatBusy := false
	for _, busy := range s.chatBusy {
		chatBusy = chatBusy || busy
	}
	s.chatMu.Unlock()
	if chatBusy {
		return "Wait for running agent conversations to finish."
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	for _, count := range s.triggerActive {
		if count > 0 {
			return "Wait for triggered agent runs to finish."
		}
	}
	for _, pending := range s.triggerQ {
		if len(pending) > 0 {
			return "Wait for queued agent runs to finish."
		}
	}
	if s.m.pg != nil {
		var busy bool
		err := s.m.pg.QueryRow(`SELECT EXISTS(SELECT 1 FROM katana_jobs WHERE status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM arsenal_jobs WHERE status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM binary_triage_jobs WHERE status IN ('queued','running'))
 OR EXISTS(SELECT 1 FROM task_archives WHERE state IN ('archive_queued','archiving','restore_queued','restoring','delete_queued','deleting'))`).Scan(&busy)
		if err != nil {
			return "Cannot verify discovery and binary jobs; update was not started."
		}
		if busy {
			return "Wait for discovery, binary and archive jobs to finish or cancel them before updating."
		}
	}
	return ""
}

func (s *Server) updateAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !managedUpdatesEnabled() || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		s.updateGate.mu.RLock()
		defer s.updateGate.mu.RUnlock()
		if s.updateBlocked() {
			writeErr(w, http.StatusServiceUnavailable, "The installation is being updated. Retry when the console reconnects.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sourceRevision() string { return strings.TrimSpace(os.Getenv("UROBOROS_SOURCE_REVISION")) }

func managedRollbackUnavailable(w http.ResponseWriter) {
	writeErr(w, 409, "Managed updates keep private recovery backups. Native binary rollback does not apply to this Docker installation.")
}
