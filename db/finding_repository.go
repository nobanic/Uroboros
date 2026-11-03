package db

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Finding columns, scan targets and conversions share one definition. Detail
// and export projections add the report; list queries deliberately omit it.
func findingFields(finding *DBFinding, assets *string, detail bool) []recordField {
	fields := []recordField{
		field("f.id", &finding.ID), field("f.task_id", &finding.TaskID), field("f.node_id", &finding.NodeID),
		field("f.vulnclass", &finding.VulnClass), field("f.name", &finding.Name).defaultRead("''"),
		field("f.severity", &finding.Severity), field("f.summary", &finding.Summary),
		field("f.evidence", &finding.Evidence), field("f.worker", &finding.Worker), field("f.asset_ids", assets),
		field("f.status", &finding.Status).defaultRead("'pending'"), field("f.created_at", &finding.CreatedAt),
		field("t.description", &finding.TaskDescription).defaultRead("''"),
		field("f.evidence_version", &finding.EvidenceVersion), field("f.report_evidence_version", &finding.ReportEvidenceVersion),
		field("(SELECT COUNT(*) FROM finding_traffic_bindings b WHERE b.finding_id=f.id)", &finding.TrafficCount),
	}
	if detail {
		fields = append(fields, field("f.report", &finding.Report).defaultRead("''"))
	}
	return fields
}

func findingProjection(detail bool) string {
	var assets string
	return recordProjection(findingFields(&DBFinding{}, &assets, detail))
}

func findingDecoder(detail bool) func(rowDecoder) (*DBFinding, error) {
	return func(row rowDecoder) (*DBFinding, error) {
		finding, assets := &DBFinding{}, ""
		if err := decodeRecord(row, findingFields(finding, &assets, detail)); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(assets), &finding.AssetIDs)
		return finding, nil
	}
}

// A selection owns its bindings from filtering through counting, paging and
// export. SQL identifiers are fixed by this adapter; operator values are bound.
type findingSelection struct {
	terms  []string
	values []any
}

func (selection *findingSelection) bind(value any) string {
	selection.values = append(selection.values, value)
	return "$" + strconv.Itoa(len(selection.values))
}

func selectFindings(filter FindingFilter) *findingSelection {
	selection := &findingSelection{}
	for _, item := range []struct{ column, value string }{{"severity", filter.Severity}, {"status", filter.Status}, {"vulnclass", filter.VulnClass}} {
		if item.value != "" {
			selection.terms = append(selection.terms, "f."+item.column+" = "+selection.bind(item.value))
		}
	}
	if filter.TaskID == FindingUnassignedTask {
		selection.terms = append(selection.terms, "(f.task_id IS NULL OR t.id IS NULL)")
	} else if task, err := strconv.ParseInt(filter.TaskID, 10, 64); err == nil && task > 0 {
		selection.terms = append(selection.terms, "f.task_id = "+selection.bind(task))
	}
	switch {
	case filter.assetMiss:
		selection.terms = append(selection.terms, "FALSE")
	case filter.assetNone:
		selection.terms = append(selection.terms, `(jsonb_array_length(COALESCE(f.asset_ids, '[]'::jsonb)) = 0 OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements_text(f.asset_ids) e(v) JOIN assets a ON a.id = e.v::bigint))`)
	case len(filter.assetIDs) != 0:
		selection.terms = append(selection.terms, "f.asset_ids @> ANY("+selection.bind(assetIDContainments(filter.assetIDs))+"::jsonb[])")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		binding := selection.bind("%" + pattern + "%")
		var matches []string
		for _, expression := range []string{"COALESCE(f.name, '')", "f.vulnclass", "f.summary", "f.evidence", "COALESCE(f.report, '')"} {
			matches = append(matches, expression+" ILIKE "+binding+` ESCAPE '\'`)
		}
		selection.terms = append(selection.terms, "("+strings.Join(matches, " OR ")+")")
	}
	return selection
}

func (selection *findingSelection) where() string {
	if len(selection.terms) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(selection.terms, " AND ")
}

func (filter FindingFilter) where() (string, []any) {
	selection := selectFindings(filter)
	return selection.where(), selection.values
}

func (selection *findingSelection) source() string {
	return " FROM findings f LEFT JOIN tasks t ON f.task_id=t.id" + selection.where()
}

func (selection *findingSelection) page(page, size int) string {
	return " LIMIT " + selection.bind(size) + " OFFSET " + selection.bind((page-1)*size)
}

func findingPageBounds(page, size, fallback int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = fallback
	}
	return page, size
}

const findingSeverityRank = "CASE f.severity WHEN 'critical' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END"

func findingOrder(sort string, stable bool) string {
	order := "f.created_at DESC"
	if sort == "severity" {
		order = findingSeverityRank + " DESC, " + order
	}
	if stable {
		order += ", f.id DESC"
	}
	return " ORDER BY " + order
}

func (selection *findingSelection) open(database *DB, detail bool, suffix string) (*sql.Rows, error) {
	return database.Query("SELECT "+findingProjection(detail)+selection.source()+suffix, selection.values...)
}

// AddFinding retains database defaults and stores absent task/node references as NULL.
func (d *DB) AddFinding(taskID, nodeID int64, vulnclass, name, severity, summary, evidence, worker string, assetIDs []int64) (int64, error) {
	var task, node *int64
	if taskID > 0 {
		task = &taskID
	}
	if nodeID > 0 {
		node = &nodeID
	}
	if assetIDs == nil {
		assetIDs = []int64{}
	}
	encoded, _ := json.Marshal(assetIDs)
	assets := string(encoded)
	return persistRecord(d, "findings", 0, []recordField{
		field("task_id", &task), field("node_id", &node), field("vulnclass", &vulnclass), field("name", &name),
		field("severity", &severity), field("summary", &summary), field("evidence", &evidence), field("worker", &worker), field("asset_ids", &assets),
	})
}

// ListFindings supplies the dashboard's newest-first, report-free snapshot.
func (d *DB) ListFindings(limit int) ([]*DBFinding, error) {
	if limit <= 0 {
		limit = 500
	}
	selection := selectFindings(FindingFilter{})
	suffix := findingOrder("time", false) + " LIMIT " + selection.bind(limit)
	rows, err := selection.open(d, false, suffix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecordRows(rows, findingDecoder(false))
}

// GetFinding includes the full report and returns nil for an absent ID.
func (d *DB) GetFinding(id int64) (*DBFinding, error) {
	selection := selectFindings(FindingFilter{})
	selection.terms = append(selection.terms, "f.id = "+selection.bind(id))
	result, err := findingDecoder(true)(d.QueryRow("SELECT "+findingProjection(true)+selection.source(), selection.values...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return result, err
}

// ListFindingsPage counts and selects the same asset-scoped filter, with a stable ID tie-breaker.
func (d *DB) ListFindingsPage(filter FindingFilter, page, size int) ([]*DBFinding, int, error) {
	page, size = findingPageBounds(page, size, 20)
	filter, err := d.applyAssetScope(filter)
	if err != nil {
		return nil, 0, err
	}
	selection := selectFindings(filter)
	var total int
	if err := d.QueryRow("SELECT COUNT(*)"+selection.source(), selection.values...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 || page > (total-1)/size+1 {
		return []*DBFinding{}, total, nil
	}
	suffix := findingOrder(filter.Sort, true) + selection.page(page, size)
	rows, err := selection.open(d, false, suffix)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	findings, err := scanRecordRows(rows, findingDecoder(false))
	return findings, total, err
}

// ListFindingGroups preserves independent task-bucket and finding totals, including the NULL task bucket.
func (d *DB) ListFindingGroups(filter FindingFilter, page, size int) ([]FindingGroup, int, int, error) {
	page, size = findingPageBounds(page, size, 10)
	filter, err := d.applyAssetScope(filter)
	if err != nil {
		return nil, 0, 0, err
	}
	selection := selectFindings(filter)
	source := selection.source() + " GROUP BY t.id, t.name, t.description, t.status, t.paused, t.queued"
	var total, findings int
	count := "SELECT COUNT(*), COALESCE(SUM(finding_count),0) FROM (SELECT COUNT(*) AS finding_count" + source + ") grouped_findings"
	if err := d.QueryRow(count, selection.values...).Scan(&total, &findings); err != nil {
		return nil, 0, 0, err
	}
	if total == 0 || page > (total-1)/size+1 {
		return []FindingGroup{}, total, findings, nil
	}
	order := "MAX(f.created_at) DESC, t.id DESC NULLS LAST"
	if filter.Sort == "severity" {
		order = "MAX(" + findingSeverityRank + ") DESC, " + order
	}
	var group FindingGroup
	fields := []recordField{
		field("t.id", &group.TaskID), field("t.name", &group.TaskName).defaultRead("''"), field("t.description", &group.TaskDescription).defaultRead("''"),
		field("CASE WHEN t.status IN ('done','failed','timeout') THEN t.status WHEN t.queued THEN 'queued' WHEN t.paused THEN 'paused' ELSE t.status END", &group.TaskStatus).defaultRead("''"),
		field("COUNT(*)", &group.Count), field("COUNT(*) FILTER (WHERE f.severity='critical')", &group.Critical),
		field("COUNT(*) FILTER (WHERE f.severity='high')", &group.High), field("COUNT(*) FILTER (WHERE f.severity='medium')", &group.Medium),
		field("COUNT(*) FILTER (WHERE f.severity='low')", &group.Low), field("MAX(f.created_at)", &group.LastFoundAt),
	}
	query := "SELECT " + recordProjection(fields) + source + " ORDER BY " + order + selection.page(page, size)
	rows, err := d.Query(query, selection.values...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	groups := []FindingGroup{}
	for rows.Next() {
		group = FindingGroup{}
		if err := decodeRecord(rows, fields); err != nil {
			return nil, 0, 0, err
		}
		groups = append(groups, group)
	}
	return groups, total, findings, rows.Err()
}

// ListFindingsForExport includes reports. Explicit IDs override the supplied filter.
func (d *DB) ListFindingsForExport(filter FindingFilter, ids []int64) ([]*DBFinding, error) {
	var selection *findingSelection
	if len(ids) != 0 {
		selection = selectFindings(FindingFilter{})
		bindings := make([]string, len(ids))
		for index, id := range ids {
			bindings[index] = selection.bind(id)
		}
		selection.terms = append(selection.terms, "f.id IN ("+strings.Join(bindings, ",")+")")
	} else {
		scoped, err := d.applyAssetScope(filter)
		if err != nil {
			return nil, err
		}
		selection = selectFindings(scoped)
	}
	rows, err := selection.open(d, true, findingOrder("severity", false))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecordRows(rows, findingDecoder(true))
}

// FindingStats merges exact live counts with the existing archived-task aggregates.
func (d *DB) FindingStats() (*FindingStats, error) {
	stats := &FindingStats{VulnClasses: []string{}, Tasks: []FindingTaskOption{}}
	fields := []recordField{field("COUNT(*)", &stats.Total)}
	for _, entry := range []struct {
		predicate string
		value     *int
	}{{"status = 'pending'", &stats.Pending}, {"severity = 'critical'", &stats.Critical}, {"severity = 'high'", &stats.High}, {"severity = 'medium'", &stats.Medium}, {"severity = 'low'", &stats.Low}} {
		fields = append(fields, field("COUNT(*) FILTER (WHERE "+entry.predicate+")", entry.value))
	}
	if err := decodeRecord(d.QueryRow("SELECT "+recordProjection(fields)+" FROM findings"), fields); err != nil {
		return nil, err
	}
	classes, err := graphRows(d, "SELECT DISTINCT vulnclass FROM findings WHERE vulnclass <> '' ORDER BY vulnclass", nil, func(row rowDecoder) (string, error) { var name string; err := row.Scan(&name); return name, err })
	if err != nil {
		return nil, err
	}
	options, err := graphRows(d, "SELECT f.task_id, COALESCE(t.name, ''), COALESCE(t.description, ''), COUNT(*) FROM findings f LEFT JOIN tasks t ON f.task_id=t.id WHERE f.task_id IS NOT NULL GROUP BY f.task_id, t.name, t.description ORDER BY MAX(f.created_at) DESC", nil, func(row rowDecoder) (FindingTaskOption, error) {
		var option FindingTaskOption
		err := row.Scan(&option.ID, &option.Name, &option.Description, &option.Count)
		return option, err
	})
	if err != nil {
		return nil, err
	}
	if options != nil {
		stats.Tasks = options
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	for _, name := range classes {
		unique[name] = true
	}
	for _, archive := range archived {
		cold := archive.FindingStats
		stats.Total += cold.Total
		stats.Pending += cold.Pending
		stats.Critical += cold.Critical
		stats.High += cold.High
		stats.Medium += cold.Medium
		stats.Low += cold.Low
		for _, name := range cold.VulnClasses {
			if name != "" {
				unique[name] = true
			}
		}
	}
	for name := range unique {
		stats.VulnClasses = append(stats.VulnClasses, name)
	}
	sort.Strings(stats.VulnClasses)
	return stats, nil
}
