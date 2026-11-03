package server

import (
	"fmt"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

// taskAdmission owns one caller's mode and pause precondition. run retains the
// scheduler lock and inflight reservation while decide commits or parks work.
type taskAdmission struct {
	server        *Server
	t             *Task
	mode          string
	requirePaused bool
}

func (command taskAdmission) decide(lifecycle taskLifecycleState) (queued bool, err error) {
	wasTerminal := isTerminalStatus(lifecycle.Status)
	wasPaused := lifecycle.Paused || command.server.engine.IsPaused(command.t.ID)
	wasQueued := lifecycle.Queued
	engineWasPaused := command.server.engine.IsPaused(command.t.ID)
	enabled, limit := command.server.m.ConcurrencyLimit()
	ready := command.server.engine.ReadyFor(command.t)

	// Work added to a task that is already admitted only needs a wake-up. This
	// matters when the configured limit was lowered below the current running
	// count: an existing task must not suddenly mark itself queued while its
	// planner/workers are still live. Still compare-and-commit the persisted
	// status: a concurrent terminal transition must win instead of being silently
	// reported as a successful follow-up admission.
	if !wasTerminal && !wasPaused && !wasQueued && command.server.engine.Started(command.t.ID) && (!enabled || ready) {
		if err := command.server.m.ApplyTaskAdmission(command.t.ID, lifecycle.Status, lifecycle.Status, false, "resume", false); err != nil {
			return false, err
		}
		command.server.startAdmittedTask(command.t, "resume")
		return false, nil
	}

	// Preserve the original first-run mode across repeated admissions. Legacy
	// queued rows have an empty queue_mode, so infer bootstrap from their graph.
	if wasQueued {
		switch lifecycle.QueueMode {
		case "bootstrap":
			command.mode = "bootstrap"
		case "":
			command.mode = command.server.resumeAdmissionMode(command.t)
		}
	}

	readyBacklog := enabled && command.server.hasReadyQueuedTask(command.t.ID)
	atCapacity := enabled && command.server.runningTaskCount(command.t.ID) >= limit
	shouldQueue := enabled && (!ready || readyBacklog || atCapacity)

	// Install the execution barrier before reviving a terminal/paused task. Without
	// this ordering, its already-running worker loops can claim the newly-opened
	// intent in the gap between status=running and queued=true.
	if shouldQueue || wasTerminal || wasPaused || wasQueued {
		command.server.engine.Pause(command.t.ID, agent.Causef("queued_for_admission", "task awaiting run admission",
			"the task is waiting in the concurrency queue or for admission-state commit; this run has stopped; it will only re-claim intents once it obtains a run slot"))
	}

	status := lifecycle.Status
	if wasTerminal {
		status = "running"
	}
	if err := command.server.m.ApplyTaskAdmission(command.t.ID, lifecycle.Status, status, shouldQueue, command.mode, wasQueued); err != nil {
		if !engineWasPaused && !wasPaused && !wasQueued {
			command.server.engine.Resume(command.t)
		}
		return false, err
	}
	if lifecycle.Status == "timeout" && status == "running" {
		// ApplyTaskAdmission reset this timed-out run's persisted clock. Clear the
		// matching Engine gates before either parking it in FIFO or starting work;
		// otherwise the old settling flag would make every worker skip forever.
		command.server.engine.resetTimeoutRevival(command.t.ID)
	}
	if shouldQueue {
		if !wasQueued {
			summary := fmt.Sprintf("Queued: reached the concurrency cap %d; will start automatically when a slot opens", limit)
			switch {
			case !ready:
				summary = "Queued: no runnable LLM configuration right now; will start automatically once a configuration is restored"
			case readyBacklog:
				summary = "Queued: an earlier task is already waiting to run; will start automatically in FIFO order"
			}
			command.server.engine.emitActivity(command.t, db.Activity{Worker: "system", Kind: "text", Summary: summary})
		}
		return true, nil
	}
	command.server.startAdmittedTask(command.t, command.mode)
	return false, nil
}
func (command taskAdmission) run() (queued bool, err error) {
	if command.t == nil {
		return false, fmt.Errorf("task not found")
	}
	if command.mode != "bootstrap" {
		command.mode = "resume"
	}
	command.server.concMu.Lock()
	defer command.server.concMu.Unlock()
	// Delete installs its barrier under concMu as well. Re-resolve after acquiring
	// the lock so a request that captured a task pointer before successful deletion
	// cannot revive that stale handle after StopTask clears its Engine maps.
	current, exists := command.server.m.Task(command.t.ID)
	if !exists || current != command.t || command.server.engine.IsDeleting(command.t.ID) {
		return false, fmt.Errorf("task is being deleted")
	}
	if !command.server.engine.beginTaskOperation(command.t.ID) {
		return false, fmt.Errorf("task is being deleted")
	}
	defer command.server.engine.decInflight(command.t.ID)
	lifecycle := command.t.lifecycleSnapshot()
	if command.requirePaused {
		if isTerminalStatus(lifecycle.Status) {
			return false, fmt.Errorf("a terminal task cannot be continued")
		}
		if !lifecycle.Paused {
			return false, fmt.Errorf("only a paused task can be continued")
		}
		command.mode = command.server.resumeAdmissionMode(command.t)
	}
	return command.decide(lifecycle)
}
