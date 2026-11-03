package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// One dataset owns the payload/count/path relationship inside the existing
// repeatable-read transaction. Large records go to the supplied writer while
// the manifest retains its empty array and the exact streamed-table path.
type archiveDataset struct {
	transaction       *sql.Tx
	task, exploration int64
	tables            map[string]json.RawMessage
	counts            map[string]int64
	streams           map[string]string
}

func (dataset *archiveDataset) capture(writer io.Writer) error {
	for _, table := range archiveSnapshotReads(dataset.task, dataset.exploration) {
		query := archiveRowQuery{source: dataset.transaction, statement: table.statement(), values: table.values}
		var payload json.RawMessage
		var count int64
		var err error
		if table.name == "llm_records" && writer != nil {
			count, err = query.sequence(writer)
			payload = json.RawMessage("[]")
			if err == nil {
				dataset.streams[table.name] = TaskArchiveLLMRecordsPath
			}
		} else {
			payload, count, err = query.array()
		}
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", table.name, err)
		}
		dataset.tables[table.name], dataset.counts[table.name] = payload, count
	}
	return nil
}

func (dataset *archiveDataset) sources() ([]int64, error) {
	rows, err := dataset.transaction.Query(`SELECT source_task_id FROM task_relations WHERE task_id=$1 ORDER BY created_at,source_task_id`, dataset.task)
	if err != nil {
		return nil, err
	}
	var sources []int64
	for rows.Next() {
		var source int64
		if err := rows.Scan(&source); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, source)
	}
	// Retain the original close-error boundary (without adding a Rows.Err check).
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return sources, nil
}

func (dataset *archiveDataset) manifest() (*TaskArchiveSnapshot, error) {
	_, exclusiveIDs, hosts, exclusiveHosts, err := archiveAssetMetadata(dataset.transaction, dataset.task, dataset.exploration, dataset.tables["assets"])
	if err != nil {
		return nil, err
	}
	sources, err := dataset.sources()
	if err != nil {
		return nil, err
	}
	aggregates, err := taskArchiveAggregates(dataset.transaction, dataset.task, dataset.exploration)
	if err != nil {
		return nil, err
	}
	return &TaskArchiveSnapshot{
		FormatVersion: TaskArchiveFormatVersion, CreatedAt: time.Now().UTC(),
		TaskID: dataset.task, ExplorationID: dataset.exploration, SourceTaskIDs: sources,
		Hosts: hosts, ExclusiveHosts: exclusiveHosts, ExclusiveAssetIDs: exclusiveIDs,
		Tables: dataset.tables, StreamedTables: dataset.streams, DataCounts: dataset.counts,
		AggregateStats: aggregates,
	}, nil
}

func readArchiveDataset(database *DB, taskID int64, writer io.Writer) (*TaskArchiveSnapshot, error) {
	transaction, err := database.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	if err := coordinateWithSchemaMigration(transaction); err != nil {
		return nil, err
	}
	var explorationID int64
	if err := transaction.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&explorationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskArchiveNotFound
		}
		return nil, err
	}
	dataset := archiveDataset{transaction: transaction, task: taskID, exploration: explorationID,
		tables: make(map[string]json.RawMessage), counts: make(map[string]int64), streams: make(map[string]string)}
	if err := dataset.capture(writer); err != nil {
		return nil, err
	}
	manifest, err := dataset.manifest()
	if err != nil {
		return nil, err
	}
	return manifest, transaction.Commit()
}
