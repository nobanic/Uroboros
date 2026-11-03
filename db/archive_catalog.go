package db

import "strconv"

// This ordered catalog binds task IDs by their original storage type. Selection,
// joins and predicates are source-owned; no API input can supply a SQL identifier.
// Order determines the first snapshot failure and the streamed writer boundary.
type archiveTableRead struct {
	name, columns, relation, criteria, order string
	values                                   []any
}

func archiveTable(name, criteria, order string, values ...any) archiveTableRead {
	return archiveTableRead{name: name, columns: "*", relation: name, criteria: criteria, order: order, values: values}
}
func (read archiveTableRead) from(relation, columns string) archiveTableRead {
	read.relation, read.columns = relation, columns
	return read
}
func (read archiveTableRead) statement() string {
	query := "SELECT " + read.columns + " FROM " + read.relation + " WHERE " + read.criteria
	if read.order != "" {
		query += " ORDER BY " + read.order
	}
	return query
}
func archiveSnapshotReads(taskID, expID int64) []archiveTableRead {
	return []archiveTableRead{
		archiveTable("tasks", "id=$1", "", taskID),
		archiveTable("explorations", "id=$1", "", expID),
		archiveTable("exploration_nodes", "exploration_id=$1", "id", expID),
		archiveTable("exploration_edges", "exploration_id=$1", "src_id,dst_id", expID),
		archiveTable("exploration_anchors", "node.exploration_id=$1", "node_id,asset_id", expID).from("exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id", "anchor.*"),
		archiveTable("task_constraints", "exploration_id=$1", "id", expID),
		archiveTable("activity", "exploration_id=$1", "id", expID),
		archiveTable("task_relations", "task_id=$1", "created_at,source_task_id", taskID),
		archiveTable("task_asset_links", "task_id=$1", "asset_id", taskID),
		archiveTable("task_llm_profiles", "task_id=$1", "position", taskID),
		archiveTable("task_scope", "task_id=$1", "id", taskID),
		archiveTable("katana_jobs", "task_id=$1", "id", taskID),
		archiveTable("katana_discoveries", "task_id=$1", "id", taskID),
		archiveTable("discovery_reviews", "task_id=$1", "id", taskID),
		archiveTable("arsenal_jobs", "task_id=$1", "id", taskID),
		archiveTable("arsenal_observations", "task_id=$1", "id", taskID),
		archiveTable("binary_artifacts", "task_id=$1", "artifact_id", taskID),
		archiveTable("binary_artifact_origins", "task_id=$1", "artifact_id,id", taskID),
		archiveTable("binary_triage_jobs", "task_id=$1", "created_at,job_id", taskID),
		archiveTable("binary_observations", "task_id=$1", "job_id,id", taskID),
		archiveTable("archive_provider_grants", "task_id=$1", "id", taskID),
		archiveTable("archive_provider_artifacts", "task_id=$1", "id", taskID),
		archiveTable("research_disclosure_grants", "task_id=$1", "id", taskID),
		archiveTable("research_egress_grants", "task_id=$1", "id", taskID),
		archiveTable("research_artifacts", "task_id=$1", "id", taskID),
		archiveTable("findings", "task_id=$1", "id", taskID),
		archiveTable("finding_experiments", "f.task_id=$1", "e.id", taskID).from("finding_experiments e JOIN findings f ON f.id=e.finding_id", "e.*"),
		archiveTable("finding_retests", "f.task_id=$1", "r.id", taskID).from("finding_retests r JOIN findings f ON f.id=r.finding_id", "r.*"),
		archiveTable("finding_experiment_replays", "f.task_id=$1", "p.retest_id,p.job_id", taskID).from("finding_experiment_replays p JOIN finding_retests r ON r.id=p.retest_id JOIN findings f ON f.id=r.finding_id", "p.*"),
		archiveTable("finding_traffic_bindings", "f.task_id=$1", "b.finding_id,b.position,b.id", taskID).from("finding_traffic_bindings b JOIN findings f ON f.id=b.finding_id", "b.*"),
		archiveTable("traffic_evidence_snapshots", "EXISTS(SELECT 1 FROM finding_traffic_bindings b JOIN findings f ON f.id=b.finding_id WHERE b.snapshot_id=s.id AND f.task_id=$1)", "s.id", taskID).from("traffic_evidence_snapshots s", "s.*"),
		archiveTable("llm_records", "COALESCE(task_id,'')=$1", "id", strconv.FormatInt(taskID, 10)),
		archiveTable("llm_usage", "COALESCE(task_id,'')=$1 OR exploration_id=$2", "id", strconv.FormatInt(taskID, 10), expID),
		archiveTable("skill_usage", "task_id=$1 OR exploration_id=$2", "id", taskID, expID),
		archiveTable("tool_usage", "task_id=$1 OR exploration_id=$2", "id", taskID, expID),
		archiveTable("intercept_pending", "COALESCE(task_id,'')=$1", "id", strconv.FormatInt(taskID, 10)),
		archiveTable("side_question_sessions", "task_id=$1", "session_key", taskID),
		archiveTable("side_question_requests", "s.task_id=$1", "r.ordinal", taskID).from("side_question_requests r JOIN side_question_sessions s ON s.session_key=r.session_key", "r.*"),
		archiveTable("assets", "asset.id IN ("+archiveAssetIDsQuery()+")", "asset.id", taskID, expID).from("assets asset", "asset.*"),
	}
}
