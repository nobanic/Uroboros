package db

import "sort"

// One node projection keeps per-exploration lists, global cursor pages and
// guarded single-node reads distinct; their historical policies are retained.
type sourceNodeRead struct {
	store      *ExplorationStore
	kind       string
	id, before int64
	limit      int
	query      string
}

func (projection sourceNodeRead) list() ([]*Node, error) {
	out, err := projection.store.ListByKind(projection.kind, projection.limit)
	if err != nil {
		return nil, err
	}
	sources, err := projection.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		nodes, err := source.Store.ListByKind(projection.kind, projection.limit)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if projection.kind == KindIntent && !inheritedIntentTerminal(node.State) {
				continue
			}
			out = append(out, markInheritedNode(node, source.Task.TaskID))
		}
	}
	return out, nil
}
func (projection sourceNodeRead) page() (nodes []*Node, hasMore bool, total int, err error) {
	if projection.limit <= 0 {
		projection.limit = 20
	}
	own, err := projection.store.listByKindPageFiltered(projection.kind, projection.before, projection.limit, projection.query)
	if err != nil {
		return nil, false, 0, err
	}
	merged := own
	total, err = projection.store.countByKindFiltered(projection.kind, projection.query)
	if err != nil {
		return nil, false, 0, err
	}

	sources, err := projection.store.DirectSourceStores()
	if err != nil {
		return nil, false, 0, err
	}
	for _, source := range sources {
		page, err := source.Store.listByKindPageFiltered(projection.kind, projection.before, projection.limit, projection.query)
		if err != nil {
			return nil, false, 0, err
		}
		for _, n := range page {
			merged = append(merged, markInheritedNode(n, source.Task.TaskID))
		}
		cnt, err := source.Store.countByKindFiltered(projection.kind, projection.query)
		if err != nil {
			return nil, false, 0, err
		}
		total += cnt
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].ID > merged[j].ID })
	hasMore = len(merged) > projection.limit
	if hasMore {
		merged = merged[:projection.limit]
	}
	return merged, hasMore, total, nil
}
func (projection sourceNodeRead) one() (*Node, error) {
	node, err := projection.store.GetNode(projection.id)
	if err != nil || node != nil {
		return node, err
	}
	sources, err := projection.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		node, err = source.Store.GetNode(projection.id)
		if err != nil {
			return nil, err
		}
		if node != nil {
			if node.Kind == KindIntent && !inheritedIntentTerminal(node.State) {
				continue
			}
			return markInheritedNode(node, source.Task.TaskID), nil
		}
	}
	return nil, nil
}
