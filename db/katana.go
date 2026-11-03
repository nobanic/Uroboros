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
	ErrKatanaJobActive    = errors.New("a Katana discovery run is already active for this task")
	ErrKatanaTaskInactive = errors.New("Katana discovery requires a live, unpaused task")
)

type KatanaJob struct {
	ID             int64           `json:"id"`
	TaskID         int64           `json:"task_id"`
	Source         string          `json:"source"`
	Status         string          `json:"status"`
	Mode           string          `json:"mode"`
	Config         json.RawMessage `json:"config"`
	ConfigHash     string          `json:"config_hash"`
	Scope          json.RawMessage `json:"scope"`
	ScopeHash      string          `json:"scope_hash"`
	Seeds          json.RawMessage `json:"seeds"`
	SeedHash       string          `json:"seed_hash"`
	BinaryVersion  string          `json:"binary_version"`
	ExitCode       *int            `json:"exit_code,omitempty"`
	Error          string          `json:"error,omitempty"`
	Stderr         string          `json:"stderr,omitempty"`
	Stats          json.RawMessage `json:"stats"`
	CandidateCount int64           `json:"candidate_count"`
	VerifiedCount  int64           `json:"verified_count"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type KatanaJobCreate struct {
	TaskID        int64
	Source        string
	Mode          string
	Config        any
	ConfigHash    string
	Scope         any
	ScopeHash     string
	Seeds         []string
	SeedHash      string
	BinaryVersion string
}

type KatanaJobFinish struct {
	Status   string
	ExitCode *int
	Error    string
	Stderr   string
	Stats    any
}

type KatanaDiscovery struct {
	ID            int64           `json:"id"`
	JobID         int64           `json:"job_id"`
	TaskID        int64           `json:"task_id"`
	Source        string          `json:"source"`
	Method        string          `json:"method"`
	URL           string          `json:"url"`
	StatusCode    *int            `json:"status_code,omitempty"`
	Technologies  []string        `json:"technologies"`
	KnowledgeBase json.RawMessage `json:"knowledge_base"`
	Verified      bool            `json:"verified"`
	TrafficID     string          `json:"traffic_id,omitempty"`
	AssetID       *int64          `json:"asset_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type KatanaDiscoveryUpsert struct {
	JobID         int64
	TaskID        int64
	Source        string
	Method        string
	URL           string
	StatusCode    *int
	Technologies  []string
	KnowledgeBase json.RawMessage
	Verified      bool
	TrafficID     string
	AssetID       *int64
}

const katanaJobSelect = `SELECT id,task_id,source,status,mode,config,config_hash,scope,scope_hash,seeds,seed_hash,
binary_version,exit_code,error,stderr,stats,candidate_count,verified_count,started_at,finished_at,created_at,updated_at
FROM katana_jobs`

func scanKatanaJob(row interface{ Scan(...any) error }) (*KatanaJob, error) {
	job := &KatanaJob{}
	err := row.Scan(&job.ID, &job.TaskID, &job.Source, &job.Status, &job.Mode, &job.Config, &job.ConfigHash,
		&job.Scope, &job.ScopeHash, &job.Seeds, &job.SeedHash, &job.BinaryVersion, &job.ExitCode, &job.Error, &job.Stderr,
		&job.Stats, &job.CandidateCount, &job.VerifiedCount, &job.StartedAt, &job.FinishedAt,
		&job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (d *DB) CreateKatanaJob(ctx context.Context, create KatanaJobCreate) (*KatanaJob, error) {
	if create.TaskID <= 0 || strings.TrimSpace(create.ConfigHash) == "" || strings.TrimSpace(create.ScopeHash) == "" || strings.TrimSpace(create.SeedHash) == "" {
		return nil, fmt.Errorf("task, config hash, scope hash and seed hash are required")
	}
	if create.Source == "" {
		create.Source = "auto"
	}
	if create.Source != "auto" && create.Source != "tool" {
		return nil, fmt.Errorf("invalid Katana job source %q", create.Source)
	}
	if create.Mode == "" {
		create.Mode = "standard"
	}
	if create.Mode != "standard" && create.Mode != "headless" && create.Mode != "hybrid" {
		return nil, fmt.Errorf("invalid Katana job mode %q", create.Mode)
	}
	config, err := json.Marshal(create.Config)
	if err != nil {
		return nil, err
	}
	scope, err := json.Marshal(create.Scope)
	if err != nil {
		return nil, err
	}
	seeds, err := json.Marshal(create.Seeds)
	if err != nil {
		return nil, err
	}
	job, err := scanKatanaJob(d.QueryRowContext(ctx, `
INSERT INTO katana_jobs(task_id,source,status,mode,config,config_hash,scope,scope_hash,seeds,seed_hash,binary_version)
SELECT task.id,$2,'queued',$3,$4,$5,$6,$7,$8,$9,$10
FROM tasks task
WHERE task.id=$1 AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued AND task.status NOT IN ('done','failed','timeout')
RETURNING id,task_id,source,status,mode,config,config_hash,scope,scope_hash,seeds,seed_hash,
binary_version,exit_code,error,stderr,stats,candidate_count,verified_count,started_at,finished_at,created_at,updated_at`,
		create.TaskID, create.Source, create.Mode, config, create.ConfigHash, scope, create.ScopeHash, seeds, create.SeedHash, create.BinaryVersion))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_katana_jobs_active_task" {
			return nil, ErrKatanaJobActive
		}
	}
	if err == nil && job == nil {
		return nil, ErrKatanaTaskInactive
	}
	return job, err
}

func (d *DB) MarkKatanaJobRunning(ctx context.Context, id int64) error {
	result, err := d.ExecContext(ctx, `UPDATE katana_jobs job SET status='running',started_at=COALESCE(job.started_at,now())
FROM tasks task
WHERE job.id=$1 AND job.status='queued' AND task.id=job.task_id
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
		return fmt.Errorf("Katana job %d cannot run because it is not queued or its task is inactive", id)
	}
	return nil
}

func (d *DB) FinishKatanaJob(ctx context.Context, id int64, finish KatanaJobFinish) error {
	switch finish.Status {
	case "completed", "failed", "canceled", "interrupted":
	default:
		return fmt.Errorf("invalid terminal Katana job status %q", finish.Status)
	}
	stats, err := json.Marshal(finish.Stats)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `UPDATE katana_jobs job SET
status=$2,exit_code=$3,error=$4,stderr=$5,stats=$6,finished_at=now(),
candidate_count=(SELECT count(*) FROM katana_discoveries WHERE job_id=job.id AND NOT verified),
verified_count=(SELECT count(*) FROM katana_discoveries WHERE job_id=job.id AND verified)
WHERE id=$1 AND status IN ('queued','running')`, id, finish.Status, finish.ExitCode,
		finish.Error, finish.Stderr, stats)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("Katana job %d is not active", id)
	}
	return nil
}

func (d *DB) KatanaJob(ctx context.Context, id int64) (*KatanaJob, error) {
	return scanKatanaJob(d.QueryRowContext(ctx, katanaJobSelect+` WHERE id=$1`, id))
}

func (d *DB) ActiveKatanaJob(ctx context.Context, taskID int64) (*KatanaJob, error) {
	return scanKatanaJob(d.QueryRowContext(ctx, katanaJobSelect+`
WHERE task_id=$1 AND status IN ('queued','running') ORDER BY id DESC LIMIT 1`, taskID))
}

func (d *DB) LatestKatanaJob(ctx context.Context, taskID int64) (*KatanaJob, error) {
	return scanKatanaJob(d.QueryRowContext(ctx, katanaJobSelect+` WHERE task_id=$1 ORDER BY id DESC LIMIT 1`, taskID))
}

func (d *DB) HasCompletedKatanaJob(ctx context.Context, taskID int64, scopeHash, configHash, seedHash string) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM katana_jobs
WHERE task_id=$1 AND scope_hash=$2 AND config_hash=$3 AND seed_hash=$4 AND status='completed')`,
		taskID, scopeHash, configHash, seedHash).Scan(&exists)
	return exists, err
}

// HasSettledKatanaJobForAuto reports whether automatic scheduling has already
// reached a stable outcome for this exact fingerprint. A completed run from
// either source remains deduplicated as before. A failed automatic run is also
// stable until its scope, config or seed fingerprint changes; explicit tool
// runs bypass this lookup and remain available for operator-requested retries.
// Canceled and interrupted jobs remain eligible for automatic recovery.
func (d *DB) HasSettledKatanaJobForAuto(ctx context.Context, taskID int64, scopeHash, configHash, seedHash string) (bool, error) {
	var exists bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM katana_jobs
WHERE task_id=$1 AND scope_hash=$2 AND config_hash=$3 AND seed_hash=$4
  AND (status='completed' OR (source='auto' AND status='failed')))`,
		taskID, scopeHash, configHash, seedHash).Scan(&exists)
	return exists, err
}

// ReconcileKatanaJobs marks processes from a previous application instance as
// interrupted. A local child process cannot survive as an owned active run after
// its parent has restarted, and the partial unique index must be released.
func (d *DB) ReconcileKatanaJobs(ctx context.Context) (int64, error) {
	result, err := d.ExecContext(ctx, `UPDATE katana_jobs SET status='interrupted',
error=CASE WHEN error='' THEN 'application stopped before the Katana process completed' ELSE error END,
finished_at=now() WHERE status IN ('queued','running')`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *DB) UpsertKatanaDiscovery(ctx context.Context, input KatanaDiscoveryUpsert) (int64, error) {
	if input.JobID <= 0 || input.TaskID <= 0 || strings.TrimSpace(input.URL) == "" {
		return 0, fmt.Errorf("job, task and URL are required")
	}
	input.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	if input.Method == "" {
		input.Method = "GET"
	}
	if input.Source == "" {
		input.Source = "katana"
	}
	if input.Technologies == nil {
		input.Technologies = []string{}
	}
	knowledgeBase := json.RawMessage(`{}`)
	if len(input.KnowledgeBase) > 0 && string(input.KnowledgeBase) != "null" {
		var object map[string]any
		if err := json.Unmarshal(input.KnowledgeBase, &object); err != nil {
			return 0, fmt.Errorf("invalid Katana knowledge base: %w", err)
		}
		if object == nil {
			return 0, fmt.Errorf("Katana knowledge base must be a JSON object")
		}
		var err error
		knowledgeBase, err = json.Marshal(object)
		if err != nil {
			return 0, err
		}
	}
	var id int64
	err := d.QueryRowContext(ctx, `
INSERT INTO katana_discoveries(job_id,task_id,source,method,url,status_code,technologies,knowledge_base,verified,traffic_id,asset_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (job_id,method,url,verified) DO UPDATE SET
source=EXCLUDED.source,
status_code=COALESCE(EXCLUDED.status_code,katana_discoveries.status_code),
technologies=(SELECT ARRAY(SELECT DISTINCT unnest(katana_discoveries.technologies || EXCLUDED.technologies))),
knowledge_base=katana_discoveries.knowledge_base || EXCLUDED.knowledge_base,
traffic_id=CASE WHEN EXCLUDED.traffic_id<>'' THEN EXCLUDED.traffic_id ELSE katana_discoveries.traffic_id END,
asset_id=COALESCE(EXCLUDED.asset_id,katana_discoveries.asset_id)
RETURNING id`, input.JobID, input.TaskID, input.Source, input.Method, input.URL, input.StatusCode,
		input.Technologies, knowledgeBase, input.Verified, input.TrafficID, input.AssetID).Scan(&id)
	return id, err
}

func (d *DB) ListKatanaDiscoveries(ctx context.Context, jobID int64, limit, offset int) ([]KatanaDiscovery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := d.QueryContext(ctx, `SELECT id,job_id,task_id,source,method,url,status_code,
COALESCE(array_to_json(technologies),'[]'::json)::text,knowledge_base,
verified,traffic_id,asset_id,created_at,updated_at FROM katana_discoveries
WHERE job_id=$1 ORDER BY verified DESC,id LIMIT $2 OFFSET $3`, jobID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]KatanaDiscovery, 0)
	for rows.Next() {
		var item KatanaDiscovery
		var technologiesJSON string
		var knowledgeBase []byte
		if err := rows.Scan(&item.ID, &item.JobID, &item.TaskID, &item.Source, &item.Method, &item.URL,
			&item.StatusCode, &technologiesJSON, &knowledgeBase, &item.Verified, &item.TrafficID, &item.AssetID,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(technologiesJSON), &item.Technologies); err != nil {
			return nil, fmt.Errorf("decode Katana technologies: %w", err)
		}
		if item.Technologies == nil {
			item.Technologies = []string{}
		}
		item.KnowledgeBase = append(json.RawMessage(nil), knowledgeBase...)
		if len(item.KnowledgeBase) == 0 {
			item.KnowledgeBase = json.RawMessage(`{}`)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
