// Package llmrec implements a recording decorator for llm.Provider. It intercepts
// every Stream call, captures the full request and accumulated response, and
// persists them to PostgreSQL for later inspection.
package llmrec

import (
	"context"

	"iter"

	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/llm"
)

type taskIDContextKey struct{}
type workerContextKey struct{}

// WithTaskID attaches the owning task registry id to an LLM call. Session ids
// are based on exploration ids, which are not interchangeable with task ids.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDContextKey{}, taskID)
}

// TaskIDFrom returns the explicit task registry id attached by the task runtime.
func TaskIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	taskID, _ := ctx.Value(taskIDContextKey{}).(string)
	return strings.TrimSpace(taskID)
}

// WithWorker overrides the agent-lane ("worker") label for an LLM call. The lane
// is normally parsed from the transcript session id (exp<N>-<role>); calls made
// outside the engine's worker/planner sessions — e.g. the intercept fallback judge
// — carry no such session, so they attach their lane explicitly here. This lets the
// usage ledger single out that spend (worker='judge') for the config page.
func WithWorker(ctx context.Context, worker string) context.Context {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return ctx
	}
	return context.WithValue(ctx, workerContextKey{}, worker)
}

// workerFrom returns the explicit lane override, or "" when none is set.
func workerFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	worker, _ := ctx.Value(workerContextKey{}).(string)
	return strings.TrimSpace(worker)
}

// Recorder wraps an llm.Provider and records every completion call.
type Recorder struct {
	inner llm.Provider
	pg    *db.DB
	model string // model name (from config, not in CompletionRequest)
	prof  string // LLM profile name (from llm_profiles)
	// thinkingType / reasoningEffort are config-level thinking parameters (thinking
	// on/off / thinking intensity). They are injected into the real HTTP body from
	// the provider config in norma's buildBody() and do not appear on
	// CompletionRequest, so the Recorder must carry its own copy here and write it
	// into the recording on serialization.
	thinkingType    string
	reasoningEffort string
	enabled         func() bool // reports whether recording is currently on; nil = always record
}

// Wrap returns a Provider that records calls to pg when enabled() reports true.
// profName is the LLM profile name (e.g. "default"); may be empty. thinkingType /
// reasoningEffort are the config-level thinking params actually sent to the API
// (empty = not sent). A nil enabled predicate records unconditionally.
func Wrap(inner llm.Provider, pg *db.DB, model, profName, thinkingType, reasoningEffort string, enabled func() bool) *Recorder {
	return &Recorder{
		inner: inner, pg: pg, model: model, prof: profName,
		thinkingType: thinkingType, reasoningEffort: reasoningEffort, enabled: enabled,
	}
}

// parseSession extracts task id and worker role from session strings like
// "exp1-worker-i3" → ("1", "worker") or "exp2-planner" → ("2", "planner").
func parseSession(s string) (taskID, worker string) {
	// format: exp<N>-<role>[-suffix]
	if !strings.HasPrefix(s, "exp") {
		return "", ""
	}
	rest := s[3:] // after "exp"
	// split task number
	i := strings.IndexByte(rest, '-')
	if i < 0 {
		return rest, ""
	}
	taskID = rest[:i]
	rest = rest[i+1:]
	// worker role is up to the next '-' (e.g. "worker" from "worker-i3")
	if j := strings.IndexByte(rest, '-'); j >= 0 {
		worker = rest[:j]
	} else {
		worker = rest
	}
	return taskID, worker
}

// Stream implements llm.Provider. It delegates to the inner provider, accumulates
// the streamed events to reconstruct the response, and records the full exchange.
//
// The session identifier (e.g. "exp1-worker-i3") is carried on ctx by the norma
// harness via transcript.WithSessionID; reading it per-call is race-free even when
// planner and multiple workers share one Recorder instance.
func (r *Recorder) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	observation := r.observeCall(ctx, req)
	return observation.stream
}

// Complete implements the atomic completion path while preserving the same
// usage metering, normalized body recording and raw wire capture as Stream.
func (r *Recorder) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return r.observeCall(ctx, req).complete()
}

func thinkingText(msg llm.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == llm.BlockThinking && block.Thinking != "" {
			b.WriteString(block.Thinking)
		}
	}
	return b.String()
}

// recordUsage appends one lightweight metering row to llm_usage (no bodies). Skips
// zero-token calls with no model, which carry nothing worth metering.
func (r *Recorder) recordUsage(taskID string, expID int64, worker string, usage llm.Usage, latencyMs int, status string) {
	(recordingUsage{recorder: r, taskID: taskID, explorationID: expID, worker: worker, usage: usage, latencyMs: latencyMs, status: status}).persist()
}

// record persists one LLM call to PostgreSQL before the provider stream returns.
// Keeping the write inside the owning task operation means task deletion can
// drain calls and then remove records without a late async insert recreating one.
func (r *Recorder) record(req llm.CompletionRequest, session, taskID, worker, reqBody string, capt *Capture, start time.Time, text, thinking string, usage llm.Usage, stopReason string, streamErr error) {
	(recordingExchange{recorder: r, session: session, taskID: taskID, worker: worker, requestBody: reqBody, capture: capt, start: start, text: text, thinking: thinking, usage: usage, stopReason: stopReason, err: streamErr}).persist()
}

// serializeRequest builds a JSON representation of the completion request.
func (r *Recorder) serializeRequest(req llm.CompletionRequest) string {
	return (recordingRequest{request: req, thinkingType: r.thinkingType, reasoningEffort: r.reasoningEffort}).json()
}
