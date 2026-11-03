package traffic

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// An index operation borrows one pinned connection and its original context.
// No transaction, pool setting, cancellation policy or SQL is added here.
type indexLease struct {
	context    context.Context
	connection *sql.Conn
}
type indexCommand struct{ sql, failure string }

func (store *Traffic) withIndexLease(operation func(indexLease) error) error {
	ctx := context.Background()
	connection, err := store.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	return operation(indexLease{context: ctx, connection: connection})
}

func (lease indexLease) execute(statement, failure string, arguments ...any) error {
	_, err := lease.connection.ExecContext(lease.context, statement, arguments...)
	if err != nil && failure != "" {
		return fmt.Errorf("%s: %w", failure, err)
	}
	return err
}

func (lease indexLease) sequence(commands ...indexCommand) error {
	for _, command := range commands {
		if err := lease.execute(command.sql, command.failure); err != nil {
			return err
		}
	}
	return nil
}

func (lease indexLease) integer(statement string) (int, error) {
	var value int
	err := lease.connection.QueryRowContext(lease.context, statement).Scan(&value)
	return value, err
}

func (lease indexLease) vacuumMode(store *Traffic) (int, error) {
	mode, err := lease.integer(`PRAGMA auto_vacuum`)
	if err == nil {
		store.incrementalVacuum = mode == autoVacuumIncremental
	}
	return mode, err
}

func (lease indexLease) initialize(store *Traffic) error {
	if err := lease.sequence(indexCommand{sql: `PRAGMA journal_mode=WAL`}, indexCommand{sql: `PRAGMA busy_timeout=5000`}); err != nil {
		return err
	}
	tables, err := lease.integer(`SELECT count(*) FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return err
	}
	if tables == 0 {
		if err := lease.sequence(indexCommand{sql: `PRAGMA auto_vacuum=incremental`}, indexCommand{sql: `VACUUM`}); err != nil {
			return err
		}
	}
	mode, err := lease.vacuumMode(store)
	if err != nil {
		return err
	}
	if !store.incrementalVacuum {
		log.Printf("[traffic] incremental reclamation is not enabled on the index database (auto_vacuum=%d): deleting traffic will not shrink index.sqlite; a one-time storage compaction is needed to convert it", mode)
	}
	if err := lease.execute(indexSchema, ""); err != nil {
		return err
	}
	if err := lease.execute(ftsSchema, ""); err != nil {
		log.Printf("[traffic] full-text index unavailable; body search will be disabled (metadata search is unaffected): %v", err)
		return nil
	}
	store.fts = true
	return nil
}

func (lease indexLease) compact(store *Traffic) error {
	if store.fts {
		if err := lease.execute(`INSERT INTO ex_fts(ex_fts) VALUES('optimize')`, "merge full-text index"); err != nil {
			return err
		}
	}
	if err := lease.sequence(indexCommand{sql: `PRAGMA auto_vacuum=incremental`}, indexCommand{sql: `VACUUM`, failure: "compact index"}); err != nil {
		return err
	}
	if _, err := lease.vacuumMode(store); err != nil {
		return err
	}
	return lease.execute(`PRAGMA wal_checkpoint(TRUNCATE)`, "truncate WAL")
}

func (lease indexLease) reclaimChunk(store *Traffic, remainingMerges *int) (bool, error) {
	merged := false
	if store.fts && *remainingMerges > 0 {
		if err := lease.execute(`INSERT INTO ex_fts(ex_fts, rank) VALUES('merge', ?)`, "merge full-text index", -reclaimMergePages); err != nil {
			return false, err
		}
		*remainingMerges--
		merged = true
	}
	if !store.incrementalVacuum {
		return merged, nil
	}
	before, err := lease.integer(`PRAGMA freelist_count`)
	if err != nil {
		return false, err
	}
	if before == 0 {
		return merged, nil
	}
	if err := lease.execute(fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, reclaimChunkPages), "reclaim free index pages"); err != nil {
		return false, err
	}
	after, err := lease.integer(`PRAGMA freelist_count`)
	if err != nil {
		return false, err
	}
	return merged || after < before, nil
}

type indexReclamation struct {
	store *Traffic
	lease indexLease
}

func beginIndexReclamation(store *Traffic) {
	if !store.reclaiming.CompareAndSwap(false, true) {
		return
	}
	store.reaping.Go(func() {
		defer store.reclaiming.Store(false)
		err := store.withIndexLease(func(lease indexLease) error {
			(indexReclamation{store: store, lease: lease}).run()
			return nil
		})
		if err != nil {
			log.Printf("[traffic] failed to reclaim index space (acquire connection): %v", err)
		}
	})
}

func (pass indexReclamation) run() {
	store := pass.store
	deadline, merges := time.Now().Add(reclaimBudget), reclaimMergeSteps
	for step := 1; ; step++ {
		store.wmu.Lock()
		progressed, err := store.reclaimChunk(pass.lease.context, pass.lease.connection, &merges)
		store.wmu.Unlock()
		if err != nil {
			log.Printf("[traffic] failed to reclaim index space: %v", err)
			return
		}
		if !progressed {
			break
		}
		if store.stopping() {
			return
		}
		if step >= reclaimMaxSteps {
			log.Printf("[traffic] index-space reclamation did not finish (hit the %d-step limit); it will continue on the next deletion", reclaimMaxSteps)
			return
		}
		if time.Now().After(deadline) {
			log.Printf("[traffic] index-space reclamation did not finish (exhausted the %s budget); it will continue on the next deletion", reclaimBudget)
			return
		}
	}
	store.wmu.Lock()
	defer store.wmu.Unlock()
	if err := pass.lease.execute(`PRAGMA wal_checkpoint(TRUNCATE)`, ""); err != nil {
		log.Printf("[traffic] failed to truncate WAL: %v", err)
	}
}

func indexFootprint(directory string) int64 {
	base := filepath.Join(directory, "_index", "index.sqlite")
	var bytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if entry, err := os.Stat(base + suffix); err == nil {
			bytes += entry.Size()
		}
	}
	return bytes
}
