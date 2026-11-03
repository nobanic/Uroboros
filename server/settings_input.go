package server

// Anonymous input shape preserves the existing pointer/null and decoder-error contracts.
type runtimeSettingsInput = struct {
	TrafficCapture      *bool `json:"traffic_capture"`
	AgentTrafficBinding *bool `json:"agent_traffic_binding"`
	LLMRecord           *bool `json:"llm_record"` // LLM recording toggle (off by default); takes effect immediately, no agent rebuild needed
	// Web search. WebSearchEnabled/Backend toggle the tool + backend; BraveKey/TavilyKey
	// are optional — omit (null) to leave a stored key untouched, send "" to clear.
	WebSearchEnabled   *bool   `json:"web_search_enabled"`
	WebSearchBackend   *string `json:"web_search_backend"`
	BraveKey           *string `json:"brave_search_api_key"`
	TavilyKey          *string `json:"tavily_search_api_key"`
	WebSearchProxy     *string `json:"web_search_proxy"` // dedicated egress proxy (http/https/socks5); null = unchanged, "" = clear
	WebSearchFallback  *string `json:"web_search_fallback_backend"`
	ResearchTaskBudget *int    `json:"web_search_task_request_budget"`
	ResearchPaidBudget *int    `json:"web_search_task_paid_request_budget"`
	ResearchRate       *int    `json:"web_search_rate_per_minute"`
	ResearchCacheTTL   *int    `json:"web_search_cache_ttl_seconds"`
	TorResearchEnabled *bool   `json:"tor_research_enabled"`
	GlobalProxy        *string `json:"global_proxy"`       // global egress proxy (http/https/socks5); null = unchanged, "" = clear (direct connection)
	PythonInterp       *string `json:"python_interpreter"` // python interpreter path for the custom script tool
	Workers            *int    `json:"workers"`            // number of concurrent worker agents (>0); applies to tasks started afterward
	// Task concurrency cap: the max number of "running" tasks at once. Off = unlimited; once on, new tasks over the cap are queued and auto-started when a slot opens.
	ConcurrencyEnabled *bool `json:"task_concurrency_enabled"`
	ConcurrencyLimit   *int  `json:"task_concurrency_limit"`
	// LLM round-robin (failover) toggle + the "fall back to the round-robin chain even when the bound configuration fails" toggle. Both require
	// rebuilding the provider chain to take effect, via the changed → applyLLM path below.
	LLMPoolEnabled      *bool `json:"llm_pool_enabled"`
	LLMPoolBindFallback *bool `json:"llm_pool_bind_fallback"`
	// Operation-constraint injection scope toggles (all on by default); take effect immediately (planner/worker read each round), no agent rebuild needed.
	ConstraintsInjectPlanner *bool `json:"constraints_inject_planner"`
	ConstraintsInjectWorker  *bool `json:"constraints_inject_worker"`
	// Experimental feature: noa context compaction toggle (off by default); read per run, applies to runs started afterward, no agent rebuild needed.
	NoaCompaction       *bool `json:"noa_compaction"`
	EarlyBootstrapScout *bool `json:"early_bootstrap_scout"` // one bounded scout + concurrent planner for newly bootstrapped tasks
	// Global settings for vulnerability IM push. All three are read once per round by the delivery engine, taking effect immediately after a change,
	// with no agent rebuild or restart needed.
	NotifyEnabled    *bool   `json:"notify_enabled"`
	NotifyBaseURL    *string `json:"notify_public_base_url"`
	NotifyDigestMins *int    `json:"notify_digest_interval_min"`
}
