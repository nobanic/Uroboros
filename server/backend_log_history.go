package server

import (
	"net/http"
	"time"

	"github.com/nobanic/UroborOS/db"
)

type backendLogHistory struct{ database *db.DB }

func (history backendLogHistory) serve(writer http.ResponseWriter, request *http.Request) {
	if history.database == nil {
		writeJSON(writer, 200, map[string]any{"items": []any{}, "has_more": false})
		return
	}
	before := int64(atoiDefault(request.URL.Query().Get("before"), 0))
	limit := atoiDefault(request.URL.Query().Get("limit"), 200)
	if limit > 500 {
		limit = 500
	}
	rows, err := history.read(before, limit)
	if err != nil {
		writeErr(writer, 500, "db: "+err.Error())
		return
	}
	items := make([]LogLine, 0, len(rows))
	for _, row := range rows {
		items = append(items, LogLine{DBID: row.ID, TS: row.CreatedAt.Format(time.RFC3339), Level: row.Level, Tag: row.Tag, Text: row.Text})
	}
	writeJSON(writer, 200, map[string]any{"items": items, "has_more": len(rows) == limit})
}

func (history backendLogHistory) read(before int64, limit int) ([]*db.DBLog, error) {
	if before <= 0 {
		return history.database.RecentLogs(limit)
	}
	return history.database.ListLogsBefore(before, limit)
}
