import type { SeverityCounts, TokenCounters, TokenPair, TokenReads } from "./contracts/accounting";
import type { ApprovalLifecycle, AssetRuleDefinition, CapturedAttempt } from "./contracts/consent";
import type { BatchOutcome, CatalogEntry, PageItems, RecordTimestamps, SourceLineage } from "./contracts/record";
import type { HTTPObservation } from "./contracts/traffic";

// Uroboros domain model — types used across the UI.
// Derived from the functional spec (section 7: key data shapes).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // optional task name; empty/missing = unnamed, falls back to the description when shown
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  first_run_at?: string; // RFC3339 first real planner/worker run; empty before execution starts
  first_run_unix?: number; // first_run_at as unix seconds (0 before execution starts)
  deadline_at?: string; // authoritative task timeout deadline; empty until first run
  deadline_unix?: number; // deadline_at as unix seconds (0 until first run)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: SeverityCounts; // number of recorded findings (bucketed by severity)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // asset-coverage feature toggle (set at creation, on by default); false = don't compute/show coverage
  strict_isolation?: boolean; // only expose assets associated with this task; cannot be combined with source tasks
  skip_initial_katana?: boolean; // suppress infrastructure crawl; explicit tool calls remain available
  initial_scout?: boolean | null; // null/missing follows the workspace default
}

export interface TaskCategory extends CatalogEntry, RecordTimestamps {
  task_count: number;
}

export interface TaskTemplate extends CatalogEntry, RecordTimestamps {
  description: string;
  goal: string;
  category_id?: number | null; // preset category; null/missing = none
  intercept_rules?: AssetInterceptRuleInput[]; // preset task-level block/allow rules
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats extends Partial<TokenCounters> {
  calls?: number;
}

export interface TaskArchive extends RecordTimestamps {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
}

export interface TaskArchivePage extends PageItems<TaskArchive> {
  size: number;
}

export interface ArchiveBatchItem extends BatchOutcome {
  id: string;
  archive_id?: number;
  queued: boolean;
}

export interface ArchiveProviderGrant extends ApprovalLifecycle {
  provider: "internet_archive_cdx";
  domain: string;
  capture_from: string;
  capture_to: string;
}

export interface ArchiveProviderGrantView {
  provider: "internet_archive_cdx";
  provider_name: string;
  provider_host: string;
  contract_version: string;
  notice_version: string;
  notice_template: string;
  raw_retained: false;
  grants: ArchiveProviderGrant[];
}

export interface ArchiveProviderArtifact extends CapturedAttempt<"started" | "page" | "http_error" | "network_error" | "contract_error" | "rate_limited"> {
  job_id: number;
  task_id: number;
  grant_id: number;
  provider: "internet_archive_cdx";
  route_sha256: string;
  raw_retained: false;
}

export type ResearchProvider =
  | "brave-free"
  | "tavily"
  | "ddgs"
  | "cve-list-v5"
  | "nvd"
  | "osv"
  | "ghsa"
  | "cisa-kev"
  | "first-epss";

export interface ResearchDisclosureGrant extends ApprovalLifecycle {
  provider: ResearchProvider;
  identities: string[];
  identity_sha256: string;
  max_requests: number;
}

export interface ResearchGrantView {
  contract_version: string;
  contract_versions: { web_search: string; vulnerability: string; research_fetch: string };
  notice_version: string;
  current_provider: ResearchProvider;
  fallback_provider?: ResearchProvider | "";
  providers: ResearchProvider[];
  disclosure_identities: string[];
  notice_template: string;
  grants: ResearchDisclosureGrant[];
}

export interface ResearchEgressGrant extends ApprovalLifecycle<"tor-research-fetch-v1", "research-egress-v1"> {
  profile_id: "tor";
  seeds: string[];
  origins: string[];
  seed_sha256: string;
  max_requests: number;
}

export interface ResearchEgressGrantView {
  profile_id: "tor";
  contract_version: "tor-research-fetch-v1";
  notice_version: "research-egress-v1";
  enabled: boolean;
  endpoint_configured: boolean;
  raw_endpoint_exposed: false;
  notice_template: string;
  grants: ResearchEgressGrant[];
}

export interface ResearchArtifact extends CapturedAttempt<| "started"
    | "success"
    | "cache_hit"
    | "stale_cache"
    | "http_error"
    | "network_error"
    | "contract_error"
    | "rate_limited"
    | "policy_denied"
    | "oversized"> {
  task_id?: number;
  session_id?: string;
  agent_key: string;
  exploration_id: number;
  intent_id: number;
  tool_name: "web_search" | "vuln_lookup" | "vuln_match" | "vuln_prioritize" | "research_fetch";
  grant_id?: number;
  egress_grant_id?: number;
  provider: ResearchProvider | "tor-fetch";
  egress_profile: "direct" | "operator_proxy" | "tor";
  isolation_sha256?: string;
  retry_index: number;
  query_sha256: string;
  query_projection: string;
  target_identifying: boolean;
  matched_identities: string[];
  provider_request_id?: string;
  cache_expires_at?: string;
  stale_expires_at?: string;
  schema_version?: string;
  etag?: string;
  last_modified?: string;
  retrieved_at?: string;
  source_artifact_id?: number;
  cost_units: number;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode extends CatalogEntry<string> {
  type: AssetType;
  key: string; // nkey
  value?: string;
  company_id?: string; // owning company asset id; empty = unattributed
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// A node in the force-directed "asset coverage graph". Unique key: asset = "a:<id>", company = "c:<id>",
// root domain with no asset row = "r:<domain>". in_scope=false marks gray context nodes used only for edges.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// The intents/facts/findings an asset is linked to in this task's exploration graph (for the coverage-graph node drawer).
export interface CoverageAssetRef extends SourceLineage {
  id: number;
  kind: string;
  state: string;
  summary: string;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// One row of a task's testing scope (coverage denominator + authorization boundary).
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // resolved by the backend JOIN on companies; set only when kind=company
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Structured asset-scope rules submitted when adding a company.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// Result of writing asset scopes. errors are the invalid rows in this submission; warnings are pre-existing
// data problems unrelated to this submission that would make attribution behave unexpectedly (e.g. an asset whose ip field stores a hostname).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// One company asset-scope rule (the single source of truth for attribution).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // set when kind=domain
  net?: string; // set when kind=ip|cidr
  value?: string; // may be returned directly by the backend when kind=icp|keyword
  raw: string; // raw user input, used for display and re-populating
  reason?: string;
}

// Company: a type=company asset node + logo + asset count + asset-scope rules.
export interface Company extends CatalogEntry {
  logo?: string; // remote logo URL; if empty the front-end uses the name's initials
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode extends SourceLineage {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  delete_reason?: string; // delete reason when an intent is soft-deleted (state='deleted')
}

// One page of the broadcast board: nodes paginated in creation order + the edges this page touches + the nodes at the other end of those edges (refs, indexed by id),
// so each broadcast can explain "where it came from and what it produced" without pulling the whole graph down.
export interface ExplorationNodePage extends PageItems<TaskNode> {
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // node id -> the assets anchored to that node (shown alongside when the broadcast board expands; includes this page's nodes and their neighbors).
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// Goal for the goal-management card (the backend has split payload into text/vulnclass).
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// Operation constraint for the constraint-management card (allow / deny).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Finding disposition status: pending / in progress / confirmed / resolved / fixed / false positive / ignored / duplicate / risk accepted.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset is an asset bound to a finding (label is pre-rendered by the backend).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding extends SourceLineage {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // row id in the standalone findings table; the handle for status updates (may be missing on older in-task nodes)
  vulnclass: string;
  name?: string; // finding name; when empty, display falls back to vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // detailed report (Markdown); returned only by the detail endpoint, empty in lists
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage is the server-paginated response for the findings list.
export interface FindingsPage extends PageItems<Finding> {
  page_size: number;
}

export interface FindingGroup extends SeverityCounts {
  task_id: string | number | null;
  task_name?: string; // optional task name; empty/missing = unnamed
  task_description: string;
  task_status: string;
  count: number;
  last_found_at: string;
}

export interface FindingGroupsPage extends PageItems<FindingGroup> {
  finding_total: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats is the whole-table aggregate of findings (stat cards + vuln-type dropdown), computed server-side and unaffected by pagination.
export interface FindingStats extends SeverityCounts {
  total: number;
  pending: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption is one item in the findings page's "by task" filter dropdown: a task that has findings (an empty description means the task was deleted,
// the front-end falls back to showing the id) and its finding count.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // optional task name; empty/missing = unnamed
  description: string;
  count: number;
}

// FindingQuery holds the findings list's pagination/filter/sort parameters.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // task id; "all"/empty = don't filter by task
  query?: string;
  sort?: "severity" | "time";
  // asset-tree node key; selecting a node = selecting its whole subtree. empty = don't filter by asset.
  assetScope?: string;
}

// ---- Findings by asset (asset view) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode is a node in the asset tree. key looks like a:<id> (asset), c:<id> (company),
// r:<domain> (a root domain with no asset row in the DB), __none__ (no associated asset).
export interface FindingAssetNode extends SeverityCounts {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // number of findings attached directly to this asset
  total: number; // includes descendants, deduplicated by finding
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET corresponds to the backend's db.FindingUnassignedAsset.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment is one uploaded file: path is relative to the conversation/task working directory (i.e. the agent's CWD).
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // absolute path (returned for scope=staging staging uploads; written into the description before the task is created)
}

export interface Activity extends SourceLineage {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile extends CatalogEntry {
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 scheduling, custom agents only) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // timer: every N seconds (0 = no timer)
  on_finding: boolean; // fires when any task produces a finding
  on_goal_met: boolean; // fires when any task meets a goal
  on_task_timeout: boolean; // fires when any task times out
  on_tool_call: boolean; // fires when a selected tool is called (after it finishes)
  on_task_create: boolean; // fires when any task is created
  interval_message: string; // a separate user message per trigger condition
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // tool keys selected for on_tool_call (at least one)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation extends RecordTimestamps {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket extends TokenCounters {
  date: string; // "YYYY-MM-DD"
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage extends TokenCounters {
  worker: string;
}

export interface SessionTokenUsage extends TokenCounters {
  session: string;
}

export interface BatchControlItem extends BatchOutcome {
  id: string;
  status?: string;
  queued?: boolean;
}

// Per-task result of a bulk category change. A failure can only mean the task was already deleted; the category write itself is atomic.
export interface BatchCategoryItem extends BatchOutcome {
  id: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal extends TokenCounters {
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage extends TokenCounters {
  profile_name: string;
  calls: number;
  tasks: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage extends TokenReads {
  profile_name: string;
  date: string; // YYYY-MM-DD
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat extends TokenCounters {
  model: string;
  calls: number;
}

export interface Session extends SourceLineage {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange extends HTTPObservation {
  id: string;
  ts: string;
  host: string;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent auto-binds traffic evidence, off by default; does not affect manual binding
  llm_record: boolean; // LLM recording toggle (off by default); when off, no LLM calls are recorded
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily"
	web_search_fallback_backend?: string;
	web_search_task_request_budget?: number;
	web_search_task_paid_request_budget?: number;
	web_search_rate_per_minute?: number;
	web_search_cache_ttl_seconds?: number;
	web_search_audit_mode?: "host_broker_v1";
  tor_research_enabled?: boolean;
  tor_egress_profile_id?: "tor";
  tor_socks_configured?: boolean;
  tor_proxy_coordinates_exposed?: false;
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // separate egress proxy (http/https/socks5) for reaching search endpoints; unrelated to the traffic-recording MITM proxy. empty = direct connection.
  web_search_proxy?: string;
  // global egress proxy (http/https/socks5, may include user:pass); all target traffic goes through it. When traffic capture is on it acts as
  // the MITM upstream; when capture is off it is injected directly into the agent's bash/WebFetch. empty = direct connection.
  global_proxy?: string;
  python_interpreter?: string; // python interpreter path for the custom script tool (empty = detect at runtime)
  workers?: number; // number of concurrent worker agents (default 3); applies to tasks started afterward
  // task concurrency cap: the max number of simultaneously "running" tasks. off = unlimited; when on, new tasks over the limit are queued and auto-started when a slot frees up.
  task_concurrency_enabled?: boolean; // default false
  task_concurrency_limit?: number; // default 5 when enabled
  // LLM round-robin (failover). off by default; when on, an agent with "no model specified" — if the current profile is unavailable
  // (insufficient balance / invalid key / rate-limited / service error) — automatically switches to the next profile.
  llm_pool_enabled?: boolean; // default false
  // whether an agent/task bound to a specific profile also falls back to the round-robin chain on failure. default false = binding means exclusive use.
  llm_pool_bind_fallback?: boolean;
  // operation-constraint injection scope (all on by default): splices the task's allow/deny constraints into the matching agent's system prompt.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // experimental: noa model-driven context compaction (off by default). When on, the four agent types wired into the platform (planner/
  // worker/main agent/chat) have their context compaction taken over by noa, replacing the built-in compaction; read once per run, applies to
  // runs started afterward.
  noa_compaction?: boolean;
  // ---- Finding IM push (channels themselves are separate resources, see /api/notify/*; only three global settings here) ----
  notify_enabled?: boolean; // master push toggle, on by default; a one-switch kill during maintenance
  notify_public_base_url?: string; // externally reachable address for the finding-detail back-link; empty = messages carry no back-link
  notify_digest_interval_min?: number; // digest-mode interval (minutes), default 30
}

// ---- Finding IM push ----

// NotificationFilter is a channel's filter conditions; all fields are optional, and missing means no filtering.
// The backend validates none of the fields: a malformed config is treated as a "match" (better to over-push than miss a push).
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // empty = no limit; if non-empty, requires overlap with the finding's owning tasks
  asset_ids?: number[]; // empty = no limit; if non-empty, requires overlap with the finding's anchored assets
  vulnclass_include?: string[]; // empty = accept all; if non-empty, requires the vuln type to match any keyword (case-insensitive substring)
  vulnclass_exclude?: string[]; // exclude if any keyword matches (exclude takes precedence over include)
  on_status_change?: boolean; // whether to also receive finding disposition-status change events
}

// NotificationChannel is a channel instance. config's fields vary by kind,
// and credential fields are replaced on read with masked values starting with "__masked__" — sending them back unchanged means "no change".
export interface NotificationChannel extends CatalogEntry, RecordTimestamps {
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  // secret_keys is provided by the backend per channel type; the front-end uses it to render password fields and the "leave blank to keep unchanged" hint,
  // without hard-coding any channel-specific knowledge.
  secret_keys: string[];
}

// NotificationKind is the channel-type metadata returned by /api/notify/meta.
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery is one delivery record, used for delivery history and failed-delivery retries.
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile extends CatalogEntry<string> {
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // thinking toggle (thinking.type): "" = don't send (default) | "disabled" = off | "enabled" = on
  thinking_type?: string;
  // reasoning effort: "" = don't send (default) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // round-robin rank: higher is selected first. The active profile is always the chain head, regardless of this value.
  priority?: number;
  // true = not used as a failover target (can still be explicitly bound by an agent/task).
  pool_exclude?: boolean;
  // true (default) = streaming (SSE) | false = truly non-streaming (stream:false, returned all at once).
  streaming?: boolean;
  // output cap for a single reply (tokens). 0 = don't send this field; let the server default decide.
  // Note the distinction from context_window_k: that is the model's total capacity, used only locally as the compaction threshold.
  max_tokens?: number;
  // which request field name carries the cap; only meaningful for format="openai":
  // "" = max_tokens (default) | "max_completion_tokens" (OpenAI reasoning models only accept this one)
  max_tokens_field?: string;
  // custom session header name: when non-empty, every request carries this HTTP header whose value is the current conversation/intent session id.
  // "" = don't send. For gateways that do prompt caching / sticky routing by the session-id header.
  session_header_key?: string;
  // this profile's retry overrides (connect / empty response / same-provider safe window). blank / all 0 = follow the global policy.
  retry?: LLMRetryOverride;
}

// ---- LLM retry policy ----
// The two knobs of one retry layer. Both treat "0 = not configured":
//   attempts    0 = use the default count | -1 = disable this layer | >0 = number of retries
//   interval_ms 0 = use the default exponential backoff | >0 = use this fixed millisecond interval
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// The three layers a single LLM profile can override (all "follow the endpoint" retries).
export interface LLMRetryOverride {
  connect: LLMRetryRule; // connect retry: connection reset/timeout/429/5xx, before the stream starts
  empty: LLMRetryRule; // empty-response retry: completed but with no content at all (openai format only)
  stream: LLMRetryRule; // same-provider safe-window retry: replay a dropped stream before any output is delivered
}

// Global policy = defaults for the three layers above + two layers that are global-only:
//   breaker  round-robin circuit breaker (attempts = how many consecutive transient failures trip it, interval_ms = fixed cooldown)
//   intent   intent rerun (the whole intent reruns after a worker ends in model_error)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM round-robin (failover) ----
// A profile's position and health in the round-robin chain. state:
//   ok       healthy
//   degraded has consecutive failures but hasn't hit the breaker threshold
//   tripped  tripped; skipped during cooldown (cooldown_secs is the remaining seconds)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // whether this is the currently active profile (always the chain head)
  excluded: boolean; // pool_exclude: excluded from round-robin
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent extends CatalogEntry<string> {
  key: string; // built-in: goals/planner/mainagent/worker; custom: a user-defined key
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // bound LLM profile; null/absent = follow task/conversation/global
  max_turns?: number; // 0 = unlimited
  run_seconds?: number; // wall-clock cap for one worker run (seconds); 0 = unlimited
  web_search?: boolean; // whether web search is enabled (gated by the global system toggle)
  interactive_shell?: boolean; // whether the interactive shell is enabled (the persistent-PTY session tool family)
  // post-trigger handling strategy for P3 (only meaningful for custom agents)
  trigger_run_mode?: "serial" | "parallel"; // serial queue / each trigger spawns its own concurrent session
  trigger_merge_mode?: "by_task" | "all" | "none"; // serial only: merge by task / merge all / no merge
  trigger_max_parallel?: number; // parallel only: per-agent concurrency cap; 0 = unlimited
  // binding counts (returned only by the list endpoint): visible MCPs / visible Skills / bound tools
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // candidate LLM profiles that can be bound (for the "default model" dropdown); see agent.llm_profile_id for the current binding
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // saved wrap-up prompt (empty = use the built-in default)
  wrapup_default?: string; // built-in default wrap-up prompt (placeholder / restore default)
  wrapup_max_turns?: number; // saved wrap-up turn count (0 = use the built-in default)
  wrapup_max_turns_default?: number; // built-in default wrap-up turn count (for the "0 = default N" hint)
  // task-level timeout wrap-up prompt (only worker/planner; this section is shown only when task_timeout_wrapup_supported=true)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer extends CatalogEntry {
  request_timeout_seconds?: number;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // call stats (the skill_usage ledger). For a skill that was never called: calls=0, last_used absent.
  calls: number;
  tasks: number; // number of tasks that loaded it (chat conversations not counted)
  usage_agents: string[]; // agent keys that loaded it
  last_used?: string;
}

// SkillCall is one Skill() invocation (the recent-call list for a single skill).
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = non-task context (chat conversation)
  session_id: string;
  args_len: number;
}

// MissingSkill is a skill that was named but doesn't exist — a "wanted but missing" gap.
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (built-in tool catalog) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // custom tool kind
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // custom tool execution spec (kind != builtin)
  deferred?: boolean; // deferred schema (SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
  family?: string; // response-only runtime family (Katana, Arsenal, Traffic & evidence, ...)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule extends CatalogEntry, RecordTimestamps {
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
}

// ---- Asset Intercept Rules (asset interception: global blacklist) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action is only used by task-level rules: block (forbid testing) allow (whitelist).
export type AssetInterceptAction = "block" | "allow";

// entry item for task-level asset block/allow rules (used when creating a task and editing task details).
export interface AssetInterceptRuleInput extends AssetRuleDefinition<AssetInterceptKind> {
  action: AssetInterceptAction;
}

export interface AssetInterceptRule extends AssetRuleDefinition<AssetInterceptKind>, RecordTimestamps {
  id: number;
  action?: AssetInterceptAction; // global rules omit this field (always block); task-level rules distinguish block/allow
  builtin: boolean;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // the rule message, or the model's decision rationale (model decisions are prefixed with [model])
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: global config for model fallback approval (the model decides only when no interception rule matches).
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = follow the active/default profile
  prompt: string; // decision prompt; on GET, if unset the backend fills in the full built-in template
  timeout_seconds: number; // model-call timeout
  fail_action: "allow" | "ask" | "deny"; // fallback when the model errors / times out / can't be parsed
  ask_timeout_seconds: number; // approval-wait timeout after the model returns ask and escalates to a human
  ask_timeout_action: "allow" | "deny"; // default action after the approval times out
}

// JudgeUsage: cumulative token usage for model fallback approval (the judge channel) + a daily series for the last N days.
export interface JudgeDayUsage extends TokenPair {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
}
export interface JudgeUsage extends TokenCounters {
  calls: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary extends TokenCounters {
  llm_profile_id: number | null;
  created_at: string;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// per-tool call stats (/commands/stats); errors is how many of those failed.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem extends TokenPair {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // the raw HTTP the provider actually sent/received: the request is the full body emitted by buildBody() (including tool
  // schema), the response is the raw SSE frames. The request_body/response_body above are normalized views that
  // drop the tool schema and tool_use blocks. Empty for older records.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot extends HTTPObservation {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check —— comparison of the current version against the latest GitHub stable release. */
export interface UpdateCheck {
  /** the version currently running; for dev builds, "dev" or the suffixed form from git describe. */
  current: string;
  /** runtime form. Under docker, swapping in a new build only affects the container's writable layer; rebuilding the container reverts to the image version. */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** whether a rollback-able previous version (uroboros.old) exists. */
  has_backup: boolean;
  /** the outcome of self-update bootstrapping on this startup (swap failed / rolled back, etc.); empty when nothing happened. */
  boot_notice?: string;
  rolled_back?: boolean;
  /** the reason when the GitHub query fails; in that case none of the fields below are present. */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** the release asset name for the current platform, and whether the Release actually ships it. */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** whether the two version numbers are comparable; false for dev builds, which disables one-click update. */
  comparable?: boolean;
  /** explanation when comparable is false. */
  reason?: string;
}

/** one update-progress event pushed by /api/update/stream. */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** only meaningful during the download phase (0-100); -1 in other phases. */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}
