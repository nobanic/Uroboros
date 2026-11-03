package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nobanic/UroborOS/db"
	"net/http"
)

// HTTP orchestration only. Existing decoders, Manager serialization/cache
// commands and DB authority retain their original public contracts.
type taskCategoryEndpoint struct{ server *Server }

func categoryResponse(w http.ResponseWriter, status int, value any, failure error, reject func(http.ResponseWriter, error)) {
	catalogResponse(w, status, value, failure, reject)
}
func (endpoint taskCategoryEndpoint) list(w http.ResponseWriter) {
	pg := endpoint.server.pg(w)
	if pg == nil {
		return
	}
	values, failure := pg.ListTaskCategories()
	categoryResponse(w, http.StatusOK, map[string]any{"categories": values}, failure, func(w http.ResponseWriter, err error) { writeErr(w, http.StatusInternalServerError, err.Error()) })
}
func (endpoint taskCategoryEndpoint) create(w http.ResponseWriter, r *http.Request) {
	pg := endpoint.server.pg(w)
	if pg == nil {
		return
	}
	request, valid := decodeTaskCategoryRequest(w, r)
	if !valid {
		return
	}
	value, failure := pg.CreateTaskCategory(*request.Name)
	categoryResponse(w, http.StatusCreated, value, failure, writeTaskCategoryError)
}
func (endpoint taskCategoryEndpoint) named(w http.ResponseWriter, r *http.Request, remove bool) {
	id, valid := pathInt(r, "id")
	if !valid {
		writeErr(w, http.StatusBadRequest, "bad task category id")
		return
	}
	if remove {
		deleted, failure := endpoint.server.m.DeleteTaskCategory(id)
		if failure == nil && !deleted {
			failure = db.ErrTaskCategoryNotFound
		}
		categoryResponse(w, http.StatusOK, map[string]any{"deleted": id}, failure, writeTaskCategoryError)
		return
	}
	request, valid := decodeTaskCategoryRequest(w, r)
	if !valid {
		return
	}
	value, failure := endpoint.server.m.RenameTaskCategory(id, *request.Name)
	categoryResponse(w, http.StatusOK, value, failure, writeTaskCategoryError)
}
func categoryMoveRequest[T any](w http.ResponseWriter, r *http.Request, request *T, largeStatus int) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskCategoryRequestBytes)
	if failure := decode(r, request); failure != nil {
		var large *http.MaxBytesError
		if largeStatus == http.StatusRequestEntityTooLarge && errors.As(failure, &large) {
			writeErr(w, largeStatus, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, failure.Error())
		}
		return false
	}
	return true
}
func (endpoint taskCategoryEndpoint) assign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, present := endpoint.server.m.Task(id); !present {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	var request struct {
		CategoryID json.RawMessage `json:"category_id"`
	}
	if !categoryMoveRequest(w, r, &request, http.StatusBadRequest) {
		return
	}
	destination, valid := parseCategoryIDField(w, request.CategoryID)
	if !valid {
		return
	}
	if _, failure := endpoint.server.m.SetTaskCategory(id, destination); failure != nil {
		writeTaskCategoryError(w, failure)
		return
	}
	task, _ := endpoint.server.m.Task(id)
	writeJSON(w, http.StatusOK, taskDTO(task, endpoint.server.resolvedTaskStatus(task)))
}

type taskCategorySelection struct {
	items   []batchCategoryItem
	pending []string
}

func (endpoint taskCategoryEndpoint) selection(ids []parsedTaskID) taskCategorySelection {
	selected := taskCategorySelection{items: make([]batchCategoryItem, 0, len(ids)), pending: make([]string, 0, len(ids))}
	for _, requested := range ids {
		item := batchCategoryItem{ID: requested.id}
		if !requested.valid {
			item.Error = "bad task id"
		} else if _, present := endpoint.server.m.Task(requested.id); present {
			selected.pending = append(selected.pending, requested.id)
		} else {
			item.Error = "task not found"
		}
		selected.items = append(selected.items, item)
	}
	return selected
}
func (selection *taskCategorySelection) assign(manager *Manager, destination *int64) (*db.TaskCategory, error) {
	if len(selection.pending) == 0 {
		return nil, nil
	}
	updated, category, failure := manager.SetTasksCategory(selection.pending, destination)
	if failure != nil {
		return nil, failure
	}
	for index := range selection.items {
		item := &selection.items[index]
		if item.Error == "" {
			item.OK = updated[item.ID]
			if !item.OK {
				item.Error = "task not found"
			}
		}
	}
	return category, nil
}
func (endpoint taskCategoryEndpoint) assignMany(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TaskIDs    []string        `json:"task_ids"`
		CategoryID json.RawMessage `json:"category_id"`
	}
	if !categoryMoveRequest(w, r, &request, http.StatusRequestEntityTooLarge) {
		return
	}
	destination, valid := parseCategoryIDField(w, request.CategoryID)
	if !valid {
		return
	}
	ids := normalizeBatchTaskIDs(request.TaskIDs)
	if len(ids) == 0 || len(ids) > db.MaxTaskCategoryBatchSize {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("task_ids count must be 1-%d", db.MaxTaskCategoryBatchSize))
		return
	}
	selection := endpoint.selection(ids)
	category, failure := selection.assign(endpoint.server.m, destination)
	categoryResponse(w, http.StatusOK, map[string]any{"items": selection.items, "category": category}, failure, writeTaskCategoryError)
}
