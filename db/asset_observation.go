package db

import (
	"fmt"
	"net"
	"strings"
)

// An observation describes one identity and its enrichment policies. All SQL
// identifiers, casts and conflict predicates below are application constants;
// observed values always travel as parameters. The existing schema remains the
// authority for uniqueness and the enclosing store owns the scope transaction.
type assetMerge uint8

const (
	assetIdentity assetMerge = iota
	assetPreferObserved
	assetPreferStored
	assetUnion
	assetAuthUnion
	assetPortUnion
	assetParamUnion
)

type assetField struct {
	name  string
	value any
	cast  string
	merge assetMerge
}

func assetValue(name string, value any, merge assetMerge) assetField {
	return assetField{name: name, value: value, merge: merge}
}

func assetOptional(name, value string, merge assetMerge) assetField {
	var nullable any
	if value != "" {
		nullable = value
	}
	return assetValue(name, nullable, merge)
}

func assetArray(name, value, cast string, merge assetMerge) assetField {
	return assetField{name: name, value: value, cast: cast, merge: merge}
}

func assetNetwork(ip string, merge assetMerge) assetField {
	field := assetOptional("c_segment", calcCSegment(ip), merge)
	field.cast = "cidr"
	return field
}

// These predicates match the partial unique indexes; they cannot be replaced
// by parameters because PostgreSQL must prove the predicate at planning time.
const (
	assetRootIdentity     = "(domain) WHERE type = 'root_domain'"
	assetIPIdentity       = "(ip) WHERE type = 'ip'"
	assetDNSIdentity      = "(domain, COALESCE(record_type,'')) WHERE type = 'subdomain'"
	assetBundleIdentity   = "(bundle_id) WHERE type = 'app' AND bundle_id IS NOT NULL"
	assetAppIdentity      = "(app_name) WHERE type = 'app' AND bundle_id IS NULL"
	assetHTTPIdentity     = "(url) WHERE type = 'service' AND service_type = 'http'"
	assetServiceIdentity  = "(COALESCE(domain,''), COALESCE(ip,''), port, service_name) WHERE type = 'service' AND service_type = 'other'"
	assetEndpointIdentity = "(url, method) WHERE type = 'endpoint'"
)

func assetMergeExpression(field assetField) string {
	stored, observed := "assets."+field.name, "EXCLUDED."+field.name
	switch field.merge {
	case assetPreferObserved:
		return "COALESCE(" + observed + ", " + stored + ")"
	case assetPreferStored:
		return "COALESCE(" + stored + ", " + observed + ")"
	case assetUnion:
		return "(SELECT ARRAY(SELECT DISTINCT unnest(" + stored + " || " + observed + ")))"
	case assetAuthUnion:
		return fmt.Sprintf(`(SELECT ARRAY(
    SELECT DISTINCT ON (elem::text) elem
    FROM unnest(%s || %s) AS elem
))`, stored, observed)
	case assetPortUnion:
		return fmt.Sprintf(`(SELECT ARRAY(
    SELECT DISTINCT ON ((elem->>'port')::int) elem
    FROM unnest(%s || %s) AS elem
    ORDER BY (elem->>'port')::int,
        CASE WHEN elem->>'service' IS NOT NULL AND elem->>'service' <> '' THEN 0 ELSE 1 END
))`, stored, observed)
	case assetParamUnion:
		return fmt.Sprintf(`(SELECT ARRAY(
    SELECT DISTINCT ON ((elem->>'location'), (elem->>'name')) elem
    FROM unnest(%s || %s) AS elem
    ORDER BY (elem->>'location'), (elem->>'name'), elem::text DESC
))`, stored, observed)
	default:
		panic("identity fields cannot be merged")
	}
}

func (s *AssetStore) recordObservation(kind, identity string, task int64, company *int64, explicit, mergeExtra bool, fields ...assetField) (int64, error) {
	tasks, source := "{}", "scope"
	if task > 0 {
		tasks = fmt.Sprintf("{%d}", task)
	}
	if explicit {
		source = "explicit"
	}
	fields = append(fields, assetValue("type", kind, assetIdentity), assetValue("company_id", company, assetIdentity), assetValue("company_source", source, assetIdentity), assetArray("task_ids", tasks, "bigint[]", assetUnion))
	columns := make([]string, 0, len(fields))
	parameters := make([]string, 0, len(fields))
	arguments := make([]any, 0, len(fields))
	updates := make([]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, field.name)
		arguments = append(arguments, field.value)
		parameter := fmt.Sprintf("$%d", len(arguments))
		if field.cast != "" {
			parameter += "::" + field.cast
		}
		parameters = append(parameters, parameter)
		if field.merge != assetIdentity {
			updates = append(updates, field.name+" = "+assetMergeExpression(field))
		}
	}
	// A direct application assignment can replace an explicit owner. Every
	// other observation preserves an existing explicit, non-null assignment.
	incomingID, incomingSource := "", ""
	if kind == "app" {
		incomingID = "WHEN EXCLUDED.company_source = 'explicit' THEN EXCLUDED.company_id "
		incomingSource = "WHEN EXCLUDED.company_source = 'explicit' THEN 'explicit' "
	}
	updates = append(updates,
		"company_id = CASE "+incomingID+"WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN assets.company_id ELSE COALESCE(EXCLUDED.company_id, assets.company_id) END",
		"company_source = CASE "+incomingSource+"WHEN assets.company_source = 'explicit' AND assets.company_id IS NOT NULL THEN 'explicit' WHEN EXCLUDED.company_id IS NOT NULL THEN 'scope' ELSE assets.company_source END")
	if mergeExtra {
		updates = append(updates, "extra = assets.extra || EXCLUDED.extra")
	}
	updates = append(updates, "last_seen = now()")
	statement := fmt.Sprintf(`INSERT INTO assets (%s)
VALUES (%s)
ON CONFLICT %s DO UPDATE SET
%s
RETURNING id`, strings.Join(columns, ", "), strings.Join(parameters, ", "), identity, strings.Join(updates, ",\n"))
	var id int64
	err := s.tx.QueryRow(statement, arguments...).Scan(&id)
	return id, err
}

func assetRoot(domain string) string {
	root, _ := RootDomain(domain)
	if root == "" {
		return domain
	}
	return root
}

func (s *AssetStore) UpsertRootDomain(req UpsertRootDomainReq) (int64, error) {
	domain := DomainKey(req.Domain)
	if domain == "" {
		return 0, fmt.Errorf("domain is required")
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		company, err := owner.resolveCompanyWithICP(domain, "", req.ICP)
		if err != nil {
			return 0, err
		}
		return owner.recordObservation("root_domain", assetRootIdentity, req.TaskID, company, false, true,
			assetValue("domain", domain, assetIdentity), assetValue("root_domain", domain, assetIdentity), assetOptional("icp", req.ICP, assetPreferObserved))
	})
}

func (s *AssetStore) UpsertIP(req UpsertIPReq) (int64, error) {
	if req.IP == "" {
		return 0, fmt.Errorf("ip is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		company, err := owner.resolveCompanyWithICP("", req.IP, "")
		if err != nil {
			return 0, err
		}
		ports, err := marshalPortServices(req.OpenPorts)
		if err != nil {
			return 0, err
		}
		return owner.recordObservation("ip", assetIPIdentity, req.TaskID, company, false, false,
			assetValue("ip", req.IP, assetIdentity), assetNetwork(req.IP, assetPreferStored),
			assetArray("bound_domains", marshalStringArray(req.BoundDomains), "text[]", assetUnion), assetArray("open_ports", ports, "jsonb[]", assetPortUnion))
	})
}

func (s *AssetStore) UpsertSubdomain(req UpsertSubdomainReq) (int64, error) {
	domain := DomainKey(req.Domain)
	if domain == "" {
		return 0, fmt.Errorf("domain is required")
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		root := assetRoot(domain)
		company, err := owner.resolveCompanyWithICP(root, "", req.ICP)
		if err != nil {
			return 0, err
		}
		// Preserve ordering and best-effort semantics of related observations.
		_, _ = owner.UpsertRootDomain(UpsertRootDomainReq{Domain: root, TaskID: req.TaskID})
		firstIP := ""
		if req.RecordType == "A" || req.RecordType == "AAAA" {
			for _, record := range req.RecordValue {
				for _, part := range strings.Split(record, ",") {
					address := strings.TrimSpace(part)
					if net.ParseIP(address) == nil {
						continue
					}
					if firstIP == "" {
						firstIP = address
					}
					_, _ = owner.UpsertIP(UpsertIPReq{IP: address, BoundDomains: []string{domain}, TaskID: req.TaskID})
				}
			}
		}
		if company == nil && firstIP != "" {
			company, _ = owner.resolveCompanyWithICP("", firstIP, "")
		}
		return owner.recordObservation("subdomain", assetDNSIdentity, req.TaskID, company, false, true,
			assetValue("domain", domain, assetIdentity), assetValue("root_domain", root, assetIdentity), assetValue("record_type", req.RecordType, assetIdentity),
			assetArray("record_value", marshalStringArray(req.RecordValue), "text[]", assetUnion),
			assetOptional("ip", firstIP, assetPreferObserved), assetNetwork(firstIP, assetPreferObserved), assetOptional("icp", req.ICP, assetPreferObserved))
	})
}

func (s *AssetStore) UpsertApp(req UpsertAppReq) (int64, error) {
	if req.Name == "" {
		return 0, fmt.Errorf("app name is required")
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		company := req.CompanyID
		if company == nil {
			var err error
			company, err = owner.resolveCompanyWithICP("", "", req.ICP)
			if err != nil {
				return 0, err
			}
		}
		identity, nameMerge := assetAppIdentity, assetIdentity
		if req.BundleID != "" {
			identity, nameMerge = assetBundleIdentity, assetPreferObserved
		}
		return owner.recordObservation("app", identity, req.TaskID, company, req.CompanyID != nil, false,
			assetOptional("bundle_id", req.BundleID, assetIdentity), assetValue("app_name", req.Name, nameMerge),
			assetOptional("category", req.Category, assetPreferObserved), assetOptional("app_description", req.Description, assetPreferObserved), assetOptional("app_icp", req.ICP, assetPreferObserved))
	})
}

func (s *AssetStore) UpsertHTTPService(req UpsertHTTPServiceReq) (int64, error) {
	if req.URL == "" {
		return 0, fmt.Errorf("url is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		location := normalizeURL(req.URL)
		domain, port, service := parseURL(location)
		root := assetRoot(domain)
		company, err := owner.resolveCompanyWithICP(root, req.IP, "")
		if err != nil {
			return 0, err
		}
		auth, err := marshalJSONBArray(req.Auth)
		if err != nil {
			return 0, err
		}
		id, err := owner.recordObservation("service", assetHTTPIdentity, req.TaskID, company, false, false,
			assetValue("url", location, assetIdentity), assetValue("service_type", "http", assetIdentity), assetValue("service_name", service, assetIdentity),
			assetOptional("domain", domain, assetIdentity), assetValue("root_domain", root, assetIdentity), assetOptional("ip", req.IP, assetPreferObserved),
			assetValue("port", nullableInt(port), assetIdentity), assetNetwork(req.IP, assetPreferObserved),
			assetOptional("favicon_mmh3", req.FaviconMMH3, assetPreferObserved), assetOptional("page_title", req.PageTitle, assetPreferObserved),
			assetValue("status_code", req.StatusCode, assetPreferObserved), assetValue("content_length", req.ContentLength, assetPreferObserved),
			assetArray("technologies", marshalStringArray(req.Technologies), "text[]", assetUnion), assetArray("auth", auth, "jsonb[]", assetAuthUnion))
		if err != nil {
			return 0, err
		}
		owner.linkHostAssets(domain, root, req.TaskID)
		if req.IP != "" {
			owner.observeServiceIP(req.IP, domain, port, service, req.TaskID)
		}
		return id, nil
	})
}

func (s *AssetStore) UpsertOtherService(req UpsertOtherServiceReq) (int64, error) {
	if req.Domain == "" && req.IP == "" {
		return 0, fmt.Errorf("domain or ip is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	if req.Port == 0 {
		return 0, fmt.Errorf("port is required")
	}
	if req.ServiceName == "" {
		return 0, fmt.Errorf("service_name is required")
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		service, domain := strings.ToLower(strings.TrimSpace(req.ServiceName)), DomainKey(req.Domain)
		root := ""
		if domain != "" {
			root = assetRoot(domain)
		}
		company, err := owner.resolveCompanyWithICP(root, req.IP, "")
		if err != nil {
			return 0, err
		}
		auth, err := marshalJSONBArray(req.Auth)
		if err != nil {
			return 0, err
		}
		id, err := owner.recordObservation("service", assetServiceIdentity, req.TaskID, company, false, false,
			assetValue("service_type", "other", assetIdentity), assetValue("service_name", service, assetIdentity),
			assetOptional("domain", domain, assetPreferObserved), assetOptional("root_domain", root, assetIdentity), assetOptional("ip", req.IP, assetPreferObserved),
			assetValue("port", req.Port, assetIdentity), assetNetwork(req.IP, assetPreferObserved), assetArray("auth", auth, "jsonb[]", assetAuthUnion))
		if err != nil {
			return 0, err
		}
		if req.IP != "" && req.Port > 0 {
			owner.observeServiceIP(req.IP, domain, req.Port, service, req.TaskID)
		}
		owner.linkHostAssets(domain, root, req.TaskID)
		return id, nil
	})
}

func (s *AssetStore) observeServiceIP(ip, domain string, port int, service string, task int64) {
	var domains []string
	var ports []PortService
	if domain != "" {
		domains = []string{domain}
	}
	if port > 0 {
		ports = []PortService{{Port: port, Service: service}}
	}
	_, _ = s.UpsertIP(UpsertIPReq{IP: ip, BoundDomains: domains, OpenPorts: ports, TaskID: task})
}

func (s *AssetStore) UpsertEndpoint(req UpsertEndpointReq) (int64, error) {
	if req.URL == "" {
		return 0, fmt.Errorf("url is required")
	}
	if req.Method == "" {
		return 0, fmt.Errorf("method is required")
	}
	if err := ValidateAssetIP(req.IP); err != nil {
		return 0, err
	}
	return s.withCompanyScopeMutation(func(owner *AssetStore) (int64, error) {
		location := normalizeURL(req.URL)
		domain, port, _ := parseURL(location)
		root := assetRoot(domain)
		company, err := owner.resolveCompanyWithICP(root, req.IP, "")
		if err != nil {
			return 0, err
		}
		params, err := marshalJSONBArray(req.Params)
		if err != nil {
			return 0, err
		}
		id, err := owner.recordObservation("endpoint", assetEndpointIdentity, req.TaskID, company, false, false,
			assetValue("url", location, assetIdentity), assetValue("method", strings.ToUpper(req.Method), assetIdentity),
			assetOptional("domain", domain, assetIdentity), assetOptional("root_domain", root, assetIdentity), assetOptional("ip", req.IP, assetPreferObserved),
			assetValue("port", nullableInt(port), assetIdentity), assetNetwork(req.IP, assetPreferObserved), assetArray("params", params, "jsonb[]", assetParamUnion))
		if err != nil {
			return 0, err
		}
		owner.linkHostAssets(domain, root, req.TaskID)
		if req.IP != "" {
			_, _ = owner.UpsertIP(UpsertIPReq{IP: req.IP, TaskID: req.TaskID})
		}
		return id, nil
	})
}
