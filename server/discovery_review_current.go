package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/katana"
)

const discoveryFingerprintStabilizationAttempts = 4

// currentDiscoveryReviewState is the synchronous admission boundary shared by
// discovery_review, planner/worker gates and every typed scanner start. It
// computes the fingerprint from task state now, asks Katana to materialize that
// exact automatic snapshot, and only then resolves the matching review token.
func (s *Server) currentDiscoveryReviewState(ctx context.Context, taskID int64) (*db.DiscoveryReviewState, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return nil, errors.New("discovery coverage store is unavailable")
	}
	if taskID <= 0 {
		return nil, errors.New("task id is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	skipped, err := s.m.pg.InitialKatanaSkipped(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("read automatic Katana startup choice: %w", err)
	}

	runner := s.m.Katana()
	if runner == nil {
		state, err := s.m.pg.CurrentDiscoveryReviewStateForFingerprint(ctx, taskID, db.KatanaAutoFingerprint{})
		if err == nil && !state.AutomaticSkipped {
			state.AutomaticUnavailableReason = "Katana manager is unavailable"
		}
		return state, err
	}

	fingerprint, err := runner.CurrentAutoFingerprint(taskID)
	if err != nil {
		if errors.Is(err, katana.ErrNoExecutableNetworkScope) || errors.Is(err, katana.ErrNoConcreteHTTPSeed) {
			state, stateErr := s.m.pg.CurrentDiscoveryReviewStateForFingerprint(ctx, taskID, db.KatanaAutoFingerprint{})
			if stateErr == nil && !state.AutomaticSkipped {
				state.AutomaticUnavailableReason = err.Error()
			}
			return state, stateErr
		}
		return nil, fmt.Errorf("compute current automatic Katana fingerprint: %w", err)
	}

	unavailableReason := ""
	schedulingAllowed := !skipped
	if skipped {
		unavailableReason = "initial automatic Katana crawl skipped by operator; explicit tool runs remain available"
	}
	if task, ok := s.m.Task(fmt.Sprint(taskID)); ok {
		lifecycle := task.lifecycleSnapshot()
		if !skipped && (lifecycle.Paused || lifecycle.Queued || isTerminalStatus(lifecycle.Status)) {
			schedulingAllowed = false
			unavailableReason = "automatic Katana scheduling is inactive for the task lifecycle"
		}
	}
	if schedulingAllowed {
		if available, reason := runner.Available(); !available {
			schedulingAllowed = false
			unavailableReason = reason
		}
	}

	if schedulingAllowed {
		stable := false
		for attempt := 0; attempt < discoveryFingerprintStabilizationAttempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, _, ensureErr := runner.EnsureAuto(taskID); ensureErr != nil {
				if errors.Is(ensureErr, db.ErrKatanaTaskInactive) {
					schedulingAllowed = false
					unavailableReason = ensureErr.Error()
					break
				}
				return nil, fmt.Errorf("ensure current automatic Katana fingerprint: %w", ensureErr)
			}
			after, fingerprintErr := runner.CurrentAutoFingerprint(taskID)
			if fingerprintErr != nil {
				return nil, fmt.Errorf("recompute current automatic Katana fingerprint: %w", fingerprintErr)
			}
			if after == fingerprint {
				stable = true
				break
			}
			fingerprint = after
		}
		if !stable {
			return nil, errors.New("automatic Katana fingerprint changed repeatedly during typed-tool admission")
		}
	}

	state, err := s.m.pg.CurrentDiscoveryReviewStateForFingerprint(ctx, taskID, fingerprint)
	if err != nil {
		return nil, err
	}
	state.AutomaticUnavailableReason = unavailableReason
	active, err := s.m.pg.ActiveKatanaJob(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if active != nil && !fingerprint.Matches(active) {
		state.Reviewable = false
		state.Review = nil
		state.PendingReason = fmt.Sprintf("automatic Katana job %d must finish before the current fingerprint can be materialized", active.ID)
	}
	if schedulingAllowed && state.KatanaJob == nil && active == nil {
		return nil, errors.New("current automatic Katana fingerprint was not materialized")
	}
	return state, nil
}

func (s *Server) DiscoveryReviewRequired(ctx context.Context, taskID int64) (bool, error) {
	required, _, err := s.DiscoveryReviewGate(ctx, taskID)
	return required, err
}

func (s *Server) EnsureCurrentDiscoveryReview(ctx context.Context, taskID int64) error {
	if s == nil || s.m == nil || s.m.pg == nil {
		return errors.New("discovery coverage store is unavailable")
	}
	applies, err := s.m.pg.DiscoveryReviewApplies(ctx, taskID)
	if err != nil || !applies {
		return err
	}
	_, err = s.currentDiscoveryReviewState(ctx, taskID)
	return err
}

func (s *Server) DiscoveryReviewGate(ctx context.Context, taskID int64) (required, reviewable bool, err error) {
	applies, err := s.m.pg.DiscoveryReviewApplies(ctx, taskID)
	if err != nil {
		return true, false, err
	}
	if !applies {
		return false, true, nil
	}
	state, err := s.currentDiscoveryReviewState(ctx, taskID)
	if err != nil {
		return true, false, err
	}
	return !state.Reviewable || state.Review == nil, state.Reviewable, nil
}

func (s *Server) DiscoveryFamilyAllowed(ctx context.Context, taskID int64, family string) (bool, error) {
	applies, err := s.m.pg.DiscoveryReviewApplies(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !applies {
		return true, nil
	}
	state, err := s.currentDiscoveryReviewState(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !state.Reviewable || state.Review == nil {
		return false, nil
	}
	for _, decision := range state.Review.Decisions {
		if decision.Family == family {
			return decision.Action == "run", nil
		}
	}
	return false, nil
}
