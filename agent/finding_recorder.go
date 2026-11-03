package agent

import (
	"context"

	"github.com/nobanic/UroborOS/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Vulnerability traffic evidence (optional)**: when calling report_finding to report a vulnerability, if you have HTTP requests/responses that you have already reviewed and confirmed support the vulnerability conclusion, you may use traffic_refs to bind the real IDs in reproduction order; domain and timestamp are only used to filter candidates and do not imply an association. For non-HTTP vulnerabilities such as TCP, or when traffic was not captured or has no exact match, omit it or pass [], keep other verifiable evidence such as command output and logs in evidence, and it is recommended to explain why nothing was bound. Do not guess IDs, and do not re-probe just to fill in packets."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
