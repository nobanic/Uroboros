package db

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Finding asset tree — the "by asset" view of the global findings list.
//
// The hierarchy shares the same source as BuildCoverageGraph (company → root_domain/ip/app → subdomain →
// service → endpoint), but that one is a force-directed graph of "assets within a task's scope", and this
// one is a tree of "all assets in the DB that have findings": it collects only assets with findings and
// their ancestor chain, with each node carrying subtree aggregate counts.
// The parent/child priority rules of the two must stay consistent — when you change one, cross-check task_scope.go and change the other.
// ---------------------------------------------------------------------------

// FindingUnassignedAsset is the node key for "no associated asset", and also the list API's filter
// sentinel: it matches findings whose asset_ids is empty, or whose referenced assets have been deleted.
const FindingUnassignedAsset = "__none__"

// findingAssetTreeMaxNodes is the node cap returned to the frontend. When exceeded, whole layers are
// dropped bottom-up (endpoint first, then service): their counts are already rolled up into the parent, so dropping nodes doesn't drop numbers.
const findingAssetTreeMaxNodes = 3000

// FindingAssetNode is one node of the asset tree. Key is isomorphic with the coverage graph: an asset row
// is "a:<id>", a company is "c:<id>", a root domain with no asset row is a synthesized "r:<domain>", and the unassigned bucket is "__none__".
type FindingAssetNode struct {
	Key       string `json:"key"`
	Parent    string `json:"parent,omitempty"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint|none
	Label     string `json:"label"`
	AssetID   int64  `json:"asset_id,omitempty"`
	CompanyID int64  `json:"company_id,omitempty"`
	// Self is the number of findings attached directly to this asset; Total includes all descendants and is
	// deduplicated by finding (when one finding is attached to several assets, it's counted once on the common ancestor).
	Self        int       `json:"self"`
	Total       int       `json:"total"`
	Critical    int       `json:"critical"`
	High        int       `json:"high"`
	Medium      int       `json:"medium"`
	Low         int       `json:"low"`
	LastFoundAt time.Time `json:"last_found_at"`
}

// FindingAssetTree is a one-shot snapshot of the whole tree. Nodes is pre-sorted: under the same parent,
// by finding count descending then label ascending, with "no associated asset" always last.
type FindingAssetTree struct {
	Nodes        []FindingAssetNode `json:"nodes"`
	FindingTotal int                `json:"finding_total"`
	// Truncated=true means the layers in DroppedKinds were dropped to control size.
	Truncated    bool     `json:"truncated"`
	DroppedKinds []string `json:"dropped_kinds,omitempty"`
}

// assetRow is the subset of asset fields needed for building the tree.
type assetRow struct {
	id          int64
	kind        string
	companyID   int64
	domain      string
	rootDomain  string
	ip          string
	url         string
	port        int
	serviceType string
	appName     string
}

func (a *assetRow) coverageNode() CoverageGraphNode {
	return CoverageGraphNode{
		Kind: a.kind, Domain: a.domain, RootDomain: a.rootDomain, IP: a.ip,
		URL: a.url, Port: a.port, ServiceType: a.serviceType, AppName: a.appName,
	}
}

// label reuses the coverage graph's label rules (URL > domain > ip > app_name > root_domain), but a
// service with no URL (SMB, a non-HTTP port, etc.) needs its port appended: otherwise its label would be
// identical to the host IP/domain row, and the two parent/child rows would look completely duplicated in the tree.
func (a *assetRow) label() string {
	if a.kind == "service" && a.url == "" {
		if host, port := a.hostPort(); host != "" && port > 0 {
			return host + ":" + strconv.Itoa(port)
		}
	}
	n := a.coverageNode()
	n.Key = assetKey(a.id)
	return coverageNodeLabel(&n)
}

// hostPort matches the coverage graph: prefer domain, then the host in the URL, finally ip.
func (a *assetRow) hostPort() (string, int) {
	n := a.coverageNode()
	return hostPortOf(&n)
}

const findingAssetSelectCols = `a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,'')`

func scanAssetRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]*assetRow, error) {
	defer rows.Close()
	var out []*assetRow
	for rows.Next() {
		a := &assetRow{}
		if err := rows.Scan(&a.id, &a.kind, &a.companyID, &a.domain, &a.rootDomain,
			&a.ip, &a.url, &a.port, &a.serviceType, &a.appName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// findingAssetHit is the minimal information about one finding needed during tree building.
type findingAssetHit struct {
	severity string
	ts       time.Time
	assetIDs []int64
}

// BuildFindingAssetTree builds the asset tree under the current filter. AssetScope itself does not
// participate (otherwise the tree would collapse into a single chain around the selected node).
func (d *DB) BuildFindingAssetTree(f FindingFilter) (*FindingAssetTree, error) {
	return d.buildFindingAssetTree(f, findingAssetTreeMaxNodes)
}

// buildFindingAssetTree is the internal implementation with a node cap. maxNodes<=0 means no truncation —
// this mode must be used when resolving AssetScope, otherwise dropped endpoints would leave the subtree id set incomplete.
func (d *DB) buildFindingAssetTree(f FindingFilter, maxNodes int) (*FindingAssetTree, error) {
	return (findingTreeQuery{database: d, filter: f, limit: maxNodes}).run()
}

// countFinding accumulates one finding onto a node (total / severity buckets / most-recent found time).
func countFinding(n *FindingAssetNode, h findingAssetHit) {
	if n == nil {
		return
	}
	n.Total++
	switch h.severity {
	case "critical":
		n.Critical++
	case "high":
		n.High++
	case "medium":
		n.Medium++
	case "low":
		n.Low++
	}
	if h.ts.After(n.LastFoundAt) {
		n.LastFoundAt = h.ts
	}
}

// loadFindingAssetRows reads the matched asset rows and fills in ancestors round by round (a service's
// host domain/IP, a subdomain's root domain). The ancestors themselves may have no findings, but the tree needs them to take shape.
func (d *DB) loadFindingAssetRows(ids map[int64]bool) (map[int64]*assetRow, error) {
	return (findingTreeAssetRead{database: d}).load(ids)
}

// missingHosts are the host identifiers to look up in the DB in one fill round, split by target asset type.
type missingHosts struct {
	services []string // an endpoint's host (to find the service row)
	domains  []string // a service/endpoint's host domain (to find the subdomain row)
	ips      []string // a service/endpoint's host IP (to find the ip row)
	roots    []string // a subdomain's root domain (to find the root_domain row)
}

func (m missingHosts) empty() bool {
	return len(m.services) == 0 && len(m.domains) == 0 && len(m.ips) == 0 && len(m.roots) == 0
}

// missingParents collects the not-yet-loaded hosts: service (for endpoints to attach to), subdomain/IP
// (for services and endpoints to attach to), and root domain (for subdomains to attach to).
func missingParents(byID map[int64]*assetRow) missingHosts {
	haveService := map[string]bool{}
	haveDomain := map[string]bool{}
	haveIP := map[string]bool{}
	haveRoot := map[string]bool{}
	for _, a := range byID {
		switch a.kind {
		case "service":
			if host, _ := a.hostPort(); host != "" {
				haveService[host] = true
			}
		case "subdomain":
			haveDomain[a.domain] = true
		case "ip":
			haveIP[a.ip] = true
		case "root_domain":
			haveRoot[a.domain] = true
		}
	}
	wantService := map[string]bool{}
	wantDomain := map[string]bool{}
	wantIP := map[string]bool{}
	wantRoot := map[string]bool{}
	for _, a := range byID {
		switch a.kind {
		case "service", "endpoint":
			host, _ := a.hostPort()
			// An endpoint first looks for a service on the same host; a service whose port doesn't match is
			// ultimately filtered out with Total=0 and won't pollute the tree.
			if a.kind == "endpoint" && host != "" && !haveService[host] {
				wantService[host] = true
			}
			if host != "" && !haveDomain[host] && !haveIP[host] && !haveRoot[host] {
				if isIPLiteral(host) {
					wantIP[host] = true
				} else {
					wantDomain[host] = true
				}
			}
			if a.ip != "" && !haveIP[a.ip] {
				wantIP[a.ip] = true
			}
		case "subdomain":
			if a.rootDomain != "" && !haveRoot[a.rootDomain] {
				wantRoot[a.rootDomain] = true
			}
		}
	}
	return missingHosts{
		services: keysOf(wantService),
		domains:  keysOf(wantDomain),
		ips:      keysOf(wantIP),
		roots:    keysOf(wantRoot),
	}
}

func keysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// isIPLiteral roughly judges whether a host is an IP literal (used to decide whether to look up the host in the ip or subdomain table).
func isIPLiteral(host string) bool {
	if strings.Contains(host, ":") {
		return true // IPv6
	}
	if host == "" {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return strings.Count(host, ".") == 3
}

// loadAssetsByHost fills in asset rows in bulk by host identifier, returning the number of rows added this round.
func (d *DB) loadAssetsByHost(want missingHosts, byID map[int64]*assetRow) (int, error) {
	return (findingTreeHostRead{database: d, want: want, rows: byID}).load()
}

// assembleFindingAssetNodes turns asset rows into nodes and wires up parent/child relations. When a parent
// is missing (the DB has no such root-domain asset at all) it synthesizes an "r:<domain>" placeholder node, consistent with the coverage graph's handling.
func (d *DB) assembleFindingAssetNodes(byID map[int64]*assetRow) (map[string]*FindingAssetNode, map[string]string) {
	return (&findingTreeHierarchy{database: d, rows: byID}).build()
}

// attachCompanyNodes adds a company parent node to top-level assets (root domain / IP / app) — a company
// layer appears only when an asset actually belongs to a company; an unattributed asset remains the top level itself.
func (d *DB) attachCompanyNodes(nodes map[string]*FindingAssetNode, parentOf map[string]string) error {
	return (&findingTreeHierarchy{database: d, nodes: nodes, parents: parentOf}).companies()
}

// sortFindingAssetNodes sorts: more findings first, ties by label; "no associated asset" always last.
// The frontend attaches child nodes in array order, so only the relative order under the same parent needs to be correct.
func sortFindingAssetNodes(nodes []FindingAssetNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if (a.Kind == "none") != (b.Kind == "none") {
			return b.Kind == "none"
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Label < b.Label
	})
}

// truncateFindingAssetTree drops whole layers when there are too many nodes (endpoint first, then service).
// Counts are already rolled up into the parent; only the expandable detail layers are dropped.
func truncateFindingAssetTree(tree *FindingAssetTree, maxNodes int) {
	if maxNodes <= 0 || len(tree.Nodes) <= maxNodes {
		return
	}
	for _, kind := range []string{"endpoint", "service"} {
		kept := tree.Nodes[:0]
		for _, n := range tree.Nodes {
			if n.Kind == kind {
				continue
			}
			kept = append(kept, n)
		}
		tree.Nodes = kept
		tree.Truncated = true
		tree.DroppedKinds = append(tree.DroppedKinds, kind)
		if len(tree.Nodes) <= maxNodes {
			return
		}
	}
}

// applyAssetScope resolves AssetScope (a node key) into a set of asset ids usable in SQL. Selecting a node
// is equivalent to selecting its whole subtree, so the tree is built first and then its descendants are collected.
func (d *DB) applyAssetScope(f FindingFilter) (FindingFilter, error) {
	return (findingTreeScope{database: d, filter: f}).resolve()
}

// assetIDContainments turns asset ids into the right-hand operand set for a jsonb containment test, for use
// with idx_findings_asset_ids (GIN jsonb_path_ops).
func assetIDContainments(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "["+strconv.FormatInt(id, 10)+"]")
	}
	return out
}
