package server

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/nobanic/UroborOS/db"
)

// Inspection queries borrow the existing manager/stores for one HTTP read.
// They never retain a request across calls or acquire execution authority.
type inspectionQuery struct {
	owner            *Server
	values           url.Values
	pathID, sequence string
}

func (s *Server) readInspection(r *http.Request) inspectionQuery {
	return inspectionQuery{owner: s, values: r.URL.Query(), pathID: r.PathValue("id"), sequence: r.PathValue("seq")}
}

// A read outcome carries a document or an explicit HTTP failure. Empty strings,
// typed nil slices and nil documents keep their original JSON meanings.
type inspectionReply struct {
	document any
	status   int
	message  string
}

func inspectionData(document any) inspectionReply {
	return inspectionReply{document: document, status: http.StatusOK}
}
func inspectionStatus(status int, message string) inspectionReply {
	return inspectionReply{status: status, message: message}
}
func inspectionResult(document any, err error) inspectionReply {
	if err != nil {
		return inspectionStatus(http.StatusInternalServerError, err.Error())
	}
	return inspectionData(document)
}
func writeInspection(w http.ResponseWriter, reply inspectionReply) {
	if reply.status >= 400 {
		writeErr(w, reply.status, reply.message)
		return
	}
	writeJSON(w, reply.status, reply.document)
}

func (q inspectionQuery) task() *Task             { return q.owner.m.ResolveTask(q.values.Get("task")) }
func (q inspectionQuery) pathTask() (*Task, bool) { return q.owner.m.Task(q.pathID) }
func (q inspectionQuery) integer(key string, fallback int) int {
	return atoiDefault(q.values.Get(key), fallback)
}

type inspectionCursor struct {
	before int64
	limit  int
	paged  bool
}

func (q inspectionQuery) cursor(fallback int) inspectionCursor {
	return inspectionCursor{before: int64(q.integer("before", 0)), limit: min(q.integer("limit", fallback), 500), paged: q.values.Get("before") != "" || q.values.Get("page") != ""}
}
func (q inspectionQuery) optionalIntent() *int64 {
	if raw := q.values.Get("intent"); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return &value
		}
	}
	return nil
}

func (q inspectionQuery) nodeFilter() db.NodeFilter {
	return db.NodeFilter{Kinds: csvValues(q.values.Get("kind")), States: csvValues(q.values.Get("state")), Query: q.values.Get("q"), Asc: q.values.Get("order") == "asc"}
}

// Coverage/scope need an asset backend. Asset-reference reads deliberately use
// the exploration store directly and do not pass through this precondition.
type inspectionTaskAssets struct {
	task    *Task
	store   *db.AssetStore
	id      int64
	failure *inspectionReply
}

func (q inspectionQuery) taskAssets() inspectionTaskAssets {
	task, found := q.pathTask()
	if !found {
		failure := inspectionStatus(404, "task not found")
		return inspectionTaskAssets{failure: &failure}
	}
	store := q.owner.m.Assets()
	if store == nil {
		failure := inspectionStatus(503, "asset store is not enabled")
		return inspectionTaskAssets{failure: &failure}
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	return inspectionTaskAssets{task: task, store: store, id: id}
}

// Fields follow the original sorted object keys. Public DTOs still own their
// scalar conversions, source attribution, secret omission and timestamps.
type inspectionTasks struct {
	Active string    `json:"active"`
	Tasks  []TaskDTO `json:"tasks"`
}
type inspectionGraph struct {
	Edges []EdgeDTO     `json:"edges"`
	Nodes []TaskNodeDTO `json:"nodes"`
}
type inspectionIntents struct {
	HasMore bool          `json:"has_more"`
	Items   []TaskNodeDTO `json:"items"`
}
type inspectionBoard struct {
	Assets *map[string][]FindingAssetDTO `json:"assets,omitempty"`
	Edges  []EdgeDTO                     `json:"edges"`
	Items  []TaskNodeDTO                 `json:"items"`
	Page   int                           `json:"page"`
	Refs   map[string]TaskNodeDTO        `json:"refs"`
	Size   int                           `json:"size"`
	Total  int                           `json:"total"`
}
type inspectionReferences struct {
	Facts    []CoverageAssetRefDTO `json:"facts"`
	Findings []CoverageAssetRefDTO `json:"findings"`
	Intents  []CoverageAssetRefDTO `json:"intents"`
}
type inspectionScope struct {
	Scope []db.TaskScope `json:"scope"`
}
type inspectionActivity struct {
	Cursor int64         `json:"cursor"`
	Items  []ActivityDTO `json:"items"`
}
type inspectionHistory struct {
	EarliestCursor int64         `json:"earliest_cursor"`
	HasMore        bool          `json:"has_more"`
	Items          []ActivityDTO `json:"items"`
	SnapshotCursor int64         `json:"snapshot_cursor"`
}
type inspectionTokens struct {
	Sessions []db.SessionTokenUsage `json:"sessions"`
	Total    TokenTotalDTO          `json:"total"`
	Workers  []db.TokenUsage        `json:"workers"`
}
type inspectionDetail struct {
	Detail string `json:"detail"`
}
type inspectionActiveTask struct {
	Description  string `json:"description"`
	EngineMode   string `json:"engine_mode"`
	Goal         string `json:"goal"`
	GoalsMet     int    `json:"goals_met"`
	GoalsTotal   int    `json:"goals_total"`
	ID           string `json:"id"`
	InFlight     int    `json:"in_flight"`
	LastActivity int64  `json:"last_activity"`
	LLMInFlight  int64  `json:"llm_in_flight"`
	Paused       bool   `json:"paused"`
	Running      bool   `json:"running"`
	Stalled      bool   `json:"stalled"`
}
type inspectionStatistics struct {
	ActiveTask     *inspectionActiveTask `json:"active_task,omitempty"`
	AssetCounts    *map[string]int       `json:"asset_counts,omitempty"`
	Assets         *int                  `json:"assets,omitempty"`
	EngineMode     string                `json:"engine_mode"`
	Exploration    *map[string]int       `json:"exploration,omitempty"`
	LLMConfigured  bool                  `json:"llm_configured"`
	Traffic        *int                  `json:"traffic,omitempty"`
	TrafficEnabled *bool                 `json:"traffic_enabled,omitempty"`
}
