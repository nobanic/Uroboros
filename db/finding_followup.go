package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// findingFollowup owns copied input and the caller's audit value. Public output
// becomes authoritative only after commit; every error returns both zero values.
type findingFollowup struct {
	store                    *ExplorationStore
	findingID, findingNodeID int64
	description              string
	audit                    Activity
}
type findingFollowupResult struct {
	id    int64
	audit Activity
}

func (command findingFollowup) run() (int64, Activity, error) {
	value, err := transactIntent(command.store, func(tx *sql.Tx) (findingFollowupResult, error) {
		var err error
		var liveNodeID int64
		err = tx.QueryRow(`SELECT n.id
		FROM findings f
		JOIN tasks t ON t.id=f.task_id
		JOIN exploration_nodes n ON n.id=f.node_id AND n.exploration_id=t.exploration_id
		WHERE f.id=$1 AND f.node_id=$2 AND t.exploration_id=$3 AND n.kind='finding'
		FOR SHARE OF f, t, n`, command.findingID, command.findingNodeID, command.store.expID).Scan(&liveNodeID)
		if err == sql.ErrNoRows {
			return findingFollowupResult{}, ErrFindingOriginUnavailable
		}
		if err != nil {
			return findingFollowupResult{}, err
		}

		anchors := []int64{}
		anchorRows, err := tx.Query(`SELECT asset_id FROM exploration_anchors WHERE node_id=$1 ORDER BY asset_id`, liveNodeID)
		if err != nil {
			return findingFollowupResult{}, err
		}
		for anchorRows.Next() {
			var assetID int64
			if err := anchorRows.Scan(&assetID); err != nil {
				anchorRows.Close()
				return findingFollowupResult{}, err
			}
			anchors = append(anchors, assetID)
		}
		if err := anchorRows.Err(); err != nil {
			anchorRows.Close()
			return findingFollowupResult{}, err
		}
		if err := anchorRows.Close(); err != nil {
			return findingFollowupResult{}, err
		}

		payload := map[string]any{
			"summary":                command.description,
			"source_finding_id":      command.findingID,
			"source_finding_node_id": liveNodeID,
		}
		if len(anchors) > 0 {
			payload["asset_ids"] = anchors
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return findingFollowupResult{}, err
		}
		var intentID int64
		if err := tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
		VALUES ($1,'intent',$2,10,'open','human') RETURNING id`, command.store.expID, raw).Scan(&intentID); err != nil {
			return findingFollowupResult{}, err
		}
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id)
		SELECT $1, asset_id FROM exploration_anchors WHERE node_id=$2
		ON CONFLICT DO NOTHING`, intentID, liveNodeID); err != nil {
			return findingFollowupResult{}, err
		}
		if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)
		VALUES ($1,$2,$3,$4)`, command.store.expID, liveNodeID, RelDerivedFrom, intentID); err != nil {
			return findingFollowupResult{}, err
		}

		command.audit.NodeID = &intentID
		if summary := strings.TrimSpace(command.audit.Summary); summary != "" {
			command.audit.Summary = fmt.Sprintf("%s #%d", summary, intentID)
		} else {
			command.audit.Summary = ""
		}
		metadata := command.audit.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		if err := tx.QueryRow(`
INSERT INTO activity(exploration_id, node_id, worker, kind, tool, tool_use_id, is_error, summary, detail, metadata, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14)
RETURNING id, created_at`, command.store.expID, command.audit.NodeID, utf8Clean(command.audit.Worker), utf8Clean(command.audit.Kind), utf8Clean(command.audit.Tool), utf8Clean(command.audit.ToolUseID), command.audit.IsError,
			utf8Clean(command.audit.Summary), utf8Clean(command.audit.Detail), metadata, command.audit.InputTokens, command.audit.OutputTokens, command.audit.CacheReadTokens, command.audit.CacheWriteTokens).
			Scan(&command.audit.ID, &command.audit.CreatedAt); err != nil {
			return findingFollowupResult{}, err
		}
		command.audit.Metadata = metadata

		return findingFollowupResult{id: intentID, audit: command.audit}, nil
	})
	if err != nil {
		return 0, Activity{}, err
	}
	return value.id, value.audit, nil
}
