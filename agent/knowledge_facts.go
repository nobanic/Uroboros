package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/internal/toolinput"
	actool "github.com/nobanic/norma/tool"
	"strings"
)

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem is a single fact for record_fact, in either batch or single mode.
type factItem struct {
	Summary           string            `json:"summary"`
	Detail            string            `json:"detail"`
	Evidence          string            `json:"evidence"`   // one line of key evidence (command + key output lines), supporting the conclusion and easy to double-check later
	Confidence        string            `json:"confidence"` // observed (seen directly) | inferred (deduced from symptoms)
	Negative          bool              `json:"negative"`
	ClosureStatus     string            `json:"closure_status"`
	TestedVariants    []string          `json:"tested_variants"`
	ContractEvidence  string            `json:"contract_evidence"`
	UnresolvedLeads   []string          `json:"unresolved_leads"`
	ProvenanceStatus  string            `json:"provenance_status"`
	ProvenanceNote    string            `json:"provenance_note"`
	TargetVersion     string            `json:"target_version_evidence"`
	SourceRevision    string            `json:"source_revision_evidence"`
	RuntimeValidation string            `json:"runtime_validation"`
	IntentID          json.RawMessage   `json:"intent_id"`
	AssetIDs          []json.RawMessage `json:"asset_ids"`
}

const (
	factClosureObservation            = "observation"
	factClosureExhausted              = "exhausted"
	factProvenanceNotApplicable       = "not_applicable"
	factProvenanceTargetObserved      = "target_observed"
	factProvenanceVersionMatched      = "version_matched"
	factProvenanceUnversionedExternal = "unversioned_external"
)

func normalizeFactStringList(name string, values []string) ([]string, error) {
	const maxItems = 16
	const maxItemLen = 400
	if len(values) > maxItems {
		return nil, fmt.Errorf("%s must contain at most %d entries", name, maxItems)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > maxItemLen {
			return nil, fmt.Errorf("%s entries must be at most %d characters", name, maxItemLen)
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

// normalizeFactClosure makes direction-closing negative conclusions a structured,
// machine-checkable claim. Every ordinary fact is merely an observation. A worker
// may mark a direction exhausted only after it supplies observed evidence, at least
// two materially distinct tested variants, the request/input contract basis, and no
// unresolved lead. This prevents absolute prose such as "no SSRF is possible" from
// silently closing an endpoint after a few guessed payloads.
func normalizeFactClosure(it *factItem) error {
	it.Confidence = strings.ToLower(strings.TrimSpace(it.Confidence))
	if it.Confidence != "" && it.Confidence != "observed" && it.Confidence != "inferred" {
		return fmt.Errorf("confidence must be observed or inferred")
	}
	it.ClosureStatus = strings.ToLower(strings.TrimSpace(it.ClosureStatus))
	if it.ClosureStatus == "" {
		it.ClosureStatus = factClosureObservation
	}
	if it.ClosureStatus != factClosureObservation && it.ClosureStatus != factClosureExhausted {
		return fmt.Errorf("closure_status must be observation or exhausted")
	}
	var err error
	it.TestedVariants, err = normalizeFactStringList("tested_variants", it.TestedVariants)
	if err != nil {
		return err
	}
	it.UnresolvedLeads, err = normalizeFactStringList("unresolved_leads", it.UnresolvedLeads)
	if err != nil {
		return err
	}
	it.ContractEvidence = strings.TrimSpace(it.ContractEvidence)
	if len(it.ContractEvidence) > 1200 {
		return fmt.Errorf("contract_evidence must be at most 1200 characters")
	}
	it.ProvenanceStatus = strings.ToLower(strings.TrimSpace(it.ProvenanceStatus))
	switch it.ProvenanceStatus {
	case "", factProvenanceNotApplicable, factProvenanceTargetObserved,
		factProvenanceVersionMatched, factProvenanceUnversionedExternal:
	default:
		return fmt.Errorf("provenance_status must be not_applicable, target_observed, version_matched, or unversioned_external")
	}
	it.ProvenanceNote = strings.TrimSpace(it.ProvenanceNote)
	it.TargetVersion = strings.TrimSpace(it.TargetVersion)
	it.SourceRevision = strings.TrimSpace(it.SourceRevision)
	it.RuntimeValidation = strings.TrimSpace(it.RuntimeValidation)
	for name, value := range map[string]string{
		"provenance_note":          it.ProvenanceNote,
		"target_version_evidence":  it.TargetVersion,
		"source_revision_evidence": it.SourceRevision,
		"runtime_validation":       it.RuntimeValidation,
	} {
		if len(value) > 1200 {
			return fmt.Errorf("%s must be at most 1200 characters", name)
		}
	}
	if it.ClosureStatus != factClosureExhausted {
		return nil
	}
	if !it.Negative {
		return fmt.Errorf("closure_status=exhausted requires negative=true")
	}
	if it.Confidence != "observed" {
		return fmt.Errorf("closure_status=exhausted requires confidence=observed")
	}
	if strings.TrimSpace(it.Evidence) == "" {
		return fmt.Errorf("closure_status=exhausted requires evidence")
	}
	if len(it.TestedVariants) < 2 {
		return fmt.Errorf("closure_status=exhausted requires at least two materially distinct tested_variants")
	}
	if it.ContractEvidence == "" {
		return fmt.Errorf("closure_status=exhausted requires contract_evidence establishing the expected input/request contract or why no contract applies")
	}
	if len(it.UnresolvedLeads) > 0 {
		return fmt.Errorf("closure_status=exhausted is incompatible with unresolved_leads; record closure_status=observation and hand the leads to the planner")
	}
	if it.ProvenanceStatus == "" {
		return fmt.Errorf("closure_status=exhausted requires provenance_status so version-sensitive conclusions cannot silently project an external default branch onto the target")
	}
	switch it.ProvenanceStatus {
	case factProvenanceUnversionedExternal:
		return fmt.Errorf("closure_status=exhausted is incompatible with provenance_status=unversioned_external; establish target/runtime provenance and use observation meanwhile")
	case factProvenanceNotApplicable:
		if it.ProvenanceNote == "" {
			return fmt.Errorf("provenance_status=not_applicable requires provenance_note explaining why the conclusion does not depend on external versioned source")
		}
	case factProvenanceTargetObserved:
		if it.RuntimeValidation == "" {
			return fmt.Errorf("provenance_status=target_observed requires runtime_validation tied to the deployed target")
		}
	case factProvenanceVersionMatched:
		if it.TargetVersion == "" || it.SourceRevision == "" || it.RuntimeValidation == "" {
			return fmt.Errorf("provenance_status=version_matched requires target_version_evidence, source_revision_evidence, and runtime_validation")
		}
	}
	return nil
}

// recordOneFact writes one fact node and links it to the intent (intent→yields→fact). defaultIntent is
// the default intent for batch mode (used when this entry gives no intent_id).
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary cannot be empty")
	}
	if err := normalizeFactClosure(&it); err != nil {
		return 0, err
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	if it.Negative {
		payload["negative"] = true
	}
	payload["closure_status"] = it.ClosureStatus
	if len(it.TestedVariants) > 0 {
		payload["tested_variants"] = it.TestedVariants
	}
	if it.ContractEvidence != "" {
		payload["contract_evidence"] = it.ContractEvidence
	}
	if len(it.UnresolvedLeads) > 0 {
		payload["unresolved_leads"] = it.UnresolvedLeads
	}
	if it.ProvenanceStatus != "" {
		payload["provenance_status"] = it.ProvenanceStatus
	}
	if it.ProvenanceNote != "" {
		payload["provenance_note"] = it.ProvenanceNote
	}
	if it.TargetVersion != "" {
		payload["target_version_evidence"] = it.TargetVersion
	}
	if it.SourceRevision != "" {
		payload["source_revision_evidence"] = it.SourceRevision
	}
	if it.RuntimeValidation != "" {
		payload["runtime_validation"] = it.RuntimeValidation
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id must be an intent of this task (related-task intents are read-only)")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "Write an exploration [fact/conclusion] into the exploration graph, linked to the intent (intent_id) that produced it. Use it to record exploration results — including [positive conclusions] such as fingerprints/enumeration, and [negative conclusions] such as 'port closed' / 'parameter not injectable' / 'no login entry found'.\n"+
		"⚠️Multiple observations from one exploration should be [aggregated into one fact], not split into several — whatever can be merged into one fact should be expressed as one fact: summary = a one-sentence summary of this conclusion, detail = the related details (may contain several specific items). Example: a fingerprinting intent → one fact {summary:'identified the tech stack and response characteristics of site X', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}, rather than recording status code, fingerprint, and title each as a separate one. One intent usually yields only one fact; splitting too finely makes the graph grow without bound.\n"+
		"★The facts array is for writing several [mutually distinct] conclusions at once (each may omit intent_id, defaulting to the top-level intent_id). Returns an ids array, same length and order as facts.\n"+
		"⚠️Write only conclusions you [actually saw] in the tool output; do not make things up. evidence and confidence exist to keep inaccurate conclusions from polluting the graph:\n"+
		"  · evidence = the [one-line] key evidence supporting this conclusion (command + the one or two output lines that best prove it), **be concise** — details are already in detail, so do not paste large output here again.\n"+
		"  · confidence = observed (seen directly in the output) | inferred (deduced from symptoms).\n"+
		"  · closure_status = observation (default; cannot close the direction) | exhausted (planner may treat the direction as closed). Absolute negative prose never overrides this structured status.\n"+
		"  · Set negative=true for every rejection/inability/no-vulnerability conclusion. Negative conclusions default to closure_status=observation and become an explicit planner recovery signal. Use exhausted only with confidence=observed, concise evidence, at least two materially distinct tested_variants, contract_evidence establishing the expected input/request contract (or why none applies), no unresolved_leads, and a provenance_status accepted by the hard version gate. A few guessed payloads or identical 4xx/5xx responses cannot exhaust an endpoint.\n"+
		"  · provenance_status = not_applicable (requires a reason) | target_observed (requires runtime validation) | version_matched (requires target version evidence, matching source revision/tag, and runtime validation) | unversioned_external (can only remain an observation). Current/default-branch source is never version_matched by itself.",
		obj(map[string]any{
			"facts":                    map[string]any{"type": "array", "description": "[Use when there are several distinct conclusions] array of facts; each element has the same fields as the top-level fields below; omitting intent_id uses the top-level intent_id. Returned ids match this array in length and order.", "items": map[string]any{"type": "object"}},
			"summary":                  str("a [one-sentence summary] of this exploration's conclusion (a recap of detail)"),
			"intent_id":                idp("the intent id that produced this fact (the intent you were handed; used as the default for each entry in batch mode)"),
			"detail":                   str("the related details of this fact: write all the observations from this exploration here"),
			"evidence":                 str("the [one-line] key evidence: the command + the one or two output lines that best prove the conclusion. Be concise; do not paste large output (put details in detail)."),
			"confidence":               str("observed (seen directly in the output) | inferred (deduced from symptoms). Always label negative conclusions honestly."),
			"negative":                 map[string]any{"type": "boolean", "description": "true for any rejection, inability, dead-end, no-vulnerability, or other negative conclusion; provisional negatives trigger planner recovery"},
			"closure_status":           str("observation (default; does not close the direction) | exhausted (requires the proof fields below and no unresolved leads)"),
			"tested_variants":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "materially distinct request/input techniques actually tested; required with at least two entries for closure_status=exhausted"},
			"contract_evidence":        str("evidence establishing the expected request/input contract and preconditions, or why no contract applies; required for closure_status=exhausted"),
			"unresolved_leads":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "remaining concrete leads (client callers, bundles, encodings, methods, auth states, parameters); any entry forces closure_status=observation"},
			"provenance_status":        str("not_applicable | target_observed | version_matched | unversioned_external; required for closure_status=exhausted"),
			"provenance_note":          str("why version provenance is not applicable, or concise provenance context"),
			"target_version_evidence":  str("target-bound runtime/banner/package/hash evidence for the deployed version/revision; required for version_matched"),
			"source_revision_evidence": str("the exact matching tag/release/commit/snapshot and relevant file evidence; required for version_matched"),
			"runtime_validation":       str("request/response or other observation validating the conclusion against the deployed target; required for target_observed and version_matched"),
			"asset_ids":                map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "related asset ids (optional, 0/1/many): which assets this fact involves"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(a struct {
				Facts    []factItem `json:"facts"`
				factItem            // single mode + batch default intent_id
			}) (actool.Result, error) {
				items, batch := pickKnowledgeItems(a.Facts, a.factItem)
				defaultIntent := pid(a.factItem.IntentID) // top-level intent_id = batch default

				writtenBatch := writeKnowledgeItems(items, func(it factItem) (int64, error) { return t.recordOneFact(it, defaultIntent) }, nil)

				return writtenBatch.result(batch, "fact recorded")

			})
		})
}
