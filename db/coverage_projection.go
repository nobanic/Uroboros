package db

// coverageGraphRead selects the registry-backed task context. Its deliberately
// task-only identity keeps the legacy exploration argument out of this read.
type coverageGraphRead struct {
	store  *AssetStore
	taskID int64
}

func (read coverageGraphRead) graph() (*CoverageGraphData, error) {
	projection := newCoverageProjection()
	if read.taskID <= 0 {
		return projection.document, nil
	}
	rows, err := read.store.db.Query(`WITH `+contextCoverageCTE+`
SELECT a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,''), COALESCE(a.page_title,''), COALESCE(a.status_code,0),
       (a.id IN (SELECT asset_id FROM tested)) AS tested
FROM assets a JOIN target t ON t.id = a.id
ORDER BY a.id`, read.taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var node CoverageGraphNode
		if err := rows.Scan(&node.AssetID, &node.Kind, &node.CompanyID, &node.Domain, &node.RootDomain, &node.IP, &node.URL, &node.Port, &node.ServiceType, &node.AppName, &node.PageTitle, &node.StatusCode, &node.Tested); err != nil {
			return nil, err
		}
		projection.asset(node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	read.connectRoots(projection)
	read.connectCompanies(projection)
	projection.containment()
	return projection.document, nil
}

func (read coverageGraphRead) connectRoots(projection *coverageProjection) {
	missing := map[string]bool{}
	for _, node := range projection.document.Nodes {
		if node.Kind == "subdomain" && node.RootDomain != "" {
			if _, exists := projection.roots[node.RootDomain]; !exists {
				missing[node.RootDomain] = true
			}
		}
	}
	for root := range missing {
		var id, companyID int64
		err := read.store.db.QueryRow(`SELECT id, COALESCE(company_id,0) FROM assets
WHERE type='root_domain' AND domain=$1 LIMIT 1`, root).Scan(&id, &companyID)
		node := CoverageGraphNode{Key: "r:" + root, Kind: "root_domain", Domain: root, Label: root}
		if err == nil && id > 0 {
			node.Key, node.AssetID, node.CompanyID = assetKey(id), id, companyID
			if companyID > 0 {
				projection.companies[companyID] = true
			}
		}
		projection.put(node)
		projection.roots[root] = node.Key
	}
}

func (read coverageGraphRead) connectCompanies(projection *coverageProjection) {
	for id := range projection.companies {
		key := companyKey(id)
		if _, exists := projection.positions[key]; exists {
			continue
		}
		var name string
		if err := read.store.db.QueryRow(`SELECT name FROM companies WHERE id=$1`, id).Scan(&name); err != nil {
			continue
		}
		projection.put(CoverageGraphNode{Key: key, Kind: "company", CompanyID: id, Label: name, AssetID: 0})
	}
}

type coverageServiceIdentity struct {
	host string
	port int
}

// coverageProjection owns the output and per-read identity/containment indices.
// Positions identify stored nodes without retaining pointers across slice growth.
// Connector maps retain the original unordered traversal and first-insert nodes;
// service indices retain last-row selection in the ascending asset query order.
type coverageProjection struct {
	document                     *CoverageGraphData
	positions                    map[string]int
	roots, subdomains, addresses map[string]string
	services                     map[coverageServiceIdentity]string
	serviceHosts                 map[string]string
	companies                    map[int64]bool
}

func newCoverageProjection() *coverageProjection {
	return &coverageProjection{document: &CoverageGraphData{Nodes: []CoverageGraphNode{}, Edges: []CoverageGraphEdge{}}, positions: map[string]int{}, roots: map[string]string{}, subdomains: map[string]string{}, addresses: map[string]string{}, services: map[coverageServiceIdentity]string{}, serviceHosts: map[string]string{}, companies: map[int64]bool{}}
}

func (projection *coverageProjection) put(node CoverageGraphNode) *CoverageGraphNode {
	if index, exists := projection.positions[node.Key]; exists {
		return &projection.document.Nodes[index]
	}
	projection.positions[node.Key] = len(projection.document.Nodes)
	projection.document.Nodes = append(projection.document.Nodes, node)
	return &projection.document.Nodes[len(projection.document.Nodes)-1]
}

func (projection *coverageProjection) asset(node CoverageGraphNode) {
	node.Key, node.InScope = assetKey(node.AssetID), true
	node.Label = coverageNodeLabel(&node)
	stored := projection.put(node)
	switch node.Kind {
	case "root_domain":
		if node.Domain != "" {
			projection.roots[node.Domain] = stored.Key
		}
	case "subdomain":
		if node.Domain != "" {
			projection.subdomains[node.Domain] = stored.Key
		}
	case "ip":
		if node.IP != "" {
			projection.addresses[node.IP] = stored.Key
		}
	case "service":
		host, port := hostPortOf(stored)
		if host != "" {
			projection.serviceHosts[host] = stored.Key
			projection.services[coverageServiceIdentity{host, port}] = stored.Key
		}
	}
	if node.CompanyID > 0 {
		projection.companies[node.CompanyID] = true
	}
}

func (projection *coverageProjection) first(keys ...string) string {
	for _, key := range keys {
		if key != "" {
			if _, exists := projection.positions[key]; exists {
				return key
			}
		}
	}
	return ""
}

func (projection *coverageProjection) link(child, parent string) {
	if parent == "" || parent == child {
		return
	}
	if _, exists := projection.positions[parent]; !exists {
		return
	}
	projection.document.Edges = append(projection.document.Edges, CoverageGraphEdge{Src: child, Dst: parent})
}

func (projection *coverageProjection) containment() {
	for i := range projection.document.Nodes {
		node := &projection.document.Nodes[i]
		switch node.Kind {
		case "subdomain":
			projection.link(node.Key, projection.roots[node.RootDomain])
		case "root_domain", "app", "ip":
			if node.CompanyID > 0 {
				projection.link(node.Key, companyKey(node.CompanyID))
			}
		case "service":
			projection.link(node.Key, projection.first(projection.subdomains[node.Domain], projection.addresses[node.IP], projection.roots[node.RootDomain]))
		case "endpoint":
			host, port := hostPortOf(node)
			parent := projection.first(projection.services[coverageServiceIdentity{host, port}], projection.serviceHosts[host], projection.subdomains[host], projection.subdomains[node.Domain], projection.addresses[host], projection.addresses[node.IP], projection.roots[node.RootDomain])
			projection.link(node.Key, parent)
		}
	}
}
