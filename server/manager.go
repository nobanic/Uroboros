package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/agent"
	arsenalrunner "github.com/nobanic/UroborOS/arsenal"
	pgdb "github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/enrich"
	"github.com/nobanic/UroborOS/guard"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/UroborOS/internal/reversal"
	katanarunner "github.com/nobanic/UroborOS/katana"
	"github.com/nobanic/UroborOS/traffic"
	actool "github.com/nobanic/norma/tool"
)

// Task is one engagement: a description + goal + its own exploration store,
// sharing the process-wide asset store. ID is the PG task id as a string; ExpID
// is the exploration the task owns.
type Task struct {
	ID           string `json:"id"`
	ExpID        int64  `json:"exploration_id"`
	Name         string `json:"name"` // optional task name; empty = unnamed
	CategoryID   *int64 `json:"category_id,omitempty"`
	CategoryName string `json:"category_name,omitempty"`
	PinnedAt     int64  `json:"pinned_at,omitempty"`
	Description  string `json:"description"`
	Goal         string `json:"goal"`
	CreatedAt    int64  `json:"created_at"`
	CompletedAt  int64  `json:"completed_at,omitempty"` // unix seconds when it entered a terminal state; 0 = not completed
	Paused       bool   `json:"paused"`
	Queued       bool   `json:"queued"` // held by the concurrency cap, waiting for a slot to auto-start; true = not yet started
	// QueuedAt is an internal Unix-nanosecond ordering key. It is deliberately
	// finer than CreatedAt so several tasks enqueued in the same second retain
	// their real FIFO order.
	QueuedAt           int64   `json:"queued_at,omitempty"`
	QueueMode          string  `json:"queue_mode,omitempty"`
	ParentRef          string  `json:"parent_ref,omitempty"`     // parent task id (orchestration spawn record)
	LLMProfileID       *int64  `json:"llm_profile_id,omitempty"` // the LLM profile this task's planner/worker runs on; nil = use the global active profile
	LLMProfileIDs      []int64 `json:"llm_profile_ids,omitempty"`
	ActiveLLMProfileID *int64  `json:"active_llm_profile_id,omitempty"`
	LLMChainRevision   int64   `json:"-"`
	LLMFailoverState   string  `json:"llm_failover_state,omitempty"`
	LLMFailoverReason  string  `json:"llm_failover_reason,omitempty"`
	SourceTaskIDs      []int64 `json:"source_task_ids,omitempty"`
	CompanyIDs         []int64 `json:"company_ids,omitempty"`
	Status             string  `json:"status"` // persisted lifecycle status (done/failed/timeout are terminal; empty/other is derived from runtime state)
	// Task-level timeout (see docs/task-timeout-and-wrapup-design.md). DeadlineAt/FirstRunAt are unix seconds, 0 = unset/not run.
	TimeoutSeconds       int                    `json:"timeout_seconds"`
	PlanHeartbeatSeconds int                    `json:"plan_heartbeat_seconds"` // planner heartbeat trigger interval (seconds)
	CoverageEnabled      bool                   `json:"coverage_enabled"`       // asset coverage feature toggle (set at creation, default on)
	StrictIsolation      bool                   `json:"strict_isolation"`       // expose only assets associated with this task; source inheritance is disabled
	SkipInitialKatana    bool                   `json:"skip_initial_katana"`
	InitialScout         *bool                  `json:"initial_scout"`
	FirstRunAt           int64                  `json:"first_run_at,omitempty"`
	DeadlineAt           int64                  `json:"deadline_at,omitempty"`
	Store                *pgdb.ExplorationStore `json:"-"`
	Guard                *guard.Guard           `json:"-"`
	notify               chan struct{}
	lifecycleMu          sync.RWMutex
	llmMu                sync.RWMutex

	// pendingTriggers accumulates the concrete changes (worker done / finding) that
	// fired planning rounds since the last one consumed them. The debounce coalesces
	// a burst into one round, so several may pile up before drainTriggers() clears them.
	trigMu          sync.Mutex
	pendingTriggers []agent.TriggerEvent
}

// taskLifecycleState is an internally consistent view of the mutable task
// lifecycle and inherited-scope context. Callers must use lifecycleSnapshot and
// updateLifecycle instead of reading or writing the corresponding Task fields
// directly after the task has been published by Manager.
type taskLifecycleState struct {
	Name          string
	PinnedAt      int64
	Status        string
	Paused        bool
	Queued        bool
	QueuedAt      int64
	QueueMode     string
	CompletedAt   int64
	FirstRunAt    int64
	DeadlineAt    int64
	SourceTaskIDs []int64
	CompanyIDs    []int64
	CategoryID    *int64
	CategoryName  string
}

func (t *Task) lifecycleSnapshot() taskLifecycleState {
	if t == nil {
		return taskLifecycleState{}
	}
	t.lifecycleMu.RLock()
	defer t.lifecycleMu.RUnlock()
	return t.lifecycleSnapshotLocked()
}

func (t *Task) lifecycleSnapshotLocked() taskLifecycleState {
	return taskLifecycleState{
		Name:          t.Name,
		PinnedAt:      t.PinnedAt,
		Status:        t.Status,
		Paused:        t.Paused,
		Queued:        t.Queued,
		QueuedAt:      t.QueuedAt,
		QueueMode:     t.QueueMode,
		CompletedAt:   t.CompletedAt,
		FirstRunAt:    t.FirstRunAt,
		DeadlineAt:    t.DeadlineAt,
		SourceTaskIDs: append([]int64(nil), t.SourceTaskIDs...),
		CompanyIDs:    append([]int64(nil), t.CompanyIDs...),
		CategoryID:    cloneInt64Ptr(t.CategoryID),
		CategoryName:  t.CategoryName,
	}
}

func (t *Task) updateLifecycle(update func(*taskLifecycleState)) {
	if t == nil || update == nil {
		return
	}
	t.lifecycleMu.Lock()
	state := t.lifecycleSnapshotLocked()
	update(&state)
	t.Name = state.Name
	t.PinnedAt = state.PinnedAt
	t.Status = state.Status
	t.Paused = state.Paused
	t.Queued = state.Queued
	t.QueuedAt = state.QueuedAt
	t.QueueMode = state.QueueMode
	t.CompletedAt = state.CompletedAt
	t.FirstRunAt = state.FirstRunAt
	t.DeadlineAt = state.DeadlineAt
	t.SourceTaskIDs = append(t.SourceTaskIDs[:0], state.SourceTaskIDs...)
	t.CompanyIDs = append(t.CompanyIDs[:0], state.CompanyIDs...)
	t.CategoryID = cloneInt64Ptr(state.CategoryID)
	t.CategoryName = state.CategoryName
	t.lifecycleMu.Unlock()
}

type taskLLMState struct {
	ProfileID      *int64
	ProfileIDs     []int64
	ActiveID       *int64
	ChainRevision  int64
	FailoverState  string
	FailoverReason string
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (t *Task) llmStateSnapshot() taskLLMState {
	t.llmMu.RLock()
	defer t.llmMu.RUnlock()
	return taskLLMState{
		ProfileID:      cloneInt64Ptr(t.LLMProfileID),
		ProfileIDs:     append(make([]int64, 0, len(t.LLMProfileIDs)), t.LLMProfileIDs...),
		ActiveID:       cloneInt64Ptr(t.ActiveLLMProfileID),
		ChainRevision:  t.LLMChainRevision,
		FailoverState:  t.LLMFailoverState,
		FailoverReason: t.LLMFailoverReason,
	}
}

func (t *Task) setLLMState(profileID, activeID *int64, profileIDs []int64, revision int64, state, reason string) bool {
	t.llmMu.Lock()
	defer t.llmMu.Unlock()
	if revision < t.LLMChainRevision {
		return false
	}
	t.LLMProfileID = cloneInt64Ptr(profileID)
	t.ActiveLLMProfileID = cloneInt64Ptr(activeID)
	t.LLMProfileIDs = append(t.LLMProfileIDs[:0], profileIDs...)
	t.LLMChainRevision = revision
	t.LLMFailoverState = state
	t.LLMFailoverReason = reason
	return true
}

// DeleteTaskOptions controls cleanup of data stored outside the task's own
// exploration graph. All options default to false for backward compatibility.
type DeleteTaskOptions struct {
	DeleteAssets     bool `json:"delete_assets"`
	DeleteTraffic    bool `json:"delete_traffic"`
	DeleteFiles      bool `json:"delete_files"`
	DeleteFindings   bool `json:"delete_findings"`
	DeleteLLMRecords bool `json:"delete_llm_records"`
}

// DeleteTaskResult makes destructive cleanup auditable to API callers.
type DeleteTaskResult struct {
	Deleted           string `json:"deleted"`
	AssetsDeleted     int64  `json:"assets_deleted"`
	AssetsDetached    int64  `json:"assets_detached"`
	TrafficDeleted    int64  `json:"traffic_deleted"`
	FilesDeleted      bool   `json:"files_deleted"`
	FindingsDeleted   int64  `json:"findings_deleted"`
	LLMRecordsDeleted int64  `json:"llm_records_deleted"`
	CleanupWarning    string `json:"cleanup_warning,omitempty"`
}

// Manager owns the PostgreSQL data source (asset graph + every task's exploration
// graph + config) and the in-memory set of task handles.
type Manager struct {
	dir         string
	pg          *pgdb.DB
	assets      *pgdb.AssetStore
	traffic     *traffic.Traffic      // process-wide recording proxy (may be nil)
	katana      *katanarunner.Manager // task-scoped core discovery runner
	katanaStop  context.CancelCauseFunc
	arsenal     *arsenalrunner.Manager // task-scoped scanner execution owner
	arsenalStop context.CancelCauseFunc
	enrich      *enrich.Engine         // engine-side asset auto-completion (DNS/HTTP)
	interceptor *intercept.Interceptor // user-configured tool-call interception rules

	companyMu sync.Mutex // serializes task/company-scope commits with live handle registration
	// taskStateMu preserves commit order between PostgreSQL lifecycle writes and
	// their in-memory mirrors. lifecycleMu makes snapshots race-free, but without
	// this outer write lock an older request could commit first and publish last.
	taskStateMu sync.Mutex
	mu          sync.RWMutex
	tasks       map[string]*Task
	active      string
	trafficOn   bool // traffic-capture toggle (default off; settings.traffic_capture)
	llmRecOn    bool // LLM recording toggle (default off; settings.llm_record)
	// Web-search toggle and provider policy (default off; settings.web_search_*).
	// Keyed providers are skipped when their key is absent; an eligible fallback may still run.
	// webSearchProxy is a dedicated egress proxy (http/https/socks5), unrelated to the traffic-recording MITM proxy.
	webSearchOn      bool
	webSearchBackend string
	braveKey         string
	tavilyKey        string
	webSearchProxy   string
	// globalProxy is the egress proxy all target traffic routes through
	// (http/https/socks5, optional user:pass). Empty = direct. When traffic
	// capture is on it becomes the MITM's upstream; when capture is off it is
	// injected into agent bash env / WebFetch directly. See ProxyAddr.
	globalProxy string
}

// Settings keys the UI toggles at runtime.
const (
	settingTrafficCapture      = "traffic_capture"
	settingAgentTrafficBinding = "agent_traffic_binding"
	settingWebSearchOn         = "web_search_enabled"
	settingWebSearchBackend    = "web_search_backend"
	settingBraveKey            = "brave_search_api_key"
	settingTavilyKey           = "tavily_search_api_key"
	settingWebSearchProxy      = "web_search_proxy"
	settingWebSearchFallback   = "web_search_fallback_backend"
	settingResearchTaskBudget  = "web_search_task_request_budget"
	settingResearchPaidBudget  = "web_search_task_paid_request_budget"
	settingResearchRate        = "web_search_rate_per_minute"
	settingResearchCacheTTL    = "web_search_cache_ttl_seconds"
	settingTorResearchEnabled  = "tor_research_enabled"
	// settingGlobalProxy is the global egress proxy for all target traffic
	// (http/https/socks5). Empty = direct. Distinct from web_search_proxy (which
	// only routes the search backend) and the per-profile LLM proxy.
	settingGlobalProxy = "global_proxy"
	settingWorkers     = "workers"
	settingLLMRecord   = "llm_record"
	// LLM round-robin (failover). Default off — when on, an agent using the "global active profile" automatically
	// switches to the next profile when the current one is unavailable (out of balance / invalid key / rate-limited / service error).
	// settingLLMPoolBindFallback only matters while round-robin is on: default off, i.e. an agent/task explicitly
	// bound to a profile uses only that one and fails if it fails; when on, a bound profile's failure also falls back to the round-robin chain.
	settingLLMPoolOn           = "llm_pool_enabled"
	settingLLMPoolBindFallback = "llm_pool_bind_fallback"
	// Task concurrency cap: a toggle + a limit number. Default off; when on the default limit is 5 (see defaultConcurrencyLimit).
	settingConcurrencyOn    = "task_concurrency_enabled"
	settingConcurrencyLimit = "task_concurrency_limit"
	// Experimental: noa model-driven context compression (norma v0.4.0). Default off — when on, the four agent types the platform
	// wires in (planner/worker/main agent/conversation) have their context compression taken over by noa, replacing the built-in compaction.
	// Read once per run; toggling only affects runs started afterwards.
	settingNoaCompaction = "noa_compaction"
	// Experimental: issue one narrowly-scoped, read-only reconnaissance intent
	// after goal/scope bootstrap, then wake the planner at the same time. Default
	// off so existing task startup semantics remain unchanged.
	settingEarlyBootstrapScout = "early_bootstrap_scout"
	// Audited research is available out of the box without an API key. Operators
	// can still persist either switch as false; these values only apply when the
	// corresponding setting has never been written.
	defaultWebSearchEnabled   = true
	defaultTorResearchEnabled = true
	// defaultWebSearchBackend is used when web search is on but no backend was picked.
	defaultWebSearchBackend   = "ddgs"
	defaultResearchTaskBudget = 40
	defaultResearchPaidBudget = 20
	defaultResearchRate       = 10
	defaultResearchCacheTTL   = 3600
	// defaultWorkers is the concurrent work-agent count when the setting is unset.
	defaultWorkers = 3
	// defaultConcurrencyLimit is the simultaneous-running-task cap when the feature
	// is enabled but no explicit limit was saved.
	defaultConcurrencyLimit = 5
)

// ConcurrencyLimit returns whether the simultaneous-running-task cap is enabled and
// its limit (default 5 when enabled but unset). limit is always >=1 when enabled.
func (m *Manager) ConcurrencyLimit() (enabled bool, limit int) {
	on, _, _ := m.pg.GetSetting(settingConcurrencyOn)
	if strings.TrimSpace(on) != "true" {
		return false, 0
	}
	limit = defaultConcurrencyLimit
	if v, ok, _ := m.pg.GetSetting(settingConcurrencyLimit); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 1 {
			limit = n
		}
	}
	return true, limit
}

// SetConcurrency persists the running-task concurrency cap. limit<1 is clamped to 1.
func (m *Manager) SetConcurrency(enabled bool, limit int) error {
	if limit < 1 {
		limit = defaultConcurrencyLimit
	}
	if err := m.pg.SetSetting(settingConcurrencyLimit, strconv.Itoa(limit)); err != nil {
		return err
	}
	return m.pg.SetSetting(settingConcurrencyOn, strconv.FormatBool(enabled))
}

// Workers returns the configured concurrent work-agent count (default 3). Read
// per-task at engine.Run, so a change applies to tasks started afterwards.
func (m *Manager) Workers() int {
	v, ok, err := m.pg.GetSetting(settingWorkers)
	if err != nil || !ok {
		return defaultWorkers
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultWorkers
	}
	return n
}

// SetWorkers persists the concurrent work-agent count. Values <=0 are rejected.
func (m *Manager) SetWorkers(n int) error {
	if n <= 0 {
		return fmt.Errorf("workers must be >0")
	}
	return m.pg.SetSetting(settingWorkers, strconv.Itoa(n))
}

// Enrich returns the asset auto-completion engine (may be nil if init failed).
func (m *Manager) Enrich() *enrich.Engine { return m.enrich }

// NewManager connects to PostgreSQL and, if proxyAddr is non-empty, starts the
// traffic-recording proxy. PostgreSQL is required (it is the single data source).
func NewManager(dir, proxyAddr string) (*Manager, error) {
	return (&managerStartup{root: dir, proxyAddr: proxyAddr}).start()
}

// TrafficEnabled reports whether traffic capture is on (default off). When off,
// no proxy/traffic tools/prompt are injected into agents (nothing is recorded).
func (m *Manager) TrafficEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trafficOn
}

// SetTrafficEnabled persists and applies the traffic-capture toggle. Callers must
// rebuild the agents (applyLLM) afterwards so the new proxy/tools/prompt take hold.
func (m *Manager) SetTrafficEnabled(on bool) error {
	if err := m.pg.SetBool(settingTrafficCapture, on); err != nil {
		return err
	}
	m.mu.Lock()
	m.trafficOn = on
	m.mu.Unlock()
	// Inject (on) or strip (off) the recording proxy + CA on the browser MCP so
	// Playwright routes through the MITM. Must run after the flag flip above, since
	// ProxyAddr/ProxyCACert honor it. putSettings rebuilds agents next (applyLLM),
	// which re-spawns the MCP with the new args/env.
	m.syncBrowserMCPProxy()
	return nil
}

// LLMRecordEnabled reports whether LLM request/response recording is on
// (default off; settings.llm_record). The recorder consults this per call, so the
// toggle takes effect immediately without rebuilding agents.
func (m *Manager) LLMRecordEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.llmRecOn
}

// SetLLMRecordEnabled persists and applies the LLM-record toggle. Effective at
// once — no applyLLM needed, since the recorder reads the flag on every call.
func (m *Manager) SetLLMRecordEnabled(on bool) error {
	if err := m.pg.SetBool(settingLLMRecord, on); err != nil {
		return err
	}
	m.mu.Lock()
	m.llmRecOn = on
	m.mu.Unlock()
	return nil
}

// NoaCompactionEnabled reports whether the experimental noa context-compression
// mechanism is on (default off; settings.noa_compaction). Read per agent run via the
// injected resolver, so a toggle takes effect on the next run without rebuild.
func (m *Manager) NoaCompactionEnabled() bool {
	return m.pg.GetBool(settingNoaCompaction, false)
}

// SetNoaCompaction persists the noa toggle. Effective on the next agent run —
// the resolver reads it per run, so no rebuild is needed.
func (m *Manager) SetNoaCompaction(on bool) error {
	return m.pg.SetBool(settingNoaCompaction, on)
}

// EarlyBootstrapScoutEnabled reports whether new tasks should start one bounded
// reconnaissance worker in parallel with their first planner round. It is read
// at task bootstrap, so changing it does not alter tasks that are already live.
func (m *Manager) EarlyBootstrapScoutEnabled() bool {
	return m.pg.GetBool(settingEarlyBootstrapScout, false)
}

// SetEarlyBootstrapScout persists the reversible startup experiment toggle.
func (m *Manager) SetEarlyBootstrapScout(on bool) error {
	return m.pg.SetBool(settingEarlyBootstrapScout, on)
}

// LLMPoolEnabled reports whether LLM failover ("round-robin") is on (default off;
// settings.llm_pool_enabled). Read when the provider chain is built (applyLLM),
// so a change requires a rebuild — putSettings does that.
func (m *Manager) LLMPoolEnabled() bool {
	if m.pg == nil {
		return false
	}
	return m.pg.GetBool(settingLLMPoolOn, false)
}

// SetLLMPoolEnabled persists the failover toggle. Callers rebuild agents
// (applyLLM) afterwards so it takes effect.
func (m *Manager) SetLLMPoolEnabled(on bool) error { return m.pg.SetBool(settingLLMPoolOn, on) }

// LLMPoolBindFallback reports whether an agent/task that is BOUND to a specific
// profile still falls back to the chain when that profile fails (default off: a binding is
// exclusive, fail means fail). Only meaningful while LLMPoolEnabled.
func (m *Manager) LLMPoolBindFallback() bool {
	if m.pg == nil {
		return false
	}
	return m.pg.GetBool(settingLLMPoolBindFallback, false)
}

// SetLLMPoolBindFallback persists the bound-profile fallback toggle. Callers
// rebuild agents (applyLLM) afterwards.
func (m *Manager) SetLLMPoolBindFallback(on bool) error {
	return m.pg.SetBool(settingLLMPoolBindFallback, on)
}

// WebSearch returns the current web-search config: whether it is enabled, the
// backend ("ddgs" | "brave-free" | "tavily"), the Brave API key, the Tavily API
// key (each empty unless set), and the dedicated egress proxy (empty = direct).
func (m *Manager) WebSearch() (on bool, backend, braveKey, tavilyKey, proxy string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	backend = m.webSearchBackend
	if backend == "" {
		backend = defaultWebSearchBackend
	}
	return m.webSearchOn, backend, m.braveKey, m.tavilyKey, m.webSearchProxy
}

// WebSearchOpts returns only the global on/off gate. Provider configuration is
// resolved inside the host broker at call time.
func (m *Manager) WebSearchOpts() agent.WebSearchOpts {
	on, _, _, _, _ := m.WebSearch()
	return agent.WebSearchOpts{Enabled: on}
}

// researchRuntimeConfig is resolved by the host-owned broker at call time. It
// never crosses the model/tool schema boundary; API keys, proxy coordinates and
// fallback policy remain server-owned.
type researchRuntimeConfig struct {
	Enabled           bool
	Primary           string
	Fallback          string
	BraveKey          string
	TavilyKey         string
	Proxy             string
	TaskBudget        int
	PaidRequestBudget int
	RatePerMinute     int
	CacheTTL          time.Duration
	TorEnabled        bool
	TorSOCKSURL       string
}

func settingInt(pg *pgdb.DB, key string, fallback, min, max int) int {
	value, ok, err := pg.GetSetting(key)
	if err != nil || !ok {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < min || n > max {
		return fallback
	}
	return n
}

func (m *Manager) ResearchRuntimeConfig() researchRuntimeConfig {
	on, primary, braveKey, tavilyKey, proxy := m.WebSearch()
	fallback, ok, _ := m.pg.GetSetting(settingWebSearchFallback)
	fallback = strings.TrimSpace(fallback)
	if !ok {
		if primary == "brave-free" || primary == "tavily" {
			fallback = "ddgs"
		}
	}
	if fallback == primary {
		fallback = ""
	}
	return researchRuntimeConfig{
		Enabled: on, Primary: primary, Fallback: fallback,
		BraveKey: braveKey, TavilyKey: tavilyKey, Proxy: proxy,
		TaskBudget:        settingInt(m.pg, settingResearchTaskBudget, defaultResearchTaskBudget, 1, 100000),
		PaidRequestBudget: settingInt(m.pg, settingResearchPaidBudget, defaultResearchPaidBudget, 0, 100000),
		RatePerMinute:     settingInt(m.pg, settingResearchRate, defaultResearchRate, 1, 10000),
		CacheTTL:          time.Duration(settingInt(m.pg, settingResearchCacheTTL, defaultResearchCacheTTL, 60, 604800)) * time.Second,
		TorEnabled:        m.pg.GetBool(settingTorResearchEnabled, defaultTorResearchEnabled),
		TorSOCKSURL:       strings.TrimSpace(os.Getenv("UROBOROS_TOR_SOCKS_URL")),
	}
}

// SetTorResearchEnabled creates or disables the fixed host-owned `tor` egress
// profile. Its SOCKS endpoint remains deployment-owned in
// UROBOROS_TOR_SOCKS_URL and is never persisted or returned by the API.
func (m *Manager) SetTorResearchEnabled(on bool) error {
	return m.pg.SetBool(settingTorResearchEnabled, on)
}

func (m *Manager) SetResearchPolicy(fallback *string, taskBudget, paidBudget, rate, cacheTTL *int) error {
	if fallback != nil {
		value := strings.TrimSpace(*fallback)
		if value != "" && !pgdb.ValidWebSearchProvider(value) {
			return errors.New("web search fallback must be brave-free, tavily, ddgs, or empty")
		}
		if err := m.pg.SetSetting(settingWebSearchFallback, value); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		value       *int
		key         string
		minimum     int
		maximum     int
		description string
	}{
		{taskBudget, settingResearchTaskBudget, 1, 100000, "task request budget"},
		{paidBudget, settingResearchPaidBudget, 0, 100000, "paid request budget"},
		{rate, settingResearchRate, 1, 10000, "research rate per minute"},
		{cacheTTL, settingResearchCacheTTL, 60, 604800, "research cache TTL"},
	} {
		if item.value == nil {
			continue
		}
		if *item.value < item.minimum || *item.value > item.maximum {
			return fmt.Errorf("%s must be between %d and %d", item.description, item.minimum, item.maximum)
		}
		if err := m.pg.SetSetting(item.key, strconv.Itoa(*item.value)); err != nil {
			return err
		}
	}
	return nil
}

// SetWebSearch persists and applies the web-search settings. braveKey, tavilyKey, and
// proxy are each left untouched when nil (so toggling the switch doesn't wipe a saved
// key/proxy; pass a pointer to "" to clear). Callers must rebuild agents (applyLLM)
// afterwards so the settings take effect.
func (m *Manager) SetWebSearch(on bool, backend string, braveKey, tavilyKey, proxy *string) error {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		backend = defaultWebSearchBackend
	}
	if !pgdb.ValidWebSearchProvider(backend) {
		return errors.New("web search backend must be brave-free, tavily, or ddgs")
	}
	if err := m.pg.SetBool(settingWebSearchOn, on); err != nil {
		return err
	}
	if err := m.pg.SetSetting(settingWebSearchBackend, backend); err != nil {
		return err
	}
	m.mu.Lock()
	m.webSearchOn = on
	m.webSearchBackend = backend
	m.mu.Unlock()
	if braveKey != nil {
		if err := m.pg.SetSetting(settingBraveKey, *braveKey); err != nil {
			return err
		}
		m.mu.Lock()
		m.braveKey = *braveKey
		m.mu.Unlock()
	}
	if tavilyKey != nil {
		if err := m.pg.SetSetting(settingTavilyKey, *tavilyKey); err != nil {
			return err
		}
		m.mu.Lock()
		m.tavilyKey = *tavilyKey
		m.mu.Unlock()
	}
	if proxy != nil {
		p := strings.TrimSpace(*proxy)
		if err := m.pg.SetSetting(settingWebSearchProxy, p); err != nil {
			return err
		}
		m.mu.Lock()
		m.webSearchProxy = p
		m.mu.Unlock()
	}
	return nil
}

// browserMCPName is the seeded Playwright MCP whose capture-proxy arguments are
// reconciled by the host.
const browserMCPName = "browser"

const (
	browserChromeBinEnv          = "UROBOROS_CHROME_BIN"
	browserNoSandboxEnv          = "UROBOROS_BROWSER_MCP_NO_SANDBOX"
	browserManagedTLSBypassEnv   = "UROBOROS_BROWSER_CAPTURE_TLS_BYPASS"
	browserIgnoreHTTPSErrorsFlag = "--ignore-https-errors"
)

// syncBrowserMCPProxy reconciles the seeded browser MCP with the current traffic-
// capture state. Capture on routes Playwright through the recording proxy and
// enables browser-context TLS tolerance for that operator-owned MITM;
// NODE_EXTRA_CA_CERTS alone affects Node, not Chromium page TLS. Capture off
// removes only the TLS flag that this function injected, preserving an operator-
// provided flag. Idempotent, and a no-op if the user deleted/renamed the MCP.
// Must be called WITHOUT m.mu held (ProxyAddr/ProxyCACert take the lock).
func (m *Manager) syncBrowserMCPProxy() {
	servers, err := m.pg.ListMCP()
	if err != nil {
		log.Printf("[mcp] browser proxy sync: read MCP list failed: %v", err)
		return
	}
	var srv *pgdb.MCPServer
	for _, s := range servers {
		if s.Name == browserMCPName {
			srv = s
			break
		}
	}
	if srv == nil {
		return // user removed/renamed it — leave it alone
	}

	proxy := m.ProxyAddr()  // "" when capture off
	cert := m.ProxyCACert() // "" when capture off

	args := decodeStrSlice(srv.Args)
	env := decodeStrMap(srv.Env)
	playwright := isPlaywrightMCP(srv.Command, args)
	args, env = reconcileBrowserCaptureArgs(args, env, proxy, cert, playwright)
	srv.Args = encodeJSON(args)
	srv.Env = encodeJSON(env)
	if _, err := m.pg.SaveMCP(srv); err != nil {
		log.Printf("[mcp] browser proxy sync failed: %v", err)
		return
	}
	if proxy != "" {
		log.Printf("[mcp] browser MCP now routed through capture proxy %s (CA %s)", proxy, cert)
	} else {
		log.Printf("[mcp] browser MCP capture-proxy config removed")
	}
}

// resolvePlaywrightMCPRuntime adds immutable image-owned launch details only to
// the child process invocation. They are deliberately not persisted in the
// operator's MCP row, so moving the DB to another runtime or updating the image
// cannot leave a stale executable path behind. The legacy unversioned seed uses
// npx; when the pinned global entrypoint exists, invoke it directly so runtime
// startup never depends on npm cache state or a network download.
func resolvePlaywrightMCPRuntime(command string, args []string) (string, []string) {
	if !isPlaywrightMCP(command, args) {
		return command, append([]string(nil), args...)
	}
	direct, _ := exec.LookPath("playwright-mcp")
	command, args = preferInstalledPlaywrightMCP(command, args, direct)
	chromeBin := strings.TrimSpace(os.Getenv(browserChromeBinEnv))
	return command, ensureBrowserRuntimeArgs(args, chromeBin, browserMCPNoSandbox(chromeBin))
}

func preferInstalledPlaywrightMCP(command string, args []string, direct string) (string, []string) {
	out := append([]string(nil), args...)
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	if direct == "" || (base != "npx" && base != "npx.cmd") {
		return command, out
	}
	packageIndex := 0
	if len(out) > 0 && (out[0] == "-y" || out[0] == "--yes") {
		packageIndex++
	}
	if packageIndex >= len(out) || out[packageIndex] != "@playwright/mcp" {
		// A versioned package or an invocation with custom npx options is an
		// operator choice; preserve it instead of reinterpreting those options as
		// playwright-mcp arguments.
		return command, out
	}
	return direct, append([]string(nil), out[packageIndex+1:]...)
}

// isPlaywrightMCP limits image-specific arguments to the seeded/local
// @playwright/mcp command. A user may keep the reserved browser name while
// replacing the command with another implementation; that command must not
// receive Playwright-only flags.
func isPlaywrightMCP(command string, args []string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	if base != "npx" && base != "npx.cmd" && base != "playwright-mcp" && base != "playwright-mcp.cmd" {
		return false
	}
	if base == "playwright-mcp" || base == "playwright-mcp.cmd" {
		return true
	}
	for _, arg := range args {
		if arg == "@playwright/mcp" || strings.HasPrefix(arg, "@playwright/mcp@") {
			return true
		}
	}
	return false
}

// ensureBrowserRuntimeArgs selects the immutable Chromium bundled in the app
// image without overriding an operator-selected browser, executable, CDP
// endpoint, or extension connection. The Linux container explicitly opts into
// no-sandbox mode because it already runs non-root, capability-dropped and with
// no-new-privileges, while Chromium's user-namespace sandbox is unavailable.
func ensureBrowserRuntimeArgs(args []string, chromeBin string, noSandbox bool) []string {
	out := append([]string(nil), args...)
	chromeBin = strings.TrimSpace(chromeBin)
	if chromeBin == "" {
		return out
	}

	executable, hasExecutable := optionValue(out, "--executable-path")
	hasAlternateBrowser := hasOption(out, "--browser") || hasOption(out, "--cdp-endpoint") || hasOption(out, "--extension")
	managedExecutable := false
	if !hasExecutable && !hasAlternateBrowser {
		out = append(out, "--executable-path", chromeBin)
		managedExecutable = true
	} else if hasExecutable && sameExecutablePath(executable, chromeBin) {
		managedExecutable = true
	}
	if managedExecutable && noSandbox && !hasOption(out, "--no-sandbox") && !hasOption(out, "--sandbox") {
		out = append(out, "--no-sandbox")
	}
	return out
}

func browserMCPNoSandbox(chromeBin string) bool {
	// Existing accepted images already expose UROBOROS_CHROME_BIN. Defaulting to
	// true when it is present repairs those images after a binary-only update;
	// native deployments can explicitly set the env to false.
	enabled := strings.TrimSpace(chromeBin) != ""
	raw, ok := os.LookupEnv(browserNoSandboxEnv)
	if !ok {
		return enabled
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		log.Printf("[mcp] invalid %s=%q; using %t", browserNoSandboxEnv, raw, enabled)
		return enabled
	}
	return parsed
}

func sameExecutablePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func hasOption(args []string, name string) bool {
	_, ok := optionValue(args, name)
	return ok
}

func optionValue(args []string, name string) (string, bool) {
	for i, arg := range args {
		if arg == name {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"="), true
		}
	}
	return "", false
}

// reconcileBrowserCaptureArgs owns proxy routing and the TLS bypass it injects.
// browserManagedTLSBypassEnv records provenance for that one flag so capture-off
// can remove it without deleting an operator-provided --ignore-https-errors.
func reconcileBrowserCaptureArgs(args []string, env map[string]string, proxy, cert string, playwright bool) ([]string, map[string]string) {
	out := stripProxyArgs(args)
	nextEnv := make(map[string]string, len(env)+2)
	for key, value := range env {
		nextEnv[key] = value
	}

	if nextEnv[browserManagedTLSBypassEnv] == "1" {
		out = removeFirstFlag(out, browserIgnoreHTTPSErrorsFlag)
	}
	delete(nextEnv, browserManagedTLSBypassEnv)
	delete(nextEnv, "NODE_EXTRA_CA_CERTS")

	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return out, nextEnv
	}
	out = append(out, "--proxy-server", proxy)
	if cert = strings.TrimSpace(cert); cert != "" {
		nextEnv["NODE_EXTRA_CA_CERTS"] = cert
	}
	if playwright && !hasOption(out, browserIgnoreHTTPSErrorsFlag) {
		out = append(out, browserIgnoreHTTPSErrorsFlag)
		nextEnv[browserManagedTLSBypassEnv] = "1"
	}
	return out, nextEnv
}

func removeFirstFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	removed := false
	for _, arg := range args {
		if !removed && arg == flag {
			removed = true
			continue
		}
		out = append(out, arg)
	}
	return out
}

// stripProxyArgs removes any --proxy-server/--proxy-bypass flags (both "--flag val"
// and "--flag=val" forms) so they can be re-added cleanly from current state,
// without mutating the input slice.
func stripProxyArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--proxy-server" || a == "--proxy-bypass" {
			i++ // skip the following value too
			continue
		}
		if strings.HasPrefix(a, "--proxy-server=") || strings.HasPrefix(a, "--proxy-bypass=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func decodeStrSlice(raw json.RawMessage) []string {
	var out []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func decodeStrMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func encodeJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// HostTools are runtime host-provided tools added to EVERY agent's base list (via
// ToolAugment); the tools table then filters them per-agent binding. Currently the
// traffic tools, gated by the global capture switch: empty when capture is off, so
// no agent gets traffic_search/traffic_get regardless of binding.
func (m *Manager) HostTools() []actool.CoreTool {
	if m.traffic == nil || !m.TrafficEnabled() {
		return nil
	}
	return m.traffic.Tools()
}

func (m *Manager) Assets() *pgdb.AssetStore        { return m.assets }
func (m *Manager) PG() *pgdb.DB                    { return m.pg }
func (m *Manager) Traffic() *traffic.Traffic       { return m.traffic }
func (m *Manager) Katana() *katanarunner.Manager   { return m.katana }
func (m *Manager) Arsenal() *arsenalrunner.Manager { return m.arsenal }

// ProxyAddr returns the egress proxy address agents route target traffic through:
//   - capture ON  → the recording MITM proxy (which itself exits via the global
//     proxy when one is set); agents also get its CA (see ProxyCACert).
//   - capture OFF → the global egress proxy directly (empty CA — real target
//     certs), or "" when no global proxy is set (direct, no recording).
//
// So the global proxy takes effect in both modes: at the MITM's upstream when
// capturing, in the agent's own bash env / WebFetch when not.
func (m *Manager) ProxyAddr() string {
	if m.traffic != nil && m.TrafficEnabled() {
		return m.traffic.ProxyAddr()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalProxy
}

// ProxyCACert returns the CA cert path agents must trust to verify HTTPS through
// the egress proxy. Non-empty ONLY when traffic capture is on (the MITM re-signs
// certs): the global proxy used directly (capture off) is a plain forwarder that
// preserves real target certs, so no custom CA is needed there. Its emptiness is
// also the worker's "recording off" signal (see workerSystem).
func (m *Manager) ProxyCACert() string {
	if m.traffic == nil || !m.TrafficEnabled() {
		return ""
	}
	return m.traffic.CACertPath()
}

// GlobalProxy returns the configured global egress proxy URL (empty = direct).
func (m *Manager) GlobalProxy() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalProxy
}

// SetGlobalProxy validates, persists and applies the global egress proxy
// (http/https/socks5, optional user:pass; empty = direct). It updates the MITM's
// upstream immediately; callers must rebuild agents (applyLLM) afterwards so the
// capture-off path (bash env / WebFetch) picks up the change too.
func (m *Manager) SetGlobalProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if _, err := traffic.ValidateProxyURL(raw); err != nil {
			return err
		}
	}
	if err := m.pg.SetSetting(settingGlobalProxy, raw); err != nil {
		return err
	}
	m.mu.Lock()
	m.globalProxy = raw
	m.mu.Unlock()
	if m.traffic != nil {
		if err := m.traffic.SetUpstreamProxy(raw); err != nil {
			return err
		}
	}
	// Keep the browser MCP's egress in sync with the new global proxy too.
	m.syncBrowserMCPProxy()
	return nil
}

func (m *Manager) Close() error {
	if m.arsenalStop != nil {
		m.arsenalStop(errors.New("Uroboros manager is closing"))
	}
	if m.arsenal != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := m.arsenal.Close(ctx); err != nil {
			log.Printf("[arsenal] shutdown incomplete: %v", err)
		}
		cancel()
	}
	if m.katanaStop != nil {
		m.katanaStop(errors.New("Uroboros manager is closing"))
	}
	if m.katana != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := m.katana.Close(ctx); err != nil {
			log.Printf("[katana] shutdown incomplete: %v", err)
		}
		cancel()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.traffic != nil {
		m.traffic.Close()
	}
	return m.pg.Close()
}

// isTerminalStatus reports whether a task status is terminal (done/failed/timeout).
// Package-local shim over db.IsTerminal so all server files share one definition.
func isTerminalStatus(status string) bool { return pgdb.IsTerminal(status) }

// unixOrZero returns t's unix seconds, or 0 when the time is nil.
func unixOrZero(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

func unixNanoOrZero(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixNano()
}

func taskFromPG(pt *pgdb.Task, store *pgdb.ExplorationStore, ic *intercept.Interceptor) *Task {
	return (taskMaterialization{row: pt, store: store, interceptor: ic}).task()
}

// UpdateTaskMetadata changes list-only task metadata without interrupting any
// planner, main-agent, or worker call.
func (m *Manager) UpdateTaskMetadata(taskID string, patch pgdb.TaskPatch) (*Task, error) {
	return (taskStateCommand{manager: m, id: taskID}).metadata(patch)
}

// CreateTask creates a task + its exploration and makes it active.
// timeoutSeconds is the task-level wall-clock budget (0 = no limit).
func (m *Manager) CreateTask(description, goal string, llmProfileID *int64, timeoutSeconds, planHeartbeatSeconds int) (*Task, error) {
	var ids []int64
	if llmProfileID != nil {
		ids = []int64{*llmProfileID}
	}
	return m.CreateTaskWithOptions(description, goal, pgdb.TaskCreateOptions{
		LLMProfileIDs: ids, TimeoutSeconds: timeoutSeconds, PlanHeartbeatSeconds: planHeartbeatSeconds,
	})
}

func (m *Manager) CreateTaskWithOptions(description, goal string, opts pgdb.TaskCreateOptions) (*Task, error) {
	if len(opts.CompanyIDs) > 0 {
		m.companyMu.Lock()
		defer m.companyMu.Unlock()
	}
	pt, err := m.pg.CreateTaskWithOptions(description, goal, opts)
	if err != nil {
		return nil, err
	}
	t := taskFromPG(pt, m.pg.Exploration(pt.ExplorationID), m.interceptor)
	m.mu.Lock()
	m.tasks[t.ID] = t
	m.active = t.ID
	m.mu.Unlock()
	return t, nil
}

// RenameTaskCategory persists a category name and refreshes every live task DTO
// that references it. taskStateMu keeps this ordered with task reassignment.
func (m *Manager) RenameTaskCategory(id int64, name string) (*pgdb.TaskCategory, error) {
	return (taskCategoryCommands{manager: m}).rename(id, name)
}

// DeleteTaskCategory moves all affected live tasks to the uncategorized bucket.
func (m *Manager) DeleteTaskCategory(id int64) (bool, error) {
	return (taskCategoryCommands{manager: m}).remove(id)
}

// SetTaskCategory updates one live task without interrupting its runtime.
func (m *Manager) SetTaskCategory(taskID string, categoryID *int64) (*pgdb.TaskCategory, error) {
	return (taskStateCommand{manager: m, id: taskID}).category(categoryID)
}

// SetTasksCategory applies one category change to several tasks at once. The
// database write and the in-memory refresh share taskStateMu, so a concurrent
// single-task update cannot interleave and leave a live DTO stale. The returned
// set holds the ids that were actually moved; callers report the rest as gone.
func (m *Manager) SetTasksCategory(taskIDs []string, categoryID *int64) (map[string]bool, *pgdb.TaskCategory, error) {
	return (taskCategoryCommands{manager: m}).assign(taskIDs, categoryID)
}

// DeleteCompanyWithAssets keeps the database cascade and live task handles in
// one manager-level critical section. This closes the gap where a task could
// commit its company scope immediately before registration and miss the
// post-delete in-memory sweep.
func (m *Manager) DeleteCompanyWithAssets(id int64, deleteAssets bool) (int64, error) {
	m.companyMu.Lock()
	defer m.companyMu.Unlock()
	assetsDeleted, err := m.pg.Companies().DeleteCompanyWithAssets(id, deleteAssets)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	for _, task := range m.tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			companyIDs := make([]int64, 0, len(state.CompanyIDs))
			for _, companyID := range state.CompanyIDs {
				if companyID != id {
					companyIDs = append(companyIDs, companyID)
				}
			}
			state.CompanyIDs = companyIDs
		})
	}
	m.mu.Unlock()
	return assetsDeleted, nil
}

// ReplaceTaskLLMProfiles resets a task's ordered provider chain and mirrors the
// committed state onto the live task handle. Terminal tasks are editable too —
// their main-agent conversation keeps running on the chain after the task finishes.
func (m *Manager) ReplaceTaskLLMProfiles(id string, profileIDs []int64, activeProfileID int64) (int64, error) {
	return (taskStateCommand{manager: m, id: id}).profiles(profileIDs, activeProfileID)
}

// LoadExisting rebuilds in-memory task handles from the PG task registry.
func (m *Manager) LoadExisting() []*Task { return (taskRegistryReload{manager: m}).load() }

// SetTaskPaused persists a task's paused state.
func (m *Manager) SetTaskPaused(id string, paused bool) error {
	return (taskStateCommand{manager: m, id: id}).paused(paused)
}

// ApplyTaskAdmission atomically commits the lifecycle fields controlled by the
// concurrency scheduler. Keeping status, paused and queue metadata in one UPDATE
// prevents a failed resume from leaving a task half-revived (for example running
// but still user-paused, or dequeued without an Engine start).
//
// preservePosition applies only when the row is already queued. A repeated
// admission keeps its FIFO timestamp; a task that was explicitly paused and is
// now re-queued receives a fresh tail position.
func (m *Manager) ApplyTaskAdmission(id, expectedStatus, status string, queued bool, mode string, preservePosition bool) error {
	return (taskStateCommand{manager: m, id: id}).admit(expectedStatus, status, queued, mode, preservePosition)
}

// ApplyTaskPause atomically removes a task from the admission queue and records
// the user pause. queue_mode is intentionally retained so resuming a never-run
// bootstrap task still performs goal decomposition, but the next enqueue receives
// a new queued_at timestamp and therefore moves to the FIFO tail.
func (m *Manager) ApplyTaskPause(id string) error {
	return (taskStateCommand{manager: m, id: id}).pause()
}

// EnqueueTask persists the concurrency hold and syncs the in-memory handle.
func (m *Manager) EnqueueTask(id, mode string) error {
	return (taskStateCommand{manager: m, id: id}).enqueue(mode)
}

// DequeueTask removes the concurrency hold. clearMode=false is used when a user
// pauses a queued task so a later resume still knows whether bootstrap is needed.
func (m *Manager) DequeueTask(id string, clearMode bool) error {
	return (taskStateCommand{manager: m, id: id}).dequeue(clearMode)
}

// TaskStatus returns a task's current in-memory status (empty if unknown).
func (m *Manager) TaskStatus(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if t := m.tasks[id]; t != nil {
		return t.lifecycleSnapshot().Status
	}
	return ""
}

// BeginTaskRun persists the exact first-provider-call transition and mirrors its
// authoritative timestamps/status onto the live task handle. The DB statement
// also refuses paused/queued/terminal rows, making this safe to call at every
// provider attempt rather than only on the first one.
func (m *Manager) BeginTaskRun(id string) (int64, error) {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, err
	}
	m.mu.RLock()
	task := m.tasks[id]
	timeout := 0
	if task != nil {
		timeout = task.TimeoutSeconds
	}
	m.mu.RUnlock()
	if task == nil {
		return 0, fmt.Errorf("task %s not found", id)
	}
	firstRun, deadline, status, err := m.pg.BeginTaskRun(n, timeout)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("task %s is not eligible for an LLM call", id)
	}
	if err != nil {
		return 0, err
	}
	deadlineUnix := int64(0)
	if deadline != nil {
		deadlineUnix = deadline.Unix()
	}
	m.mu.Lock()
	if live := m.tasks[id]; live != nil {
		live.updateLifecycle(func(state *taskLifecycleState) {
			state.FirstRunAt = firstRun.Unix()
			state.DeadlineAt = deadlineUnix
			state.Status = status
		})
	}
	m.mu.Unlock()
	return deadlineUnix, nil
}

// SetTaskStatusGuarded sets a TERMINAL status only if the task is not paused or
// already terminal (first terminal writer wins). It reflects a won status on the
// live handle; won=false asks the caller to inspect the current lifecycle.
func (m *Manager) SetTaskStatusGuarded(id, status string) (won bool, err error) {
	return (taskStateCommand{manager: m, id: id}).guarded(status)
}

// SetTaskStatus persists a task's lifecycle status (e.g. "done") and reflects it
// on the in-memory handle so the derived DTO status shows it without a reload.
func (m *Manager) SetTaskStatus(id, status string) error {
	return (taskStateCommand{manager: m, id: id}).status(status)
}

func (m *Manager) cancelKatanaTask(taskID int64, reason string) {
	if m.katana == nil || taskID <= 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.katana.CancelTask(ctx, taskID, errors.New(reason)); err != nil {
			log.Printf("[katana] task %d cancellation incomplete: %v", taskID, err)
		}
	}()
}

func (m *Manager) cancelArsenalTask(taskID int64, reason string) {
	if m.arsenal == nil || taskID <= 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.arsenal.CancelTask(ctx, taskID, errors.New(reason)); err != nil {
			log.Printf("[arsenal] task %d cancellation incomplete: %v", taskID, err)
		}
	}()
}

// DeleteTask removes a task and optionally its related global data. Traffic has
// no task-id column, so related exchanges are resolved by exact hosts from the
// task's asset rows. Files are staged before the database operation; traffic is
// staged while PostgreSQL excludes asset/anchor writers. Both are restored on a
// database failure and purged only after its commit.
func (m *Manager) DeleteTask(id string, opts DeleteTaskOptions) (DeleteTaskResult, error) {
	return (taskDeletion{manager: m, id: id, options: opts}).run()
}

// taskDeleteCommittedError means PostgreSQL deletion succeeded but purging one
// of the recoverable staging directories failed. The task must stay deleted.
type taskDeleteCommittedError struct{ err error }

func (e *taskDeleteCommittedError) Error() string {
	return "task deletion committed; external cleanup incomplete: " + e.err.Error()
}

func (e *taskDeleteCommittedError) Unwrap() error { return e.err }

func rollbackTaskDelete(cause error, trafficStage *traffic.HostDeleteStage, fileStage *taskFileDeleteStage) error {
	return (taskDeleteRollback{cause: cause, traffic: trafficStage, files: fileStage}).run()
}

func (m *Manager) forgetTask(id string, numericID int64) {
	(taskDeleteRelease{manager: m, id: id, numericID: numericID}).run()
}

type stagedTaskPath struct {
	source string
	staged string
}

type taskFileDeleteStage struct {
	stageDir string
	moves    []stagedTaskPath
	deleted  bool
	done     bool
}

// stageTaskFiles atomically renames the task workspace and owned transcripts to
// a same-filesystem staging directory. The trailing dash in the transcript
// prefix is significant: exploration 12 must not match exploration 123.
func stageTaskFiles(dataDir, taskID string, explorationID int64) (*taskFileDeleteStage, error) {
	return (taskFilePlan{dataDir: dataDir, taskID: taskID, explorationID: explorationID}).prepare()
}

func (s *taskFileDeleteStage) commit() error {
	if s == nil || s.done {
		return nil
	}
	err := os.RemoveAll(s.stageDir)
	s.done = true
	return err
}

func (s *taskFileDeleteStage) rollback() error {
	if s == nil || s.done {
		return nil
	}
	errs := reversal.Paths(s.stageDir, s.moves, func(move stagedTaskPath) (string, string) { return move.source, move.staged }, 0o755, "task file")
	s.done = true
	return errors.Join(errs...)
}

// deleteTaskFiles retains the standalone helper contract used by focused tests.
func deleteTaskFiles(dataDir, taskID string, explorationID int64) (bool, error) {
	return (taskFilePlan{dataDir: dataDir, taskID: taskID, explorationID: explorationID}).remove()
}

func (m *Manager) Task(id string) (*Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	return t, ok
}

// ActiveTask returns the currently active task (or nil).
func (m *Manager) ActiveTask() *Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == "" {
		return nil
	}
	return m.tasks[m.active]
}

// SetActive switches the active task. Returns false if the id is unknown.
func (m *Manager) SetActive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[id]; !ok {
		return false
	}
	m.active = id
	return true
}

func (m *Manager) ResolveTask(id string) *Task {
	if id == "" || id == "active" {
		return m.ActiveTask()
	}
	t, _ := m.Task(id)
	return t
}

func (m *Manager) List() []*Task { return (taskCatalogView{manager: m}).list() }

// Notify signals that the asset/exploration graph changed (debounced consumer
// wakes the planner). Non-blocking.
func (t *Task) Notify() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

// NotifyDone is Notify plus a hint: a worker just finished intentID and that is
// what triggered this wake-up. The planner reads the accumulated triggers next
// round so it can spell out which intent finished (+ its output). Events pile up
// (debounce) until the round drains them via drainTriggers.
func (t *Task) NotifyDone(intentID int64) {
	if intentID > 0 {
		t.trigMu.Lock()
		t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "done", IntentID: intentID})
		t.trigMu.Unlock()
	}
	t.Notify()
}

// NotifyFinding records that a worker reported a finding on intentID (summary),
// then wakes the planner — so the round spells out which intent found what.
func (t *Task) NotifyFinding(intentID int64, summary string) {
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "finding", IntentID: intentID, Detail: summary})
	t.trigMu.Unlock()
	t.Notify()
}

// NotifyGoal records that one OR MORE goals were added in a single set_goals call —
// by the human via the main agent — then wakes the planner, so the next round spells
// out "the human added N goals: …" instead of the planner having to spot new open goals in
// the overview. One call → one trigger event (one set_goals batch counts as one, not spamming per item).
// The event survives an early-returning terminal round (drain happens after the gate),
// so a set_goals that revives a done task still surfaces it once the task is running.
func (t *Task) NotifyGoal(texts []string) {
	if len(texts) == 0 {
		return
	}
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "goal", Goals: texts})
	t.trigMu.Unlock()
	t.Notify()
}

// NotifyHint records that one OR MORE hints were added in a single add_hint call —
// by the human via the main agent, or by cross-task orchestration — then wakes the
// planner, so the next round is told "the human added N strategy hints: …" and looks at them
// directly instead of having to spot the new hint folded into the graph overview.
// One call → one trigger event (a batched add_hint counts as one, not one per hint).
func (t *Task) NotifyHint(texts []string) {
	if len(texts) == 0 {
		return
	}
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "hint", Hints: texts})
	t.trigMu.Unlock()
	t.Notify()
}

// NotifyGoalDeleted records that the human deleted a goal (via the overview's goal management), then
// wakes the planner so the next round spells out which goal was removed. The event
// survives an early-returning terminal round (drain happens after the gate).
func (t *Task) NotifyGoalDeleted(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "goal_deleted", Detail: text})
	t.trigMu.Unlock()
	t.Notify()
}

// NotifyGoalEdited records that the human edited a goal (via the overview's goal management), then wakes
// the planner so the next round spells out the old→new change. The event survives an
// early-returning terminal round (drain happens after the gate).
func (t *Task) NotifyGoalEdited(oldText, newText string) {
	oldText, newText = strings.TrimSpace(oldText), strings.TrimSpace(newText)
	if newText == "" {
		return
	}
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "goal_edited", OldGoal: oldText, NewGoal: newText})
	t.trigMu.Unlock()
	t.Notify()
}

// NotifyCancelled records that the human deleted intentID (reason = the deletion reason), then
// wakes the planner so the next round spells out which intent was removed and why.
// summary is the intent's text captured before deletion — needed for hard delete,
// where the node is gone by the time the planner reads the trigger. Applies to both
// soft (state='deleted') and hard (physical cascade) delete.
func (t *Task) NotifyCancelled(intentID int64, summary, reason string) {
	if intentID > 0 {
		t.trigMu.Lock()
		t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "cancelled", IntentID: intentID, Summary: summary, Detail: reason})
		t.trigMu.Unlock()
	}
	t.Notify()
}

// drainTriggers returns and clears the trigger events accumulated since the last round.
func (t *Task) drainTriggers() []agent.TriggerEvent {
	t.trigMu.Lock()
	defer t.trigMu.Unlock()
	ev := t.pendingTriggers
	t.pendingTriggers = nil
	return ev
}
