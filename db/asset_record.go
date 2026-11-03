package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// All global, DSL and task-scoped asset readers use the same projection and
// decoder. Provenance hydration and strict task projection remain separate.
type assetRecord struct {
	asset    Asset
	optional [4]sql.NullInt64
	json     [8][]byte
}

func (record *assetRecord) fields() []recordField {
	a := &record.asset
	read := func(name string, target any) recordField {
		return recordField{name: name, target: target, readOnly: true}
	}
	text := func(name string, target any) recordField { return read(name, target).defaultRead("''") }
	return []recordField{
		read("id", &a.ID), read("type", &a.Type), read("company_id", &record.optional[0]), read("array_to_json(task_ids)::text", &record.json[0]),
		text("domain", &a.Domain), text("root_domain", &a.RootDomain), text("ip", &a.IP), text("c_segment::text", &a.CSegment), read("port", &record.optional[1]),
		text("icp", &a.ICP), read("array_to_json(bound_domains)::text", &record.json[1]), read("array_to_json(open_ports)::text", &record.json[2]), text("record_type", &a.RecordType),
		read("array_to_json(record_value)::text", &record.json[3]), text("bundle_id", &a.BundleID), text("app_name", &a.AppName),
		text("category", &a.Category), text("app_description", &a.AppDescription), text("app_icp", &a.AppICP),
		text("url", &a.URL), text("service_type", &a.ServiceType), text("service_name", &a.ServiceName), text("favicon_mmh3", &a.FaviconMMH3),
		read("status_code", &record.optional[2]), read("content_length", &record.optional[3]), text("page_title", &a.PageTitle),
		read("array_to_json(technologies)::text", &record.json[4]), read("array_to_json(auth)::text", &record.json[5]), text("method", &a.Method),
		read("array_to_json(params)::text", &record.json[6]), read("extra", &record.json[7]), read("last_seen::text", &a.LastSeen),
	}
}

func readAsset(row rowDecoder) (*Asset, error) {
	record := new(assetRecord)
	if err := decodeRecord(row, record.fields()); err != nil {
		return nil, err
	}
	a := &record.asset
	if value := record.optional[0]; value.Valid {
		id := value.Int64
		a.CompanyID = &id
	}
	if value := record.optional[1]; value.Valid {
		number := int(value.Int64)
		a.Port = &number
	}
	if value := record.optional[2]; value.Valid {
		number := int(value.Int64)
		a.StatusCode = &number
	}
	if value := record.optional[3]; value.Valid {
		number := value.Int64
		a.ContentLength = &number
	}
	for index, target := range []any{&a.TaskIDs, &a.BoundDomains, &a.OpenPorts, &a.RecordValue, &a.Technologies, &a.Auth, &a.Params, &a.Extra} {
		// Preserve historical NULL/empty/malformed JSON behavior rather than
		// changing the graph/tool contract during a read-adapter replacement.
		if len(record.json[index]) > 0 {
			_ = json.Unmarshal(record.json[index], target)
		}
	}
	return a, nil
}

var assetSelectCols = "SELECT " + recordProjection(new(assetRecord).fields()) + " FROM assets"

func scanAssets(rows *sql.Rows) ([]*Asset, error) { return scanRecordRows(rows, readAsset) }

type assetSelection struct {
	clauses []string
	values  []any
}

func (selection *assetSelection) where(expression string, value any) {
	selection.values = append(selection.values, value)
	selection.clauses = append(selection.clauses, fmt.Sprintf(expression, len(selection.values)))
}
func (selection assetSelection) predicate() string {
	if len(selection.clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(selection.clauses, " AND ")
}
func (s *AssetStore) countSelection(selection assetSelection) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT count(*) FROM assets"+selection.predicate(), selection.values...).Scan(&count)
	return count, err
}
func (s *AssetStore) readSelection(selection assetSelection, limit, offset int) ([]*Asset, error) {
	query := assetSelectCols + selection.predicate() + " ORDER BY last_seen DESC, id DESC"
	bind := func(keyword string, value int) {
		selection.values = append(selection.values, value)
		query += fmt.Sprintf(" %s $%d", keyword, len(selection.values))
	}
	if limit > 0 {
		bind("LIMIT", limit)
	}
	if offset > 0 {
		bind("OFFSET", offset)
	}
	rows, err := s.db.Query(query, selection.values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}
func assetsOfType(typ string) assetSelection {
	var selection assetSelection
	selection.where("type = $%d", typ)
	return selection
}
func assetsOfCompany(companyID int64, typ string) assetSelection {
	var selection assetSelection
	selection.where("company_id = $%d", companyID)
	if typ != "" {
		selection.where("type = $%d", typ)
	}
	return selection
}
func assetsOfTask(taskID int64, typ string) assetSelection {
	var selection assetSelection
	selection.where("$%d = ANY(task_ids)", taskID)
	if typ != "" {
		selection.where("type = $%d", typ)
	}
	return selection
}

// Type reads retain their historical default page size; company/task readers
// deliberately accept nonpositive limits as unlimited.
func (s *AssetStore) QueryByType(typ string, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.readSelection(assetsOfType(typ), limit, offset)
}
func (s *AssetStore) CountByType(typ string) (int, error) { return s.countSelection(assetsOfType(typ)) }
func (s *AssetStore) QueryByCompany(companyID int64, typ string, limit, offset int) ([]*Asset, error) {
	return s.readSelection(assetsOfCompany(companyID, typ), limit, offset)
}
func (s *AssetStore) CountByCompany(companyID int64, typ string) (int, error) {
	return s.countSelection(assetsOfCompany(companyID, typ))
}
func (s *AssetStore) QueryByTask(taskID int64, typ string, limit, offset int) ([]*Asset, error) {
	assets, err := s.readSelection(assetsOfTask(taskID, typ), limit, offset)
	if err == nil {
		err = s.hydrateTaskAssetSources(taskID, assets)
	}
	if err != nil {
		return nil, err
	}
	return assets, nil
}
func (s *AssetStore) CountByTask(taskID int64, typ string) (int, error) {
	return s.countSelection(assetsOfTask(taskID, typ))
}

func (s *AssetStore) assetTypeCounts(selection assetSelection) (map[string]int, error) {
	return collectQueryState(queriedRows(s.db.Query("SELECT type, count(*) FROM assets"+selection.predicate()+" GROUP BY type", selection.values...)), func() map[string]int {
		return make(map[string]int)
	}, func(queryRow rowDecoder, queryState *map[string]int) error {
		var kind string
		var count int
		if err := queryRow.Scan(&kind, &count); err != nil {
			return err
		}
		(*queryState)[kind] = count
		return nil
	})
}
func (s *AssetStore) CountsByType() (map[string]int, error) {
	return s.assetTypeCounts(assetSelection{})
}
func (s *AssetStore) CountsByTypeForTask(taskID int64) (map[string]int, error) {
	return s.assetTypeCounts(assetsOfTask(taskID, ""))
}

// Historical behavior sorts by last_seen; it never preserved the caller's ID
// array order despite the prior comment claiming that. Keep the observed order.
func (s *AssetStore) GetByIDs(ids []int64) ([]*Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	selection := assetSelection{values: make([]any, len(ids))}
	bindings := make([]string, len(ids))
	for index, id := range ids {
		selection.values[index] = id
		bindings[index] = fmt.Sprintf("$%d", index+1)
	}
	selection.clauses = []string{"id IN (" + strings.Join(bindings, ",") + ")"}
	rows, err := s.db.Query(assetSelectCols+selection.predicate()+" ORDER BY last_seen DESC", selection.values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}
