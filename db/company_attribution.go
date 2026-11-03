package db

import (
	"database/sql"
	"fmt"
	"net"
)

type companyAttributionInput struct{ domain, ip, icp string }
type companyAttributionTier struct {
	kind, join, filter, order, lookup string
	value                             func(companyAttributionInput) string
}

// Ordered policy is shared by bulk rebuilds and insert-time lookups. Their original
// IP handling stays distinct: try_inet protects stored rows, net.ParseIP gates lookup input.
func companyAttributionPolicy() [3]companyAttributionTier {
	return [3]companyAttributionTier{
		{
			kind: "kind = 'domain'", join: "a.root_domain = cs.domain",
			filter: "a.type IN ('root_domain','subdomain','service','endpoint') AND a.root_domain IS NOT NULL",
			order:  "length(%[1]sdomain) DESC, %[1]scompany_id", lookup: "domain = $1",
			value: func(input companyAttributionInput) string { return input.domain },
		},
		{
			kind: "kind IN ('ip','cidr')", join: "cs.net >>= try_inet(a.ip)",
			filter: "a.type IN ('ip','subdomain','service','endpoint') AND a.ip IS NOT NULL",
			order:  "masklen(%[1]snet) DESC, %[1]scompany_id", lookup: "net >>= $1::inet",
			value: func(input companyAttributionInput) string {
				if net.ParseIP(input.ip) == nil {
					return ""
				}
				return input.ip
			},
		},
		{
			kind:   "kind = 'icp'",
			join:   `(lower(regexp_replace(COALESCE(a.icp,''), '[[:space:]]+', '', 'g')) = cs.value OR lower(regexp_replace(COALESCE(a.app_icp,''), '[[:space:]]+', '', 'g')) = cs.value)`,
			filter: "(COALESCE(a.icp,'') <> '' OR COALESCE(a.app_icp,'') <> '')",
			order:  "%[1]scompany_id", lookup: "value = $1",
			value: func(input companyAttributionInput) string { return NormalizeICP(input.icp) },
		},
	}
}

func (tier companyAttributionTier) bulkSQL() string {
	return `WITH matched AS (
 SELECT DISTINCT ON (a.id) a.id AS asset_id, cs.company_id
 FROM assets a JOIN company_scope cs ON cs.` + tier.kind + " AND " + tier.join + `
 WHERE a.company_id IS NULL AND ` + tier.filter + `
 ORDER BY a.id, ` + fmt.Sprintf(tier.order, "cs.") + `
)
UPDATE assets a SET company_id = matched.company_id, company_source = 'scope'
FROM matched WHERE a.id = matched.asset_id`
}

func applyCompanyAttribution(tx *sql.Tx) (string, error) {
	if _, err := tx.Exec(`UPDATE assets SET company_id = NULL, company_source = 'scope' WHERE company_source = 'scope'`); err != nil {
		return "", err
	}
	for _, tier := range companyAttributionPolicy() {
		if _, err := tx.Exec(tier.bulkSQL()); err != nil {
			return "", err
		}
	}
	return malformedIPAssetWarning(tx)
}

func selectCompanyAttribution(query companyScopeQueryer, rootDomain, ip, icp string) (*int64, error) {
	input := companyAttributionInput{domain: rootDomain, ip: ip, icp: icp}
	for _, tier := range companyAttributionPolicy() {
		value := tier.value(input)
		if value == "" {
			continue
		}
		statement := "SELECT company_id FROM company_scope WHERE " + tier.kind + " AND " + tier.lookup + " ORDER BY " + fmt.Sprintf(tier.order, "") + " LIMIT 1"
		var id int64
		err := query.QueryRow(statement, value).Scan(&id)
		if err == nil {
			return &id, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	return nil, nil
}
