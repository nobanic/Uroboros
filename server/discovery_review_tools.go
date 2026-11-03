package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

func (s *Server) discoveryReviewTools() []actool.CoreTool {
	return []actool.CoreTool{s.discoveryReviewTool(), s.recordDiscoveryDecisionTool()}
}

func discoveryDecisionSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Exactly one explicit run/skip decision for every typed discovery family.",
		"minItems":    len(db.DiscoveryDecisionFamilies()),
		"maxItems":    len(db.DiscoveryDecisionFamilies()),
		"items": katanaObjectSchema(map[string]any{
			"family": map[string]any{"type": "string", "enum": db.DiscoveryDecisionFamilies()},
			"action": map[string]any{"type": "string", "enum": []string{"run", "skip"}},
			"reason": strParam("evidence-based reason tied to the current Katana result and task-local coverage"),
		}, "family", "action", "reason"),
	}
}

func (s *Server) discoveryReviewTool() actool.CoreTool {
	return roTool("discovery_review",
		"Inspect the authoritative task-local discovery snapshot that must be reviewed before goal work is dispatched. Returns the latest automatic Katana job and bounded results, task-owned asset provenance, durable typed-scanner job counts, the current review token, and any matching coverage decision. Worker traces are not scanner lifecycle authority.",
		katanaObjectSchema(nil), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("discovery review requires a task-bound agent run; no task scope is attached"), nil
			}
			if err := validateKatanaEmptyInput(input); err != nil {
				return actool.Errorf("invalid discovery review query: " + err.Error()), nil
			}
			state, err := s.currentDiscoveryReviewState(ctx, taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			assets, err := s.m.pg.TaskAssetProvenanceCounts(ctx, taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			scanners, err := s.m.pg.TaskScannerJobCounts(ctx, taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var discoveries []db.KatanaDiscovery
			if state.KatanaJob != nil {
				discoveries, err = s.m.pg.ListKatanaDiscoveries(ctx, state.KatanaJob.ID, 200, 0)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
			}
			auto := map[string]any{"status": "not_started"}
			if state.KatanaJob != nil {
				auto = katanaJobStatus(state.KatanaJob)
			}
			if state.AutomaticSkipped {
				auto = map[string]any{
					"status": "skipped_by_operator",
					"reason": "The operator disabled the initial infrastructure crawl. The planner may still authorize an explicit katana_run.",
				}
			}
			katanaAvailable := false
			katanaUnavailableReason := "Katana manager is unavailable"
			if s.m.Katana() != nil {
				katanaAvailable, katanaUnavailableReason = s.m.Katana().Available()
			}
			return jsonResult(map[string]any{
				"task_id":             taskID,
				"review_token":        state.ReviewToken,
				"reviewable":          state.Reviewable,
				"pending_reason":      state.PendingReason,
				"current_fingerprint": state.CurrentFingerprint,
				"review_required":     !state.Reviewable || state.Review == nil,
				"automatic_katana":    auto,
				"automatic_skipped":   state.AutomaticSkipped,
				"katana_runtime": map[string]any{
					"available": katanaAvailable, "unavailable_reason": katanaUnavailableReason,
					"snapshot_unavailable_reason": state.AutomaticUnavailableReason,
				},
				"katana_results":           discoveries,
				"katana_results_truncated": state.KatanaJob != nil && state.KatanaJob.CandidateCount+state.KatanaJob.VerifiedCount > int64(len(discoveries)),
				"task_asset_provenance":    assets,
				"typed_scanner_jobs":       scanners,
				"required_families":        db.DiscoveryDecisionFamilies(),
				"decision":                 state.Review,
				"authority_note":           "automatic Katana and typed scanner job tables are authoritative; worker traces alone cannot prove that infrastructure did or did not run",
			})
		})
}

func (s *Server) recordDiscoveryDecisionTool() actool.CoreTool {
	return wrTool("record_discovery_decision",
		"Persist the mandatory coverage decision for the exact review_token returned by discovery_review. The automatic Katana job must be terminal (or explicitly unavailable/not started). Every typed family needs one run/skip decision with an evidence-based reason. A run decision authorizes that typed start tool; skip keeps it fail-closed. Raw Arsenal MCP remains unavailable.",
		katanaObjectSchema(map[string]any{
			"review_token": strParam("exact current token from discovery_review"),
			"summary":      strParam("concise assessment of Katana coverage, observed gaps, and why the selected follow-ups are proportionate"),
			"decisions":    discoveryDecisionSchema(),
		}, "review_token", "summary", "decisions"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("discovery decision requires a task-bound planner run; no task scope is attached"), nil
			}
			var args struct {
				ReviewToken string                 `json:"review_token"`
				Summary     string                 `json:"summary"`
				Decisions   []db.DiscoveryDecision `json:"decisions"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid discovery decision: " + err.Error()), nil
			}
			state, err := s.currentDiscoveryReviewState(ctx, taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if !state.Reviewable {
				return actool.Errorf(db.ErrDiscoveryReviewNotReady.Error()), nil
			}
			if args.ReviewToken != state.ReviewToken {
				return actool.Errorf(db.ErrDiscoveryReviewStale.Error()), nil
			}
			fingerprint := db.KatanaAutoFingerprint{}
			if state.CurrentFingerprint != nil {
				fingerprint = *state.CurrentFingerprint
			}
			review, err := s.m.pg.RecordDiscoveryReviewForFingerprint(ctx, taskID, fingerprint, args.ReviewToken, args.Summary, "planner", args.Decisions)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if task, ok := s.m.Task(fmt.Sprint(taskID)); ok {
				s.engine.RefreshDiscoveryReviewGate(task.ID)
				task.Notify()
			}
			return jsonResult(map[string]any{
				"recorded": true,
				"review":   review,
				"next":     "Start only families marked run through their typed tool; ordinary goal dispatch is now released for this automatic Katana snapshot.",
			})
		})
}
