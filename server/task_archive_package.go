package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pgdb "github.com/nobanic/UroborOS/db"
)

const (
	archiveDirMode  = 0o700
	archiveFileMode = 0o600
	maxArchiveFiles = 1_000_000
	maxArchiveBytes = int64(1 << 47) // 128 TiB safety ceiling for corrupt headers.
)

type archiveFileMove struct {
	Source   string `json:"source"`
	Relative string `json:"relative"`
}

type archiveStageJournal struct {
	ArchiveID int64             `json:"archive_id"`
	TaskID    string            `json:"task_id"`
	Moves     []archiveFileMove `json:"moves"`
}

type archiveRestoreJournal struct {
	ArchiveID int64                 `json:"archive_id"`
	TaskID    int64                 `json:"task_id"`
	Moves     []restoredArchivePath `json:"moves"`
}

type taskArchiveFileStage struct {
	root    string
	payload string
	journal archiveStageJournal
	done    bool
}

type restoredArchivePath struct {
	Source      string
	Destination string
}

type taskArchiveRestoreFiles struct {
	extracted string
	moves     []restoredArchivePath
	done      bool
}

func writeArchiveJournal(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, archiveFileMode)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func taskArchiveRoot(dataDir string) string {
	return filepath.Join(dataDir, "archives", "tasks")
}

func taskArchivePath(dataDir string, archiveID int64, taskID string) string {
	return filepath.Join(taskArchiveRoot(dataDir), fmt.Sprintf("task-%s-%d.tar.zst", taskID, archiveID))
}

func stageTaskArchiveFiles(dataDir string, archiveID int64, taskID string, explorationID int64) (*taskArchiveFileStage, error) {
	root := filepath.Join(taskArchiveRoot(dataDir), ".staging", strconv.FormatInt(archiveID, 10))
	stage := &taskArchiveFileStage{root: root, payload: filepath.Join(root, "payload")}
	journalPath := filepath.Join(root, "journal.json")
	if raw, err := os.ReadFile(journalPath); err == nil {
		if err := json.Unmarshal(raw, &stage.journal); err != nil {
			return nil, fmt.Errorf("read task archive staging journal: %w", err)
		}
		// The journal is written before the first rename, so its presence does not mean
		// the payload is complete: a previous round may have failed mid-loop with a
		// rollback that itself errored, which leaves the root in place. Replay the moves
		// rather than packaging a payload that is missing transcripts or workspace files.
		if err := os.MkdirAll(filepath.Join(stage.payload, "files"), archiveDirMode); err != nil {
			return nil, err
		}
		if err := stage.applyMoves(); err != nil {
			_ = stage.rollback()
			return nil, err
		}
		return stage, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(stage.payload, "files"), archiveDirMode); err != nil {
		return nil, err
	}
	stage.journal = archiveStageJournal{ArchiveID: archiveID, TaskID: taskID}
	targets := []archiveFileMove{}
	taskDir := filepath.Join(dataDir, "tasks", taskID)
	if _, err := os.Lstat(taskDir); err == nil {
		targets = append(targets, archiveFileMove{Source: taskDir, Relative: filepath.Join("files", "tasks", taskID)})
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	transcriptDir := filepath.Join(dataDir, "transcripts")
	entries, err := os.ReadDir(transcriptDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	prefix := fmt.Sprintf("exp%d-", explorationID)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (!entry.IsDir() && !strings.HasSuffix(name, ".jsonl")) {
			continue
		}
		targets = append(targets, archiveFileMove{
			Source: filepath.Join(transcriptDir, name), Relative: filepath.Join("files", "transcripts", name),
		})
	}
	// Persist the complete plan before the first rename. Recovery can therefore
	// roll back any prefix of the moves after an abrupt process termination.
	stage.journal.Moves = targets
	if err := writeArchiveJournal(journalPath, stage.journal); err != nil {
		_ = stage.rollback()
		return nil, err
	}
	if err := stage.applyMoves(); err != nil {
		_ = stage.rollback()
		return nil, err
	}
	return stage, nil
}

// applyMoves performs the renames recorded in the journal. Entries already sitting
// in the payload are skipped, so an interrupted staging round can be resumed in
// place without moving anything twice.
func (s *taskArchiveFileStage) applyMoves() error {
	for _, move := range s.journal.Moves {
		destination := filepath.Join(s.payload, move.Relative)
		if _, err := os.Lstat(destination); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := os.Lstat(move.Source); err != nil {
			if os.IsNotExist(err) {
				// Neither side exists: the path was removed outside the archive flow
				// after the journal was written. Nothing can be staged for it.
				continue
			}
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), archiveDirMode); err != nil {
			return err
		}
		if err := os.Rename(move.Source, destination); err != nil {
			return fmt.Errorf("stage task archive path %s: %w", move.Source, err)
		}
	}
	return nil
}

func (s *taskArchiveFileStage) rollback() error {
	if s == nil || s.done {
		return nil
	}
	var errs []error
	for i := len(s.journal.Moves) - 1; i >= 0; i-- {
		move := s.journal.Moves[i]
		staged := filepath.Join(s.payload, move.Relative)
		if _, err := os.Lstat(staged); os.IsNotExist(err) {
			continue
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := os.Lstat(move.Source); err == nil {
			errs = append(errs, fmt.Errorf("archive rollback destination exists: %s", move.Source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, err)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(move.Source), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(staged, move.Source); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		if err := os.RemoveAll(s.root); err != nil {
			errs = append(errs, err)
		}
	}
	s.done = true
	return errors.Join(errs...)
}

func (s *taskArchiveFileStage) commit() error {
	if s == nil || s.done {
		return nil
	}
	s.done = true
	return os.RemoveAll(s.root)
}

func installTaskArchiveFiles(dataDir, extractedDir, taskID string, archiveID int64) (*taskArchiveRestoreFiles, error) {
	stage := &taskArchiveRestoreFiles{extracted: extractedDir}
	numericTaskID, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || numericTaskID <= 0 {
		return nil, fmt.Errorf("invalid restore task id %q", taskID)
	}
	sources := []restoredArchivePath{}
	workspace := filepath.Join(extractedDir, "files", "tasks", taskID)
	if _, err := os.Lstat(workspace); err == nil {
		sources = append(sources, restoredArchivePath{Source: workspace, Destination: filepath.Join(dataDir, "tasks", taskID)})
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	transcripts := filepath.Join(extractedDir, "files", "transcripts")
	entries, err := os.ReadDir(transcripts)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		sources = append(sources, restoredArchivePath{
			Source: filepath.Join(transcripts, entry.Name()), Destination: filepath.Join(dataDir, "transcripts", entry.Name()),
		})
	}
	for _, move := range sources {
		if _, err := os.Lstat(move.Destination); err == nil {
			return nil, fmt.Errorf("restore destination already exists: %s", move.Destination)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	stage.moves = sources
	journal := archiveRestoreJournal{ArchiveID: archiveID, TaskID: numericTaskID, Moves: sources}
	if err := writeArchiveJournal(filepath.Join(extractedDir, "restore-journal.json"), journal); err != nil {
		return nil, err
	}
	for _, move := range sources {
		if err := os.MkdirAll(filepath.Dir(move.Destination), 0o755); err != nil {
			_ = stage.rollback()
			return nil, err
		}
		if err := os.Rename(move.Source, move.Destination); err != nil {
			_ = stage.rollback()
			return nil, err
		}
	}
	return stage, nil
}

func (s *taskArchiveRestoreFiles) rollback() error {
	if s == nil || s.done {
		return nil
	}
	var errs []error
	for i := len(s.moves) - 1; i >= 0; i-- {
		move := s.moves[i]
		if _, err := os.Lstat(move.Destination); os.IsNotExist(err) {
			continue
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := os.Lstat(move.Source); err == nil {
			errs = append(errs, fmt.Errorf("restore rollback source exists: %s", move.Source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, err)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(move.Source), archiveDirMode); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(move.Destination, move.Source); err != nil {
			errs = append(errs, err)
		}
	}
	s.done = true
	return errors.Join(errs...)
}

func (s *taskArchiveRestoreFiles) commit() error {
	if s == nil || s.done {
		return nil
	}
	s.done = true
	return os.RemoveAll(s.extracted)
}

// recoverTaskArchiveRestoreStages resolves file installs left by an interrupted
// restore. If PostgreSQL committed, installed files are authoritative and only
// the extraction directory is stale. Otherwise all completed renames are moved
// back so the persistent restore job can retry from a clean destination.
func recoverTaskArchiveRestoreStages(dataDir string, pg *pgdb.DB) error {
	recovery := archiveRecovery{dataDir: dataDir, database: pg}
	return recovery.sweep(filepath.Join(taskArchiveRoot(recovery.dataDir), ".restore"), true, recovery.restoration)
}

// recoverTaskArchiveStages resolves file renames left by an interrupted archive.
// A committed cold task already has a verified package, so stale staging can be
// discarded; otherwise files are moved back before the persistent job retries.
func recoverTaskArchiveStages(dataDir string, pg *pgdb.DB) error {
	recovery := archiveRecovery{dataDir: dataDir, database: pg}
	return recovery.sweep(filepath.Join(taskArchiveRoot(recovery.dataDir), ".staging"), true, recovery.staging)
}

func recoverTaskArchiveDeletePackages(dataDir string, pg *pgdb.DB) error {
	recovery := archiveRecovery{dataDir: dataDir, database: pg}
	return recovery.sweep(taskArchiveRoot(recovery.dataDir), false, recovery.deletion)
}

func stageTaskArchivePackageDelete(archivePath string, archiveID int64) (string, bool, error) {
	staged := archivePath + fmt.Sprintf(".deleting-%d", archiveID)
	if _, err := os.Lstat(staged); err == nil {
		if _, originalErr := os.Lstat(archivePath); originalErr == nil {
			return staged, false, errors.New("the original archive package and the delete-staging file both exist")
		} else if !os.IsNotExist(originalErr) {
			return staged, false, originalErr
		}
		return staged, true, nil
	} else if !os.IsNotExist(err) {
		return staged, false, err
	}
	if err := os.Rename(archivePath, staged); err == nil {
		return staged, true, nil
	} else if !os.IsNotExist(err) {
		return staged, false, err
	}
	return staged, false, nil
}

func writeTaskArchivePackage(path, payloadDir string, snapshot *pgdb.TaskArchiveSnapshot) (originalSize, compressedSize int64, checksum string, err error) {
	return buildArchivePackage(path, payloadDir, snapshot)
}

func extractTaskArchivePackage(path, expectedSHA, destination string) error {
	return extractArchivePackage(path, expectedSHA, destination)
}
