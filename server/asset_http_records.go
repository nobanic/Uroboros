package server

import (
	"fmt"
	"strconv"

	"github.com/nobanic/UroborOS/db"
)

type assetPageSelection struct {
	kind, expression string
	company, task    int64
	limit, offset    int
}

func (read assetHTTP) assets() {
	store := read.assetStore()
	if store == nil {
		return
	}
	query := read.request.URL.Query()
	selection := assetPageSelection{kind: query.Get("type"), expression: query.Get("dsl")}
	selection.limit, _ = strconv.Atoi(query.Get("limit"))
	selection.offset, _ = strconv.Atoi(query.Get("offset"))
	if selection.limit <= 0 {
		selection.limit = defaultAssetPageSize
	} else if selection.limit > maxAssetPageSize {
		selection.limit = maxAssetPageSize
	}
	if selection.offset < 0 {
		selection.offset = 0
	}
	if selection.expression != "" {
		if err := db.ValidateDSL(selection.expression); err != nil {
			writeErr(read.writer, 400, err.Error())
			return
		}
	}
	selection.task, _ = strconv.ParseInt(query.Get("task_id"), 10, 64)
	selection.company, _ = strconv.ParseInt(query.Get("company_id"), 10, 64)
	read.complete(selection.page(store))
}

func (selection assetPageSelection) page(store *db.AssetStore) (map[string]any, error) {
	var count func() (int, error)
	var rows func() ([]*db.Asset, error)
	switch {
	case selection.expression != "":
		count = func() (int, error) { return store.CountDSL(selection.expression, selection.kind, selection.task) }
		rows = func() ([]*db.Asset, error) {
			return store.QueryDSL(selection.expression, selection.kind, selection.task, selection.limit, selection.offset)
		}
	case selection.company > 0:
		count = func() (int, error) { return store.CountByCompany(selection.company, selection.kind) }
		rows = func() ([]*db.Asset, error) {
			return store.QueryByCompany(selection.company, selection.kind, selection.limit, selection.offset)
		}
	case selection.task > 0:
		count = func() (int, error) { return store.CountByTask(selection.task, selection.kind) }
		rows = func() ([]*db.Asset, error) {
			return store.QueryByTask(selection.task, selection.kind, selection.limit, selection.offset)
		}
	default:
		if selection.kind == "" {
			selection.kind = "root_domain"
		}
		count = func() (int, error) { return store.CountByType(selection.kind) }
		rows = func() ([]*db.Asset, error) {
			return store.QueryByType(selection.kind, selection.limit, selection.offset)
		}
	}
	assets := []*db.Asset{}
	total, err := count()
	if err == nil && selection.offset < total {
		assets, err = rows()
	}
	return map[string]any{"count": len(assets), "total": total, "assets": assets}, err
}

func (read assetHTTP) counts() {
	store := read.assetStore()
	if store == nil {
		return
	}
	taskID, _ := strconv.ParseInt(read.request.URL.Query().Get("task_id"), 10, 64)
	if taskID > 0 {
		read.complete(store.CountsByTypeForTask(taskID))
	} else {
		read.complete(store.CountsByType())
	}
}

func (read assetHTTP) deleteAssets() {
	store := read.assetStore()
	if store == nil {
		return
	}
	var input assetDeleteHTTPInput
	if !read.assetInput(&input) {
		return
	}
	if len(input.IDs) == 0 {
		writeErr(read.writer, 400, "ids required")
		return
	}
	deleted, err := store.DeleteByIDs(input.IDs)
	read.complete(map[string]any{"deleted": deleted}, err)
}

type assetHTTPInserted struct {
	Index int    `json:"index"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}
type assetHTTPProblem struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

func (read assetHTTP) insertAssets() {
	store := read.assetStore()
	if store == nil {
		return
	}
	var input assetBatchHTTPInput
	if !read.assetInput(&input) {
		return
	}
	var results []assetHTTPInserted
	var problems []assetHTTPProblem
	for index, item := range input.Assets {
		id, err := registerHTTPAsset(store, input.TaskID, item)
		if err != nil {
			problems = append(problems, assetHTTPProblem{Index: index, Error: err.Error()})
			continue
		}
		if input.TaskID > 0 {
			_ = store.SetTaskAssetSource(input.TaskID, id, "api", "registered via the asset API", nil)
		}
		results = append(results, assetHTTPInserted{Index: index, ID: id, Type: item.Type})
	}
	if len(results) > 0 && input.TaskID > 0 {
		if task, ok := read.server.m.Task(strconv.FormatInt(input.TaskID, 10)); ok {
			task.Notify()
		}
	}
	read.complete(map[string]any{"results": results, "errors": problems}, nil)
}

func registerHTTPAsset(store *db.AssetStore, taskID int64, item assetHTTPItem) (int64, error) {
	switch item.Type {
	case "root_domain":
		return store.UpsertRootDomain(db.UpsertRootDomainReq{Domain: item.Domain, ICP: item.ICP, TaskID: taskID})
	case "ip":
		return store.UpsertIP(db.UpsertIPReq{IP: item.IP, BoundDomains: item.BoundDomains, OpenPorts: item.OpenPorts, TaskID: taskID})
	case "subdomain":
		return store.UpsertSubdomain(db.UpsertSubdomainReq{Domain: item.Domain, RecordType: item.RecordType, RecordValue: item.RecordValue, ICP: item.ICP, TaskID: taskID})
	case "app":
		return store.UpsertApp(db.UpsertAppReq{Name: item.AppName, BundleID: item.BundleID, Category: item.Category, Description: item.Description, ICP: item.AppICP, TaskID: taskID})
	case "service":
		if item.URL == "" {
			return store.UpsertOtherService(db.UpsertOtherServiceReq{Domain: item.Domain, IP: item.IP, Port: item.Port, ServiceName: item.ServiceName, Auth: item.Auth, TaskID: taskID})
		}
		return store.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: item.URL, Technologies: item.Technologies, StatusCode: item.StatusCode, ContentLength: item.ContentLength, PageTitle: item.PageTitle, FaviconMMH3: item.FaviconMMH3, Auth: item.Auth, IP: assetHTTPServiceIP(item), TaskID: taskID})
	case "endpoint":
		return store.UpsertEndpoint(db.UpsertEndpointReq{URL: item.URL, Method: item.Method, Params: item.Params, IP: assetHTTPServiceIP(item), TaskID: taskID})
	default:
		return 0, fmt.Errorf("unknown type: %s", item.Type)
	}
}

func assetHTTPServiceIP(item assetHTTPItem) string {
	if item.ServiceIP != "" {
		return item.ServiceIP
	}
	return item.IP
}
