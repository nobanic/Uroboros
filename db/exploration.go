package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"

	"strings"
	"time"
)

// ErrIntentStateConflict marks a failed compare-and-set transition. Callers use
// it to distinguish an expected control race (HTTP 409) from a storage failure.
var ErrIntentStateConflict = errors.New("intent state changed concurrently")

// utf8Clean makes a string safe for a PostgreSQL text column. It (1) replaces
// invalid UTF-8 byte sequences with U+FFFD and (2) strips NUL (0x00) bytes.
// Tool output (nmap/curl/… stdout) can carry raw/truncated bytes that PostgreSQL's
// UTF8 encoding rejects ("invalid byte sequence for encoding UTF8"); without this
// the INSERT fails and the activity record is silently lost. NUL is *valid* UTF-8
// (U+0000) so ToValidUTF8 leaves it in place, yet PostgreSQL text still rejects it
// (SQLSTATE 22021) — so it must be removed separately. JSONB columns need the
// separate jsonbClean below: json.Marshal encodes a NUL as the escape backslash-u-0000,
// which the text-type json accepts but jsonb rejects (SQLSTATE 22P05).
func utf8Clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "�")
}

// jsonbClean makes marshaled JSON safe for a PostgreSQL jsonb column. json.Marshal
// faithfully encodes a NUL byte (U+0000) as the 6-byte escape sequence \u0000; the
// json type stores it, but jsonb rejects it with "unsupported Unicode escape
// sequence" (SQLSTATE 22P05). Captured HTTP/tool bytes can carry NULs, so drop the
// escape — this also covers NULs nested inside json.RawMessage fields, which are
// copied verbatim into the output. Only a *real* escape is stripped: a \u0000 is
// genuine when preceded by an even number of backslashes, so an escaped-backslash
// run such as \\u0000 (the literal text u0000) is left intact.
func jsonbClean(b []byte) []byte {
	if !bytes.Contains(b, []byte("\\u0000")) {
		return b
	}
	out := make([]byte, 0, len(b))
	bs := 0 // consecutive backslashes already emitted before position i
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' && bs%2 == 0 && i+5 < len(b) &&
			b[i+1] == 'u' && b[i+2] == '0' && b[i+3] == '0' && b[i+4] == '0' && b[i+5] == '0' {
			i += 5 // skip the whole \u0000
			bs = 0
			continue
		}
		if b[i] == '\\' {
			bs++
		} else {
			bs = 0
		}
		out = append(out, b[i])
	}
	return out
}

// Node is a typed reasoning node (= old task_nodes). kind ∈ goal|intent|finding|hint.
type Node struct {
	FindingID     int64           `json:"finding_id,omitempty"` // populated by finding-aware reads; never inferred from ID
	FindingNodeID int64           `json:"finding_node_id,omitempty"`
	TrafficCount  int             `json:"traffic_count,omitempty"`
	ID            int64           `json:"id"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	Priority      int             `json:"priority"`
	State         string          `json:"state"`
	Origin        string          `json:"origin,omitempty"`
	Owner         string          `json:"owner,omitempty"`
	BlockedReason string          `json:"blocked_reason,omitempty"`
	DeleteReason  string          `json:"delete_reason,omitempty"` // non-empty only on intent soft delete (state='deleted')
	Anchors       []int64         `json:"anchors,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SourceTaskID  int64           `json:"source_task_id,omitempty"`
	Inherited     bool            `json:"inherited,omitempty"`
}

// Activity is one worker execution step (= old task_activity).
type Activity struct {
	ID        int64           `json:"id"`
	NodeID    *int64          `json:"node_id,omitempty"`
	Worker    string          `json:"worker,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error"`
	Summary   string          `json:"summary,omitempty"`
	Detail    string          `json:"-"` // full blob; lazy-loaded, omitted from list payloads
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	// Token usage — set only on the terminal kind='result' record; nil elsewhere.
	InputTokens      *int  `json:"input_tokens,omitempty"`
	OutputTokens     *int  `json:"output_tokens,omitempty"`
	CacheReadTokens  *int  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int  `json:"cache_write_tokens,omitempty"`
	SourceTaskID     int64 `json:"source_task_id,omitempty"`
	Inherited        bool  `json:"inherited,omitempty"`
	// MainSeg is the main-agent conversation segment (nil for non-mainagent rows;
	// nil/0 == the original session). Lets the UI route a mainagent row to its segment.
	MainSeg *int `json:"main_seg,omitempty"`
}

// TokenUsage is a per-worker token aggregate (TokenStatsByWorker).
type TokenUsage struct {
	Worker           string `json:"worker"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// SessionTokenUsage is the authoritative completed-run total for one task UI
// session. Worker sessions are keyed by intent rather than the reusable work#N
// executor name, so retries and reassignment remain attached to the same row.
type SessionTokenUsage struct {
	Session          string `json:"session"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// DailyTokenBucket is one day's global token aggregate across all tasks.
type DailyTokenBucket struct {
	Day             string `json:"day"` // "YYYY-MM-DD"
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CacheReadTokens int    `json:"cache_read_tokens"`
}

// TokenDailyAll aggregates token consumption by calendar day (UTC) for the
// past `days` days across all explorations. Only kind='result' rows carry token
// counts (the terminal per-run summary), so there is no double-counting.
func (d *DB) TokenDailyAll(days int) ([]DailyTokenBucket, error) {
	return dailyActivityTotals(d, days)

}

// ExplorationStore is a handle onto one exploration's reasoning graph + activity.
type ExplorationStore struct {
	db    *DB
	expID int64
}

// CreateExploration creates a new exploration root and returns its id.
func (d *DB) CreateExploration(description, goal string) (int64, error) {
	var id int64
	err := d.QueryRow(`INSERT INTO explorations(description, goal) VALUES ($1, $2) RETURNING id`, description, goal).Scan(&id)
	return id, err
}

// Exploration returns a handle bound to an exploration id.
func (d *DB) Exploration(id int64) *ExplorationStore { return &ExplorationStore{db: d, expID: id} }

func (s *ExplorationStore) ID() int64 { return s.expID }

// Root returns the exploration's description and goal.
func (s *ExplorationStore) Root() (description, goal string, err error) {
	var d sql.NullString
	err = s.db.QueryRow(`SELECT description, goal FROM explorations WHERE id=$1`, s.expID).Scan(&d, &goal)
	return d.String, goal, err
}

// AddNode writes a typed reasoning node with optional anchors to asset ids.
func (s *ExplorationStore) AddNode(kind string, payload map[string]any, priority int, state, origin string, anchors []int64) (int64, error) {
	return (graphWrite{store: s}).create(kind, payload, priority, state, origin, anchors)
}

// AddIntent is a convenience: an open intent.
func (s *ExplorationStore) AddIntent(payload map[string]any, priority int, anchors []int64, origin string) (int64, error) {
	if origin == "" {
		origin = "planner"
	}
	return s.AddNode("intent", payload, priority, "open", origin, anchors)
}

// AddGoal writes a goal node (state open).
func (s *ExplorationStore) AddGoal(payload map[string]any, origin string) (int64, error) {
	return s.AddNode("goal", payload, 0, "open", origin, nil)
}

// OriginFactID returns this exploration's root fact id — the KindFact node with
// state='origin' seeded at task creation (the task root). Every intent traces
// back to it. Returns 0 (no error) when none exists (legacy explorations created
// under the old 'begin' root, or none yet).
func (s *ExplorationStore) OriginFactID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='origin' ORDER BY id LIMIT 1`, s.expID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// Anchor records a node→asset reference edge (many-to-many): it captures which
// global assets an exploration node touched, as lineage/provenance. The asset
// graph itself is global and shared — anchoring no longer gates visibility (any
// task may read any asset via search); it only preserves the reasoning trail.
// Idempotent.
func (s *ExplorationStore) Anchor(nodeID, assetID int64) error {
	return (graphWrite{store: s}).anchor(nodeID, assetID)
}

// Link adds a typed exploration edge (idempotent).
func (s *ExplorationStore) Link(from int64, rel string, to int64) error {
	return (graphWrite{store: s}).link(from, rel, to)
}

// SetNodeState updates any node's state (never deletes). content_version bumps
// so a folded member's state flip invalidates its digest's cached body (§5.3).
func (s *ExplorationStore) SetNodeState(id int64, state string) error {
	return (graphWrite{store: s}).state(id, state)
}

// UpdateGoalPayload rewrites a goal node's payload text (and optional vulnclass);
// scoped to kind='goal' so it can never mutate an intent/fact by id. Returns an
// error if no such goal exists in this exploration.
func (s *ExplorationStore) UpdateGoalPayload(id int64, text, vulnclass string) error {
	return (graphWrite{store: s}).goal(id, text, vulnclass, false)
}

// DeleteGoal hard-deletes a goal node; scoped to kind='goal' so it can never drop
// an intent/fact by id. The edges (spawns) and anchors reference it with ON DELETE
// CASCADE, so they go with it; activity.node_id is ON DELETE SET NULL. Returns an
// error if no such goal exists in this exploration.
func (s *ExplorationStore) DeleteGoal(id int64) error {
	return (graphWrite{store: s}).goal(id, "", "", true)
}

// SetIntentState updates an intent node's state.
// ResetRunningIntents returns any intent left in 'running' back to 'open' so it
// is re-claimed. Called on startup: a 'running' intent with no live worker (a
// backend restart or crashed worker goroutine left it stuck) would otherwise spin
// forever in the UI. Workers resume from their transcript, so reopening is safe.
func (s *ExplorationStore) ResetRunningIntents() (int64, error) {
	return (intentPopulation{store: s}).reset()
}

// ReopenIntent flips ONE not-successfully-finished intent (blocked/exhausted/stopped)
// back to 'open' so a worker re-claims it (graph writes it already produced stay).
// The worker resumes from its prior LLM transcript instead of restarting from scratch.
// done/open/running are left untouched. Returns whether a row changed. Used by "Rerun".
func (s *ExplorationStore) ReopenIntent(id int64) (bool, error) {
	return (intentCommand{store: s, id: id}).reopen()
}

// ReopenBlockedIntents flips EVERY 'blocked' intent in this exploration back to 'open'
// (batch rerun after e.g. an LLM/network outage that blocked many at once). Returns the
// number reopened. Workers resume from their transcripts; kept graph writes remain.
func (s *ExplorationStore) ReopenBlockedIntents() (int64, error) {
	return (intentPopulation{store: s}).reopenBlocked()
}

func (s *ExplorationStore) SetIntentState(id int64, state string) error {
	return (intentCommand{store: s, id: id}).set(state)
}

// CompareAndSetIntentState transitions one local intent only when its current
// state still matches expected. It is the state boundary used by pause/resume and
// worker settlement, so a stale API read cannot overwrite a concurrent finish.
func (s *ExplorationStore) CompareAndSetIntentState(id int64, expected, state string) (bool, error) {
	return (intentCommand{store: s, id: id}).compare(expected, state)
}

// IntentCleanup summarizes the blackboard records removed when a user cancels
// one worker intent. Global assets and traffic are deliberately not part of this
// operation; exploration anchors disappear only because their owning nodes do.
type IntentCleanup struct {
	Intents    int64 `json:"intents"`
	Facts      int64 `json:"facts"`
	Findings   int64 `json:"findings"`
	Activities int64 `json:"activities"`
}

// SoftDeleteIntent soft-deletes an open/running/paused intent: sets state='deleted' and records the
// user-entered deletion reason into the delete_reason field, keeping the intent node and all its outputs/lineage
// (no longer hanging an extra fact on the graph like the old implementation). Returns the intent's pre-deletion summary for the planner notification. Side sessions are cleaned up along with the deleted state.
// The caller must first stop any running worker to prevent later writes from it.
func (s *ExplorationStore) SoftDeleteIntent(id int64, reason string) (string, error) {
	return (intentCommand{store: s, id: id}).soft(reason)
}

// CancelIntent physically deletes an intent, plus all exclusive descendant nodes "supported only by that
// intent" — nodes reachable downward from the intent via yields/derived_from whose every parent (the source of every edge pointing to it) falls within the deletion set.
// goal and origin fact are never deleted; shared nodes still referenced by an intent/digest outside the deletion set are also kept, to avoid breaking
// other branches or creating dangling links. Each deleted intent first does a token rollup (preserving irreversible accounting) and cleans up its activity
// and side sessions; each deleted finding first deletes its findings-table row (node_id FK is ON DELETE SET NULL, otherwise an orphan would remain).
// Edges are cleaned up via CASCADE with the nodes, and the whole cleanup stays in one transaction. The caller must first stop any running worker to prevent later writes from it.
func (s *ExplorationStore) CancelIntent(id int64) (IntentCleanup, error) {
	return (intentCommand{store: s, id: id}).cancel()
}

type tokenUsageBucket struct {
	Day   time.Time
	Usage TokenUsage
}

// intentTokenRollup returns exactly-once usage grouped by its original UTC day.
// Each result terminates one run. A result carrying usage is authoritative; an
// older error/cancel result without usage falls back to the latest cumulative
// usage frame since the previous result. A trailing frame represents a run that
// was interrupted before its terminal result could be persisted.
func intentTokenRollup(tx *sql.Tx, explorationID, intentID int64) ([]tokenUsageBucket, error) {
	return (intentMetering{transaction: tx, explorationID: explorationID, intentID: intentID}).read()
}

func (u *TokenUsage) add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
}

const nodeCols = `id, kind, payload, priority, state, COALESCE(origin,''), COALESCE(owner,''), COALESCE(blocked_reason,''), COALESCE(delete_reason,''), created_at`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var payload []byte
	if err := sc.Scan(&n.ID, &n.Kind, &payload, &n.Priority, &n.State, &n.Origin, &n.Owner, &n.BlockedReason, &n.DeleteReason, &n.CreatedAt); err != nil {
		return nil, err
	}
	n.Payload = json.RawMessage(payload)
	return &n, nil
}

func scanNodes(rows *sql.Rows) ([]*Node, error) {
	var out []*Node
	return collectRowValues(rows, out, func() (*Node, error) {
		return scanNode(rows)
	})
}

// ListByKind lists nodes of a kind (newest first).
func (s *ExplorationStore) ListByKind(kind string, limit int) ([]*Node, error) {
	return readGraph(s).equal("kind", kind).nodes("id DESC", graphLimit(limit, 500), nil)
}

// ListByKindPage returns one newest-first page of nodes of a kind for reverse
// pagination: up to `limit` nodes with id < `before` (before<=0 = the newest page).
// hasMore reports whether still-older nodes exist, so a client (e.g. the worker
// session list) can page past the old fixed cap instead of losing older intents.
func (s *ExplorationStore) ListByKindPage(kind string, before int64, limit int) ([]*Node, bool, error) {
	capacity := graphLimit(limit, 300)
	nodes, failure := readGraph(s).equal("kind", kind).before(before).nodes("id DESC", capacity+1, nil)
	return graphPage(nodes, capacity, failure)
}

// listByKindPageFiltered is ListByKindPage with an optional summary keyword
// filter (payload->>'summary' ILIKE %q%). Fetches one extra row so the caller can
// probe hasMore. Empty q = no filter. Newest-first by id.
func (s *ExplorationStore) listByKindPageFiltered(kind string, before int64, limit int, q string) ([]*Node, error) {
	return readGraph(s).equal("kind", kind).before(before).summary(q).nodes("id DESC", limit+1, nil)
}

// countByKindFiltered counts nodes of a kind under the same summary filter, for a
// page's total. Empty q = count all of that kind.
func (s *ExplorationStore) countByKindFiltered(kind, q string) (int, error) {
	return readGraph(s).equal("kind", kind).summary(q).count()
}

// CountFinishedIntents counts this exploration's intents in a terminal explored
// state (done/blocked/exhausted) — graph_overview's done_intents_total, so the planner
// knows recent_done_intents (capped at a recent window) is a truncated view and
// stays cautious about "already tried" dedup. Excludes 'stopped' (killed/deleted),
// matching exactly what recent_done_intents surfaces.
func (s *ExplorationStore) CountFinishedIntents() (int, error) {
	return readGraph(s).equal("kind", KindIntent).oneOf("state", []string{"done", "blocked", "exhausted"}).count()
}

// CountOpenIntents counts this exploration's open intents — graph_overview's
// frontier_open, so the planner knows open_intents (capped at the top-N by
// priority) is a truncated view and there may be more claimable work.
func (s *ExplorationStore) CountOpenIntents() (int, error) {
	return readGraph(s).equal("kind", KindIntent).equal("state", "open").count()
}

// GetNode returns one node of this exploration by id (nil, nil if not found).
func (s *ExplorationStore) GetNode(id int64) (*Node, error) {
	records, failure := readGraph(s).equal("id", id).nodes("id ASC", 1, nil)
	if failure != nil || len(records) == 0 {
		return nil, failure
	}
	return records[0], nil
}

// Nodes lists all nodes (for graph viz).
func (s *ExplorationStore) Nodes(limit int) ([]*Node, error) {
	return readGraph(s).nodes("id ASC", graphLimit(limit, 2000), nil)
}

// NodeFilter narrows a NodesPage query. Empty fields include every value.
type NodeFilter struct {
	Kinds  []string // exploration_nodes.kind
	States []string // exploration_nodes.state
	Query  string   // case-insensitive substring over payload text / origin
	Asc    bool     // true = oldest first (replay); default newest first (broadcast)
}

// NodesPage returns one 1-based page of this exploration's nodes plus the total
// matching count. The activity board reads the graph as a time series, so it pages in SQL
// rather than pulling the whole graph like Nodes does. Ordering is by id, which
// is BIGSERIAL and therefore creation order — stable when several nodes share a
// created_at second.
func (s *ExplorationStore) NodesPage(f NodeFilter, page, size int) ([]*Node, int, error) {
	if page < 1 {
		page = 1
	}
	size = min(graphLimit(size, 20), 200)
	selection := readGraph(s).oneOf("kind", f.Kinds).oneOf("state", f.States).search(f.Query)
	total, failure := selection.count()
	if failure != nil {
		return nil, 0, failure
	}
	order := "id DESC"
	if f.Asc {
		order = "id ASC"
	}
	offset := (page - 1) * size
	records, failure := selection.nodes(order, size, &offset)
	if failure != nil {
		return nil, 0, failure
	}
	return records, total, nil
}

// NodesByIDs loads the given nodes of this exploration in id order. Used to
// resolve the neighbours of an activity-board page without fetching the whole graph.
func (s *ExplorationStore) NodesByIDs(ids []int64) ([]*Node, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return readGraph(s).identifiers([]string{"id"}, ids).nodes("id ASC", len(ids), nil)
}

// EdgesTouching returns every edge with one end among ids — the activity board uses it to
// spell out where a node came from and what it produced.
func (s *ExplorationStore) EdgesTouching(ids []int64) ([]Edge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return readGraph(s).identifiers([]string{"src_id", "dst_id"}, ids).edges(nil)
}

// Edge is one row from exploration_edges.
type Edge struct {
	From int64
	Rel  string
	To   int64
}

// Edges lists exploration edges (for graph viz).
func (s *ExplorationStore) Edges(limit int) ([]Edge, error) {
	capacity := graphLimit(limit, 5000)
	return readGraph(s).edges(&capacity)
}

// FactsYielded returns the ids of fact nodes an intent produced (intent --yields-->
// fact), oldest first. Used to spell out the round's incremental facts to the planner
// alongside the finished worker's output. Best-effort: returns nil for an intent with
// no facts (or a missing one).
func (s *ExplorationStore) FactsYielded(intentID int64) ([]int64, error) {
	return (intentViews{store: s}).facts(intentID)
}

// FindingLineage returns the sub-DAG that leads FROM the exploration root TO the
// given node: the node itself plus all its ancestors (nodes reverse-reachable by
// following edges backward), and every edge whose both endpoints are in that set.
// Used to render "how this finding was reached" — origin → … → finding. Returns
// empty (not error) for a nonexistent node.
func (s *ExplorationStore) FindingLineage(nodeID int64) ([]*Node, []Edge, error) {
	return (intentViews{store: s}).lineage(nodeID)
}

// FindingIntents maps each finding id to the intent that produced it (the
// intent --yields--> finding edge; report_finding links it). Findings with no
// producing intent are absent. Precise (JOIN, no edge-scan limit).
func (s *ExplorationStore) FindingIntents() (map[int64]int64, error) {
	return (intentViews{store: s}).origins(false)
}

// FindingIntentsTerminal is the inherited-history variant: it returns finding
// lineage only when the producing intent has reached an immutable terminal
// state. This keeps a source task's live worker topology private.
func (s *ExplorationStore) FindingIntentsTerminal() (map[int64]int64, error) {
	return (intentViews{store: s}).origins(true)
}

// Frontier returns open intents ordered by priority desc then id (FIFO tiebreak).
func (s *ExplorationStore) Frontier(limit int) ([]*Node, error) {
	return readGraph(s).equal("kind", KindIntent).equal("state", "open").nodes("priority DESC, id ASC", graphLimit(limit, 50), nil)
}

// HasActiveIntent reports whether this exploration has any intent still in play
// (state open OR running). Used to decide whether to kick the FIRST planner round:
// a seeded task's intent may already have been claimed (open→running) by a worker
// before the check, so counting only 'open' (Frontier) races with worker claim and
// wrongly kicks a redundant first round. Counting open+running is race-proof.
func (s *ExplorationStore) HasActiveIntent() (bool, error) {
	return readGraph(s).equal("kind", KindIntent).oneOf("state", []string{"open", "running"}).exists()
}

// HasOpenGoal reports whether this exploration still has any goal in state 'open'.
// false ⇒ all goals are met/abandoned (or the task has no goals) ⇒ enter the goalless (manual direct-submit) branch:
// the planner stops, and whether the task ends is decided by whether the frontier drains. met and abandoned both count as "settled".
func (s *ExplorationStore) HasOpenGoal() (bool, error) {
	return readGraph(s).equal("kind", KindGoal).equal("state", "open").exists()
}

// AllGoalsMet is the persistent whole-task completion predicate. It deliberately
// requires at least one goal and requires every goal to be explicitly met;
// abandoned/deleted goals therefore do not silently turn a task into success.
// Keeping this as one aggregate query avoids the limits and races of fetching a
// recent ListByKind page and judging it in memory.
func (s *ExplorationStore) AllGoalsMet() (allMet bool, total int, err error) {
	var met int
	err = s.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE state='met')
FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, s.expID).Scan(&total, &met)
	return total > 0 && met == total, total, err
}

// ClaimIntent atomically moves an open intent to running. Returns true if claimed.
func (s *ExplorationStore) ClaimIntent(id int64, owner string) (bool, error) {
	return (graphWrite{store: s}).claim(id, owner)
}

// Stats returns node counts grouped by kind (for dashboard).
func (s *ExplorationStore) Stats() (map[string]int, error) {
	return collectQueryState(queriedRows(s.db.Query(`SELECT kind, count(*) FROM exploration_nodes WHERE exploration_id=$1 GROUP BY kind`, s.expID)), func() map[string]int {
		return map[string]int{}
	}, func(queryRow rowDecoder, queryState *map[string]int) error {
		var k string
		var c int
		if err := queryRow.Scan(&k, &c); err != nil {
			return err
		}
		(*queryState)[k] = c
		return nil
	})
}

// --- activity (poll by global id cursor) ---

// AppendActivity records one worker step and returns its id.
func (s *ExplorationStore) AppendActivity(a Activity) (int64, error) {
	return appendExecutionStep(s.db, s.expID, true, a)

}

// ExplorationDiag probes, at the moment of an activity FK violation (23503), why the
// parent row is unreachable. It reports whether the exploration row still exists, how
// many task rows reference it (a live task should keep exactly 1 — and RESTRICT on
// tasks.exploration_id means the exploration CANNOT be deleted while that row lives),
// and the current MAX(explorations.id). Together these tell apart the failure modes:
//   - expExists=false, taskRefs=0 → the whole row set is gone (DB reset / wrong DB).
//   - expExists=false, taskRefs=1 → impossible under RESTRICT; would mean a broken FK.
//   - expExists=true              → the write's expID is NOT this exploration (stale/
//     wrong id in the in-memory store); compare against Store.ID().
func (d *DB) ExplorationDiag(expID int64) (expExists bool, taskRefs int, maxExpID int64, err error) {
	err = d.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM explorations WHERE id=$1),
		(SELECT COUNT(*) FROM tasks WHERE exploration_id=$1),
		COALESCE((SELECT MAX(id) FROM explorations),0)`, expID).Scan(&expExists, &taskRefs, &maxExpID)
	return
}

// TokenTotal sums token usage across ALL workers for this exploration (whole-task
// total). Same source as TokenStatsByWorker (kind='result' rows), just ungrouped.
func (s *ExplorationStore) TokenTotal() (TokenUsage, error) {
	return activityTokenTotal(s)

}

// TokenTotalsAll returns the whole-task token total for every exploration in one
// query (exploration_id → total), so the task list can show per-task consumption
// without a query per row.
func (d *DB) TokenTotalsAll() (map[int64]TokenUsage, error) {
	return allActivityTokenTotals(d)

}

// LastActivityAll returns the unix time of the most recent activity per
// exploration (exploration_id → max created_at epoch), one query for all tasks.
// Persisted (unlike Engine.LastActivity's in-memory map), so it survives restarts
// and gives a terminal task a stable "ran until" time for computing run duration.
func (d *DB) LastActivityAll() (map[int64]int64, error) {
	return persistedActivityTimes(d)

}

// GoalCounts is the goal summary for one exploration.
type GoalCounts struct{ Total, Met int }

// FindingSeverityCounts breaks a task's findings down by severity for the task
// list (records outside the severity whitelist / with empty severity are not counted in any tier).
type FindingSeverityCounts struct{ Critical, High, Medium, Low int }

// TaskListMetrics contains the aggregates rendered in task lists.
type TaskListMetrics struct {
	Tokens         TokenUsage
	LastActivity   int64
	Goals          GoalCounts
	RunningIntents int                   // count of kind='intent' and state='running', i.e. the number of running Workers
	Findings       FindingSeverityCounts // count of this task's findings in the findings table (bucketed by severity)
}

// TaskListMetricsAll returns list aggregates for every live task in one query.
// The lateral lookups use the per-exploration indexes instead of grouping the
// complete activity history on every poll.
func (d *DB) TaskListMetricsAll() (map[int64]TaskListMetrics, error) {
	return d.taskListMetrics("")
}

// TaskListMetricsForTask returns the same authoritative aggregates as the list
// query for one live task, so list/detail DTOs cannot drift to different sources.
func (d *DB) TaskListMetricsForTask(taskID int64) (TaskListMetrics, error) {
	all, err := d.taskListMetrics(" AND task.id=$1", taskID)
	if err != nil {
		return TaskListMetrics{}, err
	}
	for _, metrics := range all {
		return metrics, nil
	}
	return TaskListMetrics{}, nil
}

func (d *DB) taskListMetrics(filter string, args ...any) (map[int64]TaskListMetrics, error) {
	return keyedExecutionMetrics[int64](d, taskMetricSources, "task.exploration_id", "", taskMetricFields, recordCriteria{where: "WHERE task.deleted_at IS NULL" + filter, values: args})

}

// GoalCountsAll returns goal totals (total / met) per exploration id, one query
// for all tasks — used by listTasks so the task list shows progress for every task.
func (d *DB) GoalCountsAll() (map[int64]GoalCounts, error) {
	return persistedGoalCounts(d)

}

// TokenStatsByWorker sums token usage per worker for this exploration (from the
// kind='result' records that carry usage). Used by the per-agent token display.
func (s *ExplorationStore) TokenStatsByWorker() ([]TokenUsage, error) {
	return activityWorkerTotals(s)

}

// TokenStatsBySession aggregates every persisted completed run, independent of
// activity-history pagination. Main/planner use fixed keys; Worker executions use
// their intent node id, even when a different work#N process handled a retry.
func (s *ExplorationStore) TokenStatsBySession() ([]SessionTokenUsage, error) {
	return activitySessionTotals(s)

}

// ActivitySessionFilter selects one UI "session" within a task's activity stream.
// Exactly one field is meaningful:
//   - Main == true  → the main-agent session (worker="mainagent") for one segment
//     (MainSeg; nil/0 = the original segment, which also matches legacy NULL rows).
//   - Worker != ""  → filter by worker name (Plan = "planner").
//     The Goal Agent's round-0 decomposition is also stored with worker="planner", so the Plan session fully covers Goal+Planner.
//   - NodeID != nil → a Worker session, filtered by node_id (= intent id).
//
// A zero value (all empty) matches the whole task (no session filter).
type ActivitySessionFilter struct {
	Worker  string
	NodeID  *int64
	Main    bool // main-agent session
	MainSeg *int // segment for the main session (nil == current, resolved by caller; 0 == original)
}

// ActivityMaxID returns the task's current maximum activity id (0 when empty). It is
// the task-level snapshot cursor handed to the SSE stream so history (id<=cursor) and
// the live tail (id>cursor) meet with no gap — deliberately task-level, not per
// session, since one SSE covers the whole task.
func (s *ExplorationStore) ActivityMaxID() (int64, error) {
	var max sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(id) FROM activity WHERE exploration_id=$1`, s.expID).Scan(&max)
	if err != nil {
		return 0, err
	}
	return max.Int64, nil
}

// MainSession is one resettable main-agent conversation segment of a task. Segment 0
// is the original session (implicit, never stored); further segments are created by
// "New Session" to start the main agent on a clean transcript while the task's graph,
// assets and goal stay shared.
type MainSession struct {
	Seq       int       `json:"seq"`
	CreatedAt time.Time `json:"created_at"`
}

// CurrentMainSeg returns the highest main-session segment (0 when none created yet).
func (s *ExplorationStore) CurrentMainSeg() (int, error) {
	var seg int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM main_sessions WHERE exploration_id=$1`, s.expID).Scan(&seg)
	return seg, err
}

// ListMainSessions returns every main-session segment newest-first, always including
// the implicit original segment 0. Segment 0 carries no stored timestamp.
func (s *ExplorationStore) ListMainSessions() ([]MainSession, error) {
	return mainSessionHistory(s)

}

// NewMainSession creates the next main-session segment (seq = current+1) and returns
// it. It touches only main_sessions — the task's exploration graph/assets/goal are
// untouched, so the new session starts with a clean transcript over the same task.
func (s *ExplorationStore) NewMainSession() (MainSession, error) {
	return allocateMainSession(s)

}

// ActivityDetail lazily returns the full detail blob for one step.
func (s *ExplorationStore) ActivityDetail(id int64) (string, error) {
	return executionStepDetail(s.db, true, s.expID, id)

}

// AssetRef is a compact exploration node (intent/fact/finding) that references an
// asset via an anchor — for the coverage graph's node drawer.
type AssetRef struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Summary      string `json:"summary"`
	SourceTaskID int64  `json:"source_task_id,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
}

// AssetRefs returns the intents / facts / findings in THIS exploration anchored to
// the given asset id (newest first) — what this task tested / concluded about it.
func (s *ExplorationStore) AssetRefs(assetID int64) ([]AssetRef, error) {
	if assetID <= 0 {
		return []AssetRef{}, nil
	}
	return collectQueryState(queriedRows(s.db.Query(`
SELECT en.id, en.kind, en.state, en.payload
FROM exploration_anchors ea
JOIN exploration_nodes en ON en.id = ea.node_id
WHERE ea.asset_id = $1 AND en.exploration_id = $2 AND en.kind IN ('intent','fact','finding')
ORDER BY en.id DESC`, assetID, s.expID)), func() []AssetRef {
		return []AssetRef{}
	}, func(queryRow rowDecoder, queryState *[]AssetRef) error {
		var r AssetRef
		var payload []byte
		if err := queryRow.Scan(&r.ID, &r.Kind, &r.State, &payload); err != nil {
			return err
		}
		var p map[string]any
		_ = json.Unmarshal(payload, &p)
		for _, k := range []string{"summary", "text"} {
			if v, ok := p[k].(string); ok && strings.TrimSpace(v) != "" {
				r.Summary = v
				break
			}
		}
		*queryState = append(*queryState, r)
		return nil
	})
}

// AddStandaloneFinding writes a finding to the standalone findings table, which
// persists across task deletion (task_id / node_id become NULL when the task or
// exploration node is deleted). taskID and nodeID may be 0 (stored as NULL).
func (s *ExplorationStore) AddStandaloneFinding(taskID, nodeID int64, vulnclass, name, severity, summary, evidence, worker string, assetIDs []int64) (int64, error) {
	return s.db.AddFinding(taskID, nodeID, vulnclass, name, severity, summary, evidence, worker, assetIDs)
}
