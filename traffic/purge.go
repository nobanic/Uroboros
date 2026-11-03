package traffic

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// trafficPurge borrows the recorder lock, index and existing reversible tree
// staging. Substring deletion and a complete purge keep distinct post-commit work.
type trafficPurge struct{ traffic *Traffic }

func (operation trafficPurge) host(host string) (int64, error) {
	like := "%" + host + "%"
	operation.traffic.wmu.Lock()
	defer operation.traffic.wmu.Unlock()
	trees, err := operation.traffic.hostTrees(`host LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	deleted, err := operation.execute(trees, `host LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		operation.traffic.reclaim()
		if err := operation.traffic.gcBlobs(); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (operation trafficPurge) all() (int64, int64, error) {
	operation.traffic.wmu.Lock()
	defer operation.traffic.wmu.Unlock()
	before := operation.traffic.indexBytes()
	trees, err := operation.traffic.allHostTrees()
	if err != nil {
		return 0, 0, err
	}
	deleted, err := operation.execute(trees, `1=1`)
	if err != nil {
		return 0, 0, err
	}
	if err := operation.traffic.gcBlobs(); err != nil {
		return deleted, 0, err
	}
	if err := operation.traffic.compactIndex(); err != nil {
		log.Printf("[traffic] failed to compact index: %v", err)
		return deleted, 0, nil
	}
	return deleted, before - operation.traffic.indexBytes(), nil
}

// A tree becomes invisible before commit. Both failure paths restore it before
// returning; collection and compaction run only after a successful commit.
func (operation trafficPurge) execute(trees []string, where string, args ...any) (int64, error) {
	transaction, err := operation.traffic.db.Begin()
	if err != nil {
		return 0, err
	}
	defer transaction.Rollback() //nolint:errcheck // no-op after commit
	deleted, err := operation.traffic.deleteWhere(transaction, where, args...)
	if err != nil {
		return 0, err
	}
	stage, moves, err := operation.traffic.stageTrees(trees)
	if err != nil {
		return 0, errors.Join(err, restoreTrees(stage, moves))
	}
	if err := transaction.Commit(); err != nil {
		return 0, errors.Join(fmt.Errorf("commit traffic index deletion: %w", err), restoreTrees(stage, moves))
	}
	operation.traffic.reapStage(stage)
	return deleted, nil
}

// Every dependent subquery reads exchanges, so its rows must be deleted last.
// The same ordered policy serves purge, exact-host staging and archive recovery.
func (operation trafficPurge) deleteRows(transaction *sql.Tx, where string, args ...any) (int64, error) {
	relations := []struct {
		table, key, source, label string
		enabled                   bool
	}{
		{"ex_fts", "rowid", "rowid", "full-text index", operation.traffic.fts},
		{"exchange_bodies", "id", "id", "bodies", true},
		{"blob_refs", "exchange_id", "id", "blob references", true},
	}
	for _, relation := range relations {
		if !relation.enabled {
			continue
		}
		statement := `DELETE FROM ` + relation.table + ` WHERE ` + relation.key + ` IN (SELECT ` + relation.source + ` FROM exchanges WHERE ` + where + `)`
		if _, err := transaction.Exec(statement, args...); err != nil {
			return 0, fmt.Errorf("delete %s: %w", relation.label, err)
		}
	}
	result, err := transaction.Exec(`DELETE FROM exchanges WHERE `+where, args...)
	if err != nil {
		return 0, fmt.Errorf("delete index rows: %w", err)
	}
	deleted, _ := result.RowsAffected()
	return deleted, nil
}
