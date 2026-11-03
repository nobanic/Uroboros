package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type recordQueryConnection interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// Expressions and storage names are adapter-authored. Only bound values come
// from API callers; repeated occurrences of one predicate share one binding.
type recordPredicate struct {
	present    bool
	value      any
	expression func(string) string
}
type recordCriteria struct {
	where  string
	values []any
}

func recordEquals(column string, value any, present bool) recordPredicate {
	return recordPredicate{present: present, value: value, expression: func(binding string) string { return column + " = " + binding }}
}
func recordLikeAny(columns []string, value string, present bool) recordPredicate {
	return recordPredicate{present: present, value: value, expression: func(binding string) string {
		parts := make([]string, len(columns))
		for index, column := range columns {
			parts[index] = column + " ILIKE " + binding
		}
		if len(parts) == 1 {
			return parts[0]
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}}
}
func compileRecordCriteria(base string, predicates ...recordPredicate) recordCriteria {
	parts, values := []string{base}, []any{}
	for _, predicate := range predicates {
		if !predicate.present {
			continue
		}
		values = append(values, predicate.value)
		parts = append(parts, predicate.expression(fmt.Sprintf("$%d", len(values))))
	}
	return recordCriteria{where: "WHERE " + strings.Join(parts, " AND "), values: values}
}

type recordPage struct{ size, offset int }

func recordPageWindow(page, size int) recordPage {
	if page < 0 {
		page = 0
	}
	if size <= 0 {
		size = 50
	}
	return recordPage{size: size, offset: page * size}
}

// A selection owns columns, row decoding, empty-result policy and ordering.
// Paged reads retain separate count/data statements and the original failure
// boundaries; they do not introduce a transaction or a read-size ceiling.
type recordSelect[T any] struct {
	from, countFrom, groupBy, orderBy string
	fields                            func(*T) []recordField
	nonNil                            bool
}
type recordBatch[T any] struct {
	items          []T
	failure        error
	iterationError error
}

func (plan recordSelect[T]) statement(criteria recordCriteria, window *recordPage) (string, []any) {
	statement := "SELECT " + recordProjection(plan.fields(new(T))) + " " + plan.from + " " + criteria.where
	if plan.groupBy != "" {
		statement += " GROUP BY " + plan.groupBy
	}
	if plan.orderBy != "" {
		statement += " ORDER BY " + plan.orderBy
	}
	values := append([]any{}, criteria.values...)
	if window != nil {
		statement += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(values)+1, len(values)+2)
		values = append(values, window.size, window.offset)
	}
	return statement, values
}
func (plan recordSelect[T]) collect(database recordQueryConnection, criteria recordCriteria, window *recordPage) recordBatch[T] {
	var items []T
	if plan.nonNil {
		items = []T{}
	}
	batch := plan.visit(database, criteria, window, func(item T) { items = append(items, item) })
	if batch.failure == nil {
		batch.items = items
	}
	return batch
}

// Visiting keeps large joined contexts streaming. Query/scan failure remains
// distinct from a final iterator error, including for callers retaining partial
// results; the cursor always closes before another query starts.
func (plan recordSelect[T]) visit(database recordQueryConnection, criteria recordCriteria, window *recordPage, accept func(T)) recordBatch[T] {
	statement, values := plan.statement(criteria, window)
	rows, err := database.Query(statement, values...)
	if err != nil {
		return recordBatch[T]{failure: err}
	}
	defer rows.Close()
	finished, err := visitRows(rows, func() error {
		var item T
		if err := decodeRecord(rows, plan.fields(&item)); err != nil {
			return err
		}
		accept(item)
		return nil
	})
	if !finished {
		return recordBatch[T]{failure: err}
	}
	return recordBatch[T]{iterationError: err}
}
func (plan recordSelect[T]) all(database recordQueryConnection, criteria recordCriteria) ([]T, error) {
	batch := plan.collect(database, criteria, nil)
	if batch.failure != nil {
		return nil, batch.failure
	}
	return batch.items, batch.iterationError
}
func (plan recordSelect[T]) page(database recordQueryConnection, criteria recordCriteria, page, size int) ([]T, int, error) {
	countFrom := plan.countFrom
	if countFrom == "" {
		countFrom = plan.from
	}
	var total int
	if err := database.QueryRow("SELECT COUNT(*) "+countFrom+" "+criteria.where, criteria.values...).Scan(&total); err != nil {
		return nil, 0, err
	}
	window := recordPageWindow(page, size)
	batch := plan.collect(database, criteria, &window)
	if batch.failure != nil {
		return nil, 0, batch.failure
	}
	return batch.items, total, batch.iterationError
}
func (plan recordSelect[T]) one(database recordQueryConnection, criteria recordCriteria) (*T, error) {
	statement, values := plan.statement(criteria, nil)
	item := new(T)
	if err := decodeRecord(database.QueryRow(statement, values...), plan.fields(item)); err != nil {
		return nil, err
	}
	return item, nil
}
