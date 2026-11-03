package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type updateTestTransport func(*http.Request) (*http.Response, error)

func (transport updateTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestManagedOperatorApplyForwardsOnlyTheSelectedCommit(t *testing.T) {
	s := managedFixture(t)
	s.jwtKey = []byte(strings.Repeat("k", 32))
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	calls := 0
	target := strings.Repeat("b", 40)
	http.DefaultTransport = updateTestTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "http://updater:8790/apply" || request.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Fatal("unexpected update boundary", request.URL)
		}
		var body struct {
			Target string `json:"target"`
		}
		if json.NewDecoder(request.Body).Decode(&body) != nil || body.Target != target {
			t.Fatal("selected commit was not forwarded")
		}
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(`{"ok":true,"target":"` + target + `"}`)), Header: make(http.Header)}, nil
	})
	request := httptest.NewRequest("POST", "/api/update/apply", strings.NewReader(`{"target":"`+target+`"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	writer := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.updateApply)).ServeHTTP(writer, request)
	if writer.Code != 202 || calls != 1 {
		t.Fatalf("code=%d calls=%d body=%s", writer.Code, calls, writer.Body.String())
	}
	request = httptest.NewRequest("POST", "/api/update/apply", strings.NewReader(`{"target":"`+target+`"}`))
	request.Header.Set("X-Uroboros-Updater", strings.Repeat("a", 64))
	writer = httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.updateApply)).ServeHTTP(writer, request)
	if writer.Code != 401 || calls != 1 {
		t.Fatal("controller credential bypassed operator authentication")
	}
}

func managedFixture(t *testing.T) *Server {
	t.Helper()
	t.Setenv("UROBOROS_UPDATER_URL", "http://updater:8790")
	t.Setenv("UROBOROS_UPDATE_TOKEN", strings.Repeat("a", 64))
	return &Server{ctx: context.Background(), m: &Manager{tasks: map[string]*Task{}}, chatBusy: map[string]bool{}, triggerQ: map[string][]triggeredRun{}, triggerActive: map[string]int{}}
}

func TestManagedMaintenanceRequiresItsOwnCredential(t *testing.T) {
	s := managedFixture(t)
	for _, token := range []string{"", "wrong", strings.Repeat("a", 64)} {
		r := httptest.NewRequest("POST", "/api/update/maintenance", strings.NewReader(`{"prepare":true}`))
		r.Header.Set("X-Uroboros-Updater", token)
		w := httptest.NewRecorder()
		s.updateMaintenance(w, r)
		want := 401
		if len(token) == 64 {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("got %d want %d", w.Code, want)
		}
	}
	if !s.updateGate.blocked.Load() {
		t.Fatal("maintenance was not entered")
	}
}

func TestManagedUpdateRefusesActiveTaskAndAgentConversation(t *testing.T) {
	s := managedFixture(t)
	s.m.tasks["1"] = &Task{ID: "1", Status: "running"}
	if reason := s.updateBusyReason(); !strings.Contains(reason, "Pause") {
		t.Fatal(reason)
	}
	s.m.tasks["1"].Paused = true
	s.chatBusy["conv-1"] = true
	if reason := s.updateBusyReason(); !strings.Contains(reason, "conversations") {
		t.Fatal(reason)
	}
	delete(s.chatBusy, "conv-1")
	if reason := s.updateBusyReason(); reason != "" {
		t.Fatal(reason)
	}
}

func TestManagedMaintenanceBlocksWritesAndKeepsStreamingReads(t *testing.T) {
	s := managedFixture(t)
	s.updateGate.blocked.Store(true)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("streaming response writer lost")
		}
		_, _ = io.WriteString(w, "admitted")
	})
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		w := httptest.NewRecorder()
		s.updateAdmission(next).ServeHTTP(w, httptest.NewRequest(method, "/api/tasks", nil))
		want := 503
		if method == "GET" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
	request := httptest.NewRequest("POST", "/api/update/maintenance", strings.NewReader(`{"prepare":false}`))
	request.Header.Set("X-Uroboros-Updater", strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	s.updateMaintenance(w, request)
	if w.Code != 200 || s.updateGate.blocked.Load() {
		t.Fatal("failed to leave maintenance")
	}
}

func TestManagedTriggerQueueDoesNotStartDuringBackup(t *testing.T) {
	s := managedFixture(t)
	s.updateGate.blocked.Store(true)
	s.triggerQ["agent"] = []triggeredRun{{agentKey: "agent"}}
	(agentTriggerQueue{server: s, key: "agent"}).refill()
	if len(s.triggerQ["agent"]) != 1 || s.triggerActive["agent"] != 0 {
		t.Fatal("trigger escaped maintenance")
	}
}
