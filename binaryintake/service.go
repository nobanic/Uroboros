// Package binaryintake is the only application-side path from task-owned
// traffic or an authenticated operator stream into the private binary broker.
// It never accepts a host path and never exposes artifact bytes after staging.
package binaryintake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	pathpkg "path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/binaryartifact"
	"github.com/nobanic/UroborOS/binarybroker"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
)

const (
	AutomaticAnalysisMaxBytes = int64(128 << 20)
	ProfileRevision           = db.BinaryTriageProfileRevision
)

var (
	ErrNotConfigured     = errors.New("binary intake is not configured")
	ErrInvalidProvenance = errors.New("binary intake provenance is invalid")
	ErrIncompleteBody    = errors.New("binary artifact body is incomplete")
	ErrPartialContent    = errors.New("partial HTTP content is not admissible")
	ErrBrokerReceipt     = errors.New("binary broker returned an invalid receipt")
	ErrWorkerIdentity    = errors.New("binary worker identity is not pinned")
)

type EvidenceReader interface {
	ReadEvidence(context.Context, []string, func(traffic.EvidenceExchange) error) error
}

type Repository interface {
	RegisterBinaryArtifact(context.Context, db.BinaryArtifactRegistration) (db.BinaryArtifactRegistrationResult, error)
	BinaryArtifactForTask(context.Context, int64, string) (*db.BinaryArtifact, error)
	ListBinaryBrokerReferences(context.Context) ([]binaryartifact.BrokerReference, error)
	SetBinaryBrokerReferenceState(context.Context, string, string, string) error
	CreateBinaryTriageJob(context.Context, db.BinaryTriageJobCreate) (*db.BinaryTriageJob, bool, error)
}

func (service *Service) ScheduleArtifact(ctx context.Context, taskID int64, artifactID string) (*db.BinaryTriageJob, error) {
	if service == nil || service.repo == nil || service.scheduler == nil || taskID <= 0 || artifactID == "" {
		return nil, ErrNotConfigured
	}
	artifact, err := service.repo.BinaryArtifactForTask(ctx, taskID, artifactID)
	if err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, fmt.Errorf("%w: artifact is not owned by this task", ErrInvalidProvenance)
	}
	fingerprint, err := service.fingerprint(artifact.BlobSHA256)
	if err != nil {
		return nil, err
	}
	job, _, err := service.repo.CreateBinaryTriageJob(ctx, db.BinaryTriageJobCreate{
		TaskID: taskID, ArtifactID: artifactID, Fingerprint: fingerprint,
		WorkerImageDigest: service.config.WorkerImageDigest, ToolRulePins: service.config.ToolRulePins,
		LimitProfile: service.config.LimitProfile, ResourceGated: artifact.ByteSize > AutomaticAnalysisMaxBytes,
	})
	if err != nil {
		return nil, err
	}
	if job != nil && job.Status == "queued" {
		if err := service.scheduler.Enqueue(Work{TaskID: taskID, ArtifactID: artifactID, JobID: job.JobID,
			ReferenceID: "hot:" + artifactID, Fingerprint: job.Fingerprint}); err != nil {
			return job, err
		}
	}
	return job, nil
}

type Broker interface {
	binaryartifact.BrokerClient
	Stage(context.Context, io.Reader, int64) (binarybroker.Receipt, error)
	DiscardReceipt(context.Context, string) error
}

type Config struct {
	WorkerImageDigest string
	ToolRulePins      map[string]string
	LimitProfile      map[string]int64
}

type Service struct {
	evidence  EvidenceReader
	repo      Repository
	broker    Broker
	config    Config
	scheduler Scheduler
}

type TrafficOrigin struct {
	TaskID        int64
	TrafficID     string
	ProducerKind  string
	ProducerJobID string
}

type UploadOrigin struct {
	TaskID      int64
	Name        string
	ContentType string
}

type Work struct {
	TaskID      int64
	ArtifactID  string
	JobID       string
	ReferenceID string
	Fingerprint string
}

type Scheduler interface {
	Enqueue(Work) error
}

type Result struct {
	Disposition string                    `json:"disposition"`
	Reason      string                    `json:"reason,omitempty"`
	Detection   *binaryartifact.Detection `json:"detection,omitempty"`
	Artifact    *db.BinaryArtifact        `json:"artifact,omitempty"`
	Job         *db.BinaryTriageJob       `json:"job,omitempty"`
	Created     bool                      `json:"created"`
}

func New(evidence EvidenceReader, repository Repository, broker Broker, config Config, schedulers ...Scheduler) (*Service, error) {
	if repository == nil || broker == nil {
		return nil, ErrNotConfigured
	}
	config.WorkerImageDigest = strings.TrimSpace(config.WorkerImageDigest)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	config.ToolRulePins = cloneStringPolicy(config.ToolRulePins)
	config.LimitProfile = cloneIntPolicy(config.LimitProfile)
	var scheduler Scheduler
	if len(schedulers) > 1 {
		return nil, ErrNotConfigured
	}
	if len(schedulers) == 1 {
		scheduler = schedulers[0]
	}
	return &Service{evidence: evidence, repo: repository, broker: broker, config: config, scheduler: scheduler}, nil
}

func validateConfig(config Config) error {
	digest := config.WorkerImageDigest
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return ErrWorkerIdentity
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
		return ErrWorkerIdentity
	}
	if len(config.ToolRulePins) == 0 || len(config.LimitProfile) == 0 {
		return ErrWorkerIdentity
	}
	for key, value := range config.ToolRulePins {
		if sanitizeText(key, 128) != key || sanitizeText(value, 512) != value || key == "" || value == "" {
			return ErrWorkerIdentity
		}
	}
	for key, value := range config.LimitProfile {
		if sanitizeText(key, 128) != key || key == "" || value <= 0 {
			return ErrWorkerIdentity
		}
	}
	expected := binarybroker.StandardWorkerPolicy(digest)
	if len(config.ToolRulePins) != len(expected.ToolRulePins) || len(config.LimitProfile) != len(expected.LimitProfile) {
		return ErrWorkerIdentity
	}
	for key, value := range expected.ToolRulePins {
		if config.ToolRulePins[key] != value {
			return ErrWorkerIdentity
		}
	}
	for key, value := range expected.LimitProfile {
		if config.LimitProfile[key] != value {
			return ErrWorkerIdentity
		}
	}
	return nil
}

func cloneStringPolicy(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneIntPolicy(input map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

// AdmitTraffic consumes exactly one already committed exchange. ProducerKind
// is code-owned: native http_request has no producer job; Katana and Arsenal
// must supply the exact task-scoped job identifier that selected TrafficID.
func (service *Service) AdmitTraffic(ctx context.Context, origin TrafficOrigin) (Result, error) {
	if service == nil || service.evidence == nil || service.repo == nil || service.broker == nil {
		return Result{}, ErrNotConfigured
	}
	if err := validateTrafficOrigin(origin); err != nil {
		return Result{}, err
	}
	var result Result
	count := 0
	err := service.evidence.ReadEvidence(ctx, []string{origin.TrafficID}, func(exchange traffic.EvidenceExchange) error {
		count++
		if count != 1 || exchange.ID != origin.TrafficID {
			return ErrInvalidProvenance
		}
		if exchange.Status == 206 || responseHasContentRange(exchange.RespHead) {
			result = Result{Disposition: "rejected", Reason: "partial_content"}
			return ErrPartialContent
		}
		if exchange.RespLen == 0 {
			result = Result{Disposition: "skipped", Reason: "empty_content"}
			return nil
		}
		metadata := originMetadata{
			Kind: "traffic_response", TrafficID: origin.TrafficID, ProducerKind: origin.ProducerKind,
			ProducerJobID: origin.ProducerJobID, URL: sanitizeURL(exchange.URL),
			Name: nameFromURL(exchange.URL), ContentType: sanitizeContentType(exchange.ContentType),
		}
		admitted, admitErr := service.admit(ctx, origin.TaskID, exchange.Response, exchange.RespLen, metadata)
		result = admitted
		return admitErr
	})
	if err != nil {
		return result, err
	}
	if count != 1 {
		return Result{}, ErrInvalidProvenance
	}
	return result, nil
}

// AdmitUpload is the authenticated operator fallback. reader is the raw HTTP
// request body; callers must require a positive Content-Length and must not
// materialize multipart data, workspace files, or host paths.
func (service *Service) AdmitUpload(ctx context.Context, origin UploadOrigin, reader io.Reader, size int64) (Result, error) {
	if service == nil || service.repo == nil || service.broker == nil {
		return Result{}, ErrNotConfigured
	}
	if origin.TaskID <= 0 || reader == nil || size <= 0 || size > binarybroker.MaxArtifactBytes {
		return Result{}, ErrInvalidProvenance
	}
	metadata := originMetadata{Kind: "operator_upload", ProducerKind: "operator_api",
		Name: sanitizeText(origin.Name, 512), ContentType: sanitizeContentType(origin.ContentType)}
	return service.admit(ctx, origin.TaskID, reader, size, metadata)
}

type originMetadata struct {
	Kind, TrafficID, ProducerKind, ProducerJobID, URL, Name, ContentType string
}

func (service *Service) admit(ctx context.Context, taskID int64, reader io.Reader, size int64, metadata originMetadata) (Result, error) {
	if taskID <= 0 || reader == nil || size <= 0 || size > binarybroker.MaxArtifactBytes {
		return Result{}, ErrIncompleteBody
	}
	prefixSize := size
	if prefixSize > binaryartifact.ContentProbeLimit {
		prefixSize = binaryartifact.ContentProbeLimit
	}
	prefix := make([]byte, int(prefixSize))
	if _, err := io.ReadFull(reader, prefix); err != nil {
		return Result{}, fmt.Errorf("%w: read content probe: %v", ErrIncompleteBody, err)
	}
	detection, err := binaryartifact.Detect(prefix, size)
	if errors.Is(err, binaryartifact.ErrUnsupportedBinaryFormat) {
		return Result{Disposition: "skipped", Reason: "unsupported_content"}, nil
	}
	if err != nil {
		return Result{Disposition: "rejected", Reason: "malformed_binary"}, err
	}
	hasher := sha256.New()
	stream := io.TeeReader(io.MultiReader(bytes.NewReader(prefix), reader), hasher)
	receipt, err := service.broker.Stage(ctx, stream, size)
	if err != nil {
		return Result{}, fmt.Errorf("stage binary artifact: %w", err)
	}
	discard := true
	defer func() {
		if discard {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = service.broker.DiscardReceipt(cleanupCtx, receipt.ID)
		}
	}()
	if err := requireReaderEOF(reader); err != nil {
		return Result{}, ErrIncompleteBody
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))
	if receipt.SHA256 != contentHash || receipt.ByteSize != size || receipt.StorageVersion != binarybroker.StorageVersion || receipt.ID == "" {
		return Result{}, ErrBrokerReceipt
	}
	provenanceHash, err := hashProvenance(taskID, contentHash, metadata)
	if err != nil {
		return Result{}, err
	}
	registration, err := service.repo.RegisterBinaryArtifact(ctx, db.BinaryArtifactRegistration{
		TaskID: taskID, BlobSHA256: contentHash, ByteSize: size, StorageVersion: binarybroker.StorageVersion,
		ReceiptID: receipt.ID, Detection: detection,
		Origin: db.BinaryArtifactOriginInput{
			Kind: metadata.Kind, TrafficID: metadata.TrafficID, ProducerKind: metadata.ProducerKind,
			ProducerJobID: metadata.ProducerJobID, SanitizedURL: metadata.URL, SanitizedName: metadata.Name,
			ContentType: metadata.ContentType, ProvenanceHash: provenanceHash,
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("register binary artifact: %w", err)
	}
	if !registration.ReceiptNeeded {
		if err := service.broker.DiscardReceipt(ctx, receipt.ID); err != nil {
			return Result{}, fmt.Errorf("discard duplicate binary receipt: %w", err)
		}
		discard = false
	} else {
		// The database receipt now roots the staged object across a crash. Do not
		// discard it if reconciliation fails; the next startup can retry it.
		discard = false
	}
	if _, err := binaryartifact.ReconcileReferences(ctx, service.repo, service.broker); err != nil {
		return Result{}, fmt.Errorf("activate binary broker reference: %w", err)
	}
	fingerprint, err := service.fingerprint(contentHash)
	if err != nil {
		return Result{}, err
	}
	job, _, err := service.repo.CreateBinaryTriageJob(ctx, db.BinaryTriageJobCreate{
		TaskID: taskID, ArtifactID: registration.Artifact.ArtifactID, Fingerprint: fingerprint,
		WorkerImageDigest: service.config.WorkerImageDigest, ToolRulePins: service.config.ToolRulePins,
		LimitProfile: service.config.LimitProfile, ResourceGated: size > AutomaticAnalysisMaxBytes,
	})
	if err != nil {
		return Result{}, fmt.Errorf("schedule binary triage: %w", err)
	}
	result := Result{Disposition: "admitted", Detection: &detection, Artifact: &registration.Artifact,
		Job: job, Created: registration.Created}
	if job != nil && job.Status == "queued" && service.scheduler != nil {
		if err := service.scheduler.Enqueue(Work{TaskID: taskID, JobID: job.JobID,
			ArtifactID: registration.Artifact.ArtifactID, ReferenceID: registration.Reference.ReferenceID,
			Fingerprint: job.Fingerprint}); err != nil {
			return result, fmt.Errorf("enqueue binary triage: %w", err)
		}
	}
	return result, nil
}

func requireReaderEOF(reader io.Reader) error {
	var trailing [1]byte
	for attempt := 0; attempt < 3; attempt++ {
		n, err := reader.Read(trailing[:])
		if n > 0 {
			return ErrIncompleteBody
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return io.ErrNoProgress
}

func responseHasContentRange(header string) bool {
	if len(header) > 1<<20 {
		return true
	}
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, _, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "Content-Range") {
			return true
		}
	}
	return false
}

func validateTrafficOrigin(origin TrafficOrigin) error {
	if origin.TaskID <= 0 || sanitizeText(origin.TrafficID, 256) != origin.TrafficID || origin.TrafficID == "" ||
		sanitizeText(origin.ProducerKind, 64) != origin.ProducerKind ||
		sanitizeText(origin.ProducerJobID, 256) != origin.ProducerJobID {
		return ErrInvalidProvenance
	}
	switch origin.ProducerKind {
	case "http_request":
		if origin.ProducerJobID != "" {
			return ErrInvalidProvenance
		}
	case "katana", "arsenal":
		if origin.ProducerJobID == "" {
			return ErrInvalidProvenance
		}
	default:
		return ErrInvalidProvenance
	}
	return nil
}

func (service *Service) fingerprint(blobHash string) (string, error) {
	return binarybroker.AnalysisFingerprint(blobHash, binarybroker.WorkerPolicy{
		ImageDigest: service.config.WorkerImageDigest, ProfileRevision: ProfileRevision,
		Analyzer: binarybroker.StandardAnalyzer, AnalyzerVersion: binarybroker.StandardAnalyzerVersion,
		ToolRulePins: service.config.ToolRulePins, LimitProfile: service.config.LimitProfile,
	})
}

func hashProvenance(taskID int64, blobHash string, metadata originMetadata) (string, error) {
	raw, err := json.Marshal(struct {
		TaskID                                                      int64
		BlobHash, Kind, TrafficID, ProducerKind, ProducerJobID, URL string
		Name, ContentType                                           string
	}{taskID, blobHash, metadata.Kind, metadata.TrafficID, metadata.ProducerKind,
		metadata.ProducerJobID, metadata.URL, metadata.Name, metadata.ContentType})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sanitizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		query[key] = []string{""}
	}
	parsed.RawQuery = query.Encode()
	return sanitizeText(parsed.String(), 4096)
}

func nameFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return ""
	}
	name, err := url.PathUnescape(pathpkg.Base(parsed.EscapedPath()))
	if err != nil || name == "." || name == "/" {
		return ""
	}
	return sanitizeText(name, 512)
}

func sanitizeContentType(raw string) string {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mediaType = strings.Split(raw, ";")[0]
	}
	return strings.ToLower(sanitizeText(strings.TrimSpace(mediaType), 255))
}

func sanitizeText(raw string, maximum int) string {
	raw = strings.ToValidUTF8(strings.TrimSpace(raw), "?")
	var builder strings.Builder
	for _, character := range raw {
		if unicode.IsControl(character) {
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > maximum {
			break
		}
		builder.WriteRune(character)
	}
	return builder.String()
}
