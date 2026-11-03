package db

import (
	"database/sql"
	"strconv"
)

// The command owns rollback/commit; its plan borrows the same locked transaction.
type taskRegistryDeletion struct {
	database                  *DB
	id                        int64
	assets, findings, records bool
	prepare                   func(TaskDeletePreparation) error
}
type taskRegistryDeletePlan struct {
	command       taskRegistryDeletion
	transaction   *sql.Tx
	explorationID int64
}

func (deletion taskRegistryDeletion) run() (TaskDeleteResult, error) {
	var result TaskDeleteResult
	tx, err := deletion.database.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var expID int64
	if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1 FOR UPDATE`, deletion.id).Scan(&expID); err != nil {
		if err == sql.ErrNoRows {
			return result, nil // already gone
		}
		return result, err
	}
	plan := taskRegistryDeletePlan{command: deletion, transaction: tx, explorationID: expID}
	if err := plan.lockAndPrepare(); err != nil {
		return result, err
	}
	result, err = plan.apply()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (plan taskRegistryDeletePlan) lockAndPrepare() error {
	if plan.command.assets || plan.command.prepare != nil {
		if _, err := plan.transaction.Exec(`LOCK TABLE assets, exploration_anchors IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
	}
	if plan.command.prepare != nil {
		hosts, err := hostsForTaskDeletion(plan.transaction, plan.command.id, plan.explorationID)
		if err != nil {
			return err
		}
		if err := plan.command.prepare(TaskDeletePreparation{
			ExplorationID: plan.explorationID,
			TrafficHosts:  hosts,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (plan taskRegistryDeletePlan) apply() (TaskDeleteResult, error) {
	var result TaskDeleteResult
	if plan.command.assets {
		// Ownership is the union of explicit task_ids and this exploration's anchors.
		// An asset is deletable only when no other live task references it through
		// either mechanism. This covers legacy anchor-only seeds without destroying
		// evidence anchored by another task.
		res, err := plan.transaction.Exec(`
WITH candidate_assets AS (
  SELECT id FROM assets WHERE $1 = ANY(task_ids)
  UNION
  SELECT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes n ON n.id=ea.node_id
  WHERE n.exploration_id=$2
),
deletable AS (
  SELECT a.id
  FROM assets a
  JOIN candidate_assets c ON c.id=a.id
  WHERE NOT EXISTS (
    SELECT 1 FROM tasks t
    WHERE t.id<>$1 AND t.deleted_at IS NULL AND t.id=ANY(a.task_ids)
  ) AND NOT EXISTS (
    SELECT 1
    FROM exploration_anchors ea
    JOIN exploration_nodes n ON n.id=ea.node_id
    JOIN tasks t ON t.exploration_id=n.exploration_id
    WHERE ea.asset_id=a.id AND t.id<>$1 AND t.deleted_at IS NULL
  )
)
DELETE FROM assets a USING deletable d WHERE a.id=d.id`, plan.command.id, plan.explorationID)
		if err != nil {
			return result, err
		}
		result.AssetsDeleted, _ = res.RowsAffected()

		res, err = plan.transaction.Exec(`UPDATE assets SET task_ids = array_remove(task_ids, $1) WHERE $1 = ANY(task_ids)`, plan.command.id)
		if err != nil {
			return result, err
		}
		result.AssetsDetached, _ = res.RowsAffected()
	}
	if plan.command.findings {
		res, err := plan.transaction.Exec(`DELETE FROM findings WHERE task_id = $1`, plan.command.id)
		if err != nil {
			return result, err
		}
		result.FindingsDeleted, _ = res.RowsAffected()
	}
	if plan.command.records {
		res, err := plan.transaction.Exec(`DELETE FROM llm_records WHERE COALESCE(task_id,'')=$1`, strconv.FormatInt(plan.command.id, 10))
		if err != nil {
			return result, err
		}
		result.LLMRecordsDeleted, _ = res.RowsAffected()
	}
	// llm_usage (the token metering ledger) is intentionally NOT deleted with the
	// task — it is kept as historical accounting even after the task is gone.
	if _, err := plan.transaction.Exec(`DELETE FROM tasks WHERE id=$1`, plan.command.id); err != nil {
		return result, err
	}
	if _, err := plan.transaction.Exec(`DELETE FROM explorations WHERE id=$1`, plan.explorationID); err != nil {
		return result, err
	}
	return result, nil
}
