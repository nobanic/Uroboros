package server

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

// Creation input is normalized and checked in its original order before the
// manager is called. Persistent creation, seeding and admission stay separate.
func (c taskCommand) creationOptions(input *createTaskReq) (db.TaskCreateOptions, error) {
	var empty db.TaskCreateOptions
	if strings.TrimSpace(input.Description) == "" {
		input.Description = "Unnamed task"
	}
	if len(input.LLMProfileIDs) == 0 && input.LLMProfileID != nil {
		input.LLMProfileIDs = []int64{*input.LLMProfileID}
	}
	if err := c.server.validateTaskProfileIDs(input.LLMProfileIDs); err != nil {
		return empty, err
	}
	if input.TimeoutSeconds < 0 {
		input.TimeoutSeconds = 0
	}
	if len(input.SourceTaskIDs) > db.MaxTaskSourceCount {
		return empty, fmt.Errorf("you can associate at most %d tasks", db.MaxTaskSourceCount)
	}
	if input.StrictIsolation && len(input.SourceTaskIDs) > 0 {
		return empty, fmt.Errorf("strict_isolation cannot be combined with source_task_ids")
	}
	sources := make([]int64, 0, len(input.SourceTaskIDs))
	seen := map[int64]bool{}
	for _, raw := range input.SourceTaskIDs {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 || seen[id] {
			return empty, fmt.Errorf("associated task id is invalid or duplicated")
		}
		if _, found := c.server.m.Task(strconv.FormatInt(id, 10)); !found {
			return empty, fmt.Errorf("associated task #%d does not exist", id)
		}
		seen[id] = true
		sources = append(sources, id)
	}
	companies, err := db.NormalizeTaskCompanyIDs(input.CompanyIDs)
	if err != nil {
		return empty, fmt.Errorf("associated company invalid: select at most %d valid companies", db.MaxTaskCompanyCount)
	}
	input.CompanyIDs = companies
	rules, err := buildTaskInterceptRules(input.InterceptRules)
	if err != nil {
		return empty, fmt.Errorf("task-level interception rule invalid: %s", err.Error())
	}
	return db.TaskCreateOptions{
		Name: strings.TrimSpace(input.Name), CategoryID: input.CategoryID,
		SourceTaskIDs: sources, CompanyIDs: input.CompanyIDs, LLMProfileIDs: input.LLMProfileIDs,
		TimeoutSeconds: input.TimeoutSeconds, PlanHeartbeatSeconds: input.PlanHeartbeatSeconds,
		CoverageEnabled: input.CoverageEnabled, StrictIsolation: input.StrictIsolation, InterceptRules: rules,
		SkipInitialKatana: input.SkipInitialKatana, InitialScout: input.InitialScout,
	}, nil
}

func (c taskCommand) create() {
	var input createTaskReq
	if !c.bind(&input, "bad json: ") {
		return
	}
	options, err := c.creationOptions(&input)
	if !c.check(err, 400) {
		return
	}
	task, err := c.server.m.CreateTaskWithOptions(input.Description, input.Goal, options)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrTaskCategoryInvalid) || errors.Is(err, db.ErrTaskCategoryNotFound):
			c.failure(400, "task category does not exist or is invalid")
		case errors.Is(err, db.ErrTaskCompanyIDsInvalid) || errors.Is(err, db.ErrTaskCompanyNotFound):
			c.failure(400, "associated company does not exist or is invalid")
		default:
			c.failure(500, err.Error())
		}
		return
	}
	log.Printf("[task] created task #%s «%s» goal: %s", task.ID, input.Description, input.Goal)
	seedText := input.Description + " " + input.Goal
	firstIntent := input.SeedFirstIntent != nil && *input.SeedFirstIntent
	if input.DeferStart != nil && *input.DeferStart {
		c.server.seed(task, seedText)
		if firstIntent {
			c.server.seedFirstIntent(task)
		}
		c.server.engine.Pause(task.ID, agent.Causef("task_start_deferred", "task start deferred for operator preflight", "the task has not started; resume it after its execution prerequisites are verified"))
		if err := c.server.m.ApplyTaskPause(task.ID); err != nil {
			c.server.engine.Resume(task)
			c.failure(500, "defer task start: "+err.Error())
			return
		}
		log.Printf("[task] #%s seeded and paused for preflight", task.ID)
	} else {
		c.server.launchTask(task, seedText, firstIntent)
	}
	c.reply(201, taskDTO(task, c.server.resolvedTaskStatus(task)))
}
