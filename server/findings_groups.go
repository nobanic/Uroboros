package server

import (
	"github.com/nobanic/UroborOS/db"
	"net/http"
	"net/url"
	"strings"
)

const maxFindingFollowUpRunes = 4000

func findingPaginationParam(raw string, fallback, upperBound int) int {
	value := atoiDefault(raw, fallback)
	if value <= 0 {
		value = fallback
	}
	if upperBound > 0 && value > upperBound {
		value = upperBound
	}
	return value
}

// findingFilterFromQuery builds the shared findings filter from a request's
// query string. List / groups / asset tree / export all share this one parser; add a new filter option only here.
func findingFilterFromQuery(q url.Values) db.FindingFilter {
	return db.FindingFilter{
		Severity:  normFilter(q.Get("severity")),
		Status:    normFilter(q.Get("status")),
		VulnClass: normFilter(q.Get("vulnclass")),
		// task_id (independent of the task parameter that switches to the "by task node" branch): filters the global table by task.
		TaskID:     normFilter(q.Get("task_id")),
		Query:      q.Get("q"),
		Sort:       q.Get("sort"),
		AssetScope: strings.TrimSpace(q.Get("asset_scope")),
	}
}

// findingAssetTree serves the "by asset" view's left-hand tree: every asset that
// carries at least one matching finding, plus the ancestors needed to place it.
func (s *Server) findingAssetTree(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).assetTree())
}

// findingGroups serves the task-group page used by the global findings view.
// Findings inside a group continue to use the existing flat findings endpoint,
// preserving dashboard/export clients and giving every expanded group its own
// independent page cursor.
func (s *Server) findingGroups(w http.ResponseWriter, r *http.Request) {
	writeFinding(w, s.findingRequest(r).groups())
}

// deepenFinding creates a high-priority human Worker intent under the finding's
// original task. The DB write validates and links the live finding node; task
// admission then revives or queues the task through the shared concurrency path.
func (s *Server) deepenFinding(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).manualFinding()
}
