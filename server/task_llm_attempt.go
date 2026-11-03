package server

import (
	"context"
	"errors"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/llm"
	"strings"
	"time"
)

func taskProviderCallContext(ctx context.Context, selection taskLLMSelection) (context.Context, context.CancelFunc) {
	if selection.task == nil || agent.TaskClockFrom(ctx).Final {
		return ctx, func() {}
	}
	deadline := selection.task.lifecycleSnapshot().DeadlineAt
	if deadline <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, time.Unix(deadline, 0))
}

func streamEventCommitsOutput(event llm.StreamEvent) bool {
	switch event.Type {
	case llm.SETextDelta, llm.SEThinkingDelta, llm.SEToolInputJSON:
		return event.Text != ""
	case llm.SEToolUseStart, llm.SEMessageDelta, llm.SEMessageStop:
		return true
	default:
		return false
	}
}

// [Default] number of same-provider retries within the pre-commit safe window. The SDK's
// doStream only retries the connect phase (before a 200); once the stream starts, a mid-stream
// break / overloaded / in-stream 429 and other transient faults bubble straight up as
// model_error with zero retries. As long as not a single token has reached the caller
// (!committed), replaying the exact same request can't duplicate model output or tool side
// effects, so we add a layer of same-provider backoff retry here to keep such flakiness from
// forcing a whole-intent re-run. Overridable by the LLM config's retry / global retry policy.
const sameProviderStreamRetries = 2

// sameProviderRetryBackoff is the [default] backoff before retry attempt N (0.5s, 1s…, capped
// at 4s), the same exponential shape as the SDK but with a smaller cap so it doesn't hold up a
// worker's settle/cancel response. Exposed as a variable so tests can zero out the backoff.
var sameProviderRetryBackoff = func(attempt int) time.Duration {
	return min(500*time.Millisecond*(1<<attempt), 4*time.Second)
}

// sameProviderRetryPolicy resolves which same-provider retry parameters this call uses: if the
// config sets a count, use it (a negative value disables this retry layer); if it sets an
// interval, replace the exponential backoff with a fixed one; with neither set, behavior is
// byte-for-byte identical to before it became configurable.
func sameProviderRetryPolicy(r agent.RetryConfig) (retries int, backoff func(int) time.Duration) {
	retries, backoff = sameProviderStreamRetries, sameProviderRetryBackoff
	if r.StreamAttempts != 0 {
		retries = max(r.StreamAttempts, 0)
	}
	if r.StreamInterval > 0 {
		d := r.StreamInterval
		backoff = func(int) time.Duration { return d }
	}
	return retries, backoff
}

// isRetryableStreamError decides whether a "stream failure before commit" is worth replaying on
// the same provider. Transient transport breaks / provider overload / rate limiting recover on
// their own and are safe to retry; these three classes are not retried:
//   - quota exhausted: handed to profile failover, don't waste retries here
//   - context too long: replaying the same request won't help, left to harness's reactive compaction
//   - 4xx deterministic rejection (400/401/403/404/422): fails the same way on any provider
func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	if isQuotaExhaustedError(err) {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "too long") || strings.Contains(s, "context length") ||
		strings.Contains(s, "context_length") || strings.Contains(s, "maximum context") ||
		strings.Contains(s, "status 413") {
		return false
	}
	for _, code := range []string{"status 400", "status 401", "status 403", "status 404", "status 422"} {
		if strings.Contains(s, code) {
			return false
		}
	}
	// everything else (transport reset/EOF/timeout, 408/429/5xx, in-stream error events such as
	// anthropic overloaded_error, etc.) is treated as transient and allowed to retry.
	return true
}

// beginTaskProviderAttempt keeps the barrier before context acquisition. Failed
// acquisition releases only its own child; successful callers own cancellation.
func beginTaskProviderAttempt(ctx context.Context, selection taskLLMSelection, before func(context.Context, taskLLMSelection) error) (context.Context, context.CancelFunc, error) {
	if before != nil {
		if err := before(ctx, selection); err != nil {
			return nil, nil, err
		}
	}
	callCtx, cancel := taskProviderCallContext(ctx, selection)
	if cause := context.Cause(callCtx); cause != nil {
		cancel()
		return nil, nil, cause
	}
	return callCtx, cancel, nil
}

func retryTaskProviderAttempt(ctx context.Context, err error, attempt, retries int, uncommitted bool) bool {
	return err != nil && uncommitted && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) && attempt < retries && isRetryableStreamError(err)
}

// advanceTaskProviderQuota borrows the original selection/revision and callbacks.
// Callers retain their distinct complete versus committed-stream error results.
func advanceTaskProviderQuota(selection taskLLMSelection, hooks taskLLMStreamHooks, cause error) (db.TaskLLMTransition, error) {
	transition, err := hooks.exhaust(selection, cause)
	if err != nil {
		return transition, err
	}
	if transition.Advanced && !transition.Stale && hooks.transition != nil {
		hooks.transition(selection, transition, cause)
	}
	return transition, nil
}
