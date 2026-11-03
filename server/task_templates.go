package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/db"
)

const maxTaskTemplateRequestBytes = 512 << 10

// taskTemplateRequest uses pointers so PATCH can distinguish omitted fields from
// explicit empty values. Empty values are still rejected by the DB validator.
// category_id / intercept_rules presence is detected via the raw key set (see
// decodeTaskTemplateRequest) so a PATCH can clear category to null.
type taskTemplateRequest struct {
	Name           *string                `json:"name"`
	Description    *string                `json:"description"`
	Goal           *string                `json:"goal"`
	CategoryID     *int64                 `json:"category_id"`
	InterceptRules []taskInterceptRuleReq `json:"intercept_rules"`
}

// decodeTaskTemplateRequest decodes the body into req and returns the set of
// top-level keys present in the JSON (for PATCH presence detection).
func decodeTaskTemplateRequest(w http.ResponseWriter, r *http.Request, req *taskTemplateRequest) (map[string]struct{}, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskTemplateRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if err := json.Unmarshal(body, req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	present := make(map[string]struct{}, len(raw))
	for k := range raw {
		present[k] = struct{}{}
	}
	return present, true
}

func validateTaskTemplateRequest(req taskTemplateRequest) error {
	checks := []struct {
		name  string
		value *string
		limit int
	}{
		{name: "name", value: req.Name, limit: db.MaxTaskTemplateNameRunes},
		{name: "description", value: req.Description, limit: db.MaxTaskTemplateTextRunes},
		{name: "goal", value: req.Goal, limit: db.MaxTaskTemplateTextRunes},
	}
	for _, check := range checks {
		if check.value == nil {
			continue
		}
		value := strings.TrimSpace(*check.value)
		if check.name == "name" {
			value = strings.Join(strings.Fields(value), " ")
		}
		if utf8.RuneCountInString(value) > check.limit {
			return fmt.Errorf("%s may be at most %d characters", check.name, check.limit)
		}
	}
	return nil
}

func writeTaskTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTaskTemplateInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrTaskTemplateNameConflict):
		writeErr(w, http.StatusConflict, "template name already exists")
	case errors.Is(err, db.ErrTaskTemplateNotFound):
		writeErr(w, http.StatusNotFound, "task template not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) pgListTaskTemplates(w http.ResponseWriter, _ *http.Request) {
	(taskTemplateEndpoint{s}).list(w)
}

func (s *Server) pgCreateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	(taskTemplateEndpoint{s}).create(w, r)
}

func (s *Server) pgUpdateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	(taskTemplateEndpoint{s}).patch(w, r)
}

func (s *Server) pgDeleteTaskTemplate(w http.ResponseWriter, r *http.Request) {
	(taskTemplateEndpoint{s}).remove(w, r)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
