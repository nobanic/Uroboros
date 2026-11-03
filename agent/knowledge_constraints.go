package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/internal/toolinput"
	actool "github.com/nobanic/norma/tool"
	"strings"
)

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint persists one operation constraint to task_constraints. origin takes t.worker (default system):
// the decomposer writes "goals", the main agent writes "human".
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text cannot be empty")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // default to deny: more conservative when the type is not labeled
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("type must be allow or deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints adds operation constraints to [this task] (allow = what may be done / deny = what is forbidden). It is both the goals
// decomposer's round-0 constraint-extraction submit tool and the main agent's tool for adding constraints at runtime — the same managed tool, whose
// description/schema can be edited in the web UI and bound per agent. Constraints are injected into the planner/worker system prompt to bound exploration.
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"Add operation constraints to [this task] to frame the exploration boundary: type=allow (operations allowed) or deny (operations forbidden).\n"+
			"A constraint = a rule about 'which operations may / may not be done' (e.g. 'test only the current port, do not scan other ports', 'no write operations on the production database', 'passive reconnaissance only'), not a goal and not an attack step.\n"+
			"★Prefer batching: put multiple into the constraints array and submit at once; returns ids of equal length and order (failed entries id=0, details in errors). For a single one, omit constraints and give the top-level text/type directly.\n"+
			"Record only constraints [explicitly written] in the task goal/description; do not invent them; when unsure of the type use deny (more conservative).",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "[Prefer this] array of constraints to add, processed in order. Each element: text (required, one constraint) + type (allow|deny). Returned ids match this array in length and order.", "items": map[string]any{"type": "object"}},
			"text":        str("[single] the content of one operation constraint"),
			"type":        str("[single] allow or deny; defaults to deny"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints is not enabled: ExplorationStore is not initialized"), nil
			}
			return toolinput.Partial(in, func(a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // single mode: top-level text/type
			}) (actool.Result, error) {
				items, batch := pickKnowledgeItems(a.Constraints, a.constraintItem)
				writtenBatch := writeKnowledgeItems(items, t.addOneConstraint, nil)
				return writtenBatch.result(batch, "constraint added")

			})
		})
}
