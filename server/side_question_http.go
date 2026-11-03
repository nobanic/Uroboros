package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/sidequestion"
)

type sideQuestionHTTP struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}
type sideQuestionDocument struct {
	status int
	value  any
}

func (document sideQuestionDocument) write(writer http.ResponseWriter) {
	if document.status != 0 {
		writeJSON(writer, document.status, document.value)
	}
}
func sideQuestionFailure(status int, message string) sideQuestionDocument {
	return sideQuestionDocument{status: status, value: map[string]any{"error": message}}
}

type sideQuestionInput = struct {
	Question string `json:"question"`
	ClientID string `json:"client_request_id"`
}

func (endpoint sideQuestionHTTP) parent(kind string) (sidequestion.Parent, bool) {
	p := sidequestion.Parent{}
	if endpoint.server.side == nil || endpoint.server.m.pg == nil {
		writeErr(endpoint.writer, 503, "side-channel service unavailable")
		return p, false
	}
	id, err := strconv.ParseInt(endpoint.request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(endpoint.writer, 400, "bad id")
		return p, false
	}
	if kind == "conversation" {
		c, err := endpoint.server.m.pg.GetConversation(id)
		if err != nil {
			writeErr(endpoint.writer, 500, err.Error())
			return p, false
		}
		if c == nil {
			writeErr(endpoint.writer, 404, "conversation not found")
			return p, false
		}
		p.ConversationID = id
		return p, true
	}
	t, ok := endpoint.server.m.Task(endpoint.request.PathValue("id"))
	if !ok {
		writeErr(endpoint.writer, 404, "task not found")
		return p, false
	}
	pt, err := endpoint.server.m.pg.GetTask(id)
	if err != nil {
		writeErr(endpoint.writer, 500, err.Error())
		return p, false
	}
	if pt == nil || endpoint.server.engine.IsDeleting(t.ID) {
		writeErr(endpoint.writer, 409, "Task is archived or being deleted")
		return p, false
	}
	p.TaskID, p.ExplorationID = id, t.ExpID
	if kind == "worker" {
		iid, err := strconv.ParseInt(endpoint.request.PathValue("iid"), 10, 64)
		if err != nil || iid <= 0 {
			writeErr(endpoint.writer, 400, "bad intent id")
			return p, false
		}
		n, err := t.Store.GetNode(iid)
		if err != nil {
			writeErr(endpoint.writer, 500, err.Error())
			return p, false
		}
		if n == nil || n.Kind != db.KindIntent {
			writeErr(endpoint.writer, 404, "intent not found")
			return p, false
		}
		if n.State == "stopped" {
			writeErr(endpoint.writer, 409, "Worker has been deleted")
			return p, false
		}
		p.IntentID = iid
	}
	return p, true
}

// Command locks remain held through response encoding. Snapshot acquisition and
// input validation still precede admission; model work never holds this lock.
func (endpoint sideQuestionHTTP) questions(parent sidequestion.Parent) {
	key := parent.Key()
	if endpoint.request.Method == "DELETE" {
		endpoint.server.side.commands.Lock()
		defer endpoint.server.side.commands.Unlock()
		endpoint.clearLocked(key).write(endpoint.writer)
		return
	}
	snapshot, err := endpoint.server.sideSnapshot(endpoint.request.Context(), parent)
	if err != nil {
		sideQuestionFailure(500, err.Error()).write(endpoint.writer)
		return
	}
	if endpoint.request.Method == "GET" {
		endpoint.history(key, snapshot).write(endpoint.writer)
		return
	}
	endpoint.request.Body = http.MaxBytesReader(endpoint.writer, endpoint.request.Body, 32<<10)
	var input sideQuestionInput
	if json.NewDecoder(endpoint.request.Body).Decode(&input) != nil {
		sideQuestionFailure(400, "bad json").write(endpoint.writer)
		return
	}
	input.Question = strings.TrimSpace(input.Question)
	if input.Question == "" || len([]rune(input.Question)) > 4000 || !validWorkerMessageRequestID(input.ClientID) {
		sideQuestionFailure(400, "The question must be 1-4000 characters and provide a valid request ID").write(endpoint.writer)
		return
	}
	endpoint.server.side.commands.Lock()
	defer endpoint.server.side.commands.Unlock()
	endpoint.admitLocked(parent, key, snapshot, input).write(endpoint.writer)
}

func (endpoint sideQuestionHTTP) clearLocked(key string) sideQuestionDocument {
	endpoint.server.side.mu.Lock()
	for _, run := range endpoint.server.side.runs {
		if run.key == key {
			run.cancel()
		}
	}
	endpoint.server.side.mu.Unlock()
	if err := endpoint.server.m.pg.ClearSideHistory(endpoint.request.Context(), key); err != nil {
		return sideQuestionFailure(500, err.Error())
	}
	return sideQuestionDocument{status: 200, value: map[string]bool{"cleared": true}}
}

func (endpoint sideQuestionHTTP) history(key string, snapshot *sidequestion.Snapshot) sideQuestionDocument {
	before, _ := strconv.ParseInt(endpoint.request.URL.Query().Get("before"), 10, 64)
	items, err := endpoint.server.m.pg.SideHistory(endpoint.request.Context(), key, before, 21)
	if err != nil {
		return sideQuestionFailure(500, err.Error())
	}
	var next int64
	if len(items) > 20 {
		items = items[:20]
		next = items[len(items)-1].Ordinal
	}
	current, err := endpoint.server.m.pg.CurrentSideRequest(endpoint.request.Context(), key)
	if err != nil {
		return sideQuestionFailure(500, err.Error())
	}
	var meta any
	if snapshot != nil {
		_, modelErr := endpoint.server.sideProvider(snapshot.Model)
		reason := ""
		if modelErr != nil {
			reason = modelErr.Error()
		}
		meta = map[string]any{"captured_at": snapshot.CapturedAt, "model": snapshot.Model, "available": modelErr == nil, "reason": reason}
	}
	return sideQuestionDocument{status: 200, value: map[string]any{"items": items, "current": current, "next_cursor": next, "snapshot": meta}}
}

func (endpoint sideQuestionHTTP) admitLocked(parent sidequestion.Parent, key string, snapshot *sidequestion.Snapshot, input sideQuestionInput) sideQuestionDocument {
	if parent.TaskID > 0 && endpoint.server.engine.IsDeleting(strconv.FormatInt(parent.TaskID, 10)) {
		return sideQuestionFailure(409, "Task is being archived or deleted")
	}
	if existing, err := endpoint.server.m.pg.ExistingSideRequest(endpoint.request.Context(), key, input.ClientID); err != nil {
		return sideQuestionFailure(500, err.Error())
	} else if existing != nil {
		if existing.Question != input.Question {
			return sideQuestionFailure(409, "The same request ID cannot be used for different questions")
		}
		return sideQuestionDocument{status: 200, value: existing}
	}
	if snapshot == nil {
		return sideQuestionFailure(409, "no context snapshot yet; please run the Main Agent")
	}
	provider, err := endpoint.server.sideProvider(snapshot.Model)
	if err != nil {
		return sideQuestionFailure(409, err.Error())
	}
	endpoint.server.side.mu.Lock()
	full := len(endpoint.server.side.runs) >= 4
	busy := false
	for _, run := range endpoint.server.side.runs {
		if run.key == key {
			busy = true
		}
	}
	endpoint.server.side.mu.Unlock()
	if busy {
		return sideQuestionFailure(409, db.ErrSideBusy.Error())
	}
	if full {
		return sideQuestionFailure(429, "side-channel requests have hit the concurrency cap; please retry later")
	}
	question, valid := endpoint.server.prepareChatMentionMessage(endpoint.writer, input.Question)
	if !valid {
		return sideQuestionDocument{}
	}
	if err = endpoint.server.m.pg.SaveSideSnapshot(endpoint.request.Context(), *snapshot); err != nil {
		return sideQuestionFailure(500, err.Error())
	}
	exchange, created, err := endpoint.server.m.pg.StartSideRequest(endpoint.request.Context(), *snapshot, input.ClientID, input.Question)
	if err != nil {
		return sideQuestionFailure(409, err.Error())
	}
	if created {
		ctx, cancel := context.WithTimeout(endpoint.server.ctx, 120*time.Second)
		endpoint.server.side.mu.Lock()
		endpoint.server.side.runs[exchange.ID] = sideRun{key: key, parent: parent, cancel: cancel, done: make(chan struct{})}
		endpoint.server.side.mu.Unlock()
		go endpoint.server.runSide(ctx, cancel, *exchange, parent, provider, *snapshot, question)
	}
	return sideQuestionDocument{status: 202, value: exchange}
}

func (endpoint sideQuestionHTTP) cancel() sideQuestionDocument {
	exchange, err := endpoint.server.m.pg.SideRequest(endpoint.request.Context(), endpoint.request.PathValue("requestID"))
	if err != nil {
		return sideQuestionFailure(500, err.Error())
	}
	if exchange == nil {
		return sideQuestionFailure(404, "side question not found")
	}
	endpoint.server.side.mu.Lock()
	if run, exists := endpoint.server.side.runs[exchange.ID]; exists {
		run.cancel()
	}
	endpoint.server.side.mu.Unlock()
	return sideQuestionDocument{status: 200, value: map[string]bool{"cancelled": true}}
}
