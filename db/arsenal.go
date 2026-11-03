package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrArsenalJobActive         = errors.New("an Arsenal scan is already active for this task")
	ErrArsenalTaskInactive      = errors.New("Arsenal scanning requires a live, unpaused task")
	ErrArjunEndpointNotAdmitted = errors.New("Arjun requires a task-scoped GET endpoint backed by verified traffic")
	ErrWAFEndpointNotAdmitted   = errors.New("passive WAF fingerprinting requires a task-scoped GET endpoint backed by verified traffic")
)

// ArjunEndpointAdmission is the minimum durable evidence needed to let Arjun
// probe one existing endpoint. The asset remains the authority boundary; the
// traffic id only proves that Uroboros observed the endpoint through an owned
// recording proxy before this admission was issued.
type ArjunEndpointAdmission struct {
	Asset          *Asset
	TrafficID      string
	EvidenceSource string
}

// WAFEndpointAdmission is intentionally the same narrow evidence projection as
// Arjun admission.  The passive worker receives only the selected Traffic
// response headers; URL and ownership checks remain on the host.
type WAFEndpointAdmission struct {
	Asset          *Asset
	TrafficID      string
	EvidenceSource string
}

// ArjunDiscoveryRouteEvidence is the bounded, host-only projection used to
// derive one exact Arjun TCP route from a completed composite discovery job.
// The caller still has to validate the DNS -> naabu -> httpx relationship in
// the sanitized records; this type only guarantees their durable job/task
// provenance and prevents an endpoint asset from supplying an address.
type ArjunDiscoveryRouteEvidence struct {
	Job          *ArsenalJob
	Observations []ArsenalObservation
}

type ArsenalJob struct {
	ID              int64           `json:"id"`
	TaskID          int64           `json:"task_id"`
	Source          string          `json:"source"`
	Tool            string          `json:"tool"`
	Status          string          `json:"status"`
	Target          string          `json:"target"`
	Config          json.RawMessage `json:"config"`
	ConfigHash      string          `json:"config_hash"`
	Scope           json.RawMessage `json:"scope"`
	ScopeHash       string          `json:"scope_hash"`
	ContractVersion int             `json:"contract_version"`
	SourceRevision  string          `json:"source_revision"`
	ToolVersion     string          `json:"tool_version"`
	ToolSHA256      string          `json:"tool_sha256"`
	TemplateVersion string          `json:"template_version,omitempty"`
	TemplateSHA256  string          `json:"template_sha256,omitempty"`
	WordlistSHA256  string          `json:"wordlist_sha256,omitempty"`
	ExitState       string          `json:"exit_state,omitempty"`
	Complete        bool            `json:"complete"`
	Error           string          `json:"error,omitempty"`
	Stats           json.RawMessage `json:"stats"`
	CandidateCount  int64           `json:"candidate_count"`
	VerifiedCount   int64           `json:"verified_count"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type ArsenalJobCreate struct {
	TaskID          int64
	Source          string
	Tool            string
	Target          string
	Config          any
	ConfigHash      string
	Scope           any
	ScopeHash       string
	ContractVersion int
	SourceRevision  string
	ToolVersion     string
	ToolSHA256      string
	TemplateVersion string
	TemplateSHA256  string
	WordlistSHA256  string
}

type ArsenalJobFinish struct {
	Status    string
	ExitState string
	Complete  bool
	Error     string
	Stats     any
}

type ArsenalObservation struct {
	ID             int64           `json:"id"`
	JobID          int64           `json:"job_id"`
	TaskID         int64           `json:"task_id"`
	Tool           string          `json:"tool"`
	ObservationKey string          `json:"observation_key"`
	Authority      string          `json:"authority"`
	Record         json.RawMessage `json:"record"`
	ProxyVerified  bool            `json:"proxy_verified"`
	TrafficID      string          `json:"traffic_id,omitempty"`
	AssetID        *int64          `json:"asset_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type ArsenalObservationUpsert struct {
	JobID          int64
	TaskID         int64
	Tool           string
	ObservationKey string
	Authority      string
	Record         any
	ProxyVerified  bool
	TrafficID      string
	AssetID        *int64
}

const arsenalJobSelect = `SELECT id,task_id,source,tool,status,target,config,config_hash,scope,scope_hash,
contract_version,source_revision,tool_version,tool_sha256,template_version,template_sha256,wordlist_sha256,
exit_state,complete,error,stats,candidate_count,verified_count,started_at,finished_at,created_at,updated_at
FROM arsenal_jobs`

func validArsenalTool(tool string) bool {
	return tool == "nuclei" || tool == "httpx" || tool == "ffuf" || tool == "discovery" || tool == "archive" || tool == "arjun" || tool == "wafw00f"
}

// AdmittedArjunEndpoint resolves an endpoint id through the task's frozen
// asset view and then requires same-task, proxy-backed discovery evidence.
// Manual assets and inherited assets without evidence produced in this task
// cannot be used to disclose a target to the scanner sidecar.
func (d *DB) AdmittedArjunEndpoint(ctx context.Context, taskID, endpointID int64) (*ArjunEndpointAdmission, error) {
	if taskID <= 0 || endpointID <= 0 {
		return nil, ErrArjunEndpointNotAdmitted
	}
	assets, err := d.Assets().GetByIDsInScope(taskID, []int64{endpointID})
	if err != nil {
		return nil, fmt.Errorf("resolve Arjun endpoint scope: %w", err)
	}
	if len(assets) != 1 || assets[0] == nil || assets[0].Type != "endpoint" ||
		!strings.EqualFold(strings.TrimSpace(assets[0].Method), "GET") || strings.TrimSpace(assets[0].URL) == "" {
		return nil, ErrArjunEndpointNotAdmitted
	}

	var source, trafficID string
	err = d.QueryRowContext(ctx, `
SELECT evidence_source,traffic_id FROM (
    SELECT 'katana'::text AS evidence_source,traffic_id,created_at,id
    FROM katana_discoveries
    WHERE task_id=$1 AND asset_id=$2 AND verified AND traffic_id<>''
    UNION ALL
    SELECT 'arsenal:' || observation.tool AS evidence_source,observation.traffic_id,
           observation.created_at,observation.id
    FROM arsenal_observations observation
    JOIN arsenal_jobs job ON job.id=observation.job_id AND job.task_id=observation.task_id
    WHERE observation.task_id=$1 AND observation.asset_id=$2
      AND observation.tool IN ('nuclei','httpx','ffuf','discovery')
      AND observation.authority='candidate' AND observation.proxy_verified
      AND observation.traffic_id<>''
      AND (observation.tool<>'discovery' OR
           (job.tool='discovery' AND job.status='completed' AND job.complete))
) evidence
ORDER BY created_at DESC,id DESC LIMIT 1`, taskID, endpointID).Scan(&source, &trafficID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrArjunEndpointNotAdmitted
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Arjun endpoint evidence: %w", err)
	}
	if strings.TrimSpace(trafficID) == "" {
		return nil, ErrArjunEndpointNotAdmitted
	}
	return &ArjunEndpointAdmission{Asset: assets[0], TrafficID: trafficID, EvidenceSource: source}, nil
}

// AdmittedWAFEndpoint selects the newest same-task Traffic-backed GET endpoint
// evidence.  It deliberately excludes prior WAF candidates, manual assets and
// archive metadata so a fingerprint can never bootstrap its own authority.
func (d *DB) AdmittedWAFEndpoint(ctx context.Context, taskID, endpointID int64) (*WAFEndpointAdmission, error) {
	admission, err := d.AdmittedArjunEndpoint(ctx, taskID, endpointID)
	if errors.Is(err, ErrArjunEndpointNotAdmitted) {
		return nil, ErrWAFEndpointNotAdmitted
	}
	if err != nil {
		return nil, err
	}
	return &WAFEndpointAdmission{
		Asset: admission.Asset, TrafficID: admission.TrafficID, EvidenceSource: admission.EvidenceSource,
	}, nil
}

// ArjunDiscoveryRouteEvidence returns the newest completed same-task composite
// discovery job that produced proxy-backed evidence for endpointID, together
// with only its route-relevant sanitized observations. A repeated discovery is
// deterministic: the newest completed job is authoritative, while ambiguity
// inside that one job is left visible for the caller to reject fail-closed.
func (d *DB) ArjunDiscoveryRouteEvidence(ctx context.Context, taskID, endpointID int64) (*ArjunDiscoveryRouteEvidence, error) {
	if taskID <= 0 || endpointID <= 0 {
		return nil, nil
	}
	job, err := scanArsenalJob(d.QueryRowContext(ctx, arsenalJobSelect+`
WHERE task_id=$1 AND tool='discovery' AND status='completed' AND complete
  AND EXISTS (
      SELECT 1 FROM arsenal_observations observation
      WHERE observation.job_id=arsenal_jobs.id
        AND observation.task_id=$1 AND observation.tool='discovery'
        AND observation.asset_id=$2 AND observation.authority='candidate'
        AND observation.proxy_verified AND observation.traffic_id<>''
  )
ORDER BY id DESC LIMIT 1`, taskID, endpointID))
	if err != nil {
		return nil, fmt.Errorf("resolve Arjun discovery evidence job: %w", err)
	}
	if job == nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `SELECT id,job_id,task_id,tool,observation_key,authority,record,
proxy_verified,traffic_id,asset_id,created_at,updated_at FROM arsenal_observations
WHERE job_id=$1 AND (
    record->>'stage' IN ('dns','naabu') OR
    (record->>'stage'='httpx' AND asset_id=$2)
)
ORDER BY id`, job.ID, endpointID)
	if err != nil {
		return nil, fmt.Errorf("load Arjun discovery route observations: %w", err)
	}
	defer rows.Close()
	observations := make([]ArsenalObservation, 0)
	for rows.Next() {
		var item ArsenalObservation
		if err := rows.Scan(&item.ID, &item.JobID, &item.TaskID, &item.Tool, &item.ObservationKey,
			&item.Authority, &item.Record, &item.ProxyVerified, &item.TrafficID, &item.AssetID,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan Arjun discovery route observation: %w", err)
		}
		observations = append(observations, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Arjun discovery route observations: %w", err)
	}
	return &ArjunDiscoveryRouteEvidence{Job: job, Observations: observations}, nil
}

func scanArsenalJob(row interface{ Scan(...any) error }) (*ArsenalJob, error) {
	job := &ArsenalJob{}
	err := row.Scan(&job.ID, &job.TaskID, &job.Source, &job.Tool, &job.Status, &job.Target,
		&job.Config, &job.ConfigHash, &job.Scope, &job.ScopeHash, &job.ContractVersion,
		&job.SourceRevision, &job.ToolVersion, &job.ToolSHA256, &job.TemplateVersion,
		&job.TemplateSHA256, &job.WordlistSHA256, &job.ExitState, &job.Complete,
		&job.Error, &job.Stats, &job.CandidateCount, &job.VerifiedCount,
		&job.StartedAt, &job.FinishedAt, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (d *DB) CreateArsenalJob(ctx context.Context, create ArsenalJobCreate) (*ArsenalJob, error) {
	if create.TaskID <= 0 || !validArsenalTool(create.Tool) || strings.TrimSpace(create.Target) == "" ||
		strings.TrimSpace(create.ConfigHash) == "" || strings.TrimSpace(create.ScopeHash) == "" ||
		create.ContractVersion <= 0 || strings.TrimSpace(create.SourceRevision) == "" ||
		strings.TrimSpace(create.ToolVersion) == "" || strings.TrimSpace(create.ToolSHA256) == "" {
		return nil, errors.New("task, tool, target, fingerprints and scanner provenance are required")
	}
	if create.Source == "" {
		create.Source = "tool"
	}
	if create.Source != "tool" && create.Source != "system" {
		return nil, fmt.Errorf("invalid Arsenal job source %q", create.Source)
	}
	config, err := json.Marshal(create.Config)
	if err != nil {
		return nil, err
	}
	scope, err := json.Marshal(create.Scope)
	if err != nil {
		return nil, err
	}
	job, err := scanArsenalJob(d.QueryRowContext(ctx, `
INSERT INTO arsenal_jobs(task_id,source,tool,status,target,config,config_hash,scope,scope_hash,
contract_version,source_revision,tool_version,tool_sha256,template_version,template_sha256,wordlist_sha256)
SELECT task.id,$2,$3,'queued',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15
FROM tasks task
WHERE task.id=$1 AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued AND task.status NOT IN ('done','failed','timeout')
RETURNING id,task_id,source,tool,status,target,config,config_hash,scope,scope_hash,
contract_version,source_revision,tool_version,tool_sha256,template_version,template_sha256,wordlist_sha256,
exit_state,complete,error,stats,candidate_count,verified_count,started_at,finished_at,created_at,updated_at`,
		create.TaskID, create.Source, create.Tool, create.Target, config, create.ConfigHash,
		scope, create.ScopeHash, create.ContractVersion, create.SourceRevision, create.ToolVersion,
		create.ToolSHA256, create.TemplateVersion, create.TemplateSHA256, create.WordlistSHA256))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_arsenal_jobs_active_task" {
			return nil, ErrArsenalJobActive
		}
	}
	if err == nil && job == nil {
		return nil, ErrArsenalTaskInactive
	}
	return job, err
}

func (d *DB) MarkArsenalJobRunning(ctx context.Context, id int64) error {
	result, err := d.ExecContext(ctx, `UPDATE arsenal_jobs job SET status='running',started_at=COALESCE(job.started_at,now())
FROM tasks task WHERE job.id=$1 AND job.status='queued' AND task.id=job.task_id
  AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued AND task.status NOT IN ('done','failed','timeout')`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("Arsenal job %d cannot run because it is not queued or its task is inactive", id)
	}
	return nil
}

func (d *DB) FinishArsenalJob(ctx context.Context, id int64, finish ArsenalJobFinish) error {
	switch finish.Status {
	case "completed", "failed", "canceled", "interrupted":
	default:
		return fmt.Errorf("invalid terminal Arsenal job status %q", finish.Status)
	}
	if finish.Complete != (finish.Status == "completed") {
		return errors.New("only a completed Arsenal job may be marked complete")
	}
	statsValue := finish.Stats
	if statsValue == nil {
		statsValue = map[string]any{}
	}
	stats, err := json.Marshal(statsValue)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `UPDATE arsenal_jobs job SET status=$2,exit_state=$3,complete=$4,
error=$5,stats=$6,finished_at=now(),
candidate_count=(SELECT count(*) FROM arsenal_observations WHERE job_id=job.id),
verified_count=(SELECT count(*) FROM arsenal_observations WHERE job_id=job.id AND proxy_verified)
WHERE id=$1 AND status IN ('queued','running')`, id, finish.Status, finish.ExitState,
		finish.Complete, finish.Error, stats)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("Arsenal job %d is not active", id)
	}
	return nil
}

func (d *DB) ArsenalJob(ctx context.Context, id int64) (*ArsenalJob, error) {
	return scanArsenalJob(d.QueryRowContext(ctx, arsenalJobSelect+` WHERE id=$1`, id))
}

func (d *DB) ActiveArsenalJob(ctx context.Context, taskID int64) (*ArsenalJob, error) {
	return scanArsenalJob(d.QueryRowContext(ctx, arsenalJobSelect+`
WHERE task_id=$1 AND status IN ('queued','running') ORDER BY id DESC LIMIT 1`, taskID))
}

func (d *DB) LatestArsenalJob(ctx context.Context, taskID int64) (*ArsenalJob, error) {
	return scanArsenalJob(d.QueryRowContext(ctx, arsenalJobSelect+` WHERE task_id=$1 ORDER BY id DESC LIMIT 1`, taskID))
}

// ReusableWAFJob makes the passive operation idempotent across repeated model
// calls.  The config hash binds endpoint, exact Traffic response-head hash,
// profile and corpus.  Inactive tasks cannot use this read path to bypass the
// normal lifecycle admission.
func (d *DB) ReusableWAFJob(ctx context.Context, taskID int64, configHash string) (*ArsenalJob, error) {
	configHash = strings.ToLower(strings.TrimSpace(configHash))
	if taskID <= 0 || len(configHash) != 64 || strings.Trim(configHash, "0123456789abcdef") != "" {
		return nil, nil
	}
	return scanArsenalJob(d.QueryRowContext(ctx, arsenalJobSelect+`
WHERE arsenal_jobs.task_id=$1 AND arsenal_jobs.tool='wafw00f'
  AND arsenal_jobs.status='completed' AND arsenal_jobs.complete
  AND arsenal_jobs.config_hash=$2
  AND EXISTS (SELECT 1 FROM tasks task WHERE task.id=arsenal_jobs.task_id
      AND task.deleted_at IS NULL AND task.archived_at IS NULL
      AND NOT task.paused AND NOT task.queued AND task.status NOT IN ('done','failed','timeout'))
ORDER BY arsenal_jobs.id DESC LIMIT 1`, taskID, configHash))
}

func (d *DB) ReconcileArsenalJobs(ctx context.Context) (int64, error) {
	result, err := d.ExecContext(ctx, `UPDATE arsenal_jobs SET status='interrupted',complete=false,
error=CASE WHEN error='' THEN 'application stopped before the Arsenal scan completed' ELSE error END,
finished_at=now() WHERE status IN ('queued','running')`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *DB) UpsertArsenalObservation(ctx context.Context, input ArsenalObservationUpsert) (int64, error) {
	if input.JobID <= 0 || input.TaskID <= 0 || !validArsenalTool(input.Tool) || strings.TrimSpace(input.ObservationKey) == "" {
		return 0, errors.New("job, task, tool and observation key are required")
	}
	if input.Tool == "archive" {
		return 0, errors.New("archive observations require the private provider artifact transaction")
	}
	if input.Authority != "candidate" && input.Authority != "hypothesis" {
		return 0, fmt.Errorf("invalid Arsenal observation authority %q", input.Authority)
	}
	if input.ProxyVerified != (strings.TrimSpace(input.TrafficID) != "") {
		return 0, errors.New("proxy verification requires exactly one durable traffic id")
	}
	if input.AssetID != nil && (input.Authority != "candidate" || !input.ProxyVerified) {
		return 0, errors.New("only a proxy-verified candidate may reference an imported asset")
	}
	record, err := json.Marshal(input.Record)
	if err != nil {
		return 0, err
	}
	var object map[string]any
	if err := json.Unmarshal(record, &object); err != nil || object == nil {
		return 0, errors.New("Arsenal observation record must be a JSON object")
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO arsenal_observations(
job_id,task_id,tool,observation_key,authority,record,proxy_verified,traffic_id,asset_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT(job_id,observation_key) DO UPDATE SET
authority=EXCLUDED.authority,record=EXCLUDED.record,
proxy_verified=arsenal_observations.proxy_verified OR EXCLUDED.proxy_verified,
traffic_id=CASE WHEN EXCLUDED.traffic_id<>'' THEN EXCLUDED.traffic_id ELSE arsenal_observations.traffic_id END,
asset_id=COALESCE(EXCLUDED.asset_id,arsenal_observations.asset_id)
RETURNING id`, input.JobID, input.TaskID, input.Tool, input.ObservationKey, input.Authority,
		record, input.ProxyVerified, input.TrafficID, input.AssetID).Scan(&id)
	return id, err
}

func (d *DB) ListArsenalObservations(ctx context.Context, jobID int64, limit, offset int) ([]ArsenalObservation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := d.QueryContext(ctx, `SELECT id,job_id,task_id,tool,observation_key,authority,record,
proxy_verified,traffic_id,asset_id,created_at,updated_at FROM arsenal_observations
WHERE job_id=$1 ORDER BY id LIMIT $2 OFFSET $3`, jobID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ArsenalObservation, 0)
	return collectRowValues(rows, out, func() (ArsenalObservation, error) {
		var item ArsenalObservation
		err := rows.Scan(&item.ID, &item.JobID, &item.TaskID, &item.Tool, &item.ObservationKey,
			&item.Authority, &item.Record, &item.ProxyVerified, &item.TrafficID, &item.AssetID,
			&item.CreatedAt, &item.UpdatedAt)
		return item, err
	})
}
