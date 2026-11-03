package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// =====================================================================
// Unified asset table
// =====================================================================

// Asset is a row in the assets table.
type Asset struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	CompanyID  *int64  `json:"company_id,omitempty"`
	TaskIDs    []int64 `json:"task_ids"`
	Domain     string  `json:"domain,omitempty"`
	RootDomain string  `json:"root_domain,omitempty"`
	IP         string  `json:"ip,omitempty"`
	CSegment   string  `json:"c_segment,omitempty"`
	Port       *int    `json:"port,omitempty"`
	ICP        string  `json:"icp,omitempty"`
	// ip fields
	BoundDomains []string         `json:"bound_domains,omitempty"`
	OpenPorts    []map[string]any `json:"open_ports,omitempty"`
	// subdomain fields
	RecordType  string   `json:"record_type,omitempty"`
	RecordValue []string `json:"record_value,omitempty"`
	// app fields
	BundleID       string `json:"bundle_id,omitempty"`
	AppName        string `json:"app_name,omitempty"`
	Category       string `json:"category,omitempty"`
	AppDescription string `json:"app_description,omitempty"`
	AppICP         string `json:"app_icp,omitempty"`
	// service fields
	URL           string           `json:"url,omitempty"`
	ServiceType   string           `json:"service_type,omitempty"`
	ServiceName   string           `json:"service_name,omitempty"`
	FaviconMMH3   string           `json:"favicon_mmh3,omitempty"`
	StatusCode    *int             `json:"status_code,omitempty"`
	ContentLength *int64           `json:"content_length,omitempty"`
	PageTitle     string           `json:"page_title,omitempty"`
	Technologies  []string         `json:"technologies,omitempty"`
	Auth          []map[string]any `json:"auth,omitempty"`
	// endpoint fields
	Method string           `json:"method,omitempty"`
	Params []map[string]any `json:"params,omitempty"`
	// meta
	Extra             map[string]any `json:"extra,omitempty"`
	LastSeen          string         `json:"last_seen"`
	TaskSource        string         `json:"task_source,omitempty"`
	TaskSourceSummary string         `json:"task_source_summary,omitempty"`
	TaskSourceNodeID  *int64         `json:"task_source_node_id,omitempty"`
}

// AuthItem is one entry in the auth array.
type AuthItem struct {
	Type        string `json:"type,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Token       string `json:"token,omitempty"`
	Description string `json:"description,omitempty"`
}

// ParamItem is one entry in the params array.
type ParamItem struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	Type     string `json:"type,omitempty"`
}

// PortService is one entry in open_ports: {"port":22,"service":"ssh"}.
type PortService struct {
	Port    int    `json:"port"`
	Service string `json:"service,omitempty"`
}

// AssetStore operates on the assets table.
type AssetStore struct {
	db      *DB
	company *CompanyStore
	tx      *sql.Tx
}

// Assets returns the asset store.
func (d *DB) Assets() *AssetStore {
	return &AssetStore{db: d, company: d.Companies()}
}

// Companies returns the company store associated with this asset store.
func (s *AssetStore) Companies() *CompanyStore { return s.company }

// withCompanyScopeMutation serializes scope resolution and every asset write
// that consumes its result in one transaction. Nested asset side effects reuse
// the same transaction through the scoped store.
func (s *AssetStore) withCompanyScopeMutation(fn func(*AssetStore) (int64, error)) (int64, error) {
	return (assetWrite{store: s}).scoped(fn)
}

func (s *AssetStore) resolveCompanyWithICP(rootDomain, ipStr, icp string) (*int64, error) {
	if s.tx != nil {
		return resolveCompanyWithICP(s.tx, rootDomain, ipStr, icp)
	}
	return s.company.ResolveCompanyWithICP(rootDomain, ipStr, icp)
}

// =====================================================================
// Helpers
// =====================================================================

// calcCSegment computes the /24 (IPv4) or /48 (IPv6) network for an IP string.
func calcCSegment(ipStr string) string {
	if ipStr == "" {
		return ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		// IPv4 /24
		parts := strings.Split(ipStr, ".")
		if len(parts) == 4 {
			return parts[0] + "." + parts[1] + "." + parts[2] + ".0/24"
		}
		return ""
	}
	// IPv6 /48
	_, ipnet, err := net.ParseCIDR(ipStr + "/48")
	if err != nil {
		return ""
	}
	return ipnet.String()
}

// normalizeURL lowercases scheme and host, strips trailing slash from bare roots.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	s := u.String()
	if strings.HasSuffix(s, "/") && u.Path == "/" && u.RawQuery == "" {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

// parseURL extracts domain, port, service_name from a URL.
func parseURL(raw string) (domain string, port int, serviceName string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, ""
	}
	domain = strings.ToLower(u.Hostname())
	port = defaultPort(strings.ToLower(u.Scheme), u.Port())
	switch strings.ToLower(u.Scheme) {
	case "https":
		serviceName = "HTTPS"
	case "http":
		serviceName = "HTTP"
	default:
		serviceName = strings.ToUpper(u.Scheme)
	}
	return
}

func marshalJSONBArray(items []map[string]any) (string, error) {
	if len(items) == 0 {
		return "{}", nil
	}
	parts := make([]string, len(items))
	for i, m := range items {
		b, err := json.Marshal(m)
		if err != nil {
			return "", err
		}
		// PostgreSQL array literal for jsonb[]: each element must be
		// double-quoted with internal backslashes and double-quotes escaped.
		s := strings.ReplaceAll(string(b), `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		parts[i] = `"` + s + `"`
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

func marshalStringArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	escaped := make([]string, len(items))
	for i, s := range items {
		escaped[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(escaped, ",") + "}"
}

func marshalPortServices(ps []PortService) (string, error) {
	items := make([]map[string]any, len(ps))
	for i, p := range ps {
		items[i] = map[string]any{"port": p.Port, "service": p.Service}
	}
	return marshalJSONBArray(items)
}

// nullableInt64 returns sql.NullInt64.
func nullableInt(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// UpsertRootDomainReq preserves the public asset input contract.
type UpsertRootDomainReq struct {
	Domain string
	ICP    string
	TaskID int64
}

// UpsertIPReq preserves the public asset input contract.
type UpsertIPReq struct {
	IP           string
	BoundDomains []string
	OpenPorts    []PortService
	TaskID       int64
}

// UpsertSubdomainReq preserves the public asset input contract.
type UpsertSubdomainReq struct {
	Domain      string
	RecordType  string
	RecordValue []string
	ICP         string
	TaskID      int64
}

// UpsertAppReq preserves the public asset input contract.
type UpsertAppReq struct {
	Name        string
	BundleID    string
	Category    string
	Description string
	ICP         string
	CompanyID   *int64 // explicit override; nil = exact ICP auto-attribution when available
	TaskID      int64
}

// UpsertHTTPServiceReq preserves the public asset input contract.
type UpsertHTTPServiceReq struct {
	URL           string
	Technologies  []string
	StatusCode    *int
	ContentLength *int64
	PageTitle     string
	FaviconMMH3   string
	Auth          []map[string]any
	IP            string // optional, from async DNS
	TaskID        int64
}

// UpsertOtherServiceReq preserves the public asset input contract.
type UpsertOtherServiceReq struct {
	Domain      string // domain or ip required
	IP          string
	Port        int
	ServiceName string
	Auth        []map[string]any
	TaskID      int64
}

// UpsertEndpointReq preserves the public asset input contract.
type UpsertEndpointReq struct {
	URL    string
	Method string
	Params []map[string]any
	IP     string // optional
	TaskID int64
}

// ErrAssetIPInvalid marks a non-address value in an asset's ip field. Both
// insert_assets and the asset API report it per item with the item index, so one
// bad entry never costs the rest of the batch.
var ErrAssetIPInvalid = errors.New("invalid asset ip")

// ValidateAssetIP keeps hostnames out of assets.ip. Network attribution casts
// that column to inet (see try_inet in schema.sql), so a hostname stored here is
// silently invisible to every IP/CIDR scope rule — the asset simply never gets
// attributed and nobody can tell why. The message states the fix rather than
// just the fault, so an agent can correct the item on its next turn. An empty
// value is accepted: the ip field is optional for service and endpoint assets.
func ValidateAssetIP(value string) error {
	if value == "" || net.ParseIP(value) != nil {
		return nil
	}
	return fmt.Errorf(
		"%w: ip must be an IPv4/IPv6 address, got %q. If this is a hostname, use type=subdomain and fill the domain field instead; "+
			"if you really want to register an address, first resolve the A/AAAA records and fill ip with the resolved address",
		ErrAssetIPInvalid, value)
}

// AppendIPPort appends a {port, service} entry to an existing IP asset's open_ports.
func (s *AssetStore) AppendIPPort(ipStr string, port int, serviceName string) error {
	return (assetWrite{store: s}).ipPort(ipStr, port, serviceName)
}

// AppendIPBoundDomain appends a domain to an existing IP asset's bound_domains.
func (s *AssetStore) AppendIPBoundDomain(ipStr, domain string) error {
	return (assetWrite{store: s}).ipDomain(ipStr, domain)
}

// linkHostAssets ensures a service/endpoint's host is also registered as its own
// root_domain and (when it's a real subdomain, not the apex or an IP) subdomain
// asset — so those asset types stay populated and can anchor task scope. Best-effort.
func (s *AssetStore) linkHostAssets(domain, rootDomain string, taskID int64) {
	if rootDomain != "" {
		_, _ = s.UpsertRootDomain(UpsertRootDomainReq{Domain: rootDomain, TaskID: taskID})
	}
	if domain != "" && domain != rootDomain && net.ParseIP(domain) == nil {
		_, _ = s.UpsertSubdomain(UpsertSubdomainReq{Domain: domain, TaskID: taskID})
	}
}

// DeleteByTaskID removes assets owned only by taskID and detaches taskID from
// assets shared with other tasks. Full task deletion uses the coordinated
// transaction in DeleteTaskCascadePrepared; this method remains for callers
// that explicitly manage only asset associations.
func (s *AssetStore) DeleteByTaskID(taskID int64) (int64, error) {
	return (assetWrite{store: s}).removeTask(taskID)
}

// HostsByTask returns the exact HTTP host candidates attached to a task's
// assets. Domain/IP columns cover root domains, subdomains and non-HTTP
// services; URL covers HTTP services and endpoints.
func (s *AssetStore) HostsByTask(taskID int64) ([]string, error) {
	return (assetHostProjection{database: s.db, statement: assetTaskHostsSQL}).read(taskID)
}

// HostsForTaskDeletion returns hosts that belong to the task being deleted and
// are not referenced by any other live task. Current-task candidates include
// both task_ids ownership and exploration anchors so legacy seeded assets (which
// were anchor-only) are covered. Protection is host-wide: if another live task
// references any asset for a candidate host, that host's global traffic remains.
func (s *AssetStore) HostsForTaskDeletion(taskID, explorationID int64) ([]string, error) {
	return hostsForTaskDeletion(s.db, taskID, explorationID)
}

type rowsQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// hostsForTaskDeletion is shared by the read-only AssetStore API and the task
// deletion transaction. Coordinated traffic deletion must call it through the
// transaction path so asset/anchor writes stay locked until the task delete is
// committed.
func hostsForTaskDeletion(q rowsQuerier, taskID, explorationID int64) ([]string, error) {
	return (assetHostProjection{database: q, statement: assetTaskDeleteHostsSQL, partitioned: true}).read(taskID, explorationID)
}

// DeleteByCompanyID hard-deletes all assets belonging to a company. Returns rows deleted.
func (s *AssetStore) DeleteByCompanyID(companyID int64) (int64, error) {
	return (assetWrite{store: s}).removeCompany(companyID)
}

// DeleteByHost hard-deletes every asset whose host exactly matches the given value:
// root_domain / subdomain / service / endpoint (they carry the host in domain or
// root_domain) plus an ip asset and its services/endpoints (ip column). The host is
// normalized the same way it is stored (DomainKey: lowercase/trim/strip trailing dot)
// so matching is exact, not fuzzy. Passing a root domain also removes its subdomains
// and their services/endpoints (they carry root_domain = that host); passing a
// subdomain/IP removes only that host's own assets. Referencing exploration_anchors
// rows are cleaned by ON DELETE CASCADE. Returns rows deleted, grouped by type.
func (s *AssetStore) DeleteByHost(host string) (map[string]int64, error) {
	return (assetWrite{store: s}).removeHost(host)
}

// DeleteByIDs hard-deletes assets by their IDs. Returns the number of rows deleted.
func (s *AssetStore) DeleteByIDs(ids []int64) (int64, error) {
	return (assetWrite{store: s}).removeIDs(ids)
}
