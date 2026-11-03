package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"

	"github.com/nobanic/UroborOS/db"
)

const efficiencyShadowMetadataKey = "efficiency_shadow"

// resultShadowRegistry observes exact tool inputs/results within one task. It is
// deliberately not consulted by the scheduler, agents, or tools: the only
// externally visible effect is additional activity metadata for offline analysis.
type resultShadowRegistry struct {
	mu sync.Mutex

	pendingSources map[string]shadowSource
	sourceCounts   map[string]int
	resultCounts   map[string]int
	pairCounts     map[string]int
	firstResults   map[string]shadowFirst
}

type shadowSource struct {
	hash       string
	bytes      int
	occurrence int
}

type shadowFirst struct {
	worker    string
	toolUseID string
	nodeID    int64
}

func newResultShadowRegistry() *resultShadowRegistry {
	return &resultShadowRegistry{
		pendingSources: make(map[string]shadowSource),
		sourceCounts:   make(map[string]int),
		resultCounts:   make(map[string]int),
		pairCounts:     make(map[string]int),
		firstResults:   make(map[string]shadowFirst),
	}
}

func (e *Engine) resultShadowFor(taskID string) *resultShadowRegistry {
	if value, ok := e.resultShadow.Load(taskID); ok {
		return value.(*resultShadowRegistry)
	}
	candidate := newResultShadowRegistry()
	value, _ := e.resultShadow.LoadOrStore(taskID, candidate)
	return value.(*resultShadowRegistry)
}

func shadowDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func shadowCallKey(activity db.Activity) string {
	return activity.Worker + "\x00" + activity.ToolUseID
}

func shadowNodeID(nodeID *int64) int64 {
	if nodeID == nil {
		return 0
	}
	return *nodeID
}

func (registry *resultShadowRegistry) observe(activity db.Activity) db.Activity {
	if registry == nil || (activity.Kind != "tool_use" && activity.Kind != "tool_result") {
		return activity
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()

	observation := map[string]any{
		"mode": "observe_only", "scope": "task",
		"hash_basis": "persisted_activity_detail",
	}
	callKey := shadowCallKey(activity)

	if activity.Kind == "tool_use" {
		hash := shadowDigest(activity.Detail)
		key := activity.Tool + "\x00" + hash
		registry.sourceCounts[key]++
		source := shadowSource{
			hash:       hash,
			bytes:      len([]byte(activity.Detail)),
			occurrence: registry.sourceCounts[key],
		}
		registry.pendingSources[callKey] = source
		observation["source_sha256"] = source.hash
		observation["source_bytes"] = source.bytes
		observation["source_occurrence"] = source.occurrence
		observation["duplicate_source"] = source.occurrence > 1
		return withEfficiencyShadowMetadata(activity, observation)
	}

	resultHash := shadowDigest(activity.Detail)
	resultKey := activity.Tool + "\x00" + strconv.FormatBool(activity.IsError) + "\x00" + resultHash
	registry.resultCounts[resultKey]++
	resultOccurrence := registry.resultCounts[resultKey]
	first := registry.firstResults[resultKey]
	if resultOccurrence == 1 {
		first = shadowFirst{
			worker:    activity.Worker,
			toolUseID: activity.ToolUseID,
			nodeID:    shadowNodeID(activity.NodeID),
		}
		registry.firstResults[resultKey] = first
	}

	observation["result_sha256"] = resultHash
	observation["result_bytes"] = len([]byte(activity.Detail))
	observation["result_occurrence"] = resultOccurrence
	observation["duplicate_result"] = resultOccurrence > 1
	observation["first_result_worker"] = first.worker
	observation["first_result_tool_use_id"] = first.toolUseID
	if first.nodeID > 0 {
		observation["first_result_node_id"] = first.nodeID
	}

	if source, ok := registry.pendingSources[callKey]; ok {
		delete(registry.pendingSources, callKey)
		observation["source_sha256"] = source.hash
		observation["source_bytes"] = source.bytes
		observation["source_occurrence"] = source.occurrence
		observation["duplicate_source"] = source.occurrence > 1

		pairKey := activity.Tool + "\x00" + source.hash + "\x00" + strconv.FormatBool(activity.IsError) + "\x00" + resultHash
		registry.pairCounts[pairKey]++
		observation["exact_pair_occurrence"] = registry.pairCounts[pairKey]
		observation["duplicate_exact_pair"] = registry.pairCounts[pairKey] > 1
	}

	return withEfficiencyShadowMetadata(activity, observation)
}

// withEfficiencyShadowMetadata preserves existing metadata entries and every
// model-visible Activity field. Invalid/non-object metadata is left untouched so
// observation can never corrupt an activity produced by another subsystem.
func withEfficiencyShadowMetadata(activity db.Activity, observation map[string]any) db.Activity {
	metadata := make(map[string]json.RawMessage)
	if len(activity.Metadata) > 0 {
		if err := json.Unmarshal(activity.Metadata, &metadata); err != nil {
			return activity
		}
		if metadata == nil { // valid JSON "null" is equivalent to no metadata
			metadata = make(map[string]json.RawMessage)
		}
	}
	encodedObservation, err := json.Marshal(observation)
	if err != nil {
		return activity
	}
	metadata[efficiencyShadowMetadataKey] = encodedObservation
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return activity
	}
	activity.Metadata = encodedMetadata
	return activity
}
