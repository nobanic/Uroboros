package db

import "strconv"

// Hierarchy has request-local typed indices. Indexing, synthetic roots and
// parent attachment stay ordered; company attachment reuses the same node maps.
type findingTreeHierarchy struct {
	database                             *DB
	rows                                 map[int64]*assetRow
	nodes                                map[string]*FindingAssetNode
	parents                              map[string]string
	roots, domains, ips, services, ports map[string]string
}

func (layout *findingTreeHierarchy) build() (map[string]*FindingAssetNode, map[string]string) {
	layout.nodes = map[string]*FindingAssetNode{}
	layout.parents = map[string]string{}
	layout.roots = map[string]string{}
	layout.domains = map[string]string{}
	layout.ips = map[string]string{}
	layout.services = map[string]string{}
	layout.ports = map[string]string{}
	layout.indexRows()
	layout.addSyntheticRoots()
	layout.attachParents()
	return layout.nodes, layout.parents
}
func (layout *findingTreeHierarchy) indexRows() {
	for _, a := range layout.rows {
		key := assetKey(a.id)
		layout.nodes[key] = &FindingAssetNode{
			Key: key, Kind: a.kind, Label: a.label(),
			AssetID: a.id, CompanyID: a.companyID,
		}
		switch a.kind {
		case "root_domain":
			if a.domain != "" {
				layout.roots[a.domain] = key
			}
		case "subdomain":
			if a.domain != "" {
				layout.domains[a.domain] = key
			}
		case "ip":
			if a.ip != "" {
				layout.ips[a.ip] = key
			}
		case "service":
			if host, port := a.hostPort(); host != "" {
				layout.services[host] = key
				layout.ports[host+"|"+strconv.Itoa(port)] = key
			}
		}
	}
}
func (layout *findingTreeHierarchy) addSyntheticRoots() {
	for _, a := range layout.rows {
		if a.kind != "subdomain" || a.rootDomain == "" {
			continue
		}
		if _, ok := layout.roots[a.rootDomain]; ok {
			continue
		}
		key := "r:" + a.rootDomain
		layout.nodes[key] = &FindingAssetNode{Key: key, Kind: "root_domain", Label: a.rootDomain}
		layout.roots[a.rootDomain] = key
	}
}
func (layout *findingTreeHierarchy) attachParents() {
	firstOf := func(keys ...string) string {
		for _, k := range keys {
			if k != "" {
				if _, ok := layout.nodes[k]; ok {
					return k
				}
			}
		}
		return ""
	}
	for _, a := range layout.rows {
		key := assetKey(a.id)
		var parent string
		switch a.kind {
		case "subdomain":
			parent = firstOf(layout.roots[a.rootDomain])
		case "service":
			host, _ := a.hostPort()
			parent = firstOf(layout.domains[a.domain], layout.domains[host],
				layout.ips[a.ip], layout.ips[host], layout.roots[a.rootDomain], layout.roots[host])
		case "endpoint":
			host, port := a.hostPort()
			parent = firstOf(layout.ports[host+"|"+strconv.Itoa(port)], layout.services[host],
				layout.domains[host], layout.domains[a.domain], layout.ips[host], layout.ips[a.ip],
				layout.roots[a.rootDomain], layout.roots[host])
		}
		if parent != "" && parent != key {
			layout.parents[key] = parent
			layout.nodes[key].Parent = parent
		}
	}
}
func (layout *findingTreeHierarchy) companies() error {
	want := map[int64]bool{}
	for _, n := range layout.nodes {
		if n.Parent != "" || n.CompanyID <= 0 {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			want[n.CompanyID] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	rows, err := layout.database.Query(`SELECT id, COALESCE(name,'') FROM companies WHERE id = ANY($1::bigint[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, name := range names {
		key := companyKey(id)
		if _, ok := layout.nodes[key]; ok {
			continue
		}
		if name == "" {
			name = "Company #" + strconv.FormatInt(id, 10)
		}
		layout.nodes[key] = &FindingAssetNode{Key: key, Kind: "company", Label: name, CompanyID: id}
	}
	for _, n := range layout.nodes {
		if n.Parent != "" || n.CompanyID <= 0 || n.Kind == "company" {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			key := companyKey(n.CompanyID)
			if _, ok := layout.nodes[key]; !ok {
				continue
			}
			n.Parent = key
			layout.parents[n.Key] = key
		}
	}
	return nil
}
