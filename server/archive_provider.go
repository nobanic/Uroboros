package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/arsenal"
	"github.com/nobanic/UroborOS/db"
)

const maxArchiveProviderRequestBytes = 32 << 10

func (s *Server) archiveProviderTask(w http.ResponseWriter, r *http.Request) (int64, bool) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return 0, false
	}
	taskID, err := parseTaskID(task.ID)
	if err != nil || taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid task id")
		return 0, false
	}
	return taskID, true
}

func archiveProviderMetadata() map[string]any {
	return map[string]any{
		"provider":         db.ArchiveProviderInternetArchive,
		"provider_name":    "Internet Archive",
		"provider_host":    "web.archive.org",
		"contract_version": arsenal.ArchiveContractVersion,
		"notice_version":   arsenal.ArchiveNoticeVersion,
		"notice_template":  arsenal.ArchiveDisclosureNotice("{domain}"),
		"raw_retained":     false,
	}
}

func (s *Server) listArchiveProviderGrants(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.archiveProviderTask(w, r)
	if !ok {
		return
	}
	grants, err := s.m.PG().ListArchiveProviderGrants(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read archive provider grants: "+err.Error())
		return
	}
	response := archiveProviderMetadata()
	response["grants"] = grants
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) createArchiveProviderGrant(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.archiveProviderTask(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxArchiveProviderRequestBytes)
	var request struct {
		Domain        string    `json:"domain"`
		CaptureFrom   string    `json:"capture_from"`
		CaptureTo     string    `json:"capture_to"`
		ExpiresAt     time.Time `json:"expires_at"`
		NoticeVersion string    `json:"notice_version"`
		Acknowledged  bool      `json:"acknowledged"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid archive provider grant: "+err.Error())
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid archive provider grant: request must contain one JSON object")
		return
	}
	if !request.Acknowledged || request.NoticeVersion != arsenal.ArchiveNoticeVersion {
		writeErr(w, http.StatusBadRequest, "the current archive provider disclosure notice must be acknowledged")
		return
	}
	if request.ExpiresAt.IsZero() || !request.ExpiresAt.After(time.Now().UTC()) {
		writeErr(w, http.StatusBadRequest, "archive provider grant expiry must be in the future")
		return
	}
	runner := s.m.Arsenal()
	if runner == nil {
		writeErr(w, http.StatusServiceUnavailable, "Arsenal archive discovery is unavailable")
		return
	}
	spec, err := runner.ValidateArchiveGrant(taskID, request.Domain, request.CaptureFrom, request.CaptureTo)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	grant, err := s.m.PG().CreateArchiveProviderGrant(r.Context(), db.ArchiveProviderGrantCreate{
		TaskID: taskID, Provider: db.ArchiveProviderInternetArchive, Domain: spec.Domain,
		CaptureFrom: spec.CaptureFrom, CaptureTo: spec.CaptureTo,
		ContractVersion: arsenal.ArchiveContractVersion, NoticeVersion: arsenal.ArchiveNoticeVersion,
		ApprovedBy: "Uroboros", ExpiresAt: request.ExpiresAt.UTC(),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (s *Server) revokeArchiveProviderGrant(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.archiveProviderTask(w, r)
	if !ok {
		return
	}
	grantID, ok := pathInt(r, "grantID")
	if !ok || grantID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid archive provider grant id")
		return
	}
	revoked, jobID, err := s.m.PG().RevokeArchiveProviderGrantAndJob(r.Context(), taskID, grantID, "Uroboros")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "revoke archive provider grant: "+err.Error())
		return
	}
	if !revoked {
		writeErr(w, http.StatusNotFound, "archive provider grant not found")
		return
	}
	if runner := s.m.Arsenal(); runner != nil && jobID > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err = runner.CancelJob(ctx, taskID, jobID, errors.New("archive provider grant revoked"))
		cancel()
		if err != nil && !errors.Is(err, arsenal.ErrJobNotActive) {
			writeErr(w, http.StatusConflict, "archive provider grant was revoked but its job is still stopping")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": grantID})
}

func (s *Server) listArchiveProviderArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.archiveProviderTask(w, r)
	if !ok {
		return
	}
	jobID, limit, offset := int64(0), 100, 0
	if raw := strings.TrimSpace(r.URL.Query().Get("job_id")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid archive job id")
			return
		}
		jobID = value
		job, err := s.m.PG().ArsenalJob(r.Context(), jobID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read archive job: "+err.Error())
			return
		}
		if job == nil || job.TaskID != taskID || job.Tool != "archive" {
			writeErr(w, http.StatusNotFound, "archive job not found in this task")
			return
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 200 {
			writeErr(w, http.StatusBadRequest, "archive artifact limit must be between 1 and 200")
			return
		}
		limit = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeErr(w, http.StatusBadRequest, "archive artifact offset must be non-negative")
			return
		}
		offset = value
	}
	artifacts, err := s.m.PG().ListArchiveProviderArtifacts(r.Context(), taskID, jobID, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read archive provider artifacts: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": artifacts, "raw_retained": false})
}
