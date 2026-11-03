package db

import (
	"encoding/json"
	"strings"
)

// Query owns the copied filter and output cap of one findings-tree read.
type findingTreeQuery struct {
	database *DB
	filter   FindingFilter
	limit    int
}

// Summary owns the mutable projection and per-finding deduplication pass.
type findingTreeSummary struct {
	tree    *FindingAssetTree
	nodes   map[string]*FindingAssetNode
	parents map[string]string
	hits    []findingAssetHit
	limit   int
}
type findingTreeScope struct {
	database *DB
	filter   FindingFilter
}

func (query findingTreeQuery) run() (*FindingAssetTree, error) {
	query.filter.AssetScope = ""
	query.filter.assetIDs, query.filter.assetNone, query.filter.assetMiss = nil, false, false
	where, args := query.filter.where()

	rows, err := query.database.Query(`SELECT COALESCE(f.severity,''), f.created_at,
       COALESCE(f.asset_ids::text,'[]')
FROM findings f LEFT JOIN tasks t ON f.task_id = t.id`+where, args...)
	if err != nil {
		return nil, err
	}
	hits := []findingAssetHit{}
	assetIDs := map[int64]bool{}
	for rows.Next() {
		var h findingAssetHit
		var aidsJSON string
		if err := rows.Scan(&h.severity, &h.ts, &aidsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &h.assetIDs)
		for _, id := range h.assetIDs {
			if id > 0 {
				assetIDs[id] = true
			}
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	tree := &FindingAssetTree{Nodes: []FindingAssetNode{}, FindingTotal: len(hits)}
	byID, err := query.database.loadFindingAssetRows(assetIDs)
	if err != nil {
		return nil, err
	}

	nodes, parentOf := query.database.assembleFindingAssetNodes(byID)
	if err := query.database.attachCompanyNodes(nodes, parentOf); err != nil {
		return nil, err
	}

	// Counting: for each finding, walk up the ancestor chain of each of its assets, collect the deduplicated
	// key set, then +1 each — so a parent is not double-counted because one finding is attached to several of its child assets.
	return (findingTreeSummary{tree: tree, nodes: nodes, parents: parentOf, hits: hits, limit: query.limit}).render()
}
func (summary findingTreeSummary) render() (*FindingAssetTree, error) {
	unassigned := &FindingAssetNode{Key: FindingUnassignedAsset, Kind: "none", Label: "Unassigned assets"}
	touched := map[string]bool{}
	for _, h := range summary.hits {
		clear(touched)
		var direct []*FindingAssetNode
		for _, id := range h.assetIDs {
			node := summary.nodes[assetKey(id)]
			if node == nil {
				continue
			}
			direct = append(direct, node)
			for key := node.Key; key != ""; key = summary.parents[key] {
				touched[key] = true
			}
		}
		if len(direct) == 0 {
			countFinding(unassigned, h)
			unassigned.Self++
			continue
		}
		for _, node := range direct {
			node.Self++
		}
		for key := range touched {
			countFinding(summary.nodes[key], h)
		}
	}

	for _, node := range summary.nodes {
		if node.Total > 0 {
			summary.tree.Nodes = append(summary.tree.Nodes, *node)
		}
	}
	if unassigned.Total > 0 {
		summary.tree.Nodes = append(summary.tree.Nodes, *unassigned)
	}
	sortFindingAssetNodes(summary.tree.Nodes)
	truncateFindingAssetTree(summary.tree, summary.limit)
	return summary.tree, nil
}
func (selection findingTreeScope) resolve() (FindingFilter, error) {
	scope := strings.TrimSpace(selection.filter.AssetScope)
	selection.filter.assetIDs, selection.filter.assetNone, selection.filter.assetMiss = nil, false, false
	if scope == "" {
		return selection.filter, nil
	}
	if scope == FindingUnassignedAsset {
		selection.filter.assetNone = true
		return selection.filter, nil
	}
	// No truncation: dropped endpoints must also take part in id collection, otherwise the list would be missing data.
	tree, err := selection.database.buildFindingAssetTree(selection.filter, 0)
	if err != nil {
		return selection.filter, err
	}
	children := map[string][]FindingAssetNode{}
	byKey := map[string]FindingAssetNode{}
	for _, n := range tree.Nodes {
		byKey[n.Key] = n
		children[n.Parent] = append(children[n.Parent], n)
	}
	if _, ok := byKey[scope]; !ok {
		// The selected node no longer exists under the current filter, so the result should be empty rather than degrade into no filtering.
		selection.filter.assetMiss = true
		return selection.filter, nil
	}
	seen := map[string]bool{scope: true}
	queue := []string{scope}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if id := byKey[key].AssetID; id > 0 {
			selection.filter.assetIDs = append(selection.filter.assetIDs, id)
		}
		for _, child := range children[key] {
			if seen[child.Key] {
				continue
			}
			seen[child.Key] = true
			queue = append(queue, child.Key)
		}
	}
	if len(selection.filter.assetIDs) == 0 {
		selection.filter.assetMiss = true
	}
	return selection.filter, nil
}
