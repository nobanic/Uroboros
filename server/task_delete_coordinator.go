package server

import (
	"errors"
	"fmt"
	pgdb "github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
	"strconv"
)

// External stages remain owned until the original database commit boundary.
type taskDeletion struct {
	manager *Manager
	id      string
	options DeleteTaskOptions
}
type taskDeleteWork struct {
	command      taskDeletion
	numericID    int64
	registered   *pgdb.Task
	result       DeleteTaskResult
	fileStage    *taskFileDeleteStage
	trafficStage *traffic.HostDeleteStage
}
type taskDeleteRollback struct {
	cause   error
	traffic *traffic.HostDeleteStage
	files   *taskFileDeleteStage
}
type taskDeleteRelease struct {
	manager   *Manager
	id        string
	numericID int64
}

func (command taskDeletion) run() (DeleteTaskResult, error) {
	result := DeleteTaskResult{Deleted: command.id}
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return result, err
	}
	registered, err := command.manager.pg.GetTask(n)
	if err != nil {
		return result, err
	}
	if registered == nil {
		command.manager.forgetTask(command.id, n)
		return result, nil
	}
	work := taskDeleteWork{command: command, numericID: n, registered: registered, result: result}
	if err := work.files(); err != nil {
		return work.result, err
	}
	dbResult, err := work.database()
	if err != nil {
		return work.result, rollbackTaskDelete(err, work.trafficStage, work.fileStage)
	}
	err = work.complete(dbResult)
	return work.result, err
}
func (work *taskDeleteWork) files() error {
	var err error
	if work.command.options.DeleteFiles {
		work.fileStage, err = stageTaskFiles(work.command.manager.dir, work.command.id, work.registered.ExplorationID)
		if err != nil {
			return err
		}
		work.result.FilesDeleted = work.fileStage.deleted
	}
	return nil
}
func (work *taskDeleteWork) database() (pgdb.TaskDeleteResult, error) {
	var err error
	var prepare func(pgdb.TaskDeletePreparation) error
	if work.command.options.DeleteTraffic && work.command.manager.traffic != nil {
		prepare = func(p pgdb.TaskDeletePreparation) error {
			if len(p.TrafficHosts) == 0 {
				return nil
			}
			work.trafficStage, err = work.command.manager.traffic.StageDeleteHostsExact(p.TrafficHosts)
			if err != nil {
				return err
			}
			work.result.TrafficDeleted = work.trafficStage.Deleted()
			return nil
		}
	}
	return work.command.manager.pg.DeleteTaskCascadePrepared(
		work.numericID, work.command.options.DeleteAssets, work.command.options.DeleteFindings, work.command.options.DeleteLLMRecords, prepare,
	)
}
func (work *taskDeleteWork) complete(dbResult pgdb.TaskDeleteResult) error {
	work.result.AssetsDeleted = dbResult.AssetsDeleted
	work.result.AssetsDetached = dbResult.AssetsDetached
	work.result.FindingsDeleted = dbResult.FindingsDeleted
	work.result.LLMRecordsDeleted = dbResult.LLMRecordsDeleted

	// PostgreSQL is now authoritative: finalize the staged external deletion and
	// forget the live task even if a final purge reports an error. Such errors are
	// typed so the HTTP layer can still tear down the task runtime instead of
	// incorrectly reviving a task whose database row is already gone.
	var finalizeErrs []error
	if work.trafficStage != nil {
		if err := work.trafficStage.Commit(); err != nil {
			finalizeErrs = append(finalizeErrs, fmt.Errorf("finalize traffic deletion: %w", err))
		}
	}
	if work.fileStage != nil {
		if err := work.fileStage.commit(); err != nil {
			finalizeErrs = append(finalizeErrs, fmt.Errorf("finalize task file deletion: %w", err))
		}
	}
	work.command.manager.forgetTask(work.command.id, work.numericID)
	if err := errors.Join(finalizeErrs...); err != nil {
		return &taskDeleteCommittedError{err: err}
	}
	return nil
}

func (rollback taskDeleteRollback) run() error {
	errs := []error{rollback.cause}
	// Reverse the preparation order. Both restorations are attempted even if the
	// first one fails, and errors.Join preserves the original PostgreSQL error.
	if rollback.traffic != nil {
		if err := rollback.traffic.Rollback(); err != nil {
			errs = append(errs, fmt.Errorf("restore traffic after task delete failure: %w", err))
		}
	}
	if rollback.files != nil {
		if err := rollback.files.rollback(); err != nil {
			errs = append(errs, fmt.Errorf("restore task files after task delete failure: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (release taskDeleteRelease) run() {
	release.manager.mu.Lock()
	delete(release.manager.tasks, release.id)
	for _, task := range release.manager.tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			kept := make([]int64, 0, len(state.SourceTaskIDs))
			for _, sourceID := range state.SourceTaskIDs {
				if sourceID != release.numericID {
					kept = append(kept, sourceID)
				}
			}
			state.SourceTaskIDs = kept
		})
	}
	if release.manager.active == release.id {
		release.manager.active = ""
		for _, t := range release.manager.tasks {
			release.manager.active = t.ID
			break
		}
	}
	release.manager.mu.Unlock()
}
