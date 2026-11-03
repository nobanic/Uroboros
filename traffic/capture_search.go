package traffic

import (
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

// Native-tool search uses its original exact host/URL-port and bounded result
// policy. It shares the accepted catalog's selection/scanner without changing
// the UI Page/Search filters or their different defaults.
type captureSearch struct{ store *Traffic }

func (search captureSearch) selection(host, contains, body string) (exchangeSelection, error) {
	var selection exchangeSelection
	selection.require("1=1")
	if host != "" {
		name, port, err := normalizeSearchHost(host)
		if err != nil {
			return selection, err
		}
		selection.require("host=?", name)
		if port != "" {
			authority := net.JoinHostPort(name, port)
			selection.require("(url LIKE ? OR url LIKE ? OR url LIKE ?)", "%://"+authority+"/%", "%://"+authority+"?%", "%://"+authority)
		}
	}
	if contains != "" {
		selection.require("(url LIKE ? OR url_template LIKE ?)", "%"+contains+"%", "%"+contains+"%")
	}
	if term := strings.TrimSpace(body); term != "" {
		condition, argument, available := search.store.ftsFilter(term)
		if !available {
			if !search.store.fts {
				return selection, fmt.Errorf("the full-text index is not enabled on this instance; body search is not available")
			}
			return selection, fmt.Errorf("a body-search term needs at least %d characters (currently %d)", minTrigram, utf8.RuneCountInString(term))
		}
		selection.require(condition, argument)
	}
	return selection, nil
}
func (search captureSearch) page(host, contains, body string, page, limit int) ([]ExchangeMeta, error) {
	if limit <= 0 {
		limit = 3
	}
	if limit > 10 {
		limit = 10
	}
	if page < 0 {
		page = 0
	}
	selection, err := search.selection(host, contains, body)
	if err != nil {
		return nil, err
	}
	arguments := append(selection.arguments, limit, page*limit)
	rows, err := search.store.db.Query("SELECT "+exchangeProjection+" FROM exchanges"+selection.where()+" ORDER BY ts DESC LIMIT ? OFFSET ?", arguments...)
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
