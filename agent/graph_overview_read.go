package agent

import (
	"encoding/json"

	"github.com/nobanic/UroborOS/db"
)

// These readers borrow existing stores. They do not cache a snapshot, claim
// inherited work, or change the graph's recovery and cold-region policies.
type overviewLineage struct {
	parents  map[int64][]int64
	yields   map[int64][]int64
	producer map[int64]int64
}

func readOverviewLineage(store *db.ExplorationStore) overviewLineage {
	index := overviewLineage{parents: map[int64][]int64{}, yields: map[int64][]int64{}, producer: map[int64]int64{}}
	rows, _ := store.Edges(5000)
	for _, row := range rows {
		switch row.Rel {
		case db.RelDerivedFrom, db.RelSpawns:
			index.parents[row.To] = append(index.parents[row.To], row.From)
		case db.RelYields:
			index.yields[row.From] = append(index.yields[row.From], row.To)
			index.producer[row.To] = row.From
		}
	}
	return index
}

func overviewTextRows(store *db.ExplorationStore, kind string, limit int, project func(*db.Node, map[string]any) map[string]any) []map[string]any {
	rows, _ := store.ListByKind(kind, limit)
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var payload map[string]any
		_ = json.Unmarshal(row.Payload, &payload)
		result = append(result, project(row, payload))
	}
	return result
}

func overviewDigests(document map[string]any, store *db.ExplorationStore, limit int, sourceID int64) {
	entries, overflow := coldDigestsRecent(store, limit)
	if len(entries) == 0 {
		return
	}
	if sourceID != 0 {
		for _, entry := range entries {
			inheritedMap(entry, sourceID)
		}
	}
	document["cold_digests"] = entries
	if len(overflow) != 0 {
		document["cold_digests_more"] = overflow
	}
}

type graphOverviewRead struct {
	tools   *ToolSet
	store   *db.ExplorationStore
	lineage overviewLineage
}

func (read graphOverviewRead) document() map[string]any {
	document := map[string]any{}
	text := func(row *db.Node, payload map[string]any) map[string]any {
		return map[string]any{"id": row.ID, "state": row.State, "text": payload["text"]}
	}
	document["goals"] = overviewTextRows(read.store, db.KindGoal, 100, text)
	document["hints"] = overviewTextRows(read.store, db.KindHint, 50, func(row *db.Node, payload map[string]any) map[string]any {
		entry := text(row, payload)
		if findingTrafficBindingEnabled() && payload["traffic_refs"] != nil {
			entry["traffic_refs"] = payload["traffic_refs"]
		}
		return entry
	})
	read.lineage = readOverviewLineage(read.store)
	covered, _ := read.store.CoveredMembers()
	var hot map[int64]bool
	if graph, _, err := loadColdGraph(read.store); err == nil {
		hot = graph.hotSet()
	}
	hidden := func(id int64) bool { _, exists := covered[id]; return exists && !hot[id] }

	frontier, _ := read.store.Frontier(30)
	document["open_intents"] = compactIntents(frontier, read.lineage.parents, read.lineage.yields)
	intents, _ := read.store.ListByKind(db.KindIntent, 300)
	var running, finished []*db.Node
	states := make(map[int64]string, len(intents))
	for _, intent := range intents {
		states[intent.ID] = intent.State
		switch intent.State {
		case "running":
			running = append(running, intent)
		case "done", "blocked", "exhausted":
			if !hidden(intent.ID) {
				finished = append(finished, intent)
			}
		}
	}
	document["running_intents"] = compactIntents(running, read.lineage.parents, read.lineage.yields)
	if total, err := read.store.CountFinishedIntents(); err == nil {
		document["done_intents_total"] = total
	}
	if total, err := read.store.CountOpenIntents(); err == nil {
		document["frontier_open"] = total
	} else {
		document["frontier_open"] = len(frontier)
	}
	findings, _ := read.store.ListByKind(db.KindFinding, 1000)
	facts, _ := read.store.ListByKind(db.KindFact, 1000)
	document["findings_total"], document["facts"] = len(findings), len(facts)
	document["finding_list"] = read.findingWindow(findings)
	document["recent_facts"] = read.factWindow(facts, hidden)
	document["recovery_needed"] = read.recoveryWindow(facts, states)
	if len(finished) > 12 {
		finished = finished[:12]
	}
	document["recent_done_intents"] = compactIntents(finished, read.lineage.parents, read.lineage.yields)
	overviewDigests(document, read.store, 15, 0)
	if description, goal, err := read.store.Root(); err == nil {
		document["task"] = map[string]any{"description": description, "goal": goal}
	}
	document["related_tasks"] = read.tools.relatedTaskOverviews()
	read.coverage(document)
	return document
}

func (read graphOverviewRead) findingWindow(rows []*db.Node) []map[string]any {
	result := make([]map[string]any, 0, 10)
	for _, row := range rows {
		if len(result) == 10 {
			break
		}
		var payload map[string]any
		_ = json.Unmarshal(row.Payload, &payload)
		entry := map[string]any{"id": row.ID, "summary": payload["summary"]}
		if id := read.lineage.producer[row.ID]; id > 0 {
			entry["from_intent"] = id
		}
		result = append(result, entry)
	}
	return result
}

func (read graphOverviewRead) factWindow(rows []*db.Node, hidden func(int64) bool) []map[string]any {
	result := make([]map[string]any, 0, 20)
	for _, row := range rows {
		if len(result) == 20 {
			break
		}
		if hidden(row.ID) {
			continue
		}
		entry := compactNode(row)
		if id := read.lineage.producer[row.ID]; id > 0 {
			entry["from_intent"] = id
		}
		var payload map[string]any
		if json.Unmarshal(row.Payload, &payload) == nil {
			addFactDecisionMetadata(entry, payload)
		}
		result = append(result, entry)
	}
	return result
}

func (read graphOverviewRead) recoveryWindow(rows []*db.Node, states map[int64]string) []map[string]any {
	result := make([]map[string]any, 0, 10)
	for _, row := range rows {
		if len(result) == 10 {
			break
		}
		id := read.lineage.producer[row.ID]
		var payload map[string]any
		if json.Unmarshal(row.Payload, &payload) != nil || !factNeedsRecovery(payload, states[id]) {
			continue
		}
		entry := compactNode(row)
		entry["from_intent"] = id
		addFactDecisionMetadata(entry, payload)
		result = append(result, entry)
	}
	return result
}

func (read graphOverviewRead) coverage(document map[string]any) {
	owner := read.tools
	if owner.as == nil || owner.ts == nil || owner.taskID <= 0 {
		return
	}
	view := map[string]any{}
	if !owner.coverageDisabled {
		if coverage, err := owner.as.TaskCoverageWithSources(owner.taskID); err == nil {
			view["denominator"], view["tested"], view["by_type"] = coverage.Denominator, coverage.Tested, coverage.ByType
			view["note"] = overviewCoverageNote
			if coverage.Denominator == 0 {
				view["pct"], view["status"] = nil, "scope not anchored"
			} else {
				view["pct"] = coverage.Pct
			}
		}
	}
	if hosts, err := owner.as.HostsByTaskWithSources(owner.taskID); err == nil {
		view["host_count"] = len(hosts)
	}
	if len(view) > 0 {
		document["coverage"] = view
	}
}

const overviewCoverageNote = "coverage is the asset-test coverage (including endpoints and other related assets); a rough estimate, for reference only: it includes the scope and fact anchors of the current task and its direct related tasks; related scope is read-only. Container-type assets / heavy enumeration make it read low — do not conclude from it that testing is complete; you can use add_task_scope to augment this task's range and list_untested_assets to see untested assets [usually don't call list_untested_assets, just keep advancing the task];"
