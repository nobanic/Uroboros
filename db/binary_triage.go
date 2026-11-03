package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const BinaryTriageProfileRevision = "standard-v1"

var ErrBinaryTriageState = errors.New("invalid binary triage job state")

type BinaryTriageJob struct {
	JobID                   string          `json:"job_id"`
	TaskID                  int64           `json:"task_id"`
	ArtifactID              string          `json:"artifact_id"`
	ProfileRevision         string          `json:"profile_revision"`
	Fingerprint             string          `json:"fingerprint"`
	WorkerImageDigest       string          `json:"worker_image_digest"`
	ToolRulePins            json.RawMessage `json:"tool_rule_pins"`
	LimitProfile            json.RawMessage `json:"limit_profile"`
	Status                  string          `json:"status"`
	Progress                int             `json:"progress"`
	LeaseNonce              string          `json:"-"`
	HeartbeatAt             *time.Time      `json:"heartbeat_at,omitempty"`
	ExitClass               string          `json:"exit_class,omitempty"`
	ErrorClass              string          `json:"error_class,omitempty"`
	Error                   string          `json:"error,omitempty"`
	RawResultHash           string          `json:"raw_result_hash,omitempty"`
	CanonicalProjectionHash string          `json:"canonical_projection_hash,omitempty"`
	StartedAt               *time.Time      `json:"started_at,omitempty"`
	FinishedAt              *time.Time      `json:"finished_at,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type BinaryTriageJobCreate struct {
	TaskID            int64
	ArtifactID        string
	Fingerprint       string
	WorkerImageDigest string
	ToolRulePins      any
	LimitProfile      any
	ResourceGated     bool
}

type BinaryTriageQueueItem struct {
	TaskID      int64
	ArtifactID  string
	JobID       string
	ReferenceID string
	Fingerprint string
}

type BinaryObservation struct {
	ID               int64           `json:"id"`
	JobID            string          `json:"job_id"`
	TaskID           int64           `json:"task_id"`
	ArtifactID       string          `json:"artifact_id"`
	Analyzer         string          `json:"analyzer"`
	Kind             string          `json:"kind"`
	StableKey        string          `json:"stable_key"`
	Authority        string          `json:"authority"`
	Address          string          `json:"address,omitempty"`
	FileOffset       *int64          `json:"file_offset,omitempty"`
	Record           json.RawMessage `json:"record"`
	ToolRuleIdentity json.RawMessage `json:"tool_rule_identity"`
	Truncated        bool            `json:"truncated"`
	CreatedAt        time.Time       `json:"created_at"`
}

type BinaryObservationInput struct {
	Analyzer         string
	Kind             string
	StableKey        string
	Authority        string
	Address          string
	FileOffset       *int64
	Record           json.RawMessage
	ToolRuleIdentity json.RawMessage
	Truncated        bool
}

type BinaryTriageFinish struct {
	Status                  string
	ExitClass               string
	ErrorClass              string
	Error                   string
	RawResultHash           string
	CanonicalProjectionHash string
	Observations            []BinaryObservationInput
	StartedAt               time.Time
	FinishedAt              time.Time
}

const binaryArtifactSelect = `SELECT artifact.artifact_id,artifact.task_id,artifact.blob_sha256,blob.byte_size,
artifact.state,artifact.quarantine_state,artifact.detected_kind,artifact.binary_format,artifact.architecture,artifact.bitness,
artifact.endianness,artifact.retention_class,artifact.execution_allowed,artifact.created_at,artifact.updated_at
FROM binary_artifacts artifact JOIN binary_blobs blob ON blob.sha256=artifact.blob_sha256`

const binaryTriageJobSelect = `SELECT job_id,task_id,artifact_id,profile_revision,fingerprint,worker_image_digest,
tool_rule_pins,limit_profile,status,progress,lease_nonce,heartbeat_at,exit_class,error_class,error,
raw_result_hash,canonical_projection_hash,started_at,finished_at,created_at,updated_at FROM binary_triage_jobs`

func scanBinaryTriageJob(scanner interface{ Scan(...any) error }) (*BinaryTriageJob, error) {
	var job BinaryTriageJob
	if err := scanner.Scan(&job.JobID, &job.TaskID, &job.ArtifactID, &job.ProfileRevision, &job.Fingerprint,
		&job.WorkerImageDigest, &job.ToolRulePins, &job.LimitProfile, &job.Status, &job.Progress,
		&job.LeaseNonce, &job.HeartbeatAt, &job.ExitClass, &job.ErrorClass, &job.Error,
		&job.RawResultHash, &job.CanonicalProjectionHash, &job.StartedAt, &job.FinishedAt,
		&job.CreatedAt, &job.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

func (database *DB) BinaryArtifactForTask(ctx context.Context, taskID int64, artifactID string) (*BinaryArtifact, error) {
	if taskID <= 0 || !binaryArtifactIDPattern.MatchString(artifactID) {
		return nil, ErrBinaryArtifactInput
	}
	artifact, err := scanBinaryArtifact(database.QueryRowContext(ctx, binaryArtifactSelect+`
WHERE artifact.task_id=$1 AND artifact.artifact_id=$2`, taskID, artifactID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return artifact, err
}

func (database *DB) ListBinaryArtifacts(ctx context.Context, taskID int64, limit, offset int) ([]BinaryArtifact, error) {
	if taskID <= 0 {
		return nil, ErrBinaryArtifactInput
	}
	limit, offset = boundedBinaryPage(limit, offset)
	rows, err := database.QueryContext(ctx, binaryArtifactSelect+`
WHERE artifact.task_id=$1 ORDER BY artifact.created_at DESC,artifact.artifact_id LIMIT $2 OFFSET $3`, taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BinaryArtifact, 0)
	for rows.Next() {
		item, err := scanBinaryArtifact(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func boundedBinaryPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// CreateBinaryTriageJob serializes on the task-owned artifact so concurrent
// origins for the same body cannot enqueue duplicate active analysis. A
// resource-gated record is durable metadata only and is never worker-claimable.
func (database *DB) CreateBinaryTriageJob(ctx context.Context, create BinaryTriageJobCreate) (*BinaryTriageJob, bool, error) {
	if create.TaskID <= 0 || !binaryArtifactIDPattern.MatchString(create.ArtifactID) ||
		!binarySHA256Pattern.MatchString(create.Fingerprint) || len(create.WorkerImageDigest) > 256 {
		return nil, false, ErrBinaryArtifactInput
	}
	pins, err := binaryJSONObject(create.ToolRulePins)
	if err != nil {
		return nil, false, fmt.Errorf("tool/rule pins: %w", err)
	}
	limits, err := binaryJSONObject(create.LimitProfile)
	if err != nil {
		return nil, false, fmt.Errorf("limit profile: %w", err)
	}
	if len(pins) > 65536 || len(limits) > 65536 {
		return nil, false, ErrBinaryArtifactInput
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var referenceState string
	err = tx.QueryRowContext(ctx, `SELECT reference.state
FROM binary_artifacts artifact
JOIN tasks task ON task.id=artifact.task_id
JOIN binary_artifact_refs reference ON reference.artifact_id=artifact.artifact_id
WHERE artifact.task_id=$1 AND artifact.artifact_id=$2
  AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued AND task.status IN ('created','running')
FOR UPDATE OF artifact`, create.TaskID, create.ArtifactID).Scan(&referenceState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("%w: artifact task is not active", ErrBinaryArtifactInput)
	}
	if err != nil {
		return nil, false, err
	}
	if referenceState != "active" {
		return nil, false, fmt.Errorf("%w: artifact broker reference is not active", ErrBinaryTriageState)
	}
	existing, err := scanBinaryTriageJob(tx.QueryRowContext(ctx, binaryTriageJobSelect+`
 WHERE task_id=$1 AND artifact_id=$2 AND fingerprint=$3
   AND status IN ('queued','running','completed','partial','unsupported','resource_gated')
 ORDER BY CASE status WHEN 'completed' THEN 0 WHEN 'partial' THEN 1 WHEN 'unsupported' THEN 2
   WHEN 'running' THEN 3 WHEN 'queued' THEN 4 ELSE 5 END,created_at DESC LIMIT 1`,
		create.TaskID, create.ArtifactID, create.Fingerprint))
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	jobID, err := newBinaryOpaqueID("binary-job-")
	if err != nil {
		return nil, false, err
	}
	status := "queued"
	if create.ResourceGated {
		status = "resource_gated"
	}
	job, err := scanBinaryTriageJob(tx.QueryRowContext(ctx, `INSERT INTO binary_triage_jobs(
job_id,task_id,artifact_id,profile_revision,fingerprint,worker_image_digest,tool_rule_pins,limit_profile,status)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING job_id,task_id,artifact_id,profile_revision,fingerprint,worker_image_digest,
tool_rule_pins,limit_profile,status,progress,lease_nonce,heartbeat_at,exit_class,error_class,error,
raw_result_hash,canonical_projection_hash,started_at,finished_at,created_at,updated_at`,
		jobID, create.TaskID, create.ArtifactID, BinaryTriageProfileRevision, create.Fingerprint,
		create.WorkerImageDigest, pins, limits, status))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return job, true, nil
}

func binaryJSONObject(value any) ([]byte, error) {
	if value == nil {
		return []byte(`{}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("value must be a JSON object")
	}
	return raw, nil
}

func (database *DB) MarkBinaryTriageRunning(ctx context.Context, taskID int64, jobID, leaseNonce string) error {
	if taskID <= 0 || !binaryBrokerIDPattern.MatchString(jobID) || !binaryBrokerIDPattern.MatchString(leaseNonce) {
		return ErrBinaryArtifactInput
	}
	result, err := database.ExecContext(ctx, `UPDATE binary_triage_jobs job SET
status='running',progress=1,lease_nonce=$3,heartbeat_at=now(),started_at=COALESCE(started_at,now())
FROM tasks task WHERE job.job_id=$1 AND job.task_id=$2 AND job.status='queued'
  AND task.id=job.task_id AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued AND task.status IN ('created','running')`, jobID, taskID, leaseNonce)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrBinaryTriageState
	}
	return nil
}

func (database *DB) FinishBinaryTriageJob(ctx context.Context, taskID int64, jobID, leaseNonce string, finish BinaryTriageFinish) error {
	if taskID <= 0 || !binaryBrokerIDPattern.MatchString(jobID) || !binaryBrokerIDPattern.MatchString(leaseNonce) ||
		len(finish.ExitClass) > 128 || len(finish.ErrorClass) > 128 || len(finish.Error) > 65536 {
		return ErrBinaryArtifactInput
	}
	switch finish.Status {
	case "completed", "partial", "unsupported", "failed", "canceled", "interrupted":
	default:
		return ErrBinaryTriageState
	}
	if (finish.RawResultHash != "" && !binarySHA256Pattern.MatchString(finish.RawResultHash)) ||
		(finish.CanonicalProjectionHash != "" && !binarySHA256Pattern.MatchString(finish.CanonicalProjectionHash)) ||
		finish.StartedAt.IsZero() || finish.FinishedAt.IsZero() || finish.FinishedAt.Before(finish.StartedAt) {
		return ErrBinaryArtifactInput
	}
	for _, observation := range finish.Observations {
		if err := validateBinaryObservation(observation); err != nil {
			return err
		}
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var artifactID string
	err = tx.QueryRowContext(ctx, `SELECT artifact_id FROM binary_triage_jobs
WHERE job_id=$1 AND task_id=$2 AND status='running' AND lease_nonce=$3 FOR UPDATE`, jobID, taskID, leaseNonce).Scan(&artifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBinaryTriageState
	}
	if err != nil {
		return err
	}
	for _, observation := range finish.Observations {
		toolIdentity := observation.ToolRuleIdentity
		if len(toolIdentity) == 0 {
			toolIdentity = json.RawMessage(`{}`)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO binary_observations(
job_id,task_id,artifact_id,analyzer,kind,stable_key,authority,address,file_offset,record,tool_rule_identity,truncated)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, jobID, taskID, artifactID,
			observation.Analyzer, observation.Kind, observation.StableKey, observation.Authority,
			observation.Address, observation.FileOffset, observation.Record, toolIdentity, observation.Truncated); err != nil {
			return err
		}
	}
	progress := 100
	switch finish.Status {
	case "partial":
		progress = 99
	case "failed", "canceled", "interrupted":
		progress = 0
	}
	if _, err := tx.ExecContext(ctx, `UPDATE binary_triage_jobs SET status=$4,progress=$5,exit_class=$6,
error_class=$7,error=$8,raw_result_hash=$9,canonical_projection_hash=$10,heartbeat_at=now(),started_at=$11,finished_at=$12
WHERE job_id=$1 AND task_id=$2 AND lease_nonce=$3`, jobID, taskID, leaseNonce, finish.Status, progress,
		finish.ExitClass, finish.ErrorClass, finish.Error, finish.RawResultHash, finish.CanonicalProjectionHash,
		finish.StartedAt.UTC(), finish.FinishedAt.UTC()); err != nil {
		return err
	}
	artifactState := "ready"
	quarantineState := "accepted"
	if finish.Status == "failed" || finish.Status == "interrupted" || finish.Status == "canceled" {
		artifactState = "quarantined"
		quarantineState = "rejected"
	} else if finish.Status == "unsupported" {
		artifactState = "unsupported"
		quarantineState = "accepted"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE binary_artifacts SET state=$2,quarantine_state=$3 WHERE artifact_id=$1`,
		artifactID, artifactState, quarantineState); err != nil {
		return err
	}
	return tx.Commit()
}

func (database *DB) FailQueuedBinaryTriageJob(ctx context.Context, taskID int64, jobID, errorClass, message string) error {
	if taskID <= 0 || !binaryBrokerIDPattern.MatchString(jobID) || len(errorClass) > 128 || len(message) > 65536 ||
		containsBinaryMetadataControl(errorClass) {
		return ErrBinaryArtifactInput
	}
	result, err := database.ExecContext(ctx, `UPDATE binary_triage_jobs SET status='failed',progress=0,
exit_class='broker_failure',error_class=$3,error=$4,started_at=COALESCE(started_at,now()),finished_at=now()
WHERE task_id=$1 AND job_id=$2 AND status='queued'`, taskID, jobID, errorClass, message)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrBinaryTriageState
	}
	return nil
}

func validateBinaryObservation(observation BinaryObservationInput) error {
	if strings.TrimSpace(observation.Analyzer) == "" || len(observation.Analyzer) > 64 ||
		strings.TrimSpace(observation.Kind) == "" || len(observation.Kind) > 128 ||
		strings.TrimSpace(observation.StableKey) == "" || len(observation.StableKey) > 512 ||
		len(observation.Address) > 128 || containsBinaryMetadataControl(observation.Analyzer) ||
		containsBinaryMetadataControl(observation.Kind) || containsBinaryMetadataControl(observation.StableKey) ||
		containsBinaryMetadataControl(observation.Address) || len(observation.Record) == 0 || len(observation.Record) > 2<<20 {
		return ErrBinaryArtifactInput
	}
	if observation.Authority != "metadata" && observation.Authority != "candidate" {
		return ErrBinaryArtifactInput
	}
	if observation.FileOffset != nil && *observation.FileOffset < 0 {
		return ErrBinaryArtifactInput
	}
	var record any
	if err := json.Unmarshal(observation.Record, &record); err != nil || record == nil {
		return ErrBinaryArtifactInput
	}
	if len(observation.ToolRuleIdentity) > 65536 {
		return ErrBinaryArtifactInput
	}
	if len(observation.ToolRuleIdentity) > 0 {
		var identity map[string]any
		if err := json.Unmarshal(observation.ToolRuleIdentity, &identity); err != nil || identity == nil {
			return ErrBinaryArtifactInput
		}
	}
	return nil
}

func (database *DB) BinaryTriageJobForTask(ctx context.Context, taskID int64, jobID string) (*BinaryTriageJob, error) {
	if taskID <= 0 || !binaryBrokerIDPattern.MatchString(jobID) {
		return nil, ErrBinaryArtifactInput
	}
	return scanBinaryTriageJob(database.QueryRowContext(ctx, binaryTriageJobSelect+` WHERE task_id=$1 AND job_id=$2`, taskID, jobID))
}

func (database *DB) ListBinaryTriageJobs(ctx context.Context, taskID int64, limit, offset int) ([]BinaryTriageJob, error) {
	if taskID <= 0 {
		return nil, ErrBinaryArtifactInput
	}
	limit, offset = boundedBinaryPage(limit, offset)
	rows, err := database.QueryContext(ctx, binaryTriageJobSelect+`
 WHERE task_id=$1 ORDER BY created_at DESC,job_id LIMIT $2 OFFSET $3`, taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BinaryTriageJob, 0)
	for rows.Next() {
		item, err := scanBinaryTriageJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (database *DB) ListBinaryObservations(ctx context.Context, taskID int64, jobID string, limit, offset int) ([]BinaryObservation, error) {
	if taskID <= 0 || !binaryBrokerIDPattern.MatchString(jobID) {
		return nil, ErrBinaryArtifactInput
	}
	limit, offset = boundedBinaryPage(limit, offset)
	rows, err := database.QueryContext(ctx, `SELECT id,job_id,task_id,artifact_id,analyzer,kind,stable_key,authority,
address,file_offset,record,tool_rule_identity,truncated,created_at FROM binary_observations
WHERE task_id=$1 AND job_id=$2 ORDER BY id LIMIT $3 OFFSET $4`, taskID, jobID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BinaryObservation, 0)
	return collectRowValues(rows, items, func() (BinaryObservation, error) {
		var item BinaryObservation
		err := rows.Scan(&item.ID, &item.JobID, &item.TaskID, &item.ArtifactID, &item.Analyzer,
			&item.Kind, &item.StableKey, &item.Authority, &item.Address, &item.FileOffset,
			&item.Record, &item.ToolRuleIdentity, &item.Truncated, &item.CreatedAt)
		return item, err
	})
}

func (database *DB) CancelBinaryTriageJob(ctx context.Context, taskID int64, jobID string) error {
	if taskID <= 0 || !binaryBrokerIDPattern.MatchString(jobID) {
		return ErrBinaryArtifactInput
	}
	result, err := database.ExecContext(ctx, `UPDATE binary_triage_jobs SET status='canceled',progress=0,
exit_class='operator_canceled',finished_at=now() WHERE task_id=$1 AND job_id=$2 AND status='queued'`, taskID, jobID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrBinaryTriageState
	}
	return nil
}

func (database *DB) ListQueuedBinaryTriageWork(ctx context.Context, limit int) ([]BinaryTriageQueueItem, error) {
	if limit <= 0 || limit > 4096 {
		limit = 4096
	}
	rows, err := database.QueryContext(ctx, `SELECT job.task_id,job.artifact_id,job.job_id,reference.broker_ref_id,job.fingerprint
FROM binary_triage_jobs job
JOIN binary_artifact_refs reference ON reference.artifact_id=job.artifact_id AND reference.task_id=job.task_id
JOIN tasks task ON task.id=job.task_id
WHERE job.status='queued' AND reference.state='active'
  AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued AND task.status IN ('created','running')
ORDER BY job.created_at,job.job_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BinaryTriageQueueItem, 0)
	return collectRowValues(rows, items, func() (BinaryTriageQueueItem, error) {
		var item BinaryTriageQueueItem
		err := rows.Scan(&item.TaskID, &item.ArtifactID, &item.JobID, &item.ReferenceID, &item.Fingerprint)
		return item, err
	})
}

// ReconcileBinaryTriageJobs releases active uniqueness for a worker lease
// interrupted by an application restart. Queued jobs remain durable and are
// re-enqueued from ListQueuedBinaryTriageWork; an old running lease can never
// finish an interrupted row.
func (database *DB) ReconcileBinaryTriageJobs(ctx context.Context) (int64, error) {
	result, err := database.ExecContext(ctx, `UPDATE binary_triage_jobs SET status='interrupted',progress=0,
error_class='host_restart',error=CASE WHEN error='' THEN 'application stopped before binary triage completed' ELSE error END,
finished_at=now() WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
