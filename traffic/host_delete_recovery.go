package traffic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// One lock spans all entries, the resolver and the final reclamation pass.
type hostStageRecovery struct {
	traffic *Traffic
	resolve func(int64, int64) (bool, error)
}
type hostStageRecoveryEntry struct {
	recovery hostStageRecovery
	parent   string
	entry    fs.DirEntry
}

func (recovery hostStageRecovery) run() error {
	if recovery.traffic == nil {
		return nil
	}
	recovery.traffic.wmu.Lock()
	defer recovery.traffic.wmu.Unlock()
	parent := filepath.Join(recovery.traffic.dir, "_delete_staging")
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	needsGC := false
	for _, entry := range entries {
		collect, failures := (hostStageRecoveryEntry{recovery: recovery, parent: parent, entry: entry}).run()
		errs = append(errs, failures...)
		if collect {
			needsGC = true
		}
	}
	if needsGC {
		recovery.traffic.reclaim()
		if err := recovery.traffic.gcBlobs(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (entryState hostStageRecoveryEntry) run() (bool, []error) {
	var errs []error
	if !entryState.entry.IsDir() {
		return false, errs
	}
	stageDir := filepath.Join(entryState.parent, entryState.entry.Name())
	raw, err := os.ReadFile(filepath.Join(stageDir, hostDeleteStageJournalName))
	if os.IsNotExist(err) {
		// Older versions did not persist enough information for lossless
		// recovery. Keep the directory for manual inspection.
		return false, errs
	}
	if err != nil {
		errs = append(errs, err)
		return false, errs
	}
	var journal hostDeleteStageJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		errs = append(errs, fmt.Errorf("read traffic staging journal %s: %w", stageDir, err))
		return false, errs
	}
	if journal.Version != 1 {
		errs = append(errs, fmt.Errorf("traffic staging journal %s has an unsupported version %d", stageDir, journal.Version))
		return false, errs
	}
	moves := make([]stagedTrafficPath, 0, len(journal.Moves))
	for _, move := range journal.Moves {
		if !pathWithin(entryState.recovery.traffic.dir, move.Source) || !pathWithin(stageDir, move.Staged) {
			errs = append(errs, fmt.Errorf("traffic staging journal contains an out-of-bounds path: %s", stageDir))
			moves = nil
			break
		}
		if _, err := os.Lstat(move.Staged); err == nil {
			moves = append(moves, stagedTrafficPath{source: move.Source, staged: move.Staged})
		} else if !os.IsNotExist(err) {
			errs = append(errs, err)
			moves = nil
			break
		}
	}
	if moves == nil {
		return false, errs
	}
	committed := false
	if journal.ArchiveID > 0 {
		if entryState.recovery.resolve == nil {
			errs = append(errs, fmt.Errorf("traffic archive %d has no state resolver", journal.ArchiveID))
			return false, errs
		}
		committed, err = entryState.recovery.resolve(journal.ArchiveID, journal.TaskID)
		if err != nil {
			errs = append(errs, err)
			return false, errs
		}
	} else if len(journal.Hosts) > 0 {
		committed, err = entryState.recovery.traffic.hostsHaveNoExchanges(journal.Hosts)
		if err != nil {
			errs = append(errs, err)
			return false, errs
		}
	}
	if !committed {
		if err := restoreTrees(stageDir, moves); err != nil {
			errs = append(errs, err)
		}
		return false, errs
	}
	if err := entryState.recovery.traffic.deleteArchivedHosts(journal.Hosts); err != nil {
		errs = append(errs, err)
		return false, errs
	}
	if err := os.RemoveAll(stageDir); err != nil {
		errs = append(errs, err)
		return false, errs
	}
	return true, errs
}
