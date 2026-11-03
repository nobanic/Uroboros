package db

import (
	"fmt"
	"strconv"
	"strings"
)

// Compilation borrows the established binding cursor. Every value remains a
// parameter; columns, operators, identity policy and SQL fragments are authored
// by the existing closed field catalog, never supplied as raw filter SQL.
type assetExpressionSQL struct{ bindings *whereBuilder }

func (compiler assetExpressionSQL) tree(node *astNode) (string, error) {
	if node.kind == "leaf" {
		return compiler.leaf(*node.expr)
	}
	var separator string
	switch node.kind {
	case "and":
		separator = " AND "
	case "or":
		separator = " OR "
	default:
		return "", fmt.Errorf("unknown node kind: %s", node.kind)
	}
	parts := make([]string, 0, len(node.children))
	for _, child := range node.children {
		clause, err := compiler.tree(child)
		if err != nil {
			return "", err
		}
		parts = append(parts, "("+clause+")")
	}
	return strings.Join(parts, separator), nil
}
func (compiler assetExpressionSQL) fuzzy(value string) string {
	return compiler.bindings.next("%" + value + "%")
}
func (compiler assetExpressionSQL) fullText(value string) string {
	parameter := compiler.fuzzy(value)
	columns := fullTextCols
	if compiler.bindings.strictIdentity {
		columns = strictFullTextCols
	}
	var alternatives []string
	for _, column := range columns {
		alternatives = append(alternatives, column+" ILIKE "+parameter)
	}
	if !compiler.bindings.strictIdentity {
		for _, column := range []string{"technologies", "bound_domains"} {
			alternatives = append(alternatives, "EXISTS (SELECT 1 FROM unnest("+column+") t(v) WHERE v ILIKE "+parameter+")")
		}
	}
	return "(" + strings.Join(alternatives, " OR ") + ")"
}
func (compiler assetExpressionSQL) leaf(expression Expr) (string, error) {
	name := strings.ToLower(expression.Field)
	if name == "" {
		return compiler.fullText(expression.Value), nil
	}
	if compiler.bindings.strictIdentity {
		if _, allowed := strictIdentityStringFields[name]; !allowed && name != "port" {
			return "", fmt.Errorf("field %s is unavailable in strict task isolation because it has no per-task provenance", name)
		}
	}
	// Historical membership fields interpret all parsed operators as identity
	// equality. Keep this behavior distinct from numeric comparison operators.
	if name == "task_id" || name == "company_id" {
		identifier, err := strconv.ParseInt(expression.Value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%s requires an integer value: %s", name, expression.Value)
		}
		parameter := compiler.bindings.next(identifier)
		if name == "task_id" {
			return parameter + " = ANY(task_ids)", nil
		}
		return "company_id = " + parameter, nil
	}
	if column, found := knownNumericFields[name]; found {
		return compiler.number(name, column, expression)
	}
	if column, found := knownArrayFields[name]; found {
		return compiler.array(name, column, expression)
	}
	if column, found := knownStringFields[name]; found {
		return compiler.text(name, column, expression)
	}
	return "", fmt.Errorf("unknown field: %s", name)
}
func (compiler assetExpressionSQL) number(name, column string, expression Expr) (string, error) {
	value, err := strconv.Atoi(expression.Value)
	if err != nil {
		return "", fmt.Errorf("field %s requires an integer value: %s", name, expression.Value)
	}
	operator := expression.Op
	if operator == "==" {
		operator = "="
	}
	switch operator {
	case "=", "!=", ">", ">=", "<", "<=":
	default:
		return "", fmt.Errorf("field %s does not support operator %s", name, expression.Op)
	}
	return column + " " + operator + " " + compiler.bindings.next(value), nil
}
func (compiler assetExpressionSQL) array(name, column string, expression Expr) (string, error) {
	switch expression.Op {
	case "==":
		return compiler.bindings.next(expression.Value) + " = ANY(" + column + ")", nil
	case "!=":
		return "NOT (" + compiler.bindings.next(expression.Value) + " = ANY(" + column + "))", nil
	case "=":
		return "EXISTS (SELECT 1 FROM unnest(" + column + ") t(v) WHERE v ILIKE " + compiler.fuzzy(expression.Value) + ")", nil
	default:
		return "", fmt.Errorf("array field %s does not support operator %s", name, expression.Op)
	}
}
func (compiler assetExpressionSQL) text(name, column string, expression Expr) (string, error) {
	var operator string
	value := expression.Value
	switch expression.Op {
	case "=":
		operator = " ILIKE "
		value = "%" + value + "%"
	case "==":
		operator = " = "
	case "!=":
		operator = " NOT ILIKE "
		value = "%" + value + "%"
	default:
		return "", fmt.Errorf("string field %s does not support operator %s", name, expression.Op)
	}
	return column + operator + compiler.bindings.next(value), nil
}
