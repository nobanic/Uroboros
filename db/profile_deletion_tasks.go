package db

import "database/sql"

type profileDeletionTask struct {
	id        int64
	position  int
	wasActive bool
}

func (deletion *profileDeletion) affectedTasks() ([]profileDeletionTask, error) {
	rows, err := deletion.tx.QueryContext(deletion.ctx, `SELECT x.task_id, x.position, COALESCE(t.active_llm_profile_id=$1, false)
FROM task_llm_profiles x
JOIN tasks t ON t.id=x.task_id
WHERE x.profile_id=$1
ORDER BY x.task_id`, deletion.id)
	if err != nil {
		return nil, err
	}
	var tasks []profileDeletionTask
	_, err = visitProfileDeletionRows(rows, func(row *sql.Rows) (bool, error) {
		var task profileDeletionTask
		if err := row.Scan(&task.id, &task.position, &task.wasActive); err != nil {
			return false, err
		}
		tasks = append(tasks, task)
		return false, nil
	})
	return tasks, err
}

func (deletion *profileDeletion) advance(task profileDeletionTask) error {
	if !task.wasActive {
		return deletion.exec(`UPDATE tasks SET llm_chain_revision=llm_chain_revision+1 WHERE id=$1`, task.id)
	}
	var successor int64
	err := deletion.tx.QueryRowContext(deletion.ctx, `SELECT profile_id FROM task_llm_profiles
WHERE task_id=$1 AND position>$2 AND status='ready'
ORDER BY position
LIMIT 1`, task.id, task.position).Scan(&successor)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		if err := deletion.exec(`DELETE FROM task_llm_profiles WHERE task_id=$1`, task.id); err != nil {
			return err
		}
		return deletion.exec(`UPDATE tasks
SET active_llm_profile_id=NULL, llm_profile_id=NULL, llm_chain_revision=llm_chain_revision+1
WHERE id=$1`, task.id)
	}
	return deletion.exec(`UPDATE tasks
SET active_llm_profile_id=$2, llm_profile_id=$2, llm_chain_revision=llm_chain_revision+1
WHERE id=$1`, task.id, successor)
}
