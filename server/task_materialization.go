package server

import (
	"log"
	"strconv"

	pgdb "github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/guard"
	"github.com/nobanic/UroborOS/intercept"
)

// taskMaterialization builds an unpublished runtime handle. Persisted metadata,
// lifecycle and provider context are projected before the handle enters the
// manager registry; subsequent changes use the existing lifecycle/LLM locks.
type taskMaterialization struct {
	row         *pgdb.Task
	store       *pgdb.ExplorationStore
	interceptor *intercept.Interceptor
}

func (projection taskMaterialization) task() *Task {
	row := projection.row
	runtime := &Task{ID: strconv.FormatInt(row.ID, 10), ExpID: row.ExplorationID, Store: projection.store}
	projection.describe(runtime)
	projection.restoreLifecycle(runtime)
	projection.restoreProviders(runtime)
	runtime.SourceTaskIDs = append([]int64(nil), row.SourceTaskIDs...)
	runtime.CompanyIDs = append([]int64(nil), row.CompanyIDs...)
	runtime.CoverageEnabled, runtime.StrictIsolation = row.CoverageEnabled, row.StrictIsolation
	runtime.SkipInitialKatana = row.SkipInitialKatana
	if row.InitialScout != nil {
		choice := *row.InitialScout
		runtime.InitialScout = &choice
	}
	runtime.Guard = guard.NewWithInterceptor(projection.interceptor)
	runtime.notify = make(chan struct{}, 1)
	return runtime
}

func (projection taskMaterialization) describe(runtime *Task) {
	row := projection.row
	runtime.Name, runtime.Description, runtime.Goal = row.Name, row.Description, row.Goal
	runtime.CategoryID = cloneInt64Ptr(row.CategoryID)
	runtime.CategoryName = row.CategoryName
	runtime.PinnedAt = unixOrZero(row.PinnedAt)
	runtime.CreatedAt = row.CreatedAt.Unix()
	runtime.ParentRef = row.ParentRef
}

func (projection taskMaterialization) restoreLifecycle(runtime *Task) {
	row := projection.row
	runtime.Paused, runtime.Queued, runtime.Status = row.Paused, row.Queued, row.Status
	runtime.QueuedAt = unixNanoOrZero(row.QueuedAt)
	runtime.QueueMode = row.QueueMode
	runtime.CompletedAt = unixOrZero(row.CompletedAt)
	runtime.TimeoutSeconds, runtime.PlanHeartbeatSeconds = row.TimeoutSeconds, row.PlanHeartbeatSeconds
	runtime.FirstRunAt, runtime.DeadlineAt = unixOrZero(row.FirstRunAt), unixOrZero(row.DeadlineAt)
}

func (projection taskMaterialization) restoreProviders(runtime *Task) {
	row := projection.row
	// Profile pointers keep the original aliases; category/slices above are copies.
	runtime.LLMProfileID, runtime.ActiveLLMProfileID = row.LLMProfileID, row.ActiveLLMProfileID
	runtime.LLMProfileIDs = append([]int64(nil), row.LLMProfileIDs...)
	runtime.LLMChainRevision = row.LLMChainRevision
	runtime.LLMFailoverState, runtime.LLMFailoverReason = row.LLMFailoverState, row.LLMFailoverReason
}

type taskRegistryReload struct{ manager *Manager }

func (reload taskRegistryReload) load() []*Task {
	rows, err := reload.manager.pg.ListTasks()
	if err != nil {
		log.Printf("[manager] reload: %v", err)
		return nil
	}
	reload.manager.mu.Lock()
	defer reload.manager.mu.Unlock()
	added := reload.install(rows)
	reload.selectInitialActive()
	if len(added) > 0 {
		log.Printf("[manager] reloaded %d task(s) from PG", len(added))
	}
	return added
}

func (reload taskRegistryReload) install(rows []*pgdb.Task) []*Task {
	var added []*Task
	for _, row := range rows {
		id := strconv.FormatInt(row.ID, 10)
		if _, exists := reload.manager.tasks[id]; exists {
			continue
		}
		runtime := taskFromPG(row, reload.manager.pg.Exploration(row.ExplorationID), reload.manager.interceptor)
		reload.manager.tasks[id] = runtime
		added = append(added, runtime)
	}
	return added
}

func (reload taskRegistryReload) selectInitialActive() {
	if reload.manager.active != "" {
		return
	}
	var newest *Task
	for _, runtime := range reload.manager.tasks {
		if newest == nil || runtime.CreatedAt > newest.CreatedAt {
			newest = runtime
		}
	}
	if newest != nil {
		reload.manager.active = newest.ID
	}
}
