package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type scopeCellMode uint8

const (
	scopeBound scopeCellMode = iota
	scopeDomainLiteral
	scopeNetworkBound
)

type scopeCell struct {
	name  string
	value any
	mode  scopeCellMode
}

// One layout owns the storage columns, bind order, literal domain kind, network cast and conflict rule.
func scopeInsert(companyID int64, rule ParsedScope, reason string) (string, []any, error) {
	cells := []scopeCell{{name: "company_id", value: companyID}}
	var conflict string
	switch rule.Kind {
	case "domain":
		cells = append(cells, scopeCell{name: "kind", mode: scopeDomainLiteral}, scopeCell{name: "domain", value: rule.Domain})
		conflict = "ON CONFLICT ON CONSTRAINT uq_sv2_domain DO NOTHING"
	case "ip", "cidr":
		cells = append(cells, scopeCell{name: "kind", value: rule.Kind}, scopeCell{name: "net", value: rule.Net, mode: scopeNetworkBound})
		conflict = "ON CONFLICT ON CONSTRAINT uq_sv2_net DO NOTHING"
	case "icp", "keyword":
		cells = append(cells, scopeCell{name: "kind", value: rule.Kind}, scopeCell{name: "value", value: rule.Value})
		conflict = "ON CONFLICT (company_id, kind, value) WHERE kind IN ('icp','keyword') DO NOTHING"
	default:
		return "", nil, fmt.Errorf("unsupported company scope kind %q", rule.Kind)
	}
	cells = append(cells, scopeCell{name: "raw", value: rule.Raw}, scopeCell{name: "reason", value: reason})
	columns, expressions := make([]string, len(cells)), make([]string, len(cells))
	values := make([]any, 0, len(cells))
	for index, cell := range cells {
		columns[index] = cell.name
		if cell.mode == scopeDomainLiteral {
			expressions[index] = "'domain'"
			continue
		}
		values = append(values, cell.value)
		expressions[index] = fmt.Sprintf("$%d", len(values))
		if cell.mode == scopeNetworkBound {
			expressions[index] += "::cidr"
		}
	}
	return "INSERT INTO company_scope(" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(expressions, ", ") + ") " + conflict, values, nil
}

func writeScopeRule(tx *sql.Tx, companyID int64, rule ParsedScope, reason string) (bool, error) {
	statement, values, err := scopeInsert(companyID, rule, reason)
	if err != nil {
		return false, err
	}
	result, err := tx.Exec(statement, values...)
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count > 0, nil
}
