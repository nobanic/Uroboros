package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ---------- LLM profiles ----------

type LLMProfile struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Format        string  `json:"format"`
	BaseURL       string  `json:"base_url,omitempty"`
	Proxy         string  `json:"proxy,omitempty"` // LLM outbound proxy (http/https/socks5); empty = use environment variables
	Model         string  `json:"model"`
	APIKey        string  `json:"-"` // never serialized to UI
	APIKeyHint    string  `json:"api_key_hint,omitempty"`
	RatePerSecond float64 `json:"rate_per_second"`
	RatePerMinute float64 `json:"rate_per_minute"`
	// ContextWindowK is the model's context window in K tokens, used to size
	// compaction thresholds. 0 = use a 200K default; capped at 1000 (1M).
	ContextWindowK int `json:"context_window_k"`
	// ThinkingType independently controls the thinking "switch" (thinking.type): "" = don't send (default);
	// "disabled" = explicitly off; "enabled" = on. Decoupled from ReasoningEffort.
	ThinkingType string `json:"thinking_type"`
	// ReasoningEffort independently controls the thinking "strength": "" = don't send (default);
	// "low"/"medium"/"high"/"xhigh"/"max" = the corresponding strength. See agent.Config.NewProvider.
	ReasoningEffort string `json:"reasoning_effort"`
	IsDefault       bool   `json:"is_default"`
	// Priority orders the failover chain: higher goes first. The ACTIVE profile
	// (IsDefault) always heads the chain regardless of this value.
	Priority int `json:"priority"`
	// PoolExclude=true keeps this profile out of the failover chain — it stays
	// usable when an agent/task binds it explicitly, it just never gets picked up
	// as a fallback target.
	PoolExclude bool `json:"pool_exclude"`
	// Streaming selects the wire protocol: true (default) = streaming (SSE);
	// false = real non-streaming (stream:false, single JSON response via
	// Provider.Complete). Non-streaming sidesteps flaky gateway SSE at the cost of
	// live in-run progress. Maps to agent.Config.Stream.
	Streaming bool `json:"streaming"`
	// MaxTokens caps a single reply's output in tokens. 0 = send no cap and let
	// the endpoint's own default apply (the historical behaviour). Unlike
	// ContextWindowK — the model's total capacity, used locally to size compaction
	// — this value travels with every request.
	MaxTokens int `json:"max_tokens"`
	// MaxTokensField picks the request key carrying MaxTokens, for format
	// "openai" only: "" = max_tokens (default); "max_completion_tokens" = the
	// newer key, which OpenAI's reasoning models require and whose budget covers
	// reasoning tokens plus visible output. Ignored by anthropic and
	// openai-responses, which name the field themselves.
	MaxTokensField string `json:"max_tokens_field"`
	// SessionHeaderKey, when non-empty, names a custom HTTP header sent on every
	// request built from this profile; its value is the current run's session id
	// (chat conversation / worker intent). For gateways that key prompt caching
	// or sticky routing off a session-id header. "" = not sent. Maps to
	// agent.Config.SessionHeaderKey.
	SessionHeaderKey string `json:"session_header_key"`
	// Retry overrides this profile's share of the retry ladder. Zero value =
	// inherit the global policy (LLMRetryPolicy), so an untouched profile behaves
	// exactly as before. See RetryOverride.
	Retry RetryOverride `json:"retry"`
}

// RetryOverride is one profile's optional override of the three retry layers
// that are per-endpoint: connect / empty-response (empty) / same-provider safe window
// (stream). Each rule's zero value means "inherit the global policy"; see
// RetryRule for the -1 / 0 / >0 semantics.
type RetryOverride struct {
	Connect RetryRule `json:"connect"`
	Empty   RetryRule `json:"empty"`
	Stream  RetryRule `json:"stream"`
}

func (d *DB) ListProfiles() ([]*LLMProfile, error) { return d.readProfiles(false, "ORDER BY id") }

// ActiveProfile returns the default (active) profile with its api key, or nil.
func (d *DB) ActiveProfile() (*LLMProfile, error) { return d.readProfile("WHERE is_default LIMIT 1") }

// ProfileByID returns one profile with its api key by id, or nil if not found.
// Used to run a task on a specific (non-default) LLM profile.
func (d *DB) ProfileByID(id int64) (*LLMProfile, error) { return d.readProfile("WHERE id = $1", id) }

// PoolProfiles returns the failover chain in run order, api keys included: the
// active profile first, then every other keyed profile that isn't excluded, by
// priority DESC (id ASC to stay stable). Profiles without an api key can't serve
// a request, so they never enter the chain. The ordering IS the policy — callers
// walk the slice front to back.
func (d *DB) PoolProfiles() ([]*LLMProfile, error) {
	return d.readProfiles(true, "WHERE COALESCE(api_key, '') <> '' AND (is_default OR NOT pool_exclude) ORDER BY is_default DESC, priority DESC, id ASC")
}

// SaveProfile inserts (id==0) or updates a profile. Empty apiKey on update keeps existing.
func (d *DB) SaveProfile(p *LLMProfile) (int64, error) { return d.storeProfile(p) }

var (
	ErrActiveLLMProfileDelete      = errors.New("cannot delete the active LLM profile; activate another profile first")
	ErrLLMProfileReferencesChanged = errors.New("LLM profile references changed while deleting; retry the request")
	ErrLLMProfileNotFound          = errors.New("LLM profile not found")
)

func (d *DB) DeleteProfile(id int64) error {
	return d.DeleteProfileContext(context.Background(), id)
}

// DeleteProfileContext removes a non-default profile while preserving the task
// failover cursor. Reference changes are retried because a task, agent, or
// conversation may start pointing at the profile between the initial scan and
// the profile row lock. A bound prevents a continuously changing workload from
// keeping an HTTP request alive forever.
func (d *DB) DeleteProfileContext(ctx context.Context, id int64) error {
	const (
		maxAttempts = 8
		maxDuration = 15 * time.Second
	)
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		retry, err := d.deleteProfile(ctx, id)
		if err != nil || !retry {
			return err
		}
	}
	return fmt.Errorf("%w: profile %d", ErrLLMProfileReferencesChanged, id)
}

func (d *DB) deleteProfile(ctx context.Context, id int64) (bool, error) {
	return deleteConfiguredProfile(d, ctx, id)
}

func lockProfileReferenceRows(ctx context.Context, tx *sql.Tx, query string, profileID int64) (map[int64]struct{}, error) {
	rows, err := tx.QueryContext(ctx, query, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locked := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		locked[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return locked, nil
}

// SetActiveProfile makes one profile the global default (single-default invariant).
func (d *DB) SetActiveProfile(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE llm_profiles SET is_default=false WHERE is_default`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE llm_profiles SET is_default=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrLLMProfileNotFound
	}
	return tx.Commit()
}

// ---------- Agents / prompts ----------

type Agent struct {
	ID               int64  `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Role             string `json:"role"`
	Builtin          bool   `json:"builtin"`
	Enabled          bool   `json:"enabled"`
	LLMProfileID     *int64 `json:"llm_profile_id"`    // bound LLM profile; nil = follow the task/conversation pin, then fall back to the global active profile
	MaxTurns         int    `json:"max_turns"`         // max turns per run; 0 = unlimited
	RunSecs          int    `json:"run_seconds"`       // worker per-run wall-clock cap (seconds); 0 = unlimited
	WebSearch        bool   `json:"web_search"`        // whether web search is enabled (gated by the system-wide master switch)
	InteractiveShell bool   `json:"interactive_shell"` // whether the interactive shell (persistent-PTY session tool family) is enabled
	WrapupPrompt     string `json:"wrapup_prompt"`     // wrap-up prompt (settlement prompt on timeout / step exhaustion); empty = use the code built-in default
	WrapupMaxTurns   int    `json:"wrapup_max_turns"`  // the wrap-up phase's own turn budget; 0 = use the code built-in default (per agent)
	// task-level timeout wrap-up prompt (a separate pair from per-run; only worker/planner use it); empty/0 = use the code built-in default.
	TaskTimeoutWrapupPrompt   string `json:"task_timeout_wrapup_prompt"`
	TaskTimeoutWrapupMaxTurns int    `json:"task_timeout_wrapup_max_turns"`
	// P3 trigger post-processing policy (only meaningful for custom agents):
	// TriggerRunMode  serial|parallel — serial queueing / each trigger runs its own concurrent session
	// TriggerMergeMode by_task|all|none — serial only: merge by task / merge all / don't merge
	// TriggerMaxParallel — per-agent concurrency cap, parallel only; 0 = unlimited
	TriggerRunMode     string `json:"trigger_run_mode"`
	TriggerMergeMode   string `json:"trigger_merge_mode"`
	TriggerMaxParallel int    `json:"trigger_max_parallel"`
}

func (d *DB) ListAgents() ([]*Agent, error) { return d.readAgents() }

func (d *DB) GetAgentByKey(key string) (*Agent, error) { return d.readAgent(key) }

// AgentBindingCounts returns per-agent binding counts in a few grouped queries
// (NO N+1): visible MCP servers and skills keyed by agent id, and bound tools
// keyed by agent key (tools.agents is a JSONB array of agent keys). Missing keys
// mean zero. Used to show "MCP N · Skill N · Tool N" on the agent cards.
func (d *DB) AgentBindingCounts() (mcp map[int64]int, skill map[int64]int, tools map[string]int, err error) {
	mcp, skill, tools = map[int64]int{}, map[int64]int{}, map[string]int{}
	byID := func(q string, into map[int64]int) error {
		rows, e := d.Query(q)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var n int
			if e := rows.Scan(&id, &n); e != nil {
				return e
			}
			into[id] = n
		}
		return rows.Err()
	}
	if err = byID(`SELECT agent_id, count(DISTINCT resource_id) FROM agent_visibility WHERE resource_kind='mcp' AND mcp_tool_name='' AND enabled GROUP BY agent_id`, mcp); err != nil {
		return
	}
	if err = byID(`SELECT agent_id, count(*) FROM agent_skill_visibility WHERE enabled GROUP BY agent_id`, skill); err != nil {
		return
	}
	rows, e := d.Query(`SELECT elem, count(*) FROM tools, jsonb_array_elements_text(agents) AS elem GROUP BY elem`)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if e := rows.Scan(&k, &n); e != nil {
			err = e
			return
		}
		tools[k] = n
	}
	err = rows.Err()
	return
}

// CreateAgent inserts a custom (builtin=false) conversational agent with role
// 'assistant'. Returns the new row. Callers validate key/name upstream; the DB
// enforces key charset + uniqueness and the role check constraint.
func (d *DB) CreateAgent(key, name, description string) (*Agent, error) {
	return d.insertAgent(key, name, description)
}

// UpdateAgentMeta updates a custom agent's display name + description. Built-in
// agents are left untouched (guarded by the caller / the builtin flag).
func (d *DB) UpdateAgentMeta(key, name, description string) error {
	_, err := d.Exec(`UPDATE agents SET name=$2, description=NULLIF($3,'') WHERE key=$1 AND builtin=false`, key, name, description)
	return err
}

// DeleteAgent removes a custom agent. Built-in agents are protected by the
// builtin=false guard. agent_prompts / agent_prompt_vars / visibility rows cascade
// via FK; tools.agents bindings for the key are cleaned by the caller.
func (d *DB) DeleteAgent(key string) error {
	_, err := d.Exec(`DELETE FROM agents WHERE key=$1 AND builtin=false`, key)
	return err
}

// SetAgentMaxTurns updates an agent's max_turns (0 = unlimited).
func (d *DB) SetAgentMaxTurns(key string, maxTurns int) error {
	if maxTurns < 0 {
		maxTurns = 0
	}
	_, err := d.Exec(`UPDATE agents SET max_turns=$1 WHERE key=$2`, maxTurns, key)
	return err
}

// SetAgentLLMProfile binds an agent to a specific LLM profile (id != nil), or clears
// the binding (id == nil) so the agent follows the task/conversation pin, else the
// global active profile. Precedence at runtime: agent binding → task/conv pin → active.
func (d *DB) SetAgentLLMProfile(key string, id *int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// DeleteProfile locks reference rows before the profile row. Keep the same
	// order here so a concurrent rebind cannot form a child/profile deadlock.
	var agentID int64
	if err := tx.QueryRow(`SELECT id FROM agents WHERE key=$1 FOR UPDATE`, key).Scan(&agentID); err != nil {
		if err == sql.ErrNoRows {
			// Preserve the previous UPDATE semantics: an unknown key is a no-op.
			return tx.Commit()
		}
		return err
	}
	if err := lockLLMProfileForReference(tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE agents SET llm_profile_id=$1 WHERE id=$2`, id, agentID); err != nil {
		return err
	}
	return tx.Commit()
}

// lockLLMProfileForReference makes the profile side of the shared lock-order
// protocol explicit. Callers must already own the referencing child row.
func lockLLMProfileForReference(tx *sql.Tx, profileID *int64) error {
	if profileID == nil {
		return nil
	}
	var lockedID int64
	if err := tx.QueryRow(`SELECT id FROM llm_profiles WHERE id=$1 FOR KEY SHARE`, *profileID).Scan(&lockedID); err != nil {
		if err == sql.ErrNoRows {
			return ErrLLMProfileNotFound
		}
		return err
	}
	return nil
}

// SetAgentWebSearch toggles whether an agent uses network search (still gated by
// the global web-search master switch + backend/key config).
func (d *DB) SetAgentWebSearch(key string, on bool) error {
	_, err := d.Exec(`UPDATE agents SET web_search=$1 WHERE key=$2`, on, key)
	return err
}

// SetAgentInteractiveShell toggles whether an agent gets the interactive shell
// (persistent PTY session) tool family + Bash prompt linkage (see docs/interactive-shell-design.md §14.2).
func (d *DB) SetAgentInteractiveShell(key string, on bool) error {
	_, err := d.Exec(`UPDATE agents SET interactive_shell=$1 WHERE key=$2`, on, key)
	return err
}

// SetAgentWrapupPrompt stores an agent's wrap-up (settlement) prompt. Empty string
// means "use the code built-in default" — resolved at runtime by resolveWrapup.
func (d *DB) SetAgentWrapupPrompt(key, prompt string) error {
	_, err := d.Exec(`UPDATE agents SET wrapup_prompt=$1 WHERE key=$2`, prompt, key)
	return err
}

// SetAgentWrapupMaxTurns stores the wrap-up phase's own turn budget. 0 means "use
// the code built-in default" — resolved at runtime by resolveWrapupTurns.
func (d *DB) SetAgentWrapupMaxTurns(key string, n int) error {
	_, err := d.Exec(`UPDATE agents SET wrapup_max_turns=$1 WHERE key=$2`, n, key)
	return err
}

// SetAgentTaskTimeoutWrapup stores an agent's task-timeout wrap-up prompt (empty =
// use code built-in default; only worker/planner have one) and its turn budget
// (0 = default). Resolved at runtime by resolveTaskTimeoutWrapup / …Turns.
func (d *DB) SetAgentTaskTimeoutWrapup(key, prompt string, maxTurns int) error {
	_, err := d.Exec(`UPDATE agents SET task_timeout_wrapup_prompt=$1, task_timeout_wrapup_max_turns=$2 WHERE key=$3`, prompt, maxTurns, key)
	return err
}

// SetAgentRunSeconds updates an agent's run_seconds wall-clock budget (0 = unlimited).
func (d *DB) SetAgentRunSeconds(key string, runSecs int) error {
	if runSecs < 0 {
		runSecs = 0
	}
	_, err := d.Exec(`UPDATE agents SET run_seconds=$1 WHERE key=$2`, runSecs, key)
	return err
}

// SetAgentTriggerBehavior stores an agent's P3 trigger post-processing policy:
// runMode(serial|parallel) / mergeMode(by_task|all|none) / maxParallel(parallel only, 0=unlimited).
// The enums are whitelist-validated, with invalid values falling back to the default, so dirty data can't skew the scheduler pump.
func (d *DB) SetAgentTriggerBehavior(key, runMode, mergeMode string, maxParallel int) error {
	switch runMode {
	case "serial", "parallel":
	default:
		runMode = "serial"
	}
	switch mergeMode {
	case "by_task", "all", "none":
	default:
		mergeMode = "by_task"
	}
	if maxParallel < 0 {
		maxParallel = 0
	}
	_, err := d.Exec(`UPDATE agents SET trigger_run_mode=$1, trigger_merge_mode=$2, trigger_max_parallel=$3 WHERE key=$4`,
		runMode, mergeMode, maxParallel, key)
	return err
}

type PromptVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Example     string `json:"example"`
	Source      string `json:"source"`
}

func (d *DB) PromptVars(agentID int64) ([]PromptVar, error) {
	return (promptCatalog{catalog: agentCatalog{database: d}, agentID: agentID}).variables()
}

// CurrentPrompt returns the agent's active template text ("" if none set yet).
func (d *DB) CurrentPrompt(agentID int64) (string, error) {
	return (promptCatalog{catalog: agentCatalog{database: d}, agentID: agentID}).current()
}

// SeedPromptIfEmpty writes the code-default template as the agent's first prompt
// version ONLY when it has none yet (current_prompt_id IS NULL). Mirrors
// SeedTool's first-insert-only philosophy: a user's edited prompt is never
// clobbered on restart. Idempotent — a no-op once any version exists.
func (d *DB) SeedPromptIfEmpty(agentID int64, tmpl string) error {
	var cur sql.NullInt64
	if err := d.QueryRow(`SELECT current_prompt_id FROM agents WHERE id=$1`, agentID).Scan(&cur); err != nil {
		return err
	}
	if cur.Valid {
		return nil // already seeded or user-edited → leave it
	}
	_, err := d.SavePrompt(agentID, tmpl, "built-in default", "system")
	return err
}

// ResetPromptToDefault appends the code-default template as a new version and
// points current at it — the explicit "restore to built-in default" action.
func (d *DB) ResetPromptToDefault(agentID int64, tmpl string) (int, error) {
	return d.SavePrompt(agentID, tmpl, "restore to built-in default", "system")
}

// SavePrompt appends a new version and points current_prompt_id at it.
func (d *DB) SavePrompt(agentID int64, template, note, by string) (int, error) {
	return (promptCatalog{catalog: agentCatalog{database: d}, agentID: agentID}).append(template, note, by)
}

type PromptVersion struct {
	Version   int       `json:"version"`
	Template  string    `json:"template_text"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"ts"`
}

func (d *DB) ListPromptVersions(agentID int64) ([]PromptVersion, error) {
	return (promptCatalog{catalog: agentCatalog{database: d}, agentID: agentID}).versions()
}

// ---------- MCP servers ----------

type MCPServer struct {
	ID                    int64           `json:"id"`
	Name                  string          `json:"name"`
	Transport             string          `json:"transport"`
	Command               string          `json:"command,omitempty"`
	Args                  json.RawMessage `json:"args"`
	Env                   json.RawMessage `json:"env"`
	URL                   string          `json:"url,omitempty"`
	Enabled               bool            `json:"enabled"`
	Insecure              bool            `json:"insecure"`                // http: skip TLS cert verification (self-signed servers, issue #108)
	Tools                 []string        `json:"tools,omitempty"`         // cached tool names (mcp_tools_cache)
	RequestTimeoutSeconds int             `json:"request_timeout_seconds"` // Streamable HTTP; 0 on save preserves/defaults
}

func (d *DB) ListMCP() ([]*MCPServer, error) { return d.mcpRecords() }

// MCPToolNames returns the cached tool names for a server (empty until discovered).
func (d *DB) MCPToolNames(serverID int64) ([]string, error) {
	return (agentCatalog{database: d}).toolNames(serverID)
}

// MCPTool is one cached tool of an MCP server (name + description).
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// MCPToolsDetailed returns the cached tools (name + description) for a server.
func (d *DB) MCPToolsDetailed(serverID int64) ([]MCPTool, error) {
	return (agentCatalog{database: d}).tools(serverID)
}

// SaveMCPTools replaces the cached tool list for a server (called after discovery).
func (d *DB) SaveMCPTools(serverID int64, tools []MCPTool) error {
	return (agentCatalog{database: d}).replaceTools(serverID, tools)
}

func (d *DB) SaveMCP(m *MCPServer) (int64, error) { return d.storeMCP(m) }

func (d *DB) DeleteMCP(id int64) error {
	return (agentCatalog{database: d}).deleteServer(id)
}

// ---------- Skill visibility (agent × skill_name) ----------

// AgentSkillNames returns the skill directory names visible to an agent.
func (d *DB) AgentSkillNames(agentID int64) ([]string, error) {
	return (agentCatalog{database: d}).skills(agentID)
}

// SkillAgents returns the agent IDs that can see a skill.
func (d *DB) SkillAgents(skillName string) ([]int64, error) {
	return (agentCatalog{database: d}).skillAgents(skillName)
}

// SetAgentSkillVisibility replaces all skill visibility for an agent.
func (d *DB) SetAgentSkillVisibility(agentID int64, names []string) error {
	return (agentCatalog{database: d}).replaceSkills(agentID, names)
}

// ToggleSkillVisibility sets one (agent, skill_name) visibility on/off.
func (d *DB) ToggleSkillVisibility(agentID int64, skillName string, on bool) error {
	return (agentCatalog{database: d}).toggleSkill(agentID, skillName, on)
}

// DeleteSkillVisibility removes all visibility rows for a skill (called on skill delete).
func (d *DB) DeleteSkillVisibility(skillName string) error {
	return (agentCatalog{database: d}).deleteSkill(skillName)
}

// ---------- Visibility (agent × mcp) ----------

// AgentVisible returns the resource ids of a kind visible to an agent.
func (d *DB) AgentVisible(agentID int64, kind string) ([]int64, error) {
	return (agentCatalog{database: d}).servers(agentID, kind)
}

// ResourceAgents returns the agent ids that can see a resource.
func (d *DB) ResourceAgents(kind string, resourceID int64) ([]int64, error) {
	return (agentCatalog{database: d}).resourceAgents(kind, resourceID)
}

// SetAgentVisibilityKind replaces the full set of visible resources of a kind for
// an agent (agent-side bulk write). Bidirectional with the resource-side view —
// both read/write the same agent_visibility rows.
func (d *DB) SetAgentVisibilityKind(agentID int64, kind string, resourceIDs []int64) error {
	return (agentCatalog{database: d}).replaceServers(agentID, kind, resourceIDs)
}

// ToggleVisibility sets one (agent, kind, resource) visibility on/off (idempotent).
func (d *DB) ToggleVisibility(agentID int64, kind string, resourceID int64, on bool) error {
	return (agentCatalog{database: d}).toggleServer(agentID, kind, resourceID, on)
}
