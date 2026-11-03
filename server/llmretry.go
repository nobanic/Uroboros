package server

import (
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

// Server-side resolution of the retry policy; see docs/LLM-retry-design.md. Of the five layers:
//   - connect / empty-response / same-provider-safe-window "follow the endpoint" — each LLM
//     config can override the global default (a profile field left blank inherits the global,
//     and if the global is also unset the built-in default is used);
//   - circuit-breaker / intent-replay are process-level, with a single global copy.
//
// The global policy reads one settings row from the DB once; all call sites are on cold paths
// (building a provider, work settlement, saving config), not worth another cache layer; the
// breaker params are the exception — they're read on every failure path, so applyRetryPolicy
// pushes them to the Registry to hold.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// counts keep the original "0=default / negative=off" semantics here: the SDK's MaxRetries /
		// EmptyResponseRetries are fully isomorphic to this, so leave them for the SDK to resolve.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Circuit-breaker (failover cooldown) defaults match llmpool's built-in ones — here we only
// override when "the user configured a value". Intent-replay defaults live in engine.go's
// modelErrorRetries / modelErrorRetryBackoff.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob — the "empty-response
// retry count": the two are two means to the same end. The SDK layer handles "not a single
// content block", by resending the same request verbatim; this handles "thinking only, no body
// and no tool", by appending an instruction so the model continues from the thinking it already
// has (resending verbatim is pointless for an empty turn shaped by the context). The emptiness
// criterion differs because the SDK judges by "was any event yielded", and a thinking delta is
// itself an event — but when a user configures "how many empty-response retries" they mean "if
// the model produced no substantive content, try once more", and sharing one count across both
// layers matches that mental model.
//
// Read the global policy, not a profile override: a run may switch profiles mid-way via
// failover, but this is a total gate for the whole intent and shouldn't change with the
// endpoint. Semantics are isomorphic to the SDK's emptyRetries():
// 0 = default defaultEmptyTurnNudges; -1 (negative) = disable empty-turn continuation; >0 = use that value.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
