package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrDiscoveryReviewNotReady = errors.New("automatic Katana discovery has not reached a terminal state")
	ErrDiscoveryReviewStale    = errors.New("discovery review token is stale")
)

// DiscoveryDecision is one explicit coverage decision for a typed discovery
// family. A scanner start is admitted only when its family is marked run.
type DiscoveryDecision struct {
	Family string `json:"family"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

var discoveryDecisionFamilies = []string{
	"katana_rerun",
	"arsenal_discover",
	"httpx",
	"ffuf",
	"nuclei",
	"arjun_discover",
	"waf_fingerprint",
	"archive_discover",
}

func DiscoveryDecisionFamilies() []string {
	return append([]string(nil), discoveryDecisionFamilies...)
}

type DiscoveryReview struct {
	ID           int64               `json:"id"`
	TaskID       int64               `json:"task_id"`
	KatanaJobID  *int64              `json:"katana_job_id,omitempty"`
	ReviewToken  string              `json:"review_token"`
	KatanaStatus string              `json:"katana_status"`
	Summary      string              `json:"summary"`
	Decisions    []DiscoveryDecision `json:"decisions"`
	AgentKey     string              `json:"agent_key"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type DiscoveryReviewState struct {
	TaskID                     int64                  `json:"task_id"`
	ReviewToken                string                 `json:"review_token"`
	Reviewable                 bool                   `json:"reviewable"`
	PendingReason              string                 `json:"pending_reason,omitempty"`
	AutomaticUnavailableReason string                 `json:"automatic_unavailable_reason,omitempty"`
	AutomaticSkipped           bool                   `json:"automatic_skipped"`
	CurrentFingerprint         *KatanaAutoFingerprint `json:"current_fingerprint,omitempty"`
	KatanaJob                  *KatanaJob             `json:"katana_job,omitempty"`
	Review                     *DiscoveryReview       `json:"review,omitempty"`
}

// KatanaAutoFingerprint is the exact automatic-discovery snapshot that a
// coverage decision authorizes. It is computed from the current task scope,
// default Katana configuration and normalized HTTP origin seeds before a typed
// scanner start; it is not inferred from whichever Katana job happens to be
// newest in the database.
type KatanaAutoFingerprint struct {
	ScopeHash  string `json:"scope_hash"`
	ConfigHash string `json:"config_hash"`
	SeedHash   string `json:"seed_hash"`
}

func (f KatanaAutoFingerprint) Empty() bool {
	return strings.TrimSpace(f.ScopeHash) == "" && strings.TrimSpace(f.ConfigHash) == "" && strings.TrimSpace(f.SeedHash) == ""
}

func (f KatanaAutoFingerprint) Matches(job *KatanaJob) bool {
	return job != nil && f.ScopeHash == job.ScopeHash && f.ConfigHash == job.ConfigHash && f.SeedHash == job.SeedHash
}

func normalizeKatanaAutoFingerprint(f KatanaAutoFingerprint) (*KatanaAutoFingerprint, error) {
	f.ScopeHash = strings.TrimSpace(f.ScopeHash)
	f.ConfigHash = strings.TrimSpace(f.ConfigHash)
	f.SeedHash = strings.TrimSpace(f.SeedHash)
	if f.Empty() {
		return nil, nil
	}
	if f.ScopeHash == "" || f.ConfigHash == "" || f.SeedHash == "" {
		return nil, errors.New("scope, config and seed hashes are all required for an automatic Katana fingerprint")
	}
	return &f, nil
}

func katanaAutoFingerprintForJob(job *KatanaJob) *KatanaAutoFingerprint {
	if job == nil {
		return nil
	}
	return &KatanaAutoFingerprint{ScopeHash: job.ScopeHash, ConfigHash: job.ConfigHash, SeedHash: job.SeedHash}
}

type TaskAssetProvenanceCount struct {
	Source    string `json:"source"`
	AssetType string `json:"asset_type"`
	Count     int64  `json:"count"`
}

type TaskScannerJobCount struct {
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

const discoveryReviewSelect = `SELECT id,task_id,katana_job_id,review_token,katana_status,summary,decisions,agent_key,created_at,updated_at FROM discovery_reviews`

func scanDiscoveryReview(row interface{ Scan(...any) error }) (*DiscoveryReview, error) {
	var review DiscoveryReview
	var decisions []byte
	if err := row.Scan(&review.ID, &review.TaskID, &review.KatanaJobID, &review.ReviewToken,
		&review.KatanaStatus, &review.Summary, &decisions, &review.AgentKey,
		&review.CreatedAt, &review.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(decisions, &review.Decisions); err != nil {
		return nil, fmt.Errorf("decode discovery decisions: %w", err)
	}
	return &review, nil
}

func (d *DB) LatestAutoKatanaJob(ctx context.Context, taskID int64) (*KatanaJob, error) {
	return scanKatanaJob(d.QueryRowContext(ctx, katanaJobSelect+` WHERE task_id=$1 AND source='auto' ORDER BY id DESC LIMIT 1`, taskID))
}

func (d *DB) AutoKatanaJobForFingerprint(ctx context.Context, taskID int64, fingerprint KatanaAutoFingerprint) (*KatanaJob, error) {
	normalized, err := normalizeKatanaAutoFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	if normalized == nil {
		return nil, nil
	}
	return scanKatanaJob(d.QueryRowContext(ctx, katanaJobSelect+`
WHERE task_id=$1 AND source='auto' AND scope_hash=$2 AND config_hash=$3 AND seed_hash=$4
ORDER BY id DESC LIMIT 1`, taskID, normalized.ScopeHash, normalized.ConfigHash, normalized.SeedHash))
}

func discoveryReviewToken(taskID int64, fingerprint *KatanaAutoFingerprint, job *KatanaJob, skipped ...bool) string {
	// v3 invalidates pre-T5 decisions, whose seven-family snapshot could not
	// express an explicit run/skip choice for passive WAF fingerprinting.
	payload := fmt.Sprintf("v3|task=%d|current=none|job=none", taskID)
	if fingerprint != nil {
		payload = fmt.Sprintf("v3|task=%d|current_scope=%s|current_config=%s|current_seed=%s|job=none",
			taskID, fingerprint.ScopeHash, fingerprint.ConfigHash, fingerprint.SeedHash)
	}
	if job != nil {
		finished := ""
		if job.FinishedAt != nil {
			finished = job.FinishedAt.UTC().Format(time.RFC3339Nano)
		}
		currentScope, currentConfig, currentSeed := "", "", ""
		if fingerprint != nil {
			currentScope, currentConfig, currentSeed = fingerprint.ScopeHash, fingerprint.ConfigHash, fingerprint.SeedHash
		}
		payload = fmt.Sprintf("v3|task=%d|current_scope=%s|current_config=%s|current_seed=%s|job=%d|status=%s|scope=%s|config=%s|seed=%s|candidate=%d|verified=%d|finished=%s",
			taskID, currentScope, currentConfig, currentSeed, job.ID, job.Status, job.ScopeHash, job.ConfigHash, job.SeedHash,
			job.CandidateCount, job.VerifiedCount, finished)
	}
	if len(skipped) > 0 && skipped[0] {
		payload += "|automatic=skipped_by_operator"
	}
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func terminalKatanaStatus(status string) bool {
	switch status {
	case "completed", "failed", "canceled", "interrupted":
		return true
	default:
		return false
	}
}

func (d *DB) CurrentDiscoveryReviewState(ctx context.Context, taskID int64) (*DiscoveryReviewState, error) {
	if taskID <= 0 {
		return nil, errors.New("task id is required")
	}
	job, err := d.LatestAutoKatanaJob(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return d.discoveryReviewState(ctx, taskID, katanaAutoFingerprintForJob(job), job)
}

// CurrentDiscoveryReviewStateForFingerprint binds the returned review token to
// the fingerprint computed from the task's state now. An older automatic job is
// deliberately ignored even when it is the latest materialized row.
func (d *DB) CurrentDiscoveryReviewStateForFingerprint(ctx context.Context, taskID int64, fingerprint KatanaAutoFingerprint) (*DiscoveryReviewState, error) {
	if taskID <= 0 {
		return nil, errors.New("task id is required")
	}
	normalized, err := normalizeKatanaAutoFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	var job *KatanaJob
	if normalized != nil {
		job, err = d.AutoKatanaJobForFingerprint(ctx, taskID, *normalized)
		if err != nil {
			return nil, err
		}
	}
	return d.discoveryReviewState(ctx, taskID, normalized, job)
}

func (d *DB) discoveryReviewState(ctx context.Context, taskID int64, fingerprint *KatanaAutoFingerprint, job *KatanaJob) (*DiscoveryReviewState, error) {
	skipped, err := d.InitialKatanaSkipped(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if skipped {
		job = nil
	}
	state := &DiscoveryReviewState{
		TaskID: taskID, CurrentFingerprint: fingerprint, KatanaJob: job,
		ReviewToken:      discoveryReviewToken(taskID, fingerprint, job, skipped),
		AutomaticSkipped: skipped,
	}
	if skipped {
		state.AutomaticUnavailableReason = "initial automatic Katana crawl skipped by operator; explicit tool runs remain available"
	}
	if job != nil && !terminalKatanaStatus(job.Status) {
		state.PendingReason = "automatic Katana job is " + job.Status
		return state, nil
	}
	state.Reviewable = true
	review, err := scanDiscoveryReview(d.QueryRowContext(ctx, discoveryReviewSelect+`
WHERE task_id=$1 AND review_token=$2`, taskID, state.ReviewToken))
	if err != nil {
		return nil, err
	}
	state.Review = review
	return state, nil
}

func validateDiscoveryDecisions(decisions []DiscoveryDecision) ([]DiscoveryDecision, error) {
	wanted := make(map[string]int, len(discoveryDecisionFamilies))
	for i, family := range discoveryDecisionFamilies {
		wanted[family] = i
	}
	normalized := make([]DiscoveryDecision, len(discoveryDecisionFamilies))
	seen := make(map[string]bool, len(decisions))
	for _, decision := range decisions {
		decision.Family = strings.TrimSpace(decision.Family)
		decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
		decision.Reason = strings.TrimSpace(decision.Reason)
		index, ok := wanted[decision.Family]
		if !ok {
			return nil, fmt.Errorf("unknown discovery family %q", decision.Family)
		}
		if seen[decision.Family] {
			return nil, fmt.Errorf("duplicate discovery family %q", decision.Family)
		}
		if decision.Action != "run" && decision.Action != "skip" {
			return nil, fmt.Errorf("family %s action must be run or skip", decision.Family)
		}
		if decision.Reason == "" {
			return nil, fmt.Errorf("family %s requires a reason", decision.Family)
		}
		seen[decision.Family] = true
		normalized[index] = decision
	}
	if len(seen) != len(discoveryDecisionFamilies) {
		missing := make([]string, 0)
		for _, family := range discoveryDecisionFamilies {
			if !seen[family] {
				missing = append(missing, family)
			}
		}
		return nil, fmt.Errorf("missing discovery decisions: %s", strings.Join(missing, ", "))
	}
	return normalized, nil
}

func (d *DB) RecordDiscoveryReview(ctx context.Context, taskID int64, token, summary, agentKey string, decisions []DiscoveryDecision) (*DiscoveryReview, error) {
	return d.recordDiscoveryReview(ctx, taskID, KatanaAutoFingerprint{}, false, token, summary, agentKey, decisions)
}

// RecordDiscoveryReviewForFingerprint persists a decision only for the exact
// fingerprint synchronously computed by the server admission path.
func (d *DB) RecordDiscoveryReviewForFingerprint(ctx context.Context, taskID int64, fingerprint KatanaAutoFingerprint, token, summary, agentKey string, decisions []DiscoveryDecision) (*DiscoveryReview, error) {
	return d.recordDiscoveryReview(ctx, taskID, fingerprint, true, token, summary, agentKey, decisions)
}

func (d *DB) recordDiscoveryReview(ctx context.Context, taskID int64, fingerprint KatanaAutoFingerprint, exact bool, token, summary, agentKey string, decisions []DiscoveryDecision) (*DiscoveryReview, error) {
	if taskID <= 0 || strings.TrimSpace(token) == "" {
		return nil, errors.New("task id and review token are required")
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, errors.New("discovery review summary is required")
	}
	if agentKey = strings.TrimSpace(agentKey); agentKey == "" {
		agentKey = "planner"
	}
	normalized, err := validateDiscoveryDecisions(decisions)
	if err != nil {
		return nil, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var skipped bool
	if err := tx.QueryRowContext(ctx, `SELECT skip_initial_katana FROM tasks WHERE id=$1 FOR SHARE`, taskID).Scan(&skipped); err != nil {
		return nil, err
	}
	var fingerprintPtr *KatanaAutoFingerprint
	var job *KatanaJob
	if exact {
		fingerprintPtr, err = normalizeKatanaAutoFingerprint(fingerprint)
		if err != nil {
			return nil, err
		}
		if fingerprintPtr != nil && !skipped {
			job, err = scanKatanaJob(tx.QueryRowContext(ctx, katanaJobSelect+`
WHERE task_id=$1 AND source='auto' AND scope_hash=$2 AND config_hash=$3 AND seed_hash=$4
ORDER BY id DESC LIMIT 1 FOR SHARE`, taskID, fingerprintPtr.ScopeHash, fingerprintPtr.ConfigHash, fingerprintPtr.SeedHash))
		}
	} else if !skipped {
		job, err = scanKatanaJob(tx.QueryRowContext(ctx, katanaJobSelect+` WHERE task_id=$1 AND source='auto' ORDER BY id DESC LIMIT 1 FOR SHARE`, taskID))
		fingerprintPtr = katanaAutoFingerprintForJob(job)
	}
	if err != nil {
		return nil, err
	}
	if job != nil && !terminalKatanaStatus(job.Status) {
		return nil, ErrDiscoveryReviewNotReady
	}
	currentToken := discoveryReviewToken(taskID, fingerprintPtr, job, skipped)
	if token != currentToken {
		return nil, ErrDiscoveryReviewStale
	}
	status := "not_started"
	var jobID *int64
	if job != nil {
		status = job.Status
		id := job.ID
		jobID = &id
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	review, err := scanDiscoveryReview(tx.QueryRowContext(ctx, `
INSERT INTO discovery_reviews(task_id,katana_job_id,review_token,katana_status,summary,decisions,agent_key)
VALUES($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT(task_id,review_token) DO UPDATE SET
katana_job_id=EXCLUDED.katana_job_id,katana_status=EXCLUDED.katana_status,
summary=EXCLUDED.summary,decisions=EXCLUDED.decisions,agent_key=EXCLUDED.agent_key
RETURNING id,task_id,katana_job_id,review_token,katana_status,summary,decisions,agent_key,created_at,updated_at`,
		taskID, jobID, token, status, summary, raw, agentKey))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return review, nil
}

// DiscoveryReviewRequired is the fail-closed dispatch predicate for ordinary
// goal-driven work. Direct/goalless task injection keeps its existing behavior.
func (d *DB) DiscoveryReviewRequired(ctx context.Context, taskID int64) (bool, error) {
	required, _, err := d.DiscoveryReviewGate(ctx, taskID)
	return required, err
}

// DiscoveryReviewApplies reports whether this task currently has goal-driven
// work. Goalless/direct-injection tasks intentionally retain their historical
// bypass semantics.
func (d *DB) DiscoveryReviewApplies(ctx context.Context, taskID int64) (bool, error) {
	return d.taskHasOpenGoal(ctx, taskID)
}

// DiscoveryReviewGate distinguishes a pending automatic crawl (required but
// not yet reviewable) from a terminal snapshot that is ready for the planner.
func (d *DB) DiscoveryReviewGate(ctx context.Context, taskID int64) (required, reviewable bool, err error) {
	hasOpenGoal, err := d.taskHasOpenGoal(ctx, taskID)
	if err != nil {
		return true, false, err
	}
	if !hasOpenGoal {
		return false, true, nil
	}
	state, err := d.CurrentDiscoveryReviewState(ctx, taskID)
	if err != nil {
		return true, false, err
	}
	return !state.Reviewable || state.Review == nil, state.Reviewable, nil
}

func (d *DB) taskHasOpenGoal(ctx context.Context, taskID int64) (bool, error) {
	var hasOpenGoal bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM tasks task JOIN exploration_nodes node ON node.exploration_id=task.exploration_id
WHERE task.id=$1 AND node.kind='goal' AND node.state='open')`, taskID).Scan(&hasOpenGoal)
	return hasOpenGoal, err
}

func (d *DB) DiscoveryFamilyAllowed(ctx context.Context, taskID int64, family string) (bool, error) {
	hasOpenGoal, err := d.taskHasOpenGoal(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !hasOpenGoal {
		return true, nil
	}
	state, err := d.CurrentDiscoveryReviewState(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !state.Reviewable || state.Review == nil {
		return false, nil
	}
	for _, decision := range state.Review.Decisions {
		if decision.Family == family {
			return decision.Action == "run", nil
		}
	}
	return false, nil
}

func (d *DB) TaskAssetProvenanceCounts(ctx context.Context, taskID int64) ([]TaskAssetProvenanceCount, error) {
	rows, err := d.QueryContext(ctx, `SELECT link.source,asset.type,count(*)
FROM task_asset_links link JOIN assets asset ON asset.id=link.asset_id
WHERE link.task_id=$1 GROUP BY link.source,asset.type ORDER BY link.source,asset.type`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskAssetProvenanceCount
	return collectRowValues(rows, out, func() (TaskAssetProvenanceCount, error) {
		var item TaskAssetProvenanceCount
		err := rows.Scan(&item.Source, &item.AssetType, &item.Count)
		return item, err
	})
}

func (d *DB) TaskScannerJobCounts(ctx context.Context, taskID int64) ([]TaskScannerJobCount, error) {
	rows, err := d.QueryContext(ctx, `SELECT tool,status,count(*) FROM arsenal_jobs
WHERE task_id=$1 GROUP BY tool,status ORDER BY tool,status`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskScannerJobCount
	return collectRowValues(rows, out, func() (TaskScannerJobCount, error) {
		var item TaskScannerJobCount
		err := rows.Scan(&item.Tool, &item.Status, &item.Count)
		return item, err
	})
}
