package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (command intentCommand) soft(reason string) (string, error) {
	return transactIntent(command.store, func(tx *sql.Tx) (string, error) {
		var state string
		var rawPayload []byte
		if err := tx.QueryRow(`SELECT state, payload FROM exploration_nodes
		WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, command.id, command.store.expID).Scan(&state, &rawPayload); err != nil {
			if err == sql.ErrNoRows {
				return "", fmt.Errorf("intent not found")
			}
			return "", err
		}
		if state != "running" && state != "paused" && state != "open" {
			return "", fmt.Errorf("%w: intent state %s cannot be deleted", ErrIntentStateConflict, state)
		}
		if _, err := tx.Exec(`UPDATE exploration_nodes
		SET state='deleted', delete_reason=$3, blocked_reason=NULL,
		    content_version=content_version+1, completed_at=now()
		WHERE id=$1 AND exploration_id=$2`, command.id, command.store.expID, reason); err != nil {
			return "", err
		}
		// The main intent's audit trail is kept, but the side Q&A session is atomically cleaned up with the deleted state (a deferred snapshot will reject it).
		if _, err := tx.Exec(`DELETE FROM side_question_sessions WHERE intent_id=$1`, command.id); err != nil {
			return "", err
		}
		var summary string
		var p map[string]any
		if json.Unmarshal(rawPayload, &p) == nil {
			if sm, ok := p["summary"].(string); ok {
				summary = sm
			}
		}
		return summary, nil
	})
}

func (command intentCommand) cancel() (IntentCleanup, error) {
	var out IntentCleanup
	return transactIntent(command.store, func(tx *sql.Tx) (IntentCleanup, error) {
		if err := tx.QueryRow(`SELECT 1 FROM exploration_nodes
		WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, command.id, command.store.expID).Scan(new(int)); err != nil {
			if err == sql.ErrNoRows {
				return out, fmt.Errorf("intent not found")
			}
			return out, err
		}

		// Load the whole graph's nodes (to decide protected) and edges (to compute downward reachability + parent set). The graph is small for real tasks.
		kind := map[int64]string{}
		protected := map[int64]bool{}
		nrows, err := tx.Query(`SELECT id, kind, state FROM exploration_nodes WHERE exploration_id=$1`, command.store.expID)
		if err != nil {
			return out, err
		}
		for nrows.Next() {
			var nid int64
			var k, st string
			if err := nrows.Scan(&nid, &k, &st); err != nil {
				nrows.Close()
				return out, err
			}
			kind[nid] = k
			if k == KindGoal || (k == KindFact && st == StateOrigin) {
				protected[nid] = true // goal and task-root facts are never deleted along with an intent
			}
		}
		nrows.Close()
		if err := nrows.Err(); err != nil {
			return out, err
		}

		parentsOf := map[int64][]int64{} // dst -> the src of every edge pointing to it (any rel, including covers: a member covered by a digest thus has a digest parent and is kept)
		downOf := map[int64][]int64{}    // src -> downward neighbors along yields/derived_from
		erows, err := tx.Query(`SELECT src_id, rel, dst_id FROM exploration_edges WHERE exploration_id=$1`, command.store.expID)
		if err != nil {
			return out, err
		}
		for erows.Next() {
			var src, dst int64
			var rel string
			if err := erows.Scan(&src, &rel, &dst); err != nil {
				erows.Close()
				return out, err
			}
			parentsOf[dst] = append(parentsOf[dst], src)
			if rel == RelYields || rel == RelDerivedFrom {
				downOf[src] = append(downOf[src], dst)
			}
		}
		erows.Close()
		if err := erows.Err(); err != nil {
			return out, err
		}

		// Exclusive cascade: expand downward from the intent; a node enters the deletion set iff it's unprotected and every one of its parents is already in the set
		// (i.e. there's no path to it except through deleted nodes). Iterate to a fixed point.
		del := map[int64]bool{command.id: true}
		for changed := true; changed; {
			changed = false
			for src := range del {
				for _, dst := range downOf[src] {
					if del[dst] || protected[dst] {
						continue
					}
					exclusive := true
					for _, p := range parentsOf[dst] {
						if !del[p] {
							exclusive = false
							break
						}
					}
					if exclusive {
						del[dst] = true
						changed = true
					}
				}
			}
		}

		// Bucket by type.
		var ids, intentIDs, findingIDs []int64
		for nid := range del {
			ids = append(ids, nid)
			switch kind[nid] {
			case KindIntent:
				intentIDs = append(intentIDs, nid)
				out.Intents++
			case KindFact:
				out.Facts++
			case KindFinding:
				findingIDs = append(findingIDs, nid)
				out.Findings++
			}
		}

		// For each deleted intent: do a token rollup (preserving irreversible accounting) then clear its activity. The rollup row has node_id NULL,
		// so it won't be hit by the node_id-based delete below.
		for _, iid := range intentIDs {
			tokenBuckets, err := intentTokenRollup(tx, command.store.expID, iid)
			if err != nil {
				return out, err
			}
			res, err := tx.Exec(`DELETE FROM activity WHERE exploration_id=$1 AND node_id=$2`, command.store.expID, iid)
			if err != nil {
				return out, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				out.Activities += n
			}
			for _, bucket := range tokenBuckets {
				metadata, _ := json.Marshal(map[string]any{
					"cancelled_intent_id": iid,
					"token_day":           bucket.Day.Format(time.DateOnly),
					"token_rollup":        true,
				})
				if _, err := tx.Exec(`INSERT INTO activity(
				exploration_id, worker, kind, summary, metadata,
				input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at)
				VALUES ($1,'token-ledger','result',$2,$3,$4,$5,$6,$7,$8)`,
					command.store.expID, fmt.Sprintf("cancelled token metering for intent #%d", iid), metadata,
					bucket.Usage.InputTokens, bucket.Usage.OutputTokens,
					bucket.Usage.CacheReadTokens, bucket.Usage.CacheWriteTokens, bucket.Day); err != nil {
					return out, err
				}
			}
			if _, err := tx.Exec(`DELETE FROM side_question_sessions WHERE intent_id=$1`, iid); err != nil {
				return out, err
			}
		}

		// Before deleting a finding node, delete its findings-table row first (otherwise a node_id=NULL orphan remains).
		for _, fid := range findingIDs {
			if _, err := tx.Exec(`DELETE FROM findings WHERE node_id=$1`, fid); err != nil {
				return out, err
			}
		}

		// Delete the nodes (edges are cleaned up via CASCADE). Delete one by one and verify row counts, to guard against concurrent changes.
		var removed int64
		for _, nid := range ids {
			res, err := tx.Exec(`DELETE FROM exploration_nodes WHERE id=$1 AND exploration_id=$2`, nid, command.store.expID)
			if err != nil {
				return out, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				removed += n
			}
		}
		if removed != int64(len(ids)) {
			return out, fmt.Errorf("intent cleanup changed concurrently")
		}
		return out, nil
	})
}
