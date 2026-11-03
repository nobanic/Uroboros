package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

type assessmentDocument struct{ input Input }

func (d assessmentDocument) status(id int64) string {
	status := d.input.FindingStatuses[id]
	if !db.ValidFindingStatus(status) {
		return db.FindingPending
	}
	return status
}

func (d assessmentDocument) summary(out *markdownDocument) {
	out.heading(2, "Summary")
	counts := map[string]int{}
	for _, node := range d.input.Findings {
		counts[d.status(node.ID)]++
	}
	out.count("Recorded findings", len(d.input.Findings))
	out.count("Confirmed findings", counts[db.FindingConfirmed])
	for _, status := range []string{db.FindingPending, db.FindingInProgress, db.FindingFixed, db.FindingResolved, db.FindingFalsePositive, db.FindingIgnored, db.FindingDuplicate, db.FindingRiskAccepted} {
		if counts[status] > 0 {
			out.count(status, counts[status])
		}
	}
	out.WriteString("- Assets: ")
	var types []string
	for kind := range d.input.AssetCounts {
		types = append(types, kind)
	}
	sort.Strings(types)
	for index, kind := range types {
		if index > 0 {
			out.WriteString(", ")
		}
		fmt.Fprintf(&out.Builder, "%s %d", kind, d.input.AssetCounts[kind])
	}
	out.WriteString("\n\n")
}

func (d assessmentDocument) findings(out *markdownDocument) {
	out.heading(2, "Findings")
	if len(d.input.Findings) == 0 {
		out.WriteString("_No findings recorded for this task._\n\n")
		return
	}
	items := make([]findingView, 0, len(d.input.Findings))
	for _, node := range d.input.Findings {
		finding := parseFinding(node)
		finding.Status = d.status(node.ID)
		items = append(items, finding)
	}
	sort.SliceStable(items, func(i, j int) bool { return sevRank[items[i].Severity] < sevRank[items[j].Severity] })
	for index, finding := range items {
		title := fmt.Sprintf("%d. [%s] %s", index+1, strings.ToUpper(nz(finding.Severity, "info")), nz(finding.Name, nz(finding.VulnClass, "Uncategorized")))
		out.heading(3, title)
		out.WriteString("**Status:** " + finding.Status + "\n\n")
		out.paragraph(nz(finding.Summary, ""))
		out.block(finding.PoC, markdownBlock{"**PoC / Evidence:**\n\n```\n", "\n```\n\n"})
	}
}

func (d assessmentDocument) render() string {
	var out markdownDocument
	out.heading(1, "Penetration Test Report — "+nz(d.input.Title, "Untitled Task"))
	out.field("Objective", nz(d.input.Goal, "(unspecified)"))
	out.field("Generated at", reportTime(d.input.GeneratedAt))
	out.WriteString("\n")
	d.summary(&out)
	d.findings(&out)
	return out.String()
}
