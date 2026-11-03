package db

import (
	"fmt"
	"strings"
)

// Activity readers share query construction, while projections remain explicit:
// session history, inherited history, trace summaries and lazy tool detail carry
// different information. Source eligibility is always checked inside the SELECT.
type activityProjection uint8

const (
	activitySession activityProjection = iota
	activitySummary
	activityDetail
)

type activityRead struct {
	view       activityProjection
	session    ActivitySessionFilter
	inherited  bool
	workerOnly bool
	descending bool
	since      *int64
	before     *int64
	search     *string
	exclude    int64
	ids        []int64
	limit      int
}

func activityWindow(requested, fallback int) int {
	if requested > 0 {
		return requested
	}
	return fallback
}

func activitySelection(view activityProjection, inherited bool) string {
	switch view {
	case activityDetail:
		return `a.id, a.node_id, COALESCE(a.kind,''), COALESCE(a.tool,''),
a.is_error, COALESCE(a.detail,'')`
	case activitySummary:
		return `a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''),
COALESCE(a.tool,''), a.is_error, COALESCE(a.summary,'')`
	default:
		prefix := `a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''),
COALESCE(a.tool,''), COALESCE(a.tool_use_id,''), a.is_error, COALESCE(a.summary,'')`
		if !inherited {
			prefix += ", a.metadata"
		}
		prefix += `, a.created_at, a.input_tokens, a.output_tokens,
a.cache_read_tokens, a.cache_write_tokens`
		if !inherited {
			prefix += ", a.main_seg"
		}
		return prefix
	}
}

func activityDestinations(row *Activity, view activityProjection, inherited bool) []any {
	switch view {
	case activityDetail:
		return []any{&row.ID, &row.NodeID, &row.Kind, &row.Tool, &row.IsError, &row.Detail}
	case activitySummary:
		return []any{&row.ID, &row.NodeID, &row.Worker, &row.Kind, &row.Tool, &row.IsError, &row.Summary}
	default:
		targets := []any{&row.ID, &row.NodeID, &row.Worker, &row.Kind, &row.Tool, &row.ToolUseID, &row.IsError, &row.Summary}
		if !inherited {
			targets = append(targets, &row.Metadata)
		}
		targets = append(targets, &row.CreatedAt, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens)
		if !inherited {
			targets = append(targets, &row.MainSeg)
		}
		return targets
	}
}

func (s *ExplorationStore) readActivity(plan activityRead) ([]Activity, error) {
	arguments := []any{s.expID}
	bind := func(value any) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	clauses := []string{"a.exploration_id=$1"}
	source := "activity a"
	if plan.inherited {
		source += " JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id"
		clauses = append(clauses, "n.kind='intent'", "n.state IN ('done','blocked','exhausted','stopped')")
	}
	if plan.inherited || plan.view == activitySummary {
		clauses = append(clauses, "a.kind NOT IN ('thinking','usage')")
	} else if plan.view == activityDetail {
		// Local legacy detail allows usage, while inherited detail never does.
		clauses = append(clauses, "a.kind<>'thinking'")
	}
	filter := plan.session
	switch {
	case filter.NodeID != nil:
		clauses = append(clauses, "a.node_id="+bind(*filter.NodeID))
	case filter.Main:
		clauses = append(clauses, "a.worker='mainagent'")
		if filter.MainSeg == nil || *filter.MainSeg == 0 {
			clauses = append(clauses, "COALESCE(a.main_seg,0)=0")
		} else {
			clauses = append(clauses, "a.main_seg="+bind(*filter.MainSeg))
		}
	case filter.Worker != "":
		clauses = append(clauses, "a.worker="+bind(filter.Worker))
	}
	if plan.workerOnly {
		clauses = append(clauses, "a.node_id IS NOT NULL")
	}
	if plan.exclude > 0 {
		clauses = append(clauses, "a.node_id<>"+bind(plan.exclude))
	}
	if plan.since != nil {
		clauses = append(clauses, "a.id>"+bind(*plan.since))
	}
	if plan.before != nil {
		cursor := bind(*plan.before)
		clauses = append(clauses, "("+cursor+"<=0 OR a.id<"+cursor+")")
	}
	if plan.search != nil {
		pattern := bind("%" + *plan.search + "%")
		clauses = append(clauses, "(a.summary ILIKE "+pattern+" OR a.detail ILIKE "+pattern+")")
	}
	if len(plan.ids) != 0 {
		identities := make([]string, len(plan.ids))
		for index, identity := range plan.ids {
			identities[index] = bind(identity)
		}
		clauses = append(clauses, "a.id IN ("+strings.Join(identities, ",")+")")
	}
	order := "a.id"
	if plan.descending {
		order += " DESC"
	}
	statement := "SELECT " + activitySelection(plan.view, plan.inherited) + " FROM " + source +
		" WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + order
	if plan.limit != 0 {
		statement += " LIMIT " + bind(plan.limit)
	}
	rows, err := s.db.Query(statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Activity{}
	return collectRowValues(rows, items, func() (Activity, error) {
		var item Activity
		err := rows.Scan(activityDestinations(&item, plan.view, plan.inherited)...)
		return item, err
	})
}

func (s *ExplorationStore) activityIncrement(plan activityRead, since int64) ([]Activity, int64, error) {
	plan.since = &since
	items, err := s.readActivity(plan)
	cursor := since
	for _, item := range items {
		if item.ID > cursor {
			cursor = item.ID
		}
	}
	return items, cursor, err
}

func (s *ExplorationStore) activityHistory(plan activityRead, before int64, limit int) ([]Activity, bool, error) {
	plan.before, plan.descending, plan.limit = &before, true, limit+1
	items, err := s.readActivity(plan)
	if err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	for first, last := 0, len(items)-1; first < last; first, last = first+1, last-1 {
		items[first], items[last] = items[last], items[first]
	}
	return items, more, nil
}

// ActivityList returns steps after sinceID (exclusive), optionally filtered by node.
// Returns items and the new cursor (max id seen).
func (s *ExplorationStore) ActivityList(nodeID *int64, sinceID int64, limit int) ([]Activity, int64, error) {
	return s.activityIncrement(activityRead{session: ActivitySessionFilter{NodeID: nodeID}, limit: activityWindow(limit, 300)}, sinceID)
}

// ActivityListForTerminalIntent is the inherited, incremental-read boundary.
// The intent state check and activity read happen in one statement so a source
// intent reopened between API-level checks cannot expose its new in-flight trace.
// Model reasoning and accounting rows are never inherited.
func (s *ExplorationStore) ActivityListForTerminalIntent(nodeID, sinceID int64, limit int) ([]Activity, int64, error) {
	return s.activityIncrement(activityRead{session: ActivitySessionFilter{NodeID: &nodeID}, inherited: true, limit: activityWindow(limit, 300)}, sinceID)
}

// ActivityPage returns one page for reverse (newest-first) pagination of a session:
// up to `limit` steps ending before id `before` (exclusive; before<=0 = the latest
// page), returned in ASCENDING id order for direct display. hasMore reports whether
// still-older steps exist before the returned window (so the client can stop loading
// on scroll-up). Summary-only columns — detail is lazy-loaded via ActivityDetail.
func (s *ExplorationStore) ActivityPage(filter ActivitySessionFilter, before int64, limit int) ([]Activity, bool, error) {
	return s.activityHistory(activityRead{session: filter}, before, activityWindow(limit, 200))
}

// ActivityPageForTerminalIntent is the inherited session-history boundary. It
// combines ownership, terminal-state and row-kind checks in one query, closing
// the race where a previously completed source intent is reopened before paging.
func (s *ExplorationStore) ActivityPageForTerminalIntent(nodeID, before int64, limit int) ([]Activity, bool, error) {
	return s.activityHistory(activityRead{session: ActivitySessionFilter{NodeID: &nodeID}, inherited: true}, before, activityWindow(limit, 200))
}

// ActivityTrace lists one work's steps (by intent/node id) for the trace tools:
// summaries only (detail is lazy — fetch via ActivityByIDs). thinking/usage rows
// are dropped so the caller sees the work's actions + observations, not the
// model's internal reasoning or token-accounting noise.
func (s *ExplorationStore) ActivityTrace(nodeID int64, limit int) ([]Activity, error) {
	return s.readActivity(activityRead{view: activitySummary, session: ActivitySessionFilter{NodeID: &nodeID}, limit: activityWindow(limit, 500)})
}

// ActivityTraceForTerminalIntent returns one inherited work trace only while its
// source intent is still terminal at query time.
func (s *ExplorationStore) ActivityTraceForTerminalIntent(nodeID int64, limit int) ([]Activity, error) {
	return s.readActivity(activityRead{view: activitySummary, session: ActivitySessionFilter{NodeID: &nodeID}, inherited: true, limit: activityWindow(limit, 500)})
}

// ActivityTraceSearch finds steps whose summary OR detail matches q
// (case-insensitive), returning summaries only. nodeID != nil scopes to one work;
// nil searches across ALL works (node_id IS NOT NULL — planner/main steps have no
// node_id, so this cleanly means "worker traces"). thinking/usage rows dropped.
func (s *ExplorationStore) ActivityTraceSearch(nodeID *int64, query string, limit int) ([]Activity, error) {
	return s.readActivity(activityRead{view: activitySummary, session: ActivitySessionFilter{NodeID: nodeID}, workerOnly: nodeID == nil, search: &query, limit: activityWindow(limit, 100)})
}

// ActivityTraceSearchForTerminalIntent is the scoped inherited search variant;
// terminal eligibility is evaluated by the same statement that reads the rows.
func (s *ExplorationStore) ActivityTraceSearchForTerminalIntent(nodeID int64, query string, limit int) ([]Activity, error) {
	return s.readActivity(activityRead{view: activitySummary, session: ActivitySessionFilter{NodeID: &nodeID}, inherited: true, search: &query, limit: activityWindow(limit, 100)})
}

// ActivityTraceSearchTerminalIntents searches inherited worker history without
// exposing planner/main rows or work that is still open/running in the source.
func (s *ExplorationStore) ActivityTraceSearchTerminalIntents(query string, limit int) ([]Activity, error) {
	return s.readActivity(activityRead{view: activitySummary, inherited: true, search: &query, limit: activityWindow(limit, 100)})
}

// ActivityTraceSearchExcluding keyword-searches every node's steps in this
// exploration EXCEPT one node's own (excludeNodeID) — so a worker's
// search_all_worker_traces doesn't return its own in-progress trace (which is
// already in its context). excludeNodeID<=0 → no exclusion (searches all nodes).
func (s *ExplorationStore) ActivityTraceSearchExcluding(excludeNodeID int64, query string, limit int) ([]Activity, error) {
	return s.readActivity(activityRead{view: activitySummary, workerOnly: true, exclude: excludeNodeID, search: &query, limit: activityWindow(limit, 100)})
}

// ActivityByIDs loads full detail for specific step ids (the trace drill-down),
// scoped to this exploration. thinking rows are skipped (never returned, even if
// their id is asked for). Order is ascending id, not the input order.
func (s *ExplorationStore) ActivityByIDs(ids []int64) ([]Activity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.readActivity(activityRead{view: activityDetail, ids: ids})
}

// ActivityByIDsForTerminalIntents is the inherited-detail read boundary. It
// deliberately excludes node-less planner/main rows, non-intent activities, live
// source work, and thinking. Local callers that need legacy behavior use
// ActivityByIDs instead.
func (s *ExplorationStore) ActivityByIDsForTerminalIntents(ids []int64) ([]Activity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.readActivity(activityRead{view: activityDetail, inherited: true, ids: ids})
}
