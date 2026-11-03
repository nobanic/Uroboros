package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/nobanic/UroborOS/db"
)

// Approval reads keep separate legacy result shapes and task/global query
// limits. The request resolves one filter/page and never infers task authority.
type interceptApprovalRead struct {
	database *db.DB
	request  *http.Request
	task     bool
	taskID   string
}

func (endpoint interceptHTTP) history() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	return (interceptApprovalRead{database: pg, request: endpoint.request}).document()
}
func (endpoint interceptHTTP) taskItems() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	taskID := endpoint.request.PathValue("taskID")
	if taskID == "" {
		return interceptFailure(400, "bad task id")
	}
	return (interceptApprovalRead{database: pg, request: endpoint.request, task: true, taskID: taskID}).document()
}
func (read interceptApprovalRead) document() interceptDocument {
	q := read.request.URL.Query()
	filter, err := interceptFilterParams(q)
	if err != nil {
		return interceptFailure(400, err.Error())
	}
	if q.Get("page") == "" && q.Get("size") == "" && filter == (db.InterceptApprovalFilter{}) {
		var items []db.InterceptApprovalRow
		var err error
		if read.task {
			items, err = read.database.ListTaskIntercepts(read.taskID)
		} else {
			items, err = read.database.ListAllIntercepts(200)
		}
		if err != nil {
			return interceptFailure(500, err.Error())
		}
		if items == nil {
			items = []db.InterceptApprovalRow{}
		}
		return interceptDocument{status: 200, value: map[string]any{"items": items, "total": len(items)}}
	}
	page, size := interceptPageParams(q)
	var items []db.InterceptApprovalRow
	var total int
	if read.task {
		items, total, err = read.database.ListTaskInterceptsPage(read.taskID, page, size, filter)
	} else {
		items, total, err = read.database.ListAllInterceptsPage(page, size, filter)
	}
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	if items == nil {
		items = []db.InterceptApprovalRow{}
	}
	return interceptDocument{status: 200, value: map[string]any{"items": items, "total": total, "page": page, "page_size": size}}
}

func (endpoint interceptHTTP) pending() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	pending, err := pg.ListPendingIntercepts()
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	if pending == nil {
		pending = []db.InterceptPending{}
	}
	return interceptDocument{status: 200, value: map[string]any{"pending": pending}}
}
func (endpoint interceptHTTP) one() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	id, ok := pathInt(endpoint.request, "id")
	if !ok {
		return interceptFailure(400, "bad pending id")
	}
	p, err := pg.GetInterceptPending(id)
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	if p == nil {
		return interceptFailure(404, "not found")
	}
	return interceptDocument{status: 200, value: p}
}
func (endpoint interceptHTTP) detail() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	id, ok := pathInt(endpoint.request, "id")
	if !ok || id <= 0 {
		return interceptFailure(400, "bad approval id")
	}
	detail, err := pg.GetInterceptDetail(id)
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	if detail == nil {
		return interceptFailure(404, "not found")
	}
	return interceptDocument{status: 200, value: detail}
}
func (endpoint interceptHTTP) execution() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	id, ok := pathInt(endpoint.request, "id")
	if !ok || id <= 0 {
		return interceptFailure(400, "bad approval id")
	}
	target, err := pg.GetInterceptExecution(id)
	if errors.Is(err, db.ErrInterceptTaskDeleted) || errors.Is(err, db.ErrInterceptSessionDeleted) {
		return interceptFailure(http.StatusGone, err.Error())
	}
	if errors.Is(err, db.ErrInterceptExecutionUnavailable) {
		return interceptFailure(409, err.Error())
	}
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	if target == nil {
		// Conversation deletion cascades approval rows. A stale source link still
		// carries its conversation ID, allowing a precise message without retaining
		// deleted conversations or changing their deletion semantics.
		if convID, parseErr := strconv.ParseInt(endpoint.request.URL.Query().Get("conversation"), 10, 64); parseErr == nil && convID > 0 {
			conv, getErr := pg.GetConversation(convID)
			if getErr != nil {
				return interceptFailure(500, getErr.Error())
			}
			if conv == nil {
				return interceptFailure(http.StatusGone, "conversation has been deleted")
			}
		}
		return interceptFailure(404, "approval record has been deleted or does not exist")
	}
	return interceptDocument{status: 200, value: map[string]any{"conversation_id": target.ConversationID, "task_id": target.TaskID, "session": target.Session, "seq": target.Seq, "items": activityDTOs(target.Items)}}
}
