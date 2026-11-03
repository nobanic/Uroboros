package agent

import (
	"encoding/json"

	"github.com/nobanic/UroborOS/db"
)

type graphSourceReads struct{ tools *ToolSet }

func (read graphSourceReads) documents() []map[string]any {
	sources, err := read.tools.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	allowance := relatedOverviewBudgetForSources(len(sources))
	result := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		view := sourceOverviewRead{tools: read.tools, source: source, hidden: hiddenMembersFor(source.Store), budget: overviewTextBudget{remaining: allowance}}
		result = append(result, view.document())
	}
	return result
}

type sourceOverviewRead struct {
	tools   *ToolSet
	source  db.DirectSourceStore
	hidden  func(int64) bool
	budget  overviewTextBudget
	lineage overviewLineage
}

func (read *sourceOverviewRead) document() map[string]any {
	task, store := read.source.Task, read.source.Store
	document := map[string]any{"source_task_id": task.TaskID, "inherited": true, "task": map[string]any{
		"description": read.budget.take(task.Description, 800), "goal": read.budget.take(task.Goal, 800), "status": task.Status,
	}}
	stats, statsErr := store.Stats()
	read.lineage = readOverviewLineage(store)
	text := func(row *db.Node, payload map[string]any) map[string]any {
		return inheritedMap(map[string]any{"id": row.ID, "state": row.State, "text": read.budget.take(payload["text"], 400)}, task.TaskID)
	}
	goals := overviewTextRows(store, db.KindGoal, relatedOverviewMaxGoalsPerSource, text)
	document["goals"] = goals
	hints := overviewTextRows(store, db.KindHint, relatedOverviewMaxHintsPerSource, text)
	document["hints"] = hints
	facts, _ := store.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
	findings, _ := store.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
	intents, _ := store.ListByKind(db.KindIntent, 300)
	terminal := make(map[int64]bool, len(intents))
	for _, intent := range intents {
		terminal[intent.ID] = inheritedIntentSummaryState(intent.State)
	}
	document["facts"], document["findings"] = len(facts), len(findings)
	if statsErr == nil {
		document["facts"], document["findings"] = stats[db.KindFact], stats[db.KindFinding]
		if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) || stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
			read.budget.truncated = true
		}
	}
	document["recent_findings"] = read.findings(findings)
	document["recent_facts"] = read.facts(facts, terminal)
	document["recent_intent_results"] = read.intentResults()
	overviewDigests(document, store, relatedOverviewMaxDigestsPerSource, task.TaskID)
	if statsErr == nil {
		document["node_stats"] = stats
	}
	read.assets(document)
	if read.budget.truncated {
		document["summary_truncated"] = true
	}
	return document
}

func (read *sourceOverviewRead) findings(rows []*db.Node) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entry := inheritedMap(compactFinding(row), read.source.Task.TaskID)
		entry["summary"] = read.budget.take(entry["summary"], 400)
		result = append(result, entry)
	}
	return result
}

func (read *sourceOverviewRead) facts(rows []*db.Node, terminal map[int64]bool) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if read.hidden(row.ID) {
			continue
		}
		entry := inheritedMap(compactNode(row), read.source.Task.TaskID)
		entry["summary"] = read.budget.take(entry["summary"], 400)
		if id := read.lineage.producer[row.ID]; id > 0 && terminal[id] {
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

func (read *sourceOverviewRead) intentResults() []map[string]any {
	store := read.source.Store
	rows := recentTerminalIntents(store, relatedOverviewMaxIntentsPerTask)
	visible := rows[:0]
	for _, row := range rows {
		if !read.hidden(row.ID) {
			visible = append(visible, row)
		}
	}
	for _, row := range visible {
		row.Inherited = true
		row.SourceTaskID = read.source.Task.TaskID
	}
	result := compactIntents(visible, read.lineage.parents, read.lineage.yields)
	for index, row := range visible {
		result[index]["summary"] = read.budget.take(result[index]["summary"], 400)
		activities, _, err := store.ActivityPageForTerminalIntent(row.ID, 0, 20)
		if err != nil {
			continue
		}
		var preferred, fallback string
		for _, activity := range activities {
			switch activity.Kind {
			case "result":
				preferred = activity.Summary
			case "text":
				fallback = activity.Summary
			}
		}
		if preferred == "" {
			preferred = fallback
		}
		if preferred != "" {
			result[index]["result_summary"] = read.budget.take(preferred, 800)
		}
	}
	return result
}

func (read *sourceOverviewRead) assets(document map[string]any) {
	if read.tools.as == nil {
		return
	}
	task := read.source.Task
	if rows, err := read.tools.as.ListTaskScope(task.TaskID); err == nil && len(rows) > 0 {
		count := len(rows)
		if count > relatedOverviewMaxScopePerSource {
			rows = rows[:relatedOverviewMaxScopePerSource]
			read.budget.truncated = true
		}
		entries := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			entry := map[string]any{"kind": row.Kind, "source": read.budget.take(row.Source, 300)}
			switch {
			case row.Domain != "":
				entry["value"] = read.budget.take(row.Domain, 400)
			case row.Net != "":
				entry["value"] = read.budget.take(row.Net, 400)
			case row.Value != "":
				entry["value"] = read.budget.take(row.Value, 400)
			case row.CompanyID != nil:
				entry["company_id"] = *row.CompanyID
			}
			entries = append(entries, entry)
		}
		document["asset_scope"], document["asset_scope_count"] = entries, count
	}
	if coverage, err := read.tools.as.TaskCoverage(task.TaskID, task.ExplorationID); err == nil {
		document["asset_coverage"] = map[string]any{"denominator": coverage.Denominator, "tested": coverage.Tested, "pct": coverage.Pct, "by_type": coverage.ByType}
	}
}
