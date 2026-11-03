package arsenal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
	"golang.org/x/text/unicode/norm"
)

const (
	arjunProfileRevision      = "arjun-get-query-v1"
	arjunBudgetSeconds        = int64(300)
	arjunMaxRequests          = int64(256)
	arjunThreads              = int64(1)
	arjunRateLimit            = int64(5)
	arjunRequestTimeout       = int64(10)
	arjunChunkSize            = int64(500)
	arjunMaximumAddresses     = 4
	arjunMaximumParameterByte = 128
)

func (m *Manager) prepareArjun(taskID int64, request StartRequest) (preparedRun, error) {
	if request.EndpointID <= 0 {
		return preparedRun{}, errors.New("Arjun requires endpoint_id")
	}
	if strings.TrimSpace(request.Target) != "" || len(request.Config) != 0 || request.MaxRequests != 0 {
		return preparedRun{}, errors.New("Arjun target, profile and request budget are host-owned")
	}
	admission, err := m.pg.AdmittedArjunEndpoint(context.Background(), taskID, request.EndpointID)
	if err != nil {
		return preparedRun{}, err
	}
	target, err := canonicalArjunEndpoint(admission.Asset.URL)
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
	if !scope.AllowsURL(target) {
		return preparedRun{}, errors.New("Arjun endpoint is outside the frozen task scope")
	}
	metadata, err := m.loadMetadata()
	if err != nil {
		return preparedRun{}, err
	}
	routeSource := "task_scope_resolver"
	var routeEvidenceJob int64
	var routeEvidenceFlow string
	evidence, err := m.pg.ArjunDiscoveryRouteEvidence(context.Background(), taskID, request.EndpointID)
	if err != nil {
		return preparedRun{}, err
	}
	var routes []traffic.ScopedTCPRoute
	if evidence != nil {
		routes, routeEvidenceFlow, err = arjunRoutesFromDiscoveryEvidence(
			taskID, request.EndpointID, target, evidence,
		)
		if err != nil {
			return preparedRun{}, fmt.Errorf("invalid Arjun discovery route evidence: %w", err)
		}
		routeSource = "completed_discovery"
		routeEvidenceJob = evidence.Job.ID
	} else {
		routes, err = m.resolveArjunRoutes(target, scope)
		if err != nil {
			return preparedRun{}, err
		}
	}
	exactHash, err := traffic.ExactEndpointPolicyFingerprint(scope, []string{target})
	if err != nil {
		return preparedRun{}, fmt.Errorf("fingerprint Arjun endpoint policy: %w", err)
	}
	routeHash, err := traffic.ScopedTCPRoutesFingerprint(routes)
	if err != nil {
		return preparedRun{}, fmt.Errorf("fingerprint Arjun route: %w", err)
	}
	config := map[string]any{
		"profile_revision":        arjunProfileRevision,
		"endpoint_id":             request.EndpointID,
		"method":                  "GET",
		"budget_seconds":          arjunBudgetSeconds,
		"max_requests":            arjunMaxRequests,
		"threads":                 arjunThreads,
		"rate_limit":              arjunRateLimit,
		"request_timeout_seconds": arjunRequestTimeout,
		"chunk_size":              arjunChunkSize,
		"artifact_sha256":         expectedTools["arjun"].Artifact,
		"wordlist_sha256":         metadata.ArjunWordlistSHA256,
		"special_sha256":          metadata.ArjunSpecialSHA256,
		"dependency_lock_sha256":  metadata.DependencyLockSHA256,
		"exact_endpoint_sha256":   exactHash,
		"route_sha256":            routeHash,
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
	configJSON, err := canonicalJSON(config)
	if err != nil {
		return preparedRun{}, err
	}
	configEnvelope, err := canonicalJSON(map[string]any{"config": config, "target": target, "tool": "arjun"})
	if err != nil {
		return preparedRun{}, err
	}
	request.Config = config
	request.MaxRequests = arjunMaxRequests
	return preparedRun{
		request: request, target: target, storedTarget: target, config: config,
		configJSON: configJSON, configHash: sha256Hex(configEnvelope), scope: scope,
		scopeJSON: scopeJSON, scopeHash: sha256Hex(scopeJSON), metadata: metadata,
		arjunEndpointID: request.EndpointID, arjunRoutes: routes,
		arjunRouteSource: routeSource, arjunEvidenceJob: routeEvidenceJob,
		arjunEvidenceFlow: routeEvidenceFlow,
		exactEndpointHash: exactHash, routeHash: routeHash,
	}, nil
}

func canonicalArjunEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Opaque != "" || parsed.Hostname() == "" {
		return "", errors.New("Arjun endpoint must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", errors.New("Arjun endpoint cannot contain user information, a query or a fragment")
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(parsed.Path, `\`) {
		return "", errors.New("Arjun endpoint path contains an encoded separator")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("Arjun endpoint path contains a dot segment")
		}
	}
	return canonicalTarget(trimmed)
}

func (m *Manager) resolveArjunRoutes(target string, scope traffic.NetworkScopeSnapshot) ([]traffic.ScopedTCPRoute, error) {
	parsed, _ := url.Parse(target)
	host := parsed.Hostname()
	port := 80
	if parsed.Scheme == "https" {
		port = 443
	}
	if parsed.Port() != "" {
		port, _ = strconv.Atoi(parsed.Port())
	}
	addresses := []net.IP{}
	if literal := net.ParseIP(host); literal != nil {
		addresses = append(addresses, literal)
	} else {
		lookupCtx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
		defer cancel()
		var err error
		addresses, err = m.resolveHost(lookupCtx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve Arjun endpoint host: %w", err)
		}
	}
	set := make(map[string]struct{}, len(addresses))
	for _, raw := range addresses {
		address, ok := netip.AddrFromSlice(raw)
		if !ok || address.Zone() != "" {
			return nil, errors.New("Arjun endpoint resolved to an invalid address")
		}
		address = address.Unmap()
		if address.IsUnspecified() || address.IsMulticast() {
			return nil, errors.New("Arjun endpoint resolved to a non-routable address")
		}
		if (!address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast()) &&
			!scope.AllowsURL(parsed.Scheme+"://"+net.JoinHostPort(address.String(), strconv.Itoa(port))+"/") {
			return nil, errors.New("Arjun endpoint resolved to a restricted address absent from task CIDR scope")
		}
		set[address.String()] = struct{}{}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) == 0 {
		return nil, errors.New("Arjun endpoint has no frozen address")
	}
	if len(values) > arjunMaximumAddresses {
		values = values[:arjunMaximumAddresses]
	}
	return []traffic.ScopedTCPRoute{{Host: host, Port: port, Addresses: values}}, nil
}

type arjunDiscoveryRecord struct {
	Tool     string `json:"tool"`
	Stage    string `json:"stage"`
	Type     string `json:"type"`
	Host     string `json:"host"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Endpoint string `json:"endpoint"`
}

// arjunRoutesFromDiscoveryEvidence reconstructs only the exact hostname/port
// route exercised by one completed composite discovery job. It never mutates
// task scope and does not consult caller/model-provided addresses.
func arjunRoutesFromDiscoveryEvidence(taskID, endpointID int64, target string,
	evidence *db.ArjunDiscoveryRouteEvidence) ([]traffic.ScopedTCPRoute, string, error) {
	if evidence == nil || evidence.Job == nil || evidence.Job.ID <= 0 ||
		evidence.Job.TaskID != taskID || evidence.Job.Tool != "discovery" ||
		evidence.Job.Status != "completed" || !evidence.Job.Complete {
		return nil, "", errors.New("route requires one completed same-task discovery job")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" {
		return nil, "", errors.New("route target is invalid")
	}
	host, err := canonicalDiscoveryHost(parsed.Hostname())
	if err != nil {
		return nil, "", errors.New("route target host is invalid")
	}
	port := 80
	if parsed.Scheme == "https" {
		port = 443
	}
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, "", errors.New("route target port is invalid")
		}
	}
	dnsAddresses := make(map[string]struct{})
	openAddresses := make(map[string]struct{})
	matchingEndpoints := 0
	trafficID := ""
	for _, observation := range evidence.Observations {
		if observation.JobID != evidence.Job.ID || observation.TaskID != taskID || observation.Tool != "discovery" {
			return nil, "", errors.New("route observation has unrelated job or task provenance")
		}
		var record arjunDiscoveryRecord
		if err := json.Unmarshal(observation.Record, &record); err != nil {
			return nil, "", errors.New("route observation record is invalid")
		}
		record.Tool = strings.ToLower(strings.TrimSpace(record.Tool))
		record.Stage = strings.ToLower(strings.TrimSpace(record.Stage))
		if record.Tool != record.Stage || observation.Authority != "candidate" {
			return nil, "", errors.New("route observation has invalid discovery authority")
		}
		switch record.Stage {
		case "dns":
			if record.Type != "dns" || observation.ProxyVerified || observation.TrafficID != "" || observation.AssetID != nil {
				return nil, "", errors.New("DNS route evidence crossed its intermediate boundary")
			}
			recordHost, address, err := canonicalArjunRouteBinding(record.Host, record.Address)
			if err != nil {
				return nil, "", fmt.Errorf("invalid DNS route evidence: %w", err)
			}
			if recordHost == host {
				dnsAddresses[address] = struct{}{}
			}
		case "naabu":
			if record.Type != "port" || observation.ProxyVerified || observation.TrafficID != "" || observation.AssetID != nil {
				return nil, "", errors.New("naabu route evidence crossed its intermediate boundary")
			}
			recordHost, address, err := canonicalArjunRouteBinding(record.Host, record.Address)
			if err != nil || record.Port < 1 || record.Port > 65535 {
				return nil, "", errors.New("invalid naabu route evidence")
			}
			if recordHost == host && record.Port == port {
				openAddresses[address] = struct{}{}
			}
		case "httpx":
			if observation.AssetID == nil || *observation.AssetID != endpointID {
				return nil, "", errors.New("httpx route evidence references an unrelated endpoint")
			}
			matchingEndpoints++
			if record.Type != "service" || !observation.ProxyVerified || strings.TrimSpace(observation.TrafficID) == "" {
				return nil, "", errors.New("httpx route evidence is not Traffic-backed")
			}
			recordEndpoint, err := canonicalArjunEndpoint(record.Endpoint)
			if err != nil || recordEndpoint != target {
				return nil, "", errors.New("httpx route evidence does not match the selected endpoint")
			}
			recordHost, err := canonicalDiscoveryHost(record.Host)
			if err != nil || recordHost != host || record.Port != port {
				return nil, "", errors.New("httpx route evidence host or effective port does not match")
			}
			trafficID = strings.TrimSpace(observation.TrafficID)
		default:
			return nil, "", errors.New("route evidence contains an unexpected discovery stage")
		}
	}
	if matchingEndpoints != 1 {
		return nil, "", errors.New("route evidence must contain exactly one selected httpx endpoint")
	}
	addresses := make([]string, 0, len(openAddresses))
	for address := range openAddresses {
		if _, ok := dnsAddresses[address]; ok {
			addresses = append(addresses, address)
		}
	}
	sort.Strings(addresses)
	if len(addresses) == 0 {
		return nil, "", errors.New("route evidence has no matching DNS and naabu address")
	}
	if len(addresses) > arjunMaximumAddresses {
		return nil, "", errors.New("route evidence exceeds the bounded address set")
	}
	return []traffic.ScopedTCPRoute{{Host: host, Port: port, Addresses: addresses}}, trafficID, nil
}

func canonicalArjunRouteBinding(rawHost, rawAddress string) (string, string, error) {
	host, err := canonicalDiscoveryHost(rawHost)
	if err != nil {
		return "", "", err
	}
	address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(rawAddress), "[]"))
	if err != nil || address.Zone() != "" {
		return "", "", errors.New("route address is invalid")
	}
	address = address.Unmap()
	if address.IsUnspecified() || address.IsMulticast() {
		return "", "", errors.New("route address is non-routable")
	}
	return host, address.String(), nil
}

func (m *Manager) executeArjun(parent context.Context, job *db.ArsenalJob, prepared preparedRun) {
	finish := db.ArsenalJobFinish{Status: "failed"}
	stats := map[string]any{
		"profile_revision":      arjunProfileRevision,
		"endpoint_id":           prepared.arjunEndpointID,
		"exact_endpoint_sha256": prepared.exactEndpointHash,
		"route_sha256":          prepared.routeHash,
		"route_source":          prepared.arjunRouteSource,
	}
	if prepared.arjunEvidenceJob > 0 {
		stats["route_evidence_job_id"] = prepared.arjunEvidenceJob
		stats["route_evidence_traffic_id"] = prepared.arjunEvidenceFlow
	}
	finish.Stats = stats
	secrets := make([]string, 0, 2)
	defer func() {
		finish.Error = redactRuntimeSecrets(finish.Error, secrets...)
		if err := m.pg.FinishArsenalJob(context.Background(), job.ID, finish); err != nil {
			log.Printf("[arsenal] Arjun job %d finalization failed: %v", job.ID, err)
		}
	}()
	runCtx, cancel := context.WithTimeout(parent, time.Duration(arjunBudgetSeconds+15)*time.Second)
	defer cancel()
	if err := m.pg.MarkArsenalJobRunning(context.Background(), job.ID); err != nil {
		finish.Error = err.Error()
		return
	}
	caPEM, err := os.ReadFile(m.traffic.CACertPath())
	if err != nil {
		finish.Error = "read recording proxy CA: " + err.Error()
		return
	}
	socksSecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create Arjun TCP lease: " + err.Error()
		return
	}
	secrets = append(secrets, socksSecret)
	socks, err := m.traffic.StartScopedSOCKS5(runCtx, traffic.ScopedSOCKS5Policy{
		RunID: strconv.FormatInt(job.ID, 10) + "-arjun", Routes: prepared.arjunRoutes,
		ListenHost: m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost,
		Username: "uroboros", Password: socksSecret,
	})
	if err != nil {
		finish.Error = "start Arjun frozen TCP lease: " + err.Error()
		return
	}
	defer socks.Close() //nolint:errcheck
	if socks.RoutesFingerprint() != prepared.routeHash {
		finish.Error = "Arjun frozen route fingerprint changed before execution"
		return
	}
	proxySecret, err := randomProxyToken()
	if err != nil {
		finish.Error = "create Arjun evidence proxy lease: " + err.Error()
		return
	}
	secrets = append(secrets, proxySecret)
	proxy, err := m.traffic.StartScopedProxy(runCtx, traffic.ScopedProxyPolicy{
		RunID: strconv.FormatInt(job.ID, 10) + "-arjun", Scope: prepared.scope,
		ExactEndpoints: []string{prepared.target}, AllowedMethods: []string{"GET"},
		MaxRequests: arjunMaxRequests, ListenHost: m.cfg.ProxyListenHost,
		AdvertiseHost: m.cfg.ProxyAdvertiseHost, ProxyToken: proxySecret,
		UpstreamProxyURL: socks.ProxyURL(),
	})
	if err != nil {
		finish.Error = "start Arjun exact endpoint proxy: " + err.Error()
		return
	}
	defer proxy.Close() //nolint:errcheck
	if proxy.ExactEndpointFingerprint() != prepared.exactEndpointHash {
		finish.Error = "Arjun exact endpoint fingerprint changed before execution"
		return
	}
	client, err := m.connect(runCtx)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	args := map[string]any{
		"task_id": job.TaskID, "run_id": strconv.FormatInt(job.ID, 10),
		"endpoint_id": prepared.arjunEndpointID, "target": prepared.target,
		"scope_json": string(prepared.scopeJSON), "scope_hash": prepared.scopeHash,
		"config_json": string(prepared.configJSON), "config_hash": prepared.configHash,
		"proxy_url": proxy.ProxyURL(), "ca_pem": string(caPEM),
	}
	raw, callErr := client.Call(runCtx, "arsenal_task_arjun", args)
	closeClientErr := client.Close()
	closeProxyErr := proxy.Close()
	closeSOCKSErr := socks.Close()
	proxyStats := proxy.Stats()
	socksStats := socks.Stats()
	stats["proxy"] = proxyStats
	stats["tcp"] = socksStats
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
		finish.Error = "call Arsenal Arjun backend: " + callErr.Error()
		return
	}
	if closeClientErr != nil || closeProxyErr != nil || closeSOCKSErr != nil {
		finish.Error = "close Arjun owned runtime resources"
		return
	}
	if err := proxy.Err(); err != nil {
		finish.Error = "Arjun exact endpoint proxy stopped unexpectedly: " + err.Error()
		return
	}
	var result scanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		finish.Error = "decode Arsenal Arjun result: " + err.Error()
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
	stats["backend"] = redactValue(backendStats, proxySecret)
	published, err := m.persistArjunObservations(job, prepared, result.Records, proxy.CapturedExchanges())
	stats["published_candidates"] = published
	if err != nil {
		finish.Error = err.Error()
		return
	}
	if proxyStats.Requests == 0 || proxyStats.Allowed == 0 || proxyStats.Captured == 0 ||
		proxyStats.Allowed != proxyStats.Captured || socksStats.Connected == 0 {
		finish.Error = "Arjun did not complete every admitted request through Traffic and its frozen TCP route"
		return
	}
	if proxyStats.ScopeBlocked != 0 || proxyStats.MethodBlocked != 0 || proxyStats.BudgetBlocked != 0 ||
		proxyStats.AuthBlocked != 0 || socksStats.ScopeBlocked != 0 || socksStats.AuthBlocked != 0 || socksStats.DialFailed != 0 {
		finish.Error = "Arjun traffic crossed or exhausted its exact network boundary"
		return
	}
	if !result.Complete || (result.ExitState != "ok" && result.ExitState != "ok_empty") {
		finish.Error = fmt.Sprintf("Arjun ended with %s and is not a completed negative", result.ExitState)
		return
	}
	finish.Status = "completed"
	finish.Complete = true
	finish.Error = ""
}

func (m *Manager) persistArjunObservations(job *db.ArsenalJob, prepared preparedRun, records []scannerRecord, exchanges []traffic.ScopedExchange) (int, error) {
	seen := make(map[string]struct{}, len(records))
	published := 0
	for _, raw := range records {
		parameter, err := validateArjunRecord(raw, prepared.arjunEndpointID)
		if err != nil {
			return published, err
		}
		if _, duplicate := seen[parameter]; duplicate {
			return published, errors.New("Arjun returned a duplicate parameter candidate")
		}
		seen[parameter] = struct{}{}
		matches := make([]traffic.ScopedExchange, 0, 1)
		for _, exchange := range exchanges {
			if arjunVerificationExchange(exchange, prepared.target, parameter) {
				matches = append(matches, exchange)
			}
		}
		if len(matches) != 1 || strings.TrimSpace(matches[0].TrafficID) == "" {
			return published, fmt.Errorf("Arjun parameter %q has %d unique final verification exchanges", parameter, len(matches))
		}
		record := map[string]any{
			"endpoint_id": prepared.arjunEndpointID, "method": "GET", "location": "query",
			"parameter": parameter, "tool_version": job.ToolVersion,
			"wordlist_sha256": job.WordlistSHA256,
		}
		key, err := canonicalJSON(map[string]any{
			"task_id": job.TaskID, "endpoint_id": prepared.arjunEndpointID,
			"location": "query", "parameter": parameter, "tool": "arjun",
			"config_hash": job.ConfigHash, "tool_version": job.ToolVersion,
			"wordlist_sha256": job.WordlistSHA256,
		})
		if err != nil {
			return published, err
		}
		assetID := prepared.arjunEndpointID
		if _, err := m.pg.UpsertArsenalObservation(context.Background(), db.ArsenalObservationUpsert{
			JobID: job.ID, TaskID: job.TaskID, Tool: "arjun", ObservationKey: sha256Hex(key),
			Authority: "candidate", Record: record, ProxyVerified: true,
			TrafficID: matches[0].TrafficID, AssetID: &assetID,
		}); err != nil {
			return published, fmt.Errorf("store Arjun parameter candidate: %w", err)
		}
		published++
	}
	return published, nil
}

func validateArjunRecord(record scannerRecord, endpointID int64) (string, error) {
	if record.Tool != "arjun" || record.Type != "parameter" || record.EndpointID != endpointID ||
		record.Method != "GET" || record.Location != "query" || record.Name != record.Parameter ||
		record.Target != "" || record.Endpoint != "" || record.Evidence != "" || record.Raw != "" ||
		record.TemplateID != "" || record.ObservationID != "" || record.DedupFP != "" {
		return "", errors.New("Arjun returned a record outside the parameter-only contract")
	}
	return sanitizeArjunParameter(record.Parameter)
}

func sanitizeArjunParameter(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || !norm.NFC.IsNormalString(raw) || len([]byte(raw)) > arjunMaximumParameterByte {
		return "", errors.New("Arjun parameter is empty or too long")
	}
	depth := 0
	baseSeen := false
	suffixStarted := false
	for _, char := range raw {
		switch char {
		case '[':
			if !baseSeen || depth != 0 {
				return "", errors.New("Arjun parameter brackets are invalid")
			}
			depth = 1
			suffixStarted = true
		case ']':
			if depth != 1 {
				return "", errors.New("Arjun parameter brackets are invalid")
			}
			depth = 0
		default:
			if suffixStarted && depth == 0 {
				return "", errors.New("Arjun parameter brackets are invalid")
			}
			if !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-' || char == '.') {
				return "", errors.New("Arjun parameter contains a forbidden character")
			}
			baseSeen = true
		}
	}
	if depth != 0 || !baseSeen {
		return "", errors.New("Arjun parameter brackets are invalid")
	}
	return raw, nil
}

func arjunVerificationExchange(exchange traffic.ScopedExchange, target, parameter string) bool {
	if strings.ToUpper(strings.TrimSpace(exchange.Method)) != "GET" || strings.TrimSpace(exchange.TrafficID) == "" {
		return false
	}
	parsed, err := url.Parse(exchange.URL)
	if err != nil {
		return false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query[parameter]) != 1 {
		return false
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	base, err := canonicalArjunEndpoint(parsed.String())
	return err == nil && base == target
}
