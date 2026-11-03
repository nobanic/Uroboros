package server

import (
	"net/http"
	"strconv"

	pgdb "github.com/nobanic/UroborOS/db"
)

// archiveControl borrows exactly one HTTP request/response. Public document
// types, decoders, error mapping, normalization and dependency policy remain
// the existing shared interfaces; this owner is never retained by the server.
type archiveControl struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

func (c archiveControl) list() {
	page, _ := strconv.Atoi(c.request.URL.Query().Get("page"))
	size, _ := strconv.Atoi(c.request.URL.Query().Get("size"))
	result, err := c.server.m.pg.ListTaskArchives(c.request.URL.Query().Get("q"), c.request.URL.Query().Get("state"), page, size)
	if err != nil {
		writeErr(c.writer, 500, err.Error())
		return
	}
	writeJSON(c.writer, 200, result)
}

func (c archiveControl) get() {
	id, ok := pathInt(c.request, "id")
	if !ok {
		writeErr(c.writer, 400, "invalid archive id")
		return
	}
	item, err := c.server.m.pg.GetTaskArchive(id)
	if err != nil {
		writeErr(c.writer, 500, err.Error())
		return
	}
	if item == nil {
		writeErr(c.writer, 404, "archive does not exist")
		return
	}
	writeJSON(c.writer, 200, item)
}

func (c archiveControl) queueTask() {
	id, ok := canonicalTaskID(c.request.PathValue("id"))
	if !ok {
		writeErr(c.writer, 400, "invalid task id")
		return
	}
	numeric, _ := strconv.ParseInt(id, 10, 64)
	item, err := c.server.m.pg.QueueTaskArchive(numeric)
	c.queued(item, err)
}

func (c archiveControl) queueArchive(restore bool) {
	id, ok := pathInt(c.request, "id")
	if !ok {
		writeErr(c.writer, 400, "invalid archive id")
		return
	}
	item, err := c.archiveAction(id, restore)
	c.queued(item, err)
}

func (c archiveControl) queued(item *pgdb.TaskArchive, err error) {
	if err != nil {
		writeArchiveError(c.writer, err)
		return
	}
	c.server.notifyTaskArchiveWorker()
	writeJSON(c.writer, http.StatusAccepted, item)
}

func (c archiveControl) archiveAction(id int64, restore bool) (*pgdb.TaskArchive, error) {
	if restore {
		return c.server.m.pg.QueueTaskArchiveRestore(id)
	}
	return c.server.m.pg.QueueTaskArchiveDelete(id)
}

func (c archiveControl) taskBatch() {
	var request archiveBatchRequest
	if err := decode(c.request, &request); err != nil {
		writeErr(c.writer, 400, err.Error())
		return
	}
	parsed := normalizeBatchTaskIDs(request.TaskIDs)
	if len(parsed) == 0 {
		writeErr(c.writer, 400, "task_ids cannot be empty")
		return
	}
	if len(parsed) > 100 {
		writeErr(c.writer, 400, "at most 100 tasks can be processed at once")
		return
	}
	ids := make([]string, 0, len(parsed))
	items := make([]archiveBatchItem, 0, len(parsed))
	for _, entry := range parsed {
		if !entry.valid {
			items = append(items, archiveBatchItem{ID: entry.id, Error: "invalid task id"})
			continue
		}
		ids = append(ids, entry.id)
	}
	for _, id := range c.server.orderLiveTaskArchiveBatch(ids) {
		numeric, _ := strconv.ParseInt(id, 10, 64)
		archive, err := c.server.m.pg.QueueTaskArchive(numeric)
		item := archiveBatchItem{ID: id, OK: err == nil, Queued: err == nil}
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Archive = archive.ID
		}
		items = append(items, item)
	}
	c.batchResult(items)
}

func (c archiveControl) archiveBatch(action string) {
	var request archiveBatchRequest
	if err := decode(c.request, &request); err != nil {
		writeErr(c.writer, 400, err.Error())
		return
	}
	ids, err := normalizeArchiveIDs(request.ArchiveIDs)
	if err != nil {
		writeErr(c.writer, 400, err.Error())
		return
	}
	ids = c.server.orderColdTaskArchiveBatch(ids, action == "restore")
	items := make([]archiveBatchItem, 0, len(ids))
	for _, id := range ids {
		archive, err := c.archiveAction(id, action == "restore")
		item := archiveBatchItem{ID: strconv.FormatInt(id, 10), Archive: id, OK: err == nil, Queued: err == nil}
		if err != nil {
			item.Error = err.Error()
		} else if archive != nil {
			item.Archive = archive.ID
		}
		items = append(items, item)
	}
	c.batchResult(items)
}

func (c archiveControl) batchResult(items []archiveBatchItem) {
	c.server.notifyTaskArchiveWorker()
	writeJSON(c.writer, http.StatusAccepted, map[string]any{"items": items})
}

// archiveBatchOrder only projects current live/cold source relationships into
// the original topoTaskIDs algorithm. Unknown/error IDs still reach admission
// as ordered per-item failures instead of disappearing from the response.
type archiveBatchOrder struct{ server *Server }

func (order archiveBatchOrder) live(ids []string) []string {
	sources := map[string][]string{}
	for _, id := range ids {
		if task, ok := order.server.m.Task(id); ok {
			for _, source := range task.lifecycleSnapshot().SourceTaskIDs {
				sources[id] = append(sources[id], strconv.FormatInt(source, 10))
			}
		}
	}
	return topoTaskIDs(ids, sources, false)
}

func (order archiveBatchOrder) cold(ids []int64, restore bool) []int64 {
	byTask := map[string]int64{}
	sources := map[string][]string{}
	var tasks []string
	for _, id := range ids {
		archive, err := order.server.m.pg.GetTaskArchive(id)
		if err != nil || archive == nil {
			continue
		}
		task := strconv.FormatInt(archive.TaskID, 10)
		byTask[task] = id
		tasks = append(tasks, task)
		for _, source := range archive.SourceTaskIDs {
			sources[task] = append(sources[task], strconv.FormatInt(source, 10))
		}
	}
	ordered := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, task := range topoTaskIDs(tasks, sources, restore) {
		id := byTask[task]
		ordered = append(ordered, id)
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			ordered = append(ordered, id)
		}
	}
	return ordered
}
