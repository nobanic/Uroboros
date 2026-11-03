package db

import (
	"errors"
	"fmt"

	"strings"
	"unicode/utf8"
)

const (
	MaxTaskAssetMutationCount = 100
	MaxTaskAssetSummaryRunes  = 500
	defaultTaskAssetSource    = "system"
	manualTaskScopeSummary    = "manually added on the test-assets page"
)

var (
	ErrTaskAssetInvalid       = errors.New("invalid task asset association")
	ErrTaskAssetTaskNotFound  = errors.New("task not found")
	ErrTaskAssetAssetNotFound = errors.New("asset not found")
)

// TaskAssetMutation summarizes one attach request. Attached counts newly added
// associations; Existing counts requested assets that were already on the task.
type TaskAssetMutation struct {
	Requested int `json:"requested"`
	Attached  int `json:"attached"`
	Existing  int `json:"existing"`
}

// TaskAssetScopeMutation summarizes one free-form scope registration. Domain
// and IP entries create or reuse global assets; every entry also becomes an
// idempotent task_scope row.
type TaskAssetScopeMutation struct {
	Requested      int `json:"requested"`
	AssetsLinked   int `json:"assets_linked"`
	AssetsExisting int `json:"assets_existing"`
	ScopesAdded    int `json:"scopes_added"`
	ScopesExisting int `json:"scopes_existing"`
}

// IntentAsset describes an asset explicitly anchored to a worker intent.
type IntentAsset struct {
	IntentID      int64  `json:"intent_id"`
	AssetID       int64  `json:"asset_id"`
	Type          string `json:"type"`
	Label         string `json:"label"`
	Source        string `json:"source"`
	SourceSummary string `json:"source_summary"`
	SourceNodeID  *int64 `json:"source_node_id,omitempty"`
	SourceTaskID  int64  `json:"source_task_id"`
	Inherited     bool   `json:"inherited"`
}

func normalizeTaskAssetIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: asset_ids is required", ErrTaskAssetInvalid)
	}
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: asset id must be positive", ErrTaskAssetInvalid)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
		if len(normalized) > MaxTaskAssetMutationCount {
			return nil, fmt.Errorf("%w: at most %d assets per request", ErrTaskAssetInvalid, MaxTaskAssetMutationCount)
		}
	}
	return normalized, nil
}

func normalizeTaskAssetSource(source, summary string) (string, string, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		source = defaultTaskAssetSource
	}
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > MaxTaskAssetSummaryRunes {
		return "", "", fmt.Errorf("%w: source summary exceeds %d characters", ErrTaskAssetInvalid, MaxTaskAssetSummaryRunes)
	}
	return source, summary, nil
}

// SetTaskAssetSource improves the generic trigger-created provenance for one
// existing task association. It never creates or deletes an asset.
func (s *AssetStore) SetTaskAssetSource(taskID, assetID int64, source, summary string, sourceNodeID *int64) error {
	return (taskAssetProvenance{store: s, taskID: taskID}).set(assetID, source, summary, sourceNodeID)
}

// RegisterTaskAssetScopes accepts the same structured scope rules as enterprise
// assets. The entire request is atomic: invalid input or any storage failure
// leaves both global assets and task scope unchanged.
func (s *AssetStore) RegisterTaskAssetScopes(taskID int64, inputs []ScopeInput) (TaskAssetScopeMutation, error) {
	return (&taskAssetScopeRegistration{store: s, taskID: taskID}).apply(inputs)
}

// AttachAssetsToTask associates existing global assets with one live task and
// records an operator-authored source summary. Global asset rows are retained.
func (s *AssetStore) AttachAssetsToTask(taskID int64, assetIDs []int64, sourceSummary string) (TaskAssetMutation, error) {
	return (taskAssetMembership{store: s, taskID: taskID}).attach(assetIDs, sourceSummary)
}

// DetachAssetFromTask removes only the task association. The global asset and
// exploration anchors remain available for historical blackboard auditing.
func (s *AssetStore) DetachAssetFromTask(taskID, assetID int64) (bool, error) {
	return (taskAssetMembership{store: s, taskID: taskID}).detach(assetID)
}

func (s *AssetStore) hydrateTaskAssetSources(taskID int64, assets []*Asset) error {
	return (taskAssetProvenance{store: s, taskID: taskID}).hydrate(assets)
}

// IntentAssets returns all local worker targets plus immutable targets from the
// task's direct sources. Inherited non-terminal intents remain hidden, matching
// the existing source-aware session contract.
func (s *AssetStore) IntentAssets(taskID int64) ([]IntentAsset, error) {
	return (taskAssetProvenance{store: s, taskID: taskID}).intents()
}
