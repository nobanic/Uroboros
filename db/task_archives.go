package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	TaskArchiveFormatVersion       = 11
	TaskArchiveLegacyFormatVersion = 1
	TaskArchiveLLMRecordsPath      = "database/llm_records.ndjson"
)

func IsTaskArchiveFormatSupported(version int) bool {
	return version >= TaskArchiveLegacyFormatVersion && version <= TaskArchiveFormatVersion
}

const (
	ArchiveQueued = "archive_queued"
	Archiving     = "archiving"
	ArchiveFailed = "archive_failed"
	ArchiveReady  = "ready"
	RestoreQueued = "restore_queued"
	Restoring     = "restoring"
	RestoreFailed = "restore_failed"
	DeleteQueued  = "delete_queued"
	Deleting      = "deleting"
	DeleteFailed  = "delete_failed"
)

var (
	ErrTaskArchiveNotFound       = errors.New("task archive not found")
	ErrTaskArchiveIneligible     = errors.New("task must be paused or terminal before archiving")
	ErrTaskArchiveQueued         = errors.New("queued task must be paused before archiving")
	ErrTaskArchiveDependent      = errors.New("task is inherited by a live task")
	ErrTaskArchiveState          = errors.New("task archive state does not allow this operation")
	ErrTaskArchiveDeleteBlocked  = errors.New("task archive is required by another archive")
	ErrTaskArchiveFormatMismatch = errors.New("task archive format is not supported")
	ErrTaskArchiveBinaryRefs     = errors.New("task binary artifacts do not have durable hot references")
)

// TaskArchive is the compact PostgreSQL record retained while a task is cold.
// Sensitive profile configuration and API keys are intentionally absent.
type TaskArchive struct {
	ID                      int64           `json:"id"`
	TaskID                  int64           `json:"task_id"`
	State                   string          `json:"state"`
	Phase                   string          `json:"phase"`
	Progress                int             `json:"progress"`
	Error                   string          `json:"error,omitempty"`
	Warnings                json.RawMessage `json:"warnings"`
	FormatVersion           int             `json:"format_version"`
	ArchivePath             string          `json:"-"`
	SHA256                  string          `json:"sha256,omitempty"`
	OriginalSize            int64           `json:"original_size"`
	CompressedSize          int64           `json:"compressed_size"`
	TaskName                string          `json:"task_name"`
	TaskDescription         string          `json:"task_description"`
	TaskGoal                string          `json:"task_goal"`
	OriginalStatus          string          `json:"original_status"`
	CategoryIDSnapshot      *int64          `json:"category_id,omitempty"`
	CategoryNameSnapshot    string          `json:"category_name,omitempty"`
	SourceTaskIDs           []int64         `json:"source_task_ids"`
	RemainingTimeoutSeconds int64           `json:"remaining_timeout_seconds"`
	DataCounts              json.RawMessage `json:"data_counts"`
	AggregateStats          json.RawMessage `json:"aggregate_stats"`
	ArchivedAt              *time.Time      `json:"archived_at,omitempty"`
	RequestedAt             time.Time       `json:"requested_at"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

// TaskArchiveBlockers returns one live direct dependent for every source task
// that cannot currently be archived. Dependents already queued for archiving do
// not block their source because the FIFO worker will compact them first.
func (d *DB) TaskArchiveBlockers() (map[int64]int64, error) {
	return collectQueryState(queriedRows(d.Query(`SELECT relation.source_task_id, MIN(child.id)
FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
LEFT JOIN task_archives pending ON pending.task_id=child.id
WHERE pending.id IS NULL OR pending.state NOT IN ('archive_queued','archiving')
GROUP BY relation.source_task_id`)), func() map[int64]int64 {
		return map[int64]int64{}
	}, func(queryRow rowDecoder, queryState *map[int64]int64) error {
		var sourceID, dependentID int64
		if err := queryRow.Scan(&sourceID, &dependentID); err != nil {
			return err
		}
		(*queryState)[sourceID] = dependentID
		return nil
	})
}

type TaskArchivePage struct {
	Items []TaskArchive `json:"items"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// TaskArchiveSnapshot is serialized into manifest.json inside the cold package.
// Small tables remain JSON arrays in Tables. Large v2 tables are streamed to
// package files listed in StreamedTables so their size is not bounded by memory.
type TaskArchiveSnapshot struct {
	FormatVersion     int                        `json:"format_version"`
	CreatedAt         time.Time                  `json:"created_at"`
	TaskID            int64                      `json:"task_id"`
	ExplorationID     int64                      `json:"exploration_id"`
	SourceTaskIDs     []int64                    `json:"source_task_ids"`
	Hosts             []string                   `json:"hosts"`
	ExclusiveHosts    []string                   `json:"exclusive_hosts"`
	ExclusiveAssetIDs []int64                    `json:"exclusive_asset_ids"`
	Tables            map[string]json.RawMessage `json:"tables"`
	StreamedTables    map[string]string          `json:"streamed_tables,omitempty"`
	DataCounts        map[string]int64           `json:"data_counts"`
	AggregateStats    map[string]any             `json:"aggregate_stats"`
}

func scanTaskArchive(sc interface{ Scan(...any) error }) (*TaskArchive, error) {
	return readArchiveRecord(sc)
}

const taskArchiveCols = `id, task_id, state, phase, progress, COALESCE(error,''), warnings,
format_version, COALESCE(archive_path,''), COALESCE(sha256,''), original_size,
compressed_size, COALESCE(task_name,''), COALESCE(task_description,''),
COALESCE(task_goal,''), COALESCE(original_status,''), category_id_snapshot,
COALESCE(category_name_snapshot,''), array_to_json(source_task_ids)::text,
remaining_timeout_seconds, data_counts, aggregate_stats, archived_at,
requested_at, created_at, updated_at`

func (d *DB) GetTaskArchive(id int64) (*TaskArchive, error) {
	return (archiveRepository{database: d}).find(id, false)
}

func (d *DB) GetTaskArchiveByTask(taskID int64) (*TaskArchive, error) {
	return (archiveRepository{database: d}).find(taskID, true)
}

func (d *DB) ListTaskArchives(search, state string, page, size int) (TaskArchivePage, error) {
	return (archiveRepository{database: d}).page(search, state, page, size)
}

// QueueTaskArchive validates lifecycle and direct inheritance while holding the
// task row. A failed archive can be explicitly retried through the same API.
func (d *DB) QueueTaskArchive(taskID int64) (*TaskArchive, error) {
	return (archiveQueue{database: d}).admit(taskID)
}

func (d *DB) QueueTaskArchiveRestore(id int64) (*TaskArchive, error) {
	return (archiveQueue{database: d}).restore(id)
}

func (d *DB) QueueTaskArchiveDelete(id int64) (*TaskArchive, error) {
	return (archiveQueue{database: d}).remove(id)
}

// RecoverTaskArchiveJobs keeps restore/delete resumable after an unclean shutdown.
// An interrupted archive requires an explicit retry: automatic startup retries can
// otherwise form a crash loop when the prior process was killed by resource limits.
func (d *DB) RecoverTaskArchiveJobs() error { return (archiveQueue{database: d}).recover() }

// ClaimTaskArchiveJob claims one persistent FIFO item for the single archive
// worker. It returns nil when the queue is empty.
func (d *DB) ClaimTaskArchiveJob(ctx context.Context) (*TaskArchive, error) {
	return (archiveQueue{database: d}).claim(ctx)
}

func (d *DB) UpdateTaskArchiveProgress(id int64, phase string, progress int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	_, err := d.Exec(`UPDATE task_archives SET phase=$2,progress=$3 WHERE id=$1`, id, phase, progress)
	return err
}

func (d *DB) AppendTaskArchiveWarning(id int64, warning string) error {
	if strings.TrimSpace(warning) == "" {
		return nil
	}
	_, err := d.Exec(`UPDATE task_archives SET warnings=warnings || jsonb_build_array($2::text) WHERE id=$1`, id, warning)
	return err
}

func (d *DB) IsTaskArchiveRestored(id int64) (bool, error) {
	var restored bool
	err := d.QueryRow(`SELECT task.deleted_at IS NULL AND task.archived_at IS NULL
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id WHERE archive.id=$1`, id).Scan(&restored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrTaskArchiveNotFound
	}
	return restored, err
}

func (d *DB) FailTaskArchiveJob(id int64, activeState string, cause error) error {
	return (archiveQueue{database: d}).fail(id, activeState, cause)
}

func queryArchiveRows(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, inner string, args ...any) (json.RawMessage, int64, error) {
	return (archiveRowQuery{source: q, statement: inner, values: args}).array()
}

func encodeArchiveRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) (json.RawMessage, int64, error) {
	return archiveRowArray(rows)
}

func writeArchiveRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, writer io.Writer) (int64, error) {
	return archiveRowSequence(rows, writer)
}

func streamArchiveRows(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, writer io.Writer, inner string, args ...any) (int64, error) {
	return (archiveRowQuery{source: q, statement: inner, values: args}).sequence(writer)
}

func rawRowCount(raw json.RawMessage) int64 {
	return int64(len(archiveInput(raw).array()))
}

func archiveAssetIDsQuery() string {
	return `SELECT id FROM assets WHERE $1=ANY(task_ids)
UNION SELECT link.asset_id FROM task_asset_links link WHERE link.task_id=$1
UNION SELECT anchor.asset_id FROM exploration_anchors anchor
      JOIN exploration_nodes node ON node.id=anchor.node_id WHERE node.exploration_id=$2
UNION SELECT value::bigint FROM findings finding
      CROSS JOIN LATERAL jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(finding.asset_ids)='array' THEN finding.asset_ids ELSE '[]'::jsonb END
      ) value WHERE finding.task_id=$1 AND value ~ '^[0-9]+$'
UNION SELECT discovery.asset_id FROM katana_discoveries discovery
      WHERE discovery.task_id=$1 AND discovery.asset_id IS NOT NULL
UNION SELECT observation.asset_id FROM arsenal_observations observation
      WHERE observation.task_id=$1 AND observation.asset_id IS NOT NULL`
}

// SnapshotTaskArchive reads one repeatable PostgreSQL snapshot. Task-owned Agent
// writes are already quiescent at the server barrier; repeatable-read also keeps
// the asset and accounting views mutually consistent during serialization.
func (d *DB) SnapshotTaskArchive(taskID int64) (*TaskArchiveSnapshot, error) {
	return d.snapshotTaskArchive(taskID, nil)
}

// SnapshotTaskArchiveWithLLMRecords streams the heavyweight record history to
// llmRecords while all other task-owned data is read from the same repeatable
// PostgreSQL snapshot.
func (d *DB) SnapshotTaskArchiveWithLLMRecords(taskID int64, llmRecords io.Writer) (*TaskArchiveSnapshot, error) {
	if llmRecords == nil {
		return nil, errors.New("nil LLM record archive writer")
	}
	return d.snapshotTaskArchive(taskID, llmRecords)
}

func (d *DB) snapshotTaskArchive(taskID int64, llmRecords io.Writer) (*TaskArchiveSnapshot, error) {
	return readArchiveDataset(d, taskID, llmRecords)
}

func archiveAssetMetadata(tx *sql.Tx, taskID, expID int64, assetRows json.RawMessage) ([]int64, []int64, []string, []string, error) {
	var rows []map[string]any
	if err := json.Unmarshal(assetRows, &rows); err != nil {
		return nil, nil, nil, nil, err
	}
	allHosts := map[string]struct{}{}
	assetIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if id, ok := jsonInt64(row["id"]); ok {
			assetIDs = append(assetIDs, id)
		}
		for _, key := range []string{"domain", "ip"} {
			if value, _ := row[key].(string); strings.TrimSpace(value) != "" {
				allHosts[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
			}
		}
		if rawURL, _ := row["url"].(string); rawURL != "" {
			if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
				allHosts[strings.ToLower(parsed.Hostname())] = struct{}{}
			}
		}
	}
	exclusiveHosts, err := hostsForTaskDeletion(tx, taskID, expID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rowsID, err := tx.Query(`WITH candidate AS (`+archiveAssetIDsQuery()+`)
SELECT asset.id FROM assets asset JOIN candidate ON candidate.id=asset.id
WHERE asset.company_id IS NULL
AND NOT EXISTS (SELECT 1 FROM tasks task WHERE task.id<>$1 AND task.deleted_at IS NULL AND task.id=ANY(asset.task_ids))
AND NOT EXISTS (
 SELECT 1 FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id
 JOIN tasks task ON task.exploration_id=node.exploration_id
 WHERE anchor.asset_id=asset.id AND task.id<>$1 AND task.deleted_at IS NULL
) ORDER BY asset.id`, taskID, expID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var exclusiveIDs []int64
	for rowsID.Next() {
		var id int64
		if err := rowsID.Scan(&id); err != nil {
			rowsID.Close()
			return nil, nil, nil, nil, err
		}
		exclusiveIDs = append(exclusiveIDs, id)
	}
	if err := rowsID.Close(); err != nil {
		return nil, nil, nil, nil, err
	}
	hosts := make([]string, 0, len(allHosts))
	for host := range allHosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return assetIDs, exclusiveIDs, hosts, exclusiveHosts, nil
}

func taskArchiveAggregates(tx *sql.Tx, taskID, expID int64) (map[string]any, error) {
	return (archiveUsageSnapshot{transaction: tx, task: taskID, exploration: expID}).read()
}

func jsonInt64(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), value == float64(int64(value))
	case json.Number:
		id, err := value.Int64()
		return id, err == nil
	case string:
		id, err := strconv.ParseInt(value, 10, 64)
		return id, err == nil
	default:
		return 0, false
	}
}
