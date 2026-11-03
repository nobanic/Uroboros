package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

// A rerun retains the original snapshots until admission commits. A failed
// admission restores intent state through the unchanged rollback helper.
type taskIntentRerun struct {
	task   *Task
	id     *int64
	before []*db.Node
	count  int64
}

func (retry *taskIntentRerun) reopen(c taskCommand) bool {
	if retry.id != nil {
		node, err := retry.task.Store.GetNode(*retry.id)
		if !c.check(err, 500) {
			return false
		}
		retry.before = []*db.Node{node}
		changed, err := retry.task.Store.ReopenIntent(*retry.id)
		if !c.check(err, 500) {
			return false
		}
		if !changed {
			c.failure(409, "this intent is not in a rerunnable state (only blocked/exhausted/stopped can be rerun)")
			return false
		}
		retry.count = 1
		return true
	}
	nodes, err := retry.task.Store.ListByKind(db.KindIntent, 1000000)
	if !c.check(err, 500) {
		return false
	}
	retry.before = make([]*db.Node, 0)
	for _, node := range nodes {
		if node.State == "blocked" {
			snapshot := *node
			retry.before = append(retry.before, &snapshot)
		}
	}
	retry.count, err = retry.task.Store.ReopenBlockedIntents()
	return c.check(err, 500)
}

func (retry taskIntentRerun) restore(cause error) error {
	if retry.id != nil {
		if err := restoreRerunIntent(retry.task, retry.before[0]); err != nil {
			return fmt.Errorf("%w; restore intent %d after admission failure: %v", cause, *retry.id, err)
		}
		return cause
	}
	errors := make([]string, 0)
	for _, node := range retry.before {
		if err := restoreRerunIntent(retry.task, node); err != nil {
			errors = append(errors, fmt.Sprintf("intent %d: %v", node.ID, err))
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%w; restore blocked intents after admission failure: %s", cause, strings.Join(errors, "; "))
	}
	return cause
}

func (retry taskIntentRerun) reopened() int64 {
	if retry.id != nil {
		return *retry.id
	}
	return retry.count
}

func (c taskCommand) rerun(single bool) {
	task := c.task()
	if task == nil {
		return
	}
	retry := taskIntentRerun{task: task}
	// Single rerun parses before reserving; intent control reserves before
	// parsing. The different error priority is part of the existing contract.
	if single {
		id, valid := c.intentID(false)
		if !valid {
			return
		}
		retry.id = &id
	}
	release := c.reserve(task, "task is being deleted; intents cannot be rerun")
	if release == nil {
		return
	}
	defer release()
	if !retry.reopen(c) {
		return
	}
	queued := false
	if retry.count > 0 {
		var err error
		queued, err = c.server.admitTask(task, "resume")
		if err != nil {
			c.failure(500, retry.restore(err).Error())
			return
		}
		if single {
			log.Printf("[task] #%s intent #%d reopened (rerun)", task.ID, *retry.id)
		} else {
			log.Printf("[task] #%s bulk-reopened %d blocked intents", task.ID, retry.count)
		}
	}
	c.reply(200, map[string]any{"id": task.ID, "reopened": retry.reopened(), "queued": queued})
}

type batchTaskInput = struct {
	TaskIDs []string `json:"task_ids"`
	Action  string   `json:"action"`
}

func (c taskCommand) batchItem(parsed parsedTaskID, action string) batchControlItem {
	item := batchControlItem{ID: parsed.id}
	if !parsed.valid {
		item.Error = "bad task id"
		return item
	}
	task, found := c.server.m.Task(parsed.id)
	if !found {
		item.Error = "task not found"
		return item
	}
	result, err := c.server.applyTaskControl(task, action)
	if err != nil {
		item.Error = err.Error()
	} else {
		item.OK, item.Status, item.Queued = true, result.Status, result.Queued
	}
	return item
}

func (c taskCommand) batch() {
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, 256<<10)
	var input batchTaskInput
	if !c.bind(&input, "bad json: ") || !c.action(input.Action) {
		return
	}
	ids := normalizeBatchTaskIDs(input.TaskIDs)
	if len(ids) == 0 || len(ids) > maxBatchControlIDs {
		c.failure(400, fmt.Sprintf("the number of task_ids must be 1-%d", maxBatchControlIDs))
		return
	}
	items := make([]batchControlItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, c.batchItem(id, input.Action))
	}
	c.reply(200, map[string]any{"items": items})
}
