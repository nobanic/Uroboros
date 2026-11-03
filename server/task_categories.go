package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/db"
)

const maxTaskCategoryRequestBytes = 16 << 10

type taskCategoryRequest struct {
	Name *string `json:"name"`
}

func decodeTaskCategoryRequest(w http.ResponseWriter, r *http.Request) (*taskCategoryRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskCategoryRequestBytes)
	var request taskCategoryRequest
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return nil, false
	}
	if request.Name == nil || strings.TrimSpace(*request.Name) == "" {
		writeErr(w, http.StatusBadRequest, "category name must not be empty")
		return nil, false
	}
	if utf8.RuneCountInString(strings.TrimSpace(*request.Name)) > db.MaxTaskCategoryNameRunes {
		writeErr(w, http.StatusBadRequest, "category name is at most 80 characters")
		return nil, false
	}
	return &request, true
}

func writeTaskCategoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTaskCategoryInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrTaskCategoryNameConflict):
		writeErr(w, http.StatusConflict, "category name already exists")
	case errors.Is(err, db.ErrTaskCategoryNotFound), errors.Is(err, db.ErrTaskCategoryTaskNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) pgListTaskCategories(w http.ResponseWriter, _ *http.Request) {
	(taskCategoryEndpoint{s}).list(w)
}

func (s *Server) pgCreateTaskCategory(w http.ResponseWriter, r *http.Request) {
	(taskCategoryEndpoint{s}).create(w, r)
}

func (s *Server) pgRenameTaskCategory(w http.ResponseWriter, r *http.Request) {
	(taskCategoryEndpoint{s}).named(w, r, false)
}

func (s *Server) pgDeleteTaskCategory(w http.ResponseWriter, r *http.Request) {
	(taskCategoryEndpoint{s}).named(w, r, true)
}

// parseCategoryIDField reads the category_id field shared by the single-task and
// batch endpoints. The field is required but may be null — that clears the
// category — so it arrives as RawMessage instead of *int64, which cannot tell
// an explicit null from an omitted key.
func parseCategoryIDField(w http.ResponseWriter, raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "category_id is required")
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid task category id")
		return nil, false
	}
	return &id, true
}

func (s *Server) updateTaskCategory(w http.ResponseWriter, r *http.Request) {
	(taskCategoryEndpoint{s}).assign(w, r)
}

// batchCategoryItem reports one requested task. Unlike batch pause/resume there
// is no per-task runtime outcome, so only the failure reason is carried.
type batchCategoryItem struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// updateTasksCategoryBatch moves every selected task into one category, or
// clears it when category_id is null. The database write is atomic; per-task
// entries only distinguish ids that never resolved to a live task, so a partial
// success here means those tasks were deleted, not that the move half-applied.
func (s *Server) updateTasksCategoryBatch(w http.ResponseWriter, r *http.Request) {
	(taskCategoryEndpoint{s}).assignMany(w, r)
}
