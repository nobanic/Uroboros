package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/db"
)

type taskMetadataInput = struct {
	Name   *string `json:"name"`
	Pinned *bool   `json:"pinned"`
}

func (c taskCommand) metadata() {
	task := c.task()
	if task == nil {
		return
	}
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, maxTaskMetadataRequestSize)
	var input taskMetadataInput
	if err := decode(c.r, &input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.failure(413, "request body too large")
		} else {
			c.failure(400, err.Error())
		}
		return
	}
	if input.Name == nil && input.Pinned == nil {
		c.failure(400, "must provide at least name or pinned")
		return
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			c.failure(400, "task name cannot be empty")
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			c.failure(400, fmt.Sprintf("task name must be at most %d characters", maxTaskNameRunes))
			return
		}
		input.Name = &name
	}
	updated, err := c.server.m.UpdateTaskMetadata(c.r.PathValue("id"), db.TaskPatch{Name: input.Name, Pinned: input.Pinned})
	if !c.check(err, 500) {
		return
	}
	if updated == nil {
		c.failure(404, "task not found")
		return
	}
	c.reply(200, taskDTO(updated, c.server.resolvedTaskStatus(updated)))
}

type taskProfileInput = struct {
	LLMProfileIDs      []int64 `json:"llm_profile_ids"`
	ActiveLLMProfileID *int64  `json:"active_llm_profile_id"`
}

func (c taskCommand) profiles() {
	task := c.task()
	if task == nil {
		return
	}
	before := task.llmStateSnapshot()
	var input taskProfileInput
	if !c.bind(&input, "bad json: ") || !c.check(c.server.validateTaskProfileIDs(input.LLMProfileIDs), 400) {
		return
	}
	active := int64(0)
	if input.ActiveLLMProfileID != nil {
		active = *input.ActiveLLMProfileID
	}
	reopened, err := c.server.m.ReplaceTaskLLMProfiles(task.ID, input.LLMProfileIDs, active)
	if !c.check(err, 400) {
		return
	}
	// Existing in-flight calls keep their provider. The original concurrency
	// reconciliation and switch-event owner handle subsequent calls and views.
	task.Notify()
	go c.server.reconcileConcurrency()
	after := task.llmStateSnapshot()
	result := map[string]any{"id": task.ID, "llm_profile_ids": after.ProfileIDs, "active_llm_profile_id": after.ActiveID, "llm_failover_state": after.FailoverState, "reopened_intents": reopened}
	if !sameOptionalID(before.ActiveID, after.ActiveID) {
		event := c.server.emitManualTaskLLMSwitch(task, before.ActiveID, after.ActiveID)
		result["switch_event"] = activityDTO(event)
	}
	c.reply(200, result)
}

type taskScopeInput = struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

func (c taskCommand) scope(remove bool) {
	task := c.task()
	if task == nil {
		return
	}
	assets := c.server.m.Assets()
	if assets == nil {
		c.failure(503, "asset store is not enabled")
		return
	}
	if !remove {
		var input taskScopeInput
		if err := json.NewDecoder(c.r.Body).Decode(&input); err != nil {
			c.failure(400, "invalid JSON")
			return
		}
		taskID, _ := strconv.ParseInt(task.ID, 10, 64)
		row, err := assets.AddAgentScope(taskID, input.Kind, input.Value, input.Reason, "manual")
		if c.check(err, 400) {
			c.reply(200, row)
		}
		return
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	id, err := strconv.ParseInt(c.r.PathValue("sid"), 10, 64)
	if err != nil {
		c.failure(400, "invalid scope id")
		return
	}
	deleted, err := assets.DeleteTaskScope(taskID, id)
	if !c.check(err, 500) {
		return
	}
	if !deleted {
		c.failure(404, "scope row not found")
		return
	}
	c.reply(200, map[string]bool{"ok": true})
}
