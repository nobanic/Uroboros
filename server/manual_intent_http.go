package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Manual commands share existing task response/admission ports. Lifecycle and
// provider authority remain in the original store, scheduler and engine.
func (c taskCommand) bindBounded(value any, limit int64) bool {
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, limit)
	if err := json.NewDecoder(c.r.Body).Decode(value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.failure(http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			c.failure(http.StatusBadRequest, "bad json: "+err.Error())
		}
		return false
	}
	return true
}
func (c taskCommand) manualFinding() {
	s, r := c.server, c.r
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		c.failure(400, "bad finding id")
		return
	}
	var req struct {
		Description string `json:"description"`
	}
	if !c.bindBounded(&req, 32<<10) {
		return
	}
	description := strings.TrimSpace(req.Description)
	switch {
	case description == "":
		c.failure(400, "description is required")
		return
	case utf8.RuneCountInString(description) > maxFindingFollowUpRunes:
		c.failure(400, fmt.Sprintf("description must be at most %d characters", maxFindingFollowUpRunes))
		return
	}

	finding, err := s.m.pg.GetFinding(id)
	if err != nil {
		c.failure(500, err.Error())
		return
	}
	if finding == nil {
		c.failure(404, "finding not found")
		return
	}
	if finding.TaskID == nil || finding.NodeID == nil {
		c.failure(409, "finding origin task or node is no longer available")
		return
	}
	t, ok := s.m.Task(i64s(*finding.TaskID))
	if !ok || t == nil {
		c.failure(409, "finding origin task is no longer available")
		return
	}
	release := c.reserve(t, "task is being deleted")
	if release == nil {
		return
	}
	defer release()

	audit := db.Activity{
		Worker:  "system",
		Kind:    "text",
		Summary: "manually submitted a deep-exploitation intent for the finding",
		Detail:  description,
	}
	intentID, audit, err := t.Store.AddFindingFollowUpIntent(id, *finding.NodeID, description, audit)
	if errors.Is(err, db.ErrFindingOriginUnavailable) {
		c.failure(409, "finding origin task or node is no longer available")
		return
	}
	if err != nil {
		c.failure(500, err.Error())
		return
	}
	queued, err := s.admitTask(t, "resume")
	if err != nil {
		if rollbackErr := t.Store.DiscardOpenIntent(intentID); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("discard follow-up intent %d: %w", intentID, rollbackErr))
		}
		c.failure(500, err.Error())
		return
	}
	// The audit row committed atomically with the intent. Publish that exact row;
	// emitActivity would insert a second copy and break DB/SSE correspondence.
	s.engine.Broadcaster().Publish(t.ID, audit)
	s.engine.touch(t.ID)
	// Admission can wake a Worker immediately. Report the persisted intent state
	// observed at response time instead of always claiming it is still open. The
	// task-level queued flag remains separate because a queued task intentionally
	// keeps its Worker intent open until it receives a concurrency slot.
	state := "open"
	if current, stateErr := t.Store.GetNode(intentID); stateErr == nil && current != nil {
		state = current.State
	}
	c.reply(200, map[string]any{
		"task_id":   t.ID,
		"intent_id": i64s(intentID),
		"state":     state,
		"queued":    queued,
	})
}
func (c taskCommand) workerMessage() {
	s, r := c.server, c.r
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		c.failure(http.StatusNotFound, "task not found")
		return
	}
	iid, valid := c.intentID(true)
	if !valid {
		return
	}
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if !c.bindBounded(&req, maxWorkerMessageBytes) {
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		c.failure(http.StatusBadRequest, "message must not be empty")
		return
	}
	if len([]rune(message)) > 4000 {
		c.failure(http.StatusBadRequest, "message must not exceed 4000 characters")
		return
	}
	if !validWorkerMessageRequestID(requestID) {
		c.failure(http.StatusBadRequest, "request_id must be 1-128 of letters, digits, -, _, . or :")
		return
	}

	// Reject non-runnable task lifecycles up front so the caller gets a clear reason
	// instead of a silent no-op. The intent itself must be paused: the UI flow is
	// interrupt (pause) first, then send.
	if s.engine.IsDeleting(t.ID) {
		c.failure(http.StatusConflict, "task is being deleted; cannot send a message to the Worker")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		c.failure(http.StatusConflict, "task is paused; resume the task before sending a message to the Worker")
		return
	case lifecycle.Queued:
		c.failure(http.StatusConflict, "a queued task cannot send a message to the Worker")
		return
	case isTerminalStatus(lifecycle.Status):
		c.failure(http.StatusConflict, "a terminal task cannot send a message to the Worker")
		return
	case s.engine.isSettling(t.ID):
		c.failure(http.StatusConflict, "task is wrapping up; cannot send a message to the Worker")
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		c.failure(http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			c.failure(http.StatusConflict, "an inherited intent is read-only; cannot send a Worker message")
			return
		}
		c.failure(http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		c.failure(http.StatusConflict, "node is not an intent")
		return
	}
	if node.State != "paused" {
		c.failure(http.StatusConflict, "only a paused Worker can receive a message; pause it first")
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(c.w, message)
	if !ok {
		return
	}

	// runDetachedIntent transitions paused->running, emits the user turn and starts a
	// dedicated run. Root the run at s.ctx so a disconnected browser cannot strand it
	// while task pause/delete/shutdown still stop it.
	if err := s.engine.runDetachedIntent(s.ctx, t, iid, requestID, message, agentMessage); err != nil {
		switch {
		case errors.Is(err, db.ErrIntentStateConflict):
			c.failure(http.StatusConflict, err.Error())
		default:
			c.failure(http.StatusInternalServerError, err.Error())
		}
		return
	}

	c.reply(http.StatusOK, map[string]any{
		"id":         iid,
		"state":      "running",
		"accepted":   true,
		"request_id": requestID,
	})
}
