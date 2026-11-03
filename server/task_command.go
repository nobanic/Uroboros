package server

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Command endpoints borrow their request/writer and the existing server. They
// describe input and HTTP outcomes; lifecycle authority stays in the engine.
type taskCommand struct {
	server *Server
	w      http.ResponseWriter
	r      *http.Request
}

func (s *Server) taskCommand(w http.ResponseWriter, r *http.Request) taskCommand {
	return taskCommand{server: s, w: w, r: r}
}
func (c taskCommand) failure(status int, message string) { writeErr(c.w, status, message) }
func (c taskCommand) reply(status int, value any)        { writeJSON(c.w, status, value) }
func (c taskCommand) check(err error, status int) bool {
	if err == nil {
		return true
	}
	c.failure(status, err.Error())
	return false
}
func (c taskCommand) bind(value any, prefix string) bool {
	if err := json.NewDecoder(c.r.Body).Decode(value); err != nil {
		c.failure(400, prefix+err.Error())
		return false
	}
	return true
}
func (c taskCommand) task() *Task {
	task, found := c.server.m.Task(c.r.PathValue("id"))
	if !found {
		c.failure(404, "task not found")
		return nil
	}
	return task
}
func (c taskCommand) action(action string) bool {
	if action == "pause" || action == "resume" {
		return true
	}
	c.failure(400, "action must be pause|resume")
	return false
}
func (c taskCommand) intentID(positive bool) (int64, bool) {
	id, err := strconv.ParseInt(c.r.PathValue("iid"), 10, 64)
	if err != nil || (positive && id <= 0) {
		c.failure(400, "bad intent id")
		return 0, false
	}
	return id, true
}
func (c taskCommand) reserve(task *Task, message string) func() {
	if !c.server.engine.beginTaskOperation(task.ID) {
		c.failure(409, message)
		return nil
	}
	return func() { c.server.engine.decInflight(task.ID) }
}

// Aliases preserve the original anonymous encoding/json field/type errors.
type taskActionInput = struct {
	Action string `json:"action"`
}
type intentActionInput = struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Mode   string `json:"mode"`
}

func (c taskCommand) active() {
	var input struct {
		ID string `json:"id"`
	}
	if !c.bind(&input, "") {
		return
	}
	if !c.server.m.SetActive(input.ID) {
		c.failure(404, "task not found")
		return
	}
	if task, found := c.server.m.Task(input.ID); found && !task.lifecycleSnapshot().Queued {
		c.server.engine.Run(c.server.ctx, task)
	}
	c.reply(200, map[string]any{"active": input.ID})
}
func (c taskCommand) control() {
	task := c.task()
	if task == nil {
		return
	}
	var input taskActionInput
	if !c.bind(&input, "") || !c.action(input.Action) {
		return
	}
	result, err := c.server.applyTaskControl(task, input.Action)
	if c.check(err, 409) {
		c.reply(200, result)
	}
}
func (c taskCommand) intent() {
	task := c.task()
	if task == nil {
		return
	}
	release := c.reserve(task, "task is being deleted; intents cannot be controlled")
	if release == nil {
		return
	}
	defer release()
	id, valid := c.intentID(true)
	if !valid {
		return
	}
	var input intentActionInput
	if !c.bind(&input, "bad json: ") {
		return
	}
	if input.Action != "pause" && input.Action != "resume" && input.Action != "cancel" {
		c.failure(400, "action must be pause|resume|cancel")
		return
	}
	result, err := c.server.applyIntentControl(c.r.Context(), task, id, input.Action, input.Reason, input.Mode)
	if c.check(err, 409) {
		c.reply(200, result)
	}
}
