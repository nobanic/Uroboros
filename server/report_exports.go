package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/report"
)

type reportExportRequest struct {
	server   *Server
	response http.ResponseWriter
	request  *http.Request
}

type findingExportSelection struct {
	ids    []int64
	filter db.FindingFilter
}

func selectFindingExport(query url.Values) (findingExportSelection, error) {
	var selection findingExportSelection
	scope := query.Get("scope")
	switch scope {
	case "selected":
		for _, part := range strings.Split(query.Get("ids"), ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return selection, fmt.Errorf("bad finding id: %s", part)
			}
			selection.ids = append(selection.ids, id)
		}
		if len(selection.ids) == 0 {
			return selection, fmt.Errorf("no findings selected")
		}
	case "all":
	case "filtered", "":
		selection.filter = findingFilterFromQuery(query)
	default:
		return selection, fmt.Errorf("bad scope: %s", scope)
	}
	return selection, nil
}

type findingExportReply struct {
	request     reportExportRequest
	rows        []*db.DBFinding
	stage       string
	generatedAt time.Time
}

func (e reportExportRequest) findings() {
	query := e.request.URL.Query()
	format := query.Get("format")
	selection, err := selectFindingExport(query)
	if err != nil {
		writeErr(e.response, 400, err.Error())
		return
	}
	rows, err := e.server.m.pg.ListFindingsForExport(selection.filter, selection.ids)
	if err != nil {
		writeErr(e.response, 500, err.Error())
		return
	}
	stage, err := os.MkdirTemp("", "uroboros-finding-export-")
	if err != nil {
		writeErr(e.response, 500, err.Error())
		return
	}
	defer os.RemoveAll(stage)
	// Freeze every format before dispatch. Even an invalid format retains the
	// original selection/staging failure precedence and temporary-file lifetime.
	if err = e.server.evidenceStore().StageFindingsExport(e.request.Context(), rows, stage, format == "md-zip"); err != nil {
		evidenceError(e.response, err)
		return
	}
	reply := findingExportReply{request: e, rows: rows, stage: stage, generatedAt: time.Now()}
	switch format {
	case "md-single":
		reply.attachment("text/markdown; charset=utf-8", "md")
		_, _ = e.response.Write([]byte(report.FindingsMarkdown(rows, reply.generatedAt)))
	case "md-zip":
		reply.zip()
	case "csv":
		reply.attachment("text/csv; charset=utf-8", "csv")
		_, _ = e.response.Write(report.FindingsCSV(rows))
	case "json":
		reply.json()
	default:
		writeErr(e.response, 400, "bad format: "+format)
	}
}

func (e findingExportReply) attachment(contentType, extension string) {
	filename := "findings-" + e.generatedAt.Format("20060102-150405") + "." + extension
	e.request.response.Header().Set("Content-Type", contentType)
	e.request.response.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
}

func (e findingExportReply) zip() {
	path := filepath.Join(e.stage, "findings.zip")
	if err := buildFindingsEvidenceZip(path, e.rows, e.stage, e.generatedAt); err != nil {
		writeErr(e.request.response, 500, err.Error())
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeErr(e.request.response, 500, err.Error())
		return
	}
	defer file.Close()
	e.attachment("application/zip", "zip")
	http.ServeContent(e.request.response, e.request.request, "findings.zip", e.generatedAt, file)
}

func (e findingExportReply) json() {
	assets := e.request.server.resolveFindingAssets(e.rows)
	out := make([]FindingDTO, 0, len(e.rows))
	for _, finding := range e.rows {
		for index := range finding.TrafficBindings {
			finding.TrafficBindings[index].Snapshot.ReqHead = ""
			finding.TrafficBindings[index].Snapshot.RespHead = ""
		}
		out = append(out, findingFromDB(finding, assets))
	}
	e.attachment("application/json; charset=utf-8", "json")
	encoder := json.NewEncoder(e.request.response)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(out)
}

func (e reportExportRequest) taskReport() {
	task := e.server.m.ResolveTask(e.request.URL.Query().Get("task"))
	if task == nil {
		writeErr(e.response, 404, "no active task")
		return
	}
	findings, err := task.Store.ListByKind(db.KindFinding, 1000)
	if err != nil {
		writeErr(e.response, 500, err.Error())
		return
	}
	statuses, err := e.server.m.pg.FindingStatusesForExploration(e.request.Context(), task.Store.ID())
	if err != nil {
		writeErr(e.response, 500, err.Error())
		return
	}
	counts := map[string]int{}
	for _, kind := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
		assets, _ := e.server.m.Assets().QueryByType(kind, 100000, 0)
		if len(assets) > 0 {
			counts[kind] = len(assets)
		}
	}
	markdown := report.Markdown(report.Input{
		Title: task.Description, Goal: task.Goal, GeneratedAt: time.Now(),
		AssetCounts: counts, Findings: findings, FindingStatuses: statuses,
	})
	e.response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	e.response.WriteHeader(200)
	_, _ = e.response.Write([]byte(markdown))
}
