package db

import "database/sql"

func (deletion *profileDeletion) lockReferences() error {
	for _, group := range []struct{ kind, query string }{
		{"task", `SELECT t.id
FROM tasks t
WHERE t.llm_profile_id=$1
   OR t.active_llm_profile_id=$1
   OR EXISTS (
       SELECT 1 FROM task_llm_profiles x
       WHERE x.task_id=t.id AND x.profile_id=$1
   )
ORDER BY t.id
FOR UPDATE OF t`},
		{"agent", `SELECT id FROM agents
WHERE llm_profile_id=$1
ORDER BY id
FOR UPDATE`},
		{"conversation", `SELECT id FROM conversations
WHERE llm_profile_id=$1
ORDER BY id
FOR UPDATE`},
	} {
		locked, err := lockProfileReferenceRows(deletion.ctx, deletion.tx, group.query, deletion.id)
		if err != nil {
			return err
		}
		deletion.locks[group.kind] = locked
	}
	return nil
}

func (deletion *profileDeletion) referencesChanged() (bool, error) {
	rows, err := deletion.tx.QueryContext(deletion.ctx, `SELECT ref_kind, ref_id FROM (
	SELECT 'task'::text AS ref_kind, t.id AS ref_id
FROM tasks t
WHERE t.llm_profile_id=$1
   OR t.active_llm_profile_id=$1
   OR EXISTS (
       SELECT 1 FROM task_llm_profiles x
       WHERE x.task_id=t.id AND x.profile_id=$1
   )
	UNION ALL
	SELECT 'agent', a.id FROM agents a WHERE a.llm_profile_id=$1
	UNION ALL
	SELECT 'conversation', c.id FROM conversations c WHERE c.llm_profile_id=$1
) refs
ORDER BY ref_kind, ref_id`, deletion.id)
	if err != nil {
		return false, err
	}
	return visitProfileDeletionRows(rows, func(row *sql.Rows) (bool, error) {
		var kind string
		var id int64
		if err := row.Scan(&kind, &id); err != nil {
			return false, err
		}
		_, locked := deletion.locks[kind][id]
		return !locked, nil
	})
}
