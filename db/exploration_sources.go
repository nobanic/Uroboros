package db

// DirectSourceStore binds one directly related task to its exploration store.
// It is intentionally a read-side helper: callers keep using the receiver store
// for every graph mutation, frontier lookup, and intent claim.
type DirectSourceStore struct {
	Task  TaskSource
	Store *ExplorationStore
}

// DirectSourceStores resolves the live, direct task relations for this
// exploration. It deliberately queries on every call: relations disappear when
// a source task is deleted, and inherited context must not retain stale rows.
// Sources of a source are never expanded.
func (s *ExplorationStore) DirectSourceStores() ([]DirectSourceStore, error) {
	return (sourceRelations{store: s}).sources()
}

// TaskID returns the live task bound to this exploration. Explorations created
// directly in tests or maintenance code have no task and return zero.
func (s *ExplorationStore) TaskID() (int64, error) { return (sourceRelations{store: s}).task() }

func markInheritedNode(n *Node, taskID int64) *Node {
	if n == nil {
		return nil
	}
	n.SourceTaskID = taskID
	n.Inherited = true
	return n
}

func markInheritedActivities(in []Activity, taskID int64) []Activity {
	for i := range in {
		in[i].SourceTaskID = taskID
		in[i].Inherited = true
	}
	return in
}

func inheritedIntentTerminal(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// ListByKindWithSources returns local nodes followed by nodes from each direct
// source in relation order. The limit remains per exploration, matching the
// existing ListByKind contract while ensuring one large task cannot hide all
// inherited context from another source.
func (s *ExplorationStore) ListByKindWithSources(kind string, limit int) ([]*Node, error) {
	return (sourceNodeRead{store: s, kind: kind, limit: limit}).list()
}

// ListByKindPageWithSources is the paginated, keyword-filterable sibling of
// ListByKindWithSources: it returns one newest-first page (id < before, before<=0
// = newest) spanning this exploration and its direct sources, plus hasMore and the
// filtered total across all of them. Node ids are globally unique, so merging each
// store's own page and re-sorting by id DESC yields the true global page; fetching
// limit+1 per store guarantees the merged top-`limit` is complete.
func (s *ExplorationStore) ListByKindPageWithSources(kind string, before int64, limit int, q string) (nodes []*Node, hasMore bool, total int, err error) {
	return (sourceNodeRead{store: s, kind: kind, before: before, limit: limit, query: q}).page()
}

// GetNodeWithSources reads a node only when it belongs to this exploration or
// one of its direct sources. Inherited nodes are tagged so tool callers can keep
// them read-only and show their provenance.
func (s *ExplorationStore) GetNodeWithSources(id int64) (*Node, error) {
	return (sourceNodeRead{store: s, id: id}).one()
}

// FindingIntentsWithSources combines finding lineage for the current
// exploration and each direct source. Node ids are globally unique.
func (s *ExplorationStore) FindingIntentsWithSources() (map[int64]int64, error) {
	return (sourceAnchorRead{store: s}).findings()
}

// ActivityTraceWithSources returns a work trace when its intent belongs to the
// current exploration or a direct source. It never searches indirect sources.
func (s *ExplorationStore) ActivityTraceWithSources(nodeID int64, limit int) ([]Activity, error) {
	return (sourceTraceRead{store: s, nodeID: nodeID, limit: limit}).run()
}

// ActivityListWithSources is the source-aware equivalent used by
// get_worker_output. Node ids are global, so the first owning exploration is
// unambiguous even when the work has not emitted any activity yet.
func (s *ExplorationStore) ActivityListWithSources(nodeID, sinceID int64, limit int) ([]Activity, int64, error) {
	return (sourceActivityRead{store: s, nodeID: nodeID, sinceID: sinceID, limit: limit}).cursor()
}

// ActivityDetailWithSources keeps the legacy local-task lookup (including local
// thinking rows), while inherited details are restricted to terminal worker
// intents. Source planner/main rows have no node id and must never become part of
// inherited context.
func (s *ExplorationStore) ActivityDetailWithSources(id int64) (string, error) {
	return (sourceActivityRead{store: s, id: id}).detail()
}

// ActivityTraceSearchWithSources performs the scoped worker-trace search used
// by get_worker_trace. A node id resolves to at most one exploration because
// exploration node ids are global.
func (s *ExplorationStore) ActivityTraceSearchWithSources(nodeID int64, q string, limit int) ([]Activity, error) {
	return (sourceTraceRead{store: s, nodeID: nodeID, limit: limit, query: q, search: true}).run()
}

// ActivityTraceSearchAllWithSources searches local worker traces plus every
// direct source. The local owner is excluded only from the current exploration;
// inherited traces are immutable historical context.
func (s *ExplorationStore) ActivityTraceSearchAllWithSources(excludeNodeID int64, q string, limit int) ([]Activity, error) {
	return (sourceActivityRead{store: s, excludeNodeID: excludeNodeID, query: q, limit: limit}).searchAll()
}

// ActivityByIDsWithSources loads local step details with the legacy behavior. For
// direct sources it only returns rows attached to terminal intents, preventing an
// arbitrary global activity id from exposing source planner/main transcripts.
func (s *ExplorationStore) ActivityByIDsWithSources(ids []int64) ([]Activity, error) {
	return (sourceActivityRead{store: s, ids: ids}).details()
}

// AssetRefsWithSources returns anchored nodes from this exploration and each
// direct source. Inherited entries retain their owning task id so API/UI callers
// can present them as immutable context.
func (s *ExplorationStore) AssetRefsWithSources(assetID int64) ([]AssetRef, error) {
	return (sourceAnchorRead{store: s, assetID: assetID}).refs()
}
