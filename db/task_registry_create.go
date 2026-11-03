package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// One creation owns the copied options and the transaction through commit.
type taskRegistryCreation struct {
	database          *DB
	description, goal string
	options           TaskCreateOptions
}

func (creation *taskRegistryCreation) admit() error {
	if len(creation.options.SourceTaskIDs) > MaxTaskSourceCount {
		return fmt.Errorf("too many source tasks: got %d, maximum is %d", len(creation.options.SourceTaskIDs), MaxTaskSourceCount)
	}
	if creation.options.StrictIsolation && len(creation.options.SourceTaskIDs) > 0 {
		return fmt.Errorf("strict isolation cannot be combined with source tasks")
	}
	companyIDs, err := NormalizeTaskCompanyIDs(creation.options.CompanyIDs)
	if err != nil {
		return err
	}
	creation.options.CompanyIDs = companyIDs
	return nil
}

func (creation *taskRegistryCreation) run() (*Task, error) {
	if err := creation.admit(); err != nil {
		return nil, err
	}
	tx, err := creation.database.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	expID, err := creation.graph(tx)
	if err != nil {
		return nil, err
	}
	t, err := creation.record(tx, expID)
	if err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

func (creation *taskRegistryCreation) graph(tx *sql.Tx) (int64, error) {
	var expID int64
	if err := tx.QueryRow(`INSERT INTO explorations(description, goal) VALUES ($1,$2) RETURNING id`, creation.description, creation.goal).Scan(&expID); err != nil {
		return 0, err
	}
	// origin fact: the exploration graph's root, a KindFact node (state='origin')
	// holding the original task description. Goals/intents/findings descend from
	// it, and the seeded target asset is anchored to it as lineage (the asset graph
	// is global and shared, not isolated per task). Being a fact (not a special 'begin' kind) lets every
	// intent uniformly connect to a fact node, including the first ones.
	originPayload, _ := json.Marshal(map[string]any{
		"summary":     "Task start: " + creation.description + "; Goal: " + creation.goal,
		"description": creation.description,
		"goal":        creation.goal,
	})
	if _, err := tx.Exec(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, 'fact', $2, 0, 'origin', 'system')`, expID, string(originPayload)); err != nil {
		return 0, err
	}
	return expID, nil
}

func (creation *taskRegistryCreation) record(tx *sql.Tx, expID int64) (*Task, error) {
	if creation.options.TimeoutSeconds < 0 {
		creation.options.TimeoutSeconds = 0
	}
	creation.options.PlanHeartbeatSeconds = normalizeHeartbeat(creation.options.PlanHeartbeatSeconds)
	coverageEnabled := creation.options.CoverageEnabled == nil || *creation.options.CoverageEnabled
	var categoryName string
	if creation.options.CategoryID != nil {
		if *creation.options.CategoryID <= 0 {
			return nil, fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
		}
		if err := tx.QueryRow(`SELECT name FROM task_categories WHERE id=$1`, *creation.options.CategoryID).Scan(&categoryName); err != nil {
			if err == sql.ErrNoRows {
				return nil, ErrTaskCategoryNotFound
			}
			return nil, err
		}
	}
	var active *int64
	if len(creation.options.LLMProfileIDs) > 0 {
		id := creation.options.LLMProfileIDs[0]
		active = &id
	}
	t := &Task{
		Name: creation.options.Name, CategoryID: creation.options.CategoryID, CategoryName: categoryName,
		Description: creation.description, Goal: creation.goal, ExplorationID: expID,
		LLMProfileID: active, ActiveLLMProfileID: active,
		LLMProfileIDs:  append([]int64(nil), creation.options.LLMProfileIDs...),
		SourceTaskIDs:  append([]int64(nil), creation.options.SourceTaskIDs...),
		CompanyIDs:     append([]int64(nil), creation.options.CompanyIDs...),
		TimeoutSeconds: creation.options.TimeoutSeconds, PlanHeartbeatSeconds: creation.options.PlanHeartbeatSeconds,
		CoverageEnabled: coverageEnabled, StrictIsolation: creation.options.StrictIsolation,
		SkipInitialKatana: creation.options.SkipInitialKatana, InitialScout: creation.options.InitialScout,
	}
	if err := insertTaskRow(tx, t); err != nil {
		return nil, err
	}
	if err := insertTaskRelations(tx, t.ID, creation.options.SourceTaskIDs); err != nil {
		return nil, err
	}
	if err := insertTaskCompanies(tx, t.ID, creation.options.CompanyIDs); err != nil {
		return nil, err
	}
	if err := insertTaskLLMProfiles(tx, t.ID, creation.options.LLMProfileIDs); err != nil {
		return nil, err
	}
	if err := insertTaskInterceptRules(tx, t.ID, creation.options.InterceptRules); err != nil {
		return nil, err
	}
	if len(creation.options.LLMProfileIDs) == 0 {
		t.LLMFailoverState = "default"
	} else {
		t.LLMFailoverState = "ready"
	}
	return t, nil
}
