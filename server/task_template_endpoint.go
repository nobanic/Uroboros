package server

import (
	"github.com/nobanic/UroborOS/db"
	"net/http"
)

// Templates share transport outcomes with the category catalog. Original body
// decoding, validation, task-rule authority and the preset wire model stay intact.
type taskTemplateEndpoint struct{ server *Server }

func (endpoint taskTemplateEndpoint) list(w http.ResponseWriter) {
	database := endpoint.server.pg(w)
	if database == nil {
		return
	}
	values, failure := database.ListTaskTemplates()
	catalogResponse(w, http.StatusOK, map[string]any{"templates": values}, failure, func(w http.ResponseWriter, err error) { writeErr(w, http.StatusInternalServerError, err.Error()) })
}
func taskTemplateRequestFields(w http.ResponseWriter, r *http.Request) (taskTemplateRequest, map[string]struct{}, bool) {
	var request taskTemplateRequest
	present, valid := decodeTaskTemplateRequest(w, r, &request)
	if valid {
		if failure := validateTaskTemplateRequest(request); failure != nil {
			writeErr(w, http.StatusBadRequest, failure.Error())
			valid = false
		}
	}
	return request, present, valid
}
func taskTemplateRuleSnapshot(w http.ResponseWriter, request taskTemplateRequest) ([]db.TaskInterceptRuleInput, bool) {
	rules, failure := buildTaskInterceptRules(request.InterceptRules)
	if failure != nil {
		writeErr(w, http.StatusBadRequest, "interception/allow rules are invalid: "+failure.Error())
		return nil, false
	}
	return rules, true
}
func (endpoint taskTemplateEndpoint) create(w http.ResponseWriter, r *http.Request) {
	database := endpoint.server.pg(w)
	if database == nil {
		return
	}
	request, _, valid := taskTemplateRequestFields(w, r)
	if !valid {
		return
	}
	rules, valid := taskTemplateRuleSnapshot(w, request)
	if !valid {
		return
	}
	value, failure := database.CreateTaskTemplate(db.TaskTemplateInput{Name: stringValue(request.Name), Description: stringValue(request.Description), Goal: stringValue(request.Goal), CategoryID: request.CategoryID, InterceptRules: rules})
	catalogResponse(w, http.StatusCreated, value, failure, writeTaskTemplateErr)
}
func (endpoint taskTemplateEndpoint) patch(w http.ResponseWriter, r *http.Request) {
	database := endpoint.server.pg(w)
	if database == nil {
		return
	}
	id, valid := pathInt(r, "id")
	if !valid {
		writeErr(w, http.StatusBadRequest, "bad task template id")
		return
	}
	request, present, valid := taskTemplateRequestFields(w, r)
	if !valid {
		return
	}
	_, category := present["category_id"]
	_, rules := present["intercept_rules"]
	if request.Name == nil && request.Description == nil && request.Goal == nil && !category && !rules {
		writeErr(w, http.StatusBadRequest, "at least one of name, description, goal, category_id, or intercept_rules must be provided")
		return
	}
	patch := db.TaskTemplatePatch{Name: request.Name, Description: request.Description, Goal: request.Goal}
	if category {
		patch.SetCategoryID = true
		patch.CategoryID = request.CategoryID
	}
	if rules {
		snapshot, valid := taskTemplateRuleSnapshot(w, request)
		if !valid {
			return
		}
		patch.SetInterceptRules = true
		patch.InterceptRules = snapshot
	}
	value, failure := database.PatchTaskTemplate(id, patch)
	catalogResponse(w, http.StatusOK, value, failure, writeTaskTemplateErr)
}
func (endpoint taskTemplateEndpoint) remove(w http.ResponseWriter, r *http.Request) {
	database := endpoint.server.pg(w)
	if database == nil {
		return
	}
	id, valid := pathInt(r, "id")
	if !valid {
		writeErr(w, http.StatusBadRequest, "bad task template id")
		return
	}
	deleted, failure := database.DeleteTaskTemplate(id)
	if failure == nil && !deleted {
		writeErr(w, http.StatusNotFound, "task template not found")
		return
	}
	catalogResponse(w, http.StatusOK, map[string]any{"deleted": id}, failure, func(w http.ResponseWriter, err error) { writeErr(w, http.StatusInternalServerError, err.Error()) })
}
