package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "User paused the task",
		"The user paused the task through the task control endpoint (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was cancelled deliberately; the running intent is returned to frontier(open) and, after the task resumes, re-claimed and executed from the start")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Orchestrator agent paused the task",
		"The orchestrator agent called the pause_task tool to pause this task. This Planner/Worker run was cancelled deliberately; the running intent is returned to frontier(open) and re-executed after resume")
	AbortTaskDeleted = cause("task_deleted", "Task deleted",
		"The task is being deleted (DELETE /api/tasks/{id}); the delete barrier cancelled the task's running Planner, Worker, and main Agent; this run's result will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "Backend restored the paused task state",
		"On startup the backend restored the task's paused state from the state persisted in the database. This run was cancelled; normally no Agent is running during the restore phase")
	AbortGoalMet = cause("goal_met", "Planner judged the task goal met",
		"The planner judged the task goal met and set the task to done, then cancelled the still-running Workers; these intents are marked stopped rather than failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task timeout wrap-up grace elapsed",
		"After the task reached timeout, it waited for running Workers to wrap up gracefully, but the 90-second drain grace period still was not enough, so a hard cancel was performed; the intent is marked exhausted, and facts and assets already written during wrap-up are kept")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner terminated this intent",
		"The planner called kill_work to deliberately terminate this intent, usually meaning it went off course or had no further value; the intent is marked stopped and is not automatically re-claimed")
	AbortWorkPausedByUser = cause("work_paused_by_user", "User paused this Worker intent",
		"The user paused a running Worker. This call was cancelled and the intent moved to paused; already-recorded intents, facts, vulnerabilities, and activity records are all kept, and execution restarts from the beginning after resume")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "User deleted this Worker intent",
		"The user deleted a running Worker. This call was cancelled; after the Worker exits the write section, the server handles the intent according to the delete mode the user chose — a soft delete only marks it deleted and keeps all outputs, while a hard delete cascades to remove this intent and the downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "Worker finished and released context",
		"The Worker finished normally and the engine released its context resources in detachWork. This is not a run interruption; if it appears in an interruption message, it means the cancel and the teardown event raced")
	AbortPausedRaceGuard = cause("paused_race_guard", "New run refused while task paused",
		"While the task is paused, the engine refuses to issue a new execution context, to prevent a race between claim and pause from letting a Worker keep starting; the claimed intent is returned to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "User stopped this conversation turn",
		"The user clicked stop, deliberately aborting this main Agent or conversation Agent run. Activity records already produced are kept, and the next message can still be sent")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task pause aborted the main chat",
		"When the user paused the task, the running main Agent conversation was cancelled in step as well. Activity records already produced are kept; after the task resumes, this turn's message is not replayed automatically")
	AbortChatTurnFinished = cause("chat_turn_finished", "Turn finished and released context",
		"This conversation turn finished normally and the server is releasing that turn's context resources. This is not a run interruption; if it appears in an interruption message, it means the cancel and the teardown event raced")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "Backend process shutting down",
		"The backend process received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running Agents are cancelled; after restart, leftover running intents are reset to open and re-executed")
	AbortRunHardTimeout = cause("run_hard_timeout", "Per-run hard-timeout backstop fired",
		"A single run exceeded the soft wall-clock budget plus the extra grace, meaning the model request or some tool did not return for a long time and the normal turn-boundary wrap-up could not run. Focus on the last tool call before the interruption that never returned")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "Upstream context hit its deadline",
			"The upstream context hit its deadline, but the setter did not attach a named cause via WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "Canceller attached no named cause",
			"The upstream context was cancelled, but the canceller did not attach a named cause via context.WithCancelCause; register a cause in agent/cancelcause.go and wire it into that cancellation site", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
