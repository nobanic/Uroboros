package db

// intentCommand owns one local intent ID; population operations retain the
// same exploration scope and original per-statement transition policies.
type intentCommand struct {
	store *ExplorationStore
	id    int64
}
type intentPopulation struct{ store *ExplorationStore }

func (command intentPopulation) reset() (int64, error) {
	res, err := command.store.db.Exec(`UPDATE exploration_nodes SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE exploration_id=$1 AND kind='intent' AND state='running'`, command.store.expID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (command intentCommand) reopen() (bool, error) {
	res, err := command.store.db.Exec(`UPDATE exploration_nodes
SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE id=$1 AND exploration_id=$2 AND kind='intent'
  AND state IN ('blocked','exhausted','stopped')`, command.id, command.store.expID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (command intentPopulation) reopenBlocked() (int64, error) {
	res, err := command.store.db.Exec(`UPDATE exploration_nodes SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE exploration_id=$1 AND kind='intent' AND state='blocked'`, command.store.expID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (command intentCommand) set(state string) error {
	// terminal states stamp completed_at; reopening (back to open/running) clears it.
	terminal := state == "done" || state == "blocked" || state == "exhausted" || state == "stopped"
	_, err := command.store.db.Exec(`UPDATE exploration_nodes
SET state=$1, blocked_reason=NULL, content_version=content_version+1, completed_at = CASE WHEN $4 THEN now() ELSE NULL END
WHERE id=$2 AND exploration_id=$3 AND kind='intent'`, state, command.id, command.store.expID, terminal)
	return err
}

func (command intentCommand) compare(expected, state string) (bool, error) {
	terminal := state == "done" || state == "blocked" || state == "exhausted" || state == "stopped"
	res, err := command.store.db.Exec(`UPDATE exploration_nodes
SET state=$1, blocked_reason=NULL, content_version=content_version+1, completed_at = CASE WHEN $5 THEN now() ELSE NULL END
WHERE id=$2 AND exploration_id=$3 AND kind='intent' AND state=$4`, state, command.id, command.store.expID, expected, terminal)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
