package agent

import (
	"fmt"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

// assetCandidate holds the original item and the two interpretations shared
// across persistence and post-write scope. Type matching stays case sensitive.
type assetCandidate struct {
	input     assetInputItem
	kind      string
	serviceIP string
}

func prepareAssetCandidate(input assetInputItem) assetCandidate {
	ip := input.ServiceIP
	if ip == "" {
		ip = input.IP
	}
	return assetCandidate{input: input, kind: strings.TrimSpace(input.Type), serviceIP: ip}
}

func (c assetCandidate) persist(store *db.AssetStore, taskID int64) (int64, error) {
	switch c.kind {
	case "root_domain":
		request := db.UpsertRootDomainReq{TaskID: taskID}
		request.Domain, request.ICP = c.input.Domain, c.input.ICP
		return store.UpsertRootDomain(request)
	case "ip":
		request := db.UpsertIPReq{TaskID: taskID, IP: c.input.IP}
		request.BoundDomains, request.OpenPorts = c.input.BoundDomains, c.input.OpenPorts
		return store.UpsertIP(request)
	case "subdomain":
		request := db.UpsertSubdomainReq{TaskID: taskID, Domain: c.input.Domain}
		request.RecordType, request.RecordValue = c.input.RecordType, c.input.RecordValue
		request.ICP = c.input.ICP
		return store.UpsertSubdomain(request)
	case "app":
		request := db.UpsertAppReq{TaskID: taskID, CompanyID: c.input.CompanyID}
		request.Name, request.BundleID = c.input.AppName, c.input.BundleID
		request.Category, request.Description, request.ICP = c.input.Category, c.input.Description, c.input.AppICP
		return store.UpsertApp(request)
	case "service":
		return c.persistService(store, taskID)
	case "endpoint":
		request := db.UpsertEndpointReq{TaskID: taskID, URL: c.input.URL}
		request.Method, request.Params = c.input.Method, c.input.Params
		// Endpoint IP has always accepted service_ip only. The HTTP fallback
		// belongs to services and the explicit-item scope hook, not endpoints.
		request.IP = c.input.ServiceIP
		return store.UpsertEndpoint(request)
	default:
		return 0, fmt.Errorf("unknown type: %s", c.kind)
	}
}

func (c assetCandidate) persistService(store *db.AssetStore, taskID int64) (int64, error) {
	// Presence, not a trimmed/validated URL, selects HTTP. Existing database
	// normalization and validation remain the authority for the actual write.
	if c.input.URL != "" {
		request := db.UpsertHTTPServiceReq{TaskID: taskID, URL: c.input.URL, IP: c.serviceIP}
		request.Technologies, request.Auth = c.input.Technologies, c.input.Auth
		request.StatusCode, request.ContentLength = c.input.StatusCode, c.input.ContentLength
		request.PageTitle, request.FaviconMMH3 = c.input.PageTitle, c.input.FaviconMMH3
		return store.UpsertHTTPService(request)
	}
	request := db.UpsertOtherServiceReq{TaskID: taskID, Domain: c.input.Domain, IP: c.input.IP}
	request.Port, request.ServiceName, request.Auth = c.input.Port, c.input.ServiceName, c.input.Auth
	// The legacy proto input is not forwarded by this tool.
	return store.UpsertOtherService(request)
}
