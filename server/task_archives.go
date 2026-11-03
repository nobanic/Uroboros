package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/agent"
	pgdb "github.com/nobanic/UroborOS/db"
)

const taskArchivePollInterval = 2 * time.Second

type archiveBatchRequest struct {
	TaskIDs    []string `json:"task_ids"`
	ArchiveIDs []int64  `json:"archive_ids"`
}

type archiveBatchItem struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Queued  bool   `json:"queued"`
	Error   string `json:"error,omitempty"`
	Archive int64  `json:"archive_id,omitempty"`
}

func (s *Server) startTaskArchiveWorker() { (archiveWorker{server: s}).start() }

func (s *Server) notifyTaskArchiveWorker() {
	if s == nil || s.archiveWake == nil {
		return
	}
	select {
	case s.archiveWake <- struct{}{}:
	default:
	}
}

func (s *Server) runOneTaskArchiveJob() error { return (archiveWorker{server: s}).next() }

func (s *Server) archiveTask(job *pgdb.TaskArchive) (runErr error) {
	return (&archiveWriteJob{server: s, job: job}).run()
}

func (s *Server) restoreTaskArchive(job *pgdb.TaskArchive) (runErr error) {
	return (archiveRestoreJob{server: s, job: job}).run()
}

func (s *Server) restoreTaskArchivePayload(job *pgdb.TaskArchive, snapshot *pgdb.TaskArchiveSnapshot, extracted string) (runErr error) {
	return (&archiveRestorePayload{operation: archiveRestoreJob{server: s, job: job}, snapshot: snapshot, extracted: extracted}).run()
}

func (s *Server) loadRestoredTask(taskID int64) {
	id := strconv.FormatInt(taskID, 10)
	var restored *Task
	for _, task := range s.m.LoadExisting() {
		if task.ID == id {
			restored = task
			break
		}
	}
	if restored == nil {
		if task, ok := s.m.Task(id); ok {
			restored = task
		}
	}
	if restored == nil {
		return
	}
	lifecycle := restored.lifecycleSnapshot()
	if lifecycle.Paused {
		s.engine.Pause(id, agent.AbortPausedOnReload)
	}
	if !isTerminalStatus(lifecycle.Status) {
		s.engine.startDeadlineCoordinator(s.ctx, restored)
		if !lifecycle.Paused {
			go s.ensureKatanaAuto(id)
		}
	}
}

func (s *Server) deleteTaskArchive(job *pgdb.TaskArchive) error {
	if err := validateArchivePath(s.m.dir, job.ArchivePath); err != nil {
		return err
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "stage_package_delete", 35)
	staged, moved, err := stageTaskArchivePackageDelete(job.ArchivePath, job.ID)
	if err != nil {
		return err
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "delete_metadata", 70)
	if err := s.m.pg.DeleteTaskArchiveStub(job.ID); err != nil {
		if moved {
			_ = os.Rename(staged, job.ArchivePath)
		}
		return err
	}
	if moved {
		if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
			log.Printf("[task-archive] archived task %d metadata deleted; stale package cleanup failed: %v", job.TaskID, err)
		}
	}
	return nil
}

func validateArchivePath(dataDir, candidate string) error {
	if strings.TrimSpace(candidate) == "" {
		return errors.New("archive package path is empty")
	}
	root, err := filepath.Abs(taskArchiveRoot(dataDir))
	if err != nil {
		return err
	}
	path, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("archive package path is outside the managed directory")
	}
	return nil
}

func (s *Server) listTaskArchives(w http.ResponseWriter, r *http.Request) {
	(archiveControl{server: s, writer: w, request: r}).list()
}

func (s *Server) getTaskArchive(w http.ResponseWriter, r *http.Request) {
	(archiveControl{server: s, writer: w, request: r}).get()
}

func (s *Server) queueTaskArchive(w http.ResponseWriter, r *http.Request) {
	(archiveControl{server: s, writer: w, request: r}).queueTask()
}

func (s *Server) queueTaskArchiveRestore(w http.ResponseWriter, r *http.Request) {
	(archiveControl{server: s, writer: w, request: r}).queueArchive(true)
}

func (s *Server) queueTaskArchiveDelete(w http.ResponseWriter, r *http.Request) {
	(archiveControl{server: s, writer: w, request: r}).queueArchive(false)
}

func (s *Server) queueTaskArchivesBatch(w http.ResponseWriter, r *http.Request) {
	(archiveControl{server: s, writer: w, request: r}).taskBatch()
}

func (s *Server) queueTaskArchiveActionBatch(w http.ResponseWriter, r *http.Request, action string) {
	(archiveControl{server: s, writer: w, request: r}).archiveBatch(action)
}

func (s *Server) restoreTaskArchivesBatch(w http.ResponseWriter, r *http.Request) {
	s.queueTaskArchiveActionBatch(w, r, "restore")
}

func (s *Server) deleteTaskArchivesBatch(w http.ResponseWriter, r *http.Request) {
	s.queueTaskArchiveActionBatch(w, r, "delete")
}

func normalizeArchiveIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, errors.New("archive_ids cannot be empty")
	}
	if len(ids) > 100 {
		return nil, errors.New("at most 100 archives can be processed at once")
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid archive id %d", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Server) orderLiveTaskArchiveBatch(ids []string) []string {
	return (archiveBatchOrder{server: s}).live(ids)
}

func (s *Server) orderColdTaskArchiveBatch(ids []int64, restore bool) []int64 {
	return (archiveBatchOrder{server: s}).cold(ids, restore)
}

// topoTaskIDs orders source->dependent for restore and dependent->source for
// archive/delete. Cycles are impossible at creation, but stable fallback keeps a
// damaged database operable.
func topoTaskIDs(ids []string, sources map[string][]string, restore bool) []string {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	adjacency := map[string][]string{}
	indegree := map[string]int{}
	for id := range set {
		indegree[id] = 0
	}
	for dependent, sourceIDs := range sources {
		for _, source := range sourceIDs {
			if !set[dependent] || !set[source] {
				continue
			}
			from, to := dependent, source
			if restore {
				from, to = source, dependent
			}
			adjacency[from] = append(adjacency[from], to)
			indegree[to]++
		}
	}
	queue := []string{}
	for _, id := range ids {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	ordered := make([]string, 0, len(ids))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		ordered = append(ordered, id)
		for _, next := range adjacency[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if len(ordered) != len(ids) {
		seen := map[string]bool{}
		for _, id := range ordered {
			seen[id] = true
		}
		for _, id := range ids {
			if !seen[id] {
				ordered = append(ordered, id)
			}
		}
	}
	return ordered
}

func writeArchiveError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, pgdb.ErrTaskArchiveNotFound) {
		status = http.StatusNotFound
	} else if !errors.Is(err, pgdb.ErrTaskArchiveIneligible) &&
		!errors.Is(err, pgdb.ErrTaskArchiveQueued) &&
		!errors.Is(err, pgdb.ErrTaskArchiveDependent) &&
		!errors.Is(err, pgdb.ErrTaskArchiveState) &&
		!errors.Is(err, pgdb.ErrTaskArchiveDeleteBlocked) {
		status = http.StatusInternalServerError
	}
	writeErr(w, status, err.Error())
}
