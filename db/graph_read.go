package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// graphRead carries its exploration boundary from construction through every
// predicate and projection. Callers describe a selection, rather than assembling
// independent SQL statements with separately maintained parameter numbers.
type graphRead struct {
	store  *ExplorationStore
	terms  []string
	values []any
}

func graphRows[T any](database *DB, statement string, values []any, decode func(rowDecoder) (T, error)) ([]T, error) {
	rows, failure := database.Query(statement, values...)
	if failure != nil {
		return nil, failure
	}
	defer rows.Close()
	return scanRecordRows(rows, decode)
}

func readGraph(store *ExplorationStore) *graphRead {
	return &graphRead{store: store, terms: []string{"exploration_id = $1"}, values: []any{store.expID}}
}

func (selection *graphRead) bind(value any) string {
	selection.values = append(selection.values, value)
	return "$" + strconv.Itoa(len(selection.values))
}

func (selection *graphRead) equal(column string, value any) *graphRead {
	selection.terms = append(selection.terms, column+" = "+selection.bind(value))
	return selection
}

func (selection *graphRead) oneOf(column string, values []string) *graphRead {
	if len(values) > 0 {
		marks := make([]string, len(values))
		for index, value := range values {
			marks[index] = selection.bind(value)
		}
		selection.terms = append(selection.terms, column+" IN ("+strings.Join(marks, ", ")+")")
	}
	return selection
}

func (selection *graphRead) identifiers(columns []string, ids []int64) *graphRead {
	if len(ids) == 0 {
		selection.terms = append(selection.terms, "FALSE")
		return selection
	}
	marks := make([]string, len(ids))
	for index, id := range ids {
		marks[index] = selection.bind(id)
	}
	var alternatives []string
	for _, column := range columns {
		alternatives = append(alternatives, column+" IN ("+strings.Join(marks, ", ")+")")
	}
	selection.terms = append(selection.terms, "("+strings.Join(alternatives, " OR ")+")")
	return selection
}

func (selection *graphRead) before(cursor int64) *graphRead {
	if cursor > 0 {
		selection.terms = append(selection.terms, "id < "+selection.bind(cursor))
	}
	return selection
}

func (selection *graphRead) summary(query string) *graphRead {
	if query != "" {
		selection.terms = append(selection.terms, "payload->>'summary' ILIKE "+selection.bind("%"+query+"%"))
	}
	return selection
}

func (selection *graphRead) search(query string) *graphRead {
	query = strings.TrimSpace(query)
	if query == "" {
		return selection
	}
	pattern := selection.bind("%" + query + "%")
	match := []string{"payload::text ILIKE " + pattern, "COALESCE(origin,'') ILIKE " + pattern}
	if id, failure := strconv.ParseInt(strings.TrimPrefix(query, "#"), 10, 64); failure == nil {
		match = append(match, "id = "+selection.bind(id))
	}
	selection.terms = append(selection.terms, "("+strings.Join(match, " OR ")+")")
	return selection
}

func (selection *graphRead) from(table string) string {
	return " FROM " + table + " WHERE " + strings.Join(selection.terms, " AND ")
}

func (selection *graphRead) count() (int, error) {
	var count int
	failure := selection.store.db.QueryRow("SELECT COUNT(*)"+selection.from("exploration_nodes"), selection.values...).Scan(&count)
	return count, failure
}

func (selection *graphRead) exists() (bool, error) {
	var found bool
	failure := selection.store.db.QueryRow("SELECT EXISTS (SELECT 1"+selection.from("exploration_nodes")+")", selection.values...).Scan(&found)
	return found, failure
}

func (selection *graphRead) nodes(order string, limit int, offset *int) ([]*Node, error) {
	statement := "SELECT " + nodeCols + selection.from("exploration_nodes") + " ORDER BY " + order
	// Keep projections reusable: limit/offset bindings belong to this execution,
	// not the selection used by the matching-count query.
	values := append([]any(nil), selection.values...)
	statement += fmt.Sprintf(" LIMIT $%d", len(values)+1)
	values = append(values, limit)
	if offset != nil {
		statement += fmt.Sprintf(" OFFSET $%d", len(values)+1)
		values = append(values, *offset)
	}
	return graphRows(selection.store.db, statement, values, func(row rowDecoder) (*Node, error) { return scanNode(row) })
}

func (selection *graphRead) edges(limit *int) ([]Edge, error) {
	statement := "SELECT src_id, rel, dst_id" + selection.from("exploration_edges")
	values := append([]any(nil), selection.values...)
	if limit != nil {
		statement += fmt.Sprintf(" LIMIT $%d", len(values)+1)
		values = append(values, *limit)
	}
	return graphRows(selection.store.db, statement, values, func(row rowDecoder) (Edge, error) {
		var edge Edge
		failure := row.Scan(&edge.From, &edge.Rel, &edge.To)
		return edge, failure
	})
}

func graphLimit(requested, fallback int) int {
	if requested <= 0 {
		return fallback
	}
	return requested
}

func graphPage(nodes []*Node, capacity int, failure error) ([]*Node, bool, error) {
	if failure != nil {
		return nil, false, failure
	}
	more := len(nodes) > capacity
	if more {
		nodes = nodes[:capacity]
	}
	return nodes, more, nil
}

// sql.ErrNoRows is handled at single-record boundaries. Collection reads keep
// their original nil-slice convention, which existing DTOs normalize separately.
var _ rowDecoder = (*sql.Row)(nil)
