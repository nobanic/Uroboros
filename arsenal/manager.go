package arsenal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/mcphttp"
	"github.com/nobanic/UroborOS/traffic"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const (
	backendName             = "arsenal"
	contractVersion         = 3
	defaultRequestTimeout   = 2*time.Hour + 5*time.Minute
	metadataRequestTimeout  = 30 * time.Second
	maxStoredEvidenceRunes  = 4000
	maxBackendRecords       = 2000
	expectedTemplateVersion = "10.5.0"
	expectedTemplateSHA256  = "dd836a827b4147d7d8294ca6a89526288ccb1a4bcc895dceac7af3563bc61775"
)

var (
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	extensionsPattern = regexp.MustCompile(`^\.[A-Za-z0-9]{1,16}(,\.[A-Za-z0-9]{1,16})*$`)
	expectedTools     = map[string]struct {
		Version  string
		Artifact string
	}{
		"nuclei":    {Version: "3.3.0", Artifact: "235f264d32e47e1ccf58d534e2eb4d0d4eeb47f1cae1ebb30a584b8b52565202"},
		"httpx":     {Version: "1.6.9", Artifact: "c8d36461b5d736e88c3f9104fed15f2112eb7263dbda35fd08aa5a771bddfb5f"},
		"ffuf":      {Version: "2.1.0", Artifact: "fc2c82736c14dcbea4daf3d3cf3878c1c4773008ba45c2bc0fceba7d17b40bb5"},
		"subfinder": {Version: "2.17.0", Artifact: "a39c3e5c7a26ef3c430915c4d6cbf7716a00f26769a75e9405aba796142eba37"},
		"naabu":     {Version: "2.6.1", Artifact: "018c4c9884dea971eda860435ede3021d1150732f34cfd245498c6726d8cab90"},
		"arjun":     {Version: "2.2.7", Artifact: "b193cdaf97bf7b0e8cd91a41da778639e01fd9738d5f666a8161377f475ce72e"},
		"wafw00f":   {Version: wafVersion, Artifact: wafSDISTSHA256},
	}
	jobTools = map[string]bool{"nuclei": true, "httpx": true, "ffuf": true, "discovery": true, "archive": true, "arjun": true, "wafw00f": true}
)

var ErrJobNotActive = errors.New("requested Arsenal job is not active in this task")

// Caller is the backend-only subset of the HTTP MCP client used by Arsenal.
// It is exported so tests can inject a deterministic sidecar without exposing
// raw scanner tools to an agent.
type Caller interface {
	Call(context.Context, string, any) (string, error)
	Close() error
}

type ManagerConfig struct {
	ServerName         string
	ProxyListenHost    string
	ProxyAdvertiseHost string
	RequestTimeout     time.Duration
	ClientFactory      func(context.Context) (Caller, error)
	// Resolve is a deterministic test seam. Production uses the host's system
	// resolver and freezes its answers before opening any TCP lease.
	Resolve func(context.Context, string) ([]net.IP, error)
}

type StartRequest struct {
	Tool        string
	Target      string
	Config      map[string]any
	MaxRequests int64
	EndpointID  int64
}

// ArchiveGrantSpec is the canonical, network-free admission projection used by
// the authenticated operator API before it persists a disclosure grant.
type ArchiveGrantSpec struct {
	Domain      string `json:"domain"`
	CaptureFrom string `json:"capture_from"`
	CaptureTo   string `json:"capture_to"`
}

type activeRun struct {
	jobID  int64
	cancel context.CancelCauseFunc
	done   chan struct{}
}

type preparedRun struct {
	request           StartRequest
	target            string
	storedTarget      string
	config            map[string]any
	configJSON        []byte
	configHash        string
	scope             traffic.NetworkScopeSnapshot
	scopeJSON         []byte
	scopeHash         string
	metadata          backendMetadata
	archiveGrant      *db.ArchiveProviderGrant
	arjunEndpointID   int64
	arjunRoutes       []traffic.ScopedTCPRoute
	arjunRouteSource  string
	arjunEvidenceJob  int64
	arjunEvidenceFlow string
	exactEndpointHash string
	routeHash         string
	wafEndpointID     int64
	wafTrafficID      string
	wafHeaderHash     string
	wafHeadersJSON    []byte
}

type backendToolMetadata struct {
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	BinarySHA256   string `json:"binary_sha256"`
}

type backendTemplateMetadata struct {
	Version       string `json:"version"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

type backendMetadata struct {
	ContractVersion      int                            `json:"contract_version"`
	SourceRevision       string                         `json:"source_revision"`
	Tools                map[string]backendToolMetadata `json:"tools"`
	Templates            backendTemplateMetadata        `json:"nuclei_templates"`
	WordlistSHA256       string                         `json:"wordlist_sha256"`
	ArjunWordlistSHA256  string                         `json:"arjun_wordlist_sha256"`
	ArjunSpecialSHA256   string                         `json:"arjun_special_sha256"`
	DependencyLockSHA256 string                         `json:"dependency_lock_sha256"`
	Authority            string                         `json:"authority"`
}

type scannerRecord struct {
	Tool          string   `json:"tool"`
	Type          string   `json:"type"`
	Severity      string   `json:"severity"`
	Name          string   `json:"name"`
	Target        string   `json:"target"`
	Endpoint      string   `json:"endpoint"`
	Evidence      string   `json:"evidence"`
	TemplateID    string   `json:"template_id"`
	ObservationID string   `json:"observation_id"`
	DedupFP       string   `json:"dedup_fp"`
	Raw           string   `json:"raw"`
	Stage         string   `json:"stage"`
	Host          string   `json:"host"`
	Address       string   `json:"address"`
	Port          int      `json:"port"`
	EndpointID    int64    `json:"endpoint_id"`
	Method        string   `json:"method"`
	Location      string   `json:"location"`
	Parameter     string   `json:"parameter"`
	PluginID      string   `json:"plugin_id"`
	Vendor        string   `json:"vendor"`
	Manufacturer  string   `json:"manufacturer"`
	EvidenceKinds []string `json:"evidence_kinds"`
}

type scanResult struct {
	Tool       string          `json:"tool"`
	ExitState  string          `json:"exit_state"`
	Complete   bool            `json:"complete"`
	Records    []scannerRecord `json:"records"`
	Stats      json.RawMessage `json:"stats"`
	TaskID     int64           `json:"task_id"`
	RunID      string          `json:"run_id"`
	ScopeHash  string          `json:"scope_hash"`
	ConfigHash string          `json:"config_hash"`
	Authority  string          `json:"authority"`
	Stage      string          `json:"stage"`
}

// Manager owns task-bound sidecar calls. PostgreSQL is authoritative for job
// history; the in-memory map exists only for cancellation and joining.
type Manager struct {
	ctx        context.Context
	pg         *db.DB
	assets     *db.AssetStore
	traffic    *traffic.Traffic
	cfg        ManagerConfig
	startMu    sync.Mutex
	mu         sync.Mutex
	active     map[int64]*activeRun
	closed     bool
	onComplete func(taskID, jobID int64)

	archiveGate       chan struct{}
	archiveScheduleMu sync.Mutex
	archiveLastStart  time.Time
	archiveNow        func() time.Time
	archiveSleep      func(context.Context, time.Duration) error
	archiveFetch      func(context.Context, archiveFetchRequest, archiveFetchAdmission) (archiveFetchResponse, error)
	archiveDial       func(context.Context, string, string) (net.Conn, error)
}

func NewManager(ctx context.Context, pg *db.DB, assets *db.AssetStore, recorder *traffic.Traffic, cfg ManagerConfig) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.ServerName) == "" {
		cfg.ServerName = backendName
	}
	if strings.TrimSpace(cfg.ProxyListenHost) == "" {
		cfg.ProxyListenHost = strings.TrimSpace(os.Getenv("UROBOROS_ARSENAL_PROXY_LISTEN_HOST"))
	}
	if strings.TrimSpace(cfg.ProxyAdvertiseHost) == "" {
		cfg.ProxyAdvertiseHost = strings.TrimSpace(os.Getenv("UROBOROS_ARSENAL_PROXY_ADVERTISE_HOST"))
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	m := &Manager{
		ctx: ctx, pg: pg, assets: assets, traffic: recorder, cfg: cfg,
		active: make(map[int64]*activeRun), archiveGate: make(chan struct{}, 1),
		archiveNow: time.Now, archiveSleep: sleepContext,
	}
	m.archiveFetch = m.fetchArchiveProvider
	if pg != nil {
		if count, err := pg.ReconcileArsenalJobs(context.Background()); err != nil {
			log.Printf("[arsenal] reconcile interrupted jobs failed: %v", err)
		} else if count > 0 {
			log.Printf("[arsenal] marked %d stale job(s) interrupted", count)
		}
		if count, err := pg.ReconcileArchiveProviderAttempts(context.Background()); err != nil {
			log.Printf("[arsenal] reconcile interrupted archive provider attempts failed: %v", err)
		} else if count > 0 {
			log.Printf("[arsenal] marked %d stale archive provider attempt(s) interrupted", count)
		}
	}
	return m
}

// SetCompletionHook installs the application lifecycle wake used after an
// Arsenal job and all of its asset/evidence writes are durably finalized. The
// hook runs outside Manager locks.
func (m *Manager) SetCompletionHook(hook func(taskID, jobID int64)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onComplete = hook
	m.mu.Unlock()
}

// ValidateArchiveGrant canonicalizes an operator's proposed disclosure scope
// against the task's current frozen root-domain authority. It performs database
// reads only and never resolves or contacts the provider or target.
func (m *Manager) ValidateArchiveGrant(taskID int64, domain, captureFrom, captureTo string) (ArchiveGrantSpec, error) {
	if m == nil || m.assets == nil {
		return ArchiveGrantSpec{}, errors.New("Arsenal manager has no PostgreSQL asset store")
	}
	if taskID <= 0 {
		return ArchiveGrantSpec{}, errors.New("task id must be positive")
	}
	config, err := normalizeArchiveConfig(map[string]any{"from": captureFrom, "to": captureTo})
	if err != nil {
		return ArchiveGrantSpec{}, err
	}
	snapshot, err := m.assets.TaskNetworkScope(taskID)
	if err != nil {
		return ArchiveGrantSpec{}, fmt.Errorf("build task network scope: %w", err)
	}
	scope, _, err := traffic.NormalizeNetworkScope(traffic.NetworkScopeSnapshot{
		RootDomains: snapshot.RootDomains, ExactHosts: snapshot.ExactHosts, CIDRs: snapshot.CIDRs,
	})
	if err != nil {
		return ArchiveGrantSpec{}, fmt.Errorf("task has no executable network scope: %w", err)
	}
	canonicalDomain, err := canonicalDiscoveryDomain(domain, scope)
	if err != nil {
		return ArchiveGrantSpec{}, err
	}
	from, _ := config["from"].(string)
	to, _ := config["to"].(string)
	return ArchiveGrantSpec{Domain: canonicalDomain, CaptureFrom: from, CaptureTo: to}, nil
}

func (m *Manager) Start(taskID int64, source string, request StartRequest) (*db.ArsenalJob, error) {
	if m == nil {
		return nil, errors.New("Arsenal manager is unavailable")
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.pg == nil || m.assets == nil {
		return nil, errors.New("Arsenal manager has no PostgreSQL asset store")
	}
	requestedTool := strings.ToLower(strings.TrimSpace(request.Tool))
	if requestedTool != "archive" && m.traffic == nil {
		return nil, errors.New("Arsenal requires the Uroboros traffic recorder")
	}
	if err := m.ctx.Err(); err != nil {
		return nil, fmt.Errorf("Arsenal manager is stopping: %w", err)
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, errors.New("Arsenal manager is closed")
	}
	if taskID <= 0 {
		return nil, errors.New("task id must be positive")
	}
	if source == "" {
		source = "tool"
	}
	if source != "tool" && source != "system" {
		return nil, errors.New("Arsenal source must be tool or system")
	}
	prepared, err := m.prepare(taskID, request)
	if err != nil {
		return nil, err
	}
	contractVersion := prepared.metadata.ContractVersion
	sourceRevision := prepared.metadata.SourceRevision
	toolMeta := backendToolMetadata{}
	if prepared.request.Tool == "archive" {
		contractVersion = 1
		sourceRevision = archiveContractSHA256()
		toolMeta = backendToolMetadata{Version: ArchiveContractVersion, BinarySHA256: archiveContractSHA256()}
	} else {
		toolMeta, err = jobProvenance(prepared.request.Tool, prepared.metadata)
		if err != nil {
			return nil, err
		}
	}
	templateVersion := prepared.metadata.Templates.Version
	templateSHA256 := prepared.metadata.Templates.ArchiveSHA256
	wordlistSHA256 := prepared.metadata.WordlistSHA256
	if prepared.request.Tool == "arjun" {
		templateVersion = "arjun-special-v1"
		templateSHA256 = prepared.metadata.ArjunSpecialSHA256
		wordlistSHA256 = prepared.metadata.ArjunWordlistSHA256
	} else if prepared.request.Tool == "wafw00f" {
		templateVersion = wafProfileRevision
		templateSHA256 = wafCorpusSHA256
		wordlistSHA256 = wafSDISTSHA256
	}
	if prepared.request.Tool == "wafw00f" {
		reusable, err := m.pg.ReusableWAFJob(context.Background(), taskID, prepared.configHash)
		if err != nil {
			return nil, err
		}
		if reusable != nil {
			return reusable, nil
		}
	}
	job, err := m.pg.CreateArsenalJob(context.Background(), db.ArsenalJobCreate{
		TaskID: taskID, Source: source, Tool: prepared.request.Tool, Target: prepared.storedTarget,
		Config: json.RawMessage(prepared.configJSON), ConfigHash: prepared.configHash,
		Scope: json.RawMessage(prepared.scopeJSON), ScopeHash: prepared.scopeHash,
		ContractVersion: contractVersion, SourceRevision: sourceRevision,
		ToolVersion: toolMeta.Version, ToolSHA256: toolMeta.BinarySHA256,
		TemplateVersion: templateVersion,
		TemplateSHA256:  templateSHA256,
		WordlistSHA256:  wordlistSHA256,
	})
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(m.ctx)
	run := &activeRun{jobID: job.ID, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if m.active[taskID] != nil {
		m.mu.Unlock()
		cancel(errors.New("duplicate in-memory Arsenal run"))
		_ = m.pg.FinishArsenalJob(context.Background(), job.ID, db.ArsenalJobFinish{Status: "canceled", Error: "duplicate active run"})
		return nil, db.ErrArsenalJobActive
	}
	m.active[taskID] = run
	m.mu.Unlock()
	go func() {
		m.execute(runCtx, job, prepared)
		m.mu.Lock()
		if m.active[taskID] == run {
			delete(m.active, taskID)
		}
		hook := m.onComplete
		m.mu.Unlock()
		if hook != nil {
			hook(taskID, job.ID)
		}
		close(run.done)
	}()
	return job, nil
}

func (m *Manager) prepare(taskID int64, request StartRequest) (preparedRun, error) {
	request.Tool = strings.ToLower(strings.TrimSpace(request.Tool))
	if !jobTools[request.Tool] {
		return preparedRun{}, errors.New("tool must be nuclei, httpx, ffuf, discovery, archive, arjun or wafw00f")
	}
	if request.Tool == "arjun" {
		return m.prepareArjun(taskID, request)
	}
	if request.Tool == "wafw00f" {
		return m.prepareWAF(taskID, request)
	}
	if request.EndpointID != 0 {
		return preparedRun{}, errors.New("endpoint_id is accepted only by Arjun or passive WAF fingerprinting")
	}
	if request.MaxRequests < 0 {
		return preparedRun{}, errors.New("max_requests cannot be negative; 0 means unlimited")
	}
	if request.Tool == "archive" && request.MaxRequests != 0 {
		return preparedRun{}, errors.New("archive discovery does not accept a target-request budget")
	}
	config, err := normalizeConfig(request.Tool, request.Config)
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
	var target string
	if request.Tool == "discovery" || request.Tool == "archive" {
		target, err = canonicalDiscoveryDomain(request.Target, scope)
	} else {
		target, err = canonicalTarget(request.Target)
		if err == nil && !scope.AllowsURL(target) {
			err = errors.New("target is outside the frozen task scope")
		}
	}
	if err != nil {
		return preparedRun{}, err
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
	var archiveGrant *db.ArchiveProviderGrant
	if request.Tool == "archive" {
		captureFrom, _ := config["from"].(string)
		captureTo, _ := config["to"].(string)
		archiveGrant, err = m.pg.MatchingArchiveProviderGrant(
			context.Background(), taskID, target, captureFrom, captureTo, m.archiveNow().UTC(),
		)
		if err != nil {
			if errors.Is(err, db.ErrArchiveGrantNotFound) {
				return preparedRun{}, errors.New("archive provider disclosure is disabled or no matching active operator grant exists")
			}
			return preparedRun{}, err
		}
		if archiveGrant.ContractVersion != ArchiveContractVersion || archiveGrant.NoticeVersion != ArchiveNoticeVersion {
			return preparedRun{}, errors.New("matching archive provider grant uses an unsupported contract or notice revision")
		}
		config["grant_id"] = archiveGrant.ID
		config["provider"] = db.ArchiveProviderInternetArchive
		config["contract_version"] = ArchiveContractVersion
	}
	configJSON, err := canonicalJSON(config)
	if err != nil {
		return preparedRun{}, err
	}
	configEnvelope, err := canonicalJSON(map[string]any{"config": config, "target": target, "tool": request.Tool})
	if err != nil {
		return preparedRun{}, err
	}
	var metadata backendMetadata
	if request.Tool != "archive" {
		metadata, err = m.loadMetadata()
		if err != nil {
			return preparedRun{}, err
		}
	}
	request.Config = config
	return preparedRun{
		request: request, target: target, storedTarget: storedArsenalTarget(request.Tool, target), config: config,
		configJSON: configJSON, configHash: sha256Hex(configEnvelope), scope: scope,
		scopeJSON: scopeJSON, scopeHash: sha256Hex(scopeJSON), metadata: metadata,
		archiveGrant: archiveGrant,
	}, nil
}

func (m *Manager) loadMetadata() (backendMetadata, error) {
	ctx, cancel := context.WithTimeout(m.ctx, metadataRequestTimeout)
	defer cancel()
	client, err := m.connect(ctx)
	if err != nil {
		return backendMetadata{}, err
	}
	defer client.Close() //nolint:errcheck
	raw, err := client.Call(ctx, "arsenal_metadata", map[string]any{})
	if err != nil {
		return backendMetadata{}, fmt.Errorf("read Arsenal provenance: %w", err)
	}
	var metadata backendMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return backendMetadata{}, fmt.Errorf("decode Arsenal provenance: %w", err)
	}
	if err := validateMetadata(metadata); err != nil {
		return backendMetadata{}, err
	}
	return metadata, nil
}

func validateMetadata(metadata backendMetadata) error {
	if metadata.ContractVersion != contractVersion || metadata.Authority != "candidate_or_hypothesis_only" {
		return errors.New("Arsenal backend contract or authority is not approved")
	}
	if !commitPattern.MatchString(strings.ToLower(metadata.SourceRevision)) {
		return errors.New("Arsenal backend source revision is missing or not a full commit")
	}
	for name, expected := range expectedTools {
		actual, ok := metadata.Tools[name]
		if !ok || actual.Version != expected.Version || strings.ToLower(actual.ArtifactSHA256) != expected.Artifact ||
			!sha256Pattern.MatchString(strings.ToLower(actual.BinarySHA256)) {
			return fmt.Errorf("Arsenal %s provenance does not match the pinned release", name)
		}
	}
	if strings.ToLower(metadata.Tools["wafw00f"].BinarySHA256) != wafCorpusSHA256 {
		return errors.New("Arsenal WAFW00F corpus provenance does not match the pinned release")
	}
	if metadata.Templates.Version != expectedTemplateVersion || strings.ToLower(metadata.Templates.ArchiveSHA256) != expectedTemplateSHA256 {
		return errors.New("Arsenal Nuclei template provenance does not match the pinned corpus")
	}
	if !sha256Pattern.MatchString(strings.ToLower(metadata.WordlistSHA256)) {
		return errors.New("Arsenal wordlist hash is unavailable")
	}
	if !sha256Pattern.MatchString(strings.ToLower(metadata.ArjunWordlistSHA256)) ||
		!sha256Pattern.MatchString(strings.ToLower(metadata.ArjunSpecialSHA256)) ||
		!sha256Pattern.MatchString(strings.ToLower(metadata.DependencyLockSHA256)) {
		return errors.New("Arsenal Arjun data or dependency provenance is unavailable")
	}
	return nil
}

func (m *Manager) connect(ctx context.Context) (Caller, error) {
	if m.cfg.ClientFactory != nil {
		return m.cfg.ClientFactory(ctx)
	}
	servers, err := m.pg.ListMCP()
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		if server.Name != m.cfg.ServerName {
			continue
		}
		if !server.Enabled {
			return nil, errors.New("Arsenal backend MCP is disabled")
		}
		if server.Transport != "http" || strings.TrimSpace(server.URL) == "" {
			return nil, errors.New("Arsenal backend must be a configured HTTP MCP")
		}
		headers, err := stringMap(server.Env)
		if err != nil {
			return nil, fmt.Errorf("decode Arsenal MCP headers: %w", err)
		}
		timeout := m.cfg.RequestTimeout
		if configured := time.Duration(server.RequestTimeoutSeconds) * time.Second; configured > timeout {
			timeout = configured
		}
		return mcphttp.NewWithTimeout(ctx, server.Name, server.URL, headers, server.Insecure, timeout)
	}
	return nil, errors.New("configured Arsenal backend MCP was not found")
}

func (m *Manager) execute(parent context.Context, job *db.ArsenalJob, prepared preparedRun) {
	if job.Tool == "archive" {
		m.executeArchive(parent, job, prepared)
		return
	}
	if job.Tool == "discovery" {
		m.executeDiscovery(parent, job, prepared)
		return
	}
	if job.Tool == "arjun" {
		m.executeArjun(parent, job, prepared)
		return
	}
	if job.Tool == "wafw00f" {
		m.executeWAF(parent, job, prepared)
		return
	}
	m.executeScan(parent, job, prepared)
}

func (m *Manager) executeScan(parent context.Context, job *db.ArsenalJob, prepared preparedRun) {
	finish := db.ArsenalJobFinish{Status: "failed"}
	var token string
	defer func() {
		finish.Error = redactRuntimeSecret(finish.Error, token)
		if err := m.pg.FinishArsenalJob(context.Background(), job.ID, finish); err != nil {
			log.Printf("[arsenal] job %d finalization failed: %v", job.ID, err)
		}
	}()
	var err error
	token, err = randomProxyToken()
	if err != nil {
		finish.Error = "create proxy lease: " + err.Error()
		return
	}
	proxy, err := m.traffic.StartScopedProxy(parent, traffic.ScopedProxyPolicy{
		RunID: strconv.FormatInt(job.ID, 10), Scope: prepared.scope, MaxRequests: prepared.request.MaxRequests,
		ListenHost: m.cfg.ProxyListenHost, AdvertiseHost: m.cfg.ProxyAdvertiseHost, ProxyToken: token,
	})
	if err != nil {
		finish.Error = "start Arsenal scoped proxy: " + err.Error()
		return
	}
	proxyURL := proxy.ProxyURL()
	defer proxy.Close() //nolint:errcheck
	caPEM, err := os.ReadFile(m.traffic.CACertPath())
	if err != nil {
		finish.Error = "read recording proxy CA: " + err.Error()
		return
	}
	if err := m.pg.MarkArsenalJobRunning(context.Background(), job.ID); err != nil {
		finish.Error = err.Error()
		return
	}
	client, err := m.connect(parent)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	args := map[string]any{
		"task_id": job.TaskID, "run_id": strconv.FormatInt(job.ID, 10), "tool": prepared.request.Tool,
		"target": prepared.target, "scope_json": string(prepared.scopeJSON), "scope_hash": prepared.scopeHash,
		"config_json": string(prepared.configJSON), "config_hash": prepared.configHash,
		"proxy_url": proxyURL, "ca_pem": string(caPEM),
	}
	raw, callErr := client.Call(parent, "arsenal_task_scan", args)
	closeClientErr := client.Close()
	closeProxyErr := proxy.Close()
	proxyStats := proxy.Stats()
	stats := map[string]any{"proxy": proxyStats}
	finish.Stats = stats
	if cause := context.Cause(parent); cause != nil {
		finish.Status = "canceled"
		finish.Error = redactRuntimeSecret(cause.Error(), token)
		return
	}
	if callErr != nil {
		finish.Error = redactRuntimeSecret("call Arsenal backend: "+callErr.Error(), token)
		return
	}
	if closeClientErr != nil {
		finish.Error = "close Arsenal backend session: " + closeClientErr.Error()
		return
	}
	if closeProxyErr != nil {
		finish.Error = "close Arsenal scoped proxy: " + closeProxyErr.Error()
		return
	}
	if err := proxy.Err(); err != nil {
		finish.Error = "Arsenal scoped proxy stopped unexpectedly: " + err.Error()
		return
	}
	var result scanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		finish.Error = "decode Arsenal scan result: " + err.Error()
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
	stats["backend"] = redactValue(backendStats, token)
	if err := m.persistObservations(job, result.Records, proxy.CapturedExchanges(), token); err != nil {
		finish.Error = err.Error()
		return
	}
	if proxyStats.Requests == 0 || proxyStats.Allowed == 0 || proxyStats.Captured == 0 {
		finish.Error = "scanner did not complete a scope-approved HTTP exchange through the task-scoped proxy"
		return
	}
	if !result.Complete || (result.ExitState != "ok" && result.ExitState != "ok_empty") {
		finish.Error = fmt.Sprintf("Arsenal scan ended with %s and is not a completed negative", result.ExitState)
		return
	}
	finish.Status = "completed"
	finish.Complete = true
	finish.Error = ""
}

func validateResult(job *db.ArsenalJob, prepared preparedRun, result scanResult) (map[string]any, backendMetadata, error) {
	if result.TaskID != job.TaskID || result.RunID != strconv.FormatInt(job.ID, 10) || result.Tool != job.Tool ||
		result.ScopeHash != prepared.scopeHash || result.ConfigHash != prepared.configHash ||
		result.Authority != "candidate_or_hypothesis_only" {
		return nil, backendMetadata{}, errors.New("Arsenal backend returned a mismatched task lease")
	}
	if len(result.Records) > maxBackendRecords {
		return nil, backendMetadata{}, fmt.Errorf("Arsenal backend returned %d records; maximum is %d", len(result.Records), maxBackendRecords)
	}
	var stats map[string]any
	if len(result.Stats) == 0 || json.Unmarshal(result.Stats, &stats) != nil || stats == nil {
		return nil, backendMetadata{}, errors.New("Arsenal backend returned invalid scan statistics")
	}
	rawProvenance, ok := stats["provenance"]
	if !ok {
		return nil, backendMetadata{}, errors.New("Arsenal backend omitted runtime provenance")
	}
	encoded, err := json.Marshal(rawProvenance)
	if err != nil {
		return nil, backendMetadata{}, err
	}
	var provenance backendMetadata
	if err := json.Unmarshal(encoded, &provenance); err != nil {
		return nil, backendMetadata{}, errors.New("Arsenal backend returned invalid runtime provenance")
	}
	delete(stats, "provenance")
	return stats, provenance, nil
}

func compareMetadata(before, after backendMetadata) error {
	if err := validateMetadata(after); err != nil {
		return err
	}
	left, err := canonicalJSON(before)
	if err != nil {
		return err
	}
	right, err := canonicalJSON(after)
	if err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return errors.New("Arsenal backend provenance changed during the scan")
	}
	return nil
}

func (m *Manager) persistObservations(job *db.ArsenalJob, records []scannerRecord, exchanges []traffic.ScopedExchange, token string) error {
	matched := make(map[string]traffic.ScopedExchange, len(exchanges))
	for _, exchange := range exchanges {
		if key, ok := canonicalEvidenceURL(exchange.URL); ok {
			matched[key] = exchange
		}
	}
	for _, raw := range records {
		matchURL := raw.Endpoint
		if strings.TrimSpace(matchURL) == "" {
			matchURL = raw.Target
		}
		matchKey, matchable := canonicalEvidenceURL(matchURL)
		if job.Tool == "discovery" && (raw.Stage != "httpx" || raw.Tool != "httpx") {
			matchable = false
		}
		record, err := sanitizeRecord(job.Tool, raw, token)
		if err != nil {
			return err
		}
		keyBytes, err := canonicalJSON(record)
		if err != nil {
			return err
		}
		observationKey := sha256Hex(keyBytes)
		authority := "candidate"
		if job.Tool == "nuclei" {
			authority = "hypothesis"
		}
		var exchange traffic.ScopedExchange
		var proxyVerified bool
		if matchable {
			exchange, proxyVerified = matched[matchKey]
		}
		var assetID *int64
		if proxyVerified && authority == "candidate" {
			id, err := m.importVerified(job, exchange)
			if err != nil {
				return err
			}
			assetID = &id
		}
		if _, err := m.pg.UpsertArsenalObservation(context.Background(), db.ArsenalObservationUpsert{
			JobID: job.ID, TaskID: job.TaskID, Tool: job.Tool, ObservationKey: observationKey,
			Authority: authority, Record: record, ProxyVerified: proxyVerified,
			TrafficID: exchange.TrafficID, AssetID: assetID,
		}); err != nil {
			return fmt.Errorf("store Arsenal observation: %w", err)
		}
	}
	return nil
}

func (m *Manager) importVerified(job *db.ArsenalJob, exchange traffic.ScopedExchange) (int64, error) {
	cleanURL, ok := canonicalEvidenceURL(exchange.URL)
	if !ok {
		return 0, errors.New("captured Arsenal exchange has an invalid URL")
	}
	origin, err := originURL(cleanURL)
	if err != nil {
		return 0, err
	}
	status := exchange.Status
	serviceID, err := m.assets.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: origin, StatusCode: &status, TaskID: job.TaskID})
	if err != nil {
		return 0, fmt.Errorf("import Arsenal HTTP service: %w", err)
	}
	if err := m.assets.SetTaskAssetSource(job.TaskID, serviceID, "arsenal", "verified by task-scoped "+job.Tool+" traffic", nil); err != nil {
		return 0, fmt.Errorf("record Arsenal service provenance: %w", err)
	}
	method := strings.ToUpper(strings.TrimSpace(exchange.Method))
	if method == "" {
		method = "GET"
	}
	endpointID, err := m.assets.UpsertEndpoint(db.UpsertEndpointReq{
		URL: cleanURL, Method: method, Params: endpointParams(cleanURL), TaskID: job.TaskID,
	})
	if err != nil {
		return 0, fmt.Errorf("import Arsenal endpoint: %w", err)
	}
	if err := m.assets.SetTaskAssetSource(job.TaskID, endpointID, "arsenal", "verified by task-scoped "+job.Tool+" traffic "+exchange.TrafficID, nil); err != nil {
		return 0, fmt.Errorf("record Arsenal endpoint provenance: %w", err)
	}
	return endpointID, nil
}

func (m *Manager) LatestJob(taskID int64) (*db.ArsenalJob, error) {
	if m == nil || m.pg == nil {
		return nil, errors.New("Arsenal job store is unavailable")
	}
	return m.pg.LatestArsenalJob(context.Background(), taskID)
}

func (m *Manager) Job(ctx context.Context, jobID int64) (*db.ArsenalJob, error) {
	if m == nil || m.pg == nil {
		return nil, errors.New("Arsenal job store is unavailable")
	}
	return m.pg.ArsenalJob(ctx, jobID)
}

func (m *Manager) Results(jobID int64, limit, offset int) ([]db.ArsenalObservation, error) {
	if m == nil || m.pg == nil {
		return nil, errors.New("Arsenal observation store is unavailable")
	}
	return m.pg.ListArsenalObservations(context.Background(), jobID, limit, offset)
}

func (m *Manager) CancelTask(ctx context.Context, taskID int64, reason error) error {
	return m.CancelJob(ctx, taskID, 0, reason)
}

func (m *Manager) CancelJob(ctx context.Context, taskID, jobID int64, reason error) error {
	if reason == nil {
		reason = errors.New("Arsenal scan canceled")
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	run := m.active[taskID]
	m.mu.Unlock()
	if run == nil {
		if jobID > 0 {
			return ErrJobNotActive
		}
		return nil
	}
	if jobID > 0 && run.jobID != jobID {
		return ErrJobNotActive
	}
	run.cancel(reason)
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	m.closed = true
	runs := make([]*activeRun, 0, len(m.active))
	for _, run := range m.active {
		runs = append(runs, run)
	}
	m.mu.Unlock()
	for _, run := range runs {
		run.cancel(errors.New("application is stopping"))
	}
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func canonicalTarget(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("target must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return "", errors.New("target credentials are not accepted; use task-owned authentication state")
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("target has an invalid port")
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	parsed.Host = host
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), nil
}

func canonicalDiscoveryHost(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if raw == "" || strings.ContainsAny(raw, "/\\?#@:*") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", errors.New("domain is malformed")
	}
	if net.ParseIP(strings.Trim(raw, "[]")) != nil {
		return "", errors.New("domain cannot be an IP literal")
	}
	ascii, err := idna.Lookup.ToASCII(raw)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", errors.New("domain is not a valid DNS name")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", errors.New("domain has an invalid DNS label")
		}
	}
	return strings.ToLower(ascii), nil
}

func canonicalDiscoveryDomain(raw string, scope traffic.NetworkScopeSnapshot) (string, error) {
	domain, err := canonicalDiscoveryHost(raw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(domain, ".") {
		return "", errors.New("discovery requires a registrable root domain")
	}
	root, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil || root != domain {
		return "", errors.New("discovery target must be the registrable domain, not a subdomain")
	}
	for _, allowed := range scope.RootDomains {
		if domain == allowed {
			return domain, nil
		}
	}
	return "", errors.New("discovery domain is not an authorized root domain in the frozen task scope")
}

func storedArsenalTarget(tool, target string) string {
	if tool == "discovery" || tool == "archive" {
		return target
	}
	return redactURL(target)
}

func jobProvenance(tool string, metadata backendMetadata) (backendToolMetadata, error) {
	if tool != "discovery" {
		value, ok := metadata.Tools[tool]
		if !ok {
			return backendToolMetadata{}, fmt.Errorf("Arsenal metadata omitted %s", tool)
		}
		return value, nil
	}
	versions := make([]string, 0, 3)
	fingerprint := make(map[string]any, 3)
	for _, name := range []string{"subfinder", "naabu", "httpx"} {
		value, ok := metadata.Tools[name]
		if !ok {
			return backendToolMetadata{}, fmt.Errorf("Arsenal metadata omitted discovery stage %s", name)
		}
		versions = append(versions, name+"/"+value.Version)
		fingerprint[name] = map[string]string{
			"version": value.Version, "artifact_sha256": value.ArtifactSHA256, "binary_sha256": value.BinarySHA256,
		}
	}
	raw, err := canonicalJSON(fingerprint)
	if err != nil {
		return backendToolMetadata{}, err
	}
	return backendToolMetadata{Version: strings.Join(versions, "+"), BinarySHA256: sha256Hex(raw)}, nil
}

func normalizeConfig(tool string, input map[string]any) (map[string]any, error) {
	if input == nil {
		input = map[string]any{}
	}
	if tool == "discovery" {
		return normalizeDiscoveryConfig(input)
	}
	if tool == "archive" {
		return normalizeArchiveConfig(input)
	}
	allowed := map[string]bool{"budget_seconds": true}
	switch tool {
	case "ffuf":
		allowed["extensions"] = true
	case "nuclei":
		for _, key := range []string{"profile", "template_ids", "severity", "tags"} {
			allowed[key] = true
		}
	}
	for key := range input {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown %s config field %q", tool, key)
		}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	if raw, ok := out["budget_seconds"]; ok {
		budget, ok := integerValue(raw)
		if !ok {
			return nil, errors.New("budget_seconds must be an integer")
		}
		min, max := int64(1), int64(30)
		if tool == "ffuf" {
			max = 120
		} else if tool == "nuclei" {
			min, max = 10, 7200
		}
		if budget < min || budget > max {
			return nil, fmt.Errorf("budget_seconds must be between %d and %d", min, max)
		}
		out["budget_seconds"] = budget
	}
	if tool == "ffuf" {
		if value, ok := out["extensions"]; ok {
			extensions, ok := value.(string)
			if !ok || (extensions != "" && !extensionsPattern.MatchString(extensions)) {
				return nil, errors.New("extensions must be a comma-separated list such as .php,.txt")
			}
		}
	}
	if tool == "nuclei" {
		if value, ok := out["profile"]; ok {
			profile, ok := value.(string)
			if !ok || (profile != "light" && profile != "full") {
				return nil, errors.New("profile must be light or full")
			}
		}
		if value, ok := out["severity"]; ok {
			severity, ok := value.(string)
			valid := map[string]bool{"": true, "info": true, "low": true, "medium": true, "high": true, "critical": true, "unknown": true}
			if !ok || !valid[strings.ToLower(severity)] {
				return nil, errors.New("severity is invalid")
			}
			out["severity"] = strings.ToLower(severity)
		}
		for _, key := range []string{"template_ids", "tags"} {
			if value, ok := out[key]; ok {
				text, ok := value.(string)
				if !ok || strings.ContainsAny(text, "\r\n\x00") {
					return nil, fmt.Errorf("%s contains invalid control characters", key)
				}
			}
		}
	}
	return out, nil
}

func normalizeDiscoveryConfig(input map[string]any) (map[string]any, error) {
	allowed := map[string]bool{
		"ports": true, "max_subdomains": true, "max_addresses_per_host": true, "budget_seconds": true,
	}
	for key := range input {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown discovery config field %q", key)
		}
	}
	out := map[string]any{
		"ports": []int64{80, 443, 8080, 8443}, "max_subdomains": int64(50),
		"max_addresses_per_host": int64(2), "budget_seconds": int64(180),
	}
	for _, item := range []struct {
		name    string
		minimum int64
		maximum int64
	}{
		{name: "max_subdomains", minimum: 1, maximum: 50},
		{name: "max_addresses_per_host", minimum: 1, maximum: 2},
		{name: "budget_seconds", minimum: 10, maximum: 600},
	} {
		if raw, ok := input[item.name]; ok {
			value, valid := integerValue(raw)
			if !valid || value < item.minimum || value > item.maximum {
				return nil, fmt.Errorf("%s must be an integer between %d and %d", item.name, item.minimum, item.maximum)
			}
			out[item.name] = value
		}
	}
	if raw, ok := input["ports"]; ok {
		values, err := integerSlice(raw)
		if err != nil || len(values) < 1 || len(values) > 8 {
			return nil, errors.New("ports must contain 1..8 integers")
		}
		seen := make(map[int64]struct{}, len(values))
		ports := make([]int64, 0, len(values))
		for _, value := range values {
			if value < 1 || value > 65535 {
				return nil, errors.New("ports must contain integers between 1 and 65535")
			}
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				ports = append(ports, value)
			}
		}
		sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
		out["ports"] = ports
	}
	return out, nil
}

func integerSlice(raw any) ([]int64, error) {
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []int:
		values = make([]any, len(typed))
		for index, value := range typed {
			values[index] = value
		}
	case []int64:
		values = make([]any, len(typed))
		for index, value := range typed {
			values[index] = value
		}
	default:
		return nil, errors.New("value is not an integer array")
	}
	out := make([]int64, 0, len(values))
	for _, rawValue := range values {
		value, ok := integerValue(rawValue)
		if !ok {
			return nil, errors.New("value is not an integer array")
		}
		out = append(out, value)
	}
	return out, nil
}

func integerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case int32:
		return int64(number), true
	case json.Number:
		result, err := number.Int64()
		return result, err == nil
	case float64:
		result := int64(number)
		return result, float64(result) == number
	default:
		return 0, false
	}
}

func canonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func nonNilStrings(values []string) []string {
	return append([]string{}, values...)
}

func randomProxyToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func stringMap(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]string{}, nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func sanitizeRecord(tool string, input scannerRecord, token string) (map[string]any, error) {
	if tool == "discovery" {
		return sanitizeDiscoveryRecord(input, token)
	}
	endpoint, _ := canonicalEvidenceURL(input.Endpoint)
	target := redactURL(input.Target)
	if endpoint == "" {
		if candidate, ok := canonicalEvidenceURL(input.Target); ok {
			endpoint = candidate
		}
	}
	return map[string]any{
		"tool": tool, "type": redactRuntimeSecret(input.Type, token), "severity": redactRuntimeSecret(input.Severity, token),
		"name": redactRuntimeSecret(input.Name, token), "target": redactRuntimeSecret(target, token),
		"endpoint": redactRuntimeSecret(redactURL(endpoint), token),
		"evidence": redactRuntimeSecret(input.Evidence, token), "template_id": redactRuntimeSecret(input.TemplateID, token),
		"sidecar_observation_id": redactRuntimeSecret(input.ObservationID, token),
		"sidecar_dedup_fp":       redactRuntimeSecret(input.DedupFP, token),
	}, nil
}

func sanitizeDiscoveryRecord(input scannerRecord, token string) (map[string]any, error) {
	input.Tool = strings.ToLower(strings.TrimSpace(input.Tool))
	input.Stage = strings.ToLower(strings.TrimSpace(input.Stage))
	if input.Tool != input.Stage {
		return nil, errors.New("Arsenal discovery record tool/stage mismatch")
	}
	wantType := map[string]string{"subfinder": "subdomain", "dns": "dns", "naabu": "port", "httpx": "service"}
	if wantType[input.Stage] == "" || input.Type != wantType[input.Stage] {
		return nil, errors.New("Arsenal discovery record has an invalid stage/type")
	}
	record := map[string]any{
		"tool": input.Tool, "stage": input.Stage, "type": input.Type,
		"severity":               redactRuntimeSecret(input.Severity, token),
		"name":                   bounded(redactRuntimeSecret(input.Name, token)),
		"evidence":               bounded(redactRuntimeSecret(input.Evidence, token)),
		"sidecar_observation_id": redactRuntimeSecret(input.ObservationID, token),
		"sidecar_dedup_fp":       redactRuntimeSecret(input.DedupFP, token),
	}
	if input.Host != "" {
		host, err := canonicalDiscoveryHost(input.Host)
		if err != nil {
			return nil, fmt.Errorf("invalid discovery observation host: %w", err)
		}
		record["host"] = host
	}
	if input.Address != "" {
		address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(input.Address), "[]"))
		if err != nil || address.Zone() != "" {
			return nil, errors.New("invalid discovery observation address")
		}
		record["address"] = address.Unmap().String()
	}
	if input.Port != 0 {
		if input.Port < 1 || input.Port > 65535 {
			return nil, errors.New("invalid discovery observation port")
		}
		record["port"] = input.Port
	}
	if input.Stage == "subfinder" && record["host"] == nil {
		return nil, errors.New("subfinder observation omitted its host")
	}
	if input.Stage == "dns" && (record["host"] == nil || record["address"] == nil) {
		return nil, errors.New("DNS observation omitted its frozen binding")
	}
	if input.Stage == "naabu" && (record["host"] == nil || record["address"] == nil || record["port"] == nil) {
		return nil, errors.New("naabu observation omitted its host/address/port")
	}
	if input.Stage == "httpx" {
		endpoint, ok := canonicalEvidenceURL(input.Endpoint)
		if !ok {
			return nil, errors.New("httpx discovery observation omitted a valid endpoint")
		}
		record["target"] = redactRuntimeSecret(redactURL(input.Target), token)
		record["endpoint"] = redactRuntimeSecret(redactURL(endpoint), token)
	}
	return record, nil
}

func bounded(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxStoredEvidenceRunes {
		runes = runes[:maxStoredEvidenceRunes]
	}
	return string(runes)
}

func canonicalEvidenceURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	parsed.Host = host
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), true
}

func redactURL(raw string) string {
	clean, ok := canonicalEvidenceURL(raw)
	if !ok {
		return ""
	}
	parsed, _ := url.Parse(clean)
	query := parsed.Query()
	for key := range query {
		query[key] = []string{"REDACTED"}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func originURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("invalid captured Arsenal URL")
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), nil
}

func endpointParams(raw string) []map[string]any {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(parsed.Query()))
	for key := range parsed.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"location": "query", "name": key})
	}
	return out
}

func redactRuntimeSecret(value, token string) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, "REDACTED")
	}
	return bounded(value)
}

func redactValue(value any, token string) any {
	switch item := value.(type) {
	case string:
		return redactRuntimeSecret(item, token)
	case []any:
		out := make([]any, len(item))
		for index := range item {
			out[index] = redactValue(item[index], token)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, child := range item {
			out[key] = redactValue(child, token)
		}
		return out
	default:
		return value
	}
}
