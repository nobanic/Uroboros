package server

import (
	"time"

	"github.com/nobanic/UroborOS/agent"
)

// workerDispatch evaluates admission gates in their original priority. It does
// not acquire admission, sleep, claim an intent or take a second agent snapshot.
type workerDispatch struct {
	engine *Engine
	task   *Task
}

func (poll workerDispatch) next() (*agent.Worker, time.Duration, bool) {
	_, worker := poll.engine.snapshotFor(poll.task)
	switch {
	case worker == nil:
		return worker, 1500 * time.Millisecond, false
	case poll.engine.IsPaused(poll.task.ID):
		return worker, 1000 * time.Millisecond, false
	case poll.engine.IsDeleting(poll.task.ID):
		return worker, 0, true
	case poll.engine.isSettling(poll.task.ID):
		return worker, 1000 * time.Millisecond, false
	case isTerminalStatus(poll.engine.m.TaskStatus(poll.task.ID)):
		return worker, 1000 * time.Millisecond, false
	case poll.engine.discoveryReviewRequired(poll.task.ID):
		return worker, 1000 * time.Millisecond, false
	}
	return worker, 0, false
}
