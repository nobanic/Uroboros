package db

import (
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// TaskScope is one row of a task's test scope — the coverage denominator and the
// per-task authorization edge. Rows come from trusted target seeding and domain
// assets (source='auto') or from explicit scope changes (source='agent'/'manual').
type TaskScope struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|cidr|icp|keyword
	CompanyID *int64 `json:"company_id,omitempty"`
	// CompanyName is resolved for kind=company so callers can label a scope row
	// without a second lookup. Empty when the row is not a company reference.
	CompanyName string `json:"company_name,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Net         string `json:"net,omitempty"`
	Value       string `json:"value,omitempty"`
	Source      string `json:"source"`
	Reason      string `json:"reason,omitempty"`
}

// stripHostPort drops a trailing :port from a host:port / ip:port / [ipv6]:port
// value, returning the bare host. Bare hosts, bare IPs (v4 or v6, whose colons make
// them ambiguous), and anything not in host:port form are returned unchanged. Scope
// is host/net based, so the port from a target like "10.0.188.136:3000" or
// "api.example.com:8080" is simply discarded rather than baked into the key.
func stripHostPort(v string) string {
	v = strings.TrimSpace(v)
	if host, _, err := net.SplitHostPort(v); err == nil {
		return host
	}
	return v
}

// ipToHostCIDR turns a bare IP into its single-host CIDR (/32 or /128). "" if invalid.
func ipToHostCIDR(ip string) string {
	ip = strings.TrimSpace(ip)
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	if p.To4() != nil {
		return ip + "/32"
	}
	return ip + "/128"
}

// upsertTaskScope inserts one scope row idempotently (uq_task_scope). A duplicate is
// silently ignored. taskID<=0 or empty kind → no-op.
func (s *AssetStore) upsertTaskScopeResult(ts TaskScope) (bool, error) {
	if ts.TaskID <= 0 || ts.Kind == "" {
		return false, nil
	}
	var domainVal, netVal, companyVal, valueVal any
	if ts.Domain != "" {
		domainVal = ts.Domain
	}
	if ts.Net != "" {
		netVal = ts.Net
	}
	if ts.CompanyID != nil && *ts.CompanyID > 0 {
		companyVal = *ts.CompanyID
	}
	if ts.Value != "" {
		valueVal = ts.Value
	}
	src := ts.Source
	if src == "" {
		src = "auto"
	}
	query := `
INSERT INTO task_scope(task_id, kind, company_id, domain, net, value, source, reason)
VALUES ($1,$2,$3,$4,$5::cidr,$6,$7,NULLIF($8,''))
ON CONFLICT DO NOTHING`
	var (
		result sql.Result
		err    error
	)
	if s.tx != nil {
		result, err = s.tx.Exec(query, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, valueVal, src, ts.Reason)
	} else {
		result, err = s.db.Exec(query, ts.TaskID, ts.Kind, companyVal, domainVal, netVal, valueVal, src, ts.Reason)
	}
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *AssetStore) upsertTaskScope(ts TaskScope) error {
	_, err := s.upsertTaskScopeResult(ts)
	return err
}

// autoTaskScope returns the conservative task scope implied by one asset. It is
// shared by trusted target seeding and agent-discovery admission; the latter
// applies an additional network-authority guard below.
func autoTaskScope(taskID int64, assetType, domain, rawURL, ip string) (TaskScope, bool) {
	if taskID <= 0 {
		return TaskScope{}, false
	}
	switch assetType {
	case "root_domain":
		if d := DomainKey(domain); d != "" {
			return TaskScope{TaskID: taskID, Kind: "root_domain", Domain: d}, true
		}
	case "subdomain":
		if d := DomainKey(domain); d != "" {
			return TaskScope{TaskID: taskID, Kind: "subdomain", Domain: d}, true
		}
	case "service", "endpoint":
		host := domain
		if host == "" && rawURL != "" {
			host, _, _ = parseURL(normalizeURL(rawURL))
		}
		host = DomainKey(host)
		if host != "" && net.ParseIP(host) == nil {
			return TaskScope{TaskID: taskID, Kind: "subdomain", Domain: host}, true
		}
		// IP-literal host or no host → fall back to ip scope if we have one.
		if c := ipToHostCIDR(ip); c != "" {
			return TaskScope{TaskID: taskID, Kind: "ip", Net: c}, true
		}
	case "ip":
		if c := ipToHostCIDR(ip); c != "" {
			return TaskScope{TaskID: taskID, Kind: "ip", Net: c}, true
		}
	}
	return TaskScope{}, false
}

// AddAutoScope records the scope implied by a trusted, explicitly selected
// target, such as the task's initial target. taskID<=0 or an invalid asset is a
// no-op. Agent-discovered assets must use AddAutoScopeForDiscoveredAsset.
func (s *AssetStore) AddAutoScope(taskID int64, assetType, domain, rawURL, ip string) error {
	ts, ok := autoTaskScope(taskID, assetType, domain, rawURL, ip)
	if !ok {
		return nil
	}
	return s.upsertTaskScope(ts)
}

func discoveredAutoTaskScope(taskID int64, assetType, domain, rawURL, ip string) (TaskScope, bool) {
	ts, ok := autoTaskScope(taskID, assetType, domain, rawURL, ip)
	if !ok || ts.Kind == "ip" || ts.Kind == "cidr" {
		return TaskScope{}, false
	}
	return ts, true
}

// AddAutoScopeForDiscoveredAsset records only domain authority implied by an
// agent-discovered asset. An observed DNS address or IP-literal service remains
// available as task-linked asset evidence, but cannot silently grant a new
// IP/CIDR authorization boundary. Network scope requires an explicit initial
// target, operator action, or add_task_scope decision.
func (s *AssetStore) AddAutoScopeForDiscoveredAsset(taskID int64, assetType, domain, rawURL, ip string) error {
	ts, ok := discoveredAutoTaskScope(taskID, assetType, domain, rawURL, ip)
	if !ok {
		return nil
	}
	return s.upsertTaskScope(ts)
}

// AddAgentScope parses a (kind,value) pair and records it as task scope.
// source is "agent" when called from an LLM tool, "manual" from the UI.
// company: value = company name or id (must already exist). root_domain/subdomain:
// value = a domain. ip/cidr: value = an IP or CIDR (bare IP → /32,/128).
func (s *AssetStore) AddAgentScope(taskID int64, kind, value, reason, source string) (TaskScope, error) {
	if source == "" {
		source = "agent"
	}
	ts := TaskScope{TaskID: taskID, Kind: kind, Source: source, Reason: reason}
	if taskID <= 0 {
		return ts, fmt.Errorf("task_id is required")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ts, fmt.Errorf("value cannot be empty")
	}
	switch kind {
	case "company":
		if s.company == nil {
			return ts, fmt.Errorf("company store is not enabled")
		}
		var comp *Company
		var err error
		if id, e := strconv.ParseInt(value, 10, 64); e == nil {
			comp, err = s.company.GetCompany(id)
		} else {
			comp, err = s.company.GetCompanyByName(value)
		}
		if err != nil {
			return ts, err
		}
		if comp == nil {
			return ts, fmt.Errorf("company does not exist: %s (confirm with list_companies first, or create the company)", value)
		}
		ts.CompanyID = &comp.ID
	case "root_domain":
		d := DomainKey(stripHostPort(value))
		root, _ := RootDomain(d)
		if root == "" {
			root = d
		}
		if root == "" {
			return ts, fmt.Errorf("invalid root domain: %s", value)
		}
		ts.Domain = root
	case "subdomain":
		d := DomainKey(stripHostPort(value))
		if d == "" {
			return ts, fmt.Errorf("invalid subdomain: %s", value)
		}
		ts.Domain = d
	case "ip", "cidr":
		v := value
		if !strings.Contains(v, "/") {
			v = ipToHostCIDR(stripHostPort(v))
			ts.Kind = "ip"
		} else {
			ts.Kind = "cidr"
		}
		if v == "" {
			return ts, fmt.Errorf("invalid ip/cidr: %s", value)
		}
		if _, _, err := net.ParseCIDR(v); err != nil {
			return ts, fmt.Errorf("invalid ip/cidr: %s", value)
		}
		ts.Net = v
	case "icp", "keyword":
		parsed, err := ParseScopeInput(ScopeInput{Kind: kind, Value: value})
		if err != nil {
			return ts, err
		}
		ts.Value = parsed.Value
	default:
		return ts, fmt.Errorf("unsupported kind: %s (company/root_domain/subdomain/ip/cidr/icp/keyword)", kind)
	}
	if err := s.upsertTaskScope(ts); err != nil {
		return ts, err
	}
	return ts, nil
}

// DeleteTaskScope removes a single scope row by id, scoped to the given task.
// Returns whether a row was actually deleted.
func (s *AssetStore) DeleteTaskScope(taskID, scopeID int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM task_scope WHERE id=$1 AND task_id=$2`, scopeID, taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListTaskScope returns all scope rows for a task.
func (s *AssetStore) ListTaskScope(taskID int64) ([]TaskScope, error) {
	return collectQueryState(queriedRows(s.db.Query(`
SELECT ts.id, ts.kind, COALESCE(ts.company_id,0), COALESCE(c.name,''), COALESCE(ts.domain,''),
       COALESCE(ts.net::text,''), COALESCE(ts.value,''), ts.source, COALESCE(ts.reason,'')
FROM task_scope ts
LEFT JOIN companies c ON c.id=ts.company_id
WHERE ts.task_id=$1 ORDER BY ts.id`, taskID)), func() []TaskScope {
		return []TaskScope{}
	}, func(queryRow rowDecoder, queryState *[]TaskScope) error {
		var t TaskScope
		var cid int64
		if err := queryRow.Scan(&t.ID, &t.Kind, &cid, &t.CompanyName, &t.Domain, &t.Net, &t.Value, &t.Source, &t.Reason); err != nil {
			return err
		}
		t.TaskID = taskID
		if cid > 0 {
			t.CompanyID = &cid
		}
		*queryState = append(*queryState, t)
		return nil
	})
}

// CoverageAsset is one in-scope asset (used for the untested backlog sample).
type CoverageAsset struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// CoverageByType is per-asset-type coverage: total in scope vs tested.
type CoverageByType struct {
	Type   string `json:"type"`
	Total  int    `json:"total"`
	Tested int    `json:"tested"`
}

// Coverage is a task's rough asset test coverage — a reference figure for the agent,
// NOT a precise metric. Denominator = assets matching any active task_scope row;
// Tested = those anchored to at least one fact node in the exploration.
type Coverage struct {
	Enabled     bool             `json:"enabled"`     // whether the asset-coverage feature is on; when false the other fields are zero values
	ScopeRows   int              `json:"scope_rows"`  // 0 → scope not anchored
	Denominator int              `json:"denominator"` // number of assets within scope
	Tested      int              `json:"tested"`      // tested (approx)
	Pct         *float64         `json:"pct"`         // coverage; null when the denominator is 0
	ByType      []CoverageByType `json:"by_type"`     // total/tested by asset type
}

// CoverageEnabled reports whether a task has the asset-coverage feature turned on
// (tasks.coverage_enabled). Missing row / error → true (fail open to the default),
// so unknown/legacy tasks keep the historical behavior. taskID<=0 → true.
func (s *AssetStore) CoverageEnabled(taskID int64) bool {
	if taskID <= 0 {
		return true
	}
	var enabled bool
	if err := s.db.QueryRow(`SELECT COALESCE(coverage_enabled,true) FROM tasks WHERE id=$1`, taskID).Scan(&enabled); err != nil {
		return true
	}
	return enabled
}

// task_scope→assets match predicate, reused by the count / by-type / untested queries. $1=taskID.
const covTargetCTE = `
target AS (
  SELECT DISTINCT a.id, a.type,
         COALESCE(a.url, a.domain, a.ip, a.app_name, a.root_domain, '') AS label
  FROM assets a
  JOIN task_scope ts ON ts.task_id = $1 AND (
       (ts.kind='company'     AND a.company_id = ts.company_id)
    OR (ts.kind='root_domain' AND a.root_domain = ts.domain)
    OR (ts.kind='subdomain'   AND a.domain = ts.domain)
    OR (ts.kind IN ('ip','cidr') AND ts.net >>= try_inet(a.ip))
    OR (ts.kind='icp' AND (
         lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = ts.value
         OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = ts.value
       ))
  )
),
tested AS (
  SELECT DISTINCT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes en ON en.id = ea.node_id
  WHERE en.exploration_id = $2 AND en.kind = 'fact'
)`

// TaskCoverage computes rough per-type coverage for a task. taskID indexes
// task_scope + assets; expID indexes the fact anchors. Reference figure only.
func (s *AssetStore) TaskCoverage(taskID, expID int64) (*Coverage, error) {
	return (legacyCoverageRead{store: s, taskID: taskID, explorationID: expID}).count()
}

// ---------------------------------------------------------------------------
// Coverage graph — a force-directed view of a task's in-scope assets.
// ---------------------------------------------------------------------------

// CoverageGraphNode is one node of the asset coverage graph. Node identity is a
// string key: an asset row is "a:<id>", a company is "c:<id>", and a root domain
// with no asset row of its own is the synthetic "r:<domain>". Out-of-scope
// connector nodes (roots pulled in only to link subdomains, and the companies
// above them) carry InScope=false and are rendered gray.
type CoverageGraphNode struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint
	Label       string `json:"label"`
	Tested      bool   `json:"tested"`
	InScope     bool   `json:"in_scope"`
	AssetID     int64  `json:"asset_id,omitempty"` // 0 for company / synthetic root
	CompanyID   int64  `json:"company_id,omitempty"`
	Domain      string `json:"domain,omitempty"`
	RootDomain  string `json:"root_domain,omitempty"`
	IP          string `json:"ip,omitempty"`
	URL         string `json:"url,omitempty"`
	Port        int    `json:"port,omitempty"`
	ServiceType string `json:"service_type,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	PageTitle   string `json:"page_title,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
}

// CoverageGraphEdge is a child→parent containment link (endpoint→service→
// subdomain/ip→root_domain→company, app→company).
type CoverageGraphEdge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// CoverageGraphData is the whole graph for one task.
type CoverageGraphData struct {
	Nodes []CoverageGraphNode `json:"nodes"`
	Edges []CoverageGraphEdge `json:"edges"`
}

func assetKey(id int64) string   { return "a:" + strconv.FormatInt(id, 10) }
func companyKey(id int64) string { return "c:" + strconv.FormatInt(id, 10) }

// hostPortOf returns the (host, port) a service / endpoint node hangs off — the
// domain if set, otherwise the URL host, otherwise the IP.
func hostPortOf(n *CoverageGraphNode) (string, int) {
	host, port := n.Domain, n.Port
	if host == "" && n.URL != "" {
		h, p, _ := parseURL(normalizeURL(n.URL))
		host = h
		if port == 0 {
			port = p
		}
	}
	if host == "" {
		host = n.IP
	}
	return host, port
}

// BuildCoverageGraph assembles the full coverage graph for a task and its direct
// read-only sources: every in-scope asset plus connector root domains/companies,
// with current-or-source fact anchors reflected in Tested. The legacy expID
// argument is retained for API compatibility; the task registry is authoritative.
func (s *AssetStore) BuildCoverageGraph(taskID, _ int64) (*CoverageGraphData, error) {
	return (coverageGraphRead{store: s, taskID: taskID}).graph()
}

// coverageNodeLabel picks the human label for a coverage-graph node.
func coverageNodeLabel(n *CoverageGraphNode) string {
	switch n.Kind {
	case "endpoint", "service":
		if n.URL != "" {
			return n.URL
		}
	case "app":
		if n.AppName != "" {
			return n.AppName
		}
	}
	for _, v := range []string{n.URL, n.Domain, n.IP, n.AppName, n.RootDomain} {
		if v != "" {
			return v
		}
	}
	return n.Key
}

// ListUntestedAssets returns a task's in-scope, not-yet-tested assets, optionally
// filtered by asset type, paginated. Returns the page + the total count. limit<=0 → 10.
func (s *AssetStore) ListUntestedAssets(taskID, expID int64, typ string, limit, offset int) ([]CoverageAsset, int, error) {
	return (legacyCoverageRead{store: s, taskID: taskID, explorationID: expID}).untested(typ, limit, offset)
}
