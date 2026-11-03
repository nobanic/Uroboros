// Package report renders a deliverable Markdown report from the exploration
// graph, with standalone finding triage statuses and severity/evidence.
package report

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
)

// Input bundles what the report needs.
type Input struct {
	Title           string
	Goal            string
	GeneratedAt     time.Time
	AssetCounts     map[string]int
	Findings        []*db.Node
	FindingStatuses map[int64]string // standalone findings are the triage source of truth
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
	Status    string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{VulnClass: p.VulnClass, Name: p.Name, Severity: p.Severity, Summary: p.Summary, PoC: p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// Markdown renders the report.
func Markdown(in Input) string {
	return (assessmentDocument{input: in}).render()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
