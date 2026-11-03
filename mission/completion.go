// Package mission defines application operations independently of models,
// transport, SQL and the external agent SDK.
package mission

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrGoalUnavailable     = errors.New("goal is unavailable in this mission")
	ErrEvidenceUnavailable = errors.New("evidence is unavailable in this mission or its direct sources")
	ErrUnprovedGoals       = errors.New("mission still has unproved goals")
)

type Proof struct {
	GoalID     int64
	EvidenceID int64
	Reason     string
}

func (proof Proof) Validate() error {
	if proof.GoalID <= 0 || proof.EvidenceID <= 0 {
		return errors.New("positive goal_id and evidence_id are required")
	}
	if proof.GoalID == proof.EvidenceID {
		return errors.New("a goal cannot serve as its own evidence")
	}
	if strings.TrimSpace(proof.Reason) == "" {
		return errors.New("explain how the evidence satisfies this goal")
	}
	return nil
}

type Completion struct {
	Total  int `json:"total"`
	Proved int `json:"proved"`
}

func (state Completion) Ready() bool {
	return state.Total > 0 && state.Proved == state.Total
}

// CompletionRepository owns transaction boundaries. Successful CommitProof means
// both the evidence edge and the goal transition are durable, or neither is.
// Repeating the same proof must be idempotent.
type CompletionRepository interface {
	CommitProof(context.Context, Proof) (Completion, error)
	CompletionState(context.Context) (Completion, error)
}

type CompletionService struct{ Repository CompletionRepository }

func (service CompletionService) Prove(ctx context.Context, proof Proof) (Completion, error) {
	if err := proof.Validate(); err != nil {
		return Completion{}, err
	}
	if service.Repository == nil {
		return Completion{}, errors.New("completion repository is unavailable")
	}
	return service.Repository.CommitProof(ctx, proof)
}

// Finish never manufactures evidence or mutates goal states. A model can ask to
// finish, but the existing persisted graph must already prove every goal.
func (service CompletionService) Finish(ctx context.Context) (Completion, error) {
	if service.Repository == nil {
		return Completion{}, errors.New("completion repository is unavailable")
	}
	state, err := service.Repository.CompletionState(ctx)
	if err != nil {
		return Completion{}, err
	}
	if !state.Ready() {
		return state, fmt.Errorf("%w (%d of %d proved)", ErrUnprovedGoals, state.Proved, state.Total)
	}
	return state, nil
}
