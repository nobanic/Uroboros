package server

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

// Task-level timeout coordinator (see docs/task-timeout-and-wrapup-design.md §4/§8.5).
// Absolute wall clock: one timer goroutine per task with a timeout; at the deadline it drives
// the ordered settle sequence:
//   ① settling → ② workers stop claiming new intents / ③ planner drops ordinary notifies
//   ④ wait for running workers to drain (within grace) → ⑤ one final planner round → ⑥ set terminal state (guarded)

const (
	settleDrainGrace     = 90 * time.Second // cap on waiting for running workers to settle gracefully; beyond it, hard cancel
	deadlinePollInterval = 2 * time.Second  // poll interval while the deadline is unstamped / the LLM isn't ready
	deadlineMaxSleep     = 30 * time.Second // max single sleep (so terminal state is re-checked periodically)
)

// ---------- settling state ----------

func (e *Engine) isSettling(taskID string) bool {
	v, _ := e.settling.Load(taskID)
	b, _ := v.(bool)
	return b
}

// markSettling flips settling on; returns true only for the first caller.
func (e *Engine) markSettling(taskID string) bool {
	_, loaded := e.settling.LoadOrStore(taskID, true)
	return !loaded
}

// ---------- in-flight count (worker.Execute + planner.Plan), used for drain ----------

func (e *Engine) inflightCounter(taskID string) *int64 {
	v, _ := e.inflight.LoadOrStore(taskID, new(int64))
	return v.(*int64)
}

// beginTaskOperation atomically registers a task-owned operation unless deletion
// has already installed its barrier. The delete handler can therefore wait for
// inflight==0 without a check-then-start race recreating files after cleanup.
func (e *Engine) beginTaskOperation(taskID string) bool {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(taskID) {
		return false
	}
	atomic.AddInt64(e.inflightCounter(taskID), 1)
	return true
}

func (e *Engine) decInflight(taskID string) { atomic.AddInt64(e.inflightCounter(taskID), -1) }
func (e *Engine) inflightCount(taskID string) int64 {
	return atomic.LoadInt64(e.inflightCounter(taskID))
}

// ---------- deadline ----------

// taskDeadline returns the task's absolute deadline (unix). Prefers the in-process
// map (stamped this session); falls back to the DB-loaded value (restart), seeding
// the map. 0 = no timeout / not yet stamped.
func (e *Engine) taskDeadline(t *Task) int64 {
	if v, ok := e.deadline.Load(t.ID); ok {
		return v.(int64)
	}
	deadlineAt := t.lifecycleSnapshot().DeadlineAt
	if deadlineAt > 0 {
		e.deadline.Store(t.ID, deadlineAt)
		return deadlineAt
	}
	return 0
}

// resetTimeoutRevival clears only the per-run timeout state after PostgreSQL has
// atomically committed timeout -> running and reset first_run_at/deadline_at. The
// configured TimeoutSeconds remains on Task, so the next real provider request
// stamps a fresh full budget. coordStarted is reset because the coordinator that
// produced the timeout has already completed (or is in its final return path).
func (e *Engine) resetTimeoutRevival(taskID string) {
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.coordStarted.Delete(taskID)
}

// clockCtx layers the task's TaskClock (absolute deadline) onto a run's context so
// worker/planner can clamp their wall-clock budget and pick per-run vs task-timeout
// wrap-up words. final marks the coordinator-driven terminal planner round.
func (e *Engine) clockCtx(base context.Context, t *Task, final bool) context.Context {
	dl := e.taskDeadline(t)
	if dl <= 0 && !final {
		return base // no timeout → unchanged behavior
	}
	return agent.WithTaskClock(base, agent.TaskClock{DeadlineUnix: dl, Final: final})
}

// ---------- coordinator ----------

// startDeadlineCoordinator launches the per-task deadline timer once (idempotent).
// Called from Run() and from the restart reload path, so non-active timeout tasks
// still get settled after their deadline even without live planner/worker loops.
func (e *Engine) startDeadlineCoordinator(ctx context.Context, t *Task) {
	if t == nil || t.TimeoutSeconds <= 0 {
		return
	}
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.coordStarted.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1)
	e.deleteMu.RUnlock()
	runTaskRoutine(rt, func(loopCtx context.Context) { e.deadlineCoordinator(loopCtx, t) })
}

// deadlineCoordinator waits until the task's absolute deadline. The persisted
// clock keeps counting through pauses, but settlement waits for resume.
func (e *Engine) deadlineCoordinator(ctx context.Context, t *Task) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return // already finished (goals met / failed) — nothing to time out
		}
		dl := e.taskDeadline(t)
		if dl <= 0 {
			if sleepCtx(ctx, deadlinePollInterval) { // not yet stamped (task hasn't really run)
				return
			}
			continue
		}
		if remaining := time.Until(time.Unix(dl, 0)); remaining > 0 {
			nap := remaining
			if nap > deadlineMaxSleep {
				nap = deadlineMaxSleep
			}
			if sleepCtx(ctx, nap) {
				return
			}
			continue
		}
		if e.taskSettlementPaused(t) {
			if sleepCtx(ctx, deadlinePollInterval) {
				return
			}
			continue
		}
		if e.settleTask(ctx, t) {
			return
		}
		if sleepCtx(ctx, deadlinePollInterval) {
			return
		}
	}
}

// settleTask runs the ordered settle sequence once (§4 steps ①–⑥).
func (e *Engine) taskSettlementPaused(t *Task) bool {
	return t != nil && (e.IsPaused(t.ID) || t.lifecycleSnapshot().Paused)
}

// settleTask returns false when a pause or shutdown interrupts it before a
// terminal write. The coordinator retains the persisted deadline and retries
// after resume.
func (e *Engine) settleTask(ctx context.Context, t *Task) bool {
	if e.taskSettlementPaused(t) {
		return false
	}
	if !e.markSettling(t.ID) {
		return true
	}
	log.Printf("[deadline] task %s reached the timeout limit, entering the settle sequence", t.ID)

	// ④ wait for running worker/planner to drain (a running run settles itself via the clamped
	// MaxDuration); if still not clear beyond grace → hard-cancel the task exec ctx (the
	// settling-aware branch classifies it correctly).
	hardStop := time.Now().Add(settleDrainGrace)
	for e.inflightCount(t.ID) > 0 {
		if e.taskSettlementPaused(t) {
			e.settling.Delete(t.ID)
			return false
		}
		if time.Now().After(hardStop) {
			log.Printf("[deadline] task %s drain timed out (%s), hard-cancelling the running run", t.ID, settleDrainGrace)
			e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
			_ = sleepCtx(ctx, 3*time.Second) // give the worker branch a moment to persist/classify
			break
		}
		if sleepCtx(ctx, 500*time.Millisecond) {
			return false // engine is shutting down entirely
		}
	}

	// ⑤ one final planner round (task-timeout wording, final goal judgment, no new intents).
	if e.taskSettlementPaused(t) {
		e.settling.Delete(t.ID)
		return false
	}
	met := e.runFinalPlannerRound(ctx, t)
	if e.taskSettlementPaused(t) {
		e.settling.Delete(t.ID)
		return false
	}
	if !e.beginTaskOperation(t.ID) {
		return true
	}
	defer e.decInflight(t.ID)
	if e.taskSettlementPaused(t) {
		e.settling.Delete(t.ID)
		return false
	}

	// ⑥ set terminal state (guarded): met → done (completed); otherwise timeout. If the normal
	// path already set done first, the guard (SetTaskStatusGuarded) refuses to overwrite, keeping
	// the completed semantics.
	status := "timeout"
	if met {
		status = "done"
	}
	won, err := e.m.SetTaskStatusGuarded(t.ID, status)
	switch {
	case err != nil:
		log.Printf("[deadline] task %s failed to set terminal state: %v", t.ID, err)
		e.settling.Delete(t.ID)
		return false
	case won:
		log.Printf("[deadline] task %s settle complete, terminal state=%s", t.ID, status)
	case e.taskSettlementPaused(t):
		// Pause can commit after the last in-memory check but before the guarded
		// SQL write. Keep the deadline and let the coordinator retry after resume.
		e.settling.Delete(t.ID)
		return false
	default:
		log.Printf("[deadline] task %s already terminal at settle time, keeping existing status", t.ID)
	}
	return true
}

// runFinalPlannerRound drives exactly ONE terminal planner round with the
// task-timeout planner words (final goal judgment; no new intents). Waits for the
// LLM to be ready (bounded by ctx) so a completable task isn't mis-judged timeout.
func (e *Engine) runFinalPlannerRound(ctx context.Context, t *Task) (met bool) {
	if e.IsDeleting(t.ID) {
		return false
	}
	if e.completeIfPersistedGoalsMet(t) {
		return true
	}
	planner, _ := e.snapshotFor(t)
	for planner == nil {
		if e.taskSettlementPaused(t) {
			return false
		}
		if sleepCtx(ctx, deadlinePollInterval) {
			return false
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return false
		}
		if e.IsDeleting(t.ID) {
			return false
		}
		planner, _ = e.snapshotFor(t)
	}
	// A user pause cancels the final round too. Settlement remains pending and is
	// retried after resume instead of issuing model/tool calls while paused.
	fctx := e.clockCtx(e.execContextFor(ctx, t.ID), t, true)
	if fctx.Err() != nil || e.taskSettlementPaused(t) {
		return false
	}
	if !e.beginTaskOperation(t.ID) {
		return false
	}
	defer e.decInflight(t.ID)
	emit := func(r db.Activity) { e.emitActivity(t, r) }
	e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: fmt.Sprintf("Task-timeout settlement · final judgment (round %d)", e.nextPlannerRound(t.ID))})
	tTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	met, reason, err := planner.Plan(fctx, tTaskID, e.m.assets, t.Store, t.Goal, t.drainTriggers(), emit, t.Notify)
	e.EndLLMCall(t.ID)
	if e.completeIfPersistedGoalsMet(t) {
		return true
	}
	if err != nil {
		log.Printf("[deadline] task %s final planning error: %v", t.ID, err)
	} else if met {
		log.Printf("[deadline] task %s final judgment: goals met: %s", t.ID, reason)
	}
	return met
}
