package db

import "database/sql"

// sourceRelations resolves live direct sources on each read. It never caches or
// expands a source's own relations and grants no write/frontier/claim capability.
type sourceRelations struct{ store *ExplorationStore }

func (relation sourceRelations) sources() ([]DirectSourceStore, error) {
	return collectQueryState(queriedRows(relation.store.db.Query(`
SELECT source.id, source.exploration_id, source.description, source.goal, source.status
FROM tasks owner
JOIN task_relations relation ON relation.task_id=owner.id
JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
WHERE owner.exploration_id=$1 AND owner.deleted_at IS NULL
ORDER BY relation.created_at, source.id`, relation.store.expID)), func() []DirectSourceStore {
		return []DirectSourceStore{}
	}, func(queryRow rowDecoder, queryState *[]DirectSourceStore) error {
		var source TaskSource
		if err := queryRow.Scan(&source.TaskID, &source.ExplorationID, &source.Description, &source.Goal, &source.Status); err != nil {
			return err
		}
		*queryState = append(*queryState, DirectSourceStore{Task: source, Store: relation.store.db.Exploration(source.ExplorationID)})
		return nil
	})
}
func (relation sourceRelations) task() (int64, error) {
	var id int64
	err := relation.store.db.QueryRow(`SELECT id FROM tasks WHERE exploration_id=$1 AND deleted_at IS NULL`, relation.store.expID).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}
