package server

import (
	"errors"
	"fmt"
	"log"
	"time"

	pgdb "github.com/nobanic/UroborOS/db"
)

// archiveWorker borrows the existing server, queue, wake channel and wait group.
// It organizes the original single worker; it does not add another scheduler.
type archiveWorker struct{ server *Server }

func (worker archiveWorker) start() {
	s := worker.server
	if s.m == nil || s.m.pg == nil {
		return
	}
	if err := s.m.pg.RecoverTaskArchiveJobs(); err != nil {
		log.Printf("[task-archive] recover jobs: %v", err)
	}
	failed := false
	for _, step := range []struct {
		recover func(string, *pgdb.DB) error
		message string
	}{
		{recoverTaskArchiveStages, "[task-archive] recover file staging: %v"},
		{recoverTaskArchiveRestoreStages, "[task-archive] recover restore staging: %v"},
		{recoverTaskArchiveDeletePackages, "[task-archive] recover delete packages: %v"},
	} {
		if err := step.recover(s.m.dir, s.m.pg); err != nil {
			log.Printf(step.message, err)
			failed = true
		}
	}
	if failed {
		log.Printf("[task-archive] worker disabled because startup recovery is incomplete")
		return
	}
	s.archiveWG.Add(1)
	go worker.run()
	s.notifyTaskArchiveWorker()
}

func (worker archiveWorker) run() {
	s := worker.server
	defer s.archiveWG.Done()
	ticker := time.NewTicker(taskArchivePollInterval)
	defer ticker.Stop()
	for {
		if err := s.runOneTaskArchiveJob(); err != nil {
			log.Printf("[task-archive] worker: %v", err)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.archiveWake:
		case <-ticker.C:
		}
	}
}

func (worker archiveWorker) next() error {
	s := worker.server
	job, err := s.m.pg.ClaimTaskArchiveJob(s.ctx)
	if err != nil || job == nil {
		return err
	}
	if err := worker.perform(job); err != nil {
		if failed := s.m.pg.FailTaskArchiveJob(job.ID, job.State, err); failed != nil {
			return errors.Join(err, failed)
		}
		return fmt.Errorf("archive job %d (%s): %w", job.ID, job.State, err)
	}
	s.notifyTaskArchiveWorker()
	return nil
}

func (worker archiveWorker) perform(job *pgdb.TaskArchive) error {
	switch job.State {
	case pgdb.Archiving:
		return worker.server.archiveTask(job)
	case pgdb.Restoring:
		return worker.server.restoreTaskArchive(job)
	case pgdb.Deleting:
		return worker.server.deleteTaskArchive(job)
	default:
		return fmt.Errorf("unknown claimed archive state %q", job.State)
	}
}
