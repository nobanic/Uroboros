package db

import "strings"

// The schema plan owns statement order. These definitions share column policies
// and object construction; migration/function bodies stay literal SQL in the plan.
type ddlType string
type ddlRule string

const (
	ddlBigSerial   ddlType = "BIGSERIAL"
	ddlText        ddlType = "TEXT"
	ddlTimestampTZ ddlType = "TIMESTAMPTZ"
	ddlBigInt      ddlType = "BIGINT"
	ddlBigIntArray ddlType = "BIGINT[]"
	ddlCIDR        ddlType = "CIDR"
	ddlInteger     ddlType = "INTEGER"
	ddlTextArray   ddlType = "TEXT[]"
	ddlJSONBArray  ddlType = "JSONB[]"
	ddlJSONB       ddlType = "JSONB"
	ddlInt         ddlType = "INT"
	ddlBoolean     ddlType = "BOOLEAN"
	ddlDouble      ddlType = "DOUBLE PRECISION"
	ddlBytes       ddlType = "BYTEA"

	ddlRequired    ddlRule = "NOT NULL"
	ddlPrimary     ddlRule = "PRIMARY KEY"
	ddlUnique      ddlRule = "UNIQUE"
	ddlEmpty       ddlRule = "DEFAULT ''"
	ddlZero        ddlRule = "DEFAULT 0"
	ddlEmptyObject ddlRule = "DEFAULT '{}'"
	ddlEmptyArray  ddlRule = "DEFAULT '[]'"
	ddlFalse       ddlRule = "DEFAULT false"
	ddlTrue        ddlRule = "DEFAULT true"
)

type ddlField struct {
	name       string
	kind       ddlType
	rules      []ddlRule
	constraint string
}

func ddlColumn(name string, kind ddlType, rules ...ddlRule) ddlField {
	return ddlField{name: name, kind: kind, rules: rules}
}

func ddlSequence(name string) ddlField { return ddlColumn(name, ddlBigSerial, ddlPrimary) }
func ddlTimestamp(name string) ddlField {
	return ddlColumn(name, ddlTimestampTZ, ddlRequired, ddlDefault("now()"))
}
func ddlDefault(expression string) ddlRule { return ddlRule("DEFAULT " + expression) }
func ddlCheck(expression string) ddlRule   { return ddlRule("CHECK " + expression) }
func ddlReferences(table, column, onDelete string) ddlRule {
	clause := "REFERENCES " + table + "(" + column + ")"
	if onDelete != "" {
		clause += " ON DELETE " + onDelete
	}
	return ddlRule(clause)
}
func ddlConstraint(expression string) ddlField { return ddlField{constraint: expression} }

type ddlSchema struct{ strings.Builder }

func (p *ddlSchema) raw(statement string) {
	// The frozen SQL used CRLF, including inside stored function bodies. Go raw
	// literals discard CR, so restore that representation before emitting them.
	p.WriteString(strings.ReplaceAll(statement, "\n", "\r\n"))
}

func (p *ddlSchema) table(name string, fields ...ddlField) {
	p.WriteString("CREATE TABLE IF NOT EXISTS " + name + " (\n")
	for i, field := range fields {
		p.WriteString("    ")
		if field.constraint != "" {
			p.WriteString(field.constraint)
		} else {
			p.WriteString(field.name + " " + string(field.kind))
			for _, rule := range field.rules {
				p.WriteString(" " + string(rule))
			}
		}
		if i+1 < len(fields) {
			p.WriteByte(',')
		}
		p.WriteByte('\n')
	}
	p.WriteString(");\n")
}

func (p *ddlSchema) index(name, table string, unique bool, expression string) {
	p.WriteString("CREATE ")
	if unique {
		p.WriteString("UNIQUE ")
	}
	p.WriteString("INDEX IF NOT EXISTS " + name + " ON " + table + " " + expression + ";\n")
}

func (p *ddlSchema) updatedTrigger(name, table string) {
	p.WriteString("DROP TRIGGER IF EXISTS " + name + " ON " + table + ";\n")
	p.WriteString("CREATE TRIGGER " + name + " BEFORE UPDATE ON " + table + " FOR EACH ROW EXECUTE FUNCTION set_updated_at();\n")
}
