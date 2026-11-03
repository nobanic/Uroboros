package db

// ExplorationToolCall is the persisted input side of one tool invocation. It is
// intentionally small: policy layers use it to recognize task-wide repeated
// work without loading tool results or model reasoning into memory.
type ExplorationToolCall struct {
	NodeID    *int64
	Worker    string
	ToolUseID string
	Input     string
}

// ExplorationToolCalls returns the newest model-visible inputs for successful
// calls of one tool in an exploration. A rejected/failed policy call therefore
// never spends the useful-work budget again. excludeToolUseID also lets a wrapper
// ignore the call currently being executed: capture persists tool_use
// synchronously before CoreTool.Call and its result afterward.
func (d *DB) ExplorationToolCalls(explorationID int64, tool, excludeToolUseID string, limit int) ([]ExplorationToolCall, error) {
	if explorationID <= 0 || tool == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	rows, err := d.Query(`
SELECT node_id, COALESCE(worker,''), COALESCE(tool_use_id,''), COALESCE(detail,'')
FROM activity
WHERE exploration_id=$1 AND kind='tool_use' AND tool=$2
  AND ($3='' OR COALESCE(tool_use_id,'')<>$3)
  AND EXISTS (
    SELECT 1 FROM activity result
    WHERE result.exploration_id=activity.exploration_id
      AND result.tool_use_id=activity.tool_use_id
      AND result.kind='tool_result' AND result.is_error=false
  )
ORDER BY id DESC
LIMIT $4`, explorationID, tool, excludeToolUseID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExplorationToolCall, 0)
	return collectRowValues(rows, out, func() (ExplorationToolCall, error) {
		var call ExplorationToolCall
		err := rows.Scan(&call.NodeID, &call.Worker, &call.ToolUseID, &call.Input)
		return call, err
	})
}
