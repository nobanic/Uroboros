package arsenal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
	"golang.org/x/text/unicode/norm"
)

const (
	wafProfileRevision    = "wafw00f-passive-response-headers-v1"
	wafVersion            = "2.4.2"
	wafTagCommit          = "e81d38748109a805872ee92fd2377612bd639b3b"
	wafSDISTSHA256        = "05e4a49f6de433793ad506e37ae2c6f537c832959ee6e2bbdf573900f22194ae"
	wafCorpusSHA256       = "fb296e4c6368b668b8c2ae31fecc19c311ddce0644a933becabf79a9a4ce5b9c"
	wafPluginCount        = int64(172)
	wafMaxHeaderFields    = 128
	wafMaxHeaderValueByte = 16 * 1024
	wafMaxHeaderTotalByte = 64 * 1024
	wafMaxMatches         = 32
	wafExecutionTimeout   = 30 * time.Second
)

var wafPluginIDPattern = regexp.MustCompile(`^[a-z0-9_]+[.]py$`)

type wafHeaders map[string][]string

func isHTTPTokenByte(value byte) bool {
	if value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", rune(value))
}

func parseWAFResponseHead(raw string) (wafHeaders, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.ContainsRune(raw, '\r') {
		return nil, errors.New("stored response head is missing or malformed")
	}
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || !regexp.MustCompile(`^HTTP [1-5][0-9][0-9]$`).MatchString(lines[0]) {
		return nil, errors.New("stored response head has an invalid status line")
	}
	headers := make(wafHeaders)
	fields := 0
	total := 0
	for index, line := range lines[1:] {
		if line == "" {
			if index != len(lines[1:])-1 {
				return nil, errors.New("stored response head contains an unexpected empty field")
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || colon+1 >= len(line) || line[colon+1] != ' ' {
			return nil, errors.New("stored response head contains a malformed field")
		}
		name := line[:colon]
		for position := 0; position < len(name); position++ {
			if !isHTTPTokenByte(name[position]) {
				return nil, errors.New("stored response head contains an invalid field name")
			}
		}
		name = strings.ToLower(name)
		value := line[colon+2:]
		if len([]byte(value)) > wafMaxHeaderValueByte {
			return nil, errors.New("stored response header value exceeds the passive profile limit")
		}
		for _, char := range value {
			if unicode.IsControl(char) {
				return nil, errors.New("stored response header value contains control characters")
			}
		}
		fields++
		total += len(name) + 2 + len([]byte(value)) + 1
		if fields > wafMaxHeaderFields || total > wafMaxHeaderTotalByte {
			return nil, errors.New("stored response headers exceed the passive profile limit")
		}
		headers[name] = append(headers[name], value)
	}
	return headers, nil
}

func wafHeaderSHA256(headers wafHeaders) string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		_, _ = digest.Write([]byte(name))
		_, _ = digest.Write([]byte{0})
		for _, value := range headers[name] {
			_, _ = digest.Write([]byte(value))
			_, _ = digest.Write([]byte{0})
		}
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (m *Manager) prepareWAF(taskID int64, request StartRequest) (preparedRun, error) {
	if request.EndpointID <= 0 {
		return preparedRun{}, errors.New("passive WAF fingerprinting requires endpoint_id")
	}
	if strings.TrimSpace(request.Target) != "" || len(request.Config) != 0 || request.MaxRequests != 0 {
		return preparedRun{}, errors.New("passive WAF target, headers, profile and request budget are host-owned")
	}
	admission, err := m.pg.AdmittedWAFEndpoint(context.Background(), taskID, request.EndpointID)
	if err != nil {
		return preparedRun{}, err
	}
	meta, responseHead, err := m.traffic.ResponseHead(admission.TrafficID)
	if err != nil {
		return preparedRun{}, fmt.Errorf("load admitted Traffic response head: %w", err)
	}
	assetTarget, err := canonicalTarget(admission.Asset.URL)
	if err != nil {
		return preparedRun{}, errors.New("admitted WAF endpoint URL is invalid")
	}
	exchangeTarget, err := canonicalTarget(meta.URL)
	if err != nil || !strings.EqualFold(strings.TrimSpace(meta.Method), "GET") || exchangeTarget != assetTarget {
		return preparedRun{}, errors.New("admitted Traffic exchange does not match the GET endpoint")
	}
	headers, err := parseWAFResponseHead(responseHead)
	if err != nil {
		return preparedRun{}, err
	}
	headerHash := wafHeaderSHA256(headers)
	headerJSON, err := canonicalJSON(headers)
	if err != nil {
		return preparedRun{}, err
	}
	snapshot, err := m.assets.TaskNetworkScope(taskID)
	if err != nil {
		return preparedRun{}, fmt.Errorf("build task network scope: %w", err)
	}
	scope, _, err := traffic.NormalizeNetworkScope(traffic.NetworkScopeSnapshot{
		RootDomains: snapshot.RootDomains, ExactHosts: snapshot.ExactHosts, CIDRs: snapshot.CIDRs,
	})
	if err != nil {
		return preparedRun{}, fmt.Errorf("task has no executable network scope: %w", err)
	}
	scopeDoc := map[string]any{
		"root_domains": nonNilStrings(scope.RootDomains),
		"exact_hosts":  nonNilStrings(scope.ExactHosts),
		"cidrs":        nonNilStrings(scope.CIDRs),
	}
	scopeJSON, err := canonicalJSON(scopeDoc)
	if err != nil {
		return preparedRun{}, err
	}
	metadata, err := m.loadMetadata()
	if err != nil {
		return preparedRun{}, err
	}
	config := map[string]any{
		"corpus_sha256": wafCorpusSHA256, "endpoint_id": request.EndpointID,
		"profile_revision": wafProfileRevision, "response_header_sha256": headerHash,
		"sdist_sha256": wafSDISTSHA256, "source_traffic_id": admission.TrafficID,
		"tag_commit": wafTagCommit, "tool_version": wafVersion,
	}
	configJSON, err := canonicalJSON(config)
	if err != nil {
		return preparedRun{}, err
	}
	configEnvelope, err := canonicalJSON(map[string]any{
		"config": config, "target": strconv.FormatInt(request.EndpointID, 10), "tool": "wafw00f",
	})
	if err != nil {
		return preparedRun{}, err
	}
	request.Config = config
	return preparedRun{
		request: request, target: assetTarget, storedTarget: redactURL(assetTarget),
		config: config, configJSON: configJSON, configHash: sha256Hex(configEnvelope),
		scope: scope, scopeJSON: scopeJSON, scopeHash: sha256Hex(scopeJSON), metadata: metadata,
		wafEndpointID: request.EndpointID, wafTrafficID: admission.TrafficID,
		wafHeaderHash: headerHash, wafHeadersJSON: headerJSON,
	}, nil
}

func decodeWAFResult(raw string) (scanResult, error) {
	var result scanResult
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return scanResult{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return scanResult{}, errors.New("multiple JSON values")
		}
		return scanResult{}, errors.New("invalid trailing JSON data")
	}
	return result, nil
}

func (m *Manager) executeWAF(parent context.Context, job *db.ArsenalJob, prepared preparedRun) {
	finish := db.ArsenalJobFinish{Status: "failed", Stats: map[string]any{
		"profile_revision": wafProfileRevision, "endpoint_id": prepared.wafEndpointID,
		"source_traffic_id":      prepared.wafTrafficID,
		"response_header_sha256": prepared.wafHeaderHash, "target_requests": 0,
	}}
	defer func() {
		if err := m.pg.FinishArsenalJob(context.Background(), job.ID, finish); err != nil {
			log.Printf("[arsenal] passive WAF job %d finalization failed: %v", job.ID, err)
		}
	}()
	if err := m.pg.MarkArsenalJobRunning(context.Background(), job.ID); err != nil {
		finish.Error = err.Error()
		return
	}
	runCtx, cancel := context.WithTimeout(parent, wafExecutionTimeout)
	defer cancel()
	client, err := m.connect(runCtx)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	args := map[string]any{
		"task_id": job.TaskID, "run_id": strconv.FormatInt(job.ID, 10),
		"endpoint_id": prepared.wafEndpointID, "source_traffic_id": prepared.wafTrafficID,
		"scope_json": string(prepared.scopeJSON), "scope_hash": prepared.scopeHash,
		"config_json": string(prepared.configJSON), "config_hash": prepared.configHash,
		"headers_json":           string(prepared.wafHeadersJSON),
		"response_header_sha256": prepared.wafHeaderHash,
	}
	raw, callErr := client.Call(runCtx, "arsenal_task_waf", args)
	closeErr := client.Close()
	if cause := context.Cause(parent); cause != nil {
		finish.Status = "canceled"
		finish.ExitState = "cancelled"
		finish.Error = cause.Error()
		return
	}
	if callErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			finish.ExitState = "timeout"
		}
		finish.Error = "call passive WAF backend: " + callErr.Error()
		return
	}
	if closeErr != nil {
		finish.Error = "close passive WAF backend session: " + closeErr.Error()
		return
	}
	result, err := decodeWAFResult(raw)
	if err != nil {
		finish.Error = "decode passive WAF result: " + err.Error()
		return
	}
	finish.ExitState = result.ExitState
	backendStats, provenance, err := validateResult(job, prepared, result)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	if err := compareMetadata(prepared.metadata, provenance); err != nil {
		finish.Error = err.Error()
		return
	}
	if err := validateWAFStats(backendStats, prepared.wafHeaderHash, len(result.Records)); err != nil {
		finish.Error = err.Error()
		return
	}
	// Re-read only the response head before publication.  A deleted/replaced
	// Traffic row or changed exchange cannot retain proxy-verified authority.
	meta, responseHead, err := m.traffic.ResponseHead(prepared.wafTrafficID)
	if err != nil || !strings.EqualFold(meta.Method, "GET") {
		finish.Error = "source Traffic exchange disappeared before WAF publication"
		return
	}
	exchangeTarget, err := canonicalTarget(meta.URL)
	if err != nil || exchangeTarget != prepared.target {
		finish.Error = "source Traffic exchange changed before WAF publication"
		return
	}
	headers, err := parseWAFResponseHead(responseHead)
	if err != nil || wafHeaderSHA256(headers) != prepared.wafHeaderHash {
		finish.Error = "source Traffic response head changed before WAF publication"
		return
	}
	published, err := m.persistWAFObservations(job, prepared, result.Records)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	backendStats["published_candidates"] = published
	finish.Stats = backendStats
	if !result.Complete || (result.ExitState != "ok" && result.ExitState != "ok_empty") {
		finish.Error = fmt.Sprintf("passive WAF fingerprinting ended with %s and is not a completed negative", result.ExitState)
		return
	}
	finish.Status = "completed"
	finish.Complete = true
	finish.Error = ""
}

func validateWAFStats(stats map[string]any, headerHash string, records int) error {
	profile, _ := stats["profile_revision"].(string)
	hash, _ := stats["response_header_sha256"].(string)
	evaluated, evaluatedOK := integerValue(stats["evaluated_plugins"])
	matches, matchesOK := integerValue(stats["match_count"])
	requests, requestsOK := integerValue(stats["target_requests"])
	if profile != wafProfileRevision || hash != headerHash || !evaluatedOK || evaluated != wafPluginCount ||
		!matchesOK || matches != int64(records) || !requestsOK || requests != 0 {
		return errors.New("passive WAF backend returned invalid zero-request statistics")
	}
	return nil
}

func cleanWAFLabel(value, label string, allowEmpty bool) (string, error) {
	if allowEmpty && value == "" {
		return "", nil
	}
	if value == "" || value != strings.TrimSpace(value) || len([]byte(value)) > 256 || !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
		return "", fmt.Errorf("passive WAF %s is not normalized", label)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", fmt.Errorf("passive WAF %s contains control characters", label)
		}
	}
	return value, nil
}

func validateWAFRecord(record scannerRecord) (map[string]any, error) {
	if record.Tool != "wafw00f" || record.Type != "waf_fingerprint" ||
		record.Severity != "" || record.Name != "" || record.Target != "" || record.Endpoint != "" ||
		record.Evidence != "" || record.TemplateID != "" || record.ObservationID != "" ||
		record.DedupFP != "" || record.Raw != "" || record.Stage != "" || record.Host != "" ||
		record.Address != "" || record.Port != 0 || record.EndpointID != 0 || record.Method != "" ||
		record.Location != "" || record.Parameter != "" || !wafPluginIDPattern.MatchString(record.PluginID) {
		return nil, errors.New("passive WAF backend returned a record outside the candidate-only contract")
	}
	vendor, err := cleanWAFLabel(record.Vendor, "vendor", false)
	if err != nil {
		return nil, err
	}
	manufacturer, err := cleanWAFLabel(record.Manufacturer, "manufacturer", true)
	if err != nil {
		return nil, err
	}
	if len(record.EvidenceKinds) == 0 || len(record.EvidenceKinds) > 2 || !sort.StringsAreSorted(record.EvidenceKinds) {
		return nil, errors.New("passive WAF evidence kinds are invalid")
	}
	seen := make(map[string]bool, len(record.EvidenceKinds))
	for _, kind := range record.EvidenceKinds {
		if (kind != "header" && kind != "cookie") || seen[kind] {
			return nil, errors.New("passive WAF evidence kinds are invalid")
		}
		seen[kind] = true
	}
	return map[string]any{
		"vendor": vendor, "manufacturer": manufacturer, "plugin_id": record.PluginID,
		"evidence_kinds": record.EvidenceKinds,
	}, nil
}

func (m *Manager) persistWAFObservations(job *db.ArsenalJob, prepared preparedRun, records []scannerRecord) (int, error) {
	if len(records) > wafMaxMatches {
		return 0, fmt.Errorf("passive WAF backend returned %d matches; maximum is %d", len(records), wafMaxMatches)
	}
	lastPlugin := ""
	published := 0
	for _, raw := range records {
		candidate, err := validateWAFRecord(raw)
		if err != nil {
			return published, err
		}
		pluginID := candidate["plugin_id"].(string)
		if lastPlugin != "" && pluginID <= lastPlugin {
			return published, errors.New("passive WAF records are duplicated or not deterministically sorted")
		}
		lastPlugin = pluginID
		record := map[string]any{
			"vendor": candidate["vendor"], "manufacturer": candidate["manufacturer"],
			"plugin_id": pluginID, "evidence_kinds": candidate["evidence_kinds"],
			"response_header_sha256": prepared.wafHeaderHash,
			"profile_revision":       wafProfileRevision, "tool_version": wafVersion,
			"tag_commit": wafTagCommit, "sdist_sha256": wafSDISTSHA256,
			"corpus_sha256": wafCorpusSHA256, "passive_only": true,
			"active_verified": false, "target_requests": 0,
		}
		key, err := canonicalJSON(map[string]any{
			"task_id": job.TaskID, "endpoint_id": prepared.wafEndpointID,
			"traffic_id": prepared.wafTrafficID, "profile_revision": wafProfileRevision,
			"corpus_sha256": wafCorpusSHA256, "plugin_id": pluginID, "tool": "wafw00f",
		})
		if err != nil {
			return published, err
		}
		assetID := prepared.wafEndpointID
		if _, err := m.pg.UpsertArsenalObservation(context.Background(), db.ArsenalObservationUpsert{
			JobID: job.ID, TaskID: job.TaskID, Tool: "wafw00f", ObservationKey: sha256Hex(key),
			Authority: "candidate", Record: record, ProxyVerified: true,
			TrafficID: prepared.wafTrafficID, AssetID: &assetID,
		}); err != nil {
			return published, fmt.Errorf("store passive WAF candidate: %w", err)
		}
		published++
	}
	return published, nil
}
