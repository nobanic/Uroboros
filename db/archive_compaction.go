package db

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type archiveCompaction struct {
	database       *DB
	archiveID      int64
	snapshot       *TaskArchiveSnapshot
	path, checksum string
	original, size int64
}

func (c archiveCompaction) apply() error {
	if c.snapshot == nil || c.snapshot.FormatVersion != TaskArchiveFormatVersion {
		return ErrTaskArchiveFormatMismatch
	}
	counts, err := json.Marshal(c.snapshot.DataCounts)
	if err != nil {
		return err
	}
	stats, err := json.Marshal(c.snapshot.AggregateStats)
	if err != nil {
		return err
	}
	mutation, err := c.database.beginArchiveMutation(c.archiveID)
	if err != nil {
		return err
	}
	defer mutation.tx.Rollback() //nolint:errcheck
	if err := mutation.requireSnapshot(c.snapshot, Archiving, "archive"); err != nil {
		return err
	}
	if err := lockAndValidateBinaryArchiveSnapshot(mutation.tx, mutation.taskID, c.snapshot.DataCounts); err != nil {
		return err
	}
	if err := mutation.rejectDependent(`SELECT child.id FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
WHERE relation.source_task_id=$1 LIMIT 1`, ErrTaskArchiveDependent, mutation.taskID); err != nil {
		return err
	}
	if err := mutation.exec(`LOCK TABLE assets, exploration_anchors IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if err := mutation.compactTaskHistory(); err != nil {
		return err
	}
	if err := mutation.moveBinaryReferences(); err != nil {
		return err
	}
	if err := mutation.compactTaskGraph(); err != nil {
		return err
	}
	if err := mutation.detachArchiveAssets(c.snapshot.ExclusiveAssetIDs); err != nil {
		return err
	}
	if err := mutation.execute(
		archiveStatement{`UPDATE explorations SET description='',goal='',status='open' WHERE id=$1`, []any{mutation.exploration}},
		archiveStatement{`UPDATE tasks SET
 name='',category_id=NULL,description='',goal='',paused=true,queued=false,queued_at=NULL,queue_mode='',
 llm_profile_id=NULL,active_llm_profile_id=NULL,llm_chain_revision=llm_chain_revision+1,
 company_id=NULL,parent_ref=NULL,timeout_seconds=0,coverage_enabled=true,pinned_at=NULL,
 first_run_at=NULL,deadline_at=NULL,archived_at=now(),deleted_at=now()
WHERE id=$1`, []any{mutation.taskID}},
		archiveStatement{`UPDATE task_archives SET
	 state=$2,phase='ready',progress=100,error='',archive_path=$3,sha256=$4,
	 original_size=$5,compressed_size=$6,data_counts=$7,aggregate_stats=$8,
	 format_version=$9,archived_at=now(),warnings='[]'
WHERE id=$1`, []any{c.archiveID, ArchiveReady, c.path, c.checksum, c.original, c.size, string(counts), string(stats), c.snapshot.FormatVersion}},
	); err != nil {
		return err
	}
	return mutation.tx.Commit()
}

func (m *archiveMutation) compactTaskHistory() error {
	key := strconv.FormatInt(m.taskID, 10)
	if err := m.execute(
		archiveStatement{`DELETE FROM intercept_pending WHERE COALESCE(task_id,'')=$1`, []any{key}},
		archiveStatement{`DELETE FROM llm_records WHERE COALESCE(task_id,'')=$1`, []any{key}},
		archiveStatement{`DELETE FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2`, []any{key, m.exploration}},
	); err != nil {
		return err
	}
	for _, table := range []string{"skill_usage", "tool_usage"} {
		if err := m.exec(`DELETE FROM `+table+` WHERE task_id=$1 OR exploration_id=$2`, m.taskID, m.exploration); err != nil {
			return err
		}
	}
	return m.execute(
		archiveStatement{`DELETE FROM side_question_sessions WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM research_artifacts WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM research_egress_grants WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM research_disclosure_grants WHERE task_id=$1`, []any{m.taskID}},
	)
}

func (m *archiveMutation) moveBinaryReferences() error {
	var unsafe int64
	if err := m.tx.QueryRow(`SELECT count(*) FROM binary_artifacts artifact
LEFT JOIN binary_artifact_refs reference ON reference.artifact_id=artifact.artifact_id
WHERE artifact.task_id=$1 AND (reference.artifact_id IS NULL OR reference.state<>'active')`, m.taskID).Scan(&unsafe); err != nil {
		return err
	}
	if unsafe != 0 {
		return fmt.Errorf("%w: %d artifact(s)", ErrTaskArchiveBinaryRefs, unsafe)
	}
	return m.execute(
		archiveStatement{`INSERT INTO binary_archive_refs(
archive_id,artifact_id,task_id,blob_sha256,broker_ref_id,state,last_error)
SELECT $1::bigint,artifact.artifact_id,artifact.task_id,artifact.blob_sha256,
       $3||artifact.artifact_id,'pending',''
FROM binary_artifacts artifact WHERE artifact.task_id=$2
ON CONFLICT (archive_id,artifact_id) DO UPDATE SET
 task_id=EXCLUDED.task_id,blob_sha256=EXCLUDED.blob_sha256,
 broker_ref_id=EXCLUDED.broker_ref_id,state='pending',last_error=''`, []any{m.archiveID, m.taskID, fmt.Sprintf("cold:%d:", m.archiveID)}},
		archiveStatement{`DELETE FROM binary_artifacts WHERE task_id=$1`, []any{m.taskID}},
	)
}

func (m *archiveMutation) compactTaskGraph() error {
	return m.execute(
		archiveStatement{`DELETE FROM findings WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM discovery_reviews WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM katana_jobs WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM arsenal_jobs WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM archive_provider_grants WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM task_relations WHERE task_id=$1 OR source_task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM task_asset_links WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM task_llm_profiles WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM task_scope WHERE task_id=$1`, []any{m.taskID}},
		archiveStatement{`DELETE FROM task_constraints WHERE exploration_id=$1`, []any{m.exploration}},
		archiveStatement{`DELETE FROM activity WHERE exploration_id=$1`, []any{m.exploration}},
		archiveStatement{`DELETE FROM exploration_nodes WHERE exploration_id=$1`, []any{m.exploration}},
	)
}

func (m *archiveMutation) detachArchiveAssets(exclusive []int64) error {
	if err := m.exec(`UPDATE assets SET task_ids=array_remove(task_ids,$1) WHERE $1=ANY(task_ids)`, m.taskID); err != nil {
		return err
	}
	if len(exclusive) == 0 {
		return nil
	}
	return m.exec(`DELETE FROM assets asset
WHERE asset.id=ANY($2::bigint[]) AND asset.company_id IS NULL
AND NOT EXISTS (SELECT 1 FROM tasks task WHERE task.id<>$1 AND task.deleted_at IS NULL AND task.id=ANY(asset.task_ids))
AND NOT EXISTS (
 SELECT 1 FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id
 JOIN tasks task ON task.exploration_id=node.exploration_id
 WHERE anchor.asset_id=asset.id AND task.id<>$1 AND task.deleted_at IS NULL
)`, m.taskID, exclusive)
}
