package db

import (
	"encoding/json"
	"fmt"
	"io"
)

// archiveRestoration owns one versioned manifest handoff. Global references and
// row remapping still use the original helpers; they never create lost global
// categories, profiles or companies as an incidental effect of restoration.
type archiveRestoration struct {
	database  *DB
	archiveID int64
	snapshot  *TaskArchiveSnapshot
	remaining int64
	records   io.Reader
}

func (r archiveRestoration) apply() ([]string, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	mutation, err := r.database.beginArchiveMutation(r.archiveID)
	if err != nil {
		return nil, err
	}
	defer mutation.tx.Rollback() //nolint:errcheck
	if err := mutation.requireSnapshot(r.snapshot, Restoring, "restore"); err != nil {
		return nil, err
	}
	mapping, warnings, err := restoreArchiveAssets(mutation.tx, mutation.taskID, r.snapshot.Tables["assets"])
	if err != nil {
		return nil, err
	}
	// The historical empty result is an allocated array, even when helpers
	// returned no warnings. Preserve it through the final response and storage.
	if warnings == nil {
		warnings = []string{}
	}
	if err := r.restoreStubs(mutation, &warnings); err != nil {
		return nil, err
	}
	if err := r.restoreResearch(mutation); err != nil {
		return nil, err
	}
	tables, err := remapArchiveAssetReferences(r.snapshot.Tables, mapping)
	if err != nil {
		return nil, err
	}
	tables["findings"], err = normalizeArchivedFindingVersions(tables["findings"])
	if err != nil {
		return nil, err
	}
	if err := mutation.restoreArchiveGraph(tables, &warnings); err != nil {
		return nil, err
	}
	if err := restoreFindingTrafficTx(mutation.tx, r.snapshot); err != nil {
		return nil, fmt.Errorf("restore finding traffic: %w", err)
	}
	if err := restoreFindingExperimentsTx(mutation.tx, r.snapshot); err != nil {
		return nil, fmt.Errorf("restore finding experiments: %w", err)
	}
	if err := r.restoreHistory(mutation, tables); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(warnings)
	if err := mutation.exec(`UPDATE task_archives SET warnings=$2,phase='database_restored',progress=85,error='' WHERE id=$1`, r.archiveID, string(raw)); err != nil {
		return nil, err
	}
	return warnings, mutation.tx.Commit()
}

func (r archiveRestoration) validate() error {
	if r.snapshot == nil || !IsTaskArchiveFormatSupported(r.snapshot.FormatVersion) {
		return ErrTaskArchiveFormatMismatch
	}
	streamed := r.snapshot.StreamedTables["llm_records"]
	if (len(r.snapshot.StreamedTables) > 0 && r.snapshot.FormatVersion < 2) || len(r.snapshot.StreamedTables) > 1 ||
		(len(r.snapshot.StreamedTables) == 1 && streamed != TaskArchiveLLMRecordsPath) {
		return fmt.Errorf("%w: unsupported streamed table metadata", ErrTaskArchiveFormatMismatch)
	}
	if streamed != "" && r.records == nil {
		return fmt.Errorf("%w: streamed LLM records are missing", ErrTaskArchiveFormatMismatch)
	}
	return nil
}

func (r archiveRestoration) restoreStubs(m *archiveMutation, warnings *[]string) error {
	rows, err := decodeArchiveRows(r.snapshot.Tables["tasks"])
	if err != nil || len(rows) != 1 {
		return fmt.Errorf("restore task row: expected one row: %w", err)
	}
	row := rows[0]
	for _, reference := range []struct{ key, table, warning string }{
		{"category_id", "task_categories", "task category %d was deleted; restored as uncategorized"},
		{"company_id", "companies", "task company %d was deleted; company association skipped"},
		{"llm_profile_id", "llm_profiles", "LLM profile %d was deleted; removed from the task configuration"},
		{"active_llm_profile_id", "llm_profiles", "LLM profile %d was deleted; removed from the task configuration"},
	} {
		id, _ := jsonInt64(row[reference.key])
		if id > 0 && !rowExists(m.tx, reference.table, id) {
			row[reference.key] = nil
			*warnings = append(*warnings, fmt.Sprintf(reference.warning, id))
		}
	}
	if err := restoreExplorationStub(m.tx, r.snapshot.Tables["explorations"], m.exploration); err != nil {
		return err
	}
	if err := restoreTaskStub(m.tx, row, m.taskID, r.remaining); err != nil {
		return err
	}
	if r.snapshot.FormatVersion >= 8 {
		return restoreBinaryArtifactRows(m.tx, r.archiveID, m.taskID, r.snapshot.Tables)
	}
	return nil
}

func (r archiveRestoration) restoreResearch(m *archiveMutation) error {
	if r.snapshot.FormatVersion < 9 {
		return nil
	}
	if err := m.restoreRows(r.snapshot.Tables, "research_disclosure_grants"); err != nil {
		return err
	}
	if r.snapshot.FormatVersion >= 11 {
		if err := m.restoreRows(r.snapshot.Tables, "research_egress_grants"); err != nil {
			return err
		}
	}
	return m.restoreRows(r.snapshot.Tables, "research_artifacts")
}

func (m *archiveMutation) restoreRows(tables map[string]json.RawMessage, names ...string) error {
	for _, table := range names {
		if err := insertArchiveRows(m.tx, table, tables[table]); err != nil {
			return fmt.Errorf("restore %s: %w", table, err)
		}
	}
	return nil
}

func (m *archiveMutation) restoreArchiveGraph(tables map[string]json.RawMessage, warnings *[]string) error {
	if err := m.restoreRows(tables, "exploration_nodes", "exploration_edges", "exploration_anchors", "task_constraints", "activity"); err != nil {
		return err
	}
	more, err := restoreTaskRelations(m.tx, m.taskID, tables["task_relations"])
	if err != nil {
		return err
	}
	*warnings = append(*warnings, more...)
	if err := m.restoreRows(tables, "side_question_sessions", "side_question_requests"); err != nil {
		return err
	}
	more, err = restoreTaskScopes(m.tx, tables["task_scope"])
	if err != nil {
		return err
	}
	*warnings = append(*warnings, more...)
	more, err = restoreTaskLLMProfiles(m.tx, tables["task_llm_profiles"])
	if err != nil {
		return err
	}
	*warnings = append(*warnings, more...)
	return m.restoreRows(tables, "task_asset_links", "findings", "katana_jobs", "katana_discoveries", "discovery_reviews", "archive_provider_grants", "arsenal_jobs", "arsenal_observations", "archive_provider_artifacts")
}

func (r archiveRestoration) restoreHistory(m *archiveMutation, tables map[string]json.RawMessage) error {
	if r.snapshot.StreamedTables["llm_records"] != "" {
		count, err := insertArchiveJSONSequenceRows(m.tx, "llm_records", r.records)
		if err != nil {
			return fmt.Errorf("restore llm_records: %w", err)
		}
		if expected := r.snapshot.DataCounts["llm_records"]; count != expected {
			return fmt.Errorf("restore llm_records: row count %d does not match manifest %d", count, expected)
		}
	} else if err := m.restoreRows(tables, "llm_records"); err != nil {
		return err
	}
	if err := m.restoreRows(tables, "llm_usage", "skill_usage", "tool_usage"); err != nil {
		return err
	}
	return restoreInterceptRows(m.tx, tables["intercept_pending"])
}
