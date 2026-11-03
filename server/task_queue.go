package server

import (
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"log"
	"sort"
	"strconv"
)

// taskQueueSweep retains one scheduler lock and one settings snapshot across
// unavailable-provider parking, stable FIFO selection and admission commits.
type taskQueueSweep struct{ server *Server }

func (command taskQueueSweep) park() {
	for _, task := range command.server.m.List() {
		lifecycle := task.lifecycleSnapshot()
		if lifecycle.Queued || lifecycle.Paused || command.server.engine.IsPaused(task.ID) || command.server.engine.IsDeleting(task.ID) ||
			isTerminalStatus(lifecycle.Status) || command.server.engine.ReadyFor(task) || command.server.engine.ActiveLLMCalls(task.ID) > 0 {
			continue
		}
		mode := command.server.resumeAdmissionMode(task)
		command.server.engine.Pause(task.ID, agent.Causef("llm_unavailable_queued", "LLM unavailable; the task is entering the wait queue",
			"the task currently cannot resolve a runnable Planner/Worker LLM; the concurrency slot has been released; it will continue in queue order once a configuration is restored"))
		if err := command.server.m.EnqueueTask(task.ID, mode); err != nil {
			command.server.engine.Resume(task)
			log.Printf("[concurrency] task %s failed to enqueue because LLM is unavailable: %v", task.ID, err)
			continue
		}
		command.server.engine.emitActivity(task, db.Activity{Worker: "system", Kind: "text",
			Summary: "Queued: no runnable LLM configuration right now; will start automatically once a configuration is restored"})
	}
}
func (command taskQueueSweep) drain(enabled bool, limit int) {
	type queuedTask struct {
		task      *Task
		lifecycle taskLifecycleState
	}
	queued := []queuedTask{}
	for _, task := range command.server.m.List() {
		lifecycle := task.lifecycleSnapshot()
		if lifecycle.Queued && !isTerminalStatus(lifecycle.Status) {
			queued = append(queued, queuedTask{task: task, lifecycle: lifecycle})
		}
	}
	sort.SliceStable(queued, func(i, j int) bool {
		left, right := queued[i].lifecycle.QueuedAt, queued[j].lifecycle.QueuedAt
		if left == 0 {
			left = queued[i].task.CreatedAt * int64(1e9)
		}
		if right == 0 {
			right = queued[j].task.CreatedAt * int64(1e9)
		}
		if left != right {
			return left < right
		}
		// Old rows may not have queued_at and task creation timestamps are only
		// kept to second precision in memory. Task ids are monotonic, providing a
		// deterministic oldest-first fallback for those ties.
		leftID, leftErr := strconv.ParseInt(queued[i].task.ID, 10, 64)
		rightID, rightErr := strconv.ParseInt(queued[j].task.ID, 10, 64)
		if leftErr == nil && rightErr == nil {
			return leftID < rightID
		}
		return queued[i].task.ID < queued[j].task.ID
	})
	slots := len(queued)
	if enabled {
		slots = limit - command.server.runningTaskCount("")
	}
	for _, entry := range queued {
		if slots <= 0 {
			break
		}
		task := entry.task
		lifecycle := task.lifecycleSnapshot()
		// Keep FIFO order, but do not consume a concurrency slot for a task
		// whose explicit chain/global provider is unavailable. It will be retried
		// after the user configures or resets its LLM chain.
		if lifecycle.Paused || (enabled && !command.server.engine.ReadyFor(task)) {
			continue
		}
		mode := lifecycle.QueueMode
		if mode != "bootstrap" && mode != "resume" {
			mode = command.server.resumeAdmissionMode(task)
		}
		if mode != "bootstrap" {
			mode = "resume"
		}
		if !command.server.engine.beginTaskOperation(task.ID) {
			continue
		}
		if err := command.server.m.ApplyTaskAdmission(task.ID, lifecycle.Status, lifecycle.Status, false, mode, false); err != nil {
			command.server.engine.decInflight(task.ID)
			continue
		}
		command.server.startAdmittedTask(task, mode)
		command.server.engine.decInflight(task.ID)
		slots--
	}
}
func (command taskQueueSweep) run() {
	command.server.concMu.Lock()
	defer command.server.concMu.Unlock()
	enabled, limit := command.server.m.ConcurrencyLimit()

	// A task whose provider chain becomes unavailable cannot do useful work and
	// must not reserve a limited running slot forever. Park it persistently so a
	// later profile edit/recovery can re-enter through the same FIFO path.
	if enabled {
		command.park()
	}

	command.drain(enabled, limit)
}
