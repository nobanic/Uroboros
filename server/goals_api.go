package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

// the overview's manual CRUD endpoints for 'goal management'. They write the same batch of goal nodes as the agent-side set_goals tool,,
// but the entry point is a human directly adding/deleting/editing in the UI; after add/edit it reuses the 'revive task' logic (admitTask resume:(admitTask resume:
// terminal -> running, unpause, queue if needed); delete does not revive (per product decision). Every mutating handler goes through
// beginTaskOperation/decInflight,, avoiding a race with task deletion (consistent with intent CRUD).

// listGoals returns all goals of this task (text/vulnclass/state split out) for rendering the goal-management card.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal manually adds a goal: persist (attach under the task root's spawns) -> record a 'goal added' trigger to wake
// planner → the planner -> revive the task so the planner re-judges completion against the new goal.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Task is being deleted; cannot add a goal")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Goal content cannot be empty")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // record 'a human added a goal: ...', trigger and wake the planner planner
	s.reviveTask(t)              // pull a completed/paused task back to the running state to continue
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "failed to read back the goal after writing")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal manually edits a goal's text (and vulnclass): update the DB -> record 'the user changed the goal from old to new'
// trigger and wake the planner -> revive the task so the planner adjusts direction per the new goal.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Task is being deleted; cannot edit a goal")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Goal content cannot be empty")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "Goal does not exist")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // record 'a human changed the goal from old to new', trigger and wake the planner planner
	s.reviveTask(t)                   // same as add: revive the task and re-judge against the new goal
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "failed to read back the goal after updating")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal manually deletes a goal (hard delete, cascade-removing edges/anchors): delete from DB -> record 'the user deleted goal X'
// trigger and wake the planner to re-judge the remaining goals accordingly. Per product decision, delete does [not] revive the task.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Task is being deleted; cannot delete a goal")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "Goal does not exist")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // record 'a human deleted goal: ...', trigger and wake the planner (does not revive the task))
	writeJSON(w, 200, map[string]bool{"ok": true})
}
