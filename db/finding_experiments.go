package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Experiment bundles retain the original canonical bytes. Hashing a JSONB
// round-trip would lose the producer's byte representation.
type ExperimentArtifact struct {
	Name string          `json:"name"`
	Hash string          `json:"hash"`
	Data json.RawMessage `json:"data"`
}

type ExperimentBundle struct {
	Format string `json:"format"`
	Job    struct {
		ID         string `json:"job_id"`
		Status     string `json:"status"`
		Complete   bool   `json:"complete"`
		BaseURL    string `json:"base_url"`
		RecipeHash string `json:"source_recipe_hash"`
		Metadata   struct {
			ContractHash   string `json:"contract_hash"`
			TargetRevision string `json:"target_revision"`
		} `json:"metadata"`
	} `json:"job"`
	Artifacts []ExperimentArtifact `json:"artifacts"`
	Events    []struct {
		Sequence int             `json:"sequence"`
		Event    string          `json:"event"`
		Phase    string          `json:"phase"`
		Data     json.RawMessage `json:"data"`
	} `json:"events"`
}

func ExperimentHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

func ValidateExperimentBundle(text, hash string) (*ExperimentBundle, error) {
	if ExperimentHash(text) != hash {
		return nil, errors.New("experiment evidence hash mismatch")
	}
	var bundle ExperimentBundle
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		return nil, err
	}
	if bundle.Format != "uroboros-api-lab-evidence-v1" || bundle.Job.ID == "" || bundle.Job.Status == "running" || bundle.Job.Status == "cancelling" {
		return nil, errors.New("a terminal API Lab evidence bundle is required")
	}
	seen := map[string]bool{}
	for _, artifact := range bundle.Artifacts {
		if artifact.Name == "" || seen[artifact.Name] || ExperimentHash(string(artifact.Data)) != artifact.Hash {
			return nil, errors.New("invalid or duplicate experiment artifact/hash")
		}
		seen[artifact.Name] = true
	}
	return &bundle, nil
}

func (b *ExperimentBundle) Artifact(name string) (ExperimentArtifact, error) {
	for _, artifact := range b.Artifacts {
		if artifact.Name == name {
			return artifact, nil
		}
	}
	return ExperimentArtifact{}, fmt.Errorf("artifact %q not present in experiment", name)
}

type FindingExperiment struct {
	ID           int64     `json:"id"`
	FindingID    int64     `json:"finding_id"`
	Server       string    `json:"server"`
	JobID        string    `json:"job_id"`
	ArtifactName string    `json:"artifact_name"`
	Hash         string    `json:"hash"`
	RecipeHash   string    `json:"recipe_hash"`
	Snapshot     string    `json:"snapshot,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

const experimentCols = `id,finding_id,server,job_id,artifact_name,hash,recipe_hash,snapshot,created_at`

func scanExperiment(row interface{ Scan(...any) error }) (*FindingExperiment, error) {
	e := &FindingExperiment{}
	err := row.Scan(&e.ID, &e.FindingID, &e.Server, &e.JobID, &e.ArtifactName, &e.Hash, &e.RecipeHash, &e.Snapshot, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (d *DB) GetFindingExperiment(ctx context.Context, id int64) (*FindingExperiment, error) {
	return scanExperiment(d.QueryRowContext(ctx, `SELECT `+experimentCols+` FROM finding_experiments WHERE id=$1`, id))
}

func (d *DB) ListFindingExperiments(ctx context.Context, findingID int64) ([]*FindingExperiment, error) {
	return collectQueryState(queriedRows(d.QueryContext(ctx, `SELECT `+experimentCols+` FROM finding_experiments WHERE finding_id=$1 ORDER BY id`, findingID)), func() []*FindingExperiment {
		return []*FindingExperiment{}
	}, func(queryRow rowDecoder, queryState *[]*FindingExperiment) error {
		e, err := scanExperiment(queryRow)
		if err != nil {
			return err
		}
		e.Snapshot = ""
		*queryState = append(*queryState, e)
		return nil
	})
}

// Attachment never changes triage. The finding lock makes retries and evidence
// version increments atomic with the graph reference.
func (d *DB) BindFindingExperiment(ctx context.Context, findingID int64, server, artifactName, snapshot, hash string) (*FindingExperiment, error) {
	bundle, err := ValidateExperimentBundle(snapshot, hash)
	if err != nil {
		return nil, err
	}
	artifact, err := bundle.Artifact(artifactName)
	if err != nil {
		return nil, err
	}
	var recipe struct {
		Plan    json.RawMessage `json:"plan"`
		BaseURL string          `json:"base_url"`
	}
	if err := json.Unmarshal(artifact.Data, &recipe); err != nil || len(recipe.Plan) == 0 || recipe.BaseURL == "" {
		return nil, errors.New("selected artifact must be a complete replay recipe")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var nodeID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT node_id FROM findings WHERE id=$1 FOR UPDATE`, findingID).Scan(&nodeID); err != nil {
		return nil, err
	}
	result, err := scanExperiment(tx.QueryRowContext(ctx, `INSERT INTO finding_experiments(finding_id,server,job_id,artifact_name,hash,recipe_hash,snapshot)
	VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(finding_id,hash,artifact_name) DO NOTHING RETURNING `+experimentCols,
		findingID, server, bundle.Job.ID, artifactName, hash, artifact.Hash, snapshot))
	if err != nil {
		return nil, err
	}
	if result == nil {
		result, err = scanExperiment(tx.QueryRowContext(ctx, `SELECT `+experimentCols+` FROM finding_experiments WHERE finding_id=$1 AND hash=$2 AND artifact_name=$3`, findingID, hash, artifactName))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE findings SET evidence_version=evidence_version+1 WHERE id=$1`, findingID)
		if err == nil && nodeID.Valid {
			ref, _ := json.Marshal([]map[string]any{{"experiment_id": result.ID, "hash": hash, "recipe_hash": artifact.Hash, "job_id": bundle.Job.ID, "artifact_name": artifactName}})
			_, err = tx.ExecContext(ctx, `UPDATE exploration_nodes SET payload=jsonb_set(payload,'{experiment_refs}',COALESCE(payload->'experiment_refs','[]'::jsonb)||$2::jsonb) WHERE id=$1`, nodeID.Int64, string(ref))
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	result.Snapshot = ""
	return result, nil
}

type ExperimentReplay struct {
	RetestID     int64
	ExperimentID int64
	JobID        string
	Snapshot     string
	Hash         string
}

func (d *DB) SaveExperimentReplay(ctx context.Context, retestID, experimentID int64, jobID string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO finding_experiment_replays(retest_id,experiment_id,job_id) VALUES($1,$2,$3) ON CONFLICT(retest_id,job_id) DO NOTHING`, retestID, experimentID, jobID)
	return err
}

func (d *DB) GetExperimentReplay(ctx context.Context, retestID int64, jobID string) (*ExperimentReplay, error) {
	r := &ExperimentReplay{}
	err := d.QueryRowContext(ctx, `SELECT retest_id,experiment_id,job_id,snapshot,hash FROM finding_experiment_replays WHERE retest_id=$1 AND job_id=$2`, retestID, jobID).Scan(&r.RetestID, &r.ExperimentID, &r.JobID, &r.Snapshot, &r.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (d *DB) ArchiveExperimentReplay(ctx context.Context, retestID int64, jobID, snapshot, hash, verdict string) error {
	if _, err := ValidateExperimentBundle(snapshot, hash); err != nil {
		return err
	}
	if verdict != "reproduced" && verdict != "fixed" && verdict != "inconclusive" {
		return errors.New("invalid replay verdict")
	}
	res, err := d.ExecContext(ctx, `UPDATE finding_experiment_replays SET snapshot=$3,hash=$4,verdict=$5 WHERE retest_id=$1 AND job_id=$2 AND (hash='' OR (hash=$4 AND verdict=$5))`, retestID, jobID, snapshot, hash, verdict)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("replay is absent or archived evidence changed")
	}
	return nil
}

func (d *DB) FindingStatusesForExploration(ctx context.Context, explorationID int64) (map[int64]string, error) {
	return collectQueryState(queriedRows(d.QueryContext(ctx, `SELECT f.node_id,f.status FROM findings f JOIN exploration_nodes n ON n.id=f.node_id WHERE n.exploration_id=$1`, explorationID)), func() map[int64]string {
		return map[int64]string{}
	}, func(queryRow rowDecoder, queryState *map[int64]string) error {
		var id int64
		var status string
		if err := queryRow.Scan(&id, &status); err != nil {
			return err
		}
		(*queryState)[id] = status
		return nil
	})
}
