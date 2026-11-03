package report

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
)

// Rendering for the findings page "Export": renders a batch of finding table
// rows into a summary Markdown, a single-finding Markdown, or CSV. JSON is
// serialized directly from the DTO in the server layer, not here.

// sortFindingsForExport sorts by severity descending, then by time descending,
// matching the grouping of the summary report.
func sortFindingsForExport(fs []*db.DBFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := sevRank[fs[i].Severity], sevRank[fs[j].Severity]
		if ri != rj {
			return ri < rj // a smaller sevRank is more severe
		}
		return fs[i].CreatedAt.After(fs[j].CreatedAt)
	})
}

// findingTitle takes the vulnerability's readable title: name → category → "Uncategorized".
func findingTitle(f *db.DBFinding) string {
	return nz(f.Name, nz(f.VulnClass, "Uncategorized"))
}

// FindingsMarkdown combines a batch of findings into one summary report (a
// summary + grouping by severity, each with category/status/task/evidence/detailed report).
func FindingsMarkdown(fs []*db.DBFinding, generatedAt time.Time) string {
	return sortedFindingBatch(fs).markdown(generatedAt)
}

// SingleFindingMarkdown renders a single vulnerability as a standalone Markdown (for the one-file-per-vulnerability bundle).
func SingleFindingMarkdown(f *db.DBFinding, generatedAt time.Time) string {
	return (findingDocument{row: f}).single(generatedAt)
}

var unsafeFilenameChars = regexp.MustCompile(`[^\p{Han}\p{L}\p{N}._-]+`)

// FindingFilename generates a safe .md filename for the one-file-per-vulnerability
// bundle, like `critical_SQL-injection_#123.md`. Strips path separators and control
// characters to avoid illegal paths inside the zip.
func FindingFilename(f *db.DBFinding) string {
	sev := nz(f.Severity, "info")
	title := findingTitle(f)
	name := fmt.Sprintf("%s_%s_#%d", sev, title, f.ID)
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = fmt.Sprintf("finding_%d", f.ID)
	}
	// Defensive: strip one more path layer to rule out zip slip.
	name = path.Base(name)
	if len(name) > 120 {
		name = name[:120]
	}
	return name + ".md"
}

// FindingsCSV renders a batch of findings into CSV (with a UTF-8 BOM, so Excel
// detects UTF-8 correctly). It does not include the long report/evidence text,
// only summary-level fields; for the full text, export Markdown/JSON.
func FindingsCSV(fs []*db.DBFinding) []byte {
	return sortedFindingBatch(fs).csv()
}

func findingTrafficIDs(f *db.DBFinding) string {
	ids := make([]string, 0, len(f.TrafficBindings))
	for _, b := range f.TrafficBindings {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	return strings.Join(ids, ",")
}

func findingTrafficMarkdown(f *db.DBFinding, attachments bool) string {
	stale := f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion
	if len(f.TrafficBindings) == 0 && !stale {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## Linked Traffic Evidence\n\n")
	fmt.Fprintf(&out, "Evidence version: %d; bindings: %d.\n\n", f.EvidenceVersion, len(f.TrafficBindings))
	if stale {
		out.WriteString("Evidence has changed; the detailed report is pending update.\n\n")
	}
	for i, b := range f.TrafficBindings {
		fmt.Fprintf(&out, "%d. **Evidence #%d · %s** — `%s %s`, status code %d\n", i+1, b.ID, b.Role, b.Snapshot.Method, strings.ReplaceAll(b.Snapshot.URL, "`", "%60"), b.Snapshot.Status)
		if b.Note != "" {
			fmt.Fprintf(&out, "   %s\n", strings.ReplaceAll(b.Note, "\n", "\n   "))
		}
		if attachments {
			fmt.Fprintf(&out, "   [Request](evidence/%d/%d/request.http) · [Response](evidence/%d/%d/response.http)\n", f.ID, b.ID, f.ID, b.ID)
		}
	}
	out.WriteString("\n")
	return out.String()
}
