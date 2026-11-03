package db

import "strconv"

// legacyCoverageRead deliberately keeps the original local task scope and
// explicit exploration identity. It does not reuse graph context authority.
type legacyCoverageRead struct {
	store                 *AssetStore
	taskID, explorationID int64
}

func (read legacyCoverageRead) count() (*Coverage, error) {
	result := &Coverage{ByType: []CoverageByType{}}
	_ = read.store.db.QueryRow(`SELECT count(*) FROM task_scope WHERE task_id=$1`, read.taskID).Scan(&result.ScopeRows)
	rows, err := read.store.db.Query(`WITH `+covTargetCTE+`
SELECT t.type, count(*) AS total,
       count(*) FILTER (WHERE t.id IN (SELECT asset_id FROM tested)) AS tested
FROM target t GROUP BY t.type ORDER BY t.type`, read.taskID, read.explorationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry CoverageByType
		if err := rows.Scan(&entry.Type, &entry.Total, &entry.Tested); err != nil {
			return nil, err
		}
		result.ByType = append(result.ByType, entry)
		result.Denominator += entry.Total
		result.Tested += entry.Tested
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result.Denominator > 0 {
		fraction := float64(result.Tested) / float64(result.Denominator)
		result.Pct = &fraction
	}
	return result, nil
}

// coveragePage owns only pagination and bindings for one backlog read. Type
// values are bound verbatim; default/clamp and count-query best effort remain.
type coveragePage struct {
	kind          string
	limit, offset int
}

func newCoveragePage(kind string, limit, offset int) coveragePage {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	return coveragePage{kind: kind, limit: limit, offset: offset}
}

func (page coveragePage) bindings(taskID, explorationID int64) (string, []any) {
	args := []any{taskID, explorationID}
	if page.kind == "" {
		return "", args
	}
	return " AND t.type = $3", append(args, page.kind)
}

func (read legacyCoverageRead) untested(kind string, limit, offset int) ([]CoverageAsset, int, error) {
	page := newCoveragePage(kind, limit, offset)
	typeFilter, args := page.bindings(read.taskID, read.explorationID)
	var total int
	_ = read.store.db.QueryRow(`WITH `+covTargetCTE+`
SELECT count(*) FROM target t WHERE t.id NOT IN (SELECT asset_id FROM tested)`+typeFilter, args...).Scan(&total)
	pageArgs := append(append([]any{}, args...), page.limit, page.offset)
	limPos := strconv.Itoa(len(args) + 1)
	offPos := strconv.Itoa(len(args) + 2)
	rows, err := read.store.db.Query(`WITH `+covTargetCTE+`
SELECT t.id, t.type, t.label FROM target t
WHERE t.id NOT IN (SELECT asset_id FROM tested)`+typeFilter+`
ORDER BY t.id LIMIT $`+limPos+` OFFSET $`+offPos, pageArgs...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	result := []CoverageAsset{}
	for rows.Next() {
		var entry CoverageAsset
		if err := rows.Scan(&entry.ID, &entry.Type, &entry.Label); err != nil {
			return nil, total, err
		}
		result = append(result, entry)
	}
	return result, total, rows.Err()
}
