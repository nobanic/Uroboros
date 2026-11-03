package db

import (
	"database/sql"
	"fmt"
)

// taskAssetMembership owns an individual task's association commands. Attach
// owns its transaction; detach intentionally uses the database directly.
type taskAssetMembership struct {
	store  *AssetStore
	taskID int64
}

func (membership taskAssetMembership) attach(assetIDs []int64, sourceSummary string) (TaskAssetMutation, error) {
	var mutation TaskAssetMutation
	assetIDs, err := normalizeTaskAssetIDs(assetIDs)
	if err != nil {
		return mutation, err
	}
	_, sourceSummary, err = normalizeTaskAssetSource("manual", sourceSummary)
	if err != nil {
		return mutation, err
	}
	if sourceSummary == "" {
		return mutation, fmt.Errorf("%w: source_summary is required", ErrTaskAssetInvalid)
	}
	mutation.Requested = len(assetIDs)
	tx, err := membership.store.db.Begin()
	if err != nil {
		return mutation, err
	}
	defer tx.Rollback() //nolint:errcheck

	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, membership.taskID).Scan(&taskExists); err != nil {
		return mutation, err
	}
	if !taskExists {
		return mutation, ErrTaskAssetTaskNotFound
	}
	var found, existing int
	if err := tx.QueryRow(`
SELECT count(*), count(*) FILTER (WHERE $1=ANY(task_ids))
FROM assets WHERE id=ANY($2::bigint[])`, membership.taskID, assetIDs).Scan(&found, &existing); err != nil {
		return mutation, err
	}
	if found != len(assetIDs) {
		return mutation, ErrTaskAssetAssetNotFound
	}
	// Order matters: this UPDATE fires trg_assets_task_links, which creates the
	// link rows with the generic source='system'. The INSERT below must stay
	// after it so the operator-authored 'manual' provenance wins; swapping the
	// two statements silently degrades every manual attach back to 'system'.
	if _, err := tx.Exec(`
UPDATE assets
SET task_ids=CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END
WHERE id=ANY($2::bigint[])`, membership.taskID, assetIDs); err != nil {
		return mutation, err
	}
	if _, err := tx.Exec(`
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT $1, id, 'manual', $3 FROM assets WHERE id=ANY($2::bigint[])
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source='manual', source_summary=EXCLUDED.source_summary, source_node_id=NULL`,
		membership.taskID, assetIDs, sourceSummary); err != nil {
		return mutation, err
	}
	mutation.Existing = existing
	mutation.Attached = len(assetIDs) - existing
	return mutation, tx.Commit()
}
func (membership taskAssetMembership) detach(assetID int64) (bool, error) {
	if membership.taskID <= 0 || assetID <= 0 {
		return false, fmt.Errorf("%w: task and asset ids must be positive", ErrTaskAssetInvalid)
	}
	var detachedID int64
	err := membership.store.db.QueryRow(`
UPDATE assets SET task_ids=array_remove(task_ids,$1)
WHERE id=$2 AND $1=ANY(task_ids)
RETURNING id`, membership.taskID, assetID).Scan(&detachedID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return detachedID == assetID, nil
}
