package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/internal/toolinput"
	"github.com/nobanic/UroborOS/mission"
	actool "github.com/nobanic/norma/tool"
	"strings"
)

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal attaches one goal node (open) to the exploration graph: linked to the task root (origin fact, rel spawns).
// origin takes t.worker (default system): entries written by the goals decomposer record "goals", and at main-agent runtime record
// "human". Waking the planner is done uniformly by setGoals after the whole batch is written (see below); this only persists.
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text cannot be empty")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals adds exploration goals (goal nodes) to [this task]. It is both the goals decomposer's submit tool and the main
// agent's tool for adding goals at runtime — the same managed tool, whose description/schema can be edited in the web UI and bound per agent.
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"Add exploration goals (goal) to [this task]. A goal = a final deliverable/verifiable result, not an attack step or a reconnaissance action.\n"+
			"★Prefer batching: put multiple goals into the goals array and submit at once; returns ids of equal length and order (failed entries id=0, details in errors). For a single one, omit goals and give the top-level text directly.\n"+
			"vulnclass optional: the corresponding vulnerability class (e.g. SQLi/IDOR); leave empty for business-logic goals. Whether a goal is met is judged and marked met by the system; this tool only adds.",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "[Prefer this] array of goals to add, processed in order. Each element: text (required, one independent verifiable final goal) + vulnclass (optional). Returned ids match this array in length and order.", "items": map[string]any{"type": "object"}},
			"text":      str("[single] one independent verifiable final goal"),
			"vulnclass": str("[single] the corresponding vulnerability class (if clear), e.g. SQLi/IDOR; may be left empty for business-logic goals"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals is not enabled: ExplorationStore is not initialized"), nil
			}
			return toolinput.Partial(in, func(a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // single mode: top-level text/vulnclass
			}) (actool.Result, error) {
				items, batch := pickKnowledgeItems(a.Goals, a.goalItem)

				var addedTexts []string
				writtenBatch := writeKnowledgeItems(items, t.addOneGoal, func(it goalItem) { addedTexts = append(addedTexts, strings.TrimSpace(it.Text)) })
				if len(addedTexts) > 0 {
					// Wake the planner (once per batch). Prefer notifyGoal: one set_goals records one 'someone added
					// N goals: …' trigger, with no per-entry spam; the decomposer/worker has no such callback → fall back to plain notify (the decomposer's
					// round-0 has not even wired notify, i.e. a no-op, since the planner has not started yet).
					switch {
					case t.notifyGoal != nil:
						t.notifyGoal(addedTexts)
					case t.notify != nil:
						t.notify()
					}
					// Adding a goal at main-agent runtime → pull a completed/paused task back to running (the terminal gate would
					// swallow a plain notify, so it must be explicitly revived). Only mainagent wires this callback; the decomposer/worker is nil.
					if t.resumeTask != nil {
						t.resumeTask()
					}
				}

				return writtenBatch.result(batch, "goal added")

			})
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "Link a fact/finding to a goal atomically. Explain how the evidence satisfies the goal. Completion is derived from persisted graph state.",
		obj(map[string]any{"goal_id": idp("goal node id"), "evidence_id": idp("supporting fact/finding node id"), "reason": str("how the cited evidence satisfies the goal")}, "goal_id", "evidence_id", "reason"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			var request struct {
				Goal     json.RawMessage `json:"goal_id"`
				Evidence json.RawMessage `json:"evidence_id"`
				Reason   string          `json:"reason"`
			}
			if err := json.Unmarshal(input, &request); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid proof: %v", err)), nil
			}
			state, err := (mission.CompletionService{Repository: t.ts}).Prove(ctx, mission.Proof{GoalID: pid(request.Goal), EvidenceID: pid(request.Evidence), Reason: request.Reason})
			if err != nil {
				return actool.Errorf(fmt.Sprintf("goal proof was not committed: %v", err)), nil
			}
			t.GoalMet = state.Ready()
			if t.GoalMet {
				t.Reason = fmt.Sprintf("All %d goals have persisted proof links", state.Total)
			}
			if t.notify != nil {
				t.notify()
			}
			return actool.Text(fmt.Sprintf("goal %d proved; %d/%d goals proved", pid(request.Goal), state.Proved, state.Total)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "Request completion after every goal has been proved. This operation checks persisted graph state and cannot bypass prove_goal.", obj(map[string]any{"reason": str("completion explanation")}, "reason"), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf("task context is required"), nil
		}
		state, err := (mission.CompletionService{Repository: t.ts}).Finish(ctx)
		if err != nil {
			return actool.Errorf(fmt.Sprintf("task completion rejected: %v", err)), nil
		}
		t.GoalMet = true
		t.Reason = fmt.Sprintf("All %d goals have persisted proof links", state.Total)
		return actool.Text(t.Reason), nil
	})
}

// --- worker write tools ---
