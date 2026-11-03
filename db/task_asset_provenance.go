package db

import (
	"database/sql"
	"fmt"
)

// taskAssetProvenance owns metadata writes and historical target projections.
// Only set honors a caller-provided transaction; reads retain committed DB views.
type taskAssetProvenance struct {
	store  *AssetStore
	taskID int64
}

func (provenance taskAssetProvenance) set(assetID int64, source, summary string, sourceNodeID *int64) error {
	if provenance.taskID <= 0 || assetID <= 0 {
		return fmt.Errorf("%w: task and asset ids must be positive", ErrTaskAssetInvalid)
	}
	source, summary, err := normalizeTaskAssetSource(source, summary)
	if err != nil {
		return err
	}
	query := `
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary, source_node_id)
SELECT task.id, asset.id, $3, $4, $5
FROM tasks task
JOIN assets asset ON asset.id=$2 AND task.id=ANY(asset.task_ids)
WHERE task.id=$1 AND task.deleted_at IS NULL
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source=EXCLUDED.source,
    source_summary=EXCLUDED.source_summary,
    source_node_id=COALESCE(EXCLUDED.source_node_id, task_asset_links.source_node_id)`
	var result sql.Result
	if provenance.store.tx != nil {
		result, err = provenance.store.tx.Exec(query, provenance.taskID, assetID, source, summary, sourceNodeID)
	} else {
		result, err = provenance.store.db.Exec(query, provenance.taskID, assetID, source, summary, sourceNodeID)
	}
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: task or asset association does not exist", ErrTaskAssetInvalid)
	}
	return nil
}
func (provenance taskAssetProvenance) hydrate(assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(assets))
	byID := make(map[int64]*Asset, len(assets))
	for _, asset := range assets {
		ids = append(ids, asset.ID)
		byID[asset.ID] = asset
	}
	rows, err := provenance.store.db.Query(`
SELECT asset_id, source, source_summary, source_node_id
FROM task_asset_links
WHERE task_id=$1 AND asset_id=ANY($2::bigint[])`, provenance.taskID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID int64
		var source, summary string
		var sourceNodeID sql.NullInt64
		if err := rows.Scan(&assetID, &source, &summary, &sourceNodeID); err != nil {
			return err
		}
		if asset := byID[assetID]; asset != nil {
			asset.TaskSource = source
			asset.TaskSourceSummary = summary
			if sourceNodeID.Valid {
				id := sourceNodeID.Int64
				asset.TaskSourceNodeID = &id
			}
		}
	}
	return rows.Err()
}
func (provenance taskAssetProvenance) intents() ([]IntentAsset, error) {
	return collectQueryState(queriedRows(provenance.store.db.Query(`
WITH context AS (
    SELECT task.id AS task_id, task.exploration_id, false AS inherited
    FROM tasks task
    WHERE task.id=$1 AND task.deleted_at IS NULL
    UNION ALL
    SELECT source.id, source.exploration_id, true
    FROM task_relations relation
    JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
    WHERE relation.task_id=$1
)
SELECT intent.id, asset.id, asset.type,
       CASE asset.type
         WHEN 'root_domain' THEN COALESCE(asset.domain,'')
         WHEN 'subdomain' THEN COALESCE(asset.domain,'')
         WHEN 'ip' THEN COALESCE(asset.ip,'')
         WHEN 'app' THEN COALESCE(asset.app_name,'')
		 WHEN 'service' THEN COALESCE(NULLIF(asset.url,''), NULLIF(concat_ws(':', COALESCE(NULLIF(asset.domain,''), NULLIF(asset.ip,'')), asset.port::text),''), NULLIF(asset.service_name,''), '#' || asset.id::text)
         WHEN 'endpoint' THEN COALESCE(NULLIF(asset.url,''), '#' || asset.id::text)
         ELSE '#' || asset.id::text
       END,
       COALESCE(link.source,'anchor'),
       COALESCE(NULLIF(link.source_summary,''), 'intent anchored this asset on the blackboard'),
       link.source_node_id, context.task_id, context.inherited
FROM context
JOIN exploration_nodes intent ON intent.exploration_id=context.exploration_id AND intent.kind='intent'
JOIN exploration_anchors anchor ON anchor.node_id=intent.id
JOIN assets asset ON asset.id=anchor.asset_id
LEFT JOIN task_asset_links link ON link.task_id=context.task_id AND link.asset_id=asset.id
WHERE NOT context.inherited OR intent.state IN ('done','blocked','exhausted','stopped')
ORDER BY context.inherited, intent.id DESC, asset.id`, provenance.taskID)), func() []IntentAsset {
		return []IntentAsset{}
	}, func(queryRow rowDecoder, queryState *[]IntentAsset) error {
		var asset IntentAsset
		var sourceNodeID sql.NullInt64
		if err := queryRow.Scan(&asset.IntentID, &asset.AssetID, &asset.Type, &asset.Label, &asset.Source, &asset.SourceSummary, &sourceNodeID, &asset.SourceTaskID, &asset.Inherited); err != nil {
			return err
		}
		if sourceNodeID.Valid {
			id := sourceNodeID.Int64
			asset.SourceNodeID = &id
		}
		*queryState = append(*queryState, asset)
		return nil
	})
}
