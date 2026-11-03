package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/UroborOS/mission"
	"github.com/nobanic/norma/harness"
)

// intentExecution owns one claimed intent from the execution barrier through
// final persistence and notification. Retries reuse its context, hooks and
// transcript request identity. The caller keeps task-operation admission.
type intentExecution struct {
	engine             *Engine
	task               *Task
	name               string
	worker             *agent.Worker
	intent             *db.Node
	requestID, message string

	hasChatMessage bool
	emit           func(db.Activity)
	ectx, workCtx  context.Context
	workCancel     context.CancelCauseFunc
	hooks          steerHooks
	wTaskID        int64
	reason         harness.TerminalReason
	wrote          agent.WriteCounts
	err            error
}

func (run *intentExecution) execute(ctx context.Context) bool {
	if !run.prepare(ctx) {
		return true
	}
	run.invoke()
	// Resolve once after the first execution, exactly as the original engine did.
	maxRetries, retryBackoff := run.engine.modelErrorRetryPolicy()
	for attempt := 1; attempt <= maxRetries &&
		retryableWorkerModelError(run.reason, run.err) &&
		run.workCtx.Err() == nil && run.ectx.Err() == nil && !run.engine.IsPaused(run.task.ID) && !run.engine.isSettling(run.task.ID); attempt++ {
		log.Printf("[worker %s] task %s intent #%d ended with model_error, retrying after %v (%d/%d)",
			run.name, run.task.ID, run.intent.ID, retryBackoff, attempt, maxRetries)
		if sleepCtx(run.workCtx, retryBackoff) {
			break
		}
		run.invoke()
	}
	return run.finish()
}

func (run *intentExecution) prepare(ctx context.Context) bool {
	run.hasChatMessage = run.message != ""
	run.engine.touch(run.task.ID)
	run.emit = func(r db.Activity) { run.engine.emitActivity(run.task, r) }
	run.ectx = run.engine.clockCtx(run.engine.execContextFor(ctx, run.task.ID), run.task, false)
	if run.ectx.Err() != nil || run.engine.IsDeleting(run.task.ID) {
		if err := transitionIntentState(run.task.Store, run.intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s intent #%d failed to roll back after claim: %v", run.name, run.task.ID, run.intent.ID, err)
		}
		return false
	}
	run.workCtx, run.workCancel = context.WithCancelCause(run.ectx)
	run.engine.registerWork(run.intent.ID, run.workCancel)
	iid := run.intent.ID
	taskEmit := func(a db.Activity) {
		nid := iid
		a.NodeID, a.Worker = &nid, run.name
		run.emit(a)
	}
	label := fmt.Sprintf("%s · #%d", run.name, iid)
	run.workCtx = intercept.WithTaskContext(run.workCtx, run.task.ID, label, taskEmit)
	// The quota belongs to the entire intent, including all model-error replays.
	run.hooks = steerHooks{
		inner:  run.task.Guard.Hooks(),
		drain:  func() (string, bool) { return run.engine.drainSteer(iid) },
		nudges: &atomic.Int64{},
		limit:  run.engine.emptyTurnNudgeLimit(),
		label:  label,
	}
	run.wTaskID, _ = strconv.ParseInt(run.task.ID, 10, 64)
	return true
}

// Keep accounting immediately around each SDK call. In particular, do not
// replace EndLLMCall with a defer that would change the legacy panic boundary.
func (run *intentExecution) invoke() {
	run.engine.BeginLLMCall(run.task.ID)
	if run.hasChatMessage {
		run.reason, run.wrote, run.err = run.worker.ExecuteWithMessage(run.workCtx, run.name, run.wTaskID, run.engine.m.assets, run.task.Store, run.intent, run.hooks, run.emit, run.engine.m.enrich, run.task.Notify, run.task.NotifyFinding, run.requestID, run.message)
	} else {
		run.reason, run.wrote, run.err = run.worker.Execute(run.workCtx, run.name, run.wTaskID, run.engine.m.assets, run.task.Store, run.intent, run.hooks, run.emit, run.engine.m.enrich, run.task.Notify, run.task.NotifyFinding)
	}
	run.engine.EndLLMCall(run.task.ID)
}

func (run *intentExecution) finish() bool {
	// Detach cancels the context: the cause and planner-stop flag must be sampled
	// before detach, while controller completion must wait for all writes/wakes.
	workCause := context.Cause(run.workCtx)
	killed := run.workCtx.Err() != nil && run.ectx.Err() == nil
	action, completeWork := run.engine.detachWork(run.intent.ID)
	if action == "" {
		switch {
		case errors.Is(workCause, agent.AbortWorkPausedByUser):
			action = "pause"
		case errors.Is(workCause, agent.AbortWorkCancelledByUser):
			action = "cancel"
		}
	}
	interruption := mission.Uninterrupted
	switch {
	case action == "pause":
		interruption = mission.OperatorPause
	case action == "cancel":
		interruption = mission.OperatorCancel
	case run.ectx.Err() != nil && taskExecutionPaused(context.Cause(run.ectx)):
		interruption = mission.MissionPause
	case run.ectx.Err() != nil && run.engine.isSettling(run.task.ID):
		interruption = mission.DeadlineSettlement
	case run.ectx.Err() != nil && isTerminalStatus(run.engine.m.TaskStatus(run.task.ID)):
		interruption = mission.MissionFinished
	case killed:
		interruption = mission.PlannerStop
	}
	settlement := mission.SettleExecution(mission.ExecutionResult{
		Interruption: interruption, Failed: run.err != nil,
		BudgetReached:          run.reason == harness.ReasonMaxTurns || run.reason == harness.ReasonTimeout,
		ProviderQuotaExhausted: isTaskLLMChainExhausted(run.err),
	})
	var persistErr error
	defer func() { completeWork(persistErr) }()
	if settlement.ProviderBlocked {
		persistErr = run.task.Store.SetIntentBlockedReason(run.intent.ID, db.IntentBlockedLLMQuota)
	} else {
		persistErr = transitionIntentState(run.task.Store, run.intent.ID, "running", settlement.State)
	}
	if persistErr != nil {
		log.Printf("[worker %s] task %s intent %d could not persist %s: %v", run.name, run.task.ID, run.intent.ID, settlement.State, persistErr)
		return true
	}
	log.Printf("[worker %s] task %s intent %d state=%s reason=%q writes=%s error=%v", run.name, run.task.ID, run.intent.ID, settlement.State, settlement.Reason, run.wrote, run.err)
	run.engine.touch(run.task.ID)
	switch settlement.Wake {
	case mission.GraphChanged:
		run.task.Notify()
	case mission.WorkFinished:
		run.task.NotifyDone(run.intent.ID)
	}
	return true
}
