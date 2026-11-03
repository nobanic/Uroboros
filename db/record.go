package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type rowDecoder interface{ Scan(...any) error }

func scanRecordRows[T any](rows interface {
	rowDecoder
	Next() bool
	Err() error
}, decode func(rowDecoder) (T, error)) ([]T, error) {
	var records []T
	return collectRowValues(rows, records, func() (T, error) {
		return decode(rows)
	})
}

// A record field owns one storage name and both directions of its conversion.
// Column lists, scan targets and write values therefore cannot drift apart.
// These definitions are authored by adapters; HTTP input cannot supply SQL names.
type recordField struct {
	name      string
	target    any
	value     func() any
	fallback  string
	nullEmpty bool
	readOnly  bool
	afterRead func()
}

func field[T any](name string, target *T) recordField {
	return recordField{name: name, target: target, value: func() any { return *target }}
}

func (column recordField) defaultRead(expression string) recordField {
	column.fallback = expression
	return column
}
func (column recordField) nullableText() recordField { column.nullEmpty = true; return column }
func (column recordField) immutable() recordField    { column.readOnly = true; return column }

// Nullable text reads retain the SQL column and NullString scan semantics while
// committing its public string only after the complete row has decoded.
func readNullableText(name string, target *string) recordField {
	stored := new(sql.NullString)
	return recordField{name: name, target: stored, value: func() any { return *target }, afterRead: func() { *target = stored.String }}
}

func recordProjection(fields []recordField) string {
	columns := make([]string, len(fields))
	for index, column := range fields {
		columns[index] = column.name
		if column.fallback != "" {
			columns[index] = "COALESCE(" + column.name + ", " + column.fallback + ")"
		}
	}
	return strings.Join(columns, ", ")
}

func decodeRecord(row rowDecoder, fields []recordField) error {
	targets := make([]any, len(fields))
	for index, column := range fields {
		targets[index] = column.target
	}
	if err := row.Scan(targets...); err != nil {
		return err
	}
	for _, column := range fields {
		if column.afterRead != nil {
			column.afterRead()
		}
	}
	return nil
}

type recordConnection interface {
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

type recordWriteSet struct {
	names, expressions, assignments []string
	values                          []any
}

func recordChanges(fields []recordField) recordWriteSet {
	var changes recordWriteSet
	for _, column := range fields {
		if column.readOnly {
			continue
		}
		changes.values = append(changes.values, column.value())
		binding := fmt.Sprintf("$%d", len(changes.values))
		if column.nullEmpty {
			binding = "NULLIF(" + binding + ", '')"
		}
		changes.names = append(changes.names, column.name)
		changes.expressions = append(changes.expressions, binding)
		changes.assignments = append(changes.assignments, column.name+" = "+binding)
	}
	return changes
}

func persistRecord(database recordConnection, table string, id int64, fields []recordField) (int64, error) {
	changes := recordChanges(fields)
	if id == 0 {
		statement := "INSERT INTO " + table + " (" + strings.Join(changes.names, ", ") + ") VALUES (" + strings.Join(changes.expressions, ", ") + ") RETURNING id"
		failure := database.QueryRow(statement, changes.values...).Scan(&id)
		return id, failure
	}
	changes.values = append(changes.values, id)
	statement := "UPDATE " + table + " SET " + strings.Join(changes.assignments, ", ") + fmt.Sprintf(" WHERE id = $%d", len(changes.values))
	_, failure := database.Exec(statement, changes.values...)
	return id, failure
}

// Catalogs can use stable text keys instead of generated numeric IDs. Names
// and guards are adapter-authored constants; only values come from callers.
func persistKeyedRecord(database recordConnection, table string, key recordField, fields []recordField, create bool, guard string) error {
	changes := recordChanges(fields)
	var statement string
	if create {
		statement = "INSERT INTO " + table + " (" + strings.Join(changes.names, ", ") + ") VALUES (" + strings.Join(changes.expressions, ", ") + ")"
	} else {
		changes.values = append(changes.values, key.value())
		statement = "UPDATE " + table + " SET " + strings.Join(changes.assignments, ", ") + fmt.Sprintf(" WHERE %s = $%d", key.name, len(changes.values))
		if guard != "" {
			statement += " AND " + guard
		}
	}
	_, failure := database.Exec(statement, changes.values...)
	return failure
}
