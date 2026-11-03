package server

import (
	pgdb "github.com/nobanic/UroborOS/db"
	"strconv"
)

// taskCategoryCommands owns category-wide updates while retaining each original
// read-lock lifetime and canonical versus raw task-ID selection policy.
type taskCategoryCommands struct{ manager *Manager }

func (command taskStateCommand) metadata(patch pgdb.TaskPatch) (*Task, error) {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	id, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil || id <= 0 {
		return nil, nil
	}
	updated, err := command.manager.pg.UpdateTask(id, patch)
	if err != nil || updated == nil {
		return nil, err
	}
	command.manager.mu.RLock()
	task := command.manager.tasks[command.id]
	command.manager.mu.RUnlock()
	if task == nil {
		return nil, nil
	}
	task.updateLifecycle(func(state *taskLifecycleState) {
		state.Name = updated.Name
		state.PinnedAt = unixOrZero(updated.PinnedAt)
	})
	return task, nil
}

func (command taskCategoryCommands) rename(id int64, name string) (*pgdb.TaskCategory, error) {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	category, err := command.manager.pg.RenameTaskCategory(id, name)
	if err != nil {
		return nil, err
	}
	command.manager.mu.RLock()
	defer command.manager.mu.RUnlock()
	for _, task := range command.manager.tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			if state.CategoryID != nil && *state.CategoryID == id {
				state.CategoryName = category.Name
			}
		})
	}
	return category, nil
}

func (command taskCategoryCommands) remove(id int64) (bool, error) {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	deleted, err := command.manager.pg.DeleteTaskCategory(id)
	if err != nil || !deleted {
		return deleted, err
	}
	command.manager.mu.RLock()
	defer command.manager.mu.RUnlock()
	for _, task := range command.manager.tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			if state.CategoryID != nil && *state.CategoryID == id {
				state.CategoryID = nil
				state.CategoryName = ""
			}
		})
	}
	return true, nil
}

func (command taskStateCommand) category(categoryID *int64) (*pgdb.TaskCategory, error) {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	id, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil || id <= 0 {
		return nil, pgdb.ErrTaskCategoryTaskNotFound
	}
	category, err := command.manager.pg.SetTaskCategory(id, categoryID)
	if err != nil {
		return nil, err
	}
	command.manager.mu.RLock()
	task := command.manager.tasks[command.id]
	command.manager.mu.RUnlock()
	if task != nil {
		task.updateLifecycle(func(state *taskLifecycleState) {
			state.CategoryID = cloneInt64Ptr(categoryID)
			state.CategoryName = ""
			if category != nil {
				state.CategoryName = category.Name
			}
		})
	}
	return category, nil
}

func (command taskCategoryCommands) assign(taskIDs []string, categoryID *int64) (map[string]bool, *pgdb.TaskCategory, error) {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	numeric := make([]int64, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		id, err := strconv.ParseInt(taskID, 10, 64)
		if err != nil || id <= 0 {
			return nil, nil, pgdb.ErrTaskCategoryTaskNotFound
		}
		numeric = append(numeric, id)
	}
	updatedIDs, category, err := command.manager.pg.SetTasksCategory(numeric, categoryID)
	if err != nil {
		return nil, nil, err
	}
	categoryName := ""
	if category != nil {
		categoryName = category.Name
	}
	updated := make(map[string]bool, len(updatedIDs))
	command.manager.mu.RLock()
	tasks := make([]*Task, 0, len(updatedIDs))
	for _, id := range updatedIDs {
		taskID := strconv.FormatInt(id, 10)
		updated[taskID] = true
		if task := command.manager.tasks[taskID]; task != nil {
			tasks = append(tasks, task)
		}
	}
	command.manager.mu.RUnlock()
	for _, task := range tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			state.CategoryID = cloneInt64Ptr(categoryID)
			state.CategoryName = categoryName
		})
	}
	return updated, category, nil
}

func (command taskStateCommand) profiles(profileIDs []int64, activeProfileID int64) (int64, error) {
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return 0, err
	}
	if err := command.manager.pg.ReplaceTaskLLMProfiles(n, profileIDs, activeProfileID); err != nil {
		return 0, err
	}
	pt, err := command.manager.pg.GetTask(n)
	if err != nil || pt == nil {
		return 0, err
	}
	command.manager.mu.Lock()
	if task := command.manager.tasks[command.id]; task != nil {
		task.setLLMState(pt.LLMProfileID, pt.ActiveLLMProfileID, pt.LLMProfileIDs, pt.LLMChainRevision, pt.LLMFailoverState, pt.LLMFailoverReason)
	}
	command.manager.mu.Unlock()
	// A terminal task does not reopen quota-blocked intents: the task has no worker running, so reopening would only move them from
	// blocked to open — where nobody executes them, and they no longer meet the "rerun intent" re-runnable condition,
	// turning into dead state instead. To keep a terminal task going, use rerun-intent / add-goal, which re-admits
	// the task back to running state.
	if pgdb.IsTerminal(pt.Status) {
		return 0, nil
	}
	if task, ok := command.manager.Task(command.id); ok {
		reopened, reopenErr := task.Store.ReopenIntentsByBlockedReason(pgdb.IntentBlockedLLMQuota)
		if reopenErr != nil {
			return reopened, reopenErr
		}
		return reopened, nil
	}
	return 0, nil
}
