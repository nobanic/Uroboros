package db

import (
	"fmt"
	"strings"
)

// Each catalog source owns both its search projection and its record reader.
// Asset subtypes share a source; their exact type is checked again on send.
type mentionSource struct {
	kind       string
	sortKind   string
	projection string
	read       func(*DB, string, int64) (any, error)
}

func mentionCatalog() [3]mentionSource {
	return [3]mentionSource{
		{kind: "finding", sortKind: "'finding'", read: readMentionFinding, projection: `SELECT 'finding' AS kind, id, COALESCE(NULLIF(name,''), vulnclass) AS label,
         concat_ws(' · ', severity, status, left(summary, 160)) AS description
  FROM findings WHERE ($1='' OR $1='finding') AND
    ($2='' OR id::text=$2 OR concat_ws(' ',name,vulnclass,summary) ILIKE $3)`},
		{kind: "company", sortKind: "'company'", read: readMentionCompany, projection: `SELECT 'company', id, name, nkey FROM companies
  WHERE ($1='' OR $1='company') AND ($2='' OR id::text=$2 OR name ILIKE $3 OR nkey ILIKE $3)`},
		{kind: "asset", sortKind: "type", read: readMentionAsset, projection: `SELECT type, id,
    CASE WHEN type='endpoint' THEN concat_ws(' ',NULLIF(method,''),url)
         ELSE COALESCE(NULLIF(app_name,''),NULLIF(url,''),NULLIF(domain,''),NULLIF(ip,''),NULLIF(bundle_id,''),'asset #'||id::text) END,
    concat_ws(' · ',type,NULLIF(page_title,''),NULLIF(service_name,''),NULLIF(bundle_id,''),NULLIF(ip,''),port::text)
  FROM assets WHERE ($1='' OR $1='asset' OR type=$1) AND
    ($2='' OR id::text=$2 OR concat_ws(' ',domain,root_domain,ip,url,app_name,bundle_id,page_title,service_name,method) ILIKE $3)`},
	}
}

func mentionCatalogSQL() string {
	var query strings.Builder
	query.WriteString("\nSELECT kind, id, left(label, 160), left(description, 240) FROM (\n")
	for index, source := range mentionCatalog() {
		if index != 0 {
			query.WriteString("\n UNION ALL\n")
		}
		query.WriteString(" (")
		query.WriteString(source.projection)
		fmt.Fprintf(&query, "\n    AND ($4::bigint=0 OR (id::text=$2)<$6 OR ((id::text=$2)=$6 AND (id<$4 OR (id=$4 AND %s>$5))))", source.sortKind)
		query.WriteString("\n  ORDER BY (id::text=$2) DESC, id DESC LIMIT 21)")
	}
	query.WriteString("\n) matches ORDER BY (id::text=$2) DESC, id DESC, kind LIMIT 21")
	return query.String()
}

// ReadChatMention reads current server records, without trusting a display label
// or granting scan/write authority. Its caller owns message composition/bounds.
func (d *DB) ReadChatMention(kind string, id int64) (any, error) {
	catalog := mentionCatalog()
	for _, source := range catalog[:2] {
		if kind == source.kind {
			return source.read(d, kind, id)
		}
	}
	return catalog[2].read(d, kind, id)
}

func relatedMention[T, R any](key string, row *T, err error, relatedKey string, related func(*T) (R, error)) (any, error) {
	if err != nil || row == nil {
		return nil, err
	}
	companion, err := related(row)
	if err != nil {
		return nil, err
	}
	return map[string]any{key: row, relatedKey: companion}, nil
}

func readMentionFinding(d *DB, _ string, id int64) (any, error) {
	row, err := d.GetFinding(id)
	return relatedMention("finding", row, err, "assets", func(finding *DBFinding) ([]*Asset, error) {
		return d.Assets().GetByIDs(finding.AssetIDs)
	})
}

func readMentionCompany(d *DB, _ string, id int64) (any, error) {
	row, err := d.Companies().GetCompany(id)
	return relatedMention("company", row, err, "scope", func(company *Company) ([]ScopeRule, error) {
		return d.Companies().GetScope(company.ID)
	})
}

func readMentionAsset(d *DB, kind string, id int64) (any, error) {
	rows, err := d.Assets().GetByIDs([]int64{id})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	asset := rows[0]
	if kind != "asset" && asset.Type != kind {
		return nil, nil
	}
	result := map[string]any{"asset": asset}
	if asset.CompanyID != nil {
		company, err := d.Companies().GetCompany(*asset.CompanyID)
		if err != nil {
			return nil, err
		}
		result["company"] = company
	}
	return result, nil
}
