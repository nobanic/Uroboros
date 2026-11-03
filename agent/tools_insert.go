package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/internal/toolinput"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

// assetInterceptCandidates extracts the domain/IP/URL candidate strings from one asset
// input item, for asset-interception matching. A URL's host is split out and classified
// so that a service/endpoint asset carrying "only a URL" can still be hit by domain/IP rules.
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// assetInputLabel returns a short identifier for an asset to be inserted, used in interception messages.
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(unknown)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"Bulk-register newly discovered assets; a single call may mix several types (see the type enum).\n"+
			"Required fields per type: root_domain→domain; ip→ip (must be IPv4/IPv6, not a hostname); subdomain→domain; app→app_name; service(HTTP)→url; service(non-HTTP)→service_name+port (at least one of ip/domain); endpoint→url+method. The meaning of the other fields is in their individual descriptions.\n"+
			"Discovered IPs and IP-literal services are stored as task-linked evidence but never expand this task's IP/CIDR authorization scope; network scope requires a separate explicit authorization decision.\n"+
			"auth/technologies/params are append-merged, not overwriting existing values.\n"+
			"Returns: {results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id is not exposed to the model: which task a worker belongs to is authoritatively assigned by the program via SetTaskID (see handler).
			"assets": map[string]any{
				"type":        "array",
				"description": "array of assets; each element is one asset record",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "asset type",
					},
					// root_domain / subdomain
					"domain":      str("root domain or subdomain (required for root_domain/subdomain)"),
					"icp":         str("ICP filing number (optional)"),
					"record_type": str("DNS record type: A/AAAA/CNAME/MX, etc. (optional for subdomain)"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "list of DNS record values (optional for subdomain, e.g. [\"1.2.3.4\",\"2.3.4.5\"])",
					},
					// ip
					"ip": str("IP address, must be an IPv4/IPv6 address, not a hostname (for a hostname use the domain field with type=subdomain); required for the ip type; optional for service/endpoint types, used to associate an IP"),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "list of domains bound to this IP (optional for the ip type)",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "list of open ports (optional for the ip type)",
						"items": obj(map[string]any{
							"port":    intp("port number"),
							"service": str("service name, e.g. http/ssh/mysql (optional)"),
						}, "port"),
					},
					// app
					"app_name":    str("application name (required for the app type)"),
					"bundle_id":   str("Bundle ID (optional for the app type)"),
					"category":    str("application category (optional)"),
					"description": str("application description (optional)"),
					"app_icp":     str("application ICP filing (optional)"),
					"company_id":  intp("owning company id (optional for the app type; an app cannot be auto-attributed via scope and must be specified explicitly; the id is returned by add_company_scope)"),
					// service (http)
					"url":         str("full URL, including scheme and port (required for an HTTP service; service_type is set to http automatically)"),
					"status_code": intp("HTTP response status code, e.g. 200/301/403/404 (optional)"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP response body size in bytes (optional)",
					},
					"page_title":   str("the page's <title> content (optional)"),
					"favicon_mmh3": str("favicon MMH3 hash (optional)"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "list of fingerprints/tech stack, e.g. [\"Nginx\",\"Vue\",\"Bootstrap\"] (optional)",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "list of discovered auth info; each entry has fields like type/username/password (optional, append not overwrite)",
						"items":       map[string]any{"type": "object"},
					},
					// service (other, non-HTTP)
					"service_name": str("service name, e.g. ssh/mysql/redis (required for a non-HTTP service)"),
					"port":         intp("port number (required for a non-HTTP service)"),
					// endpoint
					"method": str("HTTP method: GET/POST/PUT/PATCH/DELETE, etc. (required for endpoint)"),
					"params": map[string]any{
						"type":        "array",
						"description": "list of request parameters; each entry has location(query/body/header/path)/name/value/type (optional, append not overwrite)",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		t.acceptAssetBatch,
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"Add a domain/IP/CIDR/ICP filing/company keyword to a company's [asset scope] — domains, networks, and ICPs automatically claim the assets they match; keywords are only provided to the Agent as a scope hint.\n"+
			"Company name is unique: if company does not exist it is created, if it exists it is reused (the scope is just merged in).\n"+
			"One scope entry per line; the system recognizes it automatically: root domain / URL / single IP / CIDR network / ICP filing / company keyword.\n"+
			"Always provide reason to state the attribution basis (whois/certificate/ASN, etc.).\n"+
			"Guardrails: bare TLDs and overly broad networks are rejected (IPv4 prefix must be /16-/32, IPv6 prefix must be /32-/128); invalid lines are skipped and returned in errors.",
		obj(map[string]any{
			"company": str("company name (created if it does not exist, reused if it does; name is unique)"),
			"scope":   str("asset scope, one entry per line: domain / URL / IP / CIDR / ICP filing / company keyword"),
			"reason":  str("attribution basis (evidence/source); always fill this in"),
			"logo":    str("company logo URL (optional; applies only when creating a new company)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope not enabled: CompanyStore not initialized"), nil
			}
			return toolinput.Decode(in, func(a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}) (actool.Result, error) {
				if strings.TrimSpace(a.Company) == "" {
					return actool.Errorf("company must not be empty"), nil
				}
				companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
				if err != nil {
					return actool.Errorf("failed to create/get company: " + err.Error()), nil
				}
				lines := splitLines(a.Scope)
				added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
				out := map[string]any{
					"company_id": companyID,
					"added":      added,
					"skipped":    skipped,
					"invalid":    invalid,
				}
				if len(errMsgs) > 0 {
					out["errors"] = errMsgs
				}
				return jsonResult(out)

			})
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"Add a test scope to [this task] — this is the task's authorization boundary and the denominator for asset-test coverage.\n"+
			"Supported kinds: company (all assets of a company) / root_domain (the whole root domain, including all subdomains) / subdomain (a single exact subdomain) / ip / cidr / icp / keyword.\n"+
			"Note: hosts a worker encounters one by one are [automatically] added to the scope (exact subdomains); this tool is for [deliberate widening] — pull in a whole root domain / whole company, or add a specific subdomain/IP.\n"+
			"value: for company pass the company name or id (the company must already exist); for root_domain/subdomain pass a domain; for ip/cidr pass an IP or network; for icp/keyword pass a filing number or company keyword.\n"+
			"Always provide reason to state the basis (auditable). Use the entries array for multiple.",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "batch: [{kind, value}]. kind∈company/root_domain/subdomain/ip/cidr/icp/keyword.", "items": map[string]any{"type": "object"}},
			"kind":    str("[single] company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value":   str("[single] company name or id / domain / IP / CIDR / ICP / keyword"),
			"reason":  str("basis for adding (for auditing); always fill this in"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope not enabled: AssetStore not initialized"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope requires a task context (no task currently)"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // single mode
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"Query the assets within the scope of [this task and its direct related tasks] that are not yet covered by a fact anchor (related scope is read-only, provided for you to judge whether to add testing — it does not replace your decision).\n"+
			"Optionally filter by asset type: root_domain/subdomain/service/app/endpoint/ip.\n"+
			"Pagination: page starts at 1, page_size defaults to 10. Returns {assets:[{id,type,label}], total, page, page_size}. Available only in a task context.",
		obj(map[string]any{
			"type":      str("asset-type filter (optional): root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("page number, starting at 1 (default 1)"),
			"page_size": intp("items per page (default 10)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets not enabled: AssetStore not initialized"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets requires a task context"), nil
			}
			return toolinput.Partial(in, func(a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}) (actool.Result, error) {
				if a.Page <= 0 {
					a.Page = 1
				}
				if a.PageSize <= 0 {
					a.PageSize = 10
				}
				offset := (a.Page - 1) * a.PageSize
				assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(map[string]any{
					"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
				})

			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"Query the asset store: search with a DSL expression, or fetch directly by id/ids; pagination supported. Returns only assets within the test scope of [this task and its direct related tasks].\n"+
			"DSL: field=value fuzzy (ILIKE) | field==value exact | field!=value exclude | numeric fields support > >= < <= | bare word = full-text fuzzy; combine with AND/OR (AND binds tighter), grouping with parentheses. Asset type uses the separate type parameter, not the DSL.\n"+
			"When id/ids are not passed, dsl must be non-empty (an unconditional full query is not allowed).\n"+
			"Available fields: domain (root/sub/service domain), root_domain, ip, url, page_title, icp, service_name, app_name, method (e.g. GET/POST), service_type (http|other), record_type (e.g. A/CNAME), technology (array, = fuzzy == exact), port/status_code/company_id (integers).\n"+
			"Examples: status_code>=400 AND technology=shiro ; (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL query expression (syntax/fields in the tool description). Must be non-empty when id/ids are not passed.`),
			"type":   str("asset-type filter: root_domain|ip|subdomain|app|service|endpoint (a separate field, can be combined with dsl; type alone is not enough to query — dsl is still required)"),
			"id":     intp("fetch directly by a single asset id (optional, mutually exclusive with dsl/type)"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "fetch directly by multiple asset ids (optional, mutually exclusive with dsl/type)"},
			"limit":  intp("result cap, default 10 (optional)"),
			"offset": intp("pagination offset, default 0 (optional)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets not enabled: AssetStore not initialized"), nil
			}
			return toolinput.Partial(in, func(a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}) (actool.Result, error) {
				if a.Limit <= 0 {
					a.Limit = 10
				}

				var assets []*db.Asset
				var err error
				switch {
				case a.ID > 0:
					assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
				case len(a.IDs) > 0:
					assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
				case a.DSL != "":
					assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
				default:
					return actool.Errorf("dsl must not be empty when id/ids are not passed: an unconditional query of all assets is not allowed; provide a query condition"), nil
				}
				if err != nil {
					return actool.Errorf("DSL error: " + err.Error()), nil
				}
				return jsonResult(map[string]any{
					"count":  len(assets),
					"assets": assets,
				})

			})
		},
	)
}

// listCompanies lets an agent enumerate companies with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"List the [companies] in the asset store with their asset scope and the count of assets already attributed. Used to see which companies exist and to get a company_id "+
			"(used when insert_assets associates an app, and when list_assets filters by company_id). "+
			"Optional search does a fuzzy filter by company name (case-insensitive); leave empty to return all.",
		obj(map[string]any{
			"search": str("fuzzy filter by company name (optional, case-insensitive); leave empty to return all"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies not enabled: CompanyStore not initialized"), nil
			}
			return toolinput.Partial(in, func(a struct {
				Search string `json:"search"`
			}) (actool.Result, error) {
				cos, err := t.cs.ListCompanies()
				if err != nil {
					return actool.Errorf("failed to query companies: " + err.Error()), nil
				}
				q := strings.ToLower(strings.TrimSpace(a.Search))
				type companyOut struct {
					ID         int64    `json:"id"`
					Name       string   `json:"name"`
					AssetCount int      `json:"asset_count"`
					Scope      []string `json:"scope"`
				}
				out := make([]companyOut, 0, len(cos))
				for _, c := range cos {
					if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
						continue
					}
					scope := make([]string, 0, len(c.Scope))
					for _, r := range c.Scope {
						scope = append(scope, r.Raw)
					}
					out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
				}
				return jsonResult(map[string]any{"count": len(out), "companies": out})

			})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings kept: before reporting a vulnerability, first check this task's already-confirmed vulnerabilities to avoid reporting the same one twice.
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// asset management (handlers guard nil store internally).
		// add_company_scope is not given to the worker: defining a company's asset scope is the job of the planner/main/Auto; the worker only performs exploration.
		t.insertAssets(), t.listAssets(),
		// Cross-work lookback: a worker can also reuse other works' observations to avoid duplicated effort.
		// search_all_worker_traces: no need to know the intent_id first — globally fetch matching steps by keyword;
		// get_worker_trace: after locking onto one work, list its steps / search in place / fetch full content.
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail: once the worker has an intent_id/node id it can fetch that node's full detail (paired with the lookback above).
		t.nodeDetail(),
		// The following tools are still [not given] to the worker, reserved for planner/main (reading context and cross-work review are planning jobs;
		// the worker only executes and writes back a single intent): list_facts / list_companies / list_worker_traces.
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work: a human can inject a real-time course-correction instruction into a running intent (work) (without interrupting it or losing progress).
		t.steerWorkTool(),
		// set_goals: a human can add a new final goal to this task at runtime (the planner re-judges achievement against it).
		t.setGoals(),
		// set_constraints: a human can add/change operational constraints (allow/deny) on this task at runtime, bounding the planner/worker exploration edge.
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets: query this task's untested assets on demand (type + pagination) and decide for yourself whether to add testing.
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
