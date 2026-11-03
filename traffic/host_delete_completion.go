package traffic

import (
	"database/sql"
	"errors"
	"fmt"
)

type hostStageCompletion struct{ stage *HostDeleteStage }

func (completion hostStageCompletion) rollback() error {
	if completion.stage == nil || completion.stage.done {
		return nil
	}
	var errs []error
	if completion.stage.tx != nil {
		if err := completion.stage.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			errs = append(errs, fmt.Errorf("roll back traffic index: %w", err))
		}
	}
	if err := restoreTrees(completion.stage.stageDir, completion.stage.moves); err != nil {
		errs = append(errs, err)
	}
	completion.stage.done = true
	completion.stage.traffic.wmu.Unlock()
	return errors.Join(errs...)
}

func (completion hostStageCompletion) commit() error {
	if completion.stage == nil || completion.stage.done {
		return nil
	}
	if err := completion.stage.tx.Commit(); err != nil {
		// A failed SQLite commit normally rolls the transaction back. Restore the
		// trees so the traffic store remains internally consistent and recoverable.
		restoreErr := restoreTrees(completion.stage.stageDir, completion.stage.moves)
		completion.stage.done = true
		completion.stage.traffic.wmu.Unlock()
		return errors.Join(fmt.Errorf("commit traffic index deletion: %w", err), restoreErr)
	}
	// Unlinking the staged trees is what used to hold the write lock for hours;
	// it now runs in the background, while collection below only needs them to be
	// out of the live tree — which the staging rename already guaranteed.
	completion.stage.traffic.reapStage(completion.stage.stageDir)
	completion.stage.traffic.reclaim()
	var errs []error
	if err := completion.stage.traffic.gcBlobs(); err != nil {
		errs = append(errs, fmt.Errorf("reclaim traffic blobs: %w", err))
	}
	completion.stage.done = true
	completion.stage.traffic.wmu.Unlock()
	return errors.Join(errs...)
}
