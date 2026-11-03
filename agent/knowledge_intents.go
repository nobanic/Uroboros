package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/internal/toolinput"
	actool "github.com/nobanic/norma/tool"
	"regexp"
	"sort"
	"strings"
)

// intentItem is one exploration direction in a batch/single add_intent.
type intentItem struct {
	Summary             string            `json:"summary"`
	HypothesisKey       string            `json:"hypothesis_key"`
	AttackRole          string            `json:"attack_role"`
	StopCondition       string            `json:"stop_condition"`
	AssetIDs            []json.RawMessage `json:"asset_ids"`
	ParentIDs           []json.RawMessage `json:"parent_ids"`
	Priority            int               `json:"priority"`
	AvoidHypothesisKeys []string          `json:"-"`
	AvoidAttackRoles    []string          `json:"-"`
}

var nonHypothesisKey = regexp.MustCompile(`[^a-z0-9._:-]+`)

var allowedAttackRoles = map[string]struct{}{
	"surface_mapper":          {},
	"client_contract_analyst": {},
	"version_researcher":      {},
	"auth_identity_tester":    {},
	"access_control_tester":   {},
	"injection_tester":        {},
	"ssrf_egress_tester":      {},
	"file_path_tester":        {},
	"state_logic_tester":      {},
	"exploit_builder":         {},
	"exploit_validator":       {},
}

func normalizeHypothesisKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonHypothesisKey.ReplaceAllString(value, "-")
	return strings.Trim(value, "-._:")
}

func normalizeAttackRole(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

// prepareIntentBatch validates the planner's structured diversity contract before
// any node is written. Sibling keys are also handed to every worker as negative
// guidance, which makes the partition visible at execution time rather than only
// during planning.
func prepareIntentBatch(items []intentItem, batch bool) ([]intentItem, error) {
	seen := make(map[string]struct{}, len(items))
	seenRoles := make(map[string]struct{}, len(items))
	for i := range items {
		items[i].HypothesisKey = normalizeHypothesisKey(items[i].HypothesisKey)
		if len(items[i].HypothesisKey) > 64 {
			return nil, fmt.Errorf("intent %d hypothesis_key must be at most 64 characters", i)
		}
		if batch && len(items) > 1 && items[i].HypothesisKey == "" {
			return nil, fmt.Errorf("intent %d hypothesis_key is required when batching multiple directions", i)
		}
		items[i].StopCondition = strings.TrimSpace(items[i].StopCondition)
		if len(items[i].StopCondition) > 600 {
			return nil, fmt.Errorf("intent %d stop_condition must be at most 600 characters", i)
		}
		if batch && len(items) > 1 && items[i].StopCondition == "" {
			return nil, fmt.Errorf("intent %d stop_condition is required when batching multiple directions", i)
		}
		if items[i].HypothesisKey == "" {
			// A single legacy intent may omit both diversity fields. Multi-intent
			// batches are checked below before reaching this branch.
		} else {
			if _, duplicate := seen[items[i].HypothesisKey]; duplicate {
				return nil, fmt.Errorf("duplicate hypothesis_key %q in this batch", items[i].HypothesisKey)
			}
			seen[items[i].HypothesisKey] = struct{}{}
		}
		items[i].AttackRole = normalizeAttackRole(items[i].AttackRole)
		if batch && len(items) > 1 && items[i].AttackRole == "" {
			return nil, fmt.Errorf("intent %d attack_role is required when batching multiple directions", i)
		}
		if items[i].AttackRole != "" {
			if _, allowed := allowedAttackRoles[items[i].AttackRole]; !allowed {
				return nil, fmt.Errorf("intent %d attack_role %q is not one of the supported role lanes", i, items[i].AttackRole)
			}
			if _, duplicate := seenRoles[items[i].AttackRole]; duplicate {
				return nil, fmt.Errorf("duplicate attack_role %q in this batch; concurrent workers need distinct role lanes", items[i].AttackRole)
			}
			seenRoles[items[i].AttackRole] = struct{}{}
		}
	}
	for i := range items {
		for key := range seen {
			if key != items[i].HypothesisKey {
				items[i].AvoidHypothesisKeys = append(items[i].AvoidHypothesisKeys, key)
			}
		}
		sort.Strings(items[i].AvoidHypothesisKeys)
		for role := range seenRoles {
			if role != items[i].AttackRole {
				items[i].AvoidAttackRoles = append(items[i].AvoidAttackRoles, role)
			}
		}
		sort.Strings(items[i].AvoidAttackRoles)
	}
	return items, nil
}

// addOneIntent creates one intent node, links its upstream lineage, and returns its id.
// Constraint: an intent can only be anchored on confirmed knowledge — each parent_id must
// be an existing fact/finding node (it cannot hang off another intent/goal/hint). Leave
// parent_ids empty for a brand-new top-level direction, which falls back to the origin fact.
// This enforces "every intent connects to a fact node and is finding-driven, not planned
// out of thin air" at the creation path.
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary must not be empty")
	}
	it.HypothesisKey = normalizeHypothesisKey(it.HypothesisKey)
	if it.HypothesisKey != "" {
		active, err := t.ts.ListByKind(db.KindIntent, 1000)
		if err != nil {
			return 0, fmt.Errorf("check active hypothesis keys: %w", err)
		}
		for _, existing := range active {
			if existing.State != "open" && existing.State != "running" && existing.State != "paused" {
				continue
			}
			var payload map[string]any
			if json.Unmarshal(existing.Payload, &payload) != nil {
				continue
			}
			key, _ := payload["hypothesis_key"].(string)
			if normalizeHypothesisKey(key) == it.HypothesisKey {
				return 0, fmt.Errorf("hypothesis_key %q is already active in intent %d (%s)", it.HypothesisKey, existing.ID, existing.State)
			}
		}
	}
	// Validate the anchors first (before creating the node, so a bad anchor doesn't leave an orphan intent).
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d does not exist in this task or a direct related task: parent_ids must be existing [fact/finding] node ids; leave parent_ids empty for a brand-new top-level direction", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, fmt.Errorf("parent_id %d is a %q node and cannot be an intent anchor: an intent can only be anchored on a confirmed [fact/finding], not on an intent/goal/hint; leave parent_ids empty for a brand-new top-level direction", pidv, n.Kind)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	// Asset interception: if an asset bound to the intent hits a system asset-interception rule, block dispatching this intent.
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, fmt.Errorf("asset-interception check failed: %w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "the assets bound to intent %q did not pass the test-scope check; stop testing the assets involved:", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if it.HypothesisKey != "" {
		payload["hypothesis_key"] = it.HypothesisKey
	}
	if stop := strings.TrimSpace(it.StopCondition); stop != "" {
		payload["stop_condition"] = stop
	}
	if it.AttackRole != "" {
		payload["attack_role"] = it.AttackRole
	}
	if len(it.AvoidHypothesisKeys) > 0 {
		payload["avoid_hypothesis_keys"] = it.AvoidHypothesisKeys
	}
	if len(it.AvoidAttackRoles) > 0 {
		payload["avoid_attack_roles"] = it.AvoidAttackRoles
	}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, err := t.ts.AddIntent(payload, priority, anchors, "planner")
	if err != nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	intentFields := map[string]any{
		"summary":        str("one sentence describing what to test and why"),
		"hypothesis_key": str("stable mechanism/entry-surface key (lowercase slug); required and unique within a multi-intent batch"),
		"attack_role":    str("distinct execution lane for this worker: surface_mapper | client_contract_analyst | version_researcher | auth_identity_tester | access_control_tester | injection_tester | ssrf_egress_tester | file_path_tester | state_logic_tester | exploit_builder | exploit_validator; required and unique within a multi-intent batch"),
		"stop_condition": str("observable condition that ends this direction, including the failure pattern that should trigger a pivot"),
		"asset_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		"parent_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		"priority":       intp("priority 0-10, default 5"),
	}
	intentArraySchema := map[string]any{
		"type":        "array",
		"description": "preferred batch form; for 2+ directions every item needs a distinct hypothesis_key representing a materially different mechanism or entry surface",
		"items":       map[string]any{"type": "object", "properties": intentFields, "required": []string{"summary", "hypothesis_key", "attack_role", "stop_condition"}},
	}
	return t.writeExpTool("add_intent", "Generate an [exploration direction], write it into the frontier, and link it into the exploration graph. An intent is an open exploration direction, not a fixed type — freely describe in one sentence (summary) what to explore/verify/exploit.\n"+
		"★Prefer batching: put the several new directions filtered out in one round into the intents array and submit at once. Every direction in a multi-intent batch must carry a unique hypothesis_key, a distinct supported attack_role, and an observable stop_condition. The role lanes prevent nominally different workers from repeating the same research. Returns an ids array of the same length and order as intents. For a single one, omit intents and give the top-level fields directly.",
		obj(map[string]any{
			"intents":        intentArraySchema,
			"summary":        str("[single] one sentence describing this exploration direction: what to do + why. As long as the direction is clear it's fine; it does not depend on asset ids."),
			"hypothesis_key": str("[single] stable mechanism/entry-surface key used to prevent an active duplicate"),
			"attack_role":    str("[single] optional execution lane; supported values are listed in the batch schema"),
			"stop_condition": str("[single] observable condition that ends this direction and forces a pivot"),
			"asset_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "the [target asset ids] this direction will test/attack (**pass them whenever possible**, 0/1/many; these are asset ids returned by list_assets, not exploration-node ids): which assets (sites/endpoints/parameters/hosts, etc.) this exploration direction targets. Whenever the direction revolves around specific assets, pass them — it is the structured marker for 'which targets this exploration hits', used for coverage dedup and to link the intent into the asset graph. Leave empty only for pure global recon with genuinely no specific target asset."},
			"parent_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "upstream anchor ids (optional, 0/1/many): which [confirmed facts/findings] this direction is synthesized from. **Only existing fact/finding node ids are allowed, not intents/goals/hints** — an intent must be anchored on confirmed knowledge, finding-driven rather than planned out of thin air. Pass several when multiple facts jointly produce one new intent; leave empty for a brand-new top-level recon direction (it is automatically attached to the task's origin fact)."},
			"priority":       intp("priority 0-10, default 5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // single mode: top-level summary/asset_ids/parent_ids/priority
			}) (actool.Result, error) {
				items, batch := pickKnowledgeItems(a.Intents, a.intentItem)
				var prepErr error
				items, prepErr = prepareIntentBatch(items, batch)
				if prepErr != nil {
					return actool.Errorf(prepErr.Error()), nil
				}

				createdAny := false
				writtenBatch := writeKnowledgeItems(items, t.addOneIntent, func(it intentItem) { createdAny = true })

				// A human injecting an intent directly through the main agent → if the task is
				// already done (the goalless branch with no open goals), pull it back to running
				// so a worker can claim and execute this intent. resumeTask is wired only by the
				// main agent's Chat (SetResumeTask); the planner's ToolSet is nil, so this is a
				// no-op when the planner itself calls add_intent and does not affect its normal
				// intent production. The intent node was already created (open) above, so on
				// revival it won't be mistakenly judged as drained.
				if createdAny && t.resumeTask != nil {
					t.resumeTask()
				}

				return writtenBatch.result(batch, "intent created")

			})
		})
}
