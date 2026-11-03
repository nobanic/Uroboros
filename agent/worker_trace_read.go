package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/internal/toolinput"
	"strings"

	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

// These aliases retain the original anonymous decoder identities, including
// partial fields after ignored JSON errors. Public factories stay in tools.go.
type workerTraceQuery = struct {
	IntentID json.RawMessage   `json:"intent_id"`
	Q        string            `json:"q"`
	StepIDs  []json.RawMessage `json:"step_ids"`
	Limit    int               `json:"limit"`
}

type workerTraceFilter = struct {
	Q     string `json:"q"`
	Limit int    `json:"limit"`
}

// workerTraceRead borrows the existing task-bound tool set for a single call;
// source authorization and lazy detail access remain store responsibilities.
type workerTraceRead struct{ tools *ToolSet }

func (r workerTraceRead) trace(_ context.Context, raw json.RawMessage) (actool.Result, error) {
	return toolinput.Partial(raw, func(query workerTraceQuery) (actool.Result, error) {
		intent := pid(query.IntentID)
		if intent <= 0 {
			return actool.Errorf("intent_id is required"), nil
		}
		node, err := r.tools.ts.GetNodeWithSources(intent)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if node == nil || node.Kind != db.KindIntent {
			return actool.Errorf("intent_id does not belong to this task or its directly related tasks"), nil
		}
		if len(query.StepIDs) != 0 {
			return r.details(intent, node, selectTraceSteps(query.StepIDs))
		}
		var rows []db.Activity
		if strings.TrimSpace(query.Q) == "" {
			rows, err = r.tools.ts.ActivityTraceWithSources(intent, query.Limit)
		} else {
			rows, err = r.tools.ts.ActivityTraceSearchWithSources(intent, query.Q, query.Limit)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		document := map[string]any{"intent_id": intent, "steps": traceSteps(rows)}
		if node.Inherited {
			inheritedMap(document, node.SourceTaskID)
		}
		return jsonResult(document)

	})
}

type traceStepSelection struct{ requested, omitted []int64 }

const traceDetailLimit = 5

func selectTraceSteps(raw []json.RawMessage) traceStepSelection {
	var selection traceStepSelection
	seen := make(map[int64]bool)
	for _, value := range raw {
		id := pid(value)
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		selection.requested = append(selection.requested, id)
	}
	if len(selection.requested) > traceDetailLimit {
		selection.omitted = append(selection.omitted, selection.requested[traceDetailLimit:]...)
		selection.requested = selection.requested[:traceDetailLimit]
	}
	return selection
}

func (r workerTraceRead) details(intent int64, node *db.Node, selection traceStepSelection) (actool.Result, error) {
	rows, err := r.tools.ts.ActivityByIDsWithSources(selection.requested)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	steps := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row.NodeID == nil || *row.NodeID != intent || row.Inherited != node.Inherited ||
			(row.Inherited && row.SourceTaskID != node.SourceTaskID) {
			continue
		}
		step := map[string]any{"step_id": row.ID, "kind": row.Kind, "tool": row.Tool, "is_error": row.IsError, "detail": row.Detail}
		if row.Inherited {
			inheritedMap(step, row.SourceTaskID)
		}
		steps = append(steps, step)
	}
	document := map[string]any{"intent_id": intent, "steps": steps, "returned_step_ids": selection.requested}
	if len(selection.omitted) != 0 {
		document["omitted_step_ids"] = selection.omitted
		document["notice"] = fmt.Sprintf(
			"At most %d steps' full content can be fetched per call; this call returned the first %d (%v); the %d not fetched are %v."+
				"If this content is enough to locate what you need, there is no need to fetch the remaining steps; if you truly need to continue, call again with those step_ids.",
			traceDetailLimit, len(selection.requested), selection.requested, len(selection.omitted), selection.omitted)
	}
	if node.Inherited {
		inheritedMap(document, node.SourceTaskID)
	}
	return jsonResult(document)
}

func (r workerTraceRead) search(_ context.Context, raw json.RawMessage) (actool.Result, error) {
	return toolinput.Partial(raw, func(filter workerTraceFilter) (actool.Result, error) {
		if strings.TrimSpace(filter.Q) == "" {
			return actool.Errorf("q is required"), nil
		}
		rows, err := r.tools.ts.ActivityTraceSearchAllWithSources(r.tools.ownerNode, filter.Q, filter.Limit)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		hits := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			var intent int64
			if row.NodeID != nil {
				intent = *row.NodeID
			}
			hit := map[string]any{"intent_id": intent, "step_id": row.ID, "worker": row.Worker,
				"kind": row.Kind, "tool": row.Tool, "is_error": row.IsError, "summary": firstLine(row.Summary, 100)}
			if row.Inherited {
				inheritedMap(hit, row.SourceTaskID)
			}
			hits = append(hits, hit)
		}
		return jsonResult(map[string]any{"query": filter.Q, "hits": hits})

	})
}

func (r workerTraceRead) index(_ context.Context, raw json.RawMessage) (actool.Result, error) {
	return toolinput.Partial(raw, func(filter workerTraceFilter) (actool.Result, error) {
		limit := filter.Limit
		if limit <= 0 {
			limit = 50
		}
		rows, err := r.tools.ts.ListByKindWithSources(db.KindIntent, 500)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		query := strings.ToLower(strings.TrimSpace(filter.Q))
		works := make([]map[string]any, 0, limit)
		for _, row := range rows {
			item := indexedTraceWork(row, query)
			if item == nil {
				continue
			}
			works = append(works, item)
			if len(works) >= limit {
				break
			}
		}
		return jsonResult(map[string]any{"works": works})

	})
}

func indexedTraceWork(node *db.Node, query string) map[string]any {
	if node.Inherited && node.State == "running" {
		return nil
	}
	switch node.State {
	case "running", "done", "exhausted", "blocked", "stopped":
	default:
		return nil
	}
	var payload map[string]any
	_ = json.Unmarshal(node.Payload, &payload)
	summary, _ := payload["summary"].(string)
	if query != "" && !strings.Contains(strings.ToLower(summary), query) {
		return nil
	}
	item := map[string]any{"intent_id": node.ID, "summary": summary, "state": node.State}
	if node.Inherited {
		inheritedMap(item, node.SourceTaskID)
	}
	return item
}
