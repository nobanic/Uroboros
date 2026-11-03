package server

import (
	"log"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

func (q findingRequest) list() findingResponse {
	if taskID := q.query.Get("task"); taskID != "" {
		return q.taskNodes(taskID)
	}
	// The unpaged aggregation client intentionally has best-effort SQL semantics
	// and ignores filters. Only explicit paging selects the filtered repository.
	if q.query.Get("page") == "" && q.query.Get("limit") == "" {
		rows, _ := q.server.m.pg.ListFindings(500)
		return findingDocument(q.server.findingRows(rows), nil)
	}
	page, size := q.page(20, 200)
	rows, total, err := q.server.m.pg.ListFindingsPage(findingFilterFromQuery(q.query), page, size)
	if err != nil {
		return findingDocument(nil, err)
	}
	return findingDocument(map[string]any{
		"items": q.server.findingRows(rows), "total": total, "page": page, "page_size": size,
	}, nil)
}

func (q findingRequest) taskNodes(id string) findingResponse {
	task := q.server.m.ResolveTask(id)
	if task == nil {
		return findingDocument([]any{}, nil)
	}
	nodes, err := task.Store.ListByKind(db.KindFinding, 200)
	if err != nil {
		return findingDocument(nil, err)
	}
	numericID, _ := strconv.ParseInt(task.ID, 10, 64)
	meta, err := q.server.m.pg.FindingMetaByNodeID(numericID)
	if err != nil {
		log.Printf("[findings] task=%s meta: %v", task.ID, err)
	}
	view := findingDTOsForTask(task, nodes, meta, q.server.findingNodeAssets(meta))
	sources, err := task.Store.DirectSourceStores()
	if err != nil {
		return findingDocument(nil, err)
	}
	for _, source := range sources {
		inherited, err := source.Store.ListByKind(db.KindFinding, 200)
		if err != nil {
			return findingDocument(nil, err)
		}
		for _, node := range inherited {
			node.SourceTaskID, node.Inherited = source.Task.TaskID, true
		}
		metadata, err := q.server.m.pg.FindingMetaByNodeID(source.Task.TaskID)
		if err != nil {
			log.Printf("[findings] source_task=%d meta: %v", source.Task.TaskID, err)
		}
		view = append(view, findingDTOsForOwner(i64s(source.Task.TaskID), source.Task.Description,
			inherited, metadata, q.server.findingNodeAssets(metadata))...)
	}
	return findingDocument(view, nil)
}

func (q findingRequest) detail() findingResponse {
	record, failure := q.record()
	if record == nil {
		return failure
	}
	view := findingFromDB(record, q.server.resolveAssetIDs(record.AssetIDs))
	if contextID := strings.TrimSpace(q.query.Get("context_task")); contextID != "" {
		task := q.server.m.ResolveTask(contextID)
		if task == nil {
			return findingFailure(404, "context task not found")
		}
		source, inherited, allowed := findingProvenanceInTask(task, record.TaskID)
		if !allowed {
			return findingFailure(404, "finding not available in task context")
		}
		view.SourceTaskID, view.Inherited = source, inherited
	}
	return findingDocument(view, nil)
}

func (q findingRequest) lineage() findingResponse {
	record, failure := q.record()
	if record == nil {
		return failure
	}
	empty := findingDocument(map[string]any{"nodes": []any{}, "edges": []any{}}, nil)
	if record.NodeID == nil || record.TaskID == nil {
		return empty
	}
	task := q.server.m.ResolveTask(i64s(*record.TaskID))
	if task == nil {
		return empty
	}
	nodes, edges, err := task.Store.FindingLineage(*record.NodeID)
	if err != nil {
		return findingDocument(nil, err)
	}
	return findingDocument(map[string]any{"nodes": taskNodeDTOs(nodes), "edges": edgeDTOs(edges)}, nil)
}

func (q findingRequest) stats() findingResponse {
	return findingDocument(q.server.m.pg.FindingStats())
}

func (q findingRequest) assetTree() findingResponse {
	return findingDocument(q.server.m.pg.BuildFindingAssetTree(findingFilterFromQuery(q.query)))
}

func (q findingRequest) groups() findingResponse {
	page, size := q.page(10, 100)
	rows, total, count, err := q.server.m.pg.ListFindingGroups(findingFilterFromQuery(q.query), page, size)
	if err != nil {
		return findingDocument(nil, err)
	}
	// Persistence retains paused/queued authority; engine state overlays only
	// live tasks exactly as the task list does.
	if q.server.engine != nil {
		for i := range rows {
			if rows[i].TaskID != nil {
				if task, found := q.server.m.Task(i64s(*rows[i].TaskID)); found {
					rows[i].TaskStatus = q.server.resolvedTaskStatus(task)
				}
			}
		}
	}
	return findingDocument(map[string]any{
		"items": rows, "total": total, "finding_total": count, "page": page, "page_size": size,
	}, nil)
}
