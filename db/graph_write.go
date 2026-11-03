package db

import (
	"database/sql"
	"encoding/json"
)

type graphWrite struct{ store *ExplorationStore }

func (graph graphWrite) change(statement string, values ...any) recordMutation {
	return recordMutation{database: graph.store.db, statement: statement, values: values}
}
func (graph graphWrite) create(kind string, payload map[string]any, priority int, state, origin string, anchors []int64) (int64, error) {
	raw, _ := json.Marshal(payload)
	return (mutationTransaction{database: graph.store.db, retainIdentity: true}).run(func(tx *sql.Tx) (int64, error) {
		var id int64
		if err := tx.QueryRow(graphInsertSQL, graph.store.expID, kind, string(raw), priority, state, origin).Scan(&id); err != nil {
			return 0, err
		}
		for _, assetID := range anchors {
			if err := (recordMutation{database: tx, statement: graphAnchorSQL, values: []any{id, assetID}}).execute(); err != nil {
				return 0, err
			}
		}
		return id, nil
	})
}
func (graph graphWrite) anchor(node, asset int64) error {
	if node <= 0 || asset <= 0 {
		return nil
	}
	return graph.change(graphAnchorSQL, node, asset).execute()
}
func (graph graphWrite) link(from int64, relation string, to int64) error {
	return graph.change(graphLinkSQL, graph.store.expID, from, relation, to).execute()
}
func (graph graphWrite) state(id int64, state string) error {
	return graph.change(graphStateSQL, state, id, graph.store.expID).execute()
}
func (graph graphWrite) goal(id int64, text, vulnclass string, remove bool) error {
	if remove {
		return graph.change(graphDeleteGoalSQL, id, graph.store.expID).requireGoal()
	}
	payload := map[string]any{"text": text}
	if vulnclass != "" {
		payload["vulnclass"] = vulnclass
	}
	raw, _ := json.Marshal(payload)
	return graph.change(graphUpdateGoalSQL, string(raw), id, graph.store.expID).requireGoal()
}
func (graph graphWrite) claim(id int64, owner string) (bool, error) {
	count, err := graph.change(graphClaimSQL, owner, id, graph.store.expID).ignoredCount()
	return err == nil && count == 1, err
}

const graphInsertSQL = `
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`
const graphAnchorSQL = `INSERT INTO exploration_anchors(node_id, asset_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`
const graphLinkSQL = `
INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id) VALUES ($1,$2,$3,$4)
ON CONFLICT (exploration_id, src_id, rel, dst_id) DO NOTHING`
const graphStateSQL = `UPDATE exploration_nodes SET state=$1, blocked_reason=NULL, content_version=content_version+1 WHERE id=$2 AND exploration_id=$3`
const graphUpdateGoalSQL = `UPDATE exploration_nodes SET payload=$1 WHERE id=$2 AND exploration_id=$3 AND kind='goal'`
const graphDeleteGoalSQL = `DELETE FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='goal'`
const graphClaimSQL = `UPDATE exploration_nodes SET state='running', owner=$1
WHERE id=$2 AND exploration_id=$3 AND kind='intent' AND state='open'`
