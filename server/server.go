package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/binaryintake"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/llmpool"

	"github.com/nobanic/norma/llm"
)

// BuildVersion is the backend application version, injected from cmd/uroboros at
// startup (which in turn gets it from -ldflags "-X main.version=<tag>").
// Defaults to "dev" for local builds. Exposed to the frontend via GET /api/health.
var BuildVersion = "dev"

// Server exposes the Uroboros backend over a JSON HTTP API for the shadcn/ui
// frontend.
type Server struct {
	updateGate managedUpdateGate
	m          *Manager
	engine     *Engine
	ctx        context.Context

	skillDir string // root directory for skill subdirectories
	jwtKey   []byte // HS256 signing key loaded from / generated into dataDir/jwt.key
	// authDisabled is an explicit local-runtime mode. It is never enabled by
	// default and must only be used behind a loopback-only listener.
	authDisabled bool

	// concMu serializes concurrency-cap decisions (admission + reconcile) so a
	// scheduler tick and an HTTP settings change / task creation can't both count
	// free slots off the same snapshot and over-promote past the limit.
	concMu sync.Mutex

	// httpRepeats is a task-wide circuit breaker for workers that converge on the
	// same failing method+URL. It is intentionally process-local: it prevents hot
	// loops within a run without turning historical failures into permanent policy.
	httpRepeatMu sync.Mutex
	httpRepeats  map[string]*httpRepeatEndpoint
	// clientBootstraps makes the first captured HTML/JavaScript response in each
	// worker direction trigger a deterministic contract-worklist pass. The value
	// is process-local like the repeat breaker; failed passes remove their key.
	clientBootstraps sync.Map

	cfgMu     sync.Mutex
	chatAgent *agent.ChatAgent // conversational runner for the chat page; nil w/o LLM
	// llmProv is the fully-decorated global provider (recorder + failover chain)
	// installed by applyLLM. Task routers use it after an explicit chain is cleared.
	llmProv   llm.Provider
	llmDirect llm.Provider // concrete global provider, before any failover pool
	llmCfg    agent.Config // current LLM config (key not exposed)
	llmOn     bool
	llmProf   string // active LLM profile name (for llmrec tagging)

	// chatBusy guards the per-task main-agent run: the chat handler launches the
	// agent on the server's background ctx (not the request ctx) and returns
	// immediately, so a page reload / proxy timeout can't abort a live run. This
	// map serializes turns per task — one main-agent run at a time per task, so
	// concurrent messages don't corrupt the shared exp<id>-main transcript.
	chatMu   sync.Mutex
	chatBusy map[string]bool
	// chatCancel holds the cancel func for each in-flight conversation run (keyed by
	// convBusyKey), so a manual stop can abort JUST that session's agent run. Set/
	// cleared alongside chatBusy under chatMu. Aborting a run does NOT touch the P3
	// trigger queue — the drain goroutine simply proceeds to the next queued fire.
	chatCancel map[string]context.CancelCauseFunc

	// triggerQ buffers P3 trigger fires PER AGENT. A per-agent "pump" launches runs up
	// to a concurrency limit derived from the agent's strategy: serial → limit 1 (+ optional
	// merge); parallel → limit = trigger_max_parallel (0=∞), no merge. triggerActive
	// counts in-flight runs per agent (replaces a boolean drain flag); a run's
	// completion decrements it and re-pumps to fill the freed slot. triggerCfg caches
	// the agent's last-read strategy so the pump never queries the DB while holding queueMu.
	// Distinct agents always run concurrently. Queue is in-memory (matches chatBusy); a
	// restart drops pending fires — the scheduler re-fires from watermarks next tick.
	queueMu       sync.Mutex
	triggerQ      map[string][]triggeredRun
	triggerActive map[string]int
	triggerCfg    map[string]triggerBehavior

	// profChatAgents caches a per-profile ChatAgent (chat page), keyed by profile id.
	// Built lazily on first use; invalidated when any profile is saved/activated/deleted
	// so edits take effect.
	profMu         sync.Mutex
	profChatAgents map[int64]*agent.ChatAgent // per-profile ChatAgent cache (chat page)

	// provByProfile caches ONE provider per LLM profile id so every agent bound or
	// pinned to the same profile shares a single provider instance — hence one rate
	// limiter. applyLLM also resolves the persisted global-active profile through this
	// cache, so global fallback and task chains do not accidentally double the
	// configured request rate. Cleared on profile edits, then repopulated by active reapply.
	provCacheMu   sync.Mutex
	provByProfile map[int64]*provEntry
	provCacheGen  uint64

	// llmHealth is the process-wide circuit-breaker state for LLM failover (round-robin).
	// It deliberately lives OUTSIDE the provider caches: rebuilding the chain
	// (saving an unrelated profile, flipping a setting) must not erase what we
	// learned about which backends are out of credit / rate-limited.
	llmHealth *llmpool.Registry
	// research is the host-owned, auditable web-search boundary. Unlike the SDK
	// auto-tool, it can enforce task disclosure grants and persist every attempt.
	research *researchBroker
	// vulnerability exposes fixed-provider, typed CVE/package intelligence through
	// the same operator/per-agent gate, budgets, disclosure grants and receipts.
	vulnerability *vulnerabilityBroker
	// torResearch is a read-only, seed-constrained fetch boundary. It owns SOCKS
	// authentication and never exposes proxy coordinates or an egress selector.
	torResearch *torResearchBroker

	// Explicit task chains use one task-scoped dynamic provider shared by goals,
	// planner, workers, and the main agent. Bundles are stable; their runtime reads
	// the persisted current profile at every new LLM call.
	taskAgentMu sync.Mutex
	taskAgents  map[string]*taskAgentBundle

	// Cold task archives run through one persistent FIFO worker. The buffered wake
	// channel coalesces enqueue bursts; the database remains the source of truth.
	archiveWake    chan struct{}
	archiveWG      sync.WaitGroup
	side           *sideQuestionState
	binaryIntake   *binaryintake.Service
	binaryRunner   *binaryintake.Runner
	binaryTrafficQ chan binaryintake.TrafficOrigin
}

type Option func(*Server)

func WithBinaryRuntime(intake *binaryintake.Service, runner *binaryintake.Runner) Option {
	return func(server *Server) {
		server.binaryIntake = intake
		server.binaryRunner = runner
	}
}

// WithAuthenticationDisabled removes the login/JWT boundary for a deliberately
// local, loopback-only runtime. Production callers must not enable this option.
func WithAuthenticationDisabled() Option {
	return func(server *Server) {
		server.authDisabled = true
	}
}

// provEntry is a cached provider + its config for one LLM profile id.
type provEntry struct {
	prov llm.Provider
	cfg  agent.Config
}

// triggeredRun is one queued P3 trigger fire awaiting its turn for an agent.
// taskID + mergeable let the drainer coalesce several event triggers (finding/goal)
// from the SAME task into one conversation before it starts (interval fires don't merge).
type triggeredRun struct {
	agentKey  string
	title     string
	message   string // event body (trigger phrase + tool/args/result, etc.); excludes the task description/goal header
	taskID    int64  // source task for finding/goal triggers; 0 for interval/none
	taskDesc  string // task description (task-level, identical within a task); rendered only once when merging
	taskGoal  string // task goal (task-level, identical within a task); rendered only once when merging
	mergeable bool   // true for finding/goal event triggers (merge by taskID)
}

func New(ctx context.Context, m *Manager, skillDir string, dataDir string, keyDir string, options ...Option) *Server {
	return (serverStartup{context: ctx, manager: m, skills: skillDir, data: dataDir, keys: keyDir, options: options}).start()
}

// Restored deadline and worker loops must inherit the same context as new tasks,
// including the side-question checkpoint publisher installed during startup.
func (s *Server) restoreTaskRuntimes() {
	m := s.m
	// reload tasks persisted on disk so the task list survives a restart, and
	// restore persisted paused state (so a task paused before restart stays paused).
	for _, t := range m.LoadExisting() {
		lifecycle := t.lifecycleSnapshot()
		// clear stale 'running' intents from a prior crash/restart (no live worker
		// owns them) so they re-claim instead of spinning forever in the UI.
		if n, _ := t.Store.ResetRunningIntents(); n > 0 {
			log.Printf("[engine] task %s reset %d stale running intents to open", t.ID, n)
		}
		if lifecycle.Paused {
			s.engine.Pause(t.ID, agent.AbortPausedOnReload)
		}
		// Task-level timeout: start a deadline coordinator for every non-terminal task that has a timeout, independent of the planner/worker
		// loop — so inactive tasks are wrapped up after their deadline once restarted (if the deadline has already passed, the wrap-up sequence runs immediately).
		if !isTerminalStatus(lifecycle.Status) {
			s.engine.startDeadlineCoordinator(s.ctx, t)
			if !lifecycle.Paused {
				s.ensureKatanaAuto(t.ID)
			}
		}
	}
	// Restore every task that had already been admitted before shutdown. Starting
	// only the active UI task left other non-queued tasks counted as concurrency
	// occupants without live loops, which could permanently block the persistent
	// FIFO. A paused task must re-enter through resume admission instead: a never-run
	// task still needs bootstrap goal decomposition before any planner loop exists.
	// Queued tasks are admitted below as slots allow.
	for _, t := range m.List() {
		lifecycle := t.lifecycleSnapshot()
		if !lifecycle.Paused && !lifecycle.Queued && !isTerminalStatus(lifecycle.Status) {
			s.engine.Run(s.ctx, t)
		}
	}
}

// agentMaxTurns returns the configured max_turns for an agent key (0 = unlimited,
// also the fallback when no DB or no row).
func (s *Server) agentMaxTurns(key string) int {
	if s.m.pg == nil {
		return 0
	}
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil {
		return 0
	}
	return a.MaxTurns
}

// agentRunSeconds returns the configured wall-clock run budget (seconds) for an
// agent key (0 = unlimited; 1200 fallback when no DB or no row, matching schema).
func (s *Server) agentRunSeconds(key string) int {
	if s.m.pg == nil {
		return 1200
	}
	a, err := s.m.pg.GetAgentByKey(key)
	if err != nil || a == nil {
		return 1200
	}
	return a.RunSecs
}

// loadLLMConfig reads the active LLM profile from PG (llm_profiles).
func (s *Server) loadLLMConfig() (agent.Config, bool) {
	return (profileCatalog{server: s}).active()
}

// saveLLMConfig persists the LLM config as the active "default" profile in PG.
func (s *Server) saveLLMConfig(cfg agent.Config) error {
	return (profileCatalog{server: s}).save(cfg)
}

// reapplyActiveProfile hot-reloads the engine from the active DB profile so that
// saving or activating a profile takes effect without a restart. Best-effort:
// logs on failure and leaves the running engine untouched.
func (s *Server) reapplyActiveProfile() {
	(profileCatalog{server: s}).reapply()
}

// webSearchFor gates the host-owned research broker by an agent's own web_search
// flag. Provider credentials and policy remain server-side and are never exposed
// through the model-visible tool schema.
func (s *Server) webSearchFor(key string) agent.WebSearchOpts {
	o := s.m.WebSearchOpts()
	if a, err := s.m.pg.GetAgentByKey(key); err != nil || a == nil || !a.WebSearch {
		o.Enabled = false
	}
	if o.Enabled && s.research != nil {
		o.Tool = s.research.Tool()
	}
	if o.Enabled && s.vulnerability != nil {
		o.Tools = s.vulnerability.Tools()
	}
	if o.Enabled && s.torResearch != nil {
		o.Tools = append(o.Tools, s.torResearch.Tool())
	}
	return o
}

func (s *Server) chatWebSearchOpts() agent.WebSearchOpts {
	o := s.m.WebSearchOpts()
	if o.Enabled && s.research != nil {
		o.Tool = s.research.Tool()
	}
	if o.Enabled && s.vulnerability != nil {
		o.Tools = s.vulnerability.Tools()
	}
	if o.Enabled && s.torResearch != nil {
		o.Tools = append(o.Tools, s.torResearch.Tool())
	}
	return o
}

// nonStreamingResolver returns a resolver capturing a profile's streaming choice.
// The agents read it per run; a profile change rebuilds the agents (applyLLM),
// so the captured value is always the one in effect for this build.
func nonStreamingResolver(cfg agent.Config) func() bool {
	return (profileReply{configuration: cfg}).nonStreaming()
}

// maxTokensResolver mirrors nonStreamingResolver for the per-reply output cap.
func maxTokensResolver(cfg agent.Config) func() int {
	return (profileReply{configuration: cfg}).maxTokens()
}

// applyLLM (re)builds the planner/worker/main-agent from cfg and installs them on
// the running engine as the GLOBAL active pair. Safe to call at runtime (UI configures LLM).
func (s *Server) applyLLM(cfg agent.Config) error {
	return (profileActivation{server: s}).apply(cfg)
}

// loadProfileConfig builds an agent.Config from a specific profile id (with its key).
// ok=false when the profile is missing or has no api key.
func (s *Server) loadProfileConfig(id int64) (agent.Config, bool) {
	return (profileCatalog{server: s}).byID(id)
}

// effectiveProfileForAgent resolves the LLM profile id an agent should run on, by
// precedence: agent binding (agents.llm_profile_id) → pin (task/conversation) → nil
// (caller falls back to the global active profile). A binding to a deleted profile
// can't happen (FK ON DELETE SET NULL); an otherwise-invalid one is dropped downstream
// by loadProfileConfig, letting the caller fall back.
func (s *Server) effectiveProfileForAgent(agentKey string, pinID *int64) *int64 {
	return (profileChat{server: s}).effective(agentKey, pinID)
}

// resolveChatAgent picks the ChatAgent for one conversation: the agent's own
// binding or this conversation's chosen profile first, the global active config
// only as a fallback. Both the send precheck and the background runner MUST use
// this — resolving differently in the two paths is how a conversation that had
// picked a valid profile still got rejected with "LLM not configured" when no global
// config was active.
func (s *Server) resolveChatAgent(c *db.Conversation) *agent.ChatAgent {
	return (profileChat{server: s}).resolve(c)
}

// chatUnavailableReason explains why no ChatAgent could be resolved, so the user
// knows whether to add a config, activate one, or pick one for this conversation
// — rather than a flat "not configured" that hides which of those it is.
func (s *Server) chatUnavailableReason() string {
	return (profileChat{server: s}).unavailable()
}

// providerForProfile returns a cached provider+cfg for a profile id, so every agent
// bound/pinned to the same profile shares one provider instance (one rate limiter).
// ok=false when the profile is missing/invalid → caller falls back to the global pair.
func (s *Server) providerForProfile(id int64) (llm.Provider, agent.Config, bool) {
	return (profileProvider{server: s, id: id}).resolve()
}

// chatAgentForProfile returns a ChatAgent built from a specific LLM profile, cached
// per profile id. Returns nil if the profile is missing or has no API key.
func (s *Server) chatAgentForProfile(id int64) *agent.ChatAgent {
	return (profileChat{server: s}).cached(id)
}

// invalidateProfileAgents drops the per-profile ChatAgent + provider caches so a profile
// save/activate/delete — or an agent's binding change — rebuilds pinned tasks' agents
// (and re-resolves each agent's bound model) on their next round.
func (s *Server) invalidateProfileAgents() {
	(profileCatalog{server: s}).invalidate()
}

func (s *Server) chatAgentRef() *agent.ChatAgent {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.chatAgent
}

func (s *Server) Handler() http.Handler { return (httpApplication{server: s}).handler() }

// --- handlers ---

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "service": "uroboros", "version": BuildVersion, "source_revision": sourceRevision(), "auth_disabled": s.authDisabled})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).stats())
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).listTasks())
}

func (s *Server) applyTaskMetrics(dto *TaskDTO, t *Task, metric db.TaskListMetrics) {
	if dto == nil || t == nil {
		return
	}
	dto.Tokens = tokenTotalDTO(metric.Tokens)
	// Prefer the live in-memory heartbeat (fresher) and fall back to the
	// persisted max activity time (survives restarts) for run-duration display.
	dto.LastActivity = metric.LastActivity
	if s.engine != nil {
		if live := s.engine.LastActivity(t.ID); live > dto.LastActivity {
			dto.LastActivity = live
		}
	}
	dto.GoalsTotal = metric.Goals.Total
	dto.GoalsMet = metric.Goals.Met
	dto.InFlight = metric.RunningIntents
	dto.Findings = FindingSeverityDTO{
		Critical: metric.Findings.Critical,
		High:     metric.Findings.High,
		Medium:   metric.Findings.Medium,
		Low:      metric.Findings.Low,
	}
}

func (s *Server) resolvedTaskStatus(t *Task) string {
	if t == nil {
		return "created"
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Queued:
		return "queued"
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case lifecycle.Status == "running":
		return "running"
	case t.llmStateSnapshot().FailoverState == "chain_exhausted" && s.engine.Started(t.ID):
		// LLM readiness is an execution dependency, not lifecycle state. Keeping
		// an exhausted task running lets the user edit/reset its chain directly.
		return "running"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	default:
		return "created"
	}
}

func (s *Server) setActive(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).active()
}

// control pauses/resumes a task's autonomous execution (planner + workers).
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).control()
}

// controlIntent pauses, resumes, or cancels one local worker intent. A running
// worker is always stopped before state mutation/cleanup, preventing tool output
// that arrives after the user action from recreating deleted blackboard records.
func (s *Server) controlIntent(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).intent()
}

// rerunIntent reruns an intent that did not complete successfully (blocked/exhausted/stopped): it is set back to open, and the worker
// re-claims it and reruns it from scratch (facts/findings/assets already written back to the graph are kept); if the task is terminal/paused it is revived as well.
// Used for "click to continue a failed work" — after being blocked by network/LLM jitter, it can be retried with one click.
func restoreRerunIntent(t *Task, before *db.Node) error {
	if t == nil || before == nil {
		return fmt.Errorf("missing intent rollback snapshot")
	}
	if before.State == "blocked" && before.BlockedReason != "" {
		return t.Store.SetIntentBlockedReason(before.ID, before.BlockedReason)
	}
	return t.Store.SetIntentState(before.ID, before.State)
}

func (s *Server) rerunIntent(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).rerun(true)
}

// rerunBlocked bulk-reruns all blocked intents for this task (useful when a single network/LLM disconnection blocked several, so they can
// all be retried at once), sets them back to open and revives the task; returns the number reopened.
func (s *Server) rerunBlocked(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).rerun(false)
}

// getLLM returns the current LLM config (key never exposed).
func (s *Server) getLLM(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).current()
}

// setLLM configures the LLM at runtime. A blank api_key keeps the existing key.
func (s *Server) setLLM(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).configure()
}

// testLLM makes a real minimal completion to verify the config works.
func (s *Server) testLLM(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).testConnection()
}

// truncateReply clips a connection-test reply for display. A model told to answer
// "OK" can still ramble (or think out loud); the UI only needs enough to show it
// really said something. Rune-based so multibyte text never splits mid-character.
func truncateReply(s string) string {
	const max = 200
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

type createTaskReq struct {
	Name                 string   `json:"name,omitempty"` // optional task name; omitted/empty = unnamed
	CategoryID           *int64   `json:"category_id,omitempty"`
	Description          string   `json:"description"`
	Goal                 string   `json:"goal"`
	LLMProfileID         *int64   `json:"llm_profile_id,omitempty"`    // the LLM configuration to run this task; omitted/null = use the active configuration
	LLMProfileIDs        []int64  `json:"llm_profile_ids,omitempty"`   // ordered task-level configuration chain; the first item takes effect initially
	SourceTaskIDs        []string `json:"source_task_ids,omitempty"`   // source tasks inherited directly and read-only
	CompanyIDs           []int64  `json:"company_ids,omitempty"`       // associates the company scope and snapshots the current company's assets; does not copy assets or force intent generation
	TimeoutSeconds       int      `json:"timeout_seconds"`             // task-level timeout (seconds); 0/omitted = no limit
	PlanHeartbeatSeconds int      `json:"plan_heartbeat_seconds"`      // planner heartbeat trigger interval (seconds); 0/omitted = default 600 (10min); floor = default = 600, lower values are raised to 600
	SeedFirstIntent      *bool    `json:"seed_first_intent,omitempty"` // on creation, immediately issue one seed intent (content = description + goal) so the worker can start running without waiting for the first planner round; omitted/null = off by default, following the standard plan-then-execute flow. Only an explicit true enables it (for CTFs often solved in one work, the planner round before startup can be skipped).
	CoverageEnabled      *bool    `json:"coverage_enabled,omitempty"`  // asset coverage feature; omitted/null = on by default (true). false = disables coverage computation/display/automatic scope accumulation + hides add_task_scope/list_untested_assets. Company association is unaffected.
	StrictIsolation      bool     `json:"strict_isolation,omitempty"`  // exclude assets discovered by unrelated tasks; incompatible with source_task_ids
	DeferStart           *bool    `json:"defer_start,omitempty"`       // operator/harness gate: seed the task and leave it paused until explicit resume; no model call starts before external preflight completes
	SkipInitialKatana    bool     `json:"skip_initial_katana"`         // suppress infrastructure crawl, not explicit agent tool calls
	InitialScout         *bool    `json:"initial_scout"`               // omitted/null follows workspace default
	// InterceptRules task-level asset interception/allow rules (entered at creation, stored in task_intercept_rules, not in the global table).
	InterceptRules []taskInterceptRuleReq `json:"intercept_rules,omitempty"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).create()
}

func (s *Server) validateTaskProfileIDs(ids []int64) error {
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return fmt.Errorf("LLM configuration id is invalid or duplicated")
		}
		seen[id] = true
		if _, ok := s.loadProfileConfig(id); !ok {
			return fmt.Errorf("LLM configuration #%d does not exist or has no API Key set", id)
		}
	}
	return nil
}

func (s *Server) updateTaskLLMProfiles(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).profiles()
}

var (
	// Exclude common prose/Markdown delimiters so a target written as
	// `http://host:port` is parsed as the URL, not with the trailing backtick as
	// part of its port. The previous expression made generated briefs fail to
	// seed any task scope.
	reURL    = regexp.MustCompile("(?i)https?://[^\\s'\"`<>\\[\\]{}()]+")
	reIPPort = regexp.MustCompile(`\b((?:\d{1,3}\.){3}\d{1,3})(?::(\d{1,5}))?`)
	reDomain = regexp.MustCompile(`\b((?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,})(?::(\d{1,5}))?`)
)

// parseTarget extracts a target (scheme, host, port) from free text, supporting
// full URLs, IP[:port] and domain[:port]. ok=false when nothing parseable.
func parseTarget(text string) (scheme, host string, port int, ok bool) {
	text = strings.TrimSpace(text)
	if m := reURL.FindString(text); m != "" {
		if u, err := url.Parse(m); err == nil && u.Hostname() != "" {
			scheme = strings.ToLower(u.Scheme)
			host = strings.ToLower(u.Hostname())
			port = portOr(u.Port(), defaultPort(scheme))
			return scheme, host, port, true
		}
	}
	if m := reIPPort.FindStringSubmatch(text); m != nil {
		host = m[1]
		port = portOr(m[2], 80)
		return schemeForPort(port), host, port, true
	}
	if m := reDomain.FindStringSubmatch(text); m != nil {
		host = strings.ToLower(m[1])
		if m[2] == "" {
			return "https", host, 443, true
		}
		port = portOr(m[2], 443)
		return schemeForPort(port), host, port, true
	}
	return "", "", 0, false
}

func portOr(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return d
}
func defaultPort(scheme string) int {
	if scheme == "http" {
		return 80
	}
	return 443
}
func schemeForPort(p int) string {
	if p == 443 || p == 8443 {
		return "https"
	}
	return "http"
}

// llmHost returns the host of the configured LLM endpoint (to keep it out of scope).
func (s *Server) llmHost() string {
	return (profileActivation{server: s}).host()
}

func (s *Server) seed(t *Task, text string) {
	scheme, host, port, ok := parseTarget(text)
	if !ok {
		log.Printf("[seed] task %s: could not parse a target host/IP from %q, not creating a site (please configure scope manually)", t.ID, text)
		return
	}
	// P0-1 guard: never treat the configured LLM gateway as a target.
	if gw := s.llmHost(); gw != "" && host == gw {
		log.Printf("[seed] task %s: target %q is an LLM gateway, refusing to use it as a penetration target", t.ID, host)
		return
	}

	u := scheme + "://" + host
	if !(scheme == "https" && port == 443) && !(scheme == "http" && port == 80) {
		u += ":" + strconv.Itoa(port)
	}
	var rootID int64
	if as := s.m.Assets(); as != nil {
		taskID, _ := strconv.ParseInt(t.ID, 10, 64)
		if net.ParseIP(host) != nil {
			rootID, _ = as.UpsertIP(db.UpsertIPReq{IP: host, TaskID: taskID})
		} else if scheme == "https" || scheme == "http" {
			rootID, _ = as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, TaskID: taskID})
		} else {
			rootID, _ = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: taskID})
		}
		if rootID > 0 {
			_ = as.SetTaskAssetSource(taskID, rootID, "task", "initialized from the task description or goal", nil)
		}
		// Seed the same conservative authorization edge synchronously, before the
		// engine or a seed intent can invoke an active request tool. Previously the
		// initial target was an asset but did not enter task_scope until an agent
		// happened to register it later.
		scopeIP := ""
		if net.ParseIP(host) != nil {
			scopeIP = host
		}
		if err := as.AddAutoScope(taskID, "service", host, u, scopeIP); err != nil {
			log.Printf("[seed] task %s: could not register initial target scope: %v", t.ID, err)
		}
	}
	// anchor the seeded assets to this task's begin root as lineage/provenance
	// (the asset graph is global and shared; anchoring no longer gates reads).
	if rootID > 0 {
		if begin, _ := t.Store.OriginFactID(); begin > 0 {
			_ = t.Store.Anchor(begin, rootID)
		}
	}
	log.Printf("[seed] task %s: target site %s", t.ID, u)
	// Do not Notify here: whether the first round fires is decided solely by engine.Run's HasActiveIntent (seed-intent tasks
	// skip the first round). seed runs before Run, so a Notify here would be buffered to the channel and consumed when plannerLoop starts,
	// bypassing Run's gating → the seed task would still wrongly trigger the first round.
}

// seedFirstIntent writes ONE open intent (summary = description + goal) into the task's
// frontier at creation, so a worker can claim and run it immediately without first
// waiting a planner round. Mirrors a planner top-level intent: it links from the
// origin fact (RelDerivedFrom) so it still traces back to a fact node. Best-effort —
// a failure just falls back to the normal planner-driven flow.
func (s *Server) seedFirstIntent(t *Task) {
	summary := fmt.Sprintf("Complete the task goal: %s (task: %s)", t.Goal, t.Description)
	id, err := t.Store.AddIntent(map[string]any{"summary": summary}, 8, nil, "seed")
	if err != nil {
		log.Printf("[seed] task %s: failed to issue seed intent: %v", t.ID, err)
		return
	}
	if origin, _ := t.Store.OriginFactID(); origin > 0 {
		_ = t.Store.Link(origin, db.RelDerivedFrom, id)
	}
	log.Printf("[seed] task %s: issued seed intent #%d", t.ID, id)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).getTask())
}

// taskCoverage returns a task's rough asset test coverage (denominator/tested/backlog).
func (s *Server) taskCoverage(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).taskCoverage())
}

// taskCoverageGraph returns the force-directed asset coverage graph for a task:
// all in-scope assets (of each type) + the root domain/company nodes used for connections, each carrying tested/in_scope.
func (s *Server) taskCoverageGraph(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).taskCoverageGraph())
}

// taskAssetRefs returns the intents / facts / findings in this task anchored to a
// given asset id — powers the coverage-graph node drawer's "related intents / related facts".
func (s *Server) taskAssetRefs(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).taskAssetRefs())
}

// taskScopeList returns a task's scope rows (coverage denominator sources).
func (s *Server) taskScopeList(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).taskScopeList())
}

func (s *Server) taskScopeAdd(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).scope(false)
}

func (s *Server) taskScopeDelete(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).scope(true)
}

func (s *Server) frontier(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).frontier())
}

func (s *Server) findings(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).list())
}

// normFilter maps the frontend's "all" sentinel (and empty) to "" so the DB layer
// treats it as no filter.
func normFilter(v string) string {
	if v == "all" {
		return ""
	}
	return v
}

// resolveFindingAssets loads every asset anchored by the given findings, keyed by
// id, so each finding DTO can render its assets' labels.
func (s *Server) resolveFindingAssets(fs []*db.DBFinding) map[int64]*db.Asset {
	var ids []int64
	for _, f := range fs {
		ids = append(ids, f.AssetIDs...)
	}
	return s.resolveAssetIDs(ids)
}

// resolveAssetIDs de-dupes ids and loads their asset rows into an id→asset map.
func (s *Server) resolveAssetIDs(ids []int64) map[int64]*db.Asset {
	assets := map[int64]*db.Asset{}
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return assets
	}
	list, err := s.m.pg.Assets().GetByIDs(uniq)
	if err != nil {
		log.Printf("[findings] resolve assets: %v", err)
	}
	for _, a := range list {
		assets[a.ID] = a
	}
	return assets
}

// findingStats serves the whole-table aggregates (stat cards + vuln-class filter)
// for the paginated Findings page.
func (s *Server) findingStats(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).stats())
}

// getFinding returns one finding by its standalone-table id (DTO finding_id),
// with anchored assets resolved for display.
func (s *Server) getFinding(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).detail())
}

// findingsExport exports the vulnerabilities from the Findings page.
//
//	scope   = filtered (uses the page's filters) | all (everything) | selected (checked ids)
//	format  = md-single (one combined .md) | md-zip (one .md per vulnerability, packed into a zip)
//	          | csv | json
//	ids     = comma-separated finding ids (required when scope=selected)
//	filter params severity/status/vulnclass/task_id/q/sort match the list endpoint (used when scope=filtered).
func (s *Server) findingsExport(w http.ResponseWriter, r *http.Request) {
	(reportExportRequest{server: s, response: w, request: r}).findings()
}

func findingProvenanceInTask(contextTask *Task, findingTaskID *int64) (sourceTaskID string, inherited, allowed bool) {
	if contextTask == nil || findingTaskID == nil {
		return "", false, false
	}
	contextID, err := strconv.ParseInt(contextTask.ID, 10, 64)
	if err != nil {
		return "", false, false
	}
	if *findingTaskID == contextID {
		return "", false, true
	}
	for _, sourceID := range contextTask.lifecycleSnapshot().SourceTaskIDs {
		if sourceID == *findingTaskID {
			return i64s(sourceID), true, true
		}
	}
	return "", false, false
}

// findingLineage returns the exploration sub-DAG from the task root to this
// finding's node — the finding node + all its ancestors + edges among them — so
// the detail page can show "how this finding was reached". Empty {nodes,edges}
// when the finding has no node/task (e.g. the originating task was deleted).
func (s *Server) findingLineage(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).lineage())
}

// patchFinding partially updates a finding: any subset of {status, severity}.
// The id is the standalone findings-table id (DTO finding_id). Severity edits are
// mirrored onto the originating exploration node so the per-task view stays in
// sync. Returns the updated finding DTO.
func (s *Server) patchFinding(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).patch())
}

// deleteFinding removes a finding (findings row + originating exploration node).
// id is the standalone findings-table id (DTO finding_id).
func (s *Server) deleteFinding(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).delete())
}

func (s *Server) intents(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).intents())
}

func inheritedIntentResult(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// inheritedGraphSnapshot exposes immutable source-task history without leaking
// work that is still open or running. Edges are filtered with the nodes so the
// response cannot contain dangling ids that reveal hidden live intent topology.
func inheritedGraphSnapshot(nodes []*db.Node, edges []db.Edge, sourceTaskID int64) ([]*db.Node, []db.Edge) {
	visible := make(map[int64]struct{}, len(nodes))
	filteredNodes := make([]*db.Node, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || (node.Kind == db.KindIntent && !inheritedIntentResult(node.State)) {
			continue
		}
		node.SourceTaskID = sourceTaskID
		node.Inherited = true
		visible[node.ID] = struct{}{}
		filteredNodes = append(filteredNodes, node)
	}
	filteredEdges := make([]db.Edge, 0, len(edges))
	for _, edge := range edges {
		if _, ok := visible[edge.From]; !ok {
			continue
		}
		if _, ok := visible[edge.To]; !ok {
			continue
		}
		filteredEdges = append(filteredEdges, edge)
	}
	return filteredNodes, filteredEdges
}

// taskIntentHistoryPage returns one newest-first page for an exploration.
// A source may have many open/running intents, so keep paging until enough
// historical results are collected instead of leaking its frontier into the UI.
func taskIntentHistoryPage(store *db.ExplorationStore, before int64, limit int, inherited bool, sourceTaskID int64) ([]*db.Node, bool, error) {
	if !inherited {
		return store.ListByKindPage(db.KindIntent, before, limit)
	}
	batch := max(limit, 300)
	cursor := before
	out := make([]*db.Node, 0, limit+1)
	for {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil {
			return nil, false, err
		}
		for _, node := range page {
			if !inheritedIntentResult(node.State) {
				continue
			}
			node.SourceTaskID = sourceTaskID
			node.Inherited = true
			out = append(out, node)
			if len(out) > limit {
				return out[:limit], true, nil
			}
		}
		if !more || len(page) == 0 {
			return out, false, nil
		}
		cursor = page[len(page)-1].ID
	}
}

// explorationGraph returns the whole exploration chain (task graph) as nodes+edges.
func (s *Server) explorationGraph(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).explorationGraph())
}

// explorationNodes serves the broadcast board: this task's own exploration nodes as a
// paged time series (newest first unless ?order=asc), filterable by kind/state
// and a payload substring. Inherited nodes are deliberately out of scope — the
// board reports what this task is doing right now, and paging across the source
// tasks' stores would make the cursor meaningless.
// Response also carries the edges touching the page plus the neighbour nodes
// they point at, so each row can state where it came from and what it produced.
func (s *Server) explorationNodes(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).explorationNodes())
}

// nodeAnchoredAssets resolves the exploration_anchors of the given nodes into
// display-ready asset labels, keyed by node id. Anchors are provenance decoration
// for the broadcast board — a failure here must not cost the caller its page, so errors are
// logged and degrade to "no assets".
func (s *Server) nodeAnchoredAssets(t *Task, nodeIDs []int64) map[string][]FindingAssetDTO {
	out := map[string][]FindingAssetDTO{}
	anchors, err := t.Store.NodeAssets(nodeIDs)
	if err != nil {
		log.Printf("[broadcast] node assets: %v", err)
		return out
	}
	var flat []int64
	for _, ids := range anchors {
		flat = append(flat, ids...)
	}
	assets := s.resolveAssetIDs(flat)
	for nodeID, ids := range anchors {
		if dtos := findingAssetDTOs(ids, assets); len(dtos) > 0 {
			out[i64s(nodeID)] = dtos
		}
	}
	return out
}

// csvValues splits a comma-separated query parameter, dropping empty entries.
func csvValues(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// activity returns the worker execution step log (incremental via ?since=seq).
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).activity())
}

// parseActivitySession maps a stable session key (main | plan | intent:<ID>) to a
// DB session filter. Goal Agent + Planner both live under worker="planner" (the
// single Plan session); a Worker session is one intent, keyed by its node id.
func parseActivitySession(sess string) (db.ActivitySessionFilter, bool) {
	switch {
	case sess == "" || sess == "main":
		// bare "main" = the current segment; caller resolves MainSeg via the store.
		return db.ActivitySessionFilter{Main: true}, true
	case strings.HasPrefix(sess, "main:"):
		seg, err := strconv.Atoi(strings.TrimPrefix(sess, "main:"))
		if err != nil || seg < 0 {
			return db.ActivitySessionFilter{}, false
		}
		return db.ActivitySessionFilter{Main: true, MainSeg: &seg}, true
	case sess == "plan":
		return db.ActivitySessionFilter{Worker: "planner"}, true
	case strings.HasPrefix(sess, "intent:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(sess, "intent:"), 10, 64)
		if err != nil {
			return db.ActivitySessionFilter{}, false
		}
		return db.ActivitySessionFilter{NodeID: &id}, true
	}
	return db.ActivitySessionFilter{}, false
}

// activitySessionStore resolves a worker session against the current task and
// its direct sources. Planner/main always stay local; inherited intent sessions
// are immutable history and use the source exploration only for reads.
func activitySessionStore(t *Task, filter db.ActivitySessionFilter) (*db.ExplorationStore, int64, error) {
	if filter.NodeID == nil {
		return t.Store, 0, nil
	}
	node, err := t.Store.GetNodeWithSources(*filter.NodeID)
	if err != nil {
		return nil, 0, err
	}
	if node == nil || node.Kind != db.KindIntent {
		return nil, 0, nil
	}
	if !node.Inherited {
		return t.Store, 0, nil
	}
	if !inheritedIntentResult(node.State) {
		return nil, 0, nil
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		return nil, 0, err
	}
	for _, source := range sources {
		if source.Task.TaskID == node.SourceTaskID {
			return source.Store, source.Task.TaskID, nil
		}
	}
	return nil, 0, nil
}

// activityHistory serves one reverse-paginated page of a session's activity history.
// The latest page (no ?before) opens a session; ?before=<id> pulls the older page on
// scroll-up. snapshot_cursor is the TASK-level max id at query time — the client uses
// it to open the single task SSE at since=snapshot_cursor so history (id<=cursor) and
// the live tail (id>cursor) meet with no gap and no overlap.
func (s *Server) activityHistory(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).activityHistory())
}

// tokenStats returns per-worker token usage (input/output/cache read/write) for a
// task — main agent, planner, and each work#N.
func (s *Server) tokenStats(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).tokenStats())
}

// tokenDailyStats returns global token consumption aggregated by calendar day
// (UTC) across all tasks for the past ?days=N days (default 30).
func (s *Server) tokenDailyStats(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).tokenDailyStats())
}

// conversationTokens returns per-conversation token summaries so the dashboard can
// merge chat (conversation) usage into its per-profile / daily token stats — which
// otherwise count only task (exploration) usage.
func (s *Server) conversationTokens(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).conversationTokens())
}

// streamActivity is the live SSE tail: it replays history after ?since=<seq>, then
// pushes each newly appended activity for the task. The seq cursor makes history +
// live join gap-free; on reconnect the client passes its last seq to catch any
// dropped events. Optional ?intent=<id> scopes the stream to one worker session.
// getLogs returns recent backend log lines (Seq > since), newest-last.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	limit := atoiDefault(r.URL.Query().Get("limit"), 500)
	lines, cursor := logSink.recent(since, limit)
	writeJSON(w, 200, map[string]any{"items": lines, "cursor": cursor})
}

// getLogsHistory returns older log lines from the DB (before a given db_id).
// GET /api/logs/history?before=<db_id>&limit=200
// Returns {items:[LogLine], has_more: bool}.
func (s *Server) getLogsHistory(w http.ResponseWriter, r *http.Request) {
	(backendLogHistory{database: s.m.pg}).serve(w, r)
}

// streamLogs is the live SSE tail of the backend log: replays history after
// ?since=<seq>, then pushes each new line.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	(backendLogStream{writer: w, request: r}).serve()
}

func (s *Server) streamActivity(w http.ResponseWriter, r *http.Request) {
	(taskActivityStream{server: s, writer: w, request: r}).serve()
}

// activityDetail lazily returns the full detail blob for one step.
func (s *Server) activityDetail(w http.ResponseWriter, r *http.Request) {
	writeInspection(w, s.readInspection(r).activityDetail())
}

// taskActivityDetail keeps the legacy local-task behavior (including thinking
// rows), while inherited details are restricted to terminal worker intents.
// This prevents a guessed global activity id from exposing source planner/main
// or in-flight transcripts.
func taskActivityDetail(t *Task, seq int64) (string, error) {
	return t.Store.ActivityDetailWithSources(seq)
}

func (s *Server) getTraffic(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).list()
}

// getTrafficHosts returns distinct recorded hosts with counts, for the page's
// target picker (pick a host → filter the list, then delete it).
func (s *Server) getTrafficHosts(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).hosts()
}

// deleteTraffic removes recorded traffic for every host containing the query's
// host substring (the page's host filter is substring-based, so what you
// filtered is what gets deleted): index rows + each host's file tree, then
// garbage-collects blobs no remaining exchange references. Empty host → 400.
// Returns the number of exchanges deleted.
func (s *Server) deleteTraffic(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).deleteHost()
}

// deleteTrafficHosts removes traffic for a set of EXACT hosts (JSON body
// {"hosts": [...]}) — the batch path for the page's multi-select delete. Exact
// match, so picking "api.example.com" never sweeps "api.example.com.cn".
// Returns the number of exchanges deleted.
func (s *Server) deleteTrafficHosts(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).deleteHosts()
}

// deleteAllTraffic purges every recorded exchange, then compacts the index so
// the space is actually returned to the filesystem — an emptied index is the one
// moment a full rewrite is cheap. Evidence already bound to a finding lives in
// the evidence store and is deliberately left alone. Returns the number of
// exchanges deleted and the bytes of index reclaimed.
func (s *Server) deleteAllTraffic(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).deleteAll()
}

// getTrafficExchange returns the full raw request/response of one exchange,
// read on demand from the traffic tree (bodies are not in the paged list).
func (s *Server) getTrafficExchange(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).exchange()
}

// getTrafficBlob streams one oversized body by its sha256. Bodies past the inline
// threshold are not carried by the exchange endpoint — it returns a preview plus
// an "@blob sha256:<hash>" pointer — so this is how the UI fetches them whole.
// Streamed rather than buffered: these are the bodies too large to hold in memory.
func (s *Server) getTrafficBlob(w http.ResponseWriter, r *http.Request) {
	s.trafficRequest(w, r).blob()
}

// getSettings returns the runtime app settings the UI toggles. The Brave API key
// is returned as a boolean presence flag (brave_key_set), never the value itself,
// so the UI can show "configured" without echoing the secret back.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.settingsPayload())
}

func (s *Server) settingsPayload() map[string]any {
	on, backend, braveKey, tavilyKey, proxy := s.m.WebSearch()
	researchConfig := s.m.ResearchRuntimeConfig()
	_, torSOCKSConfiguredErr := parseTorSOCKSEndpoint(researchConfig.TorSOCKSURL)
	pyStored, _, _ := s.m.pg.GetSetting(settingPythonInterp)
	concOn, concLimit := s.m.ConcurrencyLimit()
	if concLimit == 0 {
		concLimit = defaultConcurrencyLimit // echo a sensible default to the UI even when disabled
	}
	return map[string]any{
		"traffic_capture":                     s.m.TrafficEnabled(),
		"agent_traffic_binding":               s.m.pg.GetBool(settingAgentTrafficBinding, false),
		"llm_record":                          s.m.LLMRecordEnabled(),
		"web_search_enabled":                  on,
		"web_search_backend":                  backend,
		"brave_key_set":                       strings.TrimSpace(braveKey) != "",
		"tavily_key_set":                      strings.TrimSpace(tavilyKey) != "",
		"web_search_proxy":                    proxy, // dedicated egress proxy (http/https/socks5), empty = direct connection
		"web_search_fallback_backend":         researchConfig.Fallback,
		"web_search_task_request_budget":      researchConfig.TaskBudget,
		"web_search_task_paid_request_budget": researchConfig.PaidRequestBudget,
		"web_search_rate_per_minute":          researchConfig.RatePerMinute,
		"web_search_cache_ttl_seconds":        int(researchConfig.CacheTTL / time.Second),
		"web_search_audit_mode":               "host_broker_v1",
		"tor_research_enabled":                researchConfig.TorEnabled,
		"tor_egress_profile_id":               db.TorResearchProfileID,
		"tor_socks_configured":                torSOCKSConfiguredErr == nil,
		"tor_proxy_coordinates_exposed":       false,
		"global_proxy":                        s.m.GlobalProxy(),           // global egress proxy (http/https/socks5), all target traffic goes through it, empty = direct connection
		"python_interpreter":                  strings.TrimSpace(pyStored), // user/auto-set value (empty = use runtime detection)
		"workers":                             s.m.Workers(),               // number of concurrent worker agents (default 3); applies to tasks started afterward
		"task_concurrency_enabled":            concOn,                      // task concurrency-cap toggle (off by default)
		"task_concurrency_limit":              concLimit,                   // max simultaneously running tasks (default 5 once enabled)
		// LLM round-robin (failover). Off by default; once enabled, agents using the global active configuration switch
		// automatically to the next configuration when the current one is unavailable. bind_fallback is only meaningful when round-robin is on (off by default).
		"llm_pool_enabled":       s.m.LLMPoolEnabled(),
		"llm_pool_bind_fallback": s.m.LLMPoolBindFallback(),
		// Operation-constraint injection scope (all on by default): splices this task's allow/deny constraints into the corresponding agent's system prompt.
		"constraints_inject_planner": s.constraintInjectPlanner(),
		"constraints_inject_worker":  s.constraintInjectWorker(),
		// Experimental feature: noa model-driven context compaction (off by default). Once enabled, the four kinds of platform-integrated agents have noa
		// take over context compaction, replacing the built-in compaction; read once per run, applying to runs started afterward.
		"noa_compaction":        s.m.NoaCompactionEnabled(),
		"early_bootstrap_scout": s.m.EarlyBootstrapScoutEnabled(),
		// Global settings for vulnerability IM push. Channels themselves are standalone resources, managed via /api/notify/*;
		// only the three settings that "apply to all channels" are placed here.
		"notify_enabled":             s.m.pg.GetBool(settingNotifyEnabled, true),
		"notify_public_base_url":     notifyPublicBaseURL(s.m.pg),
		"notify_digest_interval_min": notifyDigestIntervalMin(s.m.pg),
	}
}

// notifyPublicBaseURL reads the external address used for push callback links.
func notifyPublicBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return v
}

// notifyDigestIntervalMin reads the digest interval (minutes), falling back to the default when invalid or unconfigured.
// It echoes the default instead of an empty string so the UI can fill the currently effective value into the input box.
func notifyDigestIntervalMin(pg *db.DB) int {
	v, ok, _ := pg.GetSetting(settingNotifyDigestMinutes)
	if !ok {
		return notifyDefaultDigestMinutes
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return notifyDefaultDigestMinutes
	}
	return n
}

// pgDetectPython re-runs interpreter detection, stores + returns it.
func (s *Server) pgDetectPython(w http.ResponseWriter, r *http.Request) {
	p := detectPython()
	if p == "" {
		writeErr(w, 404, "python not detected (neither python3 nor python is in PATH)")
		return
	}
	if err := s.m.pg.SetSetting(settingPythonInterp, p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"python_interpreter": p})
}

// putSettings applies a settings change. Toggling traffic_capture rebuilds the
// agents (applyLLM) so the new proxy/traffic-tools/prompt state takes hold — when
// off, agents get no proxy config, no traffic tools, and no proxy prompt content.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var input runtimeSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if fault := s.applyRuntimeSettings(input); fault != nil {
		writeErr(w, fault.status, fault.Error())
		return
	}
	writeJSON(w, 200, s.settingsPayload())
}

// testWebSearch runs a real "test" search with the given (or currently saved)
// backend/proxy/key to verify the config can actually reach a search backend —
// mirroring testLLM. Backend/proxy come from the request (so the form's unsaved
// edits are tested); empty API keys fall back to stored values so the user need
// not retype them. Always 200 with {ok, error?, count?, backend?}.
func (s *Server) testWebSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Backend   string `json:"web_search_backend"`
		Proxy     string `json:"web_search_proxy"`
		BraveKey  string `json:"brave_search_api_key"`
		TavilyKey string `json:"tavily_search_api_key"`
	}
	// Empty body is fine — fall back entirely to the saved config below.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, err.Error())
		return
	}
	_, backend, storedBraveKey, storedTavilyKey, _ := s.m.WebSearch()
	if strings.TrimSpace(req.Backend) != "" {
		backend = req.Backend
	}
	// Proxy is taken from the form as-is (empty = direct), so testing reflects exactly
	// what's shown — including an intentional "clear proxy to test direct" before saving.
	proxy := strings.TrimSpace(req.Proxy)
	// API keys are secrets the form omits when already saved, so fall back to stored.
	braveKey := storedBraveKey
	if strings.TrimSpace(req.BraveKey) != "" {
		braveKey = req.BraveKey
	}
	tavilyKey := storedTavilyKey
	if strings.TrimSpace(req.TavilyKey) != "" {
		tavilyKey = req.TavilyKey
	}
	if !db.ValidWebSearchProvider(strings.TrimSpace(backend)) {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "backend is not supported by audited research", "backend": backend})
		return
	}
	base := s.m.ResearchRuntimeConfig()
	base.Enabled, base.Primary, base.Fallback = true, strings.TrimSpace(backend), ""
	base.BraveKey, base.TavilyKey, base.Proxy = braveKey, tavilyKey, proxy
	probe := newResearchBroker(s.m.pg, func() researchRuntimeConfig { return base })
	// Hard cap so a slow/blocked proxy can't hang the request.
	wall := 30 * time.Second
	probeQuery := "test"
	ctx, cancel := context.WithTimeout(r.Context(), wall)
	defer cancel()
	ctx = agent.WithRunInfo(ctx, agent.RunInfo{
		SessionID: fmt.Sprintf("settings-test-%d", time.Now().UTC().UnixNano()),
		AgentKey:  "settings",
	})
	probeInput, _ := json.Marshal(map[string]any{"query": probeQuery, "limit": 3})
	result, err := probe.Tool().Call(ctx, probeInput, nil)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "audited search test failed", "backend": backend})
		return
	}
	if result.IsError {
		writeJSON(w, 200, map[string]any{"ok": false, "error": result.Flatten(), "backend": backend})
		return
	}
	var payload struct {
		Results    []json.RawMessage `json:"results"`
		ArtifactID int64             `json:"artifact_id"`
	}
	if json.Unmarshal([]byte(result.Flatten()), &payload) != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "audited search returned an invalid projection", "backend": backend})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(payload.Results), "backend": backend, "artifact_id": payload.ArtifactID})
}

// mainSessions lists the task's main-agent conversation segments (newest-first) and
// the current one. The frontend renders these as switchable sessions under the Main Agent.
func (s *Server) mainSessions(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	list, err := t.Store.ListMainSessions()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	current := 0
	if len(list) > 0 {
		current = list[0].Seq // newest-first
	}
	writeJSON(w, 200, map[string]any{"sessions": list, "current": current})
}

// newMainSession starts a fresh main-agent conversation segment. Only the segment
// counter advances — the task's exploration graph, assets and goal are untouched, so
// the main agent continues over the same task with a clean transcript/context.
func (s *Server) newMainSession(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, 409, "task is being deleted; a new session cannot be created")
		return
	}
	m, err := t.Store.NewMainSession()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"seq": m.Seq, "created_at": rfc3339(m.CreatedAt), "current": m.Seq})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	(taskChatRequest{server: s, writer: w, request: r}).serve()
}

func (s *Server) cancelTaskChat(taskID string, cause error) bool {
	s.chatMu.Lock()
	cancel := s.chatCancel[taskID]
	if cancel != nil {
		cancel(cause)
	}
	s.chatMu.Unlock()
	return cancel != nil
}

func (s *Server) finishTaskChat(taskID string, cancel context.CancelCauseFunc) {
	cancel(agent.AbortChatTurnFinished)
	s.chatMu.Lock()
	delete(s.chatBusy, taskID)
	delete(s.chatCancel, taskID)
	s.chatMu.Unlock()
}

// taskChatStatus reports the authoritative state of the task's main-agent turn.
// Activity timestamps are not a reliable proxy because a tool or LLM call may run
// for minutes without emitting an intermediate frame.
func (s *Server) taskChatStatus(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	s.chatMu.Lock()
	running := s.chatBusy[t.ID]
	s.chatMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"running": running})
}

// stopChat aborts the in-flight main-agent turn for a task (manual stop button).
// Mirrors pgStopConversation: cancels only the current turn; planner/workers are
// unaffected and continue running. The user can send a new message immediately.
func (s *Server) stopChat(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.cancelTaskChat(t.ID, agent.AbortChatStoppedByUser) {
		writeJSON(w, 200, map[string]any{"status": "idle"})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "stopping"})
}

// fallbackChat is the no-LLM human-steering handler: simple commands + situation summary.
func (s *Server) fallbackChat(t *Task, msg string) string {
	m := strings.TrimSpace(msg)
	lower := strings.ToLower(m)
	switch {
	case strings.HasPrefix(lower, "intent"):
		text := strings.TrimSpace(m[len("intent"):])
		_, _ = t.Store.AddIntent(map[string]any{"summary": text}, 9, nil, "human")
		return "Injected a high-priority intent: " + text
	case strings.HasPrefix(lower, "hint"):
		text := strings.TrimSpace(m[len("hint"):])
		_, _ = t.Store.AddNode(db.KindHint, map[string]any{"text": text}, 0, "active", "human", nil)
		return "Hint recorded; the planner will read it next time: " + text
	default:
		assetCounts, _ := s.m.Assets().CountsByType()
		assets := 0
		for _, c := range assetCounts {
			assets += c
		}
		fnd, _ := t.Store.ListByKind(db.KindFinding, 1000)
		fr, _ := t.Store.Frontier(1000)
		return fmt.Sprintf("(rule mode, LLM not configured) Current situation: assets %d, pending intents %d, confirmed findings %d.\nAvailable commands: type \"intent ...\" to inject an intent, \"hint ...\" to give the planner a hint.", assets, len(fr), len(fnd))
	}
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	(reportExportRequest{server: s, response: w, request: r}).taskReport()
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, map[string]any{"entries": t.Guard.Audit(), "attributions": t.Guard.Attributions()})
}

// gc is a no-op stub (GC not yet implemented in the new asset store).
func (s *Server) gc(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"removed": 0})
}

// --- utils ---

func cors(next http.Handler) http.Handler { return crossOriginHTTP{next: next} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}
