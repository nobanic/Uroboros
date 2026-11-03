package db

import (
	"fmt"
	"strings"
	"unicode"
)

// The grammar keeps the existing byte-oriented language, including its
// permissive quoted words. Unicode byte scanning is compatibility behavior;
// changing it here would change persisted/user-authored filter interpretation.
type assetLexemeCursor struct {
	text     string
	position int
}

func readAssetWord(text string, position int) (string, int) {
	if position >= len(text) {
		return "", position
	}
	if text[position] == '"' {
		start := position + 1
		closing := strings.IndexByte(text[start:], '"')
		if closing < 0 {
			return text[start:], len(text)
		}
		return text[start : start+closing], start + closing + 1
	}
	end := position
	for end < len(text) && !unicode.IsSpace(rune(text[end])) && text[end] != '(' && text[end] != ')' {
		end++
	}
	return text[position:end], end
}

func readAssetClause(text string, position int) (Expr, int, bool) {
	end := position
	if end >= len(text) || !isIdentStart(text[end]) {
		return Expr{}, position, false
	}
	for end < len(text) && isIdentChar(text[end]) {
		end++
	}
	field := strings.ToLower(text[position:end])
	if !isKnownField(field) || end == len(text) {
		return Expr{}, position, false
	}
	operatorEnd := end + 1
	if operatorEnd < len(text) && strings.ContainsRune("=!><", rune(text[end])) && text[operatorEnd] == '=' {
		operatorEnd++
	} else if !strings.ContainsRune("><=", rune(text[end])) {
		return Expr{}, position, false
	}
	value, after := readAssetWord(text, operatorEnd)
	if after == operatorEnd {
		return Expr{}, position, false
	}
	return Expr{Field: field, Op: text[end:operatorEnd], Value: value}, after, true
}

func (cursor *assetLexemeCursor) next() (tok, bool) {
	for cursor.position < len(cursor.text) {
		index := cursor.position
		character := cursor.text[index]
		if unicode.IsSpace(rune(character)) {
			cursor.position++
			continue
		}
		if character == '(' || character == ')' {
			cursor.position++
			kind := tkLP
			if character == ')' {
				kind = tkRP
			}
			return tok{kind: kind}, true
		}
		if clause, end, ok := readAssetClause(cursor.text, index); ok {
			cursor.position = end
			return tok{kind: tkField, expr: &clause}, true
		}
		word, end := readAssetWord(cursor.text, index)
		if word == "" {
			// Empty standalone quotes historically advance one byte, rather than
			// skipping the quoted span. Field values use their own end position.
			cursor.position++
			continue
		}
		cursor.position = end
		switch strings.ToUpper(word) {
		case "AND":
			return tok{kind: tkAnd}, true
		case "OR":
			return tok{kind: tkOr}, true
		}
		return tok{kind: tkBare, expr: &Expr{Field: "", Op: "=", Value: word}}, true
	}
	return tok{kind: tkEOF}, false
}

func scanAssetTokens(text string) []tok {
	cursor := assetLexemeCursor{text: text}
	var tokens []tok
	for {
		token, more := cursor.next()
		tokens = append(tokens, token)
		if !more {
			return tokens
		}
	}
}

// Both precedence levels share one sequence parser. A single child is returned
// unchanged; explicit parentheses never flatten the original tree structure.
type assetGrammar struct{ cursor *dslParser }

func (grammar assetGrammar) expression(level int) (*astNode, error) {
	operators := [2]string{tkOr, tkAnd}
	if level == len(operators) {
		return grammar.atom()
	}
	first, err := grammar.expression(level + 1)
	if err != nil {
		return nil, err
	}
	children := []*astNode{first}
	for grammar.cursor.peek().kind == operators[level] {
		grammar.cursor.consume()
		child, err := grammar.expression(level + 1)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	if len(children) == 1 {
		return first, nil
	}
	constructors := [2]func([]*astNode) *astNode{orNode, andNode}
	return constructors[level](children), nil
}
func (grammar assetGrammar) atom() (*astNode, error) {
	current := grammar.cursor.peek()
	switch current.kind {
	case tkField, tkBare:
		grammar.cursor.consume()
		return leafNode(*current.expr), nil
	case tkLP:
		grammar.cursor.consume()
		group, err := grammar.expression(0)
		if err != nil {
			return nil, err
		}
		if grammar.cursor.peek().kind != tkRP {
			return nil, fmt.Errorf("DSL syntax error: missing closing parenthesis ')'")
		}
		grammar.cursor.consume()
		return group, nil
	case tkEOF:
		return nil, fmt.Errorf("DSL syntax error: incomplete expression")
	default:
		return nil, fmt.Errorf("DSL syntax error: unexpected token '%s'", current.kind)
	}
}
func parseAssetExpression(text string) (*astNode, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	cursor := dslParser{tokens: scanAssetTokens(text)}
	expression, err := (assetGrammar{cursor: &cursor}).expression(0)
	if err != nil {
		return nil, err
	}
	if cursor.peek().kind != tkEOF {
		return nil, fmt.Errorf("DSL syntax error: unexpected content '%s'", cursor.peek().kind)
	}
	return expression, nil
}
