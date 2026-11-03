package db

import (
	"net/url"
	"sort"
	"strings"
)

// Two projections share normalization, row lifetime and sorting. The deletion
// projection additionally subtracts every host protected by another live task.
type assetHostProjection struct {
	database    rowsQuerier
	statement   string
	partitioned bool
}
type assetHostSet map[string]struct{}

func (hosts assetHostSet) add(raw string) {
	host := strings.TrimSpace(strings.ToLower(raw))
	if host != "" {
		hosts[host] = struct{}{}
	}
}
func (hosts assetHostSet) record(domain, ip, rawURL string) {
	hosts.add(domain)
	hosts.add(ip)
	if address, err := url.Parse(rawURL); err == nil {
		hosts.add(address.Hostname())
	}
}
func (projection assetHostProjection) read(values ...any) ([]string, error) {
	rows, err := projection.database.Query(projection.statement, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, protected := make(assetHostSet), make(assetHostSet)
	for rows.Next() {
		var domain, ip, rawURL string
		candidate := true
		fields := []any{&domain, &ip, &rawURL}
		if projection.partitioned {
			fields = append(fields, &candidate)
		}
		if err := rows.Scan(fields...); err != nil {
			return nil, err
		}
		destination := candidates
		if !candidate {
			destination = protected
		}
		destination.record(domain, ip, rawURL)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(candidates))
	for host := range candidates {
		if _, shared := protected[host]; !shared {
			result = append(result, host)
		}
	}
	sort.Strings(result)
	return result, nil
}

const assetTaskHostsSQL = `
SELECT COALESCE(domain,''), COALESCE(ip,''), COALESCE(url,'')
FROM assets WHERE $1 = ANY(task_ids)`
const assetTaskDeleteHostsSQL = `
WITH current_assets AS (
  SELECT id FROM assets WHERE $1 = ANY(task_ids)
  UNION
  SELECT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes n ON n.id=ea.node_id
  WHERE n.exploration_id=$2
),
other_assets AS (
  SELECT DISTINCT a.id
  FROM assets a
  WHERE EXISTS (
    SELECT 1 FROM tasks t
    WHERE t.id<>$1 AND t.deleted_at IS NULL AND t.id=ANY(a.task_ids)
  ) OR EXISTS (
    SELECT 1
    FROM exploration_anchors ea
    JOIN exploration_nodes n ON n.id=ea.node_id
    JOIN tasks t ON t.exploration_id=n.exploration_id
    WHERE ea.asset_id=a.id AND t.id<>$1 AND t.deleted_at IS NULL
  )
)
SELECT COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.url,''), true
FROM assets a JOIN current_assets c ON c.id=a.id
UNION ALL
SELECT COALESCE(a.domain,''), COALESCE(a.ip,''), COALESCE(a.url,''), false
FROM assets a JOIN other_assets o ON o.id=a.id`
