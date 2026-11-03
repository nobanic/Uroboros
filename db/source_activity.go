package db

import "sort"

// Activity selection preserves local legacy details and source terminal-only
// history. Every call resolves current relations, never an inherited write store.
type sourceActivityRead struct {
	store                              *ExplorationStore
	nodeID, sinceID, id, excludeNodeID int64
	ids                                []int64
	query                              string
	limit                              int
}
type sourceTraceRead struct {
	store  *ExplorationStore
	nodeID int64
	limit  int
	query  string
	search bool
}

func (trace sourceTraceRead) local() ([]Activity, error) {
	if trace.search {
		return trace.store.ActivityTraceSearch(&trace.nodeID, trace.query, trace.limit)
	}
	return trace.store.ActivityTrace(trace.nodeID, trace.limit)
}
func (trace sourceTraceRead) inherited(store *ExplorationStore) ([]Activity, error) {
	if trace.search {
		return store.ActivityTraceSearchForTerminalIntent(trace.nodeID, trace.query, trace.limit)
	}
	return store.ActivityTraceForTerminalIntent(trace.nodeID, trace.limit)
}
func (trace sourceTraceRead) run() ([]Activity, error) {
	acts, err := trace.local()
	if err != nil || len(acts) > 0 {
		return acts, err
	}
	sources, err := trace.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		node, nodeErr := source.Store.GetNode(trace.nodeID)
		if nodeErr != nil {
			return nil, nodeErr
		}
		if node == nil || node.Kind != KindIntent {
			continue
		}
		acts, err = trace.inherited(source.Store)
		if err != nil {
			return nil, err
		}
		if len(acts) > 0 {
			return markInheritedActivities(acts, source.Task.TaskID), nil
		}
	}
	return []Activity{}, nil
}
func (history sourceActivityRead) cursor() ([]Activity, int64, error) {
	node, err := history.store.GetNode(history.nodeID)
	if err != nil {
		return nil, history.sinceID, err
	}
	if node != nil {
		return history.store.ActivityList(&history.nodeID, history.sinceID, history.limit)
	}
	sources, err := history.store.DirectSourceStores()
	if err != nil {
		return nil, history.sinceID, err
	}
	for _, source := range sources {
		node, err = source.Store.GetNode(history.nodeID)
		if err != nil {
			return nil, history.sinceID, err
		}
		if node == nil || node.Kind != KindIntent {
			continue
		}
		acts, cursor, err := source.Store.ActivityListForTerminalIntent(history.nodeID, history.sinceID, history.limit)
		if err != nil {
			return nil, history.sinceID, err
		}
		return markInheritedActivities(acts, source.Task.TaskID), cursor, nil
	}
	return []Activity{}, history.sinceID, nil
}
func (history sourceActivityRead) detail() (string, error) {
	detail, err := history.store.ActivityDetail(history.id)
	if err != nil || detail != "" {
		return detail, err
	}
	acts, err := history.store.ActivityByIDsWithSources([]int64{history.id})
	if err != nil || len(acts) == 0 {
		return "", err
	}
	return acts[0].Detail, nil
}
func (history sourceActivityRead) searchAll() ([]Activity, error) {
	if history.limit <= 0 {
		history.limit = 100
	}
	out, err := history.store.ActivityTraceSearchExcluding(history.excludeNodeID, history.query, history.limit)
	if err != nil {
		return nil, err
	}
	sources, err := history.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		acts, err := source.Store.ActivityTraceSearchTerminalIntents(history.query, history.limit)
		if err != nil {
			return nil, err
		}
		out = append(out, markInheritedActivities(acts, source.Task.TaskID)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > history.limit {
		out = out[:history.limit]
	}
	return out, nil
}
func (history sourceActivityRead) details() ([]Activity, error) {
	out, err := history.store.ActivityByIDs(history.ids)
	if err != nil {
		return nil, err
	}
	sources, err := history.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		acts, err := source.Store.ActivityByIDsForTerminalIntents(history.ids)
		if err != nil {
			return nil, err
		}
		out = append(out, markInheritedActivities(acts, source.Task.TaskID)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
