package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func archiveRowString(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

type archiveAssetKey struct {
	kind, statement string
	fields          []string
}

var archiveAssetKeys = []archiveAssetKey{
	{"root_domain", `SELECT id FROM assets WHERE type='root_domain' AND domain=$1`, []string{"domain"}},
	{"ip", `SELECT id FROM assets WHERE type='ip' AND ip=$1`, []string{"ip"}},
	{"subdomain", `SELECT id FROM assets WHERE type='subdomain' AND domain=$1 AND COALESCE(record_type,'')=$2`, []string{"domain", "record_type"}},
	{"app", `SELECT id FROM assets WHERE type='app' AND bundle_id=$1`, []string{"bundle_id"}},
	{"app_name", `SELECT id FROM assets WHERE type='app' AND bundle_id IS NULL AND app_name=$1`, []string{"app_name"}},
	{"service", `SELECT id FROM assets WHERE type='service' AND service_type='http' AND url=$1`, []string{"url"}},
	{"service_other", `SELECT id FROM assets WHERE type='service' AND service_type='other' AND COALESCE(domain,'')=$1 AND COALESCE(ip,'')=$2 AND port=$3 AND service_name=$4`, []string{"domain", "ip", "port", "service_name"}},
	{"endpoint", `SELECT id FROM assets WHERE type='endpoint' AND url=$1 AND method=$2`, []string{"url", "method"}},
}

func lookupArchivedAsset(tx *sql.Tx, row map[string]any) (int64, bool, error) {
	original := archiveRowString(row, "type")
	key := original
	if original == "app" && archiveRowString(row, "bundle_id") == "" {
		key = "app_name"
	}
	if original == "service" && archiveRowString(row, "service_type") != "http" {
		key = "service_other"
	}
	// Only the original public asset types may select a private specialization.
	switch original {
	case "root_domain", "ip", "subdomain", "app", "service", "endpoint":
	default:
		return 0, false, fmt.Errorf("unsupported archived asset type %q", original)
	}
	for _, plan := range archiveAssetKeys {
		if plan.kind == key {
			args := make([]any, 0, len(plan.fields))
			for _, field := range plan.fields {
				if field == "port" {
					port, _ := jsonInt64(row[field])
					args = append(args, port)
				} else {
					args = append(args, archiveRowString(row, field))
				}
			}
			var id int64
			err := tx.QueryRow(plan.statement, args...).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, nil
			}
			return id, err == nil, err
		}
	}
	panic("unbound archive asset key")
}

type archiveAssetRestore struct {
	tx       *sql.Tx
	taskID   int64
	mapping  map[int64]int64
	warnings []string
}

func (restore *archiveAssetRestore) attach(id int64) error {
	_, err := restore.tx.Exec(`UPDATE assets SET task_ids=CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END WHERE id=$2`, restore.taskID, id)
	return err
}
func (restore *archiveAssetRestore) row(row map[string]any) error {
	oldID, ok := jsonInt64(row["id"])
	if !ok || oldID <= 0 {
		return errors.New("archived asset has invalid id")
	}
	if companyID, ok := jsonInt64(row["company_id"]); ok && companyID > 0 && !rowExists(restore.tx, "companies", companyID) {
		row["company_id"] = nil
		row["company_source"] = "explicit"
		restore.warnings = append(restore.warnings, fmt.Sprintf("company %d of asset %d was deleted; restored as unattributed", oldID, companyID))
	}
	existing, found, err := findArchiveAssetNaturalID(restore.tx, row)
	if err != nil {
		return err
	}
	if found {
		restore.mapping[oldID] = existing
		return restore.attach(existing)
	}
	candidate := oldID
	if rowExists(restore.tx, "assets", oldID) {
		if err := restore.tx.QueryRow(`SELECT nextval(pg_get_serial_sequence('assets','id'))`).Scan(&candidate); err != nil {
			return err
		}
		row["id"] = candidate
		restore.warnings = append(restore.warnings, fmt.Sprintf("asset ID %d was already taken; restored as %d", oldID, candidate))
	}
	row["task_ids"] = mergeJSONTaskID(row["task_ids"], restore.taskID)
	raw, _ := json.Marshal(row)
	result, err := restore.tx.Exec(`INSERT INTO assets SELECT * FROM json_populate_record(NULL::assets,$1::json) ON CONFLICT DO NOTHING`, string(raw))
	if err != nil {
		return err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		existing, found, findErr := findArchiveAssetNaturalID(restore.tx, row)
		if findErr != nil || !found {
			return errors.Join(findErr, fmt.Errorf("could not restore asset %d", oldID))
		}
		candidate = existing
		if err := restore.attach(existing); err != nil {
			return err
		}
	}
	restore.mapping[oldID] = candidate
	return nil
}
func (restore archiveAssetRestore) apply(raw json.RawMessage) (map[int64]int64, []string, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, nil, err
	}
	restore.mapping = make(map[int64]int64, len(rows))
	restore.warnings = []string{}
	for _, row := range rows {
		if err := restore.row(row); err != nil {
			return nil, nil, err
		}
	}
	return restore.mapping, restore.warnings, nil
}
