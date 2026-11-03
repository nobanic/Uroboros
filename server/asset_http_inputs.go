package server

import "github.com/nobanic/UroborOS/db"

type companyCreateHTTPInput = struct {
	Name  string             `json:"name"`
	Logo  string             `json:"logo"`
	Scope companyScopeInputs `json:"scope"`
}

type companyScopeHTTPInput = struct {
	Scope  companyScopeInputs `json:"scope"`
	Reason string             `json:"reason"`
	Reset  bool               `json:"reset"` // if true, replace existing scope
}

type companyDeleteHTTPInput = struct {
	DeleteAssets bool `json:"delete_assets"`
}

type assetDeleteHTTPInput = struct {
	IDs []int64 `json:"ids"`
}

type assetHTTPItem = struct {
	Type string `json:"type"`
	// root_domain / subdomain
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`
	// ip
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`
	// app
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	// service http
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceIP     string           `json:"service_ip"`
	// service other
	Port        int    `json:"port"`
	ServiceName string `json:"service_name"`
	// endpoint
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

type assetBatchHTTPInput = struct {
	TaskID int64           `json:"task_id"`
	Assets []assetHTTPItem `json:"assets"`
}
