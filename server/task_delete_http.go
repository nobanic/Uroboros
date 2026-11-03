package server

import (
	"context"
	"errors"
	"github.com/nobanic/UroborOS/agent"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

type taskDeleteHTTP struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}
type taskDeleteControl struct {
	server *Server
	taskID string
}

func (request taskDeleteHTTP) run() {
	id, ok := canonicalTaskID(request.request.PathValue("id"))
	if !ok {
		writeErr(request.writer, http.StatusBadRequest, "Invalid task id")
		return
	}
	var opts DeleteTaskOptions
	if err := decode(request.request, &opts); err != nil && err != io.EOF {
		writeErr(request.writer, 400, "invalid JSON: "+err.Error())
		return
	}
	if !request.server.beginTaskDelete(id) {
		writeErr(request.writer, http.StatusConflict, "Task is being deleted")
		return
	}
	deleted := false
	defer func() {
		if !deleted {
			request.server.abortTaskDelete(id)
		}
	}()

	drainCtx, cancelDrain := context.WithTimeout(request.request.Context(), taskDeleteDrainTimeout)
	defer cancelDrain()
	// Main-agent runs use a separate context from planner/workers. Cancel it, then
	// wait until both execution domains have returned before removing transcripts.
	request.server.cancelTaskChat(id, agent.AbortTaskDeleted)
	if err := request.server.waitTaskQuiescent(drainCtx, id); err != nil {
		writeErr(request.writer, http.StatusConflict, "The task still has a running Agent; deletion canceled")
		return
	}
	// Join Katana after agents are quiescent: an already-running tool call cannot
	// race the first cancellation and create a fresh child immediately afterward.
	if runner := request.server.m.Katana(); runner != nil {
		taskID, _ := strconv.ParseInt(id, 10, 64)
		if err := runner.CancelTask(drainCtx, taskID, errors.New("task deleted")); err != nil {
			writeErr(request.writer, http.StatusConflict, "Katana discovery is still stopping; deletion canceled")
			return
		}
	}
	if runner := request.server.m.Arsenal(); runner != nil {
		taskID, _ := strconv.ParseInt(id, 10, 64)
		if err := runner.CancelTask(drainCtx, taskID, errors.New("task deleted")); err != nil {
			writeErr(request.writer, http.StatusConflict, "Arsenal scan is still stopping; deletion canceled")
			return
		}
	}

	if err := request.server.drainTaskSideQuestions(drainCtx, id); err != nil {
		writeErr(request.writer, http.StatusConflict, err.Error())
		return
	}
	result, err := request.server.m.DeleteTask(id, opts)
	if err != nil {
		var committed *taskDeleteCommittedError
		if errors.As(err, &committed) {
			// PostgreSQL is already gone. Complete runtime teardown and return the
			// auditable counts together with the post-commit cleanup warning.
			request.release(id)
			deleted = true
			writeCommittedTaskDelete(request.writer, result, err)
			return
		}
		writeErr(request.writer, 500, err.Error())
		return
	}
	// Manager has removed the task from the registry, so no new API operation can
	// resolve it. Now stop and join every task-owned Engine goroutine and clear its
	// lifecycle maps before releasing the delete barrier.
	request.release(id)
	deleted = true
	writeJSON(request.writer, 200, result)
}
func (request taskDeleteHTTP) release(id string) {
	request.server.engine.StopTask(id)
	request.server.taskAgentMu.Lock()
	delete(request.server.taskAgents, id)
	request.server.taskAgentMu.Unlock()
}

func (control taskDeleteControl) abort() {
	control.server.concMu.Lock()
	defer control.server.concMu.Unlock()
	keepPaused := true
	if id, err := strconv.ParseInt(control.taskID, 10, 64); err == nil && control.server.m != nil && control.server.m.pg != nil {
		if persisted, getErr := control.server.m.pg.GetTask(id); getErr == nil && persisted != nil {
			keepPaused = persisted.Paused || persisted.Queued
		} else if task, ok := control.server.m.Task(control.taskID); ok {
			state := task.lifecycleSnapshot()
			keepPaused = state.Paused || state.Queued
			if getErr != nil {
				log.Printf("[task-delete] task %s failed to read persistent state; recovering the barrier from in-memory state: %v", control.taskID, getErr)
			}
		} else if getErr == nil {
			// The request targeted a task that does not exist. Do not retain a
			// synthetic pause entry after releasing its temporary delete barrier.
			keepPaused = false
		}
	}
	control.server.engine.AbortDelete(control.taskID, keepPaused)
}

func (control taskDeleteControl) await(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		control.server.chatMu.Lock()
		chatBusy := control.server.chatBusy[control.taskID]
		control.server.chatMu.Unlock()
		if control.server.engine.inflightCount(control.taskID) == 0 && !chatBusy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
