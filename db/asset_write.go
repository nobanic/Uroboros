package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Global enrichment/deletion keeps using the DB even on a transaction-scoped
// store. Only withCompanyScopeMutation borrows the caller's existing transaction.
type assetWrite struct{ store *AssetStore }

func (assets assetWrite) change(statement string, values ...any) recordMutation {
	return recordMutation{database: assets.store.db, statement: statement, values: values}
}
func (assets assetWrite) scoped(write func(*AssetStore) (int64, error)) (int64, error) {
	store := assets.store
	if store.tx != nil {
		return write(store)
	}
	return (mutationTransaction{database: store.db}).run(func(tx *sql.Tx) (int64, error) {
		if err := lockCompanyScopeMutation(tx); err != nil {
			return 0, err
		}
		return write(&AssetStore{db: store.db, company: store.company, tx: tx})
	})
}
func (assets assetWrite) ipPort(ip string, port int, service string) error {
	if ip == "" || port == 0 {
		return nil
	}
	entry, _ := json.Marshal(map[string]any{"port": port, "service": service})
	return assets.change(assetIPPortSQL, string(entry), ip).execute()
}
func (assets assetWrite) ipDomain(ip, domain string) error {
	if ip == "" || domain == "" {
		return nil
	}
	return assets.change(assetIPDomainSQL, domain, ip).execute()
}
func (assets assetWrite) removeTask(task int64) (int64, error) {
	deleted, err := assets.change(assetDeleteTaskSQL, task).ignoredCount()
	if err != nil {
		return 0, err
	}
	return deleted, assets.change(assetDetachTaskSQL, task).execute()
}
func (assets assetWrite) removeCompany(company int64) (int64, error) {
	return assets.change(assetDeleteCompanySQL, company).affected()
}
func (assets assetWrite) removeIDs(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	bindings, values := make([]string, len(ids)), make([]any, len(ids))
	for n, id := range ids {
		bindings[n] = fmt.Sprintf("$%d", n+1)
		values[n] = id
	}
	return assets.change("DELETE FROM assets WHERE id IN ("+strings.Join(bindings, ",")+")", values...).affected()
}
func (assets assetWrite) removeHost(host string) (map[string]int64, error) {
	host = DomainKey(host)
	if host == "" {
		return nil, fmt.Errorf("host is required")
	}
	rows, err := assets.store.db.Query(assetDeleteHostSQL, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		counts[kind]++
	}
	return counts, rows.Err()
}

const assetIPPortSQL = `
UPDATE assets SET
    open_ports = (
        SELECT ARRAY(
            SELECT DISTINCT ON ((elem->>'port')::int) elem
            FROM unnest(open_ports || ARRAY[$1::jsonb]) AS elem
            ORDER BY (elem->>'port')::int,
                     CASE WHEN elem->>'service' IS NOT NULL AND elem->>'service' <> '' THEN 0 ELSE 1 END
        )
    ),
    last_seen = now()
WHERE type = 'ip' AND ip = $2`
const assetIPDomainSQL = `
UPDATE assets SET
    bound_domains = (SELECT ARRAY(SELECT DISTINCT unnest(bound_domains || ARRAY[$1::text]))),
    last_seen = now()
WHERE type = 'ip' AND ip = $2`
const assetDeleteTaskSQL = `DELETE FROM assets WHERE task_ids = ARRAY[$1]::bigint[]`
const assetDetachTaskSQL = `UPDATE assets SET task_ids = array_remove(task_ids, $1) WHERE $1 = ANY(task_ids)`
const assetDeleteCompanySQL = `DELETE FROM assets WHERE company_id = $1`
const assetDeleteHostSQL = `
DELETE FROM assets
WHERE domain = $1 OR root_domain = $1 OR ip = $1
RETURNING type`
