// Ordered Uroboros schema catalog. Column/index ownership is shared by the renderer;
// compatibility migrations and function bodies remain literal at their original positions.
package db

var schemaSQL = buildSchemaSQL()

func buildSchemaSQL() string {
	var plan ddlSchema
	plan.raw(`-- Uroboros PostgreSQL schema (single source of truth)
-- Idempotent: safe to re-run (IF NOT EXISTS / OR REPLACE / DROP TRIGGER IF EXISTS).

-- =====================================================================
-- 0. Common: updated_at trigger
-- =====================================================================
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

-- Safe text→inet conversion: an invalid value returns NULL instead of raising 22P02. assets.ip is
-- free text (the Agent / asset API may write a hostname into it), so a bare a.ip::inet would let one
-- dirty row crash the whole company-attribution recompute. Callers use try_inet(...) IS NULL to find
-- and flag those rows. pg_input_is_valid isn't used: it needs PG16+, and we must support older DBs.
CREATE OR REPLACE FUNCTION try_inet(value text) RETURNS inet AS $$
BEGIN
    RETURN value::inet;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE STRICT;

-- =====================================================================
-- A. Asset layer: companies / assets / company_scope
-- =====================================================================

`)
	plan.table("companies",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired),
		ddlColumn("nkey", ddlText, ddlRequired, ddlUnique),
		ddlColumn("logo", ddlText),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("idx_companies_nkey", "companies", false, "( nkey )")
	plan.updatedTrigger("trg_companies_upd", "companies")
	plan.table("assets",
		ddlSequence("id"),
		ddlColumn("type", ddlText, ddlRequired, ddlCheck("( type IN ( 'root_domain' , 'ip' , 'subdomain' , 'app' , 'service' , 'endpoint' ) )")),
		ddlColumn("company_id", ddlBigInt, ddlReferences("companies", "id", "SET NULL")),
		ddlColumn("company_source", ddlText, ddlRequired, ddlDefault("'explicit'"), ddlCheck("( company_source IN ( 'explicit' , 'scope' ) )")),
		ddlColumn("task_ids", ddlBigIntArray, ddlRequired, ddlEmptyObject),
		ddlColumn("domain", ddlText),
		ddlColumn("root_domain", ddlText),
		ddlColumn("ip", ddlText),
		ddlColumn("c_segment", ddlCIDR),
		ddlColumn("port", ddlInteger, ddlCheck("( port BETWEEN 1 AND 65535 )")),
		ddlColumn("icp", ddlText),
		ddlColumn("bound_domains", ddlTextArray, ddlRequired, ddlEmptyObject),
		ddlColumn("open_ports", ddlJSONBArray, ddlRequired, ddlEmptyObject),
		ddlColumn("record_type", ddlText),
		ddlColumn("record_value", ddlTextArray),
		ddlColumn("bundle_id", ddlText),
		ddlColumn("app_name", ddlText),
		ddlColumn("category", ddlText),
		ddlColumn("app_description", ddlText),
		ddlColumn("app_icp", ddlText),
		ddlColumn("url", ddlText),
		ddlColumn("service_type", ddlText, ddlCheck("( service_type IN ( 'http' , 'other' ) )")),
		ddlColumn("service_name", ddlText),
		ddlColumn("favicon_mmh3", ddlText),
		ddlColumn("status_code", ddlInteger),
		ddlColumn("content_length", ddlBigInt),
		ddlColumn("page_title", ddlText),
		ddlColumn("technologies", ddlTextArray, ddlRequired, ddlEmptyObject),
		ddlColumn("auth", ddlJSONBArray, ddlRequired, ddlEmptyObject),
		ddlColumn("method", ddlText),
		ddlColumn("params", ddlJSONBArray, ddlRequired, ddlEmptyObject),
		ddlColumn("extra", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlTimestamp("created_at"),
		ddlTimestamp("last_seen"),
		ddlTimestamp("updated_at"),
	)
	plan.index("uq_av2_root_domain", "assets", true, "( domain ) WHERE type = 'root_domain'")
	plan.index("uq_av2_ip", "assets", true, "( ip ) WHERE type = 'ip'")
	plan.index("uq_av2_subdomain", "assets", true, "( domain , COALESCE ( record_type , '' ) ) WHERE type = 'subdomain'")
	plan.index("uq_av2_app_bundle", "assets", true, "( bundle_id ) WHERE type = 'app' AND bundle_id IS NOT NULL")
	plan.index("uq_av2_app_name", "assets", true, "( app_name ) WHERE type = 'app' AND bundle_id IS NULL")
	plan.index("uq_av2_service_http", "assets", true, "( url ) WHERE type = 'service' AND service_type = 'http'")
	plan.index("uq_av2_service_other", "assets", true, "( COALESCE ( domain , '' ) , COALESCE ( ip , '' ) , port , service_name ) WHERE type = 'service' AND service_type = 'other'")
	plan.index("uq_av2_endpoint", "assets", true, "( url , method ) WHERE type = 'endpoint'")
	plan.index("idx_av2_company", "assets", false, "( company_id ) WHERE company_id IS NOT NULL")
	plan.index("idx_av2_company_type", "assets", false, "( company_id , type ) WHERE company_id IS NOT NULL")
	plan.index("idx_av2_task_ids", "assets", false, "USING GIN ( task_ids )")
	plan.index("idx_av2_domain", "assets", false, "( domain ) WHERE domain IS NOT NULL")
	plan.index("idx_av2_root_domain", "assets", false, "( root_domain ) WHERE root_domain IS NOT NULL")
	plan.index("idx_av2_ip", "assets", false, "( ip ) WHERE ip IS NOT NULL")
	plan.index("idx_av2_c_segment", "assets", false, "USING GIST ( c_segment inet_ops ) WHERE c_segment IS NOT NULL")
	plan.index("idx_av2_technologies", "assets", false, "USING GIN ( technologies ) WHERE type = 'service'")
	plan.index("idx_av2_bound_domains", "assets", false, "USING GIN ( bound_domains ) WHERE type = 'ip'")
	plan.index("idx_av2_open_ports", "assets", false, "USING GIN ( open_ports ) WHERE type = 'ip'")
	plan.index("idx_av2_last_seen", "assets", false, "( last_seen DESC )")
	plan.index("idx_av2_type_seen", "assets", false, "( type , last_seen DESC )")
	plan.raw(`
ALTER TABLE assets ADD COLUMN IF NOT EXISTS company_source TEXT;
UPDATE assets SET company_source = 'explicit' WHERE company_source IS NULL;
ALTER TABLE assets ALTER COLUMN company_source SET DEFAULT 'explicit';
ALTER TABLE assets ALTER COLUMN company_source SET NOT NULL;
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_company_source_check;
ALTER TABLE assets ADD CONSTRAINT assets_company_source_check
    CHECK (company_source IN ('explicit','scope'));
`)
	plan.updatedTrigger("trg_av2_upd", "assets")
	plan.table("company_scope",
		ddlSequence("id"),
		ddlColumn("company_id", ddlBigInt, ddlRequired, ddlReferences("companies", "id", "CASCADE")),
		ddlColumn("kind", ddlText, ddlRequired, ddlCheck("( kind IN ( 'domain' , 'ip' , 'cidr' , 'icp' , 'keyword' ) )")),
		ddlColumn("domain", ddlText),
		ddlColumn("net", ddlCIDR),
		ddlColumn("value", ddlText),
		ddlColumn("raw", ddlText, ddlRequired),
		ddlColumn("reason", ddlText),
		ddlTimestamp("created_at"),
		ddlConstraint("CONSTRAINT uq_sv2_domain UNIQUE ( company_id , domain )"),
		ddlConstraint("CONSTRAINT uq_sv2_net UNIQUE ( company_id , net )"),
		ddlConstraint("CONSTRAINT ck_company_scope_payload CHECK ( ( kind = 'domain' AND domain IS NOT NULL AND net IS NULL AND value IS NULL ) OR ( kind IN ( 'ip' , 'cidr' ) AND domain IS NULL AND net IS NOT NULL AND value IS NULL ) OR ( kind IN ( 'icp' , 'keyword' ) AND domain IS NULL AND net IS NULL AND value IS NOT NULL ) )"),
	)
	plan.raw(`
-- Existing installations need the new text payload and expanded kind check.
ALTER TABLE company_scope ADD COLUMN IF NOT EXISTS value TEXT;
ALTER TABLE company_scope DROP CONSTRAINT IF EXISTS company_scope_kind_check;
ALTER TABLE company_scope ADD CONSTRAINT company_scope_kind_check
    CHECK (kind IN ('domain','ip','cidr','icp','keyword'));
ALTER TABLE company_scope DROP CONSTRAINT IF EXISTS ck_company_scope_payload;
ALTER TABLE company_scope ADD CONSTRAINT ck_company_scope_payload CHECK (
    (kind = 'domain' AND domain IS NOT NULL AND net IS NULL AND value IS NULL)
    OR (kind IN ('ip','cidr') AND domain IS NULL AND net IS NOT NULL AND value IS NULL)
    OR (kind IN ('icp','keyword') AND domain IS NULL AND net IS NULL AND value IS NOT NULL)
);
`)
	plan.index("idx_sv2_domain", "company_scope", false, "( domain ) WHERE kind = 'domain'")
	plan.index("idx_sv2_net", "company_scope", false, "USING GIST ( net inet_ops ) WHERE kind IN ( 'ip' , 'cidr' )")
	plan.index("uq_sv2_value", "company_scope", true, "( company_id , kind , value ) WHERE kind IN ( 'icp' , 'keyword' )")
	plan.index("idx_sv2_icp", "company_scope", false, "( value ) WHERE kind = 'icp'")
	plan.index("idx_sv2_company", "company_scope", false, "( company_id )")
	plan.raw(`

-- =====================================================================
-- B. Reasoning / exploration layer
-- =====================================================================
`)
	plan.table("explorations",
		ddlSequence("id"),
		ddlColumn("description", ddlText),
		ddlColumn("goal", ddlText, ddlRequired),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'open'"), ddlCheck("( status IN ( 'open' , 'achieved' , 'failed' ) )")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.raw(`
-- cold-digest (§2.3): per-task planner round counter — bumped once each time the
-- planner wakes and processes a round. Drives the ≥R cold-node debounce (measured in
-- this exploration's own rounds, not global node ids or wall-clock).
ALTER TABLE explorations ADD COLUMN IF NOT EXISTS round_no BIGINT NOT NULL DEFAULT 0;
`)
	plan.updatedTrigger("trg_exp_upd", "explorations")
	plan.table("exploration_nodes",
		ddlSequence("id"),
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlReferences("explorations", "id", "CASCADE")),
		ddlColumn("kind", ddlText, ddlRequired),
		ddlColumn("payload", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("priority", ddlInt, ddlRequired, ddlZero),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'open'")),
		ddlColumn("origin", ddlText),
		ddlColumn("owner", ddlText),
		ddlColumn("blocked_reason", ddlText),
		ddlColumn("delete_reason", ddlText),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlColumn("completed_at", ddlTimestampTZ),
		ddlConstraint("CONSTRAINT ck_node_kind CHECK ( kind IN ( 'begin' , 'goal' , 'intent' , 'fact' , 'finding' , 'hint' , 'digest' ) )"),
		ddlConstraint("CONSTRAINT ck_node_state CHECK ( ( kind = 'begin' AND state IN ( 'open' ) ) OR ( kind = 'intent' AND state IN ( 'open' , 'running' , 'paused' , 'done' , 'blocked' , 'exhausted' , 'stopped' , 'deleted' ) ) OR ( kind = 'goal' AND state IN ( 'open' , 'met' , 'abandoned' ) ) OR ( kind = 'fact' AND state IN ( 'confirmed' , 'dismissed' , 'origin' ) ) OR ( kind = 'finding' AND state IN ( 'confirmed' , 'dismissed' ) ) OR ( kind = 'hint' AND state IN ( 'active' , 'consumed' ) ) OR ( kind = 'digest' AND state IN ( 'active' , 'superseded' ) ) )"),
	)
	plan.raw(`
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS blocked_reason TEXT;
-- Intent soft delete: when state='deleted', delete_reason records the deletion reason the user entered.
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS delete_reason TEXT;
-- cold-digest (§2.3/§5.3): content_version bumps on any change that could alter a
-- digest body (summary/state/confidence); cold_since_round stamps the planner round
-- a node most recently went from "has a live downstream branch" to none (NULL = hot).
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS content_version  INT    NOT NULL DEFAULT 0;
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS cold_since_round BIGINT;
-- ck_node_kind: existing installs predate the 'digest' kind — recreate to allow it.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='exploration_nodes'::regclass
          AND conname='ck_node_kind'
          AND pg_get_constraintdef(oid) NOT LIKE '%digest%'
    ) THEN
        ALTER TABLE exploration_nodes DROP CONSTRAINT ck_node_kind;
        ALTER TABLE exploration_nodes ADD CONSTRAINT ck_node_kind
            CHECK (kind IN ('begin','goal','intent','fact','finding','hint','digest'));
    END IF;
END $$;
-- ck_node_state: recreate when it lacks the 'paused' (older), 'superseded' (digest rev),
-- or 'deleted' (intent soft-delete rev) branches.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='exploration_nodes'::regclass
          AND conname='ck_node_state'
          AND (pg_get_constraintdef(oid) NOT LIKE '%paused%'
               OR pg_get_constraintdef(oid) NOT LIKE '%superseded%'
               OR pg_get_constraintdef(oid) NOT LIKE '%deleted%')
    ) THEN
        ALTER TABLE exploration_nodes DROP CONSTRAINT ck_node_state;
        ALTER TABLE exploration_nodes ADD CONSTRAINT ck_node_state CHECK (
            (kind='begin'   AND state IN ('open')) OR
            (kind='intent'  AND state IN ('open','running','paused','done','blocked','exhausted','stopped','deleted')) OR
            (kind='goal'    AND state IN ('open','met','abandoned')) OR
            (kind='fact'    AND state IN ('confirmed','dismissed','origin')) OR
            (kind='finding' AND state IN ('confirmed','dismissed')) OR
            (kind='hint'    AND state IN ('active','consumed')) OR
            (kind='digest'  AND state IN ('active','superseded'))
        );
    END IF;
END $$;
`)
	plan.index("idx_expnodes_part", "exploration_nodes", false, "( exploration_id , kind )")
	plan.index("idx_expnodes_frontier", "exploration_nodes", false, "( exploration_id , priority DESC ) WHERE kind = 'intent' AND state = 'open'")
	plan.updatedTrigger("trg_expnodes_upd", "exploration_nodes")
	plan.table("exploration_edges",
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlReferences("explorations", "id", "CASCADE")),
		ddlColumn("src_id", ddlBigInt, ddlRequired, ddlReferences("exploration_nodes", "id", "CASCADE")),
		ddlColumn("dst_id", ddlBigInt, ddlRequired, ddlReferences("exploration_nodes", "id", "CASCADE")),
		ddlColumn("rel", ddlText, ddlRequired),
		ddlTimestamp("created_at"),
		ddlConstraint("PRIMARY KEY ( exploration_id , src_id , rel , dst_id )"),
		ddlConstraint("CONSTRAINT ck_edge_noself CHECK ( src_id <> dst_id )"),
		ddlConstraint("CONSTRAINT ck_edge_rel CHECK ( rel IN ( 'spawns' , 'derived_from' , 'yields' , 'proves' , 'covers' ) )"),
	)
	plan.index("idx_expedges_src", "exploration_edges", false, "( src_id , rel )")
	plan.index("idx_expedges_dst", "exploration_edges", false, "( dst_id , rel )")
	plan.raw(`
-- cold-digest (§1): the 'covers' relation (digest→member) postdates shipped installs,
-- whose rel CHECK is an inline auto-named constraint. Find and recreate it as ck_edge_rel.
DO $$
DECLARE cname text;
BEGIN
    SELECT conname INTO cname FROM pg_constraint
     WHERE conrelid='exploration_edges'::regclass AND contype='c'
       AND pg_get_constraintdef(oid) LIKE '%rel%'
       AND pg_get_constraintdef(oid) NOT LIKE '%covers%';
    IF cname IS NOT NULL THEN
        EXECUTE 'ALTER TABLE exploration_edges DROP CONSTRAINT '||quote_ident(cname);
        ALTER TABLE exploration_edges ADD CONSTRAINT ck_edge_rel
            CHECK (rel IN ('spawns','derived_from','yields','proves','covers'));
    END IF;
END $$;

`)
	plan.table("exploration_anchors",
		ddlColumn("node_id", ddlBigInt, ddlRequired, ddlReferences("exploration_nodes", "id", "CASCADE")),
		ddlColumn("asset_id", ddlBigInt, ddlRequired, ddlReferences("assets", "id", "CASCADE")),
		ddlConstraint("PRIMARY KEY ( node_id , asset_id )"),
	)
	plan.index("idx_anchor_asset", "exploration_anchors", false, "( asset_id )")
	plan.raw(`

-- task_constraints: operator-authored operation constraints (allow/deny) for a task.
-- Extracted by the goals decomposer at round 0 (from goal/description), editable at
-- runtime by the main agent + the Overview "Constraint Management" panel. Injected into the planner/worker system
-- prompt each round (config-gated) to keep exploration within the operator's boundary.
`)
	plan.table("task_constraints",
		ddlSequence("id"),
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlReferences("explorations", "id", "CASCADE")),
		ddlColumn("kind", ddlText, ddlRequired, ddlCheck("( kind IN ( 'allow' , 'deny' ) )")),
		ddlColumn("text", ddlText, ddlRequired),
		ddlColumn("origin", ddlText),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("idx_task_constraints_exp", "task_constraints", false, "( exploration_id )")
	plan.table("activity",
		ddlSequence("id"),
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlReferences("explorations", "id", "CASCADE")),
		ddlColumn("node_id", ddlBigInt, ddlReferences("exploration_nodes", "id", "SET NULL")),
		ddlColumn("worker", ddlText),
		ddlColumn("kind", ddlText),
		ddlColumn("tool", ddlText),
		ddlColumn("tool_use_id", ddlText),
		ddlColumn("is_error", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("summary", ddlText),
		ddlColumn("detail", ddlText),
		ddlColumn("metadata", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("input_tokens", ddlInteger),
		ddlColumn("output_tokens", ddlInteger),
		ddlColumn("cache_read_tokens", ddlInteger),
		ddlColumn("cache_write_tokens", ddlInteger),
		ddlTimestamp("created_at"),
	)
	plan.raw(`
ALTER TABLE activity ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}';
-- main_seg segments the main-agent session into resettable conversations: a new
-- main session bumps the segment so its transcript + activity start clean while the
-- task's graph/assets/goal are untouched. NULL == legacy rows == segment 0 (the
-- original session). Only worker='mainagent' rows carry it.
ALTER TABLE activity ADD COLUMN IF NOT EXISTS main_seg INTEGER;
`)
	plan.index("idx_act_node", "activity", false, "( exploration_id , node_id , id )")
	plan.index("idx_act_since", "activity", false, "( exploration_id , id )")
	plan.index("idx_act_tool_call", "activity", false, "( exploration_id , tool_use_id , id ) WHERE kind IN ( 'tool_use' , 'tool_result' )")
	plan.raw(`
-- Runtime policy reads prior inputs for one tool (for example task-wide source
-- provenance/dedup) without scanning the full activity stream.
`)
	plan.index("idx_act_tool_inputs", "activity", false, "( exploration_id , tool , id DESC ) WHERE kind = 'tool_use'")
	plan.raw(`
-- Main/Plan history pages filter by worker (both carry NULL node_id, so idx_act_node
-- can't distinguish them); this covers reverse pagination of those sessions.
`)
	plan.index("idx_act_worker", "activity", false, "( exploration_id , worker , id )")
	plan.raw(`
-- Main-session pages filter by segment on top of worker='mainagent'; this partial
-- index covers reverse pagination within one segment.
`)
	plan.index("idx_act_main_seg", "activity", false, "( exploration_id , main_seg , id ) WHERE worker = 'mainagent'")
	plan.raw(`
-- Task-list polls aggregate result usage and find the latest event repeatedly.
-- Cover the token columns for index-only aggregation and the timestamp order for
-- per-exploration latest-activity lookups.
`)
	plan.index("idx_act_result_usage", "activity", false, "( exploration_id ) INCLUDE ( input_tokens , output_tokens , cache_read_tokens , cache_write_tokens ) WHERE kind = 'result'")
	plan.index("idx_act_latest", "activity", false, "( exploration_id , created_at DESC )")
	plan.raw(`

-- main_sessions records the resettable main-agent conversation segments of a task.
-- Segment 0 (the original session) is implicit and never stored; this table holds
-- only the extra segments created by "New Session" (seq >= 1). The current segment is
-- MAX(seq) or 0. Each segment gets its own transcript file + activity slice; the
-- task's exploration graph/assets/goal are shared and never reset.
`)
	plan.table("main_sessions",
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlReferences("explorations", "id", "CASCADE")),
		ddlColumn("seq", ddlInteger, ddlRequired),
		ddlTimestamp("created_at"),
		ddlConstraint("PRIMARY KEY ( exploration_id , seq )"),
	)
	plan.raw(`

-- =====================================================================
-- C. LLM profiles
-- =====================================================================
`)
	plan.table("settings",
		ddlColumn("key", ddlText, ddlPrimary),
		ddlColumn("value", ddlText, ddlRequired),
		ddlTimestamp("updated_at"),
	)
	plan.table("llm_profiles",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired, ddlUnique),
		ddlColumn("format", ddlText, ddlRequired, ddlCheck("( format IN ( 'openai' , 'anthropic' , 'openai-responses' ) )")),
		ddlColumn("base_url", ddlText),
		ddlColumn("proxy", ddlText),
		ddlColumn("model", ddlText, ddlRequired),
		ddlColumn("api_key", ddlText),
		ddlColumn("api_key_hint", ddlText),
		ddlColumn("rate_per_second", ddlDouble, ddlRequired, ddlZero),
		ddlColumn("rate_per_minute", ddlDouble, ddlRequired, ddlZero),
		ddlColumn("context_window_k", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("reasoning_effort", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("thinking_type", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("is_default", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("priority", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("pool_exclude", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("streaming", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("max_tokens", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("max_tokens_field", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("session_header_key", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("retry_connect_attempts", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("retry_connect_interval_ms", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("retry_empty_attempts", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("retry_empty_interval_ms", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("retry_stream_attempts", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("retry_stream_interval_ms", ddlInteger, ddlRequired, ddlZero),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("uq_llm_one_default", "llm_profiles", true, "( is_default ) WHERE is_default")
	plan.updatedTrigger("trg_llm_upd", "llm_profiles")
	plan.raw(`
-- Round-robin ordering / exclusion flag; backfill for old DBs. Default 0 / false = all profiles participate in the rotation.
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS priority     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS pool_exclude BOOLEAN NOT NULL DEFAULT false;
-- Streaming switch; backfill for old DBs. Default true = keep existing streaming behavior, old profiles upgrade transparently.
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS streaming    BOOLEAN NOT NULL DEFAULT true;
-- Relax the format constraint to accommodate openai-responses (OpenAI Responses API); backfill for old DBs.
-- Runs on every startup, idempotent: drop the old CHECK then create the new three-value CHECK.
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_format_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_format_check
    CHECK (format IN ('openai','anthropic','openai-responses'));

-- Output cap and its field name; backfill for old DBs. Default 0 / '' = don't send a cap, keep the max_tokens field name,
-- old profiles' behavior unchanged.
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_tokens       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_tokens_field TEXT    NOT NULL DEFAULT '';
-- Same as format: drop then create, ensuring idempotency on every startup.
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_max_tokens_field_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_max_tokens_field_check
    CHECK (max_tokens_field IN ('','max_completion_tokens'));
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_max_tokens_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_max_tokens_check
    CHECK (max_tokens >= 0);
-- Custom session header name; backfill for old DBs. Default '' = don't send, old profiles' behavior unchanged.
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS session_header_key TEXT NOT NULL DEFAULT '';

-- Per-profile retry overrides (see docs/LLM-retry-design.md). Three groups, each a pair of "attempts + fixed interval",
-- with uniform semantics: attempts 0=use the global default, -1=disable that retry layer, >0=use this value; interval 0=use that
-- layer's default exponential backoff, >0=use this fixed millisecond value. All default to 0, so old DBs/profiles behave unchanged.
--   connect = connect retry (SDK doStream: connection reset/timeout/429/5xx, before the stream starts)
--   empty   = empty-response retry (SDK: completed but with no content block at all, openai format only)
--   stream  = same-provider safe-window retry (this project's task_llm: replay on a dropped stream before any output was delivered)
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_connect_attempts    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_connect_interval_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_empty_attempts      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_empty_interval_ms   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_stream_attempts     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS retry_stream_interval_ms  INTEGER NOT NULL DEFAULT 0;
-- Same as format: drop then create, ensuring idempotency on every startup. Attempts floor is -1 (disable); interval cannot be negative.
ALTER TABLE llm_profiles DROP CONSTRAINT IF EXISTS llm_profiles_retry_check;
ALTER TABLE llm_profiles ADD  CONSTRAINT llm_profiles_retry_check CHECK (
    retry_connect_attempts >= -1 AND retry_empty_attempts >= -1 AND retry_stream_attempts >= -1
    AND retry_connect_interval_ms >= 0 AND retry_empty_interval_ms >= 0 AND retry_stream_interval_ms >= 0);

-- The thinking-switch field thinking_type, split once-and-for-all from the old single reasoning_effort semantics.
-- schema.sql runs on every startup, so the migration must run only once: backfill only when the column does not yet exist,
-- otherwise every startup would overwrite the combination the user later set manually. Old reasoning_effort semantics:
--   'off'                    → explicitly off  → thinking_type='disabled', strength cleared
--   'low/medium/high/max'    → on + strength   → thinking_type='enabled', strength kept
--   ''                       → don't send      → both empty (default)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'llm_profiles' AND column_name = 'thinking_type'
    ) THEN
        ALTER TABLE llm_profiles ADD COLUMN thinking_type TEXT NOT NULL DEFAULT '';
        UPDATE llm_profiles SET thinking_type = 'enabled'
            WHERE reasoning_effort IN ('low','medium','high','max');
        UPDATE llm_profiles SET thinking_type = 'disabled', reasoning_effort = ''
            WHERE reasoning_effort = 'off';
    END IF;
END $$;

-- LLM round-robin circuit-breaker state: after a profile fails repeatedly (insufficient balance / invalid key / rate limit) it enters
-- cooldown, and during cooldown the rotation skips it outright. The in-memory state is authoritative; persisting here only avoids losing
-- the cooldown window across restarts — on load we take only rows not yet expired (open_until > now); expired ones naturally return to
-- "normal" and wait for the next call's half-open probe.
`)
	plan.table("llm_profile_health",
		ddlColumn("profile_id", ddlBigInt, ddlPrimary, ddlReferences("llm_profiles", "id", "CASCADE")),
		ddlColumn("fails", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("trips", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("open_until", ddlTimestampTZ),
		ddlColumn("last_error", ddlText, ddlRequired, ddlEmpty),
		ddlTimestamp("last_at"),
	)
	plan.raw(`

-- =====================================================================
-- D. Task layer
-- =====================================================================
-- Global task categories are intentionally independent from task templates.
-- Deleting a category only moves its tasks back to the uncategorized bucket.
`)
	plan.table("task_categories",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired),
		ddlColumn("nkey", ddlText, ddlRequired, ddlUnique),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("idx_task_categories_name", "task_categories", false, "( name , id )")
	plan.updatedTrigger("trg_task_categories_upd", "task_categories")
	plan.table("tasks",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("category_id", ddlBigInt, ddlReferences("task_categories", "id", "SET NULL")),
		ddlColumn("description", ddlText, ddlRequired),
		ddlColumn("goal", ddlText, ddlRequired),
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlUnique, ddlReferences("explorations", "id", "RESTRICT")),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'created'"), ddlCheck("( status IN ( 'created' , 'running' , 'paused' , 'done' , 'failed' , 'timeout' ) )")),
		ddlColumn("paused", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("queued", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("queued_at", ddlTimestampTZ),
		ddlColumn("queue_mode", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("llm_profile_id", ddlBigInt, ddlReferences("llm_profiles", "id", "SET NULL")),
		ddlColumn("active_llm_profile_id", ddlBigInt, ddlReferences("llm_profiles", "id", "SET NULL")),
		ddlColumn("llm_chain_revision", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("company_id", ddlBigInt, ddlReferences("companies", "id", "SET NULL")),
		ddlColumn("parent_ref", ddlText),
		ddlColumn("timeout_seconds", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("plan_heartbeat_seconds", ddlInteger, ddlRequired, ddlDefault("300")),
		ddlColumn("coverage_enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("strict_isolation", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("skip_initial_katana", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("initial_scout", ddlBoolean),
		ddlColumn("pinned_at", ddlTimestampTZ),
		ddlColumn("first_run_at", ddlTimestampTZ),
		ddlColumn("deadline_at", ddlTimestampTZ),
		ddlColumn("archived_at", ddlTimestampTZ),
		ddlColumn("deleted_at", ddlTimestampTZ),
		ddlColumn("completed_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("idx_tasks_alive", "tasks", false, "( created_at DESC ) WHERE deleted_at IS NULL")
	plan.index("idx_tasks_status", "tasks", false, "( status ) WHERE deleted_at IS NULL")
	plan.updatedTrigger("trg_tasks_upd", "tasks")
	plan.raw(`
-- planner heartbeat trigger interval (seconds); backfill for old DBs. Default 300s (5min). See docs/planner-trigger-impl-plan.md
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS plan_heartbeat_seconds INTEGER NOT NULL DEFAULT 300;
-- Concurrency-limit pending state; backfill for old DBs. true = queued due to the concurrency limit, waiting for a free slot to auto-start.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queued BOOLEAN NOT NULL DEFAULT false;
-- Asset-coverage feature switch; backfill for old DBs. true (default) = compute/display test coverage, auto-accumulate the test scope,
-- expose add_task_scope/list_untested_assets to the agent; false = all off (see task_scope.go).
-- Existing tasks default to true to preserve prior behavior; company association (task_scope kind=company) is unaffected by this switch.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS coverage_enabled BOOLEAN NOT NULL DEFAULT true;
-- Strict tasks see only assets associated with their own task id. This prevents
-- benchmark/control runs from consuming discoveries stored by unrelated tasks.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS strict_isolation BOOLEAN NOT NULL DEFAULT false;
-- Per-operation startup choices. Existing operations retain automatic Katana
-- and the workspace scout default. These fields never deny explicit tool use.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS skip_initial_katana BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS initial_scout BOOLEAN;
-- queued_at makes admission FIFO reflect the actual enqueue order rather than the
-- task creation order. queue_mode distinguishes first bootstrap from resuming an
-- exploration that already owns goals/history.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queued_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS queue_mode TEXT NOT NULL DEFAULT '';
-- Optional task name; backfill for old DBs. Empty string = unnamed; the frontend falls back to the description when displaying.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS category_id BIGINT REFERENCES task_categories(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS active_llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS llm_chain_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
`)
	plan.index("idx_tasks_category", "tasks", false, "( category_id , created_at DESC ) WHERE deleted_at IS NULL AND category_id IS NOT NULL")
	plan.index("idx_tasks_pinned", "tasks", false, "( pinned_at DESC ) WHERE deleted_at IS NULL AND pinned_at IS NOT NULL")
	plan.index("idx_tasks_archived", "tasks", false, "( archived_at DESC ) WHERE archived_at IS NOT NULL")
	plan.raw(`

-- Cold task archives retain only compact metadata in PostgreSQL. The complete
-- task payload lives in a versioned .tar.zst package under data/archives/tasks.
-- task_id stays unique so an operation can be retried safely after a restart.
`)
	plan.table("task_archives",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlUnique, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'archive_queued'"), ddlCheck("( state IN ( 'archive_queued' , 'archiving' , 'archive_failed' , 'ready' , 'restore_queued' , 'restoring' , 'restore_failed' , 'delete_queued' , 'deleting' , 'delete_failed' ) )")),
		ddlColumn("phase", ddlText, ddlRequired, ddlDefault("'queued'")),
		ddlColumn("progress", ddlInteger, ddlRequired, ddlZero, ddlCheck("( progress BETWEEN 0 AND 100 )")),
		ddlColumn("error", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("warnings", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("format_version", ddlInteger, ddlRequired, ddlDefault("5")),
		ddlColumn("archive_path", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("original_size", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("compressed_size", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("task_name", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("task_description", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("task_goal", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("original_status", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("category_id_snapshot", ddlBigInt),
		ddlColumn("category_name_snapshot", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("source_task_ids", ddlBigIntArray, ddlRequired, ddlEmptyObject),
		ddlColumn("remaining_timeout_seconds", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("data_counts", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("aggregate_stats", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("archived_at", ddlTimestampTZ),
		ddlTimestamp("requested_at"),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.raw(`
ALTER TABLE task_archives ALTER COLUMN format_version SET DEFAULT 10;
`)
	plan.index("idx_task_archives_state", "task_archives", false, "( state , requested_at , id )")
	plan.index("idx_task_archives_archived", "task_archives", false, "( archived_at DESC , id DESC )")
	plan.index("idx_task_archives_sources", "task_archives", false, "USING GIN ( source_task_ids )")
	plan.updatedTrigger("trg_task_archives_upd", "task_archives")
	plan.raw(`

-- B0 binary-artifact contract. PostgreSQL stores identity, provenance,
-- lifecycle, bounded projections, and the desired broker references only. Raw
-- artifact bytes and raw analyzer output belong to the private broker volume
-- and must never be inserted into these tables.
`)
	plan.table("binary_blobs",
		ddlColumn("sha256", ddlText, ddlPrimary, ddlCheck("( sha256 ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("byte_size", ddlBigInt, ddlRequired, ddlCheck("( byte_size BETWEEN 0 AND 536870912 )")),
		ddlColumn("storage_version", ddlInteger, ddlRequired, ddlDefault("1"), ddlCheck("( storage_version = 1 )")),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'staged'"), ddlCheck("( state IN ( 'staged' , 'available' , 'missing' , 'unreferenced' ) )")),
		ddlColumn("unreferenced_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("CHECK ( ( state = 'unreferenced' ) = ( unreferenced_at IS NOT NULL ) )"),
	)
	plan.index("idx_binary_blobs_unreferenced", "binary_blobs", false, "( unreferenced_at ) WHERE state = 'unreferenced'")
	plan.updatedTrigger("trg_binary_blobs_upd", "binary_blobs")
	plan.table("binary_artifacts",
		ddlColumn("artifact_id", ddlText, ddlPrimary, ddlCheck("( artifact_id ~ '^artifact-[0-9a-f]{32}$' )")),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("blob_sha256", ddlText, ddlRequired, ddlReferences("binary_blobs", "sha256", "RESTRICT")),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'pending'"), ddlCheck("( state IN ( 'pending' , 'ready' , 'quarantined' , 'unsupported' ) )")),
		ddlColumn("quarantine_state", ddlText, ddlRequired, ddlDefault("'pending'"), ddlCheck("( quarantine_state IN ( 'pending' , 'accepted' , 'rejected' , 'missing' ) )")),
		ddlColumn("detected_kind", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("binary_format", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("architecture", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("bitness", ddlInteger, ddlCheck("( bitness IS NULL OR bitness IN ( 32 , 64 ) )")),
		ddlColumn("endianness", ddlText, ddlRequired, ddlEmpty, ddlCheck("( endianness IN ( '' , 'little' , 'big' ) )")),
		ddlColumn("retention_class", ddlText, ddlRequired, ddlDefault("'standard'"), ddlCheck("( retention_class IN ( 'standard' , 'hold' ) )")),
		ddlColumn("execution_allowed", ddlBoolean, ddlRequired, ddlFalse, ddlCheck("( execution_allowed = false )")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("UNIQUE ( task_id , blob_sha256 )"),
		ddlConstraint("UNIQUE ( artifact_id , task_id , blob_sha256 )"),
		ddlConstraint("UNIQUE ( artifact_id , task_id )"),
		ddlConstraint("CHECK ( octet_length ( detected_kind ) <= 64 )"),
		ddlConstraint("CHECK ( octet_length ( binary_format ) <= 64 )"),
		ddlConstraint("CHECK ( octet_length ( architecture ) <= 128 )"),
	)
	plan.index("idx_binary_artifacts_task", "binary_artifacts", false, "( task_id , created_at , artifact_id )")
	plan.updatedTrigger("trg_binary_artifacts_upd", "binary_artifacts")
	plan.table("binary_artifact_origins",
		ddlSequence("id"),
		ddlColumn("artifact_id", ddlText, ddlRequired),
		ddlColumn("task_id", ddlBigInt, ddlRequired),
		ddlColumn("origin_kind", ddlText, ddlRequired, ddlCheck("( origin_kind IN ( 'traffic_response' , 'operator_upload' , 'repository_build' ) )")),
		ddlColumn("traffic_id", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("producer_kind", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("producer_job_id", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("sanitized_url", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("sanitized_name", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("content_type", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("provenance_hash", ddlText, ddlRequired, ddlCheck("( provenance_hash ~ '^[0-9a-f]{64}$' )")),
		ddlTimestamp("created_at"),
		ddlConstraint("UNIQUE ( artifact_id , provenance_hash )"),
		ddlConstraint("FOREIGN KEY ( artifact_id , task_id ) REFERENCES binary_artifacts ( artifact_id , task_id ) ON DELETE CASCADE"),
		ddlConstraint("CHECK ( origin_kind <> 'traffic_response' OR traffic_id <> '' )"),
		ddlConstraint("CHECK ( octet_length ( traffic_id ) <= 256 )"),
		ddlConstraint("CHECK ( octet_length ( producer_kind ) <= 64 )"),
		ddlConstraint("CHECK ( octet_length ( producer_job_id ) <= 256 )"),
		ddlConstraint("CHECK ( octet_length ( sanitized_url ) <= 4096 )"),
		ddlConstraint("CHECK ( octet_length ( sanitized_name ) <= 512 )"),
		ddlConstraint("CHECK ( octet_length ( content_type ) <= 255 )"),
	)
	plan.index("idx_binary_artifact_origins_task", "binary_artifact_origins", false, "( task_id , artifact_id , id )")
	plan.table("binary_triage_jobs",
		ddlColumn("job_id", ddlText, ddlPrimary, ddlCheck("( job_id ~ '^binary-job-[0-9a-f]{32}$' )")),
		ddlColumn("task_id", ddlBigInt, ddlRequired),
		ddlColumn("artifact_id", ddlText, ddlRequired),
		ddlColumn("profile_revision", ddlText, ddlRequired, ddlCheck("( profile_revision = 'standard-v1' )")),
		ddlColumn("fingerprint", ddlText, ddlRequired, ddlCheck("( fingerprint ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("worker_image_digest", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("tool_rule_pins", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("limit_profile", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'queued'"), ddlCheck("( status IN ( 'queued' , 'running' , 'completed' , 'partial' , 'unsupported' , 'resource_gated' , 'failed' , 'canceled' , 'interrupted' ) )")),
		ddlColumn("progress", ddlInteger, ddlRequired, ddlZero, ddlCheck("( progress BETWEEN 0 AND 100 )")),
		ddlColumn("lease_nonce", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("heartbeat_at", ddlTimestampTZ),
		ddlColumn("exit_class", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("error_class", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("error", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("raw_result_hash", ddlText, ddlRequired, ddlEmpty, ddlCheck("( raw_result_hash = '' OR raw_result_hash ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("canonical_projection_hash", ddlText, ddlRequired, ddlEmpty, ddlCheck("( canonical_projection_hash = '' OR canonical_projection_hash ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("started_at", ddlTimestampTZ),
		ddlColumn("finished_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("UNIQUE ( job_id , task_id , artifact_id )"),
		ddlConstraint("FOREIGN KEY ( artifact_id , task_id ) REFERENCES binary_artifacts ( artifact_id , task_id ) ON DELETE CASCADE"),
		ddlConstraint("CHECK ( jsonb_typeof ( tool_rule_pins ) = 'object' )"),
		ddlConstraint("CHECK ( jsonb_typeof ( limit_profile ) = 'object' )"),
		ddlConstraint("CHECK ( octet_length ( tool_rule_pins :: text ) <= 65536 )"),
		ddlConstraint("CHECK ( octet_length ( limit_profile :: text ) <= 65536 )"),
		ddlConstraint("CHECK ( octet_length ( worker_image_digest ) <= 256 )"),
		ddlConstraint("CHECK ( octet_length ( lease_nonce ) <= 256 )"),
		ddlConstraint("CHECK ( octet_length ( exit_class ) <= 128 )"),
		ddlConstraint("CHECK ( octet_length ( error_class ) <= 128 )"),
		ddlConstraint("CHECK ( octet_length ( error ) <= 65536 )"),
	)
	plan.index("idx_binary_triage_jobs_task", "binary_triage_jobs", false, "( task_id , created_at DESC , job_id )")
	plan.index("uq_binary_triage_jobs_active_fingerprint", "binary_triage_jobs", true, "( artifact_id , fingerprint ) WHERE status IN ( 'queued' , 'running' )")
	plan.index("uq_binary_triage_jobs_completed_fingerprint", "binary_triage_jobs", true, "( artifact_id , fingerprint ) WHERE status = 'completed'")
	plan.updatedTrigger("trg_binary_triage_jobs_upd", "binary_triage_jobs")
	plan.table("binary_observations",
		ddlSequence("id"),
		ddlColumn("job_id", ddlText, ddlRequired),
		ddlColumn("task_id", ddlBigInt, ddlRequired),
		ddlColumn("artifact_id", ddlText, ddlRequired),
		ddlColumn("analyzer", ddlText, ddlRequired),
		ddlColumn("kind", ddlText, ddlRequired),
		ddlColumn("stable_key", ddlText, ddlRequired),
		ddlColumn("authority", ddlText, ddlRequired, ddlCheck("( authority IN ( 'metadata' , 'candidate' ) )")),
		ddlColumn("address", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("file_offset", ddlBigInt, ddlCheck("( file_offset IS NULL OR file_offset >= 0 )")),
		ddlColumn("record", ddlJSONB, ddlRequired),
		ddlColumn("tool_rule_identity", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("truncated", ddlBoolean, ddlRequired, ddlFalse),
		ddlTimestamp("created_at"),
		ddlConstraint("UNIQUE ( job_id , stable_key )"),
		ddlConstraint("FOREIGN KEY ( job_id , task_id , artifact_id ) REFERENCES binary_triage_jobs ( job_id , task_id , artifact_id ) ON DELETE CASCADE"),
		ddlConstraint("CHECK ( jsonb_typeof ( tool_rule_identity ) = 'object' )"),
		ddlConstraint("CHECK ( octet_length ( record :: text ) <= 2097152 )"),
		ddlConstraint("CHECK ( octet_length ( tool_rule_identity :: text ) <= 65536 )"),
		ddlConstraint("CHECK ( octet_length ( analyzer ) <= 64 )"),
		ddlConstraint("CHECK ( octet_length ( kind ) <= 128 )"),
		ddlConstraint("CHECK ( octet_length ( stable_key ) <= 512 )"),
		ddlConstraint("CHECK ( octet_length ( address ) <= 128 )"),
	)
	plan.index("idx_binary_observations_page", "binary_observations", false, "( task_id , job_id , id )")
	plan.raw(`
CREATE OR REPLACE FUNCTION enforce_binary_projection_budget() RETURNS trigger AS $$
DECLARE
    used_bytes BIGINT;
    next_bytes BIGINT;
BEGIN
    -- Serialize writers for one job so concurrent inserts cannot each observe
    -- a value below the aggregate ceiling and jointly exceed it.
    PERFORM 1 FROM binary_triage_jobs WHERE job_id=NEW.job_id FOR UPDATE;
    SELECT COALESCE(sum(octet_length(record::text)+octet_length(tool_rule_identity::text)),0)
      INTO used_bytes
      FROM binary_observations
     WHERE job_id=NEW.job_id AND id<>NEW.id;
    next_bytes := octet_length(NEW.record::text)+octet_length(NEW.tool_rule_identity::text);
    IF used_bytes + next_bytes > 2097152 THEN
        RAISE EXCEPTION 'binary canonical projection exceeds 2 MiB job ceiling'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_binary_observations_budget ON binary_observations;
CREATE TRIGGER trg_binary_observations_budget
    BEFORE INSERT OR UPDATE OF job_id,record,tool_rule_identity ON binary_observations
    FOR EACH ROW EXECUTE FUNCTION enforce_binary_projection_budget();

-- Desired-state references are split by owner so PostgreSQL can enforce both
-- foreign keys. The broker reconciler adds a destination before removing a
-- source, making hot<->cold transitions safe across process crashes.
`)
	plan.table("binary_artifact_refs",
		ddlColumn("artifact_id", ddlText, ddlPrimary),
		ddlColumn("task_id", ddlBigInt, ddlRequired),
		ddlColumn("blob_sha256", ddlText, ddlRequired),
		ddlColumn("broker_ref_id", ddlText, ddlRequired, ddlUnique, ddlCheck("( broker_ref_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$' )")),
		ddlColumn("receipt_id", ddlText, ddlRequired, ddlEmpty, ddlCheck("( receipt_id = '' OR receipt_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$' )")),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'pending'"), ddlCheck("( state IN ( 'pending' , 'active' , 'missing' ) )")),
		ddlColumn("last_error", ddlText, ddlRequired, ddlEmpty, ddlCheck("( octet_length ( last_error ) <= 4096 )")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("FOREIGN KEY ( artifact_id , task_id , blob_sha256 ) REFERENCES binary_artifacts ( artifact_id , task_id , blob_sha256 ) ON DELETE CASCADE"),
	)
	plan.index("idx_binary_artifact_refs_blob", "binary_artifact_refs", false, "( blob_sha256 )")
	plan.updatedTrigger("trg_binary_artifact_refs_upd", "binary_artifact_refs")
	plan.table("binary_archive_refs",
		ddlColumn("archive_id", ddlBigInt, ddlRequired, ddlReferences("task_archives", "id", "CASCADE")),
		ddlColumn("artifact_id", ddlText, ddlRequired, ddlCheck("( artifact_id ~ '^artifact-[0-9a-f]{32}$' )")),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlCheck("( task_id > 0 )")),
		ddlColumn("blob_sha256", ddlText, ddlRequired, ddlReferences("binary_blobs", "sha256", "RESTRICT")),
		ddlColumn("broker_ref_id", ddlText, ddlRequired, ddlUnique, ddlCheck("( broker_ref_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$' )")),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'pending'"), ddlCheck("( state IN ( 'pending' , 'active' , 'missing' ) )")),
		ddlColumn("last_error", ddlText, ddlRequired, ddlEmpty, ddlCheck("( octet_length ( last_error ) <= 4096 )")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("PRIMARY KEY ( archive_id , artifact_id )"),
	)
	plan.index("idx_binary_archive_refs_blob", "binary_archive_refs", false, "( blob_sha256 )")
	plan.index("idx_binary_archive_refs_task", "binary_archive_refs", false, "( task_id , archive_id )")
	plan.updatedTrigger("trg_binary_archive_refs_upd", "binary_archive_refs")
	plan.raw(`

CREATE OR REPLACE FUNCTION refresh_binary_blob_state() RETURNS trigger AS $$
DECLARE
    target_hash TEXT;
    target_hashes TEXT[];
    next_state TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_hashes := ARRAY[OLD.blob_sha256];
    ELSIF TG_OP = 'UPDATE' AND OLD.blob_sha256 IS DISTINCT FROM NEW.blob_sha256 THEN
        target_hashes := ARRAY[OLD.blob_sha256,NEW.blob_sha256];
    ELSE
        target_hashes := ARRAY[NEW.blob_sha256];
    END IF;
    FOREACH target_hash IN ARRAY target_hashes LOOP
        IF EXISTS (
            SELECT 1 FROM binary_artifact_refs WHERE blob_sha256=target_hash AND state='active'
            UNION ALL
            SELECT 1 FROM binary_archive_refs WHERE blob_sha256=target_hash AND state='active'
        ) THEN next_state := 'available';
        ELSIF EXISTS (
            SELECT 1 FROM binary_artifact_refs WHERE blob_sha256=target_hash AND state='pending'
            UNION ALL
            SELECT 1 FROM binary_archive_refs WHERE blob_sha256=target_hash AND state='pending'
        ) THEN next_state := 'staged';
        ELSIF EXISTS (
            SELECT 1 FROM binary_artifact_refs WHERE blob_sha256=target_hash
            UNION ALL
            SELECT 1 FROM binary_archive_refs WHERE blob_sha256=target_hash
        ) THEN next_state := 'missing';
        ELSE next_state := 'unreferenced';
        END IF;
        UPDATE binary_blobs SET
            state=next_state,
            unreferenced_at=CASE
                WHEN next_state='unreferenced' THEN COALESCE(unreferenced_at,now())
                ELSE NULL
            END
        WHERE sha256=target_hash;
    END LOOP;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_binary_artifact_refs_blob_state ON binary_artifact_refs;
CREATE TRIGGER trg_binary_artifact_refs_blob_state
    AFTER INSERT OR DELETE OR UPDATE OF state,blob_sha256 ON binary_artifact_refs
    FOR EACH ROW EXECUTE FUNCTION refresh_binary_blob_state();
DROP TRIGGER IF EXISTS trg_binary_archive_refs_blob_state ON binary_archive_refs;
CREATE TRIGGER trg_binary_archive_refs_blob_state
    AFTER INSERT OR DELETE OR UPDATE OF state,blob_sha256 ON binary_archive_refs
    FOR EACH ROW EXECUTE FUNCTION refresh_binary_blob_state();

-- Reusable task description/goal presets. nkey is the normalized, case-insensitive
-- identity used to reject visually equivalent duplicate names.
`)
	plan.table("task_templates",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired),
		ddlColumn("nkey", ddlText, ddlRequired, ddlUnique),
		ddlColumn("description", ddlText, ddlRequired),
		ddlColumn("goal", ddlText, ddlRequired),
		ddlColumn("category_id", ddlBigInt, ddlReferences("task_categories", "id", "SET NULL")),
		ddlColumn("intercept_rules", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.raw(`
-- Backfill for old DBs (already released; add columns with IF NOT EXISTS).
ALTER TABLE task_templates ADD COLUMN IF NOT EXISTS category_id BIGINT REFERENCES task_categories(id) ON DELETE SET NULL;
ALTER TABLE task_templates ADD COLUMN IF NOT EXISTS intercept_rules JSONB NOT NULL DEFAULT '[]';
`)
	plan.index("idx_task_templates_updated", "task_templates", false, "( updated_at DESC , id DESC )")
	plan.updatedTrigger("trg_task_templates_upd", "task_templates")
	plan.raw(`

-- Direct, read-only task context inheritance. Relations are intentionally not
-- recursive: a task sees only the source tasks explicitly chosen at creation.
`)
	plan.table("task_relations",
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("source_task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlTimestamp("created_at"),
		ddlConstraint("PRIMARY KEY ( task_id , source_task_id )"),
		ddlConstraint("CONSTRAINT ck_task_relation_not_self CHECK ( task_id <> source_task_id )"),
	)
	plan.index("idx_task_relations_source", "task_relations", false, "( source_task_id )")
	plan.raw(`

-- Task/asset provenance supplements the legacy assets.task_ids association. The
-- array remains the compatibility source for existing query and cleanup paths;
-- this relation records how each association was obtained for operator review.
`)
	plan.table("task_asset_links",
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("asset_id", ddlBigInt, ddlRequired, ddlReferences("assets", "id", "CASCADE")),
		ddlColumn("source", ddlText, ddlRequired, ddlDefault("'system'")),
		ddlColumn("source_summary", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("source_node_id", ddlBigInt, ddlReferences("exploration_nodes", "id", "SET NULL")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("PRIMARY KEY ( task_id , asset_id )"),
	)
	plan.index("idx_task_asset_links_asset", "task_asset_links", false, "( asset_id , task_id )")
	plan.index("idx_task_asset_links_node", "task_asset_links", false, "( source_node_id ) WHERE source_node_id IS NOT NULL")
	plan.updatedTrigger("trg_task_asset_links_upd", "task_asset_links")
	plan.raw(`

-- Keep provenance rows synchronized when existing asset upsert paths append or
-- remove task ids. Detailed callers overwrite the generic source after upsert.
CREATE OR REPLACE FUNCTION sync_task_asset_links() RETURNS trigger AS $$
BEGIN
    INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
    SELECT task.id, NEW.id, 'system', 'auto-linked during task execution'
    FROM unnest(NEW.task_ids) AS requested(task_id)
    JOIN tasks task ON task.id=requested.task_id AND task.deleted_at IS NULL
    ON CONFLICT (task_id, asset_id) DO NOTHING;

    DELETE FROM task_asset_links link
    WHERE link.asset_id=NEW.id
      AND NOT (link.task_id=ANY(NEW.task_ids));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_assets_task_links ON assets;
CREATE TRIGGER trg_assets_task_links AFTER INSERT OR UPDATE OF task_ids ON assets
    FOR EACH ROW EXECUTE FUNCTION sync_task_asset_links();

-- Existing installations receive an auditable legacy source without rewriting
-- task_ids. Ignore stale array ids that no longer resolve to a live task.
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT task.id, asset.id, 'legacy', 'migrated from legacy task-asset links'
FROM assets asset
CROSS JOIN LATERAL unnest(asset.task_ids) AS requested(task_id)
JOIN tasks task ON task.id=requested.task_id AND task.deleted_at IS NULL
ON CONFLICT (task_id, asset_id) DO NOTHING;

-- Ordered task-level LLM failover chain. A quota-exhausted entry is skipped
-- until the user saves/resets the chain, which clears all failure state.
`)
	plan.table("task_llm_profiles",
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("profile_id", ddlBigInt, ddlRequired, ddlReferences("llm_profiles", "id", "CASCADE")),
		ddlColumn("position", ddlInteger, ddlRequired, ddlCheck("( position >= 0 )")),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'ready'"), ddlCheck("( status IN ( 'ready' , 'quota_exhausted' ) )")),
		ddlColumn("last_error", ddlText),
		ddlColumn("exhausted_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("PRIMARY KEY ( task_id , profile_id )"),
		ddlConstraint("UNIQUE ( task_id , position )"),
	)
	plan.index("idx_task_llm_profiles_order", "task_llm_profiles", false, "( task_id , position )")
	plan.index("idx_task_llm_profiles_profile", "task_llm_profiles", false, "( profile_id , task_id )")
	plan.index("idx_tasks_llm_profile", "tasks", false, "( llm_profile_id ) WHERE llm_profile_id IS NOT NULL")
	plan.index("idx_tasks_active_llm_profile", "tasks", false, "( active_llm_profile_id ) WHERE active_llm_profile_id IS NOT NULL")
	plan.updatedTrigger("trg_task_llm_profiles_upd", "task_llm_profiles")
	plan.raw(`

-- One-time-compatible backfill: old pinned tasks become one-entry chains. A user
-- can still clear the chain later because the update path also clears the legacy
-- llm_profile_id column, preventing this block from re-adding it on restart.
INSERT INTO task_llm_profiles(task_id, profile_id, position)
SELECT t.id, t.llm_profile_id, 0
FROM tasks t
WHERE t.llm_profile_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM task_llm_profiles x WHERE x.task_id=t.id)
ON CONFLICT DO NOTHING;
UPDATE tasks t
SET active_llm_profile_id = t.llm_profile_id
WHERE t.active_llm_profile_id IS NULL
  AND t.llm_profile_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM task_llm_profiles x WHERE x.task_id=t.id AND x.profile_id=t.llm_profile_id);

-- Task test scope (the denominator of asset coverage + the authorization boundary).
--   Auto-filled (source='auto'): trusted initial targets keep conservative type-matched scope;
--     insertAssets and worker targets add only domain authority (root_domain→root_domain,
--     subdomain/service/endpoint→subdomain(host)). Discovered IPs and IP-literal services remain
--     task-linked evidence and require a separate explicit IP/CIDR authorization decision;
--     side-effect-derived assets are not added to scope (the hook is at the top of the handler, derivation happens inside the db layer).
--   Agent-filled (source='agent'): add_task_scope adds company/root_domain/subdomain/ip.
-- Coverage = of the assets matching active rows (the denominator), the fraction anchored by a fact node (the numerator).
`)
	plan.table("task_scope",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("kind", ddlText, ddlRequired, ddlCheck("( kind IN ( 'company' , 'root_domain' , 'subdomain' , 'ip' , 'cidr' , 'icp' , 'keyword' ) )")),
		ddlColumn("company_id", ddlBigInt, ddlReferences("companies", "id", "CASCADE")),
		ddlColumn("domain", ddlText),
		ddlColumn("net", ddlCIDR),
		ddlColumn("value", ddlText),
		ddlColumn("source", ddlText, ddlRequired, ddlDefault("'auto'"), ddlCheck("( source IN ( 'auto' , 'agent' , 'manual' ) )")),
		ddlColumn("reason", ddlText),
		ddlTimestamp("created_at"),
	)
	plan.raw(`
-- Old-DB upgrade: extend the task scope so it matches the single-textbox recognition capability of the company scope.
ALTER TABLE task_scope ADD COLUMN IF NOT EXISTS value TEXT;
ALTER TABLE task_scope DROP CONSTRAINT IF EXISTS task_scope_kind_check;
ALTER TABLE task_scope ADD CONSTRAINT task_scope_kind_check
    CHECK (kind IN ('company','root_domain','subdomain','ip','cidr','icp','keyword'));
-- Deduplication: the same scope entry for the same task is stored only once (auto-fill batch inserts rely on this for idempotency).
DROP INDEX IF EXISTS uq_task_scope;
`)
	plan.index("uq_task_scope_v2", "task_scope", true, "( task_id , kind , COALESCE ( domain , '' ) , COALESCE ( net :: text , '' ) , COALESCE ( company_id , 0 ) , COALESCE ( value , '' ) )")
	plan.index("idx_ts_domain", "task_scope", false, "( domain ) WHERE kind IN ( 'root_domain' , 'subdomain' )")
	plan.index("idx_ts_net", "task_scope", false, "USING GIST ( net inet_ops ) WHERE kind IN ( 'ip' , 'cidr' )")
	plan.index("idx_ts_company", "task_scope", false, "( company_id ) WHERE kind = 'company'")
	plan.raw(`

-- Task-bound Katana discovery runs. The frozen scope/config are stored with the
-- run so later task-scope edits cannot retroactively change what was authorized.
-- A discovery is evidence/candidate metadata, never a vulnerability finding.
`)
	plan.table("katana_jobs",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("source", ddlText, ddlRequired, ddlDefault("'auto'"), ddlCheck("( source IN ( 'auto' , 'tool' ) )")),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'queued'"), ddlCheck("( status IN ( 'queued' , 'running' , 'completed' , 'failed' , 'canceled' , 'interrupted' ) )")),
		ddlColumn("mode", ddlText, ddlRequired, ddlDefault("'standard'"), ddlCheck("( mode IN ( 'standard' , 'headless' , 'hybrid' ) )")),
		ddlColumn("config", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("config_hash", ddlText, ddlRequired),
		ddlColumn("scope", ddlJSONB, ddlRequired),
		ddlColumn("scope_hash", ddlText, ddlRequired),
		ddlColumn("seeds", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("seed_hash", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("binary_version", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("exit_code", ddlInteger),
		ddlColumn("error", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("stderr", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("stats", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("candidate_count", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("verified_count", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("started_at", ddlTimestampTZ),
		ddlColumn("finished_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.raw(`
-- Old feature-build upgrade: seed_hash fingerprints only scheme+host+port so
-- automatic discovery reacts to a newly found HTTP origin, not every new path.
ALTER TABLE katana_jobs ADD COLUMN IF NOT EXISTS seed_hash TEXT NOT NULL DEFAULT '';
`)
	plan.index("idx_katana_jobs_task", "katana_jobs", false, "( task_id , id DESC )")
	plan.index("uq_katana_jobs_id_task", "katana_jobs", true, "( id , task_id )")
	plan.index("uq_katana_jobs_active_task", "katana_jobs", true, "( task_id ) WHERE status IN ( 'queued' , 'running' )")
	plan.updatedTrigger("trg_katana_jobs_upd", "katana_jobs")
	plan.table("katana_discoveries",
		ddlSequence("id"),
		ddlColumn("job_id", ddlBigInt, ddlRequired, ddlReferences("katana_jobs", "id", "CASCADE")),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("source", ddlText, ddlRequired, ddlDefault("'katana'")),
		ddlColumn("method", ddlText, ddlRequired, ddlDefault("'GET'")),
		ddlColumn("url", ddlText, ddlRequired),
		ddlColumn("status_code", ddlInteger),
		ddlColumn("technologies", ddlTextArray, ddlRequired, ddlEmptyObject),
		ddlColumn("knowledge_base", ddlJSONB, ddlRequired, ddlDefault("'{}' :: jsonb")),
		ddlColumn("verified", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("traffic_id", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("asset_id", ddlBigInt, ddlReferences("assets", "id", "SET NULL")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("UNIQUE ( job_id , method , url , verified )"),
	)
	plan.raw(`
-- Old feature-build upgrade: persist the bounded, redacted Katana knowledge
-- metadata without requiring a destructive schema rebuild.
ALTER TABLE katana_discoveries ADD COLUMN IF NOT EXISTS knowledge_base JSONB NOT NULL DEFAULT '{}'::jsonb;
-- The composite reference prevents a discovery from naming one task while its
-- owning job belongs to another. The exception form also upgrades an install
-- that created these new tables during an earlier feature build.
DO $$ BEGIN
    ALTER TABLE katana_discoveries ADD CONSTRAINT fk_katana_discovery_job_task
        FOREIGN KEY (job_id, task_id) REFERENCES katana_jobs(id, task_id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
`)
	plan.index("idx_katana_discoveries_task", "katana_discoveries", false, "( task_id , id DESC )")
	plan.index("idx_katana_discoveries_job_verified", "katana_discoveries", false, "( job_id , verified , id )")
	plan.index("idx_katana_discoveries_traffic", "katana_discoveries", false, "( traffic_id ) WHERE traffic_id <> ''")
	plan.updatedTrigger("trg_katana_discoveries_upd", "katana_discoveries")
	plan.raw(`

-- A planner must review the current automatic Katana outcome before ordinary
-- goal work is dispatched. The token binds the decision to one immutable
-- automatic-job snapshot; a later automatic job therefore invalidates the old
-- review without deleting its audit history. Decisions authorize only the
-- typed Uroboros scanner contracts, never raw sidecar MCP calls.
`)
	plan.table("discovery_reviews",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("katana_job_id", ddlBigInt),
		ddlColumn("review_token", ddlText, ddlRequired),
		ddlColumn("katana_status", ddlText, ddlRequired, ddlCheck("( katana_status IN ( 'not_started' , 'completed' , 'failed' , 'canceled' , 'interrupted' ) )")),
		ddlColumn("summary", ddlText, ddlRequired),
		ddlColumn("decisions", ddlJSONB, ddlRequired),
		ddlColumn("agent_key", ddlText, ddlRequired, ddlDefault("'planner'")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("UNIQUE ( task_id , review_token )"),
		ddlConstraint("CONSTRAINT fk_discovery_review_katana_job_task FOREIGN KEY ( katana_job_id , task_id ) REFERENCES katana_jobs ( id , task_id ) ON DELETE CASCADE"),
	)
	plan.index("idx_discovery_reviews_task", "discovery_reviews", false, "( task_id , id DESC )")
	plan.updatedTrigger("trg_discovery_reviews_upd", "discovery_reviews")
	plan.raw(`

-- Task-bound Arsenal scanner leases. Uroboros owns lifecycle, frozen authority
-- and durable evidence; the HTTP MCP sidecar is only an execution adapter.
`)
	plan.table("arsenal_jobs",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("source", ddlText, ddlRequired, ddlDefault("'tool'"), ddlCheck("( source IN ( 'tool' , 'system' ) )")),
		ddlColumn("tool", ddlText, ddlRequired, ddlCheck("( tool IN ( 'nuclei' , 'httpx' , 'ffuf' , 'discovery' , 'archive' , 'arjun' , 'wafw00f' ) )")),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'queued'"), ddlCheck("( status IN ( 'queued' , 'running' , 'completed' , 'failed' , 'canceled' , 'interrupted' ) )")),
		ddlColumn("target", ddlText, ddlRequired),
		ddlColumn("config", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("config_hash", ddlText, ddlRequired),
		ddlColumn("scope", ddlJSONB, ddlRequired),
		ddlColumn("scope_hash", ddlText, ddlRequired),
		ddlColumn("contract_version", ddlInteger, ddlRequired),
		ddlColumn("source_revision", ddlText, ddlRequired),
		ddlColumn("tool_version", ddlText, ddlRequired),
		ddlColumn("tool_sha256", ddlText, ddlRequired),
		ddlColumn("template_version", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("template_sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("wordlist_sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("exit_state", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("complete", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("error", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("stats", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("candidate_count", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("verified_count", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("started_at", ddlTimestampTZ),
		ddlColumn("finished_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("idx_arsenal_jobs_task", "arsenal_jobs", false, "( task_id , id DESC )")
	plan.index("uq_arsenal_jobs_id_task", "arsenal_jobs", true, "( id , task_id )")
	plan.index("uq_arsenal_jobs_id_task_tool", "arsenal_jobs", true, "( id , task_id , tool )")
	plan.index("uq_arsenal_jobs_active_task", "arsenal_jobs", true, "( task_id ) WHERE status IN ( 'queued' , 'running' )")
	plan.updatedTrigger("trg_arsenal_jobs_upd", "arsenal_jobs")
	plan.table("arsenal_observations",
		ddlSequence("id"),
		ddlColumn("job_id", ddlBigInt, ddlRequired, ddlReferences("arsenal_jobs", "id", "CASCADE")),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("tool", ddlText, ddlRequired, ddlCheck("( tool IN ( 'nuclei' , 'httpx' , 'ffuf' , 'discovery' , 'archive' , 'arjun' , 'wafw00f' ) )")),
		ddlColumn("observation_key", ddlText, ddlRequired),
		ddlColumn("authority", ddlText, ddlRequired, ddlCheck("( authority IN ( 'candidate' , 'hypothesis' ) )")),
		ddlColumn("record", ddlJSONB, ddlRequired),
		ddlColumn("proxy_verified", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("traffic_id", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("asset_id", ddlBigInt, ddlReferences("assets", "id", "SET NULL")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("UNIQUE ( job_id , observation_key )"),
		ddlConstraint("CHECK ( ( proxy_verified AND traffic_id <> '' ) OR ( NOT proxy_verified AND traffic_id = '' ) )"),
		ddlConstraint("CHECK ( asset_id IS NULL OR ( authority = 'candidate' AND proxy_verified AND traffic_id <> '' ) )"),
	)
	plan.raw(`
DO $$ BEGIN
    ALTER TABLE arsenal_observations ADD CONSTRAINT fk_arsenal_observation_job_task
        FOREIGN KEY (job_id,task_id) REFERENCES arsenal_jobs(id,task_id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE arsenal_observations ADD CONSTRAINT fk_arsenal_observation_job_task_tool
        FOREIGN KEY (job_id,task_id,tool) REFERENCES arsenal_jobs(id,task_id,tool) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
`)
	plan.index("idx_arsenal_observations_task", "arsenal_observations", false, "( task_id , id DESC )")
	plan.index("idx_arsenal_observations_job_verified", "arsenal_observations", false, "( job_id , proxy_verified , id )")
	plan.index("idx_arsenal_observations_traffic", "arsenal_observations", false, "( traffic_id ) WHERE traffic_id <> ''")
	plan.updatedTrigger("trg_arsenal_observations_upd", "arsenal_observations")
	plan.raw(`

-- T2/T3/T4 add composite discovery, archive-metadata and Arjun job kinds. Existing
-- databases retain the inline CHECK created by the earlier schema, so replace
-- it when either additive kind is missing.
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='arsenal_jobs'::regclass AND conname='arsenal_jobs_tool_check'
          AND (position('discovery' in pg_get_constraintdef(oid)) = 0
               OR position('archive' in pg_get_constraintdef(oid)) = 0
               OR position('arjun' in pg_get_constraintdef(oid)) = 0
               OR position('wafw00f' in pg_get_constraintdef(oid)) = 0)
    ) THEN
        ALTER TABLE arsenal_jobs DROP CONSTRAINT arsenal_jobs_tool_check;
        ALTER TABLE arsenal_jobs ADD CONSTRAINT arsenal_jobs_tool_check
            CHECK (tool IN ('nuclei','httpx','ffuf','discovery','archive','arjun','wafw00f'));
    END IF;
END $$;
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='arsenal_observations'::regclass AND conname='arsenal_observations_tool_check'
          AND (position('discovery' in pg_get_constraintdef(oid)) = 0
               OR position('archive' in pg_get_constraintdef(oid)) = 0
               OR position('arjun' in pg_get_constraintdef(oid)) = 0
               OR position('wafw00f' in pg_get_constraintdef(oid)) = 0)
    ) THEN
        ALTER TABLE arsenal_observations DROP CONSTRAINT arsenal_observations_tool_check;
        ALTER TABLE arsenal_observations ADD CONSTRAINT arsenal_observations_tool_check
            CHECK (tool IN ('nuclei','httpx','ffuf','discovery','archive','arjun','wafw00f'));
    END IF;
END $$;

-- Explicit operator authorization for disclosing one task-scoped root domain
-- to one archive metadata provider. The task-private identity key is reused by
-- that task's grants and never exposes raw historical URLs to agents.
`)
	plan.table("archive_provider_grants",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("provider", ddlText, ddlRequired, ddlCheck("( provider IN ( 'internet_archive_cdx' ) )")),
		ddlColumn("domain", ddlText, ddlRequired),
		ddlColumn("capture_from", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("capture_to", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("contract_version", ddlText, ddlRequired, ddlRule("CONSTRAINT archive_provider_grants_contract_ck CHECK ( contract_version = 'ia-cdx-index-v1' )")),
		ddlColumn("notice_version", ddlText, ddlRequired, ddlRule("CONSTRAINT archive_provider_grants_notice_ck CHECK ( notice_version = 'ia-cdx-disclosure-v1' )")),
		ddlColumn("approved_by", ddlText, ddlRequired),
		ddlColumn("identity_key", ddlBytes, ddlRequired),
		ddlTimestamp("approved_at"),
		ddlColumn("expires_at", ddlTimestampTZ, ddlRequired),
		ddlColumn("revoked_at", ddlTimestampTZ),
		ddlColumn("revoked_by", ddlText, ddlRequired, ddlEmpty),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("CHECK ( octet_length ( identity_key ) >= 32 )"),
		ddlConstraint("CHECK ( capture_from = '' OR capture_from ~ '^[0-9]{4}([0-9]{2}([0-9]{2}([0-9]{6})?)?)?$' )"),
		ddlConstraint("CHECK ( capture_to = '' OR capture_to ~ '^[0-9]{4}([0-9]{2}([0-9]{2}([0-9]{6})?)?)?$' )"),
		ddlConstraint("CHECK ( expires_at > approved_at )"),
	)
	plan.raw(`
DO $$ BEGIN
    ALTER TABLE archive_provider_grants ADD CONSTRAINT archive_provider_grants_contract_ck
        CHECK (contract_version = 'ia-cdx-index-v1');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE archive_provider_grants ADD CONSTRAINT archive_provider_grants_notice_ck
        CHECK (notice_version = 'ia-cdx-disclosure-v1');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
`)
	plan.index("idx_archive_provider_grants_task", "archive_provider_grants", false, "( task_id , id DESC )")
	plan.index("idx_archive_provider_grants_match", "archive_provider_grants", false, "( task_id , provider , domain , expires_at DESC ) WHERE revoked_at IS NULL")
	plan.updatedTrigger("trg_archive_provider_grants_upd", "archive_provider_grants")
	plan.raw(`

-- Private, operator-readable provider evidence. Raw response bytes and raw URLs
-- are never retained here; projection is a sanitized page projection and the
-- separate raw hash only proves which decoded provider body was inspected.
`)
	plan.table("archive_provider_artifacts",
		ddlSequence("id"),
		ddlColumn("job_id", ddlBigInt, ddlRequired, ddlReferences("arsenal_jobs", "id", "CASCADE")),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("grant_id", ddlBigInt, ddlRequired, ddlRule("REFERENCES archive_provider_grants ( id ) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED")),
		ddlColumn("provider", ddlText, ddlRequired, ddlCheck("( provider IN ( 'internet_archive_cdx' ) )")),
		ddlColumn("attempt_id", ddlText, ddlRequired),
		ddlColumn("request_sha256", ddlText, ddlRequired),
		ddlColumn("route_sha256", ddlText, ddlRequired),
		ddlColumn("response_status", ddlInteger),
		ddlColumn("raw_body_sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("projection_sha256", ddlText, ddlRequired),
		ddlColumn("projection", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("raw_retained", ddlBoolean, ddlRequired, ddlFalse, ddlCheck("( NOT raw_retained )")),
		ddlColumn("outcome", ddlText, ddlRequired, ddlRule("CONSTRAINT archive_provider_artifacts_outcome_ck CHECK ( outcome IN ( 'started' , 'page' , 'http_error' , 'network_error' , 'contract_error' , 'rate_limited' ) )")),
		ddlColumn("error_code", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("started_at", ddlTimestampTZ, ddlRequired),
		ddlColumn("finished_at", ddlTimestampTZ, ddlRequired),
		ddlTimestamp("created_at"),
		ddlConstraint("UNIQUE ( job_id , attempt_id )"),
		ddlConstraint("CHECK ( request_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CHECK ( route_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CHECK ( raw_body_sha256 = '' OR raw_body_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CONSTRAINT archive_provider_artifacts_projection_sha_ck CHECK ( projection_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CONSTRAINT archive_provider_artifacts_attempt_ck CHECK ( attempt_id ~ '^[0-9]{1,20}-[0-9]{1,20}-[0-9]{1,20}$' )"),
		ddlConstraint("CONSTRAINT archive_provider_artifacts_response_ck CHECK ( response_status IS NULL OR response_status BETWEEN 100 AND 599 )"),
		ddlConstraint("CONSTRAINT archive_provider_artifacts_time_ck CHECK ( finished_at >= started_at )"),
		ddlConstraint("CONSTRAINT archive_provider_artifacts_state_ck CHECK ( ( outcome <> 'started' OR ( response_status IS NULL AND raw_body_sha256 = '' AND error_code = '' ) ) AND ( outcome <> 'page' OR ( response_status = 200 AND raw_body_sha256 <> '' AND error_code = '' ) ) AND ( outcome IN ( 'started' , 'page' ) OR error_code <> '' ) )"),
	)
	plan.raw(`
ALTER TABLE archive_provider_artifacts ALTER COLUMN projection_sha256 DROP DEFAULT;
DO $$ BEGIN
    ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_projection_sha_ck
        CHECK (projection_sha256 ~ '^[0-9a-f]{64}$');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_attempt_ck
        CHECK (attempt_id ~ '^[0-9]{1,20}-[0-9]{1,20}-[0-9]{1,20}$');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_response_ck
        CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_time_ck
        CHECK (finished_at >= started_at);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_state_ck CHECK (
        (outcome <> 'started' OR (response_status IS NULL AND raw_body_sha256='' AND error_code=''))
        AND (outcome <> 'page' OR (response_status=200 AND raw_body_sha256<>'' AND error_code=''))
        AND (outcome IN ('started','page') OR error_code<>'')
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ DECLARE item RECORD; BEGIN
    FOR item IN SELECT conname FROM pg_constraint
        WHERE conrelid='archive_provider_artifacts'::regclass AND contype='c'
          AND position('outcome' in pg_get_constraintdef(oid)) > 0
          AND position('started' in pg_get_constraintdef(oid)) = 0
    LOOP
        EXECUTE format('ALTER TABLE archive_provider_artifacts DROP CONSTRAINT %I', item.conname);
    END LOOP;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid='archive_provider_artifacts'::regclass
          AND conname='archive_provider_artifacts_outcome_ck') THEN
        ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_outcome_ck
            CHECK (outcome IN ('started','page','http_error','network_error','contract_error','rate_limited'));
    END IF;
END $$;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid='archive_provider_artifacts'::regclass
          AND conname='archive_provider_artifacts_grant_id_fkey' AND NOT condeferrable) THEN
        ALTER TABLE archive_provider_artifacts DROP CONSTRAINT archive_provider_artifacts_grant_id_fkey;
        ALTER TABLE archive_provider_artifacts ADD CONSTRAINT archive_provider_artifacts_grant_id_fkey
            FOREIGN KEY (grant_id) REFERENCES archive_provider_grants(id)
            ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;
    END IF;
END $$;
DO $$ BEGIN
    ALTER TABLE archive_provider_artifacts ADD CONSTRAINT fk_archive_artifact_job_task
        FOREIGN KEY (job_id,task_id) REFERENCES arsenal_jobs(id,task_id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
`)
	plan.index("idx_archive_provider_artifacts_task", "archive_provider_artifacts", false, "( task_id , id DESC )")
	plan.index("idx_archive_provider_artifacts_job", "archive_provider_artifacts", false, "( job_id , id )")
	plan.raw(`

-- Audited external research. A grant snapshots the task identities that the
-- operator agreed may be disclosed to one provider; later scope expansion does
-- not silently expand an existing grant. Generic (non-target-identifying)
-- searches do not require a disclosure grant, but every outbound attempt is
-- still budgeted and recorded below.
`)
	plan.table("research_disclosure_grants",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("provider", ddlText, ddlRequired, ddlCheck("( provider IN ( 'brave-free' , 'tavily' , 'ddgs' , 'cve-list-v5' , 'nvd' , 'osv' , 'ghsa' , 'cisa-kev' , 'first-epss' ) )")),
		ddlColumn("identities", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("identity_sha256", ddlText, ddlRequired, ddlCheck("( identity_sha256 ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("contract_version", ddlText, ddlRequired, ddlCheck("( contract_version IN ( 'research-query-v1' , 'vulnerability-query-v1' ) )")),
		ddlColumn("notice_version", ddlText, ddlRequired, ddlCheck("( notice_version = 'research-disclosure-v1' )")),
		ddlColumn("approved_by", ddlText, ddlRequired),
		ddlTimestamp("approved_at"),
		ddlColumn("expires_at", ddlTimestampTZ, ddlRequired),
		ddlColumn("max_requests", ddlInteger, ddlRequired, ddlDefault("20"), ddlCheck("( max_requests > 0 )")),
		ddlColumn("revoked_at", ddlTimestampTZ),
		ddlColumn("revoked_by", ddlText, ddlRequired, ddlEmpty),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("CHECK ( jsonb_typeof ( identities ) = 'array' )"),
		ddlConstraint("CHECK ( expires_at > approved_at )"),
	)
	plan.index("idx_research_grants_task", "research_disclosure_grants", false, "( task_id , id DESC )")
	plan.index("idx_research_grants_active", "research_disclosure_grants", false, "( task_id , provider , expires_at DESC ) WHERE revoked_at IS NULL")
	plan.updatedTrigger("trg_research_disclosure_grants_upd", "research_disclosure_grants")
	plan.raw(`
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid='research_disclosure_grants'::regclass
                 AND conname='research_disclosure_grants_provider_check'
                 AND pg_get_constraintdef(oid) NOT LIKE '%cve-list-v5%') THEN
        ALTER TABLE research_disclosure_grants DROP CONSTRAINT research_disclosure_grants_provider_check;
        ALTER TABLE research_disclosure_grants ADD CONSTRAINT research_disclosure_grants_provider_check
            CHECK (provider IN ('brave-free','tavily','ddgs','cve-list-v5','nvd','osv','ghsa','cisa-kev','first-epss'));
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid='research_disclosure_grants'::regclass
                 AND conname='research_disclosure_grants_contract_version_check'
                 AND pg_get_constraintdef(oid) NOT LIKE '%vulnerability-query-v1%') THEN
        ALTER TABLE research_disclosure_grants DROP CONSTRAINT research_disclosure_grants_contract_version_check;
        ALTER TABLE research_disclosure_grants ADD CONSTRAINT research_disclosure_grants_contract_version_check
            CHECK (contract_version IN ('research-query-v1','vulnerability-query-v1'));
    END IF;
END $$;

-- A Tor egress grant is distinct from a provider disclosure grant. The
-- operator supplies one or more seed URLs; only their canonical origins may be
-- fetched, and later model output cannot broaden the approved host set.
`)
	plan.table("research_egress_grants",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("profile_id", ddlText, ddlRequired, ddlCheck("( profile_id = 'tor' )")),
		ddlColumn("seeds", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("origins", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("seed_sha256", ddlText, ddlRequired, ddlCheck("( seed_sha256 ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("contract_version", ddlText, ddlRequired, ddlCheck("( contract_version = 'tor-research-fetch-v1' )")),
		ddlColumn("notice_version", ddlText, ddlRequired, ddlCheck("( notice_version = 'research-egress-v1' )")),
		ddlColumn("approved_by", ddlText, ddlRequired),
		ddlTimestamp("approved_at"),
		ddlColumn("expires_at", ddlTimestampTZ, ddlRequired),
		ddlColumn("max_requests", ddlInteger, ddlRequired, ddlDefault("20"), ddlCheck("( max_requests > 0 )")),
		ddlColumn("revoked_at", ddlTimestampTZ),
		ddlColumn("revoked_by", ddlText, ddlRequired, ddlEmpty),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("CHECK ( jsonb_typeof ( seeds ) = 'array' AND jsonb_array_length ( seeds ) BETWEEN 1 AND 32 )"),
		ddlConstraint("CHECK ( jsonb_typeof ( origins ) = 'array' AND jsonb_array_length ( origins ) BETWEEN 1 AND 32 )"),
		ddlConstraint("CHECK ( expires_at > approved_at )"),
	)
	plan.index("idx_research_egress_grants_task", "research_egress_grants", false, "( task_id , id DESC )")
	plan.index("idx_research_egress_grants_active", "research_egress_grants", false, "( task_id , profile_id , expires_at DESC ) WHERE revoked_at IS NULL")
	plan.updatedTrigger("trg_research_egress_grants_upd", "research_egress_grants")
	plan.raw(`

-- One row per outbound attempt (plus explicit cache-hit audit rows). Provider
-- response bytes are hashed and discarded; only a bounded, sanitized projection
-- can be returned to an agent or retained in PostgreSQL. These rows have no FK
-- path to assets, findings or graph facts, so research never gains evidence
-- authority merely by being stored.
`)
	plan.table("research_artifacts",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("session_id", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("agent_key", ddlText, ddlRequired, ddlCheck("( agent_key <> '' AND length ( agent_key ) <= 128 )")),
		ddlColumn("exploration_id", ddlBigInt, ddlRequired, ddlZero, ddlCheck("( exploration_id >= 0 )")),
		ddlColumn("intent_id", ddlBigInt, ddlRequired, ddlZero, ddlCheck("( intent_id >= 0 )")),
		ddlColumn("tool_name", ddlText, ddlRequired, ddlDefault("'web_search'"), ddlCheck("( tool_name IN ( 'web_search' , 'vuln_lookup' , 'vuln_match' , 'vuln_prioritize' , 'research_fetch' ) )")),
		ddlColumn("grant_id", ddlBigInt, ddlRule("REFERENCES research_disclosure_grants ( id ) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED")),
		ddlColumn("egress_grant_id", ddlBigInt, ddlRule("REFERENCES research_egress_grants ( id ) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED")),
		ddlColumn("provider", ddlText, ddlRequired, ddlCheck("( provider IN ( 'brave-free' , 'tavily' , 'ddgs' , 'cve-list-v5' , 'nvd' , 'osv' , 'ghsa' , 'cisa-kev' , 'first-epss' , 'tor-fetch' ) )")),
		ddlColumn("egress_profile", ddlText, ddlRequired, ddlCheck("( egress_profile IN ( 'direct' , 'operator_proxy' , 'tor' ) )")),
		ddlColumn("isolation_sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("attempt_id", ddlText, ddlRequired, ddlUnique),
		ddlColumn("retry_index", ddlInteger, ddlRequired, ddlZero, ddlCheck("( retry_index >= 0 AND retry_index <= 8 )")),
		ddlColumn("query_sha256", ddlText, ddlRequired, ddlCheck("( query_sha256 ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("query_projection", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("target_identifying", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("matched_identities", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("request_sha256", ddlText, ddlRequired, ddlCheck("( request_sha256 ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("response_status", ddlInteger),
		ddlColumn("provider_request_id", ddlText, ddlRequired, ddlEmpty, ddlCheck("( length ( provider_request_id ) <= 240 )")),
		ddlColumn("raw_body_sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("projection_sha256", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("projection", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("cache_key", ddlText, ddlRequired, ddlCheck("( cache_key ~ '^[0-9a-f]{64}$' )")),
		ddlColumn("cache_expires_at", ddlTimestampTZ),
		ddlColumn("stale_expires_at", ddlTimestampTZ),
		ddlColumn("schema_version", ddlText, ddlRequired, ddlEmpty, ddlCheck("( length ( schema_version ) <= 120 )")),
		ddlColumn("etag", ddlText, ddlRequired, ddlEmpty, ddlCheck("( length ( etag ) <= 512 )")),
		ddlColumn("last_modified", ddlText, ddlRequired, ddlEmpty, ddlCheck("( length ( last_modified ) <= 512 )")),
		ddlColumn("retrieved_at", ddlTimestampTZ),
		ddlColumn("source_artifact_id", ddlBigInt, ddlReferences("research_artifacts", "id", "SET NULL")),
		ddlColumn("outcome", ddlText, ddlRequired, ddlCheck("( outcome IN ( 'started' , 'success' , 'cache_hit' , 'http_error' , 'network_error' , 'contract_error' , 'rate_limited' , 'policy_denied' , 'oversized' , 'stale_cache' ) )")),
		ddlColumn("error_code", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("cost_units", ddlInteger, ddlRequired, ddlZero, ddlCheck("( cost_units >= 0 )")),
		ddlColumn("untrusted_content", ddlBoolean, ddlRequired, ddlTrue, ddlCheck("( untrusted_content )")),
		ddlColumn("raw_retained", ddlBoolean, ddlRequired, ddlFalse, ddlCheck("( NOT raw_retained )")),
		ddlColumn("started_at", ddlTimestampTZ, ddlRequired),
		ddlColumn("finished_at", ddlTimestampTZ, ddlRequired),
		ddlTimestamp("created_at"),
		ddlConstraint("CHECK ( ( task_id IS NOT NULL ) <> ( session_id <> '' ) )"),
		ddlConstraint("CHECK ( jsonb_typeof ( matched_identities ) = 'array' )"),
		ddlConstraint("CHECK ( response_status IS NULL OR response_status BETWEEN 100 AND 599 )"),
		ddlConstraint("CHECK ( raw_body_sha256 = '' OR raw_body_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CHECK ( projection_sha256 = '' OR projection_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CHECK ( isolation_sha256 = '' OR isolation_sha256 ~ '^[0-9a-f]{64}$' )"),
		ddlConstraint("CHECK ( finished_at >= started_at )"),
		ddlConstraint("CHECK ( NOT target_identifying OR task_id IS NOT NULL )"),
		ddlConstraint("CHECK ( NOT target_identifying OR grant_id IS NOT NULL )"),
		ddlConstraint("CHECK ( ( egress_profile = 'tor' ) = ( egress_grant_id IS NOT NULL ) )"),
		ddlConstraint("CHECK ( ( egress_profile = 'tor' ) = ( isolation_sha256 <> '' ) )"),
		ddlConstraint("CHECK ( ( tool_name = 'research_fetch' ) = ( provider = 'tor-fetch' AND egress_profile = 'tor' ) )"),
	)
	plan.raw(`
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS stale_expires_at TIMESTAMPTZ;
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS schema_version TEXT NOT NULL DEFAULT '';
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS etag TEXT NOT NULL DEFAULT '';
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS last_modified TEXT NOT NULL DEFAULT '';
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS retrieved_at TIMESTAMPTZ;
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS egress_grant_id BIGINT;
ALTER TABLE research_artifacts ADD COLUMN IF NOT EXISTS isolation_sha256 TEXT NOT NULL DEFAULT '';
DO $$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint
	               WHERE conrelid='research_artifacts'::regclass
	                 AND conname='research_artifacts_egress_grant_id_fkey') THEN
		ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_egress_grant_id_fkey
			FOREIGN KEY (egress_grant_id) REFERENCES research_egress_grants(id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;
	END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid='research_artifacts'::regclass
                     AND conname='research_artifacts_schema_version_len_check') THEN
        ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_schema_version_len_check
            CHECK (length(schema_version) <= 120);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid='research_artifacts'::regclass
                     AND conname='research_artifacts_etag_len_check') THEN
        ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_etag_len_check
            CHECK (length(etag) <= 512);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid='research_artifacts'::regclass
                     AND conname='research_artifacts_last_modified_len_check') THEN
        ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_last_modified_len_check
            CHECK (length(last_modified) <= 512);
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid='research_artifacts'::regclass
                 AND conname='research_artifacts_tool_name_check'
                 AND pg_get_constraintdef(oid) NOT LIKE '%research_fetch%') THEN
        ALTER TABLE research_artifacts DROP CONSTRAINT research_artifacts_tool_name_check;
        ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_tool_name_check
            CHECK (tool_name IN ('web_search','vuln_lookup','vuln_match','vuln_prioritize','research_fetch'));
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid='research_artifacts'::regclass
                 AND conname='research_artifacts_provider_check'
                 AND pg_get_constraintdef(oid) NOT LIKE '%tor-fetch%') THEN
        ALTER TABLE research_artifacts DROP CONSTRAINT research_artifacts_provider_check;
        ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_provider_check
            CHECK (provider IN ('brave-free','tavily','ddgs','cve-list-v5','nvd','osv','ghsa','cisa-kev','first-epss','tor-fetch'));
    END IF;
	IF EXISTS (SELECT 1 FROM pg_constraint
	           WHERE conrelid='research_artifacts'::regclass
	             AND conname='research_artifacts_egress_profile_check'
	             AND pg_get_constraintdef(oid) NOT LIKE '%tor%') THEN
		ALTER TABLE research_artifacts DROP CONSTRAINT research_artifacts_egress_profile_check;
		ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_egress_profile_check
			CHECK (egress_profile IN ('direct','operator_proxy','tor'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='research_artifacts'::regclass
	               AND conname='research_artifacts_isolation_sha256_check') THEN
		ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_isolation_sha256_check
			CHECK (isolation_sha256 = '' OR isolation_sha256 ~ '^[0-9a-f]{64}$');
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='research_artifacts'::regclass
	               AND conname='research_artifacts_tor_grant_check') THEN
		ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_tor_grant_check
			CHECK ((egress_profile = 'tor') = (egress_grant_id IS NOT NULL));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='research_artifacts'::regclass
	               AND conname='research_artifacts_tor_isolation_check') THEN
		ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_tor_isolation_check
			CHECK ((egress_profile = 'tor') = (isolation_sha256 <> ''));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='research_artifacts'::regclass
	               AND conname='research_artifacts_tor_tool_check') THEN
		ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_tor_tool_check
			CHECK ((tool_name = 'research_fetch') = (provider = 'tor-fetch' AND egress_profile = 'tor'));
	END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid='research_artifacts'::regclass
                 AND conname='research_artifacts_outcome_check'
                 AND pg_get_constraintdef(oid) NOT LIKE '%stale_cache%') THEN
        ALTER TABLE research_artifacts DROP CONSTRAINT research_artifacts_outcome_check;
        ALTER TABLE research_artifacts ADD CONSTRAINT research_artifacts_outcome_check
            CHECK (outcome IN ('started','success','cache_hit','http_error','network_error',
                               'contract_error','rate_limited','policy_denied','oversized','stale_cache'));
    END IF;
END $$;
`)
	plan.index("idx_research_artifacts_task", "research_artifacts", false, "( task_id , id DESC ) WHERE task_id IS NOT NULL")
	plan.index("idx_research_artifacts_session", "research_artifacts", false, "( session_id , id DESC ) WHERE session_id <> ''")
	plan.index("idx_research_artifacts_cache", "research_artifacts", false, "( cache_key , provider , cache_expires_at DESC ) WHERE outcome = 'success'")
	plan.index("idx_research_artifacts_rate", "research_artifacts", false, "( task_id , started_at DESC ) WHERE task_id IS NOT NULL")
	plan.raw(`

-- =====================================================================
-- E. Agents / prompt templates / variable catalog
-- =====================================================================
`)
	plan.table("agents",
		ddlSequence("id"),
		ddlColumn("key", ddlText, ddlRequired, ddlUnique, ddlCheck("( key ~ '^[a-z][a-z0-9_]*$' )")),
		ddlColumn("name", ddlText, ddlRequired),
		ddlColumn("description", ddlText),
		ddlColumn("role", ddlText, ddlRequired),
		ddlColumn("builtin", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("llm_profile_id", ddlBigInt, ddlReferences("llm_profiles", "id", "SET NULL")),
		ddlColumn("current_prompt_id", ddlBigInt),
		ddlColumn("max_turns", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("run_seconds", ddlInteger, ddlRequired, ddlDefault("1200")),
		ddlColumn("web_search", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("interactive_shell", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("wrapup_prompt", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("wrapup_max_turns", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("task_timeout_wrapup_prompt", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("task_timeout_wrapup_max_turns", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("trigger_run_mode", ddlText, ddlRequired, ddlDefault("'serial'"), ddlCheck("( trigger_run_mode IN ( 'serial' , 'parallel' ) )")),
		ddlColumn("trigger_merge_mode", ddlText, ddlRequired, ddlDefault("'all'"), ddlCheck("( trigger_merge_mode IN ( 'by_task' , 'all' , 'none' ) )")),
		ddlColumn("trigger_max_parallel", ddlInteger, ddlRequired, ddlDefault("5")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
		ddlConstraint("CONSTRAINT agents_role_ck CHECK ( role IN ( 'goals' , 'main' , 'planner' , 'worker' , 'assistant' ) )"),
	)
	plan.raw(`
-- Add-column migration (already released; old DBs get the columns on upgrade, new DBs' CREATE already has them. The migration carries no CHECK: safe for existing old-DB data + the backend's write whitelist as a fallback).
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_run_mode     TEXT    NOT NULL DEFAULT 'serial';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_merge_mode   TEXT    NOT NULL DEFAULT 'all';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS trigger_max_parallel INTEGER NOT NULL DEFAULT 5;
-- per-agent LLM binding (agent-level default model): the column has been in the CREATE above since the first version; this ALTER is only a fallback for very old DBs (idempotent).
ALTER TABLE agents ADD COLUMN IF NOT EXISTS llm_profile_id BIGINT REFERENCES llm_profiles(id) ON DELETE SET NULL;
-- run_seconds single-run wall-clock default 600→1200: only changes the column default (affects future inserts), leaves existing old-DB rows untouched.
ALTER TABLE agents ALTER COLUMN run_seconds SET DEFAULT 1200;
`)
	plan.index("idx_agents_llm_profile", "agents", false, "( llm_profile_id ) WHERE llm_profile_id IS NOT NULL")
	plan.updatedTrigger("trg_agents_upd", "agents")
	plan.table("agent_prompts",
		ddlSequence("id"),
		ddlColumn("agent_id", ddlBigInt, ddlRequired, ddlReferences("agents", "id", "CASCADE")),
		ddlColumn("version", ddlInt, ddlRequired),
		ddlColumn("template_text", ddlText, ddlRequired),
		ddlColumn("note", ddlText),
		ddlColumn("updated_by", ddlText),
		ddlTimestamp("created_at"),
		ddlConstraint("UNIQUE ( agent_id , version )"),
	)
	plan.raw(`
-- Circular foreign key: agents.current_prompt_id → agent_prompts.id (must be added after both tables exist)
DO $$ BEGIN
    ALTER TABLE agents ADD CONSTRAINT fk_agents_curprompt
        FOREIGN KEY (current_prompt_id) REFERENCES agent_prompts(id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

`)
	plan.table("agent_prompt_vars",
		ddlSequence("id"),
		ddlColumn("agent_id", ddlBigInt, ddlRequired, ddlReferences("agents", "id", "CASCADE")),
		ddlColumn("var_name", ddlText, ddlRequired),
		ddlColumn("description", ddlText),
		ddlColumn("example", ddlText),
		ddlColumn("source", ddlText, ddlRequired, ddlCheck("( source IN ( 'exploration' , 'runtime' , 'distilled' ) )")),
		ddlConstraint("UNIQUE ( agent_id , var_name )"),
	)
	plan.raw(`

-- =====================================================================
-- F. MCP services
-- =====================================================================
`)
	plan.table("mcp_servers",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired, ddlUnique),
		ddlColumn("transport", ddlText, ddlRequired, ddlCheck("( transport IN ( 'stdio' , 'http' , 'sse' ) )")),
		ddlColumn("command", ddlText),
		ddlColumn("args", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("env", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("url", ddlText),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("insecure", ddlBoolean, ddlRequired, ddlFalse),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.raw(`

-- Allow legacy MCP SSE servers on databases created before SSE support.
ALTER TABLE mcp_servers DROP CONSTRAINT IF EXISTS mcp_servers_transport_check;
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_transport_check
    CHECK (transport IN ('stdio','http','sse'));
-- Backfill column for old DBs (schema.sql is Exec'd on every startup).
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS insecure BOOLEAN NOT NULL DEFAULT false;
`)
	plan.updatedTrigger("trg_mcp_upd", "mcp_servers")
	plan.raw(`

-- Streamable HTTP deadline includes scanner execution and result delivery.
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS request_timeout_seconds INTEGER NOT NULL DEFAULT 120;

`)
	plan.table("mcp_tools_cache",
		ddlSequence("id"),
		ddlColumn("server_id", ddlBigInt, ddlRequired, ddlReferences("mcp_servers", "id", "CASCADE")),
		ddlColumn("tool_name", ddlText, ddlRequired),
		ddlColumn("description", ddlText),
		ddlColumn("schema", ddlJSONB),
		ddlTimestamp("discovered_at"),
		ddlConstraint("UNIQUE ( server_id , tool_name )"),
	)
	plan.raw(`

-- =====================================================================
-- G. Visibility: agent × mcp / skill
-- =====================================================================
`)
	plan.table("agent_visibility",
		ddlColumn("agent_id", ddlBigInt, ddlRequired, ddlReferences("agents", "id", "CASCADE")),
		ddlColumn("resource_kind", ddlText, ddlRequired, ddlCheck("( resource_kind IN ( 'mcp' ) )")),
		ddlColumn("resource_id", ddlBigInt, ddlRequired),
		ddlColumn("mcp_tool_name", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlTimestamp("created_at"),
		ddlConstraint("PRIMARY KEY ( agent_id , resource_kind , resource_id , mcp_tool_name )"),
	)
	plan.index("idx_vis_resource", "agent_visibility", false, "( resource_kind , resource_id )")
	plan.table("agent_skill_visibility",
		ddlColumn("agent_id", ddlBigInt, ddlRequired, ddlReferences("agents", "id", "CASCADE")),
		ddlColumn("skill_name", ddlText, ddlRequired),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlTimestamp("created_at"),
		ddlConstraint("PRIMARY KEY ( agent_id , skill_name )"),
	)
	plan.index("idx_askv_skill", "agent_skill_visibility", false, "( skill_name )")
	plan.raw(`

-- Skill-call ledger (see db/skill_usage.go). One row per Skill() call, recording only dimensions, not the body.
-- Deliberately no foreign key: statistics must survive task/session deletion (same as llm_usage), and a skill is just
-- a directory name on the filesystem with no corresponding table.
`)
	plan.table("skill_usage",
		ddlSequence("id"),
		ddlTimestamp("ts"),
		ddlColumn("skill", ddlText, ddlRequired),
		ddlColumn("agent_key", ddlText),
		ddlColumn("task_id", ddlBigInt),
		ddlColumn("exploration_id", ddlBigInt),
		ddlColumn("intent_id", ddlBigInt),
		ddlColumn("session_id", ddlText),
		ddlColumn("args_len", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("found", ddlBoolean, ddlRequired, ddlTrue),
	)
	plan.index("idx_skill_usage_skill", "skill_usage", false, "( skill , ts DESC )")
	plan.index("idx_skill_usage_task", "skill_usage", false, "( task_id )")
	plan.raw(`

-- Tool-call ledger (see db/tool_usage.go). One row per actual CoreTool.Call, recording only attribution dimensions,
-- not the tool's arguments or return content. Deliberately no foreign key; statistics survive deletion of a task, session, or custom tool.
`)
	plan.table("tool_usage",
		ddlSequence("id"),
		ddlTimestamp("ts"),
		ddlColumn("tool_key", ddlText, ddlRequired),
		ddlColumn("agent_key", ddlText),
		ddlColumn("task_id", ddlBigInt),
		ddlColumn("exploration_id", ddlBigInt),
		ddlColumn("intent_id", ddlBigInt),
		ddlColumn("session_id", ddlText),
	)
	plan.index("idx_tool_usage_tool", "tool_usage", false, "( tool_key , ts DESC )")
	plan.index("idx_tool_usage_task", "tool_usage", false, "( task_id )")
	plan.raw(`

-- =====================================================================
-- H. Built-in tool catalog
-- =====================================================================
`)
	plan.table("tools",
		ddlColumn("key", ddlText, ddlPrimary),
		ddlColumn("system", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("description", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("schema", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("agents", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("kind", ddlText, ddlRequired, ddlDefault("'builtin'")),
		ddlColumn("exec", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("deferred", ddlBoolean, ddlRequired, ddlFalse),
		ddlTimestamp("updated_at"),
	)
	plan.updatedTrigger("trg_tools_upd", "tools")
	plan.raw(`

-- =====================================================================
-- I. Sessions (chat page)
-- =====================================================================
`)
	plan.table("conversations",
		ddlSequence("id"),
		ddlColumn("agent_key", ddlText, ddlRequired),
		ddlColumn("title", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("llm_profile_id", ddlBigInt, ddlReferences("llm_profiles", "id", "SET NULL")),
		ddlColumn("pinned_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.raw(`
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
`)
	plan.index("idx_conversations_llm_profile", "conversations", false, "( llm_profile_id ) WHERE llm_profile_id IS NOT NULL")
	plan.index("idx_conversations_pinned", "conversations", false, "( pinned_at DESC ) WHERE pinned_at IS NOT NULL")
	plan.updatedTrigger("trg_conversations_upd", "conversations")
	plan.table("conversation_activities",
		ddlSequence("id"),
		ddlColumn("conversation_id", ddlBigInt, ddlRequired, ddlReferences("conversations", "id", "CASCADE")),
		ddlColumn("worker", ddlText),
		ddlColumn("kind", ddlText),
		ddlColumn("tool", ddlText),
		ddlColumn("tool_use_id", ddlText),
		ddlColumn("is_error", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("summary", ddlText),
		ddlColumn("detail", ddlText),
		ddlColumn("input_tokens", ddlInteger),
		ddlColumn("output_tokens", ddlInteger),
		ddlColumn("cache_read_tokens", ddlInteger),
		ddlColumn("cache_write_tokens", ddlInteger),
		ddlTimestamp("created_at"),
	)
	plan.index("idx_conv_act", "conversation_activities", false, "( conversation_id , id )")
	plan.index("idx_conv_act_tool_call", "conversation_activities", false, "( conversation_id , tool_use_id , id ) WHERE kind IN ( 'tool_use' , 'tool_result' )")
	plan.raw(`

-- =====================================================================
-- J. Agent triggers
-- =====================================================================
`)
	plan.table("agent_triggers",
		ddlSequence("id"),
		ddlColumn("agent_key", ddlText, ddlRequired),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("interval_sec", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("on_finding", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("on_goal_met", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("on_task_timeout", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("on_tool_call", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("on_task_create", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("interval_message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("finding_message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("goal_message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("task_timeout_message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("tool_call_message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("task_create_message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("tool_names", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("last_fire", ddlTimestampTZ),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.index("idx_agent_triggers_agent", "agent_triggers", false, "( agent_key )")
	plan.raw(`
-- Add-column migration (already released; old DBs get the columns on upgrade; new DBs' CREATE already has them, so the ALTER is a no-op). Idempotent, re-runnable on every startup.
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS on_tool_call        BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS tool_call_message   TEXT    NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS tool_names          TEXT    NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS on_task_create      BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS task_create_message TEXT    NOT NULL DEFAULT '';
`)
	plan.updatedTrigger("trg_agent_triggers_upd", "agent_triggers")
	plan.table("scheduler_state",
		ddlColumn("key", ddlText, ddlPrimary),
		ddlColumn("value", ddlText, ddlRequired, ddlEmpty),
	)
	plan.raw(`

-- =====================================================================
-- K. Intercept rules
-- =====================================================================
`)
	plan.table("intercept_rules",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("priority", ddlInteger, ddlRequired, ddlZero),
		ddlColumn("match_target", ddlText, ddlRequired, ddlCheck("( match_target IN ( 'tool_name' , 'tool_input' ) )")),
		ddlColumn("match_type", ddlText, ddlRequired, ddlCheck("( match_type IN ( 'string' , 'regex' ) )")),
		ddlColumn("pattern", ddlText, ddlRequired),
		ddlColumn("action", ddlText, ddlRequired, ddlCheck("( action IN ( 'allow' , 'deny' , 'ask' ) )")),
		ddlColumn("message", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("timeout_enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("timeout_seconds", ddlInteger, ddlRequired, ddlDefault("60")),
		ddlColumn("timeout_action", ddlText, ddlRequired, ddlDefault("'deny'")),
		ddlColumn("created_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
		ddlColumn("updated_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
	)
	plan.updatedTrigger("trg_intercept_rules_upd", "intercept_rules")
	plan.table("intercept_pending",
		ddlSequence("id"),
		ddlColumn("rule_id", ddlBigInt, ddlReferences("intercept_rules", "id", "SET NULL")),
		ddlColumn("conversation_id", ddlBigInt, ddlReferences("conversations", "id", "CASCADE")),
		ddlColumn("task_id", ddlText),
		ddlColumn("agent_name", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("tool_name", ddlText, ddlRequired),
		ddlColumn("tool_input", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'pending'"), ddlCheck("( status IN ( 'pending' , 'allowed' , 'denied' , 'timeout' ) )")),
		ddlColumn("reason", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("decided_at", ddlTimestampTZ),
		ddlColumn("created_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
	)
	plan.index("idx_intercept_pending_status", "intercept_pending", false, "( status , created_at DESC )")
	plan.index("idx_intercept_pending_task", "intercept_pending", false, "( task_id , created_at DESC )")
	plan.raw(`
-- Backfill for old DBs: the reason column (already released; adding a column needs IF NOT EXISTS).
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
-- Detail payloads are lazy-loaded; NULL preserves the meaning of legacy history.
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS audit JSONB;
ALTER TABLE intercept_pending ADD COLUMN IF NOT EXISTS decision_source TEXT NOT NULL DEFAULT '';
UPDATE intercept_pending SET decision_source=CASE WHEN rule_id IS NOT NULL THEN 'rule'
 WHEN reason LIKE '[model]%' THEN 'model' ELSE 'unknown' END WHERE decision_source='';

-- =====================================================================
-- L. Vulnerability finding persistence
-- =====================================================================
`)
	plan.table("findings",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlReferences("tasks", "id", "SET NULL")),
		ddlColumn("node_id", ddlBigInt, ddlReferences("exploration_nodes", "id", "SET NULL")),
		ddlColumn("vulnclass", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("name", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("severity", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("summary", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("evidence", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("worker", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("asset_ids", ddlJSONB, ddlRequired, ddlEmptyArray),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'pending'")),
		ddlColumn("report", ddlText, ddlRequired, ddlEmpty),
		ddlTimestamp("created_at"),
	)
	plan.raw(`
ALTER TABLE findings ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS name   TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN IF NOT EXISTS report TEXT NOT NULL DEFAULT '';
`)
	plan.index("idx_findings_task", "findings", false, "( task_id , created_at DESC )")
	plan.index("idx_findings_time", "findings", false, "( created_at DESC )")
	plan.index("idx_findings_status", "findings", false, "( status , created_at DESC )")
	plan.raw(`
-- The "by asset" view reverse-looks-up findings via asset_ids @> '[<id>]'; without this GIN index it's a full table scan.
`)
	plan.index("idx_findings_asset_ids", "findings", false, "USING GIN ( asset_ids jsonb_path_ops )")
	plan.raw(`

-- Manual retests are independent sessions; their verdict is stored separately from the original finding's disposition status.
`)
	plan.table("finding_retests",
		ddlSequence("id"),
		ddlColumn("finding_id", ddlBigInt, ddlRequired, ddlReferences("findings", "id", "CASCADE")),
		ddlColumn("conversation_id", ddlBigInt, ddlUnique, ddlReferences("conversations", "id", "SET NULL")),
		ddlColumn("status", ddlText, ddlRequired, ddlDefault("'pending'"), ddlCheck("( status IN ( 'pending' , 'running' , 'completed' , 'failed' , 'stopped' ) )")),
		ddlColumn("verdict", ddlText, ddlRequired, ddlEmpty, ddlCheck("( verdict IN ( '' , 'reproduced' , 'fixed' , 'inconclusive' ) )")),
		ddlColumn("notes", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("snapshot", ddlJSONB, ddlRequired),
		ddlColumn("summary", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("evidence", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("error", ddlText, ddlRequired, ddlEmpty),
		ddlTimestamp("created_at"),
		ddlColumn("started_at", ddlTimestampTZ),
		ddlColumn("finished_at", ddlTimestampTZ),
	)
	plan.index("idx_finding_retests_history", "finding_retests", false, "( finding_id , id DESC )")
	plan.index("idx_finding_retests_active", "finding_retests", true, "( finding_id ) WHERE status IN ( 'pending' , 'running' )")
	plan.raw(`

-- Deleting a session keeps the retest records while releasing any not-yet-finished retest that was holding it.
CREATE OR REPLACE FUNCTION stop_deleted_conversation_retest() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE finding_retests SET status='stopped', error='retest session deleted', finished_at=now()
    WHERE conversation_id=OLD.id AND status IN ('pending','running');
    RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS trg_conversation_retest_delete ON conversations;
CREATE TRIGGER trg_conversation_retest_delete BEFORE DELETE ON conversations
    FOR EACH ROW EXECUTE FUNCTION stop_deleted_conversation_retest();

ALTER TABLE findings ADD COLUMN IF NOT EXISTS evidence_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE findings ADD COLUMN IF NOT EXISTS report_evidence_version BIGINT NOT NULL DEFAULT 0;

`)
	plan.table("traffic_evidence_snapshots",
		ddlColumn("id", ddlText, ddlPrimary),
		ddlColumn("source_traffic_id", ddlText, ddlRequired),
		ddlColumn("captured_at", ddlBigInt, ddlRequired),
		ddlColumn("url", ddlText, ddlRequired),
		ddlColumn("method", ddlText, ddlRequired),
		ddlColumn("status", ddlInteger, ddlRequired),
		ddlColumn("content_type", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("req_head", ddlText, ddlRequired),
		ddlColumn("resp_head", ddlText, ddlRequired),
		ddlColumn("req_hash", ddlText, ddlRequired),
		ddlColumn("resp_hash", ddlText, ddlRequired),
		ddlColumn("req_len", ddlBigInt, ddlRequired),
		ddlColumn("resp_len", ddlBigInt, ddlRequired),
		ddlColumn("unreferenced_at", ddlTimestampTZ),
		ddlTimestamp("created_at"),
	)
	plan.table("finding_traffic_bindings",
		ddlSequence("id"),
		ddlColumn("finding_id", ddlBigInt, ddlRequired, ddlReferences("findings", "id", "CASCADE")),
		ddlColumn("snapshot_id", ddlText, ddlRequired, ddlReferences("traffic_evidence_snapshots", "id", "")),
		ddlColumn("role", ddlText, ddlRequired, ddlDefault("'supporting'")),
		ddlColumn("note", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("position", ddlInteger, ddlRequired),
		ddlTimestamp("created_at"),
		ddlConstraint("UNIQUE ( finding_id , snapshot_id )"),
	)
	plan.index("idx_finding_traffic_order", "finding_traffic_bindings", false, "( finding_id , position , id )")
	plan.index("idx_finding_traffic_snapshot", "finding_traffic_bindings", false, "( snapshot_id )")
	plan.raw(`

-- =====================================================================
-- M. Backend log persistence
-- =====================================================================
`)
	plan.table("server_logs",
		ddlSequence("id"),
		ddlTimestamp("created_at"),
		ddlColumn("level", ddlText, ddlRequired, ddlDefault("'info'")),
		ddlColumn("tag", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("text", ddlText, ddlRequired, ddlEmpty),
	)
	plan.index("idx_server_logs_id", "server_logs", false, "( id DESC )")
	plan.raw(`

-- Independent /btw history and the latest provider-ready main checkpoint.
`)
	plan.table("side_question_sessions",
		ddlColumn("session_key", ddlText, ddlPrimary),
		ddlColumn("conversation_id", ddlBigInt, ddlReferences("conversations", "id", "CASCADE")),
		ddlColumn("task_id", ddlBigInt, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("exploration_id", ddlBigInt, ddlReferences("explorations", "id", "CASCADE")),
		ddlColumn("intent_id", ddlBigInt, ddlReferences("exploration_nodes", "id", "CASCADE")),
		ddlColumn("run_id", ddlBigInt, ddlRequired),
		ddlColumn("version", ddlBigInt, ddlRequired),
		ddlColumn("snapshot", ddlJSONB, ddlRequired),
		ddlColumn("generation", ddlBigInt, ddlRequired, ddlZero),
		ddlConstraint("CHECK ( ( conversation_id IS NOT NULL AND task_id IS NULL AND exploration_id IS NULL AND intent_id IS NULL ) OR ( conversation_id IS NULL AND task_id IS NOT NULL AND exploration_id IS NOT NULL ) )"),
	)
	plan.index("idx_side_sessions_conv", "side_question_sessions", false, "( conversation_id )")
	plan.index("idx_side_sessions_task", "side_question_sessions", false, "( task_id )")
	plan.index("idx_side_sessions_exp", "side_question_sessions", false, "( exploration_id )")
	plan.index("idx_side_sessions_intent", "side_question_sessions", false, "( intent_id )")
	plan.table("side_question_requests",
		ddlColumn("id", ddlText, ddlPrimary),
		ddlColumn("ordinal", ddlBigSerial, ddlUnique),
		ddlColumn("session_key", ddlText, ddlRequired, ddlReferences("side_question_sessions", "session_key", "CASCADE")),
		ddlColumn("generation", ddlBigInt, ddlRequired),
		ddlColumn("client_id", ddlText, ddlRequired),
		ddlColumn("question", ddlText, ddlRequired),
		ddlColumn("answer", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("status", ddlText, ddlRequired, ddlCheck("( status IN ( 'running' , 'completed' , 'failed' , 'cancelled' , 'interrupted' ) )")),
		ddlColumn("error", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("model", ddlJSONB, ddlRequired),
		ddlColumn("snapshot_at", ddlTimestampTZ, ddlRequired),
		ddlTimestamp("created_at"),
		ddlColumn("sequence", ddlBigInt, ddlRequired, ddlZero),
		ddlColumn("usage", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlConstraint("UNIQUE ( session_key , generation , client_id )"),
	)
	plan.index("idx_side_request_running", "side_question_requests", true, "( session_key ) WHERE status = 'running'")
	plan.index("idx_side_requests_history", "side_question_requests", false, "( session_key , ordinal DESC )")
	plan.raw(`

-- Additive v3 archive fields; old archives restore these as empty objects.
ALTER TABLE side_question_sessions ADD COLUMN IF NOT EXISTS memory JSONB NOT NULL DEFAULT '{}';
ALTER TABLE side_question_requests ADD COLUMN IF NOT EXISTS context_info JSONB NOT NULL DEFAULT '{}';

-- =====================================================================
-- Asset intercept rules (global blocklist)
-- Independent of the §K command intercept (intercept_rules): intercept_rules matches tool name / input text,
-- this table matches the "target asset" — exact/fuzzy domain · IP · URL as well as CIDR ranges.
-- Stores rules only; the actual match/intercept logic is implemented elsewhere.
-- Seven kinds:
--   exact_domain / exact_ip / exact_url  —— exact match
--   fuzzy_domain / fuzzy_ip / fuzzy_url  —— fuzzy match
--   cidr                                 —— CIDR range
-- =====================================================================
`)
	plan.table("asset_intercept_rules",
		ddlSequence("id"),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("kind", ddlText, ddlRequired, ddlCheck("( kind IN ( 'exact_domain' , 'exact_ip' , 'exact_url' , 'fuzzy_domain' , 'fuzzy_ip' , 'fuzzy_url' , 'cidr' ) )")),
		ddlColumn("pattern", ddlText, ddlRequired),
		ddlColumn("note", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("builtin", ddlBoolean, ddlRequired, ddlFalse),
		ddlColumn("created_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
		ddlColumn("updated_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
	)
	plan.index("idx_asset_intercept_enabled", "asset_intercept_rules", false, "( enabled )")
	plan.updatedTrigger("trg_asset_intercept_rules_upd", "asset_intercept_rules")
	plan.raw(`

-- =====================================================================
-- Task-level asset intercept/allow rules
-- Isomorphic to the global asset_intercept_rules (kind/pattern/note/enabled), but associated by task_id
-- and cascade-deleted with the task; entered at task creation and editable in the task detail view.
-- action: 'block'=intercept (testing forbidden)  'allow'=allow (whitelist).
-- Decision at execution: first match against the intercept rules (global ∪ task block); a hit means forbidden; if there's no hit and
-- the task has enabled allow rules, some allow rule must match to let it through, otherwise "testing not allowed".
-- =====================================================================
`)
	plan.table("task_intercept_rules",
		ddlSequence("id"),
		ddlColumn("task_id", ddlBigInt, ddlRequired, ddlReferences("tasks", "id", "CASCADE")),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("action", ddlText, ddlRequired, ddlDefault("'block'"), ddlCheck("( action IN ( 'block' , 'allow' ) )")),
		ddlColumn("kind", ddlText, ddlRequired, ddlCheck("( kind IN ( 'exact_domain' , 'exact_ip' , 'exact_url' , 'fuzzy_domain' , 'fuzzy_ip' , 'fuzzy_url' , 'cidr' ) )")),
		ddlColumn("pattern", ddlText, ddlRequired),
		ddlColumn("note", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("created_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
		ddlColumn("updated_at", ddlTimestampTZ, ddlRequired, ddlDefault("NOW ( )")),
	)
	plan.index("idx_task_intercept_task", "task_intercept_rules", false, "( task_id )")
	plan.raw(`
-- Backfill for old DBs (this session created the table earlier without the action column): add the column (with IF NOT EXISTS).
ALTER TABLE task_intercept_rules ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT 'block';
`)
	plan.updatedTrigger("trg_task_intercept_rules_upd", "task_intercept_rules")
	plan.raw(`

-- =====================================================================
-- M. Vulnerability IM push
--
-- The three tables are deliberately separate; the core reason is **blast radius**: the transaction that writes the finding (RecordFindingTx,
-- holding the task row lock) is only allowed one blind INSERT — it must not read the channel table or run the user's filter rules. Otherwise
-- a single misconfigured webhook filter condition could pollute/abort the transaction and keep the finding from being stored.
--
--   notification_channels   channel instance config (mutable, holds credentials, UI-managed)
--   notification_events     event facts (blind-inserted inside the write-finding transaction, with a rendered snapshot)
--   notification_deliveries delivery tasks (produced by fan-out outside the transaction, carrying state/retry/batch)
-- =====================================================================

-- Channel instance: the same kind can be configured any number of times (e.g. an "emergency group" and a "daily group", each its own DingTalk bot).
-- kind values are validated by the server-side whitelist, no CHECK: same as findings.status,
-- adding channels later should not require a schema change.
`)
	plan.table("notification_channels",
		ddlSequence("id"),
		ddlColumn("name", ddlText, ddlRequired),
		ddlColumn("kind", ddlText, ddlRequired),
		ddlColumn("enabled", ddlBoolean, ddlRequired, ddlTrue),
		ddlColumn("config", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("mode", ddlText, ddlRequired, ddlDefault("'realtime'")),
		ddlColumn("filter", ddlJSONB, ddlRequired, ddlEmptyObject),
		ddlColumn("rate_per_min", ddlInteger, ddlRequired, ddlDefault("20")),
		ddlTimestamp("created_at"),
		ddlTimestamp("updated_at"),
	)
	plan.updatedTrigger("trg_notification_channels_upd", "notification_channels")
	plan.raw(`

-- Event facts. Written by RecordFindingTx / status-change transactions **in the same transaction**, guaranteeing that "finding stored"
-- and "push task exists" are atomically consistent — there is no window where the commit succeeds but nothing was enqueued and the message is lost forever.
-- snapshot is deliberately redundant: a finding is later renamed/re-leveled/re-statused, but the push content should reflect "as it was at the time",
-- and fan-out and rendering need not query back across the findings/tasks/assets tables.
-- Events are not cascade-deleted when a finding is deleted: consistent with the findings table's "survives independently even after task deletion" semantics.
`)
	plan.table("notification_events",
		ddlSequence("id"),
		ddlColumn("kind", ddlText, ddlRequired),
		ddlColumn("finding_id", ddlBigInt, ddlRequired),
		ddlColumn("snapshot", ddlJSONB, ddlRequired),
		ddlColumn("fanned_out", ddlBoolean, ddlRequired, ddlFalse),
		ddlTimestamp("created_at"),
	)
	plan.index("idx_notification_events_pending", "notification_events", false, "( id ) WHERE NOT fanned_out")
	plan.raw(`

-- Delivery tasks: one event × one enabled channel = one row. fan-out happens outside the transaction, so enabling a channel
-- later does not backfill history (same semantics as agent_triggers' "a late-enabled trigger doesn't backfill history",
-- avoiding a flood of backlogged history when a channel is enabled).
-- channel_id cascade delete: once the channel config is gone, its delivery history is meaningless.
`)
	plan.table("notification_deliveries",
		ddlSequence("id"),
		ddlColumn("event_id", ddlBigInt, ddlRequired, ddlReferences("notification_events", "id", "CASCADE")),
		ddlColumn("channel_id", ddlBigInt, ddlRequired, ddlReferences("notification_channels", "id", "CASCADE")),
		ddlColumn("state", ddlText, ddlRequired, ddlDefault("'pending'")),
		ddlColumn("attempts", ddlInteger, ddlRequired, ddlZero),
		ddlTimestamp("next_attempt_at"),
		ddlColumn("last_error", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("batch_id", ddlBigInt),
		ddlTimestamp("created_at"),
		ddlColumn("sent_at", ddlTimestampTZ),
	)
	plan.index("idx_notification_deliveries_due", "notification_deliveries", false, "( next_attempt_at ) WHERE state = 'pending'")
	plan.index("idx_notification_deliveries_history", "notification_deliveries", false, "( id DESC )")
	plan.index("idx_notification_deliveries_batch", "notification_deliveries", false, "( batch_id ) WHERE batch_id IS NOT NULL")
	plan.index("idx_notification_deliveries_channel", "notification_deliveries", false, "( channel_id , id DESC )")
	plan.raw(`

-- Durable canonical API Lab evidence, independent of sidecar history retention.
`)
	plan.table("finding_experiments",
		ddlSequence("id"),
		ddlColumn("finding_id", ddlBigInt, ddlRequired, ddlReferences("findings", "id", "CASCADE")),
		ddlColumn("server", ddlText, ddlRequired),
		ddlColumn("job_id", ddlText, ddlRequired),
		ddlColumn("artifact_name", ddlText, ddlRequired),
		ddlColumn("hash", ddlText, ddlRequired),
		ddlColumn("recipe_hash", ddlText, ddlRequired),
		ddlColumn("snapshot", ddlText, ddlRequired),
		ddlTimestamp("created_at"),
		ddlConstraint("UNIQUE ( finding_id , hash , artifact_name )"),
	)
	plan.table("finding_experiment_replays",
		ddlColumn("retest_id", ddlBigInt, ddlRequired, ddlReferences("finding_retests", "id", "CASCADE")),
		ddlColumn("experiment_id", ddlBigInt, ddlRequired, ddlReferences("finding_experiments", "id", "CASCADE")),
		ddlColumn("job_id", ddlText, ddlRequired),
		ddlColumn("snapshot", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("hash", ddlText, ddlRequired, ddlEmpty),
		ddlColumn("verdict", ddlText, ddlRequired, ddlEmpty),
		ddlConstraint("PRIMARY KEY ( retest_id , job_id )"),
	)
	return plan.String()
}
