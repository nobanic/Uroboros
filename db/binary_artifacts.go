package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/nobanic/UroborOS/binaryartifact"
)

var (
	binarySHA256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	binaryArtifactIDPattern = regexp.MustCompile(`^artifact-[0-9a-f]{32}$`)
	binaryBrokerIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
)

var (
	ErrBinaryArtifactInput     = errors.New("invalid binary artifact registration")
	ErrBinaryReferenceNotFound = errors.New("binary broker reference not found")
)

type BinaryArtifactOriginInput struct {
	Kind           string
	TrafficID      string
	ProducerKind   string
	ProducerJobID  string
	SanitizedURL   string
	SanitizedName  string
	ContentType    string
	ProvenanceHash string
}

type BinaryArtifactRegistration struct {
	TaskID         int64
	BlobSHA256     string
	ByteSize       int64
	StorageVersion int
	ReceiptID      string
	Detection      binaryartifact.Detection
	Origin         BinaryArtifactOriginInput
}

type BinaryArtifact struct {
	ArtifactID       string
	TaskID           int64
	BlobSHA256       string
	ByteSize         int64
	State            string
	QuarantineState  string
	DetectedKind     string
	BinaryFormat     string
	Architecture     string
	Bitness          int
	Endianness       string
	RetentionClass   string
	ExecutionAllowed bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type BinaryArtifactRegistrationResult struct {
	Artifact  BinaryArtifact
	Reference binaryartifact.BrokerReference
	Created   bool
	// ReceiptNeeded is false when an existing durable/pending reference already
	// owns the artifact. In that case the caller must discard its newly staged
	// receipt through the broker so it does not linger until receipt GC.
	ReceiptNeeded bool
}

func newBinaryOpaqueID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func binaryHotReferenceID(artifactID string) string { return "hot:" + artifactID }

func binaryColdReferenceID(archiveID int64, artifactID string) string {
	return fmt.Sprintf("cold:%d:%s", archiveID, artifactID)
}

func validateBinaryRegistration(input BinaryArtifactRegistration) error {
	if input.TaskID <= 0 || !binarySHA256Pattern.MatchString(input.BlobSHA256) || input.ByteSize < 0 || input.ByteSize > 512<<20 || input.StorageVersion != 1 || !binaryBrokerIDPattern.MatchString(input.ReceiptID) {
		return ErrBinaryArtifactInput
	}
	if err := validateBinaryDetection(input.Detection); err != nil {
		return err
	}
	origin := input.Origin
	if !binarySHA256Pattern.MatchString(origin.ProvenanceHash) {
		return ErrBinaryArtifactInput
	}
	switch origin.Kind {
	case "traffic_response":
		if strings.TrimSpace(origin.TrafficID) == "" {
			return ErrBinaryArtifactInput
		}
	case "operator_upload", "repository_build":
	default:
		return ErrBinaryArtifactInput
	}
	limits := []struct {
		value string
		limit int
	}{
		{origin.TrafficID, 256}, {origin.ProducerKind, 64}, {origin.ProducerJobID, 256},
		{origin.SanitizedURL, 4096}, {origin.SanitizedName, 512}, {origin.ContentType, 255},
	}
	for _, field := range limits {
		if len(field.value) > field.limit || containsBinaryMetadataControl(field.value) {
			return ErrBinaryArtifactInput
		}
	}
	return nil
}

func validateBinaryDetection(detection binaryartifact.Detection) error {
	switch detection.Kind {
	case "native_binary":
	case "managed_binary":
		if detection.Format != "pe" {
			return ErrBinaryArtifactInput
		}
	default:
		return ErrBinaryArtifactInput
	}
	switch detection.Format {
	case "pe", "elf", "mach_o":
		if detection.Bitness != 32 && detection.Bitness != 64 {
			return ErrBinaryArtifactInput
		}
	case "mach_o_fat":
		if detection.Kind != "native_binary" || detection.Bitness != 0 || detection.Architecture != "universal" {
			return ErrBinaryArtifactInput
		}
	default:
		return ErrBinaryArtifactInput
	}
	if detection.Architecture == "" || len(detection.Architecture) > 128 || containsBinaryMetadataControl(detection.Architecture) {
		return ErrBinaryArtifactInput
	}
	if detection.Endianness != "little" && detection.Endianness != "big" {
		return ErrBinaryArtifactInput
	}
	return nil
}

func containsBinaryMetadataControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// RegisterBinaryArtifact records a staged broker receipt and task-owned
// provenance without ever receiving raw bytes or a host path. The caller must
// commit the returned desired reference through the broker reconciler.
func (database *DB) RegisterBinaryArtifact(ctx context.Context, input BinaryArtifactRegistration) (BinaryArtifactRegistrationResult, error) {
	var result BinaryArtifactRegistrationResult
	if err := validateBinaryRegistration(input); err != nil {
		return result, err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback() //nolint:errcheck
	// FOR SHARE serializes intake with pause/status/archive transitions. Without
	// it, an origin could be committed after the archive snapshot was taken.
	var lockedTaskID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM tasks
WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL
  AND NOT paused AND NOT queued AND status IN ('created','running')
FOR SHARE`, input.TaskID).Scan(&lockedTaskID); errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("%w: task is not active", ErrBinaryArtifactInput)
	} else if err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO binary_blobs(sha256,byte_size,storage_version,state)
VALUES($1,$2,$3,'staged') ON CONFLICT (sha256) DO NOTHING`, input.BlobSHA256, input.ByteSize, input.StorageVersion); err != nil {
		return result, err
	}
	var storedSize int64
	var storedVersion int
	if err := tx.QueryRowContext(ctx, `SELECT byte_size,storage_version FROM binary_blobs WHERE sha256=$1 FOR UPDATE`, input.BlobSHA256).Scan(&storedSize, &storedVersion); err != nil {
		return result, err
	}
	if storedSize != input.ByteSize || storedVersion != input.StorageVersion {
		return result, fmt.Errorf("%w: blob metadata conflicts with its sha256", ErrBinaryArtifactInput)
	}

	artifact, err := scanBinaryArtifact(tx.QueryRowContext(ctx, `SELECT artifact.artifact_id,artifact.task_id,artifact.blob_sha256,blob.byte_size,
artifact.state,artifact.quarantine_state,artifact.detected_kind,artifact.binary_format,artifact.architecture,artifact.bitness,
artifact.endianness,artifact.retention_class,artifact.execution_allowed,artifact.created_at,artifact.updated_at
FROM binary_artifacts artifact JOIN binary_blobs blob ON blob.sha256=artifact.blob_sha256
WHERE artifact.task_id=$1 AND artifact.blob_sha256=$2 FOR UPDATE OF artifact`, input.TaskID, input.BlobSHA256))
	if errors.Is(err, sql.ErrNoRows) {
		artifactID, idErr := newBinaryOpaqueID("artifact-")
		if idErr != nil {
			return result, idErr
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO binary_artifacts(
artifact_id,task_id,blob_sha256,detected_kind,binary_format,architecture,bitness,endianness)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, artifactID, input.TaskID, input.BlobSHA256,
			input.Detection.Kind, input.Detection.Format, input.Detection.Architecture,
			binaryBitnessValue(input.Detection.Bitness), input.Detection.Endianness); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO binary_artifact_refs(
artifact_id,task_id,blob_sha256,broker_ref_id,receipt_id,state)
VALUES($1,$2,$3,$4,$5,'pending')`, artifactID, input.TaskID, input.BlobSHA256, binaryHotReferenceID(artifactID), input.ReceiptID); err != nil {
			return result, err
		}
		artifact, err = scanBinaryArtifact(tx.QueryRowContext(ctx, `SELECT artifact.artifact_id,artifact.task_id,artifact.blob_sha256,blob.byte_size,
artifact.state,artifact.quarantine_state,artifact.detected_kind,artifact.binary_format,artifact.architecture,artifact.bitness,
artifact.endianness,artifact.retention_class,artifact.execution_allowed,artifact.created_at,artifact.updated_at
FROM binary_artifacts artifact JOIN binary_blobs blob ON blob.sha256=artifact.blob_sha256 WHERE artifact.artifact_id=$1`, artifactID))
		if err != nil {
			return result, err
		}
		result.Created = true
		result.ReceiptNeeded = true
	} else if err != nil {
		return result, err
	} else if artifact.DetectedKind != input.Detection.Kind || artifact.BinaryFormat != input.Detection.Format ||
		artifact.Architecture != input.Detection.Architecture || artifact.Bitness != input.Detection.Bitness ||
		artifact.Endianness != input.Detection.Endianness {
		return result, fmt.Errorf("%w: artifact classification conflicts with its sha256", ErrBinaryArtifactInput)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO binary_artifact_origins(
artifact_id,task_id,origin_kind,traffic_id,producer_kind,producer_job_id,
sanitized_url,sanitized_name,content_type,provenance_hash)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (artifact_id,provenance_hash) DO NOTHING`, artifact.ArtifactID, artifact.TaskID,
		input.Origin.Kind, input.Origin.TrafficID, input.Origin.ProducerKind, input.Origin.ProducerJobID,
		input.Origin.SanitizedURL, input.Origin.SanitizedName, input.Origin.ContentType, input.Origin.ProvenanceHash); err != nil {
		return result, err
	}

	reference, err := scanBinaryBrokerReference(tx.QueryRowContext(ctx, `SELECT broker_ref_id,receipt_id,blob_sha256,state,'hot',task_id,0,artifact_id,updated_at
FROM binary_artifact_refs WHERE artifact_id=$1`, artifact.ArtifactID))
	if err != nil {
		return result, err
	}
	if !result.Created && reference.State == binaryartifact.ReferenceMissing {
		if _, err := tx.ExecContext(ctx, `UPDATE binary_artifact_refs SET receipt_id=$2,state='pending',last_error='' WHERE artifact_id=$1`, artifact.ArtifactID, input.ReceiptID); err != nil {
			return result, err
		}
		reference.ReceiptID = input.ReceiptID
		reference.State = binaryartifact.ReferencePending
		result.ReceiptNeeded = true
	}
	result.Artifact = *artifact
	result.Reference = reference
	if err := tx.Commit(); err != nil {
		return BinaryArtifactRegistrationResult{}, err
	}
	return result, nil
}

func scanBinaryArtifact(scanner interface{ Scan(...any) error }) (*BinaryArtifact, error) {
	var artifact BinaryArtifact
	var bitness sql.NullInt64
	if err := scanner.Scan(&artifact.ArtifactID, &artifact.TaskID, &artifact.BlobSHA256, &artifact.ByteSize,
		&artifact.State, &artifact.QuarantineState, &artifact.DetectedKind, &artifact.BinaryFormat,
		&artifact.Architecture, &bitness, &artifact.Endianness, &artifact.RetentionClass, &artifact.ExecutionAllowed,
		&artifact.CreatedAt, &artifact.UpdatedAt); err != nil {
		return nil, err
	}
	if bitness.Valid {
		artifact.Bitness = int(bitness.Int64)
	}
	return &artifact, nil
}

func binaryBitnessValue(bitness int) any {
	if bitness == 0 {
		return nil
	}
	return bitness
}

func scanBinaryBrokerReference(scanner interface{ Scan(...any) error }) (binaryartifact.BrokerReference, error) {
	var reference binaryartifact.BrokerReference
	err := scanner.Scan(&reference.ReferenceID, &reference.ReceiptID, &reference.BlobSHA256, &reference.State,
		&reference.OwnerKind, &reference.TaskID, &reference.ArchiveID, &reference.ArtifactID, &reference.UpdatedAt)
	return reference, err
}

func (database *DB) ListBinaryBrokerReferences(ctx context.Context) ([]binaryartifact.BrokerReference, error) {
	rows, err := database.QueryContext(ctx, `SELECT broker_ref_id,receipt_id,blob_sha256,state,'hot',task_id,0,artifact_id,updated_at
FROM binary_artifact_refs
UNION ALL
SELECT broker_ref_id,'',blob_sha256,state,'cold',task_id,archive_id,artifact_id,updated_at
FROM binary_archive_refs
ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var references []binaryartifact.BrokerReference
	return collectRowValues(rows, references, func() (binaryartifact.BrokerReference, error) {
		return scanBinaryBrokerReference(rows)
	})
}

func (database *DB) SetBinaryBrokerReferenceState(ctx context.Context, referenceID, state, lastError string) error {
	if !binaryBrokerIDPattern.MatchString(referenceID) || (state != binaryartifact.ReferencePending && state != binaryartifact.ReferenceActive && state != binaryartifact.ReferenceMissing) {
		return ErrBinaryArtifactInput
	}
	if len(lastError) > 4096 {
		runes := []rune(lastError)
		if len(runes) > 4096 {
			lastError = string(runes[:4096])
		}
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	receiptExpression := "receipt_id"
	if state == binaryartifact.ReferenceActive {
		receiptExpression = "''"
	}
	result, err := tx.ExecContext(ctx, `UPDATE binary_artifact_refs SET state=$2,last_error=$3,receipt_id=`+receiptExpression+` WHERE broker_ref_id=$1`, referenceID, state, lastError)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	result, err = tx.ExecContext(ctx, `UPDATE binary_archive_refs SET state=$2,last_error=$3 WHERE broker_ref_id=$1`, referenceID, state, lastError)
	if err != nil {
		return err
	}
	coldCount, _ := result.RowsAffected()
	count += coldCount
	if count != 1 {
		return ErrBinaryReferenceNotFound
	}
	return tx.Commit()
}
