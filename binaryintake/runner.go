package binaryintake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/binarybroker"
	"github.com/nobanic/UroborOS/db"
)

var (
	ErrTriageQueueFull = errors.New("binary triage queue is full")
	triageArtifactID   = regexp.MustCompile(`^artifact-[0-9a-f]{32}$`)
	triageJobID        = regexp.MustCompile(`^binary-job-[0-9a-f]{32}$`)
	triageReferenceID  = regexp.MustCompile(`^hot:artifact-[0-9a-f]{32}$`)
	triageFingerprint  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type AnalysisBroker interface {
	Analyze(context.Context, binarybroker.AnalysisRequest) (binarybroker.AnalysisResult, error)
}

type TriageRepository interface {
	MarkBinaryTriageRunning(context.Context, int64, string, string) error
	FinishBinaryTriageJob(context.Context, int64, string, string, db.BinaryTriageFinish) error
}

type Runner struct {
	repo       TriageRepository
	broker     AnalysisBroker
	queue      chan Work
	jobTimeout time.Duration
	OnError    func(Work, error)
	mu         sync.Mutex
	pending    map[string]struct{}
}

func NewRunner(repository TriageRepository, broker AnalysisBroker, queueSize int, jobTimeout time.Duration) (*Runner, error) {
	if repository == nil || broker == nil || queueSize <= 0 || queueSize > 4096 || jobTimeout <= 0 {
		return nil, ErrNotConfigured
	}
	return &Runner{repo: repository, broker: broker, queue: make(chan Work, queueSize),
		jobTimeout: jobTimeout, pending: make(map[string]struct{})}, nil
}

func (runner *Runner) Enqueue(work Work) error {
	if runner == nil || !validWork(work) {
		return ErrInvalidProvenance
	}
	runner.mu.Lock()
	if _, exists := runner.pending[work.JobID]; exists {
		runner.mu.Unlock()
		return nil
	}
	runner.pending[work.JobID] = struct{}{}
	select {
	case runner.queue <- work:
		runner.mu.Unlock()
		return nil
	default:
		delete(runner.pending, work.JobID)
		runner.mu.Unlock()
		return ErrTriageQueueFull
	}
}

func (runner *Runner) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case work := <-runner.queue:
			if err := runner.Process(ctx, work); err != nil && runner.OnError != nil {
				runner.OnError(work, err)
			}
			runner.mu.Lock()
			delete(runner.pending, work.JobID)
			runner.mu.Unlock()
		}
	}
}

func (runner *Runner) Process(parent context.Context, work Work) error {
	if runner == nil || runner.repo == nil || runner.broker == nil {
		return ErrNotConfigured
	}
	if !validWork(work) {
		return ErrInvalidProvenance
	}
	ctx, cancel := context.WithTimeout(parent, runner.jobTimeout)
	defer cancel()
	leaseNonce, err := newTriageLease()
	if err != nil {
		return err
	}
	if err := runner.repo.MarkBinaryTriageRunning(ctx, work.TaskID, work.JobID, leaseNonce); err != nil {
		return fmt.Errorf("claim binary triage job: %w", err)
	}
	claimedAt := time.Now().UTC()
	result, err := runner.broker.Analyze(ctx, binarybroker.AnalysisRequest{
		JobID: work.JobID, LeaseNonce: leaseNonce, ReferenceID: work.ReferenceID, Fingerprint: work.Fingerprint,
	})
	if err != nil {
		message := err.Error()
		if len(message) > 65536 {
			message = message[:65536]
		}
		status, exitClass, errorClass := "failed", "broker_failure", "broker_or_worker"
		if parent.Err() != nil {
			status, exitClass, errorClass = "interrupted", "host_shutdown", "context_canceled"
		}
		finishErr := runner.finish(context.WithoutCancel(parent), work, leaseNonce, db.BinaryTriageFinish{
			Status: status, ExitClass: exitClass, ErrorClass: errorClass, Error: message,
			StartedAt: claimedAt, FinishedAt: time.Now().UTC(),
		})
		return errors.Join(fmt.Errorf("analyze binary artifact: %w", err), finishErr)
	}
	if result.JobID != work.JobID || result.LeaseNonce != leaseNonce {
		protocolErr := errors.New("broker returned mismatched binary triage identity")
		finishErr := runner.finish(context.WithoutCancel(parent), work, leaseNonce, db.BinaryTriageFinish{
			Status: "failed", ExitClass: "broker_failure", ErrorClass: "identity_mismatch", Error: protocolErr.Error(),
			StartedAt: claimedAt, FinishedAt: time.Now().UTC(),
		})
		return errors.Join(protocolErr, finishErr)
	}
	finish := db.BinaryTriageFinish{
		Status: result.Status, ExitClass: result.ExitClass, ErrorClass: result.ErrorClass, Error: result.Error,
		RawResultHash: result.RawResultHash, CanonicalProjectionHash: result.CanonicalProjectionHash,
		StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
	}
	if len(result.Projection) > 0 {
		var untrusted binarybroker.ProjectionEnvelope
		if err := json.Unmarshal(result.Projection, &untrusted); err != nil {
			return runner.failInvalidProjection(parent, work, leaseNonce, claimedAt, err)
		}
		policy := binarybroker.StandardWorkerPolicy(result.WorkerIdentity)
		envelope, err := binarybroker.ValidateProjection(result.Projection, result.ArtifactSHA256,
			untrusted.Artifact.ByteSize, policy)
		if err != nil {
			return runner.failInvalidProjection(parent, work, leaseNonce, claimedAt, err)
		}
		observations, err := projectionObservations(envelope, result.WorkerIdentity)
		if err != nil {
			return runner.failInvalidProjection(parent, work, leaseNonce, claimedAt, err)
		}
		finish.Observations = observations
	}
	if err := runner.finish(context.WithoutCancel(parent), work, leaseNonce, finish); err != nil {
		return fmt.Errorf("persist binary triage result: %w", err)
	}
	return nil
}

func (runner *Runner) failInvalidProjection(parent context.Context, work Work, leaseNonce string, startedAt time.Time, cause error) error {
	protocolErr := fmt.Errorf("invalid binary worker projection: %w", cause)
	finishErr := runner.finish(context.WithoutCancel(parent), work, leaseNonce, db.BinaryTriageFinish{
		Status: "failed", ExitClass: "broker_failure", ErrorClass: "invalid_projection", Error: protocolErr.Error(),
		StartedAt: startedAt, FinishedAt: time.Now().UTC(),
	})
	return errors.Join(protocolErr, finishErr)
}

func projectionObservations(envelope binarybroker.ProjectionEnvelope, workerIdentity string) ([]db.BinaryObservationInput, error) {
	liefIdentity, err := json.Marshal(map[string]any{
		"worker_image_digest": workerIdentity, "analyzer": "lief", "analyzer_version": binarybroker.LIEFVersion,
		"source_commit": binarybroker.LIEFSourceCommit, "profile_revision": ProfileRevision,
		"normalization_schema": binarybroker.BinaryNormalizationSchema,
	})
	if err != nil {
		return nil, err
	}
	capaIdentity, err := json.Marshal(map[string]any{
		"worker_image_digest": workerIdentity, "analyzer": "capa", "analyzer_version": binarybroker.CapaVersion,
		"source_commit": binarybroker.CapaSourceCommit, "rules_version": binarybroker.CapaRulesVersion,
		"rules_commit": binarybroker.CapaRulesCommit, "rules_manifest_sha256": binarybroker.CapaRulesManifestSHA256,
		"signatures_manifest_sha256": binarybroker.CapaSignaturesManifestSHA256,
		"profile_revision":           ProfileRevision, "normalization_schema": binarybroker.CapaCandidateNormalization,
	})
	if err != nil {
		return nil, err
	}
	flossIdentity, err := json.Marshal(map[string]any{
		"worker_image_digest": workerIdentity, "analyzer": "floss", "analyzer_version": binarybroker.FLOSSVersion,
		"source_commit":              binarybroker.FLOSSSourceCommit,
		"signatures_manifest_sha256": binarybroker.FLOSSSignaturesManifestSHA256,
		"profile_revision":           ProfileRevision, "normalization_schema": binarybroker.FLOSSCandidateNormalization,
	})
	if err != nil {
		return nil, err
	}
	observations := make([]db.BinaryObservationInput, 0, len(envelope.Capa.Candidates)+len(envelope.FLOSS.Candidates)+3)
	if envelope.LIEF.Status != "failed" {
		observations = append(observations, db.BinaryObservationInput{
			Analyzer: "lief", Kind: "structural_projection", StableKey: binarybroker.LIEFProjectionStableKey,
			Authority: "metadata", Record: append(json.RawMessage(nil), envelope.LIEF.Record...),
			ToolRuleIdentity: liefIdentity, Truncated: envelope.LIEF.Truncated,
		})
	}
	if envelope.Capa.Status != "failed" {
		observations = append(observations, db.BinaryObservationInput{
			Analyzer: "capa", Kind: "capability_summary", StableKey: binarybroker.CapaSummaryStableKey,
			Authority: "candidate", Record: append(json.RawMessage(nil), envelope.Capa.Summary...),
			ToolRuleIdentity: capaIdentity, Truncated: envelope.Capa.Truncated,
		})
		for _, candidate := range envelope.Capa.Candidates {
			observations = append(observations, db.BinaryObservationInput{
				Analyzer: "capa", Kind: "capability_match", StableKey: candidate.StableKey,
				Authority: "candidate", Record: append(json.RawMessage(nil), candidate.Record...),
				ToolRuleIdentity: capaIdentity, Truncated: candidate.Truncated,
			})
		}
	}
	if envelope.FLOSS.Status != "failed" {
		observations = append(observations, db.BinaryObservationInput{
			Analyzer: "floss", Kind: "decoded_string_summary", StableKey: binarybroker.FLOSSSummaryStableKey,
			Authority: "candidate", Record: append(json.RawMessage(nil), envelope.FLOSS.Summary...),
			ToolRuleIdentity: flossIdentity, Truncated: envelope.FLOSS.Truncated,
		})
		for _, candidate := range envelope.FLOSS.Candidates {
			observations = append(observations, db.BinaryObservationInput{
				Analyzer: "floss", Kind: "decoded_string_candidate", StableKey: candidate.StableKey,
				Authority: "candidate", Record: append(json.RawMessage(nil), candidate.Record...),
				ToolRuleIdentity: flossIdentity, Truncated: candidate.Truncated,
			})
		}
	}
	return observations, nil
}

func validWork(work Work) bool {
	return work.TaskID > 0 && triageArtifactID.MatchString(work.ArtifactID) &&
		triageJobID.MatchString(work.JobID) && triageReferenceID.MatchString(work.ReferenceID) &&
		triageFingerprint.MatchString(work.Fingerprint)
}

func (runner *Runner) finish(parent context.Context, work Work, leaseNonce string, finish db.BinaryTriageFinish) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return runner.repo.FinishBinaryTriageJob(ctx, work.TaskID, work.JobID, leaseNonce, finish)
}

func newTriageLease() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(raw[:]), nil
}
