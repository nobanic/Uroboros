package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CompleteTaskArchive performs the hot-store compaction only after the external
// package has been fully written and checksummed. The task/exploration rows remain
// as minimal ID stubs; all heavyweight task-owned rows move into the package.
func (d *DB) CompleteTaskArchive(
	archiveID int64,
	snapshot *TaskArchiveSnapshot,
	archivePath, sha256 string,
	originalSize, compressedSize int64,
) error {
	return (archiveCompaction{database: d, archiveID: archiveID, snapshot: snapshot, path: archivePath, checksum: sha256, original: originalSize, size: compressedSize}).apply()
}

// lockAndValidateBinaryArchiveSnapshot closes the interval between snapshot
// creation and hot-row deletion. Parent-row locks also block new origin/job/
// observation inserts through their foreign keys until this transaction ends.
func lockAndValidateBinaryArchiveSnapshot(tx *sql.Tx, taskID int64, expected map[string]int64) error {
	tables := []struct {
		name string
		key  string
	}{
		{"binary_artifacts", "artifact_id"},
		{"binary_artifact_origins", "id"},
		{"binary_triage_jobs", "job_id"},
		{"binary_observations", "id"},
		{"research_disclosure_grants", "id"},
		{"research_egress_grants", "id"},
		{"research_artifacts", "id"},
	}
	for _, table := range tables {
		rows, err := tx.Query(`SELECT `+table.key+` FROM `+table.name+` WHERE task_id=$1 FOR UPDATE`, taskID)
		if err != nil {
			return err
		}
		var count int64
		for rows.Next() {
			count++
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if rowErr != nil {
			return rowErr
		}
		if closeErr != nil {
			return closeErr
		}
		if count != expected[table.name] {
			return fmt.Errorf("%w: %s changed after snapshot (have %d, snapshot %d)", ErrTaskArchiveState, table.name, count, expected[table.name])
		}
	}
	return nil
}

// RestoreTaskArchive restores PostgreSQL rows from a verified manifest. It is
// idempotent for accounting/traffic retry scenarios and returns non-fatal
// warnings for global objects that intentionally are not recreated.
func (d *DB) RestoreTaskArchive(archiveID int64, snapshot *TaskArchiveSnapshot, remainingTimeoutSeconds int64) ([]string, error) {
	return d.restoreTaskArchive(archiveID, snapshot, remainingTimeoutSeconds, nil)
}

// RestoreTaskArchiveWithLLMRecords restores a v2 package whose heavyweight LLM
// record history is stored as a sequence of JSON objects outside manifest.json.
func (d *DB) RestoreTaskArchiveWithLLMRecords(
	archiveID int64,
	snapshot *TaskArchiveSnapshot,
	remainingTimeoutSeconds int64,
	llmRecords io.Reader,
) ([]string, error) {
	if llmRecords == nil {
		return nil, errors.New("nil streamed LLM record reader")
	}
	return d.restoreTaskArchive(archiveID, snapshot, remainingTimeoutSeconds, llmRecords)
}

func (d *DB) restoreTaskArchive(
	archiveID int64,
	snapshot *TaskArchiveSnapshot,
	remainingTimeoutSeconds int64,
	llmRecords io.Reader,
) ([]string, error) {
	return (archiveRestoration{database: d, archiveID: archiveID, snapshot: snapshot, remaining: remainingTimeoutSeconds, records: llmRecords}).apply()
}

func restoreExplorationStub(tx *sql.Tx, raw json.RawMessage, expID int64) error {
	_, err := tx.Exec(`UPDATE explorations current SET
 description=archived.description,goal=archived.goal,status=archived.status,
 created_at=archived.created_at,updated_at=archived.updated_at
FROM json_populate_record(NULL::explorations,$2::json) archived
WHERE current.id=$1 AND archived.id=$1`, expID, string(firstArchiveRow(raw)))
	return err
}

func restoreTaskStub(tx *sql.Tx, row map[string]any, taskID, remaining int64) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE tasks current SET
 name=archived.name,category_id=archived.category_id,description=archived.description,goal=archived.goal,
 status=archived.status,paused=archived.paused,queued=false,queued_at=NULL,queue_mode='',
 llm_profile_id=archived.llm_profile_id,active_llm_profile_id=archived.active_llm_profile_id,
 llm_chain_revision=archived.llm_chain_revision,company_id=archived.company_id,parent_ref=archived.parent_ref,
 timeout_seconds=archived.timeout_seconds,plan_heartbeat_seconds=archived.plan_heartbeat_seconds,
 coverage_enabled=archived.coverage_enabled,pinned_at=archived.pinned_at,first_run_at=archived.first_run_at,
 skip_initial_katana=COALESCE(archived.skip_initial_katana,false),initial_scout=archived.initial_scout,
 deadline_at=CASE WHEN archived.paused AND $3>0 THEN now()+make_interval(secs=>$3::double precision)
                  ELSE archived.deadline_at END,
 deleted_at=NULL,archived_at=NULL,completed_at=archived.completed_at,
 created_at=archived.created_at,updated_at=archived.updated_at
FROM json_populate_record(NULL::tasks,$2::json) archived
WHERE current.id=$1 AND archived.id=$1`, taskID, string(raw), remaining)
	return err
}

func firstArchiveRow(raw json.RawMessage) json.RawMessage {
	return archiveInput(raw).first()
}

func decodeArchiveRows(raw json.RawMessage) ([]map[string]any, error) {
	return archiveInput(raw).objects()
}

func rowExists(tx *sql.Tx, table string, id int64) bool {
	if table != "task_categories" && table != "llm_profiles" && table != "companies" && table != "tasks" && table != "assets" {
		return false
	}
	var exists bool
	_ = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1)`, id).Scan(&exists)
	return exists
}

func restoreBinaryArtifactRows(tx *sql.Tx, archiveID, taskID int64, tables map[string]json.RawMessage) error {
	artifacts, err := decodeArchiveRows(tables["binary_artifacts"])
	if err != nil {
		return fmt.Errorf("restore binary_artifacts: %w", err)
	}
	seen := make(map[string]struct{}, len(artifacts))
	for _, row := range artifacts {
		artifactID, _ := row["artifact_id"].(string)
		blobHash, _ := row["blob_sha256"].(string)
		rowTaskID, ok := jsonInt64(row["task_id"])
		if !ok || rowTaskID != taskID || !binaryArtifactIDPattern.MatchString(artifactID) || !binarySHA256Pattern.MatchString(blobHash) {
			return errors.New("restore binary_artifacts: invalid task, artifact id, or blob hash")
		}
		if _, duplicate := seen[artifactID]; duplicate {
			return fmt.Errorf("restore binary_artifacts: duplicate artifact %s", artifactID)
		}
		seen[artifactID] = struct{}{}
		var retained bool
		if err := tx.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM binary_archive_refs reference
 JOIN binary_blobs blob ON blob.sha256=reference.blob_sha256
 WHERE reference.archive_id=$1 AND reference.artifact_id=$2
   AND reference.task_id=$3 AND reference.blob_sha256=$4
)`, archiveID, artifactID, taskID, blobHash).Scan(&retained); err != nil {
			return err
		}
		if !retained {
			return fmt.Errorf("restore binary_artifacts: cold reference missing for %s", artifactID)
		}
	}
	for _, table := range []string{"binary_artifacts", "binary_artifact_origins", "binary_triage_jobs", "binary_observations"} {
		if err := insertArchiveRows(tx, table, tables[table]); err != nil {
			return fmt.Errorf("restore %s: %w", table, err)
		}
	}
	result, err := tx.Exec(`INSERT INTO binary_artifact_refs(
artifact_id,task_id,blob_sha256,broker_ref_id,receipt_id,state,last_error)
SELECT artifact_id,task_id,blob_sha256,'hot:'||artifact_id,'','pending',''
FROM binary_artifacts WHERE task_id=$1`, taskID)
	if err != nil {
		return err
	}
	inserted, _ := result.RowsAffected()
	if inserted != int64(len(artifacts)) {
		return fmt.Errorf("restore binary artifacts: inserted %d hot references for %d artifacts", inserted, len(artifacts))
	}
	return nil
}

func insertArchiveRows(tx *sql.Tx, table string, raw json.RawMessage) error {
	return restoreArchiveInput(tx, table, archiveInput(raw))
}

func insertArchiveJSONSequenceRows(tx *sql.Tx, table string, reader io.Reader) (int64, error) {
	return restoreArchiveSequence(tx, table, reader)
}

func restoreArchiveAssets(tx *sql.Tx, taskID int64, raw json.RawMessage) (map[int64]int64, []string, error) {
	return (archiveAssetRestore{tx: tx, taskID: taskID}).apply(raw)
}

func mergeJSONTaskID(value any, taskID int64) []int64 {
	out := []int64{}
	seen := map[int64]bool{}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if id, ok := jsonInt64(item); ok && id > 0 && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if !seen[taskID] {
		out = append(out, taskID)
	}
	return out
}

func findArchiveAssetNaturalID(tx *sql.Tx, row map[string]any) (int64, bool, error) {
	return lookupArchivedAsset(tx, row)
}

func remapArchiveAssetReferences(tables map[string]json.RawMessage, mapping map[int64]int64) (map[string]json.RawMessage, error) {
	return remapArchivedAssetRows(tables, mapping)
}

func restoreTaskRelations(tx *sql.Tx, taskID int64, raw json.RawMessage) ([]string, error) {
	rows, err := readArchiveReferences(tx, raw)
	if err != nil {
		return nil, err
	}
	return rows.relations(taskID)
}

func restoreTaskScopes(tx *sql.Tx, raw json.RawMessage) ([]string, error) {
	rows, err := readArchiveReferences(tx, raw)
	if err != nil {
		return nil, err
	}
	return rows.scopes()
}

func restoreTaskLLMProfiles(tx *sql.Tx, raw json.RawMessage) ([]string, error) {
	rows, err := readArchiveReferences(tx, raw)
	if err != nil {
		return nil, err
	}
	return rows.profiles()
}

func restoreInterceptRows(tx *sql.Tx, raw json.RawMessage) error {
	rows, err := readArchiveReferences(tx, raw)
	if err != nil {
		return err
	}
	return rows.approvals()
}

func liveTaskExists(tx *sql.Tx, id int64) bool {
	var exists bool
	_ = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists)
	return exists
}

func nilIfEmptyString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// CompleteTaskArchiveRestore removes compact metadata after every external
// component has been verified and the package has been consumed.
func (d *DB) CompleteTaskArchiveRestore(archiveID int64) error {
	res, err := d.Exec(`DELETE FROM task_archives archive USING tasks task
WHERE archive.id=$1 AND archive.task_id=task.id AND task.deleted_at IS NULL AND task.archived_at IS NULL
AND NOT EXISTS (
 SELECT 1 FROM binary_artifacts artifact
 LEFT JOIN binary_artifact_refs reference ON reference.artifact_id=artifact.artifact_id
 WHERE artifact.task_id=task.id AND (reference.artifact_id IS NULL OR reference.state<>'active')
)`, archiveID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrTaskArchiveState
	}
	return nil
}

// DeleteTaskArchiveStub permanently removes the cold task after its package has
// been staged for deletion. Dependency protection is rechecked transactionally.
func (d *DB) DeleteTaskArchiveStub(archiveID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	var taskID, expID int64
	var state string
	if err := tx.QueryRow(`SELECT archive.task_id,task.exploration_id,archive.state
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id
WHERE archive.id=$1 FOR UPDATE OF archive,task`, archiveID).Scan(&taskID, &expID, &state); err != nil {
		return err
	}
	if state != Deleting {
		return ErrTaskArchiveState
	}
	var dependent int64
	err = tx.QueryRow(`SELECT task_id FROM task_archives WHERE id<>$1 AND $2=ANY(source_task_ids) LIMIT 1`, archiveID, taskID).Scan(&dependent)
	if err == nil {
		return fmt.Errorf("%w: task %d", ErrTaskArchiveDeleteBlocked, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id=$1 AND archived_at IS NOT NULL`, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM explorations WHERE id=$1`, expID); err != nil {
		return err
	}
	return tx.Commit()
}

// ArchivedAggregateStats returns compact summaries used by global dashboards so
// cold data does not disappear from historical totals.
func (d *DB) ArchivedAggregateStats() ([]json.RawMessage, error) {
	rows, err := d.Query(`SELECT archive.aggregate_stats
FROM task_archives archive
JOIN tasks task ON task.id=archive.task_id
WHERE task.archived_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	return collectRowValues(rows, out, func() (json.RawMessage, error) {
		var raw json.RawMessage
		err := rows.Scan(&raw)
		return raw, err
	})
}
