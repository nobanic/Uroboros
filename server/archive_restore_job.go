package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pgdb "github.com/nobanic/UroborOS/db"
)

type archiveRestoreJob struct {
	server *Server
	job    *pgdb.TaskArchive
}

func (operation archiveRestoreJob) run() error {
	s, job := operation.server, operation.job
	if err := validateArchivePath(s.m.dir, job.ArchivePath); err != nil {
		return err
	}
	restored, err := s.m.pg.IsTaskArchiveRestored(job.ID)
	if err != nil {
		return err
	}
	if restored {
		return operation.consume()
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "verify_package", 10)
	parent := filepath.Join(taskArchiveRoot(s.m.dir), ".restore")
	if err := os.MkdirAll(parent, archiveDirMode); err != nil {
		return err
	}
	extracted, err := os.MkdirTemp(parent, fmt.Sprintf("%d-", job.ID))
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(extracted) }()
	if err := extractTaskArchivePackage(job.ArchivePath, job.SHA256, extracted); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(extracted, "manifest.json"))
	if err != nil {
		return err
	}
	var snapshot pgdb.TaskArchiveSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if snapshot.TaskID != job.TaskID || !pgdb.IsTaskArchiveFormatSupported(snapshot.FormatVersion) {
		return pgdb.ErrTaskArchiveFormatMismatch
	}
	evidence, err := pgdb.ArchiveEvidenceSnapshots(&snapshot)
	if err != nil {
		return err
	}
	return s.evidenceStore().WithInstalledSnapshots(s.ctx, evidence, filepath.Join(extracted, "evidence"), func() error { return s.restoreTaskArchivePayload(job, &snapshot, extracted) })
}

func (operation archiveRestoreJob) consume() error {
	if err := os.Remove(operation.job.ArchivePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := operation.server.m.pg.CompleteTaskArchiveRestore(operation.job.ID); err != nil {
		return err
	}
	operation.server.loadRestoredTask(operation.job.TaskID)
	return nil
}

// archiveRestorePayload owns extraction-backed readers and installed paths.
// databaseRestored changes at the original boundary after record-close/error
// handling and warning activity; cleanup errors must retain that distinction.
type archiveRestorePayload struct {
	operation        archiveRestoreJob
	snapshot         *pgdb.TaskArchiveSnapshot
	extracted        string
	records          *os.File
	files            *taskArchiveRestoreFiles
	databaseRestored bool
}

func (payload *archiveRestorePayload) run() (runErr error) {
	s, job, snapshot := payload.operation.server, payload.operation.job, payload.snapshot
	relative, streamed := snapshot.StreamedTables["llm_records"]
	if len(snapshot.StreamedTables) > 0 && (snapshot.FormatVersion < 2 || !streamed || len(snapshot.StreamedTables) != 1 || relative != pgdb.TaskArchiveLLMRecordsPath) {
		return pgdb.ErrTaskArchiveFormatMismatch
	}
	var err error
	if streamed {
		payload.records, err = os.Open(filepath.Join(payload.extracted, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		defer func() {
			if payload.records != nil {
				_ = payload.records.Close()
			}
		}()
	}
	if s.m.traffic != nil {
		_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "restore_traffic", 35)
		if _, err := s.m.traffic.ImportArchive(filepath.Join(payload.extracted, "traffic")); err != nil {
			return err
		}
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "restore_files", 55)
	payload.files, err = installTaskArchiveFiles(s.m.dir, payload.extracted, strconv.FormatInt(job.TaskID, 10), job.ID)
	if err != nil {
		return err
	}
	defer func() {
		if !payload.databaseRestored {
			runErr = errors.Join(runErr, payload.files.rollback())
		}
	}()
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "restore_database", 70)
	warnings, err := payload.restoreRows()
	if err != nil {
		return err
	}
	payload.warn(warnings)
	payload.databaseRestored = true
	if err := payload.files.commit(); err != nil {
		return err
	}
	_ = s.m.pg.UpdateTaskArchiveProgress(job.ID, "consume_package", 95)
	return payload.operation.consume()
}

func (payload *archiveRestorePayload) restoreRows() ([]string, error) {
	s, job := payload.operation.server, payload.operation.job
	if payload.records == nil {
		return s.m.pg.RestoreTaskArchive(job.ID, payload.snapshot, job.RemainingTimeoutSeconds)
	}
	warnings, err := s.m.pg.RestoreTaskArchiveWithLLMRecords(job.ID, payload.snapshot, job.RemainingTimeoutSeconds, payload.records)
	closeErr := payload.records.Close()
	payload.records = nil
	return warnings, errors.Join(err, closeErr)
}

func (payload *archiveRestorePayload) warn(warnings []string) {
	s, job, snapshot := payload.operation.server, payload.operation.job, payload.snapshot
	for _, warning := range warnings {
		log.Printf("[task-archive] restored task %d with warning: %s", job.TaskID, warning)
	}
	if len(warnings) > 0 {
		if _, err := s.m.pg.Exploration(snapshot.ExplorationID).AppendActivity(pgdb.Activity{
			Worker: "system", Kind: "system", Summary: "task restore completed, but some associations were downgraded", Detail: strings.Join(warnings, "\n"),
		}); err != nil {
			log.Printf("[task-archive] persist restore warnings for task %d: %v", job.TaskID, err)
		}
	}
}
