package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nobanic/UroborOS/agent"
	pgdb "github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
)

// archiveWriteJob owns only one job's hot-to-cold resource lifetime. Defers
// remain registered at the original acquisition points, retaining traffic ->
// package -> files -> drain context -> task barrier failure cleanup order.
type archiveWriteJob struct {
	server        *Server
	job           *pgdb.TaskArchive
	taskID        string
	committed     bool
	files         *taskArchiveFileStage
	traffic       *traffic.HostDeleteStage
	packagePath   string
	removePackage bool
}

func (operation *archiveWriteJob) run() (runErr error) {
	s, job := operation.server, operation.job
	operation.taskID = strconv.FormatInt(job.TaskID, 10)
	if !s.beginTaskDelete(operation.taskID) {
		return errors.New("task already has another archive or delete operation in progress")
	}
	defer func() {
		if !operation.committed {
			s.abortTaskDelete(operation.taskID)
		}
	}()
	s.cancelTaskChat(operation.taskID, agent.AbortTaskDeleted)
	drainCtx, cancel := context.WithTimeout(s.ctx, taskDeleteDrainTimeout)
	defer cancel()
	if err := operation.drain(drainCtx); err != nil {
		return err
	}
	task, err := s.m.pg.GetTask(job.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return pgdb.ErrTaskArchiveNotFound
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "stage_files", 10)
	operation.files, err = stageTaskArchiveFiles(s.m.dir, job.ID, operation.taskID, task.ExplorationID)
	if err != nil {
		return err
	}
	defer func() {
		if !operation.committed {
			runErr = errors.Join(runErr, operation.files.rollback())
		}
	}()
	snapshot, err := operation.snapshot(task.ExplorationID)
	if err != nil {
		return err
	}
	if s.m.traffic != nil && len(snapshot.Hosts) > 0 {
		_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "snapshot_traffic", 38)
		count, err := s.m.traffic.ExportHosts(snapshot.Hosts, filepath.Join(operation.files.payload, "traffic"))
		if err != nil {
			return err
		}
		snapshot.DataCounts["traffic"] = count
	}
	operation.packagePath = taskArchivePath(s.m.dir, job.ID, operation.taskID)
	evidence, err := pgdb.ArchiveEvidenceSnapshots(snapshot)
	if err != nil {
		return err
	}
	if err := s.evidenceStore().CopySnapshots(s.ctx, evidence, filepath.Join(operation.files.payload, "evidence")); err != nil {
		return err
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "compress", 50)
	original, size, checksum, err := writeTaskArchivePackage(operation.packagePath, operation.files.payload, snapshot)
	if err != nil {
		return err
	}
	operation.removePackage = true
	defer func() {
		if operation.removePackage {
			_ = os.Remove(operation.packagePath)
		}
	}()
	if s.m.traffic != nil && len(snapshot.ExclusiveHosts) > 0 {
		_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "compact_traffic", 68)
		operation.traffic, err = s.m.traffic.StageDeleteHostsExactForArchive(snapshot.ExclusiveHosts, job.ID, job.TaskID)
		if err != nil {
			return err
		}
		defer func() {
			if !operation.committed {
				runErr = errors.Join(runErr, operation.traffic.Rollback())
			}
		}()
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "compact_database", 78)
	if err := s.m.pg.CompleteTaskArchive(job.ID, snapshot, operation.packagePath, checksum, original, size); err != nil {
		return err
	}
	operation.committed, operation.removePackage = true, false
	operation.finish()
	return nil
}

// drain retains the original runner/side-question cancellation statements.
// The operator proof checks them against the inherited job before accepting.
func (operation *archiveWriteJob) drain(drainCtx context.Context) error {
	s, job, taskID := operation.server, operation.job, operation.taskID
	if err := s.waitTaskQuiescent(drainCtx, taskID); err != nil {
		return errors.New("task still has running agents; please pause it and retry the archive")
	}
	if runner := s.m.Katana(); runner != nil {
		if err := runner.CancelTask(drainCtx, job.TaskID, errors.New("task is being archived")); err != nil {
			return fmt.Errorf("stop Katana discovery before archive: %w", err)
		}
	}
	if runner := s.m.Arsenal(); runner != nil {
		if err := runner.CancelTask(drainCtx, job.TaskID, errors.New("task is being archived")); err != nil {
			return fmt.Errorf("stop Arsenal scan before archive: %w", err)
		}
	}
	if err := s.drainTaskSideQuestions(drainCtx, taskID); err != nil {
		return err
	}
	return nil
}

func (operation *archiveWriteJob) snapshot(explorationID int64) (*pgdb.TaskArchiveSnapshot, error) {
	s, job := operation.server, operation.job
	path := filepath.Join(operation.files.payload, filepath.FromSlash(pgdb.TaskArchiveLLMRecordsPath))
	if err := os.MkdirAll(filepath.Dir(path), archiveDirMode); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, archiveFileMode)
	if err != nil {
		return nil, err
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "snapshot_database", 20)
	snapshot, snapshotErr := s.m.pg.SnapshotTaskArchiveWithLLMRecords(job.TaskID, file)
	var syncErr error
	if snapshotErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(snapshotErr, syncErr, closeErr); err != nil {
		return nil, err
	}
	if snapshot.ExplorationID != explorationID {
		return nil, errors.New("task exploration record changed during the archive snapshot")
	}
	return snapshot, nil
}

func (operation *archiveWriteJob) warn(message string) {
	log.Printf("[task-archive] task %s: %s", operation.taskID, message)
	_ = operation.server.m.pg.AppendTaskArchiveWarning(operation.job.ID, message)
}

func (operation *archiveWriteJob) finish() {
	if operation.traffic != nil {
		if err := operation.traffic.Commit(); err != nil {
			operation.warn("archive completed, but cleanup of exclusive traffic hot storage failed (the archive package is still restorable): " + err.Error())
		}
	}
	if err := operation.files.commit(); err != nil {
		operation.warn("archive completed, but staging directory cleanup failed: " + err.Error())
	}
	s, job, taskID := operation.server, operation.job, operation.taskID
	s.engine.StopTask(taskID)
	s.taskAgentMu.Lock()
	delete(s.taskAgents, taskID)
	s.taskAgentMu.Unlock()
	s.m.forgetTask(taskID, job.TaskID)
}
