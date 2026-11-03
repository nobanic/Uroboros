package binarybroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	WorkerProtocolVersion = 3
	MaxProjectionBytes    = 2 << 20
	workerHelloFrameSize  = 16 << 10
	workerResultFrameSize = MaxProjectionBytes + 64<<10

	ProjectionSchemaVersion       = 3
	StandardAnalyzer              = "lief+capa+floss"
	StandardAnalyzerVersion       = "1.0.0+9.4.0+3.2.0"
	LIEFVersion                   = "1.0.0"
	LIEFSourceCommit              = "d05b3499b6934137e917c009a4df0a9dc8cb11c5"
	CapaVersion                   = "9.4.0"
	CapaSourceCommit              = "7a79f799a70f052e8382cdbeaec8115daba14a5b"
	CapaRulesVersion              = "9.4.0"
	CapaRulesCommit               = "2af9fbfc1c9b4634dbeb76b5d34fca9389fa7f80"
	CapaRulesManifestSHA256       = "1910582fdcc7fd0bff198cfe07f4f1e3ceaf3acbd0c840d52d6507009d2bb5e2"
	CapaSignaturesManifestSHA256  = "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e"
	FLOSSVersion                  = "3.2.0"
	FLOSSSourceCommit             = "69e8ea21ca78deb2418abb68f86c838d1d2672c7"
	FLOSSSignaturesManifestSHA256 = "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e"
	BinaryNormalizationSchema     = "binary-projection-v3"
	CapaCandidateNormalization    = "capa-candidate-v1"
	FLOSSCandidateNormalization   = "floss-decoded-candidate-v1"
	CapaSummaryStableKey          = "capa:summary:standard-v1"
	FLOSSSummaryStableKey         = "floss:summary:standard-v1"
	LIEFProjectionStableKey       = "lief:projection:standard-v1"
)

var (
	ErrWorkerUnavailable   = errors.New("binary analysis worker is unavailable")
	ErrWorkerProtocol      = errors.New("binary analysis worker protocol failed")
	workerHashPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	workerJobPattern       = regexp.MustCompile(`^binary-job-[0-9a-f]{32}$`)
	workerLeasePattern     = regexp.MustCompile(`^lease-[0-9a-f]{32}$`)
	workerReferencePattern = regexp.MustCompile(`^hot:artifact-[0-9a-f]{32}$`)
	capaStableKeyPattern   = regexp.MustCompile(`^capa:rule:[0-9a-f]{64}:standard-v1$`)
	flossStableKeyPattern  = regexp.MustCompile(`^floss:decoded:[0-9a-f]{64}:standard-v1$`)
)

type WorkerPolicy struct {
	ImageDigest     string            `json:"image_digest"`
	ProfileRevision string            `json:"profile_revision"`
	Analyzer        string            `json:"analyzer"`
	AnalyzerVersion string            `json:"analyzer_version"`
	ToolRulePins    map[string]string `json:"tool_rule_pins"`
	LimitProfile    map[string]int64  `json:"limit_profile"`
}

type AnalysisRequest struct {
	JobID       string `json:"job_id"`
	LeaseNonce  string `json:"lease_nonce"`
	ReferenceID string `json:"reference_id"`
	Fingerprint string `json:"fingerprint"`
}

type AnalysisResult struct {
	JobID                   string          `json:"job_id"`
	LeaseNonce              string          `json:"lease_nonce"`
	Status                  string          `json:"status"`
	ExitClass               string          `json:"exit_class,omitempty"`
	ErrorClass              string          `json:"error_class,omitempty"`
	Error                   string          `json:"error,omitempty"`
	ArtifactSHA256          string          `json:"artifact_sha256"`
	RawResultHash           string          `json:"raw_result_hash,omitempty"`
	CanonicalProjectionHash string          `json:"canonical_projection_hash,omitempty"`
	Projection              json.RawMessage `json:"projection,omitempty"`
	WorkerIdentity          string          `json:"worker_identity"`
	Analyzer                string          `json:"analyzer"`
	AnalyzerVersion         string          `json:"analyzer_version"`
	Truncated               bool            `json:"truncated"`
	StartedAt               time.Time       `json:"started_at"`
	FinishedAt              time.Time       `json:"finished_at"`
}

// ProjectionEnvelope is the only analyzer payload accepted across the worker
// boundary. It deliberately separates LIEF metadata from capa candidates so a
// downstream caller cannot upgrade static capability matches into evidence.
type ProjectionEnvelope struct {
	SchemaVersion   int                  `json:"schema_version"`
	ProfileRevision string               `json:"profile_revision"`
	Artifact        ProjectionArtifact   `json:"artifact"`
	LIEF            ProjectionStage      `json:"lief"`
	Capa            CapaProjectionStage  `json:"capa"`
	FLOSS           FLOSSProjectionStage `json:"floss"`
}

type ProjectionArtifact struct {
	SHA256   string `json:"sha256"`
	ByteSize int64  `json:"byte_size"`
}

type ProjectionStage struct {
	Status          string          `json:"status"`
	AnalyzerVersion string          `json:"analyzer_version"`
	ProjectionHash  string          `json:"projection_hash,omitempty"`
	Record          json.RawMessage `json:"record,omitempty"`
	Truncated       bool            `json:"truncated"`
}

type CapaProjectionStage struct {
	Status          string                    `json:"status"`
	AnalyzerVersion string                    `json:"analyzer_version"`
	ProjectionHash  string                    `json:"projection_hash,omitempty"`
	Summary         json.RawMessage           `json:"summary,omitempty"`
	Candidates      []CapaCandidateProjection `json:"candidates"`
	Truncated       bool                      `json:"truncated"`
}

type CapaCandidateProjection struct {
	StableKey string          `json:"stable_key"`
	Record    json.RawMessage `json:"record"`
	Truncated bool            `json:"truncated"`
}

type FLOSSProjectionStage struct {
	Status          string                     `json:"status"`
	AnalyzerVersion string                     `json:"analyzer_version"`
	ProjectionHash  string                     `json:"projection_hash,omitempty"`
	Summary         json.RawMessage            `json:"summary,omitempty"`
	Candidates      []FLOSSCandidateProjection `json:"candidates"`
	Truncated       bool                       `json:"truncated"`
}

type FLOSSCandidateProjection struct {
	StableKey string          `json:"stable_key"`
	Record    json.RawMessage `json:"record"`
	Truncated bool            `json:"truncated"`
}

type projectionRecordArtifact struct {
	SHA256   string `json:"sha256"`
	ByteSize int64  `json:"byte_size"`
}

type capaAnalysisRecord struct {
	Arch      string `json:"arch"`
	Extractor string `json:"extractor"`
	Format    string `json:"format"`
	OS        string `json:"os"`
}

type capaSummaryRecord struct {
	Analysis                  *capaAnalysisRecord `json:"analysis,omitempty"`
	ArtifactSHA256            string              `json:"artifact_sha256"`
	Authority                 string              `json:"authority"`
	CandidateCount            int                 `json:"candidate_count"`
	DiagnosticsTruncated      bool                `json:"diagnostics_truncated,omitempty"`
	EmittedCount              int                 `json:"emitted_count"`
	ExcludedInternalRuleCount int                 `json:"excluded_internal_rule_count,omitempty"`
	ExcludedLibraryRuleCount  int                 `json:"excluded_library_rule_count,omitempty"`
	ProfileRevision           string              `json:"profile_revision"`
	RawRuleMatchCount         int                 `json:"raw_rule_match_count,omitempty"`
	ReasonCode                string              `json:"reason_code,omitempty"`
	SchemaVersion             int                 `json:"schema_version"`
	Truncated                 bool                `json:"truncated"`
	Verified                  bool                `json:"verified"`
}

type capaCandidateRecord struct {
	ArtifactSHA256  string          `json:"artifact_sha256"`
	Authority       string          `json:"authority"`
	Match           capaMatchRecord `json:"match"`
	ProfileRevision string          `json:"profile_revision"`
	Rule            capaRuleRecord  `json:"rule"`
	SchemaVersion   int             `json:"schema_version"`
	Verified        bool            `json:"verified"`
}

type capaMatchRecord struct {
	Count              int            `json:"count"`
	Locations          []capaLocation `json:"locations"`
	LocationsTruncated bool           `json:"locations_truncated"`
}

type capaLocation struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
}

type capaRuleRecord struct {
	Attack      []map[string]string `json:"attack"`
	Description string              `json:"description,omitempty"`
	MBC         []map[string]string `json:"mbc"`
	Name        string              `json:"name"`
	Namespace   string              `json:"namespace"`
	Scopes      map[string]string   `json:"scopes"`
}

type flossSummaryRecord struct {
	ArtifactSHA256           string `json:"artifact_sha256"`
	Authority                string `json:"authority"`
	CandidateCount           int    `json:"candidate_count"`
	DiagnosticsTruncated     bool   `json:"diagnostics_truncated,omitempty"`
	DuplicateCount           int    `json:"duplicate_count"`
	EmittedCount             int    `json:"emitted_count"`
	FunctionsAnalyzedDecoded int    `json:"functions_analyzed_decoded,omitempty"`
	FunctionsDiscovered      int    `json:"functions_discovered,omitempty"`
	FunctionsLibrary         int    `json:"functions_library,omitempty"`
	ProfileRevision          string `json:"profile_revision"`
	RawDecodedStringCount    int    `json:"raw_decoded_string_count"`
	ReasonCode               string `json:"reason_code,omitempty"`
	SchemaVersion            int    `json:"schema_version"`
	Truncated                bool   `json:"truncated"`
	Verified                 bool   `json:"verified"`
}

type flossCandidateRecord struct {
	ArtifactSHA256  string                   `json:"artifact_sha256"`
	Authority       string                   `json:"authority"`
	DecodedString   flossDecodedStringRecord `json:"decoded_string"`
	Location        flossLocationRecord      `json:"location"`
	ProfileRevision string                   `json:"profile_revision"`
	SchemaVersion   int                      `json:"schema_version"`
	Verified        bool                     `json:"verified"`
}

type flossDecodedStringRecord struct {
	Encoding       string `json:"encoding"`
	OriginalLength int    `json:"original_length"`
	Value          string `json:"value"`
	ValueSHA256    string `json:"value_sha256"`
	ValueTruncated bool   `json:"value_truncated"`
}

type flossLocationRecord struct {
	Address         uint64 `json:"address"`
	AddressType     string `json:"address_type"`
	DecodedAt       uint64 `json:"decoded_at"`
	DecodingRoutine uint64 `json:"decoding_routine"`
}

type workerHello struct {
	Version         int    `json:"version"`
	WorkerIdentity  string `json:"worker_identity"`
	Analyzer        string `json:"analyzer"`
	AnalyzerVersion string `json:"analyzer_version"`
}

type workerAssignment struct {
	Version      int               `json:"version"`
	JobID        string            `json:"job_id"`
	LeaseNonce   string            `json:"lease_nonce"`
	Fingerprint  string            `json:"fingerprint"`
	SHA256       string            `json:"sha256"`
	ExpectedSize int64             `json:"expected_size"`
	Profile      string            `json:"profile_revision"`
	ToolRulePins map[string]string `json:"tool_rule_pins"`
	Limits       map[string]int64  `json:"limit_profile"`
}

type WorkerCoordinator struct {
	store   *Store
	policy  WorkerPolicy
	timeout time.Duration
	ready   chan workerSlot
	mu      sync.Mutex
}

type workerSlot struct {
	connection net.Conn
	done       chan struct{}
}

type fingerprintStringPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type fingerprintIntPair struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

func AnalysisFingerprint(blobHash string, policy WorkerPolicy) (string, error) {
	if !workerHashPattern.MatchString(blobHash) || !validWorkerPolicy(policy) {
		return "", errors.New("binary analysis fingerprint input is invalid")
	}
	stringKeys := make([]string, 0, len(policy.ToolRulePins))
	for key := range policy.ToolRulePins {
		stringKeys = append(stringKeys, key)
	}
	sort.Strings(stringKeys)
	pins := make([]fingerprintStringPair, 0, len(stringKeys))
	for _, key := range stringKeys {
		pins = append(pins, fingerprintStringPair{Key: key, Value: policy.ToolRulePins[key]})
	}
	intKeys := make([]string, 0, len(policy.LimitProfile))
	for key := range policy.LimitProfile {
		intKeys = append(intKeys, key)
	}
	sort.Strings(intKeys)
	limits := make([]fingerprintIntPair, 0, len(intKeys))
	for _, key := range intKeys {
		limits = append(limits, fingerprintIntPair{Key: key, Value: policy.LimitProfile[key]})
	}
	raw, err := json.Marshal(map[string]any{
		"analyzer": policy.Analyzer, "analyzer_version": policy.AnalyzerVersion,
		"blob_sha256": blobHash, "limit_profile": limits, "profile_revision": policy.ProfileRevision,
		"tool_rule_pins": pins, "worker_image_digest": policy.ImageDigest,
	})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func StandardWorkerPolicy(imageDigest string) WorkerPolicy {
	return WorkerPolicy{
		ImageDigest: imageDigest, ProfileRevision: "standard-v1", Analyzer: StandardAnalyzer,
		AnalyzerVersion: StandardAnalyzerVersion,
		ToolRulePins: map[string]string{
			"lief": LIEFVersion, "lief_commit": LIEFSourceCommit,
			"capa": CapaVersion, "capa_commit": CapaSourceCommit,
			"capa_rules": CapaRulesVersion, "capa_rules_commit": CapaRulesCommit,
			"capa_rules_manifest_sha256":      CapaRulesManifestSHA256,
			"capa_signatures_manifest_sha256": CapaSignaturesManifestSHA256,
			"floss":                           FLOSSVersion, "floss_commit": FLOSSSourceCommit,
			"floss_normalization_schema":       FLOSSCandidateNormalization,
			"floss_signatures_manifest_sha256": FLOSSSignaturesManifestSHA256,
			"normalization_schema":             BinaryNormalizationSchema,
			"capa_normalization_schema":        CapaCandidateNormalization,
		},
		LimitProfile: map[string]int64{
			"artifact_bytes": 128 << 20, "projection_bytes": MaxProjectionBytes,
			"lief_projection_bytes": 1 << 20, "capa_projection_bytes": 512 << 10,
			"lief_wall_seconds": 60, "capa_wall_seconds": 240, "wall_seconds": 600,
			"floss_artifact_bytes": 32 << 20, "floss_projection_bytes": 384 << 10,
			"floss_wall_seconds": 240, "floss_candidates": 512, "floss_string_characters": 2048,
			"floss_stdout_bytes": 16 << 20, "floss_stderr_bytes": 1 << 20,
			"sections": 512, "imports": 4096, "exports": 4096, "libraries": 512,
			"capa_candidates": 512, "capa_locations_per_candidate": 64,
			"capa_stdout_bytes": 16 << 20, "capa_stderr_bytes": 1 << 20,
		},
	}
}

func NewWorkerCoordinator(store *Store, policy WorkerPolicy, timeout time.Duration) (*WorkerCoordinator, error) {
	if store == nil || !isStandardWorkerPolicy(policy) {
		return nil, errors.New("binary worker coordinator policy is invalid")
	}
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	return &WorkerCoordinator{store: store, policy: cloneWorkerPolicy(policy), timeout: timeout, ready: make(chan workerSlot, 1)}, nil
}

func isStandardWorkerPolicy(policy WorkerPolicy) bool {
	if !validWorkerPolicy(policy) {
		return false
	}
	expected := StandardWorkerPolicy(policy.ImageDigest)
	if len(policy.ToolRulePins) != len(expected.ToolRulePins) || len(policy.LimitProfile) != len(expected.LimitProfile) {
		return false
	}
	for key, value := range expected.ToolRulePins {
		if policy.ToolRulePins[key] != value {
			return false
		}
	}
	for key, value := range expected.LimitProfile {
		if policy.LimitProfile[key] != value {
			return false
		}
	}
	return true
}

func validWorkerPolicy(policy WorkerPolicy) bool {
	if !strings.HasPrefix(policy.ImageDigest, "sha256:") || len(policy.ImageDigest) != 71 ||
		policy.ProfileRevision != "standard-v1" || policy.Analyzer != StandardAnalyzer ||
		policy.AnalyzerVersion != StandardAnalyzerVersion ||
		len(policy.ToolRulePins) == 0 || len(policy.LimitProfile) == 0 {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(policy.ImageDigest, "sha256:")); err != nil {
		return false
	}
	for key, value := range policy.ToolRulePins {
		if key == "" || value == "" || len(key) > 128 || len(value) > 512 {
			return false
		}
	}
	for key, value := range policy.LimitProfile {
		if key == "" || len(key) > 128 || value <= 0 {
			return false
		}
	}
	return true
}

func cloneWorkerPolicy(policy WorkerPolicy) WorkerPolicy {
	clone := policy
	clone.ToolRulePins = make(map[string]string, len(policy.ToolRulePins))
	for key, value := range policy.ToolRulePins {
		clone.ToolRulePins[key] = value
	}
	clone.LimitProfile = make(map[string]int64, len(policy.LimitProfile))
	for key, value := range policy.LimitProfile {
		clone.LimitProfile[key] = value
	}
	return clone
}

// ServeWorkers accepts a persistent worker-only socket. Every accepted
// connection is authenticated by an exact, fixed identity and is consumed by
// exactly one artifact assignment before it is closed.
func (coordinator *WorkerCoordinator) ServeWorkers(ctx context.Context, listener net.Listener) error {
	if coordinator == nil || listener == nil {
		return errors.New("binary worker listener is not configured")
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	defer close(done)
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err := setWorkerListenerMode(listener, 0); err != nil {
			_ = connection.Close()
			return err
		}
		slot := workerSlot{connection: connection, done: make(chan struct{})}
		if !coordinator.authenticateWorker(connection) {
			_ = connection.Close()
			if err := setWorkerListenerMode(listener, 0o620); err != nil {
				return err
			}
			continue
		}
		select {
		case coordinator.ready <- slot:
		case <-ctx.Done():
			_ = connection.Close()
			return nil
		}
		select {
		case <-slot.done:
		case <-ctx.Done():
			_ = connection.Close()
			return nil
		}
		if err := setWorkerListenerMode(listener, 0o620); err != nil {
			return err
		}
	}
}

func (coordinator *WorkerCoordinator) admitWorker(ctx context.Context, connection net.Conn) {
	if !coordinator.authenticateWorker(connection) {
		_ = connection.Close()
		return
	}
	slot := workerSlot{connection: connection, done: make(chan struct{})}
	select {
	case coordinator.ready <- slot:
	case <-ctx.Done():
		_ = connection.Close()
	}
}

func (coordinator *WorkerCoordinator) authenticateWorker(connection net.Conn) bool {
	_ = connection.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello workerHello
	if err := readFrame(connection, &hello, workerHelloFrameSize); err != nil ||
		hello.Version != WorkerProtocolVersion || hello.WorkerIdentity != coordinator.policy.ImageDigest ||
		hello.Analyzer != coordinator.policy.Analyzer || hello.AnalyzerVersion != coordinator.policy.AnalyzerVersion {
		return false
	}
	_ = connection.SetDeadline(time.Time{})
	return true
}

func setWorkerListenerMode(listener net.Listener, mode os.FileMode) error {
	address, ok := listener.Addr().(*net.UnixAddr)
	if !ok || address == nil || address.Net != "unix" || !strings.HasPrefix(address.Name, "/") {
		return nil
	}
	return os.Chmod(address.Name, mode)
}

func (coordinator *WorkerCoordinator) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResult, error) {
	var empty AnalysisResult
	if coordinator == nil || !workerJobPattern.MatchString(request.JobID) ||
		!workerLeasePattern.MatchString(request.LeaseNonce) ||
		!workerReferencePattern.MatchString(request.ReferenceID) || !workerHashPattern.MatchString(request.Fingerprint) {
		return empty, ErrWorkerProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, coordinator.timeout)
	defer cancel()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	var slot workerSlot
	select {
	case slot = <-coordinator.ready:
	case <-ctx.Done():
		return empty, errors.Join(ErrWorkerUnavailable, ctx.Err())
	}
	connection := slot.connection
	if connection == nil {
		return empty, ErrWorkerUnavailable
	}
	defer close(slot.done)
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()
	reference, file, err := coordinator.store.openReference(request.ReferenceID)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	expectedFingerprint, err := AnalysisFingerprint(reference.SHA256, coordinator.policy)
	if err != nil || request.Fingerprint != expectedFingerprint {
		return empty, fmt.Errorf("%w: analysis fingerprint mismatch", ErrWorkerProtocol)
	}
	if limit := coordinator.policy.LimitProfile["artifact_bytes"]; limit <= 0 || reference.ByteSize > limit {
		return empty, fmt.Errorf("%w: artifact exceeds fixed analysis profile", ErrWorkerProtocol)
	}
	assignment := workerAssignment{
		Version: WorkerProtocolVersion, JobID: request.JobID, LeaseNonce: request.LeaseNonce, Fingerprint: request.Fingerprint,
		SHA256: reference.SHA256, ExpectedSize: reference.ByteSize, Profile: coordinator.policy.ProfileRevision,
		ToolRulePins: cloneStringMap(coordinator.policy.ToolRulePins), Limits: cloneIntMap(coordinator.policy.LimitProfile),
	}
	brokerStartedAt := time.Now().UTC()
	deadline := time.Now().Add(coordinator.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	if err := writeFrame(connection, assignment, maxProtocolRequestFrameSize); err != nil {
		return empty, fmt.Errorf("%w: assignment: %v", ErrWorkerProtocol, err)
	}
	written, err := io.CopyN(connection, file, reference.ByteSize)
	if err != nil || written != reference.ByteSize {
		return empty, fmt.Errorf("%w: artifact stream", ErrWorkerProtocol)
	}
	var result AnalysisResult
	if err := readFrame(connection, &result, workerResultFrameSize); err != nil {
		return empty, fmt.Errorf("%w: result: %v", ErrWorkerProtocol, err)
	}
	result.StartedAt = brokerStartedAt
	result.FinishedAt = time.Now().UTC()
	if err := coordinator.validateResult(request, assignment, result); err != nil {
		return empty, err
	}
	return result, nil
}

func (coordinator *WorkerCoordinator) validateResult(request AnalysisRequest, assignment workerAssignment, result AnalysisResult) error {
	if result.JobID != request.JobID || result.LeaseNonce != assignment.LeaseNonce ||
		result.WorkerIdentity != coordinator.policy.ImageDigest || result.Analyzer != coordinator.policy.Analyzer ||
		result.AnalyzerVersion != coordinator.policy.AnalyzerVersion || result.ArtifactSHA256 != assignment.SHA256 ||
		result.StartedAt.IsZero() || result.FinishedAt.IsZero() || result.FinishedAt.Before(result.StartedAt) ||
		len(result.ExitClass) > 128 || len(result.ErrorClass) > 128 || len(result.Error) > 65536 {
		return ErrWorkerProtocol
	}
	switch result.Status {
	case "completed", "partial", "unsupported":
		if len(result.Projection) == 0 || len(result.Projection) > MaxProjectionBytes || !json.Valid(result.Projection) {
			return ErrWorkerProtocol
		}
		hash := sha256.Sum256(result.Projection)
		if result.CanonicalProjectionHash != hex.EncodeToString(hash[:]) || !workerHashPattern.MatchString(result.RawResultHash) {
			return ErrWorkerProtocol
		}
		envelope, err := ValidateProjection(result.Projection, assignment.SHA256, assignment.ExpectedSize, coordinator.policy)
		if err != nil {
			return fmt.Errorf("%w: projection: %v", ErrWorkerProtocol, err)
		}
		if result.Status != combinedProjectionStatus(envelope) ||
			result.Truncated != (envelope.LIEF.Truncated || envelope.Capa.Truncated || envelope.FLOSS.Truncated) {
			return fmt.Errorf("%w: projection status is inconsistent", ErrWorkerProtocol)
		}
	case "failed":
		if len(result.Projection) != 0 || result.CanonicalProjectionHash != "" ||
			(result.RawResultHash != "" && !workerHashPattern.MatchString(result.RawResultHash)) || result.Truncated {
			return ErrWorkerProtocol
		}
	default:
		return ErrWorkerProtocol
	}
	return nil
}

// ValidateProjection applies the host-owned schema and authority boundary to a
// worker projection. Callers outside the broker may reuse it before mapping the
// envelope into durable observations.
func ValidateProjection(raw json.RawMessage, artifactSHA256 string, artifactBytes int64, policy WorkerPolicy) (ProjectionEnvelope, error) {
	var envelope ProjectionEnvelope
	if !isStandardWorkerPolicy(policy) || len(raw) == 0 || len(raw) > MaxProjectionBytes {
		return envelope, ErrWorkerProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return ProjectionEnvelope{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ProjectionEnvelope{}, errors.New("projection contains trailing data")
	}
	if envelope.SchemaVersion != ProjectionSchemaVersion || envelope.ProfileRevision != policy.ProfileRevision ||
		envelope.Artifact.SHA256 != artifactSHA256 || envelope.Artifact.ByteSize != artifactBytes ||
		!workerHashPattern.MatchString(envelope.Artifact.SHA256) {
		return ProjectionEnvelope{}, errors.New("projection identity is invalid")
	}
	if err := validateLIEFStage(envelope.LIEF, envelope.Artifact, policy); err != nil {
		return ProjectionEnvelope{}, err
	}
	if err := validateCapaStage(envelope.Capa, envelope.Artifact, policy); err != nil {
		return ProjectionEnvelope{}, err
	}
	if err := validateFLOSSStage(envelope.FLOSS, envelope.Artifact, policy); err != nil {
		return ProjectionEnvelope{}, err
	}
	return envelope, nil
}

func validateLIEFStage(stage ProjectionStage, artifact ProjectionArtifact, policy WorkerPolicy) error {
	if stage.AnalyzerVersion != LIEFVersion || !validStageStatus(stage.Status) {
		return errors.New("LIEF stage identity is invalid")
	}
	if stage.Status == "failed" {
		if len(stage.Record) != 0 || stage.ProjectionHash != "" || stage.Truncated {
			return errors.New("failed LIEF stage contains a projection")
		}
		return nil
	}
	if len(stage.Record) == 0 || int64(len(stage.Record)) > policy.LimitProfile["lief_projection_bytes"] ||
		!jsonObject(stage.Record) || !matchesRawHash(stage.Record, stage.ProjectionHash) {
		return errors.New("LIEF stage projection is invalid")
	}
	if (stage.Status == "partial") != stage.Truncated ||
		(stage.Status == "unsupported" && stage.Truncated) {
		return errors.New("LIEF stage status is inconsistent")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stage.Record, &fields); err != nil {
		return errors.New("LIEF record is invalid")
	}
	wanted := []string{"artifact", "profile_revision", "schema_version"}
	if stage.Status == "unsupported" {
		wanted = append(wanted, "unsupported_reason")
	} else {
		wanted = append(wanted, "binary", "exports", "imports", "libraries", "sections", "truncated_categories")
	}
	if !exactJSONFields(fields, wanted...) || !matchesProjectionArtifact(fields["artifact"], artifact) ||
		!rawJSONStringEquals(fields["profile_revision"], policy.ProfileRevision) || !rawJSONIntEquals(fields["schema_version"], 1) {
		return errors.New("LIEF record identity is invalid")
	}
	return nil
}

func validateCapaStage(stage CapaProjectionStage, artifact ProjectionArtifact, policy WorkerPolicy) error {
	if stage.AnalyzerVersion != CapaVersion || !validStageStatus(stage.Status) ||
		len(stage.Candidates) > int(policy.LimitProfile["capa_candidates"]) {
		return errors.New("capa stage identity is invalid")
	}
	if stage.Status == "failed" {
		if len(stage.Summary) != 0 || len(stage.Candidates) != 0 || stage.ProjectionHash != "" || stage.Truncated {
			return errors.New("failed capa stage contains candidates")
		}
		return nil
	}
	if len(stage.Summary) == 0 || !jsonObject(stage.Summary) || !workerHashPattern.MatchString(stage.ProjectionHash) {
		return errors.New("capa summary is invalid")
	}
	var summary capaSummaryRecord
	if err := strictJSON(stage.Summary, &summary); err != nil || !exactCapaSummaryFields(stage.Summary, stage.Status) ||
		summary.ArtifactSHA256 != artifact.SHA256 || summary.Authority != "candidate" || summary.Verified ||
		summary.ProfileRevision != policy.ProfileRevision || summary.SchemaVersion != 1 ||
		summary.CandidateCount < 0 || summary.EmittedCount != len(stage.Candidates) ||
		summary.CandidateCount < summary.EmittedCount || summary.ExcludedInternalRuleCount < 0 ||
		summary.ExcludedLibraryRuleCount < 0 || summary.RawRuleMatchCount < 0 || summary.Truncated != stage.Truncated {
		return errors.New("capa summary authority or identity is invalid")
	}
	if stage.Status == "unsupported" {
		if stage.Truncated || len(stage.Candidates) != 0 || summary.CandidateCount != 0 ||
			summary.ReasonCode == "" || len(summary.ReasonCode) > 128 || containsControl(summary.ReasonCode) ||
			summary.Analysis != nil {
			return errors.New("unsupported capa summary is inconsistent")
		}
	} else if summary.Analysis == nil || summary.ReasonCode != "" ||
		!validCapaAnalysis(summary.Analysis) ||
		summary.RawRuleMatchCount != summary.CandidateCount+summary.ExcludedInternalRuleCount+summary.ExcludedLibraryRuleCount ||
		(stage.Status == "partial") != stage.Truncated ||
		(!stage.Truncated && summary.CandidateCount != summary.EmittedCount) {
		return errors.New("capa stage status is inconsistent")
	}
	used := len(stage.Summary)
	previous := ""
	seen := make(map[string]struct{}, len(stage.Candidates))
	truncationCause := summary.DiagnosticsTruncated || summary.CandidateCount > summary.EmittedCount
	for _, candidate := range stage.Candidates {
		if !capaStableKeyPattern.MatchString(candidate.StableKey) || candidate.StableKey <= previous ||
			!jsonObject(candidate.Record) || len(candidate.Record) > 64<<10 {
			return errors.New("capa candidate is invalid")
		}
		if _, duplicate := seen[candidate.StableKey]; duplicate {
			return errors.New("duplicate capa candidate")
		}
		var record capaCandidateRecord
		if err := strictJSON(candidate.Record, &record); err != nil ||
			!exactCapaCandidateFields(candidate.Record, record) ||
			record.ArtifactSHA256 != artifact.SHA256 || record.Authority != "candidate" || record.Verified ||
			record.ProfileRevision != policy.ProfileRevision || record.SchemaVersion != 1 ||
			candidate.StableKey != expectedCapaStableKey(record) ||
			!validCapaCandidateRecord(record, candidate.Truncated, policy) {
			return errors.New("capa candidate authority or schema is invalid")
		}
		truncationCause = truncationCause || candidate.Truncated
		seen[candidate.StableKey] = struct{}{}
		previous = candidate.StableKey
		used += len(candidate.Record) + len(candidate.StableKey) + 64
	}
	if int64(used) > policy.LimitProfile["capa_projection_bytes"] {
		return errors.New("capa projection exceeds its fixed budget")
	}
	if stage.Status != "unsupported" && stage.Truncated != truncationCause {
		return errors.New("capa truncation metadata is inconsistent")
	}
	if !matchesCapaProjectionHash(stage) {
		return errors.New("capa projection hash is invalid")
	}
	return nil
}

func validateFLOSSStage(stage FLOSSProjectionStage, artifact ProjectionArtifact, policy WorkerPolicy) error {
	if stage.AnalyzerVersion != FLOSSVersion || !validFLOSSStageStatus(stage.Status) ||
		len(stage.Candidates) > int(policy.LimitProfile["floss_candidates"]) {
		return errors.New("FLOSS stage identity is invalid")
	}
	if stage.Status == "failed" {
		if len(stage.Summary) != 0 || len(stage.Candidates) != 0 || stage.ProjectionHash != "" || stage.Truncated {
			return errors.New("failed FLOSS stage contains candidates")
		}
		return nil
	}
	if len(stage.Summary) == 0 || !jsonObject(stage.Summary) || !workerHashPattern.MatchString(stage.ProjectionHash) {
		return errors.New("FLOSS summary is invalid")
	}
	var summary flossSummaryRecord
	if err := strictJSON(stage.Summary, &summary); err != nil || !exactFLOSSSummaryFields(stage.Summary, stage.Status) ||
		summary.ArtifactSHA256 != artifact.SHA256 || summary.Authority != "candidate" || summary.Verified ||
		summary.ProfileRevision != policy.ProfileRevision || summary.SchemaVersion != 1 ||
		summary.CandidateCount < 0 || summary.DuplicateCount < 0 || summary.EmittedCount != len(stage.Candidates) ||
		summary.CandidateCount < summary.EmittedCount || summary.RawDecodedStringCount != summary.CandidateCount+summary.DuplicateCount ||
		summary.Truncated != stage.Truncated {
		return errors.New("FLOSS summary authority or identity is invalid")
	}
	if stage.Status == "unsupported" || stage.Status == "resource_gated" {
		if stage.Truncated || len(stage.Candidates) != 0 || summary.CandidateCount != 0 || summary.DuplicateCount != 0 ||
			summary.RawDecodedStringCount != 0 || summary.ReasonCode == "" || len(summary.ReasonCode) > 128 ||
			containsControl(summary.ReasonCode) {
			return errors.New("unavailable FLOSS summary is inconsistent")
		}
	} else if summary.ReasonCode != "" || summary.FunctionsAnalyzedDecoded < 0 || summary.FunctionsDiscovered < 0 ||
		summary.FunctionsLibrary < 0 || (stage.Status == "partial") != stage.Truncated ||
		(!stage.Truncated && summary.CandidateCount != summary.EmittedCount) {
		return errors.New("FLOSS stage status is inconsistent")
	}
	used := len(stage.Summary)
	previous := ""
	seen := make(map[string]struct{}, len(stage.Candidates))
	truncationCause := summary.DiagnosticsTruncated || summary.CandidateCount > summary.EmittedCount
	for _, candidate := range stage.Candidates {
		if !flossStableKeyPattern.MatchString(candidate.StableKey) || candidate.StableKey <= previous ||
			!jsonObject(candidate.Record) || len(candidate.Record) > 16<<10 {
			return errors.New("FLOSS candidate is invalid")
		}
		if _, duplicate := seen[candidate.StableKey]; duplicate {
			return errors.New("duplicate FLOSS candidate")
		}
		var record flossCandidateRecord
		if err := strictJSON(candidate.Record, &record); err != nil || !exactFLOSSCandidateFields(candidate.Record) ||
			record.ArtifactSHA256 != artifact.SHA256 || record.Authority != "candidate" || record.Verified ||
			record.ProfileRevision != policy.ProfileRevision || record.SchemaVersion != 1 ||
			candidate.StableKey != expectedFLOSSStableKey(record) || !validFLOSSCandidateRecord(record, candidate.Truncated, policy) {
			return errors.New("FLOSS candidate authority or schema is invalid")
		}
		truncationCause = truncationCause || candidate.Truncated
		seen[candidate.StableKey] = struct{}{}
		previous = candidate.StableKey
		used += len(candidate.Record) + len(candidate.StableKey) + 64
	}
	if int64(used) > policy.LimitProfile["floss_projection_bytes"] {
		return errors.New("FLOSS projection exceeds its fixed budget")
	}
	if stage.Status != "unsupported" && stage.Status != "resource_gated" && stage.Truncated != truncationCause {
		return errors.New("FLOSS truncation metadata is inconsistent")
	}
	if !matchesFLOSSProjectionHash(stage) {
		return errors.New("FLOSS projection hash is invalid")
	}
	return nil
}

func exactFLOSSSummaryFields(raw json.RawMessage, status string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	common := []string{"artifact_sha256", "authority", "candidate_count", "duplicate_count", "emitted_count",
		"profile_revision", "raw_decoded_string_count", "schema_version", "truncated", "verified"}
	if status == "unsupported" || status == "resource_gated" {
		return exactJSONFields(fields, append(common, "reason_code")...)
	}
	if status == "failed" {
		return false
	}
	return exactJSONFields(fields, append(common, "diagnostics_truncated", "functions_analyzed_decoded",
		"functions_discovered", "functions_library")...)
}

func exactFLOSSCandidateFields(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !exactJSONFields(fields,
		"artifact_sha256", "authority", "decoded_string", "location", "profile_revision", "schema_version", "verified") {
		return false
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(fields["decoded_string"], &decoded) != nil || !exactJSONFields(decoded,
		"encoding", "original_length", "value", "value_sha256", "value_truncated") {
		return false
	}
	var location map[string]json.RawMessage
	return json.Unmarshal(fields["location"], &location) == nil && exactJSONFields(location,
		"address", "address_type", "decoded_at", "decoding_routine")
}

func expectedFLOSSStableKey(record flossCandidateRecord) string {
	identity := fmt.Sprintf("floss-decoded-v1\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d",
		record.DecodedString.ValueSHA256, record.DecodedString.Encoding, record.Location.AddressType,
		record.Location.Address, record.Location.DecodedAt, record.Location.DecodingRoutine)
	hash := sha256.Sum256([]byte(identity))
	return "floss:decoded:" + hex.EncodeToString(hash[:]) + ":standard-v1"
}

func validFLOSSCandidateRecord(record flossCandidateRecord, truncated bool, policy WorkerPolicy) bool {
	decoded := record.DecodedString
	if decoded.Encoding != "ASCII" && decoded.Encoding != "UTF-16LE" && decoded.Encoding != "UTF-8" {
		return false
	}
	if record.Location.AddressType != "STACK" && record.Location.AddressType != "GLOBAL" && record.Location.AddressType != "HEAP" {
		return false
	}
	if decoded.OriginalLength < 0 || !workerHashPattern.MatchString(decoded.ValueSHA256) ||
		utf8.RuneCountInString(decoded.Value) > int(policy.LimitProfile["floss_string_characters"]) ||
		containsControl(decoded.Value) || decoded.ValueTruncated != truncated {
		return false
	}
	if !decoded.ValueTruncated {
		hash := sha256.Sum256([]byte(decoded.Value))
		if decoded.ValueSHA256 != hex.EncodeToString(hash[:]) {
			return false
		}
	}
	return true
}

func matchesFLOSSProjectionHash(stage FLOSSProjectionStage) bool {
	return workerHashPattern.MatchString(stage.ProjectionHash) && stage.ProjectionHash == flossProjectionHash(stage)
}

func flossProjectionHash(stage FLOSSProjectionStage) string {
	var projection bytes.Buffer
	projection.WriteString(`{"candidates":[`)
	for index, candidate := range stage.Candidates {
		if index > 0 {
			projection.WriteByte(',')
		}
		key, _ := json.Marshal(candidate.StableKey)
		projection.WriteString(`{"record":`)
		projection.Write(candidate.Record)
		projection.WriteString(`,"stable_key":`)
		projection.Write(key)
		projection.WriteString(`,"truncated":`)
		if candidate.Truncated {
			projection.WriteString("true")
		} else {
			projection.WriteString("false")
		}
		projection.WriteByte('}')
	}
	projection.WriteString(`],"summary":`)
	projection.Write(stage.Summary)
	projection.WriteByte('}')
	hash := sha256.Sum256(projection.Bytes())
	return hex.EncodeToString(hash[:])
}

func exactCapaSummaryFields(raw json.RawMessage, status string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	common := []string{"artifact_sha256", "authority", "candidate_count", "emitted_count", "profile_revision", "schema_version", "truncated", "verified"}
	if status == "unsupported" {
		return exactJSONFields(fields, append(common, "reason_code")...)
	}
	if status == "failed" {
		return false
	}
	wanted := append(common, "analysis", "diagnostics_truncated", "excluded_internal_rule_count",
		"excluded_library_rule_count", "raw_rule_match_count")
	if !exactJSONFields(fields, wanted...) {
		return false
	}
	var analysis map[string]json.RawMessage
	return json.Unmarshal(fields["analysis"], &analysis) == nil &&
		exactJSONFields(analysis, "arch", "extractor", "format", "os")
}

func exactCapaCandidateFields(raw json.RawMessage, record capaCandidateRecord) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil ||
		!exactJSONFields(fields, "artifact_sha256", "authority", "match", "profile_revision", "rule", "schema_version", "verified") {
		return false
	}
	var match map[string]json.RawMessage
	if json.Unmarshal(fields["match"], &match) != nil ||
		!exactJSONFields(match, "count", "locations", "locations_truncated") {
		return false
	}
	var rule map[string]json.RawMessage
	if json.Unmarshal(fields["rule"], &rule) != nil {
		return false
	}
	ruleFields := []string{"attack", "mbc", "name", "namespace", "scopes"}
	if record.Rule.Description != "" {
		ruleFields = append(ruleFields, "description")
	}
	if !exactJSONFields(rule, ruleFields...) {
		return false
	}
	var locations []json.RawMessage
	if json.Unmarshal(match["locations"], &locations) != nil || len(locations) != len(record.Match.Locations) {
		return false
	}
	for index, rawLocation := range locations {
		var location map[string]json.RawMessage
		if json.Unmarshal(rawLocation, &location) != nil {
			return false
		}
		wanted := []string{"type"}
		if record.Match.Locations[index].Type != "no address" {
			wanted = append(wanted, "value")
		}
		if !exactJSONFields(location, wanted...) {
			return false
		}
	}
	return true
}

func validCapaAnalysis(analysis *capaAnalysisRecord) bool {
	if analysis == nil {
		return false
	}
	for _, value := range []struct {
		text    string
		maximum int
	}{{analysis.Arch, 64}, {analysis.Extractor, 128}, {analysis.Format, 64}, {analysis.OS, 64}} {
		if len(value.text) > value.maximum || containsControl(value.text) {
			return false
		}
	}
	return true
}

func expectedCapaStableKey(record capaCandidateRecord) string {
	identity := []byte("capa-rule-v1\x00" + record.Rule.Name + "\x00" + record.Rule.Namespace)
	hash := sha256.Sum256(identity)
	return "capa:rule:" + hex.EncodeToString(hash[:]) + ":standard-v1"
}

func combinedProjectionStatus(envelope ProjectionEnvelope) string {
	if envelope.LIEF.Status == "completed" && envelope.Capa.Status == "completed" && envelope.FLOSS.Status == "completed" {
		return "completed"
	}
	if (envelope.LIEF.Status == "unsupported") && (envelope.Capa.Status == "unsupported") &&
		(envelope.FLOSS.Status == "unsupported" || envelope.FLOSS.Status == "resource_gated") {
		return "unsupported"
	}
	return "partial"
}

func strictJSON(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func exactJSONFields(fields map[string]json.RawMessage, wanted ...string) bool {
	if len(fields) != len(wanted) {
		return false
	}
	for _, key := range wanted {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func matchesProjectionArtifact(raw json.RawMessage, expected ProjectionArtifact) bool {
	var artifact projectionRecordArtifact
	return strictJSON(raw, &artifact) == nil && artifact.SHA256 == expected.SHA256 && artifact.ByteSize == expected.ByteSize
}

func rawJSONStringEquals(raw json.RawMessage, expected string) bool {
	var actual string
	return json.Unmarshal(raw, &actual) == nil && actual == expected
}

func rawJSONIntEquals(raw json.RawMessage, expected int) bool {
	var actual int
	return json.Unmarshal(raw, &actual) == nil && actual == expected
}

func validCapaCandidateRecord(record capaCandidateRecord, truncated bool, policy WorkerPolicy) bool {
	if record.Match.Count < 0 || len(record.Match.Locations) > int(policy.LimitProfile["capa_locations_per_candidate"]) ||
		record.Match.Count < len(record.Match.Locations) || record.Match.LocationsTruncated != truncated ||
		record.Rule.Name == "" || len(record.Rule.Name) > 512 || len(record.Rule.Namespace) > 256 ||
		len(record.Rule.Description) > 512 || containsControl(record.Rule.Name) || containsControl(record.Rule.Namespace) ||
		containsControl(record.Rule.Description) || len(record.Rule.Attack) > 32 || len(record.Rule.MBC) > 32 ||
		!validFrameworkRecords(record.Rule.Attack, map[string]bool{"id": true, "tactic": true, "technique": true, "subtechnique": true}) ||
		!validFrameworkRecords(record.Rule.MBC, map[string]bool{"id": true, "objective": true, "behavior": true, "method": true}) {
		return false
	}
	for key, value := range record.Rule.Scopes {
		if (key != "static" && key != "dynamic") || value == "" || len(value) > 64 || containsControl(value) {
			return false
		}
	}
	for _, location := range record.Match.Locations {
		switch location.Type {
		case "no address":
			if len(location.Value) != 0 {
				return false
			}
		case "absolute", "relative", "file", "token", "token offset":
			if !validCapaLocationValue(location.Value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validCapaLocationValue(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > 256 || !json.Valid(raw) {
		return false
	}
	var number uint64
	if json.Unmarshal(raw, &number) == nil {
		return true
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text != "" && len(text) <= 128 && !containsControl(text)
}

func validFrameworkRecords(records []map[string]string, allowed map[string]bool) bool {
	for _, record := range records {
		if len(record) == 0 {
			return false
		}
		for key, value := range record {
			if !allowed[key] || value == "" || len(value) > 256 || containsControl(value) {
				return false
			}
		}
	}
	return true
}

func containsControl(value string) bool {
	for _, character := range value {
		if !unicode.IsPrint(character) {
			return true
		}
	}
	return false
}

func matchesCapaProjectionHash(stage CapaProjectionStage) bool {
	if !workerHashPattern.MatchString(stage.ProjectionHash) {
		return false
	}
	return stage.ProjectionHash == capaProjectionHash(stage)
}

func capaProjectionHash(stage CapaProjectionStage) string {
	var projection bytes.Buffer
	projection.WriteString(`{"candidates":[`)
	for index, candidate := range stage.Candidates {
		if index > 0 {
			projection.WriteByte(',')
		}
		key, _ := json.Marshal(candidate.StableKey)
		projection.WriteString(`{"record":`)
		projection.Write(candidate.Record)
		projection.WriteString(`,"stable_key":`)
		projection.Write(key)
		projection.WriteString(`,"truncated":`)
		if candidate.Truncated {
			projection.WriteString("true")
		} else {
			projection.WriteString("false")
		}
		projection.WriteByte('}')
	}
	projection.WriteString(`],"summary":`)
	projection.Write(stage.Summary)
	projection.WriteByte('}')
	hash := sha256.Sum256(projection.Bytes())
	return hex.EncodeToString(hash[:])
}

func validStageStatus(status string) bool {
	switch status {
	case "completed", "partial", "unsupported", "failed":
		return true
	default:
		return false
	}
}

func validFLOSSStageStatus(status string) bool {
	switch status {
	case "completed", "partial", "unsupported", "resource_gated", "failed":
		return true
	default:
		return false
	}
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func matchesRawHash(raw json.RawMessage, expected string) bool {
	if !workerHashPattern.MatchString(expected) {
		return false
	}
	hash := sha256.Sum256(raw)
	return expected == hex.EncodeToString(hash[:])
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneIntMap(input map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
