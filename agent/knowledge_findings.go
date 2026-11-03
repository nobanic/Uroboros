package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/internal/toolinput"
	actool "github.com/nobanic/norma/tool"
)

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "Record a confirmed vulnerability, using evidence to supply verifiable proof such as command output and logs. In a task context, pass the current intent_id. The returned finding_id is the standalone vulnerability-record ID; finding_node_id is the exploration-node ID (the first line preserves that node number).", obj(map[string]any{
		"vulnclass": str("vulnerability class"), "name": str("vulnerability name"), "severity": str("critical|high|medium|low"), "summary": str("finding summary"),
		"intent_id": idp("intent id of the current task"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "affected asset ids"},
		"evidence":         str("evidence/PoC text"),
		"evidence_hint_id": idp("optional: the hint node ID in this task that corresponds to this vulnerability; its structured traffic_refs are carried automatically; cannot reference an inherited hint or a hint for another vulnerability"),
		"traffic_refs": map[string]any{"type": "array", "description": "optional; for HTTP/HTTPS vulnerabilities, first search and verify item by item that the request/response really supports the vulnerability conclusion, then fill in the real IDs in reproduction order. For non-HTTP vulnerabilities such as TCP, or when nothing was captured or no exact record can be found, omit or pass []; this does not block reporting — explain why in evidence and provide other verifiable proof. Do not guess IDs, infer associations by domain/time, or re-probe just to pad packets. Roles: baseline normal control / proof vulnerability proof / verification supplementary verification / supporting supporting evidence.",
			"items": obj(map[string]any{"traffic_id": str("the real traffic ID returned by traffic_search"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("how this traffic supports the vulnerability conclusion")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		return toolinput.Decode(in, func(a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("report_finding requires a task context; in a platform conversation, hand the vulnerability off to the relevant task via add_task_hint, carrying any existing traffic_refs in the hint, and let the task Agent register it. An already-registered vulnerability can be bound later with bind_finding_traffic."), nil
			}
			// Auto-binding off: ignore the evidence params instead of rejecting the call.
			// stripTrafficParameters already removes them from the advertised schema, but
			// models routinely emit fields anyway — failing here would discard a confirmed
			// finding over a stray parameter. The success path below reports evidence_status
			// "not_bound" with the "disabled, associate manually on the page" note, which is what the caller needs.
			if !findingTrafficBindingEnabled() {
				a.TrafficRefs, a.EvidenceHintID = nil, nil
			}
			if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
				return actool.Errorf("evidence_hint_id must be a valid hint node ID; omit it when there is no hand-off hint"), nil
			}
			refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
			var recorded *db.RecordedFinding
			if t.findingRecorder != nil {
				recorded, err = t.findingRecorder.Record(ctx, input, refs)
			} else if len(refs) > 0 {
				return actool.Errorf("traffic-evidence storage is unavailable; the vulnerability was not recorded"), nil
			} else {
				recorded, err = t.ts.RecordFinding(ctx, input)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if t.notifyFinding != nil {
				iid := input.IntentID
				if iid <= 0 {
					iid = t.ownerNode
				}
				t.notifyFinding(iid, a.Summary)
			} else if t.notify != nil {
				t.notify()
			}
			t.writes.Findings++
			// Keep the first line's node-ID contract for existing reporter triggers.
			for i := range recorded.Traffic.Bindings {
				recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
				recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
			}
			result := struct {
				*db.RecordedFinding
				EvidenceStatus string `json:"evidence_status"`
				EvidenceNote   string `json:"evidence_note,omitempty"`
			}{RecordedFinding: recorded, EvidenceStatus: "bound"}
			if len(recorded.Traffic.Bindings) == 0 {
				result.EvidenceStatus = "not_bound"
				result.EvidenceNote = "Vulnerability saved, no traffic bound. For TCP / no-packet cases you may continue normally; if you already have verified HTTP traffic, use the available bind_finding_traffic or the vulnerability page to bind it, then complete the evidence hand-off. Do not create the vulnerability again."
				if !findingTrafficBindingEnabled() {
					result.EvidenceNote = "Vulnerability saved. Agent auto-binding of traffic is disabled; you can associate traffic manually on the page."
				}
			}
			raw, _ := json.Marshal(result)
			return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil

		})
	})
}
