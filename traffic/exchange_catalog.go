package traffic

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Catalog selections never load message bodies. The recorder, evidence snapshot
// writer and deletion/archive transactions keep ownership of stored exchanges.
const exchangeProjection = "id,ts,host,method,url_template,url,status,content_type,resp_len,path"

type exchangeSelection struct {
	conditions []string
	arguments  []any
}

func (s *exchangeSelection) require(condition string, arguments ...any) {
	s.conditions = append(s.conditions, condition)
	s.arguments = append(s.arguments, arguments...)
}

func (s exchangeSelection) where() string {
	if len(s.conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(s.conditions, " AND ")
}

func scanExchangeMetadata(row interface{ Scan(...any) error }) (ExchangeMeta, error) {
	var item ExchangeMeta
	err := row.Scan(&item.ID, &item.TS, &item.Host, &item.Method, &item.URLTemplate,
		&item.URL, &item.Status, &item.ContentType, &item.RespLen, &item.Path)
	return item, err
}

func (t *Traffic) pageSelection(filter PageQuery) exchangeSelection {
	var selection exchangeSelection
	for _, part := range []struct{ value, condition string }{
		{strings.TrimSpace(filter.Host), "host LIKE ?"},
		{strings.TrimSpace(filter.Method), "method=?"},
		{strings.TrimSpace(filter.Path), "url_template LIKE ?"},
	} {
		if part.value == "" {
			continue
		}
		value := "%" + part.value + "%"
		if part.condition == "method=?" {
			value = strings.ToUpper(part.value)
		}
		selection.require(part.condition, value)
	}
	if condition, arguments, valid := statusFilter(filter.Status); valid {
		selection.require(condition, arguments...)
	}
	if filter.RespMin >= 0 {
		selection.require("resp_len>=?", filter.RespMin)
	}
	if filter.RespMax >= 0 {
		selection.require("resp_len<=?", filter.RespMax)
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		metadata := "host LIKE ? OR url LIKE ? OR url_template LIKE ? OR method LIKE ? OR content_type LIKE ? OR CAST(status AS TEXT) LIKE ?"
		arguments := []any{}
		for range 6 {
			arguments = append(arguments, "%"+query+"%")
		}
		if text, argument, supported := t.ftsFilter(query); supported {
			selection.require("(("+metadata+") OR "+text+")", append(arguments, argument)...)
		} else {
			selection.require("("+metadata+")", arguments...)
		}
	}
	if body := strings.TrimSpace(filter.Body); body != "" {
		if condition, argument, supported := t.ftsFilter(body); supported {
			selection.require(condition, argument)
		}
	}
	return selection
}

func (t *Traffic) Search(host string, page, size int) ([]ExchangeMeta, error) {
	if size <= 0 || size > 500 {
		size = 100
	}
	var selection exchangeSelection
	if host != "" {
		selection.require("host=?", host)
	}
	arguments := append(selection.arguments, size, page*size)
	rows, err := t.db.Query("SELECT "+exchangeProjection+" FROM exchanges"+selection.where()+" ORDER BY ts DESC LIMIT ? OFFSET ?", arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ExchangeMeta
	for rows.Next() {
		item, err := scanExchangeMetadata(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (t *Traffic) Page(filter PageQuery, page, size int) (items []ExchangeMeta, total int, err error) {
	if size <= 0 || size > 500 {
		size = 100
	}
	if page < 0 {
		page = 0
	}
	selection := t.pageSelection(filter)
	if err = t.db.QueryRow("SELECT COUNT(*) FROM exchanges"+selection.where(), selection.arguments...).Scan(&total); err != nil {
		return nil, 0, err
	}
	column := sortColumns[strings.ToLower(strings.TrimSpace(filter.Sort))]
	if column == "" {
		column = "ts"
	}
	direction := "DESC"
	if strings.EqualFold(strings.TrimSpace(filter.Order), "asc") {
		direction = "ASC"
	}
	query := "SELECT " + exchangeProjection + " FROM exchanges" + selection.where() + " ORDER BY " + column + " " + direction + ", id DESC LIMIT ? OFFSET ?"
	arguments := append(append([]any(nil), selection.arguments...), size, page*size)
	rows, err := t.db.Query(query, arguments...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanExchangeMetadata(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

type capturedMessage struct {
	header string
	body   []byte
	blob   sql.NullString
	length int
}

func (t *Traffic) Get(id string) (string, string, error) {
	var request, response capturedMessage
	err := t.db.QueryRow(`SELECT b.req_head,b.req_body,b.req_blob,b.resp_head,b.resp_body,b.resp_blob,e.req_len,e.resp_len
FROM exchange_bodies b JOIN exchanges e ON e.id=b.id WHERE b.id=?`, id).Scan(
		&request.header, &request.body, &request.blob, &response.header, &response.body, &response.blob, &request.length, &response.length)
	if err == nil {
		return assembleRaw(request.header, request.body, request.blob, request.length), assembleRaw(response.header, response.body, response.blob, response.length), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	var relative string
	if err := t.db.QueryRow("SELECT path FROM exchanges WHERE id=?", id).Scan(&relative); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(relative) == "" {
		return "", "", fmt.Errorf("exchange %s has no body record", id)
	}
	// Legacy missing files historically yield empty sides. Keep that compatibility
	// while the authoritative evidence/range readers retain their stricter errors.
	requestBytes, _ := os.ReadFile(filepath.Join(t.dir, relative, "request.http"))
	responseBytes, _ := os.ReadFile(filepath.Join(t.dir, relative, "response.http"))
	return string(requestBytes), string(responseBytes), nil
}

func (t *Traffic) Hosts() ([]HostCount, error) {
	rows, err := t.db.Query("SELECT host, COUNT(*), MAX(ts) FROM exchanges GROUP BY host ORDER BY MAX(ts) DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []HostCount
	for rows.Next() {
		var host HostCount
		var latest int64
		if err := rows.Scan(&host.Host, &host.Count, &latest); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

func (t *Traffic) Count() (int, error) {
	var count int
	err := t.db.QueryRow("SELECT COUNT(*) FROM exchanges").Scan(&count)
	return count, err
}
