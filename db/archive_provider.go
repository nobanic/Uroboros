package db

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const (
	ArchiveProviderInternetArchive = "internet_archive_cdx"
	ArchiveProviderContractVersion = "ia-cdx-index-v1"
	ArchiveProviderNoticeVersion   = "ia-cdx-disclosure-v1"
	ArchiveProjectionVersion       = "archive-url-projection-v1"
)

var (
	ErrArchiveGrantNotFound = errors.New("matching archive provider grant was not found")
	ErrArchiveAdmission     = errors.New("archive provider request is not admitted")
	archiveDatePattern      = regexp.MustCompile(`^[0-9]{4}([0-9]{2}([0-9]{2}([0-9]{6})?)?)?$`)
	archiveSHA256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	archiveErrorCodePattern = regexp.MustCompile(`^[a-z0-9_]{0,64}$`)
	archiveAttemptIDPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,20}-[0-9]{1,20}$`)
	archiveMIMEPattern      = regexp.MustCompile(`^(?:-|[A-Za-z0-9.+-]+/[A-Za-z0-9.+-]+)$`)
	archiveDigestPattern    = regexp.MustCompile(`^(?:-|[A-Za-z0-9:+/_=-]{1,160})$`)
	archivePathTokenPattern = regexp.MustCompile(`^[a-z0-9._~-]{1,32}$`)
	archivePathExtPattern   = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
	archiveSafePathTokens   = map[string]bool{
		".well-known": true, "admin": true, "api": true, "assets": true,
		"css": true, "docs": true, "graphql": true, "health": true,
		"images": true, "img": true, "index.html": true, "js": true,
		"login": true, "logout": true, "openapi": true, "robots.txt": true,
		"sitemap.xml": true, "static": true, "swagger": true,
		"v1": true, "v2": true, "v3": true,
	}
	archiveSafeQueryNames = map[string]bool{
		"callback": true, "format": true, "id": true, "lang": true,
		"limit": true, "locale": true, "next": true, "offset": true,
		"order": true, "page": true, "q": true, "query": true,
		"redirect": true, "return": true, "sort": true, "url": true,
	}
)

func validArchiveDate(value string) bool {
	if value == "" {
		return true
	}
	layouts := map[int]string{4: "2006", 6: "200601", 8: "20060102", 14: "20060102150405"}
	layout, ok := layouts[len(value)]
	if !ok || !archiveDatePattern.MatchString(value) {
		return false
	}
	_, err := time.Parse(layout, value)
	return err == nil
}

func validArchiveGrantDomain(value string) bool {
	if value == "" || value != strings.ToLower(value) || strings.TrimSpace(value) != value ||
		strings.HasSuffix(value, ".") || net.ParseIP(strings.Trim(value, "[]")) != nil {
		return false
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil || ascii != value || len(ascii) > 253 {
		return false
	}
	root, err := publicsuffix.EffectiveTLDPlusOne(ascii)
	return err == nil && root == ascii
}

// ArchiveProviderGrant is an operator-owned disclosure authorization. IdentityKey
// is shared by every grant for the same task and is needed only by the host
// adapter to derive task-private candidate identities. It is never serialized to
// an API response or an agent tool result.
type ArchiveProviderGrant struct {
	ID              int64      `json:"id"`
	TaskID          int64      `json:"task_id"`
	Provider        string     `json:"provider"`
	Domain          string     `json:"domain"`
	CaptureFrom     string     `json:"capture_from"`
	CaptureTo       string     `json:"capture_to"`
	ContractVersion string     `json:"contract_version"`
	NoticeVersion   string     `json:"notice_version"`
	ApprovedBy      string     `json:"approved_by"`
	IdentityKey     []byte     `json:"-"`
	ApprovedAt      time.Time  `json:"approved_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	RevokedBy       string     `json:"revoked_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type ArchiveProviderGrantCreate struct {
	TaskID          int64
	Provider        string
	Domain          string
	CaptureFrom     string
	CaptureTo       string
	ContractVersion string
	NoticeVersion   string
	ApprovedBy      string
	ExpiresAt       time.Time
}

func scanArchiveProviderGrant(row interface{ Scan(...any) error }) (*ArchiveProviderGrant, error) {
	grant := &ArchiveProviderGrant{}
	err := row.Scan(&grant.ID, &grant.TaskID, &grant.Provider, &grant.Domain,
		&grant.CaptureFrom, &grant.CaptureTo, &grant.ContractVersion, &grant.NoticeVersion,
		&grant.ApprovedBy, &grant.IdentityKey, &grant.ApprovedAt, &grant.ExpiresAt,
		&grant.RevokedAt, &grant.RevokedBy, &grant.CreatedAt, &grant.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return grant, err
}

const archiveProviderGrantCols = `id,task_id,provider,domain,capture_from,capture_to,
contract_version,notice_version,approved_by,identity_key,approved_at,expires_at,
revoked_at,revoked_by,created_at,updated_at`

func validateArchiveGrantCreate(input ArchiveProviderGrantCreate) error {
	if input.TaskID <= 0 || input.Provider != ArchiveProviderInternetArchive ||
		!validArchiveGrantDomain(input.Domain) || input.ContractVersion != ArchiveProviderContractVersion ||
		input.NoticeVersion != ArchiveProviderNoticeVersion || strings.TrimSpace(input.ApprovedBy) == "" {
		return errors.New("task, provider, domain, contract, notice and approver are required")
	}
	if !validArchiveDate(input.CaptureFrom) || !validArchiveDate(input.CaptureTo) ||
		(input.CaptureFrom != "" && input.CaptureTo != "" &&
			input.CaptureFrom+strings.Repeat("0", 14-len(input.CaptureFrom)) >
				input.CaptureTo+strings.Repeat("9", 14-len(input.CaptureTo))) {
		return errors.New("archive capture bounds are invalid")
	}
	if !input.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("archive provider grant expiry must be in the future")
	}
	return nil
}

func (d *DB) CreateArchiveProviderGrant(ctx context.Context, input ArchiveProviderGrantCreate) (*ArchiveProviderGrant, error) {
	if err := validateArchiveGrantCreate(input); err != nil {
		return nil, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var lockedTaskID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM tasks
WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL FOR UPDATE`, input.TaskID).Scan(&lockedTaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("task is unavailable for an archive provider grant")
		}
		return nil, err
	}
	var identityKey []byte
	err = tx.QueryRowContext(ctx, `SELECT identity_key FROM archive_provider_grants
WHERE task_id=$1 ORDER BY id LIMIT 1`, input.TaskID).Scan(&identityKey)
	if errors.Is(err, sql.ErrNoRows) {
		identityKey = make([]byte, 32)
		if _, err := rand.Read(identityKey); err != nil {
			return nil, fmt.Errorf("generate archive candidate identity key: %w", err)
		}
	} else if err != nil {
		return nil, err
	} else if len(identityKey) < 32 {
		return nil, errors.New("existing archive candidate identity key is invalid")
	}
	grant, err := scanArchiveProviderGrant(tx.QueryRowContext(ctx, `
INSERT INTO archive_provider_grants(task_id,provider,domain,capture_from,capture_to,
 contract_version,notice_version,approved_by,identity_key,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
RETURNING `+archiveProviderGrantCols,
		input.TaskID, input.Provider, input.Domain, input.CaptureFrom, input.CaptureTo,
		input.ContractVersion, input.NoticeVersion, input.ApprovedBy, identityKey, input.ExpiresAt.UTC()))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return grant, nil
}

func (d *DB) ListArchiveProviderGrants(ctx context.Context, taskID int64) ([]ArchiveProviderGrant, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+archiveProviderGrantCols+`
FROM archive_provider_grants WHERE task_id=$1 ORDER BY id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchiveProviderGrant{}
	for rows.Next() {
		grant, err := scanArchiveProviderGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *grant)
	}
	return out, rows.Err()
}

func (d *DB) MatchingArchiveProviderGrant(
	ctx context.Context,
	taskID int64,
	domain, captureFrom, captureTo string,
	now time.Time,
) (*ArchiveProviderGrant, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+archiveProviderGrantCols+`
FROM archive_provider_grants
WHERE task_id=$1 AND provider=$2 AND domain=$3
  AND revoked_at IS NULL AND expires_at>$4
ORDER BY approved_at DESC,id DESC`, taskID, ArchiveProviderInternetArchive, domain, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		grant, err := scanArchiveProviderGrant(rows)
		if err != nil {
			return nil, err
		}
		if archiveRangeWithin(captureFrom, captureTo, grant.CaptureFrom, grant.CaptureTo) {
			return grant, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrArchiveGrantNotFound
}

func (d *DB) RevokeArchiveProviderGrant(ctx context.Context, taskID, grantID int64, revokedBy string) (bool, error) {
	revoked, _, err := d.RevokeArchiveProviderGrantAndJob(ctx, taskID, grantID, revokedBy)
	return revoked, err
}

// RevokeArchiveProviderGrantAndJob commits revocation and identifies the exact
// active archive job bound to the grant in one transaction. The caller can then
// cancel and join that owned job without a second fallible lookup.
func (d *DB) RevokeArchiveProviderGrantAndJob(
	ctx context.Context,
	taskID, grantID int64,
	revokedBy string,
) (bool, int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	var revokedID int64
	err = tx.QueryRowContext(ctx, `UPDATE archive_provider_grants
SET revoked_at=COALESCE(revoked_at,now()),revoked_by=CASE WHEN revoked_at IS NULL THEN $3 ELSE revoked_by END
WHERE id=$1 AND task_id=$2 RETURNING id`, grantID, taskID, strings.TrimSpace(revokedBy)).Scan(&revokedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	var jobID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM arsenal_jobs
WHERE task_id=$1 AND tool='archive' AND status IN ('queued','running')
  AND config->>'grant_id'=$2 ORDER BY id DESC LIMIT 1`, taskID, strconv.FormatInt(grantID, 10)).Scan(&jobID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		jobID = 0
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return true, jobID, nil
}

func archiveRangeWithin(requestFrom, requestTo, grantFrom, grantTo string) bool {
	low := func(value string) string {
		if value == "" {
			return ""
		}
		return value + strings.Repeat("0", 14-len(value))
	}
	high := func(value string) string {
		if value == "" {
			return ""
		}
		return value + strings.Repeat("9", 14-len(value))
	}
	if grantFrom != "" && (requestFrom == "" || low(requestFrom) < low(grantFrom)) {
		return false
	}
	if grantTo != "" && (requestTo == "" || high(requestTo) > high(grantTo)) {
		return false
	}
	return requestFrom == "" || requestTo == "" || low(requestFrom) <= high(requestTo)
}

// AuthorizeArchiveProviderRequest rechecks the persisted task, job and grant at
// the last durable boundary before a provider request. A caller must still bind
// the request to its cancelable task context so a concurrent revoke/pause closes
// an already admitted connection.
func (d *DB) AuthorizeArchiveProviderRequest(
	ctx context.Context,
	jobID, grantID int64,
	domain, captureFrom, captureTo string,
	now time.Time,
) (*ArchiveProviderGrant, error) {
	grant, err := scanArchiveProviderGrant(d.QueryRowContext(ctx, `SELECT
g.id,g.task_id,g.provider,g.domain,g.capture_from,g.capture_to,
g.contract_version,g.notice_version,g.approved_by,g.identity_key,g.approved_at,g.expires_at,
g.revoked_at,g.revoked_by,g.created_at,g.updated_at
FROM archive_provider_grants g
JOIN arsenal_jobs job ON job.id=$1 AND job.task_id=g.task_id AND job.tool='archive'
JOIN tasks task ON task.id=g.task_id
WHERE g.id=$2 AND g.domain=$3 AND g.provider=$4
  AND g.revoked_at IS NULL AND g.expires_at>$5
  AND g.contract_version=$6 AND g.notice_version=$7
  AND job.status IN ('queued','running') AND job.target=$3
  AND job.config->>'grant_id'=$2::text
  AND job.config->>'provider'=$4 AND job.config->>'contract_version'=$6
  AND task.deleted_at IS NULL AND task.archived_at IS NULL
  AND NOT task.paused AND NOT task.queued
  AND task.status NOT IN ('done','failed','timeout')`,
		jobID, grantID, domain, ArchiveProviderInternetArchive, now.UTC(),
		ArchiveProviderContractVersion, ArchiveProviderNoticeVersion))
	if err != nil {
		return nil, err
	}
	if grant == nil || !archiveRangeWithin(captureFrom, captureTo, grant.CaptureFrom, grant.CaptureTo) {
		return nil, ErrArchiveAdmission
	}
	return grant, nil
}

type ArchiveProviderArtifact struct {
	ID               int64           `json:"id"`
	JobID            int64           `json:"job_id"`
	TaskID           int64           `json:"task_id"`
	GrantID          int64           `json:"grant_id"`
	Provider         string          `json:"provider"`
	AttemptID        string          `json:"attempt_id"`
	RequestSHA256    string          `json:"request_sha256"`
	RouteSHA256      string          `json:"route_sha256"`
	ResponseStatus   *int            `json:"response_status,omitempty"`
	RawBodySHA256    string          `json:"raw_body_sha256,omitempty"`
	ProjectionSHA256 string          `json:"projection_sha256,omitempty"`
	Projection       json.RawMessage `json:"projection"`
	RawRetained      bool            `json:"raw_retained"`
	Outcome          string          `json:"outcome"`
	ErrorCode        string          `json:"error_code,omitempty"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       time.Time       `json:"finished_at"`
	CreatedAt        time.Time       `json:"created_at"`
}

type ArchiveProviderArtifactCreate struct {
	JobID            int64
	TaskID           int64
	GrantID          int64
	Provider         string
	AttemptID        string
	RequestSHA256    string
	RouteSHA256      string
	ResponseStatus   *int
	RawBodySHA256    string
	ProjectionSHA256 string
	Projection       any
	Outcome          string
	ErrorCode        string
	StartedAt        time.Time
	FinishedAt       time.Time
}

type ArchiveObservationCreate struct {
	ObservationKey string
	Record         any
}

func scanArchiveProviderArtifact(row interface{ Scan(...any) error }) (*ArchiveProviderArtifact, error) {
	artifact := &ArchiveProviderArtifact{}
	err := row.Scan(&artifact.ID, &artifact.JobID, &artifact.TaskID, &artifact.GrantID,
		&artifact.Provider, &artifact.AttemptID, &artifact.RequestSHA256, &artifact.RouteSHA256,
		&artifact.ResponseStatus, &artifact.RawBodySHA256, &artifact.ProjectionSHA256,
		&artifact.Projection, &artifact.RawRetained, &artifact.Outcome, &artifact.ErrorCode,
		&artifact.StartedAt, &artifact.FinishedAt, &artifact.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return artifact, err
}

const archiveProviderArtifactCols = `id,job_id,task_id,grant_id,provider,attempt_id,
request_sha256,route_sha256,response_status,raw_body_sha256,projection_sha256,
projection,raw_retained,outcome,error_code,started_at,finished_at,created_at`

func validateArchiveArtifact(input ArchiveProviderArtifactCreate) error {
	validOutcomes := map[string]bool{
		"started": true,
		"page":    true, "http_error": true, "network_error": true,
		"contract_error": true, "rate_limited": true,
	}
	if input.JobID <= 0 || input.TaskID <= 0 || input.GrantID <= 0 ||
		input.Provider != ArchiveProviderInternetArchive || !archiveAttemptIDPattern.MatchString(input.AttemptID) ||
		!archiveSHA256Pattern.MatchString(input.RequestSHA256) ||
		!archiveSHA256Pattern.MatchString(input.RouteSHA256) || !validOutcomes[input.Outcome] {
		return errors.New("archive artifact identity, fingerprints or outcome are invalid")
	}
	attemptParts := strings.Split(input.AttemptID, "-")
	attemptJob, jobErr := strconv.ParseInt(attemptParts[0], 10, 64)
	attemptPage, pageErr := strconv.ParseInt(attemptParts[1], 10, 64)
	attemptNumber, attemptErr := strconv.ParseInt(attemptParts[2], 10, 64)
	if jobErr != nil || pageErr != nil || attemptErr != nil || attemptJob != input.JobID ||
		attemptPage <= 0 || attemptNumber <= 0 {
		return errors.New("archive artifact attempt id is not bound to its job")
	}
	for _, digest := range []string{input.RawBodySHA256, input.ProjectionSHA256} {
		if digest != "" && !archiveSHA256Pattern.MatchString(digest) {
			return errors.New("archive artifact digest is invalid")
		}
	}
	if input.StartedAt.IsZero() || input.FinishedAt.IsZero() || input.FinishedAt.Before(input.StartedAt) {
		return errors.New("archive artifact timestamps are invalid")
	}
	if input.ResponseStatus != nil && (*input.ResponseStatus < 100 || *input.ResponseStatus > 599) {
		return errors.New("archive artifact response status is invalid")
	}
	if !archiveErrorCodePattern.MatchString(input.ErrorCode) {
		return errors.New("archive artifact error code is invalid")
	}
	if input.ProjectionSHA256 == "" {
		return errors.New("archive artifact projection fingerprint is required")
	}
	if input.Outcome == "started" && (input.ResponseStatus != nil || input.RawBodySHA256 != "" || input.ErrorCode != "") {
		return errors.New("started archive artifact cannot contain a response or error")
	}
	if input.Outcome == "page" && (input.ResponseStatus == nil || *input.ResponseStatus != 200 ||
		input.RawBodySHA256 == "" || input.ErrorCode != "") {
		return errors.New("archive page artifact requires a successful hashed response")
	}
	if (input.Outcome == "http_error" || input.Outcome == "rate_limited") && input.ResponseStatus == nil {
		return errors.New("archive HTTP failure artifact requires a response status")
	}
	if input.Outcome != "started" && input.Outcome != "page" && input.ErrorCode == "" {
		return errors.New("failed archive artifact requires an error code")
	}
	return nil
}

func archiveCanonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func archiveJSONSHA256(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func decodeArchiveJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		if err == nil {
			err = errors.New("JSON value is not an object")
		}
		return nil, err
	}
	return object, nil
}

func validateArchiveProjection(artifact ArchiveProviderArtifactCreate, projection map[string]any) ([]any, error) {
	allowed := map[string]bool{
		"provider": true, "contract_version": true, "projection_version": true,
		"raw_retained": true, "rows": true,
	}
	if artifact.Outcome == "page" {
		allowed["continuation_sha256"] = true
	} else {
		allowed["outcome"] = true
		allowed["error_code"] = true
	}
	for key := range projection {
		if !allowed[key] {
			return nil, fmt.Errorf("archive artifact projection contains forbidden field %q", key)
		}
	}
	if provider, _ := projection["provider"].(string); provider != ArchiveProviderInternetArchive {
		return nil, errors.New("archive artifact projection provider is invalid")
	}
	if contract, _ := projection["contract_version"].(string); contract != ArchiveProviderContractVersion {
		return nil, errors.New("archive artifact projection contract is invalid")
	}
	if version, _ := projection["projection_version"].(string); version != ArchiveProjectionVersion {
		return nil, errors.New("archive artifact projection revision is invalid")
	}
	if retained, ok := projection["raw_retained"].(bool); !ok || retained {
		return nil, errors.New("archive artifact projection must declare raw_retained=false")
	}
	rows, ok := projection["rows"].([]any)
	if !ok {
		return nil, errors.New("archive artifact projection rows are invalid")
	}
	if artifact.Outcome == "page" {
		cursorSHA, ok := projection["continuation_sha256"].(string)
		if !ok || (cursorSHA != "" && !archiveSHA256Pattern.MatchString(cursorSHA)) {
			return nil, errors.New("archive artifact continuation fingerprint is invalid")
		}
	} else {
		outcome, _ := projection["outcome"].(string)
		errorCode, _ := projection["error_code"].(string)
		if outcome != artifact.Outcome || errorCode != artifact.ErrorCode || len(rows) != 0 {
			return nil, errors.New("archive artifact failure projection does not match the attempt")
		}
	}
	return rows, nil
}

func archiveInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		integer := int64(typed)
		return integer, float64(integer) == typed
	case json.Number:
		integer, err := typed.Int64()
		return integer, err == nil
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

func validateArchiveProjectedURL(raw string) (string, error) {
	if raw == "" || len(raw) > 16<<10 || strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", errors.New("archive observation URL projection is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("archive observation URL projection is invalid")
	}
	host := parsed.Hostname()
	if host != strings.ToLower(host) || strings.TrimSuffix(host, ".") != host {
		return "", errors.New("archive observation URL host is not canonical")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("archive observation URL port is invalid")
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/") {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "~") {
			extension := strings.TrimPrefix(segment, "~")
			if extension != "" && !archivePathExtPattern.MatchString(extension) {
				return "", errors.New("archive observation URL path projection is invalid")
			}
			continue
		}
		if !archivePathTokenPattern.MatchString(segment) || !archiveSafePathTokens[segment] {
			return "", errors.New("archive observation URL path projection is invalid")
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", errors.New("archive observation URL query projection is invalid")
	}
	for key, values := range query {
		if key != strings.ToLower(key) || !archivePathTokenPattern.MatchString(key) || !archiveSafeQueryNames[key] || len(values) == 0 {
			return "", errors.New("archive observation URL query projection is invalid")
		}
		for _, value := range values {
			if value != "REDACTED" {
				return "", errors.New("archive observation URL query value is not redacted")
			}
		}
	}
	return host, nil
}

type archiveValidatedObservation struct {
	Host           string
	Canonical      []byte
	ScopeHash      string
	ConfigHash     string
	ObservationKey string
}

func validateArchiveObservationRecord(
	value any,
	attemptID, rawBodySHA256 string,
) (archiveValidatedObservation, error) {
	raw, err := archiveCanonicalJSON(value)
	if err != nil {
		return archiveValidatedObservation{}, err
	}
	object, err := decodeArchiveJSONObject(raw)
	if err != nil {
		return archiveValidatedObservation{}, errors.New("archive observation record must be a JSON object")
	}
	allowed := map[string]bool{
		"provider": true, "contract_version": true, "projection_version": true,
		"authority": true, "target_proxy_verified": true, "candidate_id": true,
		"shape_key": true, "url": true, "requires_reconstruction": true,
		"capture_timestamp": true, "mimetype": true, "status_code": true,
		"provider_digest": true, "length": true, "observed_at": true,
		"provider_exchange_id": true, "raw_page_sha256": true, "row_ordinal": true,
		"scope_hash": true, "config_hash": true,
	}
	for key := range object {
		if !allowed[key] {
			return archiveValidatedObservation{}, fmt.Errorf("archive observation contains forbidden field %q", key)
		}
	}
	if len(object) != len(allowed) {
		return archiveValidatedObservation{}, errors.New("archive observation omits required candidate provenance")
	}
	provider, _ := object["provider"].(string)
	contract, _ := object["contract_version"].(string)
	authority, _ := object["authority"].(string)
	verified, verifiedOK := object["target_proxy_verified"].(bool)
	candidateID, _ := object["candidate_id"].(string)
	shapeKey, _ := object["shape_key"].(string)
	rawPageSHA, _ := object["raw_page_sha256"].(string)
	projectionVersion, _ := object["projection_version"].(string)
	urlProjection, _ := object["url"].(string)
	if provider != ArchiveProviderInternetArchive || contract != ArchiveProviderContractVersion ||
		authority != "candidate" || !verifiedOK || verified || projectionVersion != ArchiveProjectionVersion ||
		!archiveSHA256Pattern.MatchString(candidateID) || !archiveSHA256Pattern.MatchString(shapeKey) ||
		rawPageSHA != rawBodySHA256 || !archiveSHA256Pattern.MatchString(rawPageSHA) {
		return archiveValidatedObservation{}, errors.New("archive observation does not satisfy the candidate-only contract")
	}
	reconstruct, reconstructOK := object["requires_reconstruction"].(bool)
	_ = reconstruct
	timestamp, _ := object["capture_timestamp"].(string)
	mimeType, _ := object["mimetype"].(string)
	digest, _ := object["provider_digest"].(string)
	observedAt, _ := object["observed_at"].(string)
	exchangeID, _ := object["provider_exchange_id"].(string)
	scopeHash, _ := object["scope_hash"].(string)
	configHash, _ := object["config_hash"].(string)
	rowOrdinal, rowOK := archiveInteger(object["row_ordinal"])
	if !reconstructOK || !validArchiveDate(timestamp) || len(timestamp) != 14 ||
		!archiveMIMEPattern.MatchString(mimeType) || !archiveDigestPattern.MatchString(digest) ||
		exchangeID != attemptID || !archiveAttemptIDPattern.MatchString(exchangeID) ||
		!archiveSHA256Pattern.MatchString(scopeHash) || !archiveSHA256Pattern.MatchString(configHash) ||
		!rowOK || rowOrdinal < 1 || rowOrdinal > 500 {
		return archiveValidatedObservation{}, errors.New("archive observation metadata is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, observedAt); err != nil {
		return archiveValidatedObservation{}, errors.New("archive observation time is invalid")
	}
	if status := object["status_code"]; status != nil {
		value, ok := archiveInteger(status)
		if !ok || value < 100 || value > 599 {
			return archiveValidatedObservation{}, errors.New("archive observation status is invalid")
		}
	}
	if length := object["length"]; length != nil {
		value, ok := archiveInteger(length)
		if !ok || value < 0 {
			return archiveValidatedObservation{}, errors.New("archive observation length is invalid")
		}
	}
	host, err := validateArchiveProjectedURL(urlProjection)
	if err != nil {
		return archiveValidatedObservation{}, err
	}
	if archiveJSONSHA256([]byte(urlProjection)) != shapeKey {
		return archiveValidatedObservation{}, errors.New("archive observation shape fingerprint does not match its URL projection")
	}
	keyRaw, err := archiveCanonicalJSON(map[string]any{
		"candidate_id": candidateID, "timestamp": timestamp, "digest": digest,
	})
	if err != nil {
		return archiveValidatedObservation{}, err
	}
	return archiveValidatedObservation{
		Host: host, Canonical: raw, ScopeHash: scopeHash, ConfigHash: configHash,
		ObservationKey: archiveJSONSHA256(keyRaw),
	}, nil
}

func marshalValidatedArchiveProjection(
	artifact ArchiveProviderArtifactCreate,
) ([]byte, []any, error) {
	projection, err := archiveCanonicalJSON(artifact.Projection)
	if err != nil {
		return nil, nil, err
	}
	if archiveJSONSHA256(projection) != artifact.ProjectionSHA256 {
		return nil, nil, errors.New("archive artifact projection fingerprint does not match its sanitized projection")
	}
	projectionObject, err := decodeArchiveJSONObject(projection)
	if err != nil {
		return nil, nil, errors.New("archive artifact projection must be a JSON object")
	}
	rows, err := validateArchiveProjection(artifact, projectionObject)
	return projection, rows, err
}

// BeginArchiveProviderAttempt durably records the exact admitted request and
// frozen provider route before any HTTP bytes are sent. If this write fails, the
// caller must not make the provider request.
func (d *DB) BeginArchiveProviderAttempt(
	ctx context.Context,
	artifact ArchiveProviderArtifactCreate,
) (*ArchiveProviderArtifact, error) {
	if err := validateArchiveArtifact(artifact); err != nil {
		return nil, err
	}
	if artifact.Outcome != "started" {
		return nil, errors.New("archive provider admission must use the started outcome")
	}
	projection, rows, err := marshalValidatedArchiveProjection(artifact)
	if err != nil {
		return nil, err
	}
	if len(rows) != 0 {
		return nil, errors.New("started archive provider admission cannot contain rows")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var jobTarget string
	var jobConfig json.RawMessage
	var grantFrom, grantTo string
	err = tx.QueryRowContext(ctx, `SELECT job.target,job.config,g.capture_from,g.capture_to
FROM arsenal_jobs job JOIN tasks task ON task.id=job.task_id
JOIN archive_provider_grants g ON g.id=$3 AND g.task_id=job.task_id
WHERE job.id=$1 AND job.task_id=$2 AND job.tool='archive' AND job.status='running'
  AND task.deleted_at IS NULL AND task.archived_at IS NULL AND NOT task.paused AND NOT task.queued
  AND task.status NOT IN ('done','failed','timeout')
  AND g.provider=$4 AND g.contract_version=$5 AND g.notice_version=$6
  AND g.domain=job.target AND g.revoked_at IS NULL AND g.expires_at>now()
FOR SHARE OF job,task,g`, artifact.JobID, artifact.TaskID, artifact.GrantID,
		ArchiveProviderInternetArchive, ArchiveProviderContractVersion, ArchiveProviderNoticeVersion).
		Scan(&jobTarget, &jobConfig, &grantFrom, &grantTo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrArchiveAdmission
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		GrantID     int64  `json:"grant_id"`
		Provider    string `json:"provider"`
		Contract    string `json:"contract_version"`
		CaptureFrom string `json:"from"`
		CaptureTo   string `json:"to"`
	}
	if json.Unmarshal(jobConfig, &config) != nil || config.GrantID != artifact.GrantID ||
		config.Provider != ArchiveProviderInternetArchive || config.Contract != ArchiveProviderContractVersion ||
		!archiveRangeWithin(config.CaptureFrom, config.CaptureTo, grantFrom, grantTo) || strings.TrimSpace(jobTarget) == "" {
		return nil, ErrArchiveAdmission
	}
	stored, err := scanArchiveProviderArtifact(tx.QueryRowContext(ctx, `
INSERT INTO archive_provider_artifacts(job_id,task_id,grant_id,provider,attempt_id,
 request_sha256,route_sha256,response_status,raw_body_sha256,projection_sha256,
 projection,raw_retained,outcome,error_code,started_at,finished_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,false,$12,$13,$14,$15)
RETURNING `+archiveProviderArtifactCols,
		artifact.JobID, artifact.TaskID, artifact.GrantID, artifact.Provider, artifact.AttemptID,
		artifact.RequestSHA256, artifact.RouteSHA256, artifact.ResponseStatus,
		artifact.RawBodySHA256, artifact.ProjectionSHA256, projection,
		artifact.Outcome, artifact.ErrorCode, artifact.StartedAt.UTC(), artifact.FinishedAt.UTC()))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

// CompleteArchiveProviderAttempt finalizes one previously admitted provider
// attempt and atomically publishes any sanitized candidate observations. It
// cannot import an asset, attach a Traffic id or promote a row above candidate.
func (d *DB) CompleteArchiveProviderAttempt(
	ctx context.Context,
	artifact ArchiveProviderArtifactCreate,
	observations []ArchiveObservationCreate,
) (*ArchiveProviderArtifact, error) {
	if err := validateArchiveArtifact(artifact); err != nil {
		return nil, err
	}
	if artifact.Outcome == "started" {
		return nil, errors.New("completed archive provider attempt cannot remain started")
	}
	projection, projectionRows, err := marshalValidatedArchiveProjection(artifact)
	if err != nil {
		return nil, err
	}
	if (artifact.Outcome == "page" && len(projectionRows) != len(observations)) ||
		(artifact.Outcome != "page" && (len(projectionRows) != 0 || len(observations) != 0)) {
		return nil, errors.New("archive artifact projection rows do not match the attempt")
	}
	validated := make([]archiveValidatedObservation, len(observations))
	for index, observation := range observations {
		observationRecord, err := validateArchiveObservationRecord(
			observation.Record, artifact.AttemptID, artifact.RawBodySHA256,
		)
		if err != nil {
			return nil, err
		}
		projectionRecord, err := validateArchiveObservationRecord(
			projectionRows[index], artifact.AttemptID, artifact.RawBodySHA256,
		)
		if err != nil {
			return nil, err
		}
		if observation.ObservationKey != observationRecord.ObservationKey ||
			observationRecord.Host != projectionRecord.Host ||
			!bytes.Equal(observationRecord.Canonical, projectionRecord.Canonical) {
			return nil, errors.New("archive artifact row differs from its candidate observation")
		}
		validated[index] = observationRecord
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	started, err := scanArchiveProviderArtifact(tx.QueryRowContext(ctx, `SELECT `+archiveProviderArtifactCols+`
FROM archive_provider_artifacts WHERE job_id=$1 AND task_id=$2 AND grant_id=$3 AND attempt_id=$4
FOR UPDATE`, artifact.JobID, artifact.TaskID, artifact.GrantID, artifact.AttemptID))
	if err != nil {
		return nil, err
	}
	if started == nil || started.Outcome != "started" || started.Provider != artifact.Provider ||
		started.RequestSHA256 != artifact.RequestSHA256 || started.RouteSHA256 != artifact.RouteSHA256 ||
		started.StartedAt.Sub(artifact.StartedAt).Abs() > time.Microsecond {
		return nil, errors.New("archive provider attempt has no matching durable admission")
	}
	var jobTarget, jobScopeHash, jobConfigHash string
	if err := tx.QueryRowContext(ctx, `SELECT target,scope_hash,config_hash FROM arsenal_jobs
WHERE id=$1 AND task_id=$2 AND tool='archive' AND status='running'`, artifact.JobID, artifact.TaskID).
		Scan(&jobTarget, &jobScopeHash, &jobConfigHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArchiveAdmission
		}
		return nil, err
	}
	for _, record := range validated {
		if (record.Host != jobTarget && !strings.HasSuffix(record.Host, "."+jobTarget)) ||
			record.ScopeHash != jobScopeHash || record.ConfigHash != jobConfigHash {
			return nil, errors.New("archive observation does not match the admitted job provenance")
		}
	}
	stored, err := scanArchiveProviderArtifact(tx.QueryRowContext(ctx, `UPDATE archive_provider_artifacts SET
 response_status=$5,raw_body_sha256=$6,projection_sha256=$7,projection=$8,
 outcome=$9,error_code=$10,finished_at=$11
WHERE job_id=$1 AND task_id=$2 AND grant_id=$3 AND attempt_id=$4 AND outcome='started'
RETURNING `+archiveProviderArtifactCols, artifact.JobID, artifact.TaskID, artifact.GrantID, artifact.AttemptID,
		artifact.ResponseStatus, artifact.RawBodySHA256, artifact.ProjectionSHA256, projection,
		artifact.Outcome, artifact.ErrorCode, artifact.FinishedAt.UTC()))
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, errors.New("archive provider attempt admission changed before completion")
	}
	for _, observation := range observations {
		if strings.TrimSpace(observation.ObservationKey) == "" {
			return nil, errors.New("archive observation key is required")
		}
		record, err := json.Marshal(observation.Record)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO arsenal_observations(
 job_id,task_id,tool,observation_key,authority,record,proxy_verified,traffic_id,asset_id)
VALUES($1,$2,'archive',$3,'candidate',$4,false,'',NULL)
ON CONFLICT(job_id,observation_key) DO UPDATE SET record=EXCLUDED.record,updated_at=now()`,
			artifact.JobID, artifact.TaskID, observation.ObservationKey, record); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

// ReconcileArchiveProviderAttempts makes crash-interrupted, already admitted
// provider requests visible after active Arsenal jobs are marked interrupted.
func (d *DB) ReconcileArchiveProviderAttempts(ctx context.Context) (int64, error) {
	projection := map[string]any{
		"provider": ArchiveProviderInternetArchive, "contract_version": ArchiveProviderContractVersion,
		"projection_version": ArchiveProjectionVersion, "raw_retained": false,
		"rows": []any{}, "outcome": "network_error", "error_code": "interrupted",
	}
	raw, err := archiveCanonicalJSON(projection)
	if err != nil {
		return 0, err
	}
	result, err := d.ExecContext(ctx, `UPDATE archive_provider_artifacts artifact SET
 outcome='network_error',error_code='interrupted',projection=$1,projection_sha256=$2,
 finished_at=GREATEST(artifact.started_at,now())
FROM arsenal_jobs job WHERE artifact.job_id=job.id AND artifact.outcome='started'
  AND job.status IN ('completed','failed','canceled','interrupted')`, raw, archiveJSONSHA256(raw))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *DB) ListArchiveProviderArtifacts(
	ctx context.Context,
	taskID, jobID int64,
	limit, offset int,
) ([]ArchiveProviderArtifact, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := d.QueryContext(ctx, `SELECT `+archiveProviderArtifactCols+`
FROM archive_provider_artifacts
WHERE task_id=$1 AND ($2=0 OR job_id=$2)
ORDER BY id DESC LIMIT $3 OFFSET $4`, taskID, jobID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchiveProviderArtifact{}
	for rows.Next() {
		item, err := scanArchiveProviderArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}
