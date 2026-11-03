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

// archiveRecovery owns one startup sweep, not a new worker. Each journal or
// staged package is resolved independently, in ReadDir order; failed entries
// remain available for the existing fail-closed startup gate and manual retry.
type archiveRecovery struct {
	dataDir  string
	database *pgdb.DB
}

func (r archiveRecovery) sweep(parent string, directories bool, recoverEntry func(string, string) []error) error {
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() != directories {
			continue
		}
		failures = append(failures, recoverEntry(entry.Name(), filepath.Join(parent, entry.Name()))...)
	}
	return errors.Join(failures...)
}

func readArchiveRecoveryJournal(path string, value any) (bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, json.Unmarshal(raw, value)
}

func (r archiveRecovery) staging(_ string, root string) []error {
	var journal archiveStageJournal
	found, err := readArchiveRecoveryJournal(filepath.Join(root, "journal.json"), &journal)
	if err != nil {
		return []error{err}
	}
	if !found {
		return []error{os.RemoveAll(root)}
	}
	archive, err := r.database.GetTaskArchive(journal.ArchiveID)
	if err != nil {
		return []error{err}
	}
	if archive != nil && archive.State == pgdb.ArchiveReady {
		return []error{os.RemoveAll(root)}
	}
	stage := &taskArchiveFileStage{root: root, payload: filepath.Join(root, "payload"), journal: journal}
	rollbackErr := stage.rollback()
	// The previous package cleanup remains best effort even when rollback
	// itself failed. Keep that ordering and the original journal retention.
	if archive != nil && archive.ArchivePath != "" {
		_ = os.Remove(archive.ArchivePath)
	}
	return []error{rollbackErr}
}

func (r archiveRecovery) restoration(_ string, root string) []error {
	var journal archiveRestoreJournal
	found, err := readArchiveRecoveryJournal(filepath.Join(root, "restore-journal.json"), &journal)
	if err != nil {
		return []error{err}
	}
	if !found {
		return []error{os.RemoveAll(root)}
	}
	restored, err := r.database.IsTaskArchiveRestored(journal.ArchiveID)
	if errors.Is(err, pgdb.ErrTaskArchiveNotFound) && journal.TaskID > 0 {
		task, taskErr := r.database.GetTask(journal.TaskID)
		if taskErr != nil {
			return []error{taskErr}
		}
		restored, err = task != nil, nil
	}
	if err != nil {
		return []error{err}
	}
	if restored {
		return []error{os.RemoveAll(root)}
	}
	stage := &taskArchiveRestoreFiles{extracted: root, moves: journal.Moves}
	if err := stage.rollback(); err != nil {
		return []error{err}
	}
	return []error{os.RemoveAll(root)}
}

func (r archiveRecovery) deletion(name, staged string) []error {
	marker := strings.LastIndex(name, ".deleting-")
	if marker < 0 {
		return nil
	}
	id, err := strconv.ParseInt(name[marker+len(".deleting-"):], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	original := filepath.Join(filepath.Dir(staged), name[:marker])
	archive, err := r.database.GetTaskArchive(id)
	if err != nil {
		return []error{err}
	}
	if archive == nil {
		if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
			return []error{err}
		}
		return nil
	}
	switch archive.State {
	case pgdb.DeleteQueued, pgdb.Deleting, pgdb.DeleteFailed:
		return nil // The existing delete worker consumes this staged path.
	}
	if _, err := os.Lstat(original); err == nil {
		return []error{fmt.Errorf("archive delete recovery destination exists: %s", original)}
	} else if !os.IsNotExist(err) {
		return []error{err}
	}
	return []error{os.Rename(staged, original)}
}
