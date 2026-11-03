package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
)

type findingBatch struct{ rows []*db.DBFinding }
type findingDocument struct{ row *db.DBFinding }
type findingDocumentStyle struct{ overview, evidence, report markdownBlock }

func sortedFindingBatch(rows []*db.DBFinding) findingBatch {
	items := append([]*db.DBFinding(nil), rows...)
	sortFindingsForExport(items)
	return findingBatch{rows: items}
}

func (f findingDocument) title() string {
	return "[" + strings.ToUpper(nz(f.row.Severity, "info")) + "] " + findingTitle(f.row)
}

func (f findingDocument) metadata(out *markdownDocument, generatedAt *time.Time) {
	row := f.row
	if row.VulnClass != "" {
		out.field("Category", row.VulnClass)
	}
	if generatedAt != nil {
		out.field("Severity", nz(row.Severity, "info"))
	}
	out.field("Status", nz(row.Status, "pending"))
	if task := strings.TrimSpace(row.TaskDescription); task != "" {
		out.field("Task", task)
	}
	out.field("Discovered at", reportTime(row.CreatedAt))
	if generatedAt != nil {
		out.field("Generated at", reportTime(*generatedAt))
	}
	out.WriteString("\n")
}

func (f findingDocument) body(out *markdownDocument, style findingDocumentStyle, attachments bool) {
	out.trimmedBlock(f.row.Summary, style.overview)
	out.trimmedBlock(f.row.Evidence, style.evidence)
	out.trimmedBlock(f.row.Report, style.report)
	out.WriteString(findingTrafficMarkdown(f.row, attachments))
}

func (f findingDocument) single(generatedAt time.Time) string {
	var out markdownDocument
	out.heading(1, f.title())
	f.metadata(&out, &generatedAt)
	f.body(&out, findingDocumentStyle{
		overview: markdownBlock{"## Overview\n\n", "\n\n"},
		evidence: markdownBlock{"## Evidence\n\n```\n", "\n```\n\n"},
		report:   markdownBlock{"## Detailed Report\n\n", "\n"},
	}, true)
	return out.String()
}

func (b findingBatch) markdown(generatedAt time.Time) string {
	var out markdownDocument
	out.heading(1, "Vulnerability Findings Summary Report")
	out.field("Generated at", reportTime(generatedAt))
	out.field("Total findings", fmt.Sprint(len(b.rows)))
	out.WriteString("\n")
	counts := map[string]int{}
	for _, row := range b.rows {
		counts[row.Severity]++
	}
	out.heading(2, "Summary")
	out.WriteString("| Severity | Count |\n| --- | --- |\n")
	for _, severity := range []struct{ key, label string }{
		{"critical", "Critical"}, {"high", "High"}, {"medium", "Medium"}, {"low", "Low"},
	} {
		fmt.Fprintf(&out.Builder, "| %s | %d |\n", severity.label, counts[severity.key])
	}
	out.WriteString("\n")
	if len(b.rows) == 0 {
		out.WriteString("_No matching vulnerabilities._\n")
		return out.String()
	}
	out.heading(2, "Vulnerability Details")
	style := findingDocumentStyle{
		overview: markdownBlock{"", "\n\n"},
		evidence: markdownBlock{"**Evidence:**\n\n```\n", "\n```\n\n"},
		report:   markdownBlock{"**Detailed report:**\n\n", "\n\n"},
	}
	for index, row := range b.rows {
		finding := findingDocument{row: row}
		out.heading(3, fmt.Sprintf("%d. %s", index+1, finding.title()))
		finding.metadata(&out, nil)
		finding.body(&out, style, false)
		out.WriteString("---\n\n")
	}
	return out.String()
}

func (f findingDocument) csvRow() []string {
	row := f.row
	return []string{
		fmt.Sprintf("%d", row.ID), findingTitle(row), row.VulnClass,
		nz(row.Severity, "info"), nz(row.Status, "pending"), row.TaskDescription,
		reportTime(row.CreatedAt), strings.TrimSpace(row.Summary),
		fmt.Sprint(len(row.TrafficBindings)), findingTrafficIDs(row),
	}
}

func (b findingBatch) csv() []byte {
	var out bytes.Buffer
	out.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&out)
	_ = writer.Write([]string{"ID", "Name", "Category", "Severity", "Status", "Task", "Discovered at", "Summary", "Traffic evidence count", "Traffic evidence IDs"})
	for _, row := range b.rows {
		_ = writer.Write((findingDocument{row: row}).csvRow())
	}
	writer.Flush()
	return out.Bytes()
}
