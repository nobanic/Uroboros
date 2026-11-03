package db

import (
	"fmt"
	"strings"
)

// Expr is one leaf DSL clause.
type Expr struct {
	Field string // empty = bare-text full-text search
	Op    string // "=", "==", "!=", ">", ">=", "<", "<="
	Value string
}

// astNode is a node in the parsed DSL expression tree.
type astNode struct {
	kind     string // "and", "or", "leaf"
	children []*astNode
	expr     *Expr // only for "leaf"
}

func andNode(cs []*astNode) *astNode { return &astNode{kind: "and", children: cs} }
func orNode(cs []*astNode) *astNode  { return &astNode{kind: "or", children: cs} }
func leafNode(e Expr) *astNode       { return &astNode{kind: "leaf", expr: &e} }

// knownStringFields maps DSL field name → SQL column name.
// NOTE: "type" is intentionally excluded — it is a separate parameter, not a DSL field.
var knownStringFields = map[string]string{
	"domain":       "domain",
	"root_domain":  "root_domain",
	"ip":           "ip",
	"url":          "url",
	"page_title":   "page_title",
	"title":        "page_title",
	"icp":          "icp",
	"service_name": "service_name",
	"app_name":     "app_name",
	"bundle_id":    "bundle_id",
	"category":     "category",
	"app_icp":      "app_icp",
	"method":       "method",
	"service_type": "service_type",
	"record_type":  "record_type",
}

// knownArrayFields maps DSL field name → SQL column name (array).
var knownArrayFields = map[string]string{
	"technology":   "technologies",
	"technologies": "technologies",
	"tech":         "technologies",
}

// knownNumericFields maps DSL field name → SQL column name (integer).
var knownNumericFields = map[string]string{
	"port":        "port",
	"status_code": "status_code",
	"status":      "status_code",
}

func isKnownField(f string) bool {
	f = strings.ToLower(f)
	_, s := knownStringFields[f]
	_, a := knownArrayFields[f]
	_, n := knownNumericFields[f]
	return s || a || n || f == "company_id" || f == "task_id"
}

// ── tokeniser ────────────────────────────────────────────────────────────────

const (
	tkField = "FIELD"
	tkBare  = "BARE"
	tkAnd   = "AND"
	tkOr    = "OR"
	tkLP    = "LPAREN"
	tkRP    = "RPAREN"
	tkEOF   = "EOF"
)

type tok struct {
	kind string
	expr *Expr // set for tkField and tkBare
}

func tokenize(s string) ([]tok, error) {
	return scanAssetTokens(s), nil
}

// ── parser ───────────────────────────────────────────────────────────────────
//
// Grammar (AND binds tighter than OR):
//   expr     = or_expr
//   or_expr  = and_expr (OR and_expr)*
//   and_expr = atom    (AND atom)*
//   atom     = FIELD | BARE | '(' expr ')'

type dslParser struct {
	tokens []tok
	pos    int
}

func (p *dslParser) peek() tok {
	if p.pos >= len(p.tokens) {
		return tok{kind: tkEOF}
	}
	return p.tokens[p.pos]
}

func (p *dslParser) consume() tok {
	t := p.peek()
	p.pos++
	return t
}

func (p *dslParser) parseOr() (*astNode, error) {
	return (assetGrammar{cursor: p}).expression(0)
}

func (p *dslParser) parseAnd() (*astNode, error) {
	return (assetGrammar{cursor: p}).expression(1)
}

func (p *dslParser) parseAtom() (*astNode, error) {
	return (assetGrammar{cursor: p}).atom()
}

// ParseDSL parses a DSL query string into an expression tree.
//
// Syntax:
//
//	field=value      fuzzy match (ILIKE '%value%')
//	field==value     exact match
//	field!=value     exclude fuzzy
//	port>8080        numeric comparison
//	bare word        full-text fuzzy across all main text fields
//
// Operators: AND OR (case-insensitive), parentheses for grouping.
// AND binds tighter than OR.
func ParseDSL(s string) (*astNode, error) {
	return parseAssetExpression(s)
}

// ── SQL builder ──────────────────────────────────────────────────────────────

// fullTextCols are searched for bare-text tokens.
var fullTextCols = []string{
	"domain", "root_domain", "ip", "url", "page_title",
	"icp", "service_name", "app_name", "app_description",
}

var strictFullTextCols = []string{
	"domain", "root_domain", "ip", "url", "service_name", "app_name", "bundle_id", "method", "record_type",
}

var strictIdentityStringFields = map[string]struct{}{
	"domain": {}, "root_domain": {}, "ip": {}, "url": {},
	"service_name": {}, "app_name": {}, "bundle_id": {}, "method": {},
	"service_type": {}, "record_type": {},
}

type whereBuilder struct {
	args           []any
	strictIdentity bool
	base           int // placeholders are numbered base+1, base+2, …; 0 = the usual $1, $2, …
}

func (b *whereBuilder) next(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", b.base+len(b.args))
}

func (b *whereBuilder) build(node *astNode) (string, error) {
	return (assetExpressionSQL{bindings: b}).tree(node)
}

func (b *whereBuilder) buildLeaf(e Expr) (string, error) {
	return (assetExpressionSQL{bindings: b}).leaf(e)
}

func buildDSLWhere(node *astNode) (string, []any, error) {
	return buildDSLWhereBase(node, 0)
}

// buildDSLWhereBase is buildDSLWhere with a placeholder offset: emitted args are
// numbered base+1 onward, leaving $1..$base free for the caller (e.g. a scope CTE
// that reserves $1 for the task id).
func buildDSLWhereBase(node *astNode, base int) (string, []any, error) {
	return buildDSLWhereBaseMode(node, base, false)
}

func buildDSLWhereBaseMode(node *astNode, base int, strictIdentity bool) (string, []any, error) {
	if node == nil {
		return "1=1", nil, nil
	}
	b := &whereBuilder{base: base, strictIdentity: strictIdentity}
	clause, err := b.build(node)
	if err != nil {
		return "", nil, err
	}
	return clause, b.args, nil
}

// ── helpers (shared with parser) ─────────────────────────────────────────────

// tryParseFieldExpr tries to parse "field op value" at pos.
func tryParseFieldExpr(s string, pos int) (Expr, int, bool) {
	return readAssetClause(s, pos)
}

// readToken reads a quoted or unquoted token starting at pos.
func readToken(s string, pos int) (string, int) {
	return readAssetWord(s, pos)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// ── QueryDSL ─────────────────────────────────────────────────────────────────

// ValidateDSL parses and compiles a DSL expression without touching the
// database. HTTP callers use it to distinguish client syntax errors from query
// failures, which must remain server errors.
func ValidateDSL(dsl string) error {
	node, err := ParseDSL(dsl)
	if err != nil {
		return err
	}
	_, _, err = buildDSLWhere(node)
	return err
}

// CountDSL returns the total number of assets matching a DSL expression (and optional
// type), for server-side pagination — same WHERE as QueryDSL, without LIMIT/OFFSET.
// taskID > 0 scopes the count to assets attached to that task.
func (s *AssetStore) CountDSL(dsl, typ string, taskID int64) (int, error) {
	node, err := ParseDSL(dsl)
	if err != nil {
		return 0, err
	}
	where, args, err := buildDSLWhere(node)
	if err != nil {
		return 0, err
	}
	if typ != "" {
		args = append(args, typ)
		where += fmt.Sprintf(" AND type = $%d", len(args))
	}
	if taskID > 0 {
		args = append(args, taskID)
		where += fmt.Sprintf(" AND $%d = ANY(task_ids)", len(args))
	}
	var n int
	err = s.db.QueryRow("SELECT count(*) FROM assets WHERE "+where, args...).Scan(&n)
	return n, err
}

// QueryDSL executes a DSL query string against the asset store.
// typ is an optional asset type filter applied independently of the DSL expression.
// taskID > 0 scopes results to assets attached to that task and hydrates each
// row's per-task source metadata (as QueryByTask does).
func (s *AssetStore) QueryDSL(dsl, typ string, taskID int64, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	node, err := ParseDSL(dsl)
	if err != nil {
		return nil, err
	}
	where, args, err := buildDSLWhere(node)
	if err != nil {
		return nil, err
	}
	if typ != "" {
		args = append(args, typ)
		where += fmt.Sprintf(" AND type = $%d", len(args))
	}
	if taskID > 0 {
		args = append(args, taskID)
		where += fmt.Sprintf(" AND $%d = ANY(task_ids)", len(args))
	}
	args = append(args, limit, offset)
	q := assetSelectCols + " WHERE " + where +
		fmt.Sprintf(" ORDER BY last_seen DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, err
	}
	if taskID > 0 {
		if err := s.hydrateTaskAssetSources(taskID, assets); err != nil {
			return nil, err
		}
	}
	return assets, nil
}

// QueryDSLInScope is QueryDSL restricted to assets that BELONG to taskID's (and its
// direct source tasks') declared scope — membership, not literal value: a
// root_domain scope returns every subdomain / service / endpoint under it. This is
// the agent-facing list_assets path, so an agent queries the task's relevant assets
// instead of the whole shared library. taskID<=0 (non-task contexts: Auto / pentest / chat)
// has no scope to honor and falls back to the plain global QueryDSL. Rows carry the
// same per-task source metadata as QueryByTask. Strict tasks can search and
// receive identity columns only; mutable global enrichment has no per-task
// provenance and is therefore unavailable in this path.
func (s *AssetStore) QueryDSLInScope(dsl, typ string, taskID int64, limit, offset int) ([]*Asset, error) {
	if taskID <= 0 {
		return s.QueryDSL(dsl, typ, 0, limit, offset)
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	node, err := ParseDSL(dsl)
	if err != nil {
		return nil, err
	}
	policy, err := s.loadTaskAssetViewPolicy(taskID)
	if err != nil {
		return nil, err
	}
	// $1 is reserved for taskID (scopeTargetCTE); DSL placeholders start at $2.
	where, dslArgs, err := buildDSLWhereBaseMode(node, 1, policy.strict)
	if err != nil {
		return nil, err
	}
	args := []any{taskID}
	args = append(args, dslArgs...)
	if typ != "" {
		args = append(args, typ)
		where += fmt.Sprintf(" AND type = $%d", len(args))
	}
	where += " AND id IN (SELECT id FROM target)"
	args = append(args, limit, offset)
	q := `WITH ` + scopeTargetCTE + ` ` + assetSelectCols + " WHERE " + where +
		fmt.Sprintf(" ORDER BY last_seen DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateTaskAssetSources(taskID, assets); err != nil {
		return nil, err
	}
	applyTaskAssetViewPolicy(assets, policy)
	return assets, nil
}

// GetByIDsInScope is GetByIDs restricted to ids that BELONG to taskID's (and its
// direct source tasks') declared scope, so an agent cannot reach out-of-scope
// assets by id. taskID<=0 (non-task contexts) falls back to the global GetByIDs.
// Out-of-scope ids are silently dropped from the result (not an error).
func (s *AssetStore) GetByIDsInScope(taskID int64, ids []int64) ([]*Asset, error) {
	if taskID <= 0 {
		return s.GetByIDs(ids)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, taskID) // $1 reserved for scopeTargetCTE
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, id)
	}
	q := `WITH ` + scopeTargetCTE + ` ` + assetSelectCols +
		" WHERE id IN (" + strings.Join(placeholders, ",") + ")" +
		" AND id IN (SELECT id FROM target) ORDER BY last_seen DESC, id DESC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assets, err := scanAssets(rows)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateTaskAssetSources(taskID, assets); err != nil {
		return nil, err
	}
	if err := s.restrictAssetTaskIDs(taskID, assets); err != nil {
		return nil, err
	}
	return assets, nil
}
