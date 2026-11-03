package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

const (
	ResearchContractVersion      = "research-query-v1"
	VulnerabilityContractVersion = "vulnerability-query-v1"
	ResearchNoticeVersion        = "research-disclosure-v1"
	TorResearchContractVersion   = "tor-research-fetch-v1"
	ResearchEgressNoticeVersion  = "research-egress-v1"
	TorResearchProfileID         = "tor"
	TorResearchProvider          = "tor-fetch"
)

var (
	ErrResearchGrantNotFound = errors.New("matching research disclosure grant was not found")
	ErrResearchEgressGrant   = errors.New("matching research egress grant was not found")
	ErrResearchBudget        = errors.New("research request budget is exhausted")
	ErrResearchRate          = errors.New("research request rate limit is exhausted")
	ErrResearchUnavailable   = errors.New("research run attribution is unavailable")
)

func ValidWebSearchProvider(provider string) bool {
	switch strings.TrimSpace(provider) {
	case "brave-free", "tavily", "ddgs":
		return true
	default:
		return false
	}
}

func ValidVulnerabilityProvider(provider string) bool {
	switch strings.TrimSpace(provider) {
	case "cve-list-v5", "nvd", "osv", "ghsa", "cisa-kev", "first-epss":
		return true
	default:
		return false
	}
}

// ValidResearchProvider covers every fixed external-intelligence provider that
// may receive a task disclosure grant or own an audited research artifact.
func ValidResearchProvider(provider string) bool {
	return ValidWebSearchProvider(provider) || ValidVulnerabilityProvider(provider)
}

func ResearchContractForProvider(provider string) string {
	if ValidVulnerabilityProvider(provider) {
		return VulnerabilityContractVersion
	}
	return ResearchContractVersion
}

func validResearchToolProvider(toolName, provider string) bool {
	switch toolName {
	case "web_search":
		return ValidWebSearchProvider(provider)
	case "vuln_lookup":
		return provider == "cve-list-v5" || provider == "nvd" || provider == "osv" || provider == "ghsa"
	case "vuln_match":
		return provider == "osv" || provider == "nvd"
	case "vuln_prioritize":
		return provider == "cisa-kev" || provider == "first-epss"
	case "research_fetch":
		return provider == TorResearchProvider
	default:
		return false
	}
}

var torResearchHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

// NormalizeTorResearchURL validates an operator-provided seed without resolving
// it locally. Returning the canonical origin separately lets one approval cover
// read-only navigation on that host while redirects to every other origin fail
// closed. Only default HTTP(S) ports are admitted in this first phase.
func NormalizeTorResearchURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return "", "", errors.New("seed URL must contain 1..2048 safe characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil {
		return "", "", errors.New("seed must be an absolute HTTP(S) URL without credentials")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", errors.New("seed scheme must be http or https")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || len(host) > 253 {
		return "", "", errors.New("seed hostname is invalid")
	}
	port := parsed.Port()
	defaultPort := map[string]string{"http": "80", "https": "443"}[parsed.Scheme]
	if port != "" && port != defaultPort {
		return "", "", errors.New("only the default HTTP(S) port is allowed")
	}
	if strings.HasSuffix(host, ".onion") {
		label := strings.TrimSuffix(host, ".onion")
		if len(label) != 56 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz234567") != "" {
			return "", "", errors.New("only canonical v3 onion service names are allowed")
		}
		decoded, decodeErr := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(label))
		if decodeErr != nil || len(decoded) != 35 || decoded[34] != 3 {
			return "", "", errors.New("onion service name has an invalid v3 payload")
		}
		digest := sha3.New256()
		_, _ = digest.Write([]byte(".onion checksum"))
		_, _ = digest.Write(decoded[:32])
		_, _ = digest.Write(decoded[34:35])
		checksum := digest.Sum(nil)
		if decoded[32] != checksum[0] || decoded[33] != checksum[1] {
			return "", "", errors.New("onion service name has an invalid v3 checksum")
		}
	} else {
		if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
			strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
			return "", "", errors.New("local hostnames are not allowed")
		}
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				return "", "", errors.New("local or private IP addresses are not allowed")
			}
		} else {
			if !torResearchHostPattern.MatchString(host) || strings.Contains(host, "..") {
				return "", "", errors.New("seed hostname must use canonical ASCII DNS syntax")
			}
			for _, label := range strings.Split(host, ".") {
				if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return "", "", errors.New("seed hostname has an invalid DNS label")
				}
			}
		}
	}
	hostForURL := host
	if strings.Contains(host, ":") {
		hostForURL = "[" + host + "]"
	}
	parsed.Host = hostForURL
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	origin := parsed.Scheme + "://" + hostForURL
	return parsed.String(), origin, nil
}

type ResearchEgressGrant struct {
	ID              int64           `json:"id"`
	TaskID          int64           `json:"task_id"`
	ProfileID       string          `json:"profile_id"`
	Seeds           json.RawMessage `json:"seeds"`
	Origins         json.RawMessage `json:"origins"`
	SeedSHA256      string          `json:"seed_sha256"`
	ContractVersion string          `json:"contract_version"`
	NoticeVersion   string          `json:"notice_version"`
	ApprovedBy      string          `json:"approved_by"`
	ApprovedAt      time.Time       `json:"approved_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	MaxRequests     int             `json:"max_requests"`
	RevokedAt       *time.Time      `json:"revoked_at,omitempty"`
	RevokedBy       string          `json:"revoked_by,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

const researchEgressGrantCols = `id,task_id,profile_id,seeds,origins,seed_sha256,
contract_version,notice_version,approved_by,approved_at,expires_at,max_requests,
revoked_at,revoked_by,created_at,updated_at`

func scanResearchEgressGrant(row interface{ Scan(...any) error }) (*ResearchEgressGrant, error) {
	grant := &ResearchEgressGrant{}
	err := row.Scan(&grant.ID, &grant.TaskID, &grant.ProfileID, &grant.Seeds, &grant.Origins,
		&grant.SeedSHA256, &grant.ContractVersion, &grant.NoticeVersion, &grant.ApprovedBy,
		&grant.ApprovedAt, &grant.ExpiresAt, &grant.MaxRequests, &grant.RevokedAt,
		&grant.RevokedBy, &grant.CreatedAt, &grant.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return grant, err
}

func normalizeTorResearchSeeds(seeds []string) (json.RawMessage, json.RawMessage, string, error) {
	if len(seeds) == 0 || len(seeds) > 32 {
		return nil, nil, "", errors.New("between 1 and 32 Tor research seeds are required")
	}
	normalized := make([]string, 0, len(seeds))
	origins := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		canonical, origin, err := NormalizeTorResearchURL(seed)
		if err != nil {
			return nil, nil, "", err
		}
		normalized = append(normalized, canonical)
		origins = append(origins, origin)
	}
	sort.Strings(normalized)
	sort.Strings(origins)
	normalized = compactStrings(normalized)
	origins = compactStrings(origins)
	seedRaw, err := json.Marshal(normalized)
	if err != nil {
		return nil, nil, "", err
	}
	originRaw, err := json.Marshal(origins)
	if err != nil {
		return nil, nil, "", err
	}
	sum := sha256.Sum256(seedRaw)
	return seedRaw, originRaw, hex.EncodeToString(sum[:]), nil
}

func egressGrantCovers(grant *ResearchEgressGrant, origin string) bool {
	if grant == nil {
		return false
	}
	var origins []string
	if json.Unmarshal(grant.Origins, &origins) != nil {
		return false
	}
	for _, allowed := range origins {
		if allowed == origin {
			return true
		}
	}
	return false
}

func (d *DB) CreateResearchEgressGrant(ctx context.Context, taskID int64, profileID string,
	seeds []string, approvedBy string, expiresAt time.Time, maxRequests int) (*ResearchEgressGrant, error) {
	profileID = strings.TrimSpace(profileID)
	approvedBy = strings.TrimSpace(approvedBy)
	if taskID <= 0 || profileID != TorResearchProfileID || approvedBy == "" ||
		!expiresAt.After(time.Now().UTC()) || maxRequests <= 0 || maxRequests > 10000 {
		return nil, errors.New("task, tor profile, approver, future expiry and max_requests 1..10000 are required")
	}
	seedRaw, originRaw, digest, err := normalizeTorResearchSeeds(seeds)
	if err != nil {
		return nil, err
	}
	grant, err := scanResearchEgressGrant(d.QueryRowContext(ctx, `
INSERT INTO research_egress_grants(
 task_id,profile_id,seeds,origins,seed_sha256,contract_version,notice_version,
 approved_by,expires_at,max_requests)
SELECT $1,$2,$3::jsonb,$4::jsonb,$5,$6,$7,$8,$9,$10
WHERE EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL)
RETURNING `+researchEgressGrantCols, taskID, profileID, string(seedRaw), string(originRaw), digest,
		TorResearchContractVersion, ResearchEgressNoticeVersion, approvedBy, expiresAt.UTC(), maxRequests))
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return nil, errors.New("task is unavailable for a research egress grant")
	}
	return grant, nil
}

func (d *DB) ListResearchEgressGrants(ctx context.Context, taskID int64) ([]ResearchEgressGrant, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+researchEgressGrantCols+`
FROM research_egress_grants WHERE task_id=$1 ORDER BY id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResearchEgressGrant{}
	for rows.Next() {
		grant, err := scanResearchEgressGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *grant)
	}
	return out, rows.Err()
}

func (d *DB) MatchingResearchEgressGrant(ctx context.Context, taskID int64, profileID, origin string,
	now time.Time) (*ResearchEgressGrant, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+researchEgressGrantCols+`
FROM research_egress_grants
WHERE task_id=$1 AND profile_id=$2 AND contract_version=$3
 AND revoked_at IS NULL AND expires_at>$4
ORDER BY approved_at DESC,id DESC`, taskID, profileID, TorResearchContractVersion, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		grant, err := scanResearchEgressGrant(rows)
		if err != nil {
			return nil, err
		}
		if egressGrantCovers(grant, origin) {
			return grant, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrResearchEgressGrant
}

func (d *DB) RevokeResearchEgressGrant(ctx context.Context, taskID, grantID int64, revokedBy string) (bool, error) {
	result, err := d.ExecContext(ctx, `UPDATE research_egress_grants
SET revoked_at=COALESCE(revoked_at,now()),
    revoked_by=CASE WHEN revoked_at IS NULL THEN $3 ELSE revoked_by END
WHERE id=$1 AND task_id=$2`, grantID, taskID, strings.TrimSpace(revokedBy))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

type ResearchDisclosureGrant struct {
	ID              int64           `json:"id"`
	TaskID          int64           `json:"task_id"`
	Provider        string          `json:"provider"`
	Identities      json.RawMessage `json:"identities"`
	IdentitySHA256  string          `json:"identity_sha256"`
	ContractVersion string          `json:"contract_version"`
	NoticeVersion   string          `json:"notice_version"`
	ApprovedBy      string          `json:"approved_by"`
	ApprovedAt      time.Time       `json:"approved_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	MaxRequests     int             `json:"max_requests"`
	RevokedAt       *time.Time      `json:"revoked_at,omitempty"`
	RevokedBy       string          `json:"revoked_by,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

const researchGrantCols = `id,task_id,provider,identities,identity_sha256,
contract_version,notice_version,approved_by,approved_at,expires_at,max_requests,
revoked_at,revoked_by,created_at,updated_at`

func scanResearchGrant(row interface{ Scan(...any) error }) (*ResearchDisclosureGrant, error) {
	grant := &ResearchDisclosureGrant{}
	err := row.Scan(&grant.ID, &grant.TaskID, &grant.Provider, &grant.Identities,
		&grant.IdentitySHA256, &grant.ContractVersion, &grant.NoticeVersion,
		&grant.ApprovedBy, &grant.ApprovedAt, &grant.ExpiresAt, &grant.MaxRequests,
		&grant.RevokedAt, &grant.RevokedBy, &grant.CreatedAt, &grant.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return grant, err
}

// ResearchTaskIdentities returns the normalized disclosure identities that are
// currently in a task's explicit scope. It deliberately does not infer targets
// from free-form task text or model output.
func (d *DB) ResearchTaskIdentities(ctx context.Context, taskID int64) ([]string, error) {
	if taskID <= 0 {
		return nil, ErrResearchUnavailable
	}
	rows, err := d.QueryContext(ctx, `
SELECT identity FROM (
 SELECT lower(domain) AS identity FROM task_scope
  WHERE task_id=$1 AND domain IS NOT NULL AND domain<>''
 UNION
 SELECT lower(net::text) AS identity FROM task_scope
  WHERE task_id=$1 AND net IS NOT NULL
 UNION
 SELECT lower(value) AS identity FROM task_scope
  WHERE task_id=$1 AND value IS NOT NULL AND value<>''
 UNION
 SELECT lower(company.name) AS identity FROM task_scope scope
  JOIN companies company ON company.id=scope.company_id
  WHERE scope.task_id=$1
) identities WHERE identity<>'' ORDER BY identity`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return out, rows.Err()
}

func researchIdentityDigest(identities []string) (json.RawMessage, string, error) {
	values := append([]string(nil), identities...)
	sort.Strings(values)
	values = compactStrings(values)
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func compactStrings(values []string) []string {
	// Keep the empty form as [] rather than nil so JSON persistence satisfies
	// the array-only disclosure and artifact constraints.
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || (len(out) > 0 && value == out[len(out)-1]) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func boundedDBText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func (d *DB) CreateResearchDisclosureGrant(
	ctx context.Context,
	taskID int64,
	provider, approvedBy string,
	expiresAt time.Time,
	maxRequests int,
) (*ResearchDisclosureGrant, error) {
	provider = strings.TrimSpace(provider)
	approvedBy = strings.TrimSpace(approvedBy)
	if taskID <= 0 || !ValidResearchProvider(provider) || approvedBy == "" ||
		!expiresAt.After(time.Now().UTC()) || maxRequests <= 0 || maxRequests > 10000 {
		return nil, errors.New("task, supported provider, approver, future expiry and max_requests 1..10000 are required")
	}
	identities, err := d.ResearchTaskIdentities(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(identities) == 0 {
		return nil, errors.New("task has no explicit scope identities to authorize")
	}
	raw, digest, err := researchIdentityDigest(identities)
	if err != nil {
		return nil, err
	}
	contractVersion := ResearchContractForProvider(provider)
	grant, err := scanResearchGrant(d.QueryRowContext(ctx, `
INSERT INTO research_disclosure_grants(
 task_id,provider,identities,identity_sha256,contract_version,notice_version,
 approved_by,expires_at,max_requests)
SELECT $1,$2,$3::jsonb,$4,$5,$6,$7,$8,$9
WHERE EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL)
RETURNING `+researchGrantCols, taskID, provider, string(raw), digest,
		contractVersion, ResearchNoticeVersion, approvedBy, expiresAt.UTC(), maxRequests))
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return nil, errors.New("task is unavailable for a research disclosure grant")
	}
	return grant, nil
}

func (d *DB) ListResearchDisclosureGrants(ctx context.Context, taskID int64) ([]ResearchDisclosureGrant, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+researchGrantCols+`
FROM research_disclosure_grants WHERE task_id=$1 ORDER BY id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResearchDisclosureGrant{}
	for rows.Next() {
		grant, err := scanResearchGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *grant)
	}
	return out, rows.Err()
}

func grantCovers(grant *ResearchDisclosureGrant, identities []string) bool {
	if grant == nil {
		return false
	}
	var values []string
	if json.Unmarshal(grant.Identities, &values) != nil {
		return false
	}
	allowed := make(map[string]bool, len(values))
	for _, value := range values {
		allowed[value] = true
	}
	for _, identity := range identities {
		if !allowed[identity] {
			return false
		}
	}
	return true
}

func (d *DB) MatchingResearchDisclosureGrant(
	ctx context.Context,
	taskID int64,
	provider string,
	identities []string,
	now time.Time,
) (*ResearchDisclosureGrant, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+researchGrantCols+`
FROM research_disclosure_grants
WHERE task_id=$1 AND provider=$2 AND contract_version=$3
 AND revoked_at IS NULL AND expires_at>$4
ORDER BY approved_at DESC,id DESC`, taskID, provider, ResearchContractForProvider(provider), now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		grant, err := scanResearchGrant(rows)
		if err != nil {
			return nil, err
		}
		if grantCovers(grant, identities) {
			return grant, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrResearchGrantNotFound
}

func (d *DB) RevokeResearchDisclosureGrant(ctx context.Context, taskID, grantID int64, revokedBy string) (bool, error) {
	result, err := d.ExecContext(ctx, `UPDATE research_disclosure_grants
SET revoked_at=COALESCE(revoked_at,now()),
    revoked_by=CASE WHEN revoked_at IS NULL THEN $3 ELSE revoked_by END
WHERE id=$1 AND task_id=$2`, grantID, taskID, strings.TrimSpace(revokedBy))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

type ResearchAttemptStart struct {
	TaskID            int64
	SessionID         string
	AgentKey          string
	ExplorationID     int64
	IntentID          int64
	ToolName          string
	GrantID           *int64
	EgressGrantID     *int64
	Provider          string
	EgressProfile     string
	RequestedOrigin   string
	IsolationSHA256   string
	AttemptID         string
	RetryIndex        int
	QuerySHA256       string
	QueryProjection   string
	TargetIdentifying bool
	MatchedIdentities []string
	RequestSHA256     string
	CacheKey          string
	CostUnits         int
	RequestBudget     int
	PaidRequestBudget int
	RatePerMinute     int
	StartedAt         time.Time
}

type ResearchArtifactFinish struct {
	ResponseStatus    *int
	ProviderRequestID string
	RawBodySHA256     string
	ProjectionSHA256  string
	Projection        json.RawMessage
	CacheExpiresAt    *time.Time
	StaleExpiresAt    *time.Time
	SchemaVersion     string
	ETag              string
	LastModified      string
	RetrievedAt       *time.Time
	SourceArtifactID  *int64
	Outcome           string
	ErrorCode         string
	FinishedAt        time.Time
}

type ResearchArtifact struct {
	ID                int64           `json:"id"`
	TaskID            *int64          `json:"task_id,omitempty"`
	SessionID         string          `json:"session_id,omitempty"`
	AgentKey          string          `json:"agent_key"`
	ExplorationID     int64           `json:"exploration_id"`
	IntentID          int64           `json:"intent_id"`
	ToolName          string          `json:"tool_name"`
	GrantID           *int64          `json:"grant_id,omitempty"`
	EgressGrantID     *int64          `json:"egress_grant_id,omitempty"`
	Provider          string          `json:"provider"`
	EgressProfile     string          `json:"egress_profile"`
	IsolationSHA256   string          `json:"isolation_sha256,omitempty"`
	AttemptID         string          `json:"attempt_id"`
	RetryIndex        int             `json:"retry_index"`
	QuerySHA256       string          `json:"query_sha256"`
	QueryProjection   string          `json:"query_projection"`
	TargetIdentifying bool            `json:"target_identifying"`
	MatchedIdentities json.RawMessage `json:"matched_identities"`
	RequestSHA256     string          `json:"request_sha256"`
	ResponseStatus    *int            `json:"response_status,omitempty"`
	ProviderRequestID string          `json:"provider_request_id,omitempty"`
	RawBodySHA256     string          `json:"raw_body_sha256"`
	ProjectionSHA256  string          `json:"projection_sha256"`
	Projection        json.RawMessage `json:"projection"`
	CacheKey          string          `json:"cache_key"`
	CacheExpiresAt    *time.Time      `json:"cache_expires_at,omitempty"`
	StaleExpiresAt    *time.Time      `json:"stale_expires_at,omitempty"`
	SchemaVersion     string          `json:"schema_version,omitempty"`
	ETag              string          `json:"etag,omitempty"`
	LastModified      string          `json:"last_modified,omitempty"`
	RetrievedAt       *time.Time      `json:"retrieved_at,omitempty"`
	SourceArtifactID  *int64          `json:"source_artifact_id,omitempty"`
	Outcome           string          `json:"outcome"`
	ErrorCode         string          `json:"error_code"`
	CostUnits         int             `json:"cost_units"`
	StartedAt         time.Time       `json:"started_at"`
	FinishedAt        time.Time       `json:"finished_at"`
	CreatedAt         time.Time       `json:"created_at"`
}

const researchArtifactCols = `id,task_id,session_id,agent_key,exploration_id,intent_id,tool_name,
grant_id,egress_grant_id,provider,egress_profile,isolation_sha256,
attempt_id,retry_index,query_sha256,query_projection,target_identifying,matched_identities,
request_sha256,response_status,provider_request_id,raw_body_sha256,projection_sha256,projection,cache_key,
cache_expires_at,stale_expires_at,schema_version,etag,last_modified,retrieved_at,
source_artifact_id,outcome,error_code,cost_units,started_at,finished_at,created_at`

func scanResearchArtifact(row interface{ Scan(...any) error }) (*ResearchArtifact, error) {
	item := &ResearchArtifact{}
	err := row.Scan(&item.ID, &item.TaskID, &item.SessionID, &item.AgentKey,
		&item.ExplorationID, &item.IntentID, &item.ToolName, &item.GrantID, &item.EgressGrantID,
		&item.Provider, &item.EgressProfile, &item.IsolationSHA256, &item.AttemptID,
		&item.RetryIndex, &item.QuerySHA256,
		&item.QueryProjection, &item.TargetIdentifying, &item.MatchedIdentities,
		&item.RequestSHA256, &item.ResponseStatus, &item.ProviderRequestID, &item.RawBodySHA256,
		&item.ProjectionSHA256, &item.Projection, &item.CacheKey, &item.CacheExpiresAt,
		&item.StaleExpiresAt, &item.SchemaVersion, &item.ETag, &item.LastModified,
		&item.RetrievedAt, &item.SourceArtifactID, &item.Outcome, &item.ErrorCode, &item.CostUnits,
		&item.StartedAt, &item.FinishedAt, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func researchRunKey(taskID int64, sessionID string) (string, error) {
	if taskID > 0 && strings.TrimSpace(sessionID) == "" {
		return fmt.Sprintf("task:%d", taskID), nil
	}
	if taskID == 0 && strings.TrimSpace(sessionID) != "" && len(sessionID) <= 240 {
		return "session:" + sessionID, nil
	}
	return "", ErrResearchUnavailable
}

func validResearchOutcome(value string) bool {
	switch value {
	case "success", "http_error", "network_error", "contract_error", "rate_limited", "policy_denied", "oversized":
		return true
	default:
		return false
	}
}

// BeginResearchAttempt atomically reserves request/rate/paid/grant budget and
// inserts the immutable start record before any network operation begins.
func (d *DB) BeginResearchAttempt(ctx context.Context, input ResearchAttemptStart) (*ResearchArtifact, error) {
	runKey, err := researchRunKey(input.TaskID, input.SessionID)
	input.AgentKey = strings.TrimSpace(input.AgentKey)
	input.ToolName = strings.TrimSpace(input.ToolName)
	if input.ToolName == "" {
		input.ToolName = "web_search"
	}
	if err != nil || !validResearchToolProvider(input.ToolName, input.Provider) || input.AttemptID == "" ||
		input.AgentKey == "" || len(input.AgentKey) > 128 || input.ExplorationID < 0 || input.IntentID < 0 ||
		input.EgressProfile != "direct" && input.EgressProfile != "operator_proxy" && input.EgressProfile != TorResearchProfileID {
		return nil, ErrResearchUnavailable
	}
	if input.EgressProfile == TorResearchProfileID {
		if input.ToolName != "research_fetch" || input.TaskID <= 0 || input.EgressGrantID == nil ||
			input.GrantID != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(input.IsolationSHA256) {
			return nil, ErrResearchUnavailable
		}
	} else if input.EgressGrantID != nil || input.RequestedOrigin != "" || input.IsolationSHA256 != "" {
		return nil, ErrResearchUnavailable
	}
	matchedRaw, _ := json.Marshal(compactStrings(append([]string(nil), input.MatchedIdentities...)))
	if input.StartedAt.IsZero() {
		input.StartedAt = time.Now().UTC()
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7337742))`, runKey); err != nil {
		return nil, err
	}
	where := `task_id=$1`
	identity := any(input.TaskID)
	if input.TaskID == 0 {
		where = `session_id=$1`
		identity = input.SessionID
	}
	var requests, paid, recent int
	query := `SELECT count(*),COALESCE(sum(cost_units),0),
count(*) FILTER (WHERE started_at >= $2)
FROM research_artifacts WHERE ` + where + ` AND outcome NOT IN ('cache_hit','stale_cache')`
	if err := tx.QueryRowContext(ctx, query, identity, input.StartedAt.Add(-time.Minute)).Scan(&requests, &paid, &recent); err != nil {
		return nil, err
	}
	if input.RequestBudget > 0 && requests >= input.RequestBudget {
		return nil, ErrResearchBudget
	}
	if input.PaidRequestBudget > 0 && paid+input.CostUnits > input.PaidRequestBudget {
		return nil, ErrResearchBudget
	}
	if input.RatePerMinute > 0 && recent >= input.RatePerMinute {
		return nil, ErrResearchRate
	}
	if input.TargetIdentifying {
		if input.TaskID <= 0 || input.GrantID == nil {
			return nil, ErrResearchGrantNotFound
		}
		grant, err := scanResearchGrant(tx.QueryRowContext(ctx, `SELECT `+researchGrantCols+`
FROM research_disclosure_grants WHERE id=$1 AND task_id=$2 AND provider=$3
 AND contract_version=$4 AND revoked_at IS NULL AND expires_at>$5 FOR UPDATE`,
			*input.GrantID, input.TaskID, input.Provider,
			ResearchContractForProvider(input.Provider), input.StartedAt))
		if err != nil {
			return nil, err
		}
		if grant == nil || !grantCovers(grant, input.MatchedIdentities) {
			return nil, ErrResearchGrantNotFound
		}
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM research_artifacts
WHERE grant_id=$1 AND outcome NOT IN ('cache_hit','stale_cache')`, grant.ID).Scan(&used); err != nil {
			return nil, err
		}
		if used >= grant.MaxRequests {
			return nil, ErrResearchBudget
		}
	}
	if input.EgressProfile == TorResearchProfileID {
		grant, err := scanResearchEgressGrant(tx.QueryRowContext(ctx, `SELECT `+researchEgressGrantCols+`
FROM research_egress_grants WHERE id=$1 AND task_id=$2 AND profile_id=$3
 AND contract_version=$4 AND revoked_at IS NULL AND expires_at>$5 FOR UPDATE`,
			*input.EgressGrantID, input.TaskID, TorResearchProfileID,
			TorResearchContractVersion, input.StartedAt))
		if err != nil {
			return nil, err
		}
		if grant == nil || !egressGrantCovers(grant, input.RequestedOrigin) {
			return nil, ErrResearchEgressGrant
		}
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM research_artifacts
WHERE egress_grant_id=$1 AND outcome NOT IN ('cache_hit','stale_cache')`, grant.ID).Scan(&used); err != nil {
			return nil, err
		}
		if used >= grant.MaxRequests {
			return nil, ErrResearchBudget
		}
	}
	var taskValue any
	if input.TaskID > 0 {
		taskValue = input.TaskID
	}
	item, err := scanResearchArtifact(tx.QueryRowContext(ctx, `
INSERT INTO research_artifacts(
 task_id,session_id,agent_key,exploration_id,intent_id,tool_name,
 grant_id,egress_grant_id,provider,egress_profile,isolation_sha256,attempt_id,retry_index,
 query_sha256,query_projection,target_identifying,matched_identities,request_sha256,
 cache_key,outcome,error_code,cost_units,started_at,finished_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18,$19,'started','',$20,$21,$21)
RETURNING `+researchArtifactCols, taskValue, input.SessionID, input.AgentKey,
		input.ExplorationID, input.IntentID, input.ToolName, input.GrantID, input.EgressGrantID,
		input.Provider, input.EgressProfile, input.IsolationSHA256, input.AttemptID, input.RetryIndex, input.QuerySHA256,
		input.QueryProjection, input.TargetIdentifying, string(matchedRaw), input.RequestSHA256,
		input.CacheKey, input.CostUnits, input.StartedAt.UTC()))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (d *DB) FinishResearchArtifact(ctx context.Context, id int64, finish ResearchArtifactFinish) (*ResearchArtifact, error) {
	if id <= 0 || !validResearchOutcome(finish.Outcome) {
		return nil, errors.New("invalid research artifact completion")
	}
	if finish.FinishedAt.IsZero() {
		finish.FinishedAt = time.Now().UTC()
	}
	if len(finish.Projection) == 0 {
		finish.Projection = json.RawMessage(`{}`)
	}
	item, err := scanResearchArtifact(d.QueryRowContext(ctx, `UPDATE research_artifacts SET
 response_status=$2,provider_request_id=$3,raw_body_sha256=$4,projection_sha256=$5,projection=$6::jsonb,
 cache_expires_at=$7,stale_expires_at=$8,schema_version=$9,etag=$10,last_modified=$11,
 retrieved_at=$12,source_artifact_id=$13,outcome=$14,error_code=$15,finished_at=$16
WHERE id=$1 AND outcome='started' RETURNING `+researchArtifactCols,
		id, finish.ResponseStatus, finish.ProviderRequestID, finish.RawBodySHA256, finish.ProjectionSHA256,
		string(finish.Projection), finish.CacheExpiresAt, finish.StaleExpiresAt,
		boundedDBText(finish.SchemaVersion, 120), boundedDBText(finish.ETag, 512),
		boundedDBText(finish.LastModified, 512), finish.RetrievedAt, finish.SourceArtifactID,
		finish.Outcome, finish.ErrorCode, finish.FinishedAt.UTC()))
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errors.New("research artifact is not in started state")
	}
	return item, nil
}

func (d *DB) FindResearchCache(
	ctx context.Context,
	taskID int64,
	sessionID, provider, cacheKey string,
	now time.Time,
) (*ResearchArtifact, error) {
	return d.FindResearchCacheForTool(ctx, taskID, sessionID, "web_search", provider, cacheKey, now)
}

func (d *DB) FindResearchCacheForTool(
	ctx context.Context,
	taskID int64,
	sessionID, toolName, provider, cacheKey string,
	now time.Time,
) (*ResearchArtifact, error) {
	if _, err := researchRunKey(taskID, sessionID); err != nil {
		return nil, err
	}
	query := `SELECT ` + researchArtifactCols + ` FROM research_artifacts
WHERE task_id=$1 AND tool_name=$2 AND provider=$3 AND cache_key=$4
 AND outcome='success' AND cache_expires_at>$5
ORDER BY id DESC LIMIT 1`
	args := []any{taskID, toolName, provider, cacheKey, now.UTC()}
	if taskID == 0 {
		query = `SELECT ` + researchArtifactCols + ` FROM research_artifacts
WHERE session_id=$1 AND tool_name=$2 AND provider=$3 AND cache_key=$4
 AND outcome='success' AND cache_expires_at>$5
ORDER BY id DESC LIMIT 1`
		args[0] = sessionID
	}
	return scanResearchArtifact(d.QueryRowContext(ctx, query, args...))
}

func (d *DB) FindLatestResearchCacheForTool(
	ctx context.Context,
	taskID int64,
	sessionID, toolName, provider, cacheKey string,
) (*ResearchArtifact, error) {
	if _, err := researchRunKey(taskID, sessionID); err != nil {
		return nil, err
	}
	query := `SELECT ` + researchArtifactCols + ` FROM research_artifacts
WHERE task_id=$1 AND tool_name=$2 AND provider=$3 AND cache_key=$4 AND outcome='success'
ORDER BY id DESC LIMIT 1`
	args := []any{taskID, toolName, provider, cacheKey}
	if taskID == 0 {
		query = `SELECT ` + researchArtifactCols + ` FROM research_artifacts
WHERE session_id=$1 AND tool_name=$2 AND provider=$3 AND cache_key=$4 AND outcome='success'
ORDER BY id DESC LIMIT 1`
		args[0] = sessionID
	}
	return scanResearchArtifact(d.QueryRowContext(ctx, query, args...))
}

func (d *DB) RecordResearchCacheHit(
	ctx context.Context,
	source *ResearchArtifact,
	attemptID, queryProjection, agentKey string,
	explorationID, intentID int64,
	grantID *int64,
	targetIdentifying bool,
	matchedIdentities []string,
	now time.Time,
) (*ResearchArtifact, error) {
	return d.RecordResearchCacheUse(ctx, source, "cache_hit", attemptID, queryProjection,
		agentKey, explorationID, intentID, grantID, targetIdentifying, matchedIdentities, now)
}

func (d *DB) RecordResearchCacheUse(
	ctx context.Context,
	source *ResearchArtifact,
	outcome, attemptID, queryProjection, agentKey string,
	explorationID, intentID int64,
	grantID *int64,
	targetIdentifying bool,
	matchedIdentities []string,
	now time.Time,
) (*ResearchArtifact, error) {
	agentKey = strings.TrimSpace(agentKey)
	if source == nil || source.Outcome != "success" || agentKey == "" || len(agentKey) > 128 || explorationID < 0 || intentID < 0 ||
		(targetIdentifying && grantID == nil) || (outcome != "cache_hit" && outcome != "stale_cache") {
		return nil, errors.New("valid source artifact is required")
	}
	matchedRaw, _ := json.Marshal(compactStrings(append([]string(nil), matchedIdentities...)))
	return scanResearchArtifact(d.QueryRowContext(ctx, `
INSERT INTO research_artifacts(
 task_id,session_id,agent_key,exploration_id,intent_id,tool_name,
 grant_id,egress_grant_id,provider,egress_profile,isolation_sha256,attempt_id,retry_index,
 query_sha256,query_projection,target_identifying,matched_identities,request_sha256,
 response_status,provider_request_id,raw_body_sha256,projection_sha256,projection,cache_key,cache_expires_at,
 stale_expires_at,schema_version,etag,last_modified,retrieved_at,
 source_artifact_id,outcome,error_code,cost_units,started_at,finished_at)
SELECT task_id,session_id,$6,$7,$8,tool_name,$4,egress_grant_id,provider,egress_profile,isolation_sha256,$3,0,query_sha256,$5,
 $9,$10::jsonb,request_sha256,response_status,provider_request_id,
 raw_body_sha256,projection_sha256,projection,cache_key,cache_expires_at,
 stale_expires_at,schema_version,etag,last_modified,retrieved_at,id,$2,'',0,$11,$11
FROM research_artifacts WHERE id=$1 AND outcome='success'
RETURNING `+researchArtifactCols, source.ID, outcome, attemptID, grantID, queryProjection,
		agentKey, explorationID, intentID, targetIdentifying, string(matchedRaw), now.UTC()))
}

func (d *DB) ListResearchArtifacts(ctx context.Context, taskID int64, limit int) ([]ResearchArtifact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `SELECT `+researchArtifactCols+`
FROM research_artifacts WHERE task_id=$1 ORDER BY id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResearchArtifact{}
	for rows.Next() {
		item, err := scanResearchArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}
