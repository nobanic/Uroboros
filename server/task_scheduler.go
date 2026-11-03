package server

import "log"

// taskSchedule owns readiness/slot queries and the stable admission entry paths.
type taskSchedule struct{ server *Server }

func (command taskSchedule) slot(t *Task) bool {
	if t == nil {
		return false
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.Queued || lifecycle.Paused || isTerminalStatus(lifecycle.Status) {
		return false
	}
	// Deletion temporarily pauses the Engine but has not committed yet. Preserve
	// the task's slot until PostgreSQL deletion succeeds; otherwise FIFO promotion
	// during the drain window could over-admit if deletion later aborts and the
	// persisted running task is restored.
	if command.server.engine.IsDeleting(t.ID) {
		return true
	}
	return !command.server.engine.IsPaused(t.ID) && (command.server.engine.ReadyFor(t) || command.server.engine.ActiveLLMCalls(t.ID) > 0)
}

func (command taskSchedule) running(excludeID string) int {
	count := 0
	for _, task := range command.server.m.List() {
		if task.ID != excludeID && command.server.occupiesConcurrencySlot(task) {
			count++
		}
	}
	return count
}

func (command taskSchedule) admit(t *Task, mode string) (queued bool, err error) {
	return command.server.admitTaskWhen(t, mode, false)
}

func (command taskSchedule) resume(t *Task) (queued bool, err error) {
	return command.server.admitTaskWhen(t, "resume", true)
}

func (command taskSchedule) backlog(excludeID string) bool {
	for _, task := range command.server.m.List() {
		lifecycle := task.lifecycleSnapshot()
		if task.ID != excludeID && lifecycle.Queued && !lifecycle.Paused && !isTerminalStatus(lifecycle.Status) && command.server.engine.ReadyFor(task) {
			return true
		}
	}
	return false
}

func (command taskSchedule) revive(t *Task) {
	if t == nil {
		return
	}
	if _, err := command.server.admitTask(t, "resume"); err != nil {
		log.Printf("[revive] task %s failed to resume: %v", t.ID, err)
	}
}
