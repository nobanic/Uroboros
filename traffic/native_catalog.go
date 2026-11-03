package traffic

import actool "github.com/nobanic/norma/tool"

// The catalog binds immutable command descriptions to typed native executors.
func trafficCommandCatalog(t *Traffic) []actool.CoreTool {
	return []actool.CoreTool{
		bindTrafficCommand[trafficSearchInput]("traffic_search", TrafficSearchDescription, []trafficParameter{
			{key: "body_contains", kind: "string", description: "Full-text body search (optional, at least 3 characters), matching request/response headers and bodies, e.g. 'password' / 'root:x:0' / 'internal-test'"},
			{key: "contains", kind: "string", description: "URL substring filter (optional, e.g. 'api' / 'login')"},
			{key: "host", kind: "string", description: "Filter by host (required; e.g. '107.172.96.177', '107.172.96.177:8082', or 'http://107.172.96.177:8082/path')", required: true},
			{key: "limit", kind: "integer", description: "Entries per page, default 3, max 10"},
			{key: "page", kind: "integer", description: "Page number, starting at 0, default 0 (paged by ts descending)"},
		}, false, t.executeTrafficSearch),
		bindTrafficCommand[trafficGetInput]("traffic_get", "Fetch the raw request/response of one captured exchange by id (truncated if too large). Use together with traffic_search to avoid re-curling.", []trafficParameter{
			{key: "id", kind: "string", description: "the id returned by traffic_search", required: true},
		}, false, t.executeTrafficGet),
		bindTrafficCommand[trafficBlobInput]("traffic_blob", "Read the raw body of an oversized request/response in chunks. The part shown in traffic_get as '…[truncated] @blob sha256:<hash>' is stored here; pass that hash in to fetch the full content. Returns at most 8KB per call; use offset to read further (the result reports the total length). Suited to paging through backup files, source-code leaks, large JSON exports, and other responses that exceed the inline threshold.", []trafficParameter{
			{key: "hash", kind: "string", description: "the 64-character hexadecimal value after @blob sha256: in traffic_get", required: true},
			{key: "length", kind: "integer", description: "Number of bytes to read this time, default and maximum 8192"},
			{key: "offset", kind: "integer", description: "Starting byte offset, default 0"},
		}, false, t.executeTrafficBlob),
		bindTrafficCommand[trafficMatchInput]("traffic_match", "Search the immutable request/response body of one captured exchange by traffic id. Supports literal or RE2 regular-expression matching and returns bounded before/match/after context plus reproducible byte offsets and line/column. This searches the full content-addressed blob when present (up to an explicit 8 MiB scan cap) without copying a giant response into model context.", []trafficParameter{
			{key: "case_sensitive", kind: "boolean", description: "case-sensitive matching; default false"},
			{key: "context_bytes", kind: "integer", description: "bytes of bounded context before and after each match, default 160, max 1024"},
			{key: "id", kind: "string", description: "captured exchange id returned by traffic_search or http_request", required: true},
			{key: "max_matches", kind: "integer", description: "maximum returned matches, default 8, max 20"},
			{key: "pattern", kind: "string", description: "literal text or RE2 pattern to search for (max 512 bytes)", required: true},
			{key: "regex", kind: "boolean", description: "interpret pattern as an RE2 regular expression; default false (literal)"},
			{key: "side", kind: "string", description: "body side to search, default response", choices: []string{"request", "response", "both"}},
		}, true, t.executeTrafficMatch),
		bindTrafficCommand[trafficRoutesInput]("traffic_routes", "Extract and deduplicate route-looking strings from one captured response body without loading the whole HTML/JavaScript bundle into context. Defaults to /api and /webapi, understands common JavaScript slash escapes, and returns bounded evidence context, byte offsets, occurrence counts, body SHA-256 and blob provenance.", []trafficParameter{
			{key: "context_bytes", kind: "integer", description: "bounded context around the first occurrence, default 96, max 1024"},
			{key: "id", kind: "string", description: "captured exchange id returned by traffic_search or http_request", required: true},
			{key: "max_candidates", kind: "integer", description: "maximum unique candidates, default 50, max 200"},
			{key: "prefixes", kind: "array", description: "route prefixes, default [/api, /webapi], at most 8", itemKind: "string"},
		}, true, t.executeTrafficRoutes),
		bindTrafficCommand[trafficContractInput]("traffic_contract_worklist", "Build a deterministic, bounded client-contract worklist across all captured HTML/JavaScript for one target host or host:port. It searches several literal or RE2 route/symbol leads with immutable body hashes and bounded caller context, extracts referenced JavaScript chunks, identifies which chunks are already captured, and returns referenced-but-uncaptured same-target chunks as pending_fetches. The tool is read-only: fetch useful pending URLs with http_request and rerun it until the relevant client call contract is recovered or search_complete proves the bounded corpus was covered.", []trafficParameter{
			{key: "case_sensitive", kind: "boolean", description: "case-sensitive matching; default false"},
			{key: "context_bytes", kind: "integer", description: "bounded raw context before and after each match, default 240, max 1024"},
			{key: "host", kind: "string", description: "target host, host:port, or full target URL; a supplied port is isolated from other services", required: true},
			{key: "max_documents", kind: "integer", description: "maximum unique captured client documents to inspect, default 48, max 128"},
			{key: "max_matches", kind: "integer", description: "maximum total bounded matches returned, default 40, max 80"},
			{key: "max_total_bytes", kind: "integer", description: "cross-document response scan budget in bytes, default 33554432, max 67108864"},
			{key: "patterns", kind: "array", description: "literal route/symbol leads, or RE2 expressions when regex=true; at most 8, each at most 512 bytes; may be empty for a chunk inventory", itemKind: "string"},
			{key: "regex", kind: "boolean", description: "interpret every pattern as RE2; default false"},
		}, true, t.executeTrafficContract),
	}
}
