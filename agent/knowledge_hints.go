package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/internal/toolinput"
	actool "github.com/nobanic/norma/tool"
	"strings"
)

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint attaches one hint node (active/human) to the exploration graph, can anchor assets, returns the id.
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("the agent's automatic traffic binding is off; the hint carrying traffic_refs was not saved; enable it in system settings, or hand off text only")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text cannot be empty")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// Waking the planner is not done per-entry here — addHint triggers it once after the whole batch is written (carrying the hint text),
	// to avoid a single add_hint with multiple hints spamming the planner's trigger line one by one.
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "Attach the human / main agent's strategic hint to the exploration graph; the planner will read it next time it generates intents.\n"+
		"★Prefer batching: put multiple hints into the hints array and submit at once (fewer round-trips than one-by-one calls). Returns an ids array, same length and order as hints (failed entries id=0, details in errors). For a single one, omit hints and give the top-level text directly.",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "[Prefer this] array of hints to add, processed in order. Each element has the same fields as the top-level fields below (text/asset_ids/traffic_refs). Returned ids match this array in length and order.", "items": obj(map[string]any{"text": str("hint content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[single] hint content, e.g. 'focus on authenticated endpoints'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "anchored asset ids (optional, 0/1/many)"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // single mode: top-level text/asset_ids
			}) (actool.Result, error) {
				items, batch := pickKnowledgeItems(a.Hints, a.hintItem)

				var addedTexts []string
				writtenBatch := writeKnowledgeItems(items, t.addOneHint, func(it hintItem) { addedTexts = append(addedTexts, strings.TrimSpace(it.Text)) })
				if len(addedTexts) > 0 {
					// Wake the planner (once per batch). Prefer notifyHint: one add_hint records one 'someone added
					// N strategic hints: …' trigger, so the planner clearly sees "this round was triggered by a new hint" and the hint content;
					// when that callback is not wired, fall back to plain notify (bare wake; the hint is still folded into the graph for it to read itself).
					switch {
					case t.notifyHint != nil:
						t.notifyHint(addedTexts)
					case t.notify != nil:
						t.notify()
					}
				}

				return writtenBatch.result(batch, "hint added")

			})
		})
}
