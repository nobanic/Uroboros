package db

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Old archives omit these tables. Retest conversations are not task-owned
// archive objects: keep their historical evidence but clear conversation handles.
func restoreFindingExperimentsTx(tx *sql.Tx, snapshot *TaskArchiveSnapshot) error {
	for _, table := range []string{"finding_experiments", "finding_retests", "finding_experiment_replays"} {
		rows, err := decodeArchiveRows(snapshot.Tables[table])
		if err != nil {
			return err
		}
		for _, row := range rows {
			var findingID int64
			if table == "finding_experiment_replays" {
				rid, ok := jsonInt64(row["retest_id"])
				if !ok {
					return errors.New("invalid archived retest id")
				}
				eid, ok := jsonInt64(row["experiment_id"])
				if !ok {
					return errors.New("invalid archived experiment id")
				}
				if err := tx.QueryRow(`SELECT r.finding_id FROM finding_retests r JOIN finding_experiments e ON e.finding_id=r.finding_id WHERE r.id=$1 AND e.id=$2`, rid, eid).Scan(&findingID); err != nil {
					return err
				}
			} else {
				var ok bool
				findingID, ok = jsonInt64(row["finding_id"])
				if !ok {
					return errors.New("invalid archived finding id")
				}
			}
			var owned bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM findings WHERE id=$1 AND task_id=$2)`, findingID, snapshot.TaskID).Scan(&owned); err != nil {
				return err
			}
			if !owned {
				return errors.New("experiment archive references a finding outside the task")
			}
			if table == "finding_experiments" || table == "finding_experiment_replays" {
				text, _ := row["snapshot"].(string)
				hash, _ := row["hash"].(string)
				if table == "finding_experiments" || hash != "" {
					if _, err := ValidateExperimentBundle(text, hash); err != nil {
						return err
					}
				}
			}
			if table == "finding_retests" {
				row["conversation_id"] = nil
				if row["status"] == "pending" || row["status"] == "running" {
					row["status"] = "stopped"
					row["error"] = "task was archived; start a new retest"
				}
			}
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		if err = insertArchiveRows(tx, table, raw); err != nil {
			return err
		}
	}
	return nil
}
