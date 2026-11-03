package traffic

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type hostTreeStaging struct {
	traffic           *Traffic
	dirs, hosts       []string
	archiveID, taskID int64
}
type hostDeleteJournal struct {
	path    string
	journal hostDeleteStageJournal
}
type hostStageReaper struct {
	traffic  *Traffic
	stageDir string
}

func (staging hostTreeStaging) run() (stageDir string, moves []stagedTrafficPath, err error) {
	seen := make(map[string]struct{}, len(staging.dirs))
	planned := make([]stagedTrafficPath, 0, len(staging.dirs))
	for _, source := range staging.dirs {
		if _, dup := seen[source]; dup {
			continue
		}
		seen[source] = struct{}{}
		if _, err := os.Lstat(source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return stageDir, moves, fmt.Errorf("check legacy traffic directory %s: %w", source, err)
		}
		planned = append(planned, stagedTrafficPath{source: source})
	}
	// The archive path must leave a journal even when there is not a single legacy host
	// directory (traffic on a fresh install lives only in SQLite + _blobs): if a crash lands
	// between the PostgreSQL commit and the SQLite commit, the SQLite transaction is rolled back
	// on restart, and only this journal lets recovery re-apply the host-row deletion. Without it,
	// the exclusive traffic of a task already moved to cold storage would stay in the hot DB forever.
	uniqueHosts := uniqueArchiveHosts(staging.hosts)
	if len(planned) == 0 && len(uniqueHosts) == 0 {
		return "", nil, nil
	}
	parent := filepath.Join(staging.traffic.dir, "_delete_staging")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", nil, fmt.Errorf("create traffic staging directory: %w", err)
	}
	if stageDir, err = os.MkdirTemp(parent, "hosts-"); err != nil {
		return "", nil, fmt.Errorf("create traffic staging directory: %w", err)
	}
	journal := hostDeleteStageJournal{Version: 1, ArchiveID: staging.archiveID, TaskID: staging.taskID, Hosts: uniqueHosts}
	for i := range planned {
		planned[i].staged = filepath.Join(stageDir, fmt.Sprintf("%d-%s", i, filepath.Base(planned[i].source)))
		journal.Moves = append(journal.Moves, hostDeleteStageMove{Source: planned[i].source, Staged: planned[i].staged})
	}
	if err := writeHostDeleteStageJournal(filepath.Join(stageDir, hostDeleteStageJournalName), journal); err != nil {
		_ = os.RemoveAll(stageDir)
		return "", nil, err
	}
	for _, move := range planned {
		source, staged := move.source, move.staged
		if err := os.Rename(source, staged); err != nil {
			return stageDir, moves, fmt.Errorf("move aside legacy traffic directory %s: %w", source, err)
		}
		moves = append(moves, stagedTrafficPath{source: source, staged: staged})
	}
	return stageDir, moves, nil
}

func (journal hostDeleteJournal) write() error {
	raw, err := json.Marshal(journal.journal)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(journal.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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

func (reaper hostStageReaper) run() {
	if reaper.stageDir == "" {
		return
	}
	reaper.traffic.reaping.Go(func() {
		if err := os.RemoveAll(reaper.stageDir); err != nil {
			log.Printf("[traffic] failed to clean up legacy traffic directory %s: %v", reaper.stageDir, err)
		}
	})
}
