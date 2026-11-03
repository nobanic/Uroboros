package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

// assetBatch owns the ordered outcomes and gate snapshot of one tool call.
// Successful items commit independently; a later rejection cannot undo them.
// The ToolSet remains borrowed so existing store, owner and notify hooks keep
// their original lifetimes. Only the program-assigned task ID is captured.
type assetBatch struct {
	tools   *ToolSet
	taskID  int64
	blocked []db.AssetInterceptRule
	allowed []db.AssetInterceptRule
	results []assetBatchResult
	errors  []assetBatchError
}

type assetBatchResult struct {
	Index int    `json:"index"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

type assetBatchError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

func (t *ToolSet) acceptAssetBatch(_ context.Context, raw json.RawMessage) (actool.Result, error) {
	if t.as == nil {
		return actool.Errorf("insert_assets not enabled: AssetStore not initialized"), nil
	}
	var input struct {
		Assets []assetInputItem `json:"assets"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return actool.Errorf("invalid input: " + err.Error()), nil
	}
	batch := assetBatch{tools: t, taskID: t.taskID}
	batch.readGate()
	for index, item := range input.Assets {
		batch.accept(index, item)
	}
	return batch.finish()
}

func (b *assetBatch) readGate() {
	// Preserve the original best-effort reads and block-before-allow policy.
	// Rules are read even for an empty batch, once before any item is written.
	b.blocked, _ = b.tools.as.ListAssetInterceptRules()
	if b.tools.taskID <= 0 {
		return
	}
	blocks, allows, err := b.tools.as.TaskInterceptRulesSplit(b.tools.taskID)
	if err == nil {
		b.blocked = append(b.blocked, blocks...)
		b.allowed = allows
	}
}

func (b *assetBatch) accept(index int, item assetInputItem) {
	domains, ips, urls := assetInterceptCandidates(item)
	gate := db.EvaluateAssetGate(b.blocked, b.allowed, domains, ips, urls)
	if !gate.Allowed {
		b.reject(index, fmt.Errorf("asset %s %s; insertion blocked", assetInputLabel(item), gate.Reason))
		return
	}
	candidate := prepareAssetCandidate(item)
	id, err := candidate.persist(b.tools.as, b.taskID)
	if err != nil {
		b.reject(index, err)
		return
	}
	b.results = append(b.results, assetBatchResult{Index: index, ID: id, Type: candidate.kind})
	b.tools.writes.Assets++
	b.tools.anchorOwner(id)
	b.recordSource(id)
	// Only this explicit item can contribute domain authority. DB-created
	// parents/IPs remain evidence, and coverage settings do not suppress scope.
	_ = b.tools.as.AddAutoScopeForDiscoveredAsset(b.taskID, candidate.kind, item.Domain, item.URL, candidate.serviceIP)
}

func (b *assetBatch) reject(index int, err error) {
	b.errors = append(b.errors, assetBatchError{Index: index, Error: err.Error()})
}

func (b *assetBatch) recordSource(assetID int64) {
	if b.taskID <= 0 {
		return
	}
	var sourceNode *int64
	summary := "registered by the Agent via insert_assets"
	if b.tools.ownerNode > 0 {
		owner := b.tools.ownerNode
		sourceNode = &owner
		summary = fmt.Sprintf("registered by worker intent #%d via insert_assets", b.tools.ownerNode)
	}
	_ = b.tools.as.SetTaskAssetSource(b.taskID, assetID, "agent", summary, sourceNode)
}

func (b *assetBatch) finish() (actool.Result, error) {
	// One successful batch is one graph-change edge, including dedup updates.
	// The existing task callback debounces it and reconsiders discovery seeds.
	if len(b.results) != 0 && b.tools.notify != nil {
		b.tools.notify()
	}
	return jsonResult(map[string]any{"results": b.results, "errors": b.errors})
}
