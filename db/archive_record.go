package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type archiveRecord struct {
	value   TaskArchive
	sources string
}

func (record *archiveRecord) fields() []recordField {
	item := &record.value
	return []recordField{
		field("id", &item.ID), field("task_id", &item.TaskID),
		field("state", &item.State), field("phase", &item.Phase),
		field("progress", &item.Progress), field("error", &item.Error),
		field("warnings", &item.Warnings), field("format_version", &item.FormatVersion),
		field("archive_path", &item.ArchivePath), field("sha256", &item.SHA256),
		field("original_size", &item.OriginalSize), field("compressed_size", &item.CompressedSize),
		field("task_name", &item.TaskName), field("task_description", &item.TaskDescription),
		field("task_goal", &item.TaskGoal), field("original_status", &item.OriginalStatus),
		field("category_id_snapshot", &item.CategoryIDSnapshot), field("category_name_snapshot", &item.CategoryNameSnapshot),
		field("source_task_ids", &record.sources), field("remaining_timeout_seconds", &item.RemainingTimeoutSeconds),
		field("data_counts", &item.DataCounts), field("aggregate_stats", &item.AggregateStats),
		field("archived_at", &item.ArchivedAt), field("requested_at", &item.RequestedAt),
		field("created_at", &item.CreatedAt), field("updated_at", &item.UpdatedAt),
	}
}

func readArchiveRecord(row rowDecoder) (*TaskArchive, error) {
	record := new(archiveRecord)
	if err := decodeRecord(row, record.fields()); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(record.sources), &record.value.SourceTaskIDs); err != nil {
		return nil, err
	}
	for target, fallback := range map[*json.RawMessage]string{
		&record.value.Warnings: "[]", &record.value.DataCounts: "{}", &record.value.AggregateStats: "{}",
	} {
		if len(*target) == 0 {
			*target = json.RawMessage(fallback)
		}
	}
	return &record.value, nil
}

// The repository borrows the current connection; queue/claim/restore transaction
// owners keep using their original row locks and the same public decoder.
type archiveRepository struct{ database *DB }

func (repository archiveRepository) find(id int64, byTask bool) (*TaskArchive, error) {
	column := "id"
	if byTask {
		column = "task_id"
	}
	item, err := readArchiveRecord(repository.database.QueryRow("SELECT "+taskArchiveCols+" FROM task_archives WHERE "+column+"=$1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (repository archiveRepository) page(search, state string, page, size int) (TaskArchivePage, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	result := TaskArchivePage{Page: page, Size: size}
	search, state = strings.TrimSpace(search), strings.TrimSpace(state)
	criteria := `WHERE ($1='' OR task_id::text ILIKE '%'||$1||'%' OR task_name ILIKE '%'||$1||'%' OR task_description ILIKE '%'||$1||'%')
AND ($2='' OR state=$2)`
	if err := repository.database.QueryRow("SELECT count(*) FROM task_archives "+criteria, search, state).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := repository.database.Query("SELECT "+taskArchiveCols+" FROM task_archives "+criteria+`
ORDER BY COALESCE(archived_at, requested_at) DESC, id DESC LIMIT $3 OFFSET $4`, search, state, size, (page-1)*size)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := readArchiveRecord(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, *item)
	}
	return result, rows.Err()
}
