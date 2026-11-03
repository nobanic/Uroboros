package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nobanic/UroborOS/mission"
)

// CommitProof changes the existing graph atomically. There is no parallel
// summary/proof store: the authoritative records remain nodes and edges.
func (store *ExplorationStore) CommitProof(ctx context.Context, proof mission.Proof) (mission.Completion, error) {
	if err := proof.Validate(); err != nil {
		return mission.Completion{}, err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return mission.Completion{}, err
	}
	defer transaction.Rollback()
	var explorationID int64
	if err = transaction.QueryRowContext(ctx, `SELECT id FROM explorations WHERE id=$1 FOR UPDATE`, store.expID).Scan(&explorationID); err != nil {
		return mission.Completion{}, fmt.Errorf("lock mission graph: %w", err)
	}
	var goalState string
	err = transaction.QueryRowContext(ctx, `SELECT state FROM exploration_nodes
		WHERE exploration_id=$1 AND id=$2 AND kind='goal' FOR UPDATE`, store.expID, proof.GoalID).Scan(&goalState)
	if errors.Is(err, sql.ErrNoRows) {
		return mission.Completion{}, mission.ErrGoalUnavailable
	}
	if err != nil {
		return mission.Completion{}, err
	}
	if goalState != "open" && goalState != "met" {
		return mission.Completion{}, fmt.Errorf("goal cannot be proved from state %q", goalState)
	}

	var evidenceID int64
	err = transaction.QueryRowContext(ctx, `SELECT evidence.id FROM exploration_nodes evidence
		WHERE evidence.id=$2 AND evidence.kind IN ('fact','finding')
		AND evidence.state = 'confirmed'
		AND (evidence.exploration_id=$1 OR EXISTS (
			SELECT 1 FROM tasks owner
			JOIN task_relations relation ON relation.task_id=owner.id
			JOIN tasks source ON source.id=relation.source_task_id
			WHERE owner.exploration_id=$1 AND owner.deleted_at IS NULL
			AND source.deleted_at IS NULL AND source.exploration_id=evidence.exploration_id
		)) FOR SHARE OF evidence`, store.expID, proof.EvidenceID).Scan(&evidenceID)
	if errors.Is(err, sql.ErrNoRows) {
		return mission.Completion{}, mission.ErrEvidenceUnavailable
	}
	if err != nil {
		return mission.Completion{}, err
	}

	if _, err = transaction.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)
		VALUES($1,$2,'proves',$3) ON CONFLICT(exploration_id,src_id,rel,dst_id) DO NOTHING`, store.expID, evidenceID, proof.GoalID); err != nil {
		return mission.Completion{}, err
	}
	if _, err = transaction.ExecContext(ctx, `UPDATE exploration_nodes SET state='met',blocked_reason=NULL,
		payload=jsonb_set(payload,'{proof_reason}',to_jsonb($3::text)),
		content_version=content_version+1 WHERE exploration_id=$1 AND id=$2 AND state <> 'met'`, store.expID, proof.GoalID, proof.Reason); err != nil {
		return mission.Completion{}, err
	}
	state, err := readCompletion(ctx, transaction, store.expID)
	if err != nil {
		return mission.Completion{}, err
	}
	if err = transaction.Commit(); err != nil {
		return mission.Completion{}, err
	}
	return state, nil
}

type completionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readCompletion(ctx context.Context, reader completionReader, explorationID int64) (mission.Completion, error) {
	var state mission.Completion
	err := reader.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE state='met')
		FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, explorationID).Scan(&state.Total, &state.Proved)
	return state, err
}

func (store *ExplorationStore) CompletionState(ctx context.Context) (mission.Completion, error) {
	return readCompletion(ctx, store.db, store.expID)
}
