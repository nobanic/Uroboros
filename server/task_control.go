package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

const maxBatchControlIDs = 100

type taskControlResult struct {
	ID     string `json:"id"`
	Paused bool   `json:"paused"`
	Queued bool   `json:"queued"`
	Status string `json:"status"`
}

type intentControlResult struct {
	ID      int64             `json:"id"`
	State   string            `json:"state"`
	Deleted *db.IntentCleanup `json:"deleted,omitempty"`
}

// parsedTaskID carries one requested batch id together with whether it parsed.
// Invalid ids are kept rather than dropped so the response can name them.
type parsedTaskID struct {
	id    string
	valid bool
}

// normalizeBatchTaskIDs trims, canonicalizes and de-duplicates the ids of one
// batch request while preserving the caller's order. Shared by every batch
// endpoint so they agree on what counts as a duplicate.
func normalizeBatchTaskIDs(raw []string) []parsedTaskID {
	seen := map[string]bool{}
	seenInvalid := map[string]bool{}
	taskIDs := make([]parsedTaskID, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		id, valid := canonicalTaskID(trimmed)
		if !valid {
			if seenInvalid[trimmed] {
				continue
			}
			seenInvalid[trimmed] = true
			taskIDs = append(taskIDs, parsedTaskID{id: trimmed})
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		taskIDs = append(taskIDs, parsedTaskID{id: id, valid: true})
	}
	return taskIDs
}

type batchControlItem struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Status string `json:"status,omitempty"`
	Queued bool   `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) resumeAdmissionMode(t *Task) string {
	if t == nil {
		return "resume"
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.QueueMode == "bootstrap" {
		return "bootstrap"
	}
	if lifecycle.FirstRunAt == 0 {
		goals, err := t.Store.ListByKind(db.KindGoal, 1)
		if err == nil && len(goals) == 0 {
			return "bootstrap"
		}
	}
	return "resume"
}

// applyTaskControl is shared by the single and batch endpoints. Task resume is
// deliberately limited to paused tasks; reruns and finding follow-ups use
// admitTask directly when they need to revive a terminal task.
func (s *Server) applyTaskControl(t *Task, action string) (taskControlResult, error) {
	return s.applyTaskControlWithCause(t, action, agent.AbortPausedByUser)
}

func (s *Server) applyTaskControlWithCause(t *Task, action string, pauseCause error) (taskControlResult, error) {
	if t == nil {
		return taskControlResult{}, fmt.Errorf("task not found")
	}
	out := taskControlResult{ID: t.ID}
	switch action {
	case "pause":
		s.concMu.Lock()
		defer s.concMu.Unlock()
		current, exists := s.m.Task(t.ID)
		if !exists || current != t || s.engine.IsDeleting(t.ID) {
			return out, fmt.Errorf("task is being deleted and cannot be controlled")
		}
		if !s.engine.beginTaskOperation(t.ID) {
			return out, fmt.Errorf("task is being deleted and cannot be controlled")
		}
		defer s.engine.decInflight(t.ID)
		lifecycle := t.lifecycleSnapshot()
		if isTerminalStatus(lifecycle.Status) {
			return out, fmt.Errorf("a task in a terminal state cannot be paused")
		}
		if lifecycle.Paused {
			return out, fmt.Errorf("task is already paused")
		}
		wasQueued := lifecycle.Queued
		wasEnginePaused := s.engine.IsPaused(t.ID)
		if pauseCause == nil {
			pauseCause = agent.AbortPausedByUser
		}
		s.engine.Pause(t.ID, pauseCause)
		if err := s.m.ApplyTaskPause(t.ID); err != nil {
			if !wasEnginePaused && !wasQueued {
				s.engine.Resume(t)
			}
			return out, err
		}
		// Main Agent is independently cancellable. Only cancel its current turn
		// after the persistent pause commits, so a failed control request is fully
		// compensated and does not lose an otherwise valid conversation turn.
		s.cancelTaskChat(t.ID, agent.AbortChatPausedWithTask)
		if runner := s.m.Katana(); runner != nil {
			taskID, _ := strconv.ParseInt(t.ID, 10, 64)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := runner.CancelTask(ctx, taskID, errors.New("task paused"))
			cancel()
			if err != nil {
				log.Printf("[katana] task %s pause cancellation incomplete: %v", t.ID, err)
			}
		}
		if runner := s.m.Arsenal(); runner != nil {
			taskID, _ := strconv.ParseInt(t.ID, 10, 64)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := runner.CancelTask(ctx, taskID, errors.New("task paused"))
			cancel()
			if err != nil {
				log.Printf("[arsenal] task %s pause cancellation incomplete: %v", t.ID, err)
			}
		}
		out.Paused, out.Status = true, "paused"
		go s.reconcileConcurrency()
	case "resume":
		queued, err := s.admitPausedTask(t)
		if err != nil {
			return out, err
		}
		out.Queued = queued
		out.Status = map[bool]string{true: "queued", false: "running"}[queued]
	default:
		return out, fmt.Errorf("action must be pause|resume")
	}
	log.Printf("[task] #%s %s", t.ID, map[string]string{"pause": "paused", "resume": "resumed"}[action])
	return out, nil
}

// intentSummaryOf pulls the summary out of the intent payload so the deletion notification can keep a record before the intent node disappears (hard delete).
func intentSummaryOf(n *db.Node) string {
	if n == nil {
		return ""
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok {
			return s
		}
	}
	return ""
}

func (s *Server) applyIntentControl(ctx context.Context, t *Task, iid int64, action, reason, mode string) (intentControlResult, error) {
	out := intentControlResult{ID: iid}
	node, err := t.Store.GetNode(iid)
	if err != nil {
		return out, err
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			return out, fmt.Errorf("inherited intent is read-only and cannot be controlled")
		}
		return out, fmt.Errorf("intent not found")
	}
	if node.Kind != db.KindIntent {
		return out, fmt.Errorf("node is not an intent")
	}
	switch action {
	case "pause":
		if node.State != "running" {
			return out, fmt.Errorf("only a running intent can be paused")
		}
		if err := s.engine.ControlWork(ctx, iid, "pause"); err != nil {
			return out, err
		}
		out.State = "paused"
	case "resume":
		if node.State != "paused" {
			return out, fmt.Errorf("only a paused intent can be resumed")
		}
		changed, err := t.Store.CompareAndSetIntentState(iid, "paused", "open")
		if err != nil {
			return out, err
		}
		if !changed {
			return out, fmt.Errorf("%w: intent is no longer in the paused state", db.ErrIntentStateConflict)
		}
		t.Notify()
		out.State = "open"
	case "cancel":
		// Deletion supports two modes:
		//   soft (default, soft delete): the intent is parked at state='deleted' and the reason is recorded in the delete_reason field,
		//     keeping the intent node and all of its outputs/lineage, without attaching an extra fact to the graph.
		//   hard (true delete): physically delete this intent and the exclusive descendant nodes "supported only by it" (cascading to the leaves), to avoid leaving
		//     orphaned data; shared nodes, the goal, and the task root fact are kept.
		// Both modes fire a cancelled trigger to inform the planner (intent content + deletion reason) so it can re-plan accordingly.
		if node.State != "running" && node.State != "paused" && node.State != "open" {
			return out, fmt.Errorf("only a pending / running / paused intent can be deleted")
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return out, fmt.Errorf("please provide a deletion reason")
		}
		if node.State == "running" {
			if err := s.engine.ControlWork(ctx, iid, "cancel"); err != nil {
				return out, err
			}
		}
		summary := intentSummaryOf(node)
		if mode == "hard" {
			cleanup, err := t.Store.CancelIntent(iid)
			if err != nil {
				return out, err
			}
			s.cancelWorkerSide(t.ID, t.ExpID, iid)
			t.NotifyCancelled(iid, summary, reason)
			out.Deleted = &cleanup
			out.State = "" // node already deleted; the frontend removes it from the list based on Deleted
		} else {
			if _, err := t.Store.SoftDeleteIntent(iid, reason); err != nil {
				return out, err
			}
			s.cancelWorkerSide(t.ID, t.ExpID, iid)
			t.NotifyCancelled(iid, summary, reason)
			out.State = db.StateIntentDeleted
		}
	default:
		return out, fmt.Errorf("action must be pause|resume|cancel")
	}
	return out, nil
}

func (s *Server) controlTasksBatch(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).batch()
}
