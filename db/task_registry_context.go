package db

import (
	"database/sql"
	"fmt"
)

// These context writes borrow the caller's transaction; they never commit it.
type taskRegistryContext struct {
	transaction *sql.Tx
	taskID      int64
}

func (context taskRegistryContext) companies(companyIDs []int64) error {
	if len(companyIDs) == 0 {
		return nil
	}
	// Company scope edits rebuild assets.company_id. Serialize the creation-time
	// snapshot with those edits so the task sees one committed attribution state.
	if err := lockCompanyScopeMutation(context.transaction); err != nil {
		return err
	}
	var inserted int
	err := context.transaction.QueryRow(`
WITH requested(company_id, position) AS (
    SELECT company_id, position
    FROM unnest($2::bigint[]) WITH ORDINALITY AS requested(company_id, position)
), inserted AS (
    INSERT INTO task_scope(task_id, kind, company_id, source, reason)
    SELECT $1, 'company', companies.id, 'manual', 'associated company at task creation'
    FROM requested
    JOIN companies ON companies.id=requested.company_id
    ORDER BY requested.position
    RETURNING company_id
)
SELECT count(*) FROM inserted`, context.taskID, companyIDs).Scan(&inserted)
	if err != nil {
		return err
	}
	if inserted != len(companyIDs) {
		return fmt.Errorf("%w: one or more companies do not exist", ErrTaskCompanyNotFound)
	}

	// Updating task_ids fires sync_task_asset_links, which first creates generic
	// source rows. The provenance upsert must therefore run afterwards so the
	// company name and creation-time reason remain visible to operators.
	if _, err := context.transaction.Exec(`
UPDATE assets
SET task_ids=CASE
    WHEN $1=ANY(task_ids) THEN task_ids
    ELSE array_append(task_ids, $1)
END
WHERE company_id=ANY($2::bigint[])`, context.taskID, companyIDs); err != nil {
		return err
	}
	if _, err := context.transaction.Exec(`
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT $1, asset.id, $3, 'associated company at task creation: ' || company.name
FROM assets asset
JOIN companies company ON company.id=asset.company_id
WHERE asset.company_id=ANY($2::bigint[])
  AND $1=ANY(asset.task_ids)
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source=EXCLUDED.source,
    source_summary=EXCLUDED.source_summary,
    source_node_id=NULL`, context.taskID, companyIDs, taskCompanyAssetSource); err != nil {
		return err
	}
	return nil
}

func (context taskRegistryContext) sources(sourceIDs []int64) error {
	seen := map[int64]bool{}
	for _, sourceID := range sourceIDs {
		if sourceID <= 0 || sourceID == context.taskID || seen[sourceID] {
			return fmt.Errorf("invalid or duplicate source task id %d", sourceID)
		}
		seen[sourceID] = true
		res, err := context.transaction.Exec(`INSERT INTO task_relations(task_id, source_task_id)
SELECT $1, id FROM tasks WHERE id=$2 AND deleted_at IS NULL`, context.taskID, sourceID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("source task %d not found", sourceID)
		}
	}
	return nil
}

func (context taskRegistryContext) profiles(profileIDs []int64) error {
	seen := map[int64]bool{}
	for position, profileID := range profileIDs {
		if profileID <= 0 || seen[profileID] {
			return fmt.Errorf("invalid or duplicate LLM profile id %d", profileID)
		}
		seen[profileID] = true
		if _, err := context.transaction.Exec(`INSERT INTO task_llm_profiles(task_id, profile_id, position) VALUES ($1,$2,$3)`, context.taskID, profileID, position); err != nil {
			return err
		}
	}
	return nil
}
