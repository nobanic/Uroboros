package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/llmrec"
	"github.com/nobanic/norma/llm"
	"iter"
	"log"
)

// streamHooks builds the failover/exhaustion callbacks shared by Stream and
// Complete: how to read the current profile selection, how to mark it quota
// exhausted, and how to emit a failover transition.
func (r *taskLLMRuntime) streamHooks() taskLLMStreamHooks {
	return taskLLMStreamHooks{
		current:    r.current,
		beforeCall: r.beforeProviderCall,
		exhaust: func(selection taskLLMSelection, cause error) (db.TaskLLMTransition, error) {
			taskNum, _ := parseTaskID(r.taskID)
			transition, err := r.s.m.pg.MarkTaskLLMProfileQuotaExhaustedAtRevision(taskNum, selection.profileID, selection.revision, cause.Error())
			if err != nil {
				return transition, err
			}
			if pt, getErr := r.s.m.pg.GetTask(taskNum); getErr == nil && pt != nil {
				r.s.syncTaskLLMState(pt)
			}
			return transition, nil
		},
		transition: func(selection taskLLMSelection, transition db.TaskLLMTransition, cause error) {
			r.s.emitTaskLLMTransition(selection.task, transition, cause)
		},
	}
}

// beforeProviderCall is the single execution boundary shared by goal
// decomposition, planner, worker and main-agent runtimes. It is checked before
// every real provider attempt (including retries/failover). concMu orders it with
// pause/resume admission; PostgreSQL then atomically stamps the first run and
// rejects any lifecycle state that must not issue a request.
func (r *taskLLMRuntime) beforeProviderCall(ctx context.Context, selection taskLLMSelection) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if r == nil || r.s == nil || r.s.m == nil || r.s.engine == nil {
		return errors.New("task LLM runtime is unavailable")
	}
	r.s.concMu.Lock()
	defer r.s.concMu.Unlock()
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	t, ok := r.s.m.Task(r.taskID)
	if !ok || t == nil || (selection.task != nil && selection.task != t) || r.s.engine.IsDeleting(r.taskID) {
		return fmt.Errorf("task %s is unavailable for an LLM call", r.taskID)
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.Paused || lifecycle.Queued || isTerminalStatus(lifecycle.Status) || r.s.engine.IsPaused(r.taskID) {
		return agent.AbortPausedRaceGuard
	}
	deadline, err := r.s.m.BeginTaskRun(r.taskID)
	if err != nil {
		return err
	}
	r.s.engine.stamped.Store(r.taskID, true)
	if deadline > 0 {
		r.s.engine.deadline.Store(r.taskID, deadline)
		parent := r.s.ctx
		if parent == nil {
			parent = context.Background()
		}
		r.s.engine.startDeadlineCoordinator(parent, t)
	}
	return nil
}

func (r *taskLLMRuntime) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	ctx = llmrec.WithTaskID(ctx, r.taskID)
	return streamTaskLLM(ctx, r.taskID, req, r.streamHooks())
}

// Complete is the non-streaming counterpart of Stream. A non-streaming call is
// atomic — it never delivers partial output — so every failure is safe to retry
// on the same provider or fail over to the next profile without risking
// duplicated model output or tool execution (the "committed" bookkeeping the
// streaming path needs is unnecessary here).
func (r *taskLLMRuntime) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	ctx = llmrec.WithTaskID(ctx, r.taskID)
	return completeTaskLLM(ctx, r.taskID, req, r.streamHooks())
}

func completeTaskLLM(ctx context.Context, taskID string, req llm.CompletionRequest, hooks taskLLMStreamHooks) (llm.Message, string, llm.Usage, error) {
	for {
		selection, err := hooks.current()
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		var (
			msg     llm.Message
			sr      string
			usage   llm.Usage
			callErr error
		)
		// same-provider safe-window retry: a non-streaming call either fully succeeds or fully
		// fails — there's no partially-delivered output — so any transient failure can be retried as-is.
		retries, backoffOf := sameProviderRetryPolicy(selection.retry)
		for attempt := 0; ; attempt++ {
			callCtx, cancelCall, attemptErr := beginTaskProviderAttempt(ctx, selection, hooks.beforeCall)
			if attemptErr != nil {
				callErr = attemptErr
				break
			}
			msg, sr, usage, callErr = selection.provider.Complete(callCtx, req)
			cancelCall()
			if retryTaskProviderAttempt(ctx, callErr, attempt, retries, true) {
				backoff := backoffOf(attempt)
				log.Printf("[task-llm] task %s non-streaming call failed, retrying on the same provider after %v (%d/%d): %v",
					taskID, backoff, attempt+1, retries, callErr)
				if sleepCtx(ctx, backoff) {
					break // ctx cancelled during backoff → stop retrying
				}
				continue
			}
			break
		}
		if callErr == nil {
			return msg, sr, usage, nil
		}
		// profileID=0: the explicit chain was cleared; non-quota error: pass through. Neither changes task failover state.
		if selection.profileID == 0 || !isQuotaExhaustedError(callErr) {
			return llm.Message{}, "", llm.Usage{}, callErr
		}
		transition, markErr := advanceTaskProviderQuota(selection, hooks, callErr)
		if markErr != nil {
			return llm.Message{}, "", llm.Usage{}, fmt.Errorf("mark profile quota exhausted after %v: %w", callErr, markErr)
		}
		if !transition.Stale && transition.NextProfileID == nil {
			return llm.Message{}, "", llm.Usage{}, &taskLLMError{taskID: taskID, chainExhausted: transition.ChainExhausted, cause: callErr}
		}
		// Nothing was delivered to the caller, so replaying the same logical request on the next profile is safe.
	}
}

func streamTaskLLM(ctx context.Context, taskID string, req llm.CompletionRequest, hooks taskLLMStreamHooks) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for {
			selection, err := hooks.current()
			if err != nil {
				yield(llm.StreamEvent{}, err)
				return
			}
			committed := false
			var pending []llm.StreamEvent
			var streamErr error
			retries, backoffOf := sameProviderRetryPolicy(selection.retry)
			// same-provider safe-window retry: a transient failure before committed (no output
			// delivered to the caller yet) can be replayed as-is without duplicating model output
			// or tool execution. After committed, ctx cancellation, or a deterministic/quota error,
			// break out and hand off to the existing pass-through/failover logic below.
			for attempt := 0; ; attempt++ {
				committed = false
				pending = nil
				streamErr = nil
				callCtx, cancelCall, attemptErr := beginTaskProviderAttempt(ctx, selection, hooks.beforeCall)
				if attemptErr != nil {
					streamErr = attemptErr
					break
				}
				for event, err := range selection.provider.Stream(callCtx, req) {
					if err != nil {
						streamErr = err
						break
					}
					if !committed && !streamEventCommitsOutput(event) {
						pending = append(pending, event)
						continue
					}
					if !committed {
						for _, buffered := range pending {
							if !yield(buffered, nil) {
								cancelCall()
								return
							}
						}
						pending = nil
						committed = true
					}
					if !yield(event, nil) {
						cancelCall()
						return
					}
				}
				cancelCall()
				if retryTaskProviderAttempt(ctx, streamErr, attempt, retries, !committed) {
					backoff := backoffOf(attempt)
					log.Printf("[task-llm] task %s stream failed before commit, retrying on the same provider after %v (%d/%d): %v",
						taskID, backoff, attempt+1, retries, streamErr)
					if sleepCtx(ctx, backoff) {
						break // ctx cancelled during backoff → stop retrying
					}
					continue
				}
				break
			}
			if streamErr == nil {
				for _, buffered := range pending {
					if !yield(buffered, nil) {
						return
					}
				}
				return
			}
			// profileID=0 means the explicit chain was cleared while this stable task
			// bundle was still in use. Agent/global fallback errors follow the legacy
			// behavior and never mutate task failover state.
			if selection.profileID == 0 || !isQuotaExhaustedError(streamErr) {
				for _, buffered := range pending {
					if !yield(buffered, nil) {
						return
					}
				}
				yield(llm.StreamEvent{}, streamErr)
				return
			}
			transition, markErr := advanceTaskProviderQuota(selection, hooks, streamErr)
			if markErr != nil {
				cause := fmt.Errorf("mark profile quota exhausted after %v: %w", streamErr, markErr)
				if committed {
					// Output may already have driven tool execution. Report the persistence
					// failure, but classify it as router-handled so the worker does not
					// replay the entire intent and duplicate those side effects.
					yield(llm.StreamEvent{}, &taskLLMError{taskID: taskID, cause: cause})
				} else {
					yield(llm.StreamEvent{}, cause)
				}
				return
			}
			if committed || (!transition.Stale && transition.NextProfileID == nil) {
				yield(llm.StreamEvent{}, &taskLLMError{taskID: taskID, chainExhausted: transition.ChainExhausted, cause: streamErr})
				return
			}
			// No event reached the caller, so replaying the same logical request on
			// the next profile cannot duplicate model output or tool execution.
		}
	}
}
