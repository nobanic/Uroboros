package server

import (
	"context"
	"errors"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/binarybroker"
	"github.com/nobanic/UroborOS/binaryintake"
	"github.com/nobanic/UroborOS/db"
)

func (s *Server) enqueueBinaryTraffic(origin binaryintake.TrafficOrigin) {
	if s == nil || s.binaryIntake == nil || s.binaryTrafficQ == nil || origin.TaskID <= 0 || origin.TrafficID == "" {
		return
	}
	select {
	case s.binaryTrafficQ <- origin:
	default:
		log.Printf("[binary-intake] queue full; task=%d producer=%s traffic=%s", origin.TaskID, origin.ProducerKind, origin.TrafficID)
	}
}

func (s *Server) runBinaryIntakeQueue() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case origin := <-s.binaryTrafficQ:
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
			result, err := s.binaryIntake.AdmitTraffic(ctx, origin)
			cancel()
			if err != nil {
				log.Printf("[binary-intake] rejected task=%d producer=%s job=%s traffic=%s: %v",
					origin.TaskID, origin.ProducerKind, origin.ProducerJobID, origin.TrafficID, err)
				continue
			}
			if result.Disposition == "admitted" && result.Artifact != nil {
				log.Printf("[binary-intake] admitted task=%d artifact=%s format=%s bytes=%d",
					origin.TaskID, result.Artifact.ArtifactID, result.Artifact.BinaryFormat, result.Artifact.ByteSize)
			}
		}
	}
}

func (s *Server) enqueueKatanaBinaryTraffic(taskID, jobID int64) {
	if s.binaryIntake == nil || taskID <= 0 || jobID <= 0 || s.m == nil || s.m.PG() == nil {
		return
	}
	go func() {
		seen := make(map[string]struct{})
		for offset := 0; ; offset += 1000 {
			items, err := s.m.PG().ListKatanaDiscoveries(s.ctx, jobID, 1000, offset)
			if err != nil {
				log.Printf("[binary-intake] list Katana job %d traffic: %v", jobID, err)
				return
			}
			for _, item := range items {
				if item.TaskID != taskID || !item.Verified || item.TrafficID == "" {
					continue
				}
				if _, duplicate := seen[item.TrafficID]; duplicate {
					continue
				}
				seen[item.TrafficID] = struct{}{}
				s.enqueueBinaryTraffic(binaryintake.TrafficOrigin{TaskID: taskID, TrafficID: item.TrafficID,
					ProducerKind: "katana", ProducerJobID: strconv.FormatInt(jobID, 10)})
			}
			if len(items) < 1000 {
				return
			}
		}
	}()
}

func (s *Server) enqueueArsenalBinaryTraffic(taskID, jobID int64) {
	if s.binaryIntake == nil || taskID <= 0 || jobID <= 0 || s.m == nil || s.m.PG() == nil {
		return
	}
	go func() {
		seen := make(map[string]struct{})
		for offset := 0; ; offset += 1000 {
			items, err := s.m.PG().ListArsenalObservations(s.ctx, jobID, 1000, offset)
			if err != nil {
				log.Printf("[binary-intake] list Arsenal job %d traffic: %v", jobID, err)
				return
			}
			for _, item := range items {
				if item.TaskID != taskID || !item.ProxyVerified || item.Authority != "candidate" || item.TrafficID == "" {
					continue
				}
				if _, duplicate := seen[item.TrafficID]; duplicate {
					continue
				}
				seen[item.TrafficID] = struct{}{}
				s.enqueueBinaryTraffic(binaryintake.TrafficOrigin{TaskID: taskID, TrafficID: item.TrafficID,
					ProducerKind: "arsenal", ProducerJobID: strconv.FormatInt(jobID, 10)})
			}
			if len(items) < 1000 {
				return
			}
		}
	}()
}

func (s *Server) binaryTask(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return s.archiveProviderTask(w, r)
}

func (s *Server) uploadBinaryArtifact(w http.ResponseWriter, r *http.Request) {
	if s.binaryIntake == nil {
		writeErr(w, http.StatusServiceUnavailable, "binary artifact intake is not configured")
		return
	}
	taskID, ok := s.binaryTask(w, r)
	if !ok {
		return
	}
	if r.ContentLength <= 0 {
		writeErr(w, http.StatusLengthRequired, "a positive Content-Length is required")
		return
	}
	if r.ContentLength > binarybroker.MaxArtifactBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "binary artifact exceeds the 512 MiB intake ceiling")
		return
	}
	contentType := r.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		writeErr(w, http.StatusUnsupportedMediaType, "multipart and workspace uploads are not accepted; send the raw body")
		return
	}
	name := strings.TrimSpace(r.Header.Get("X-Uroboros-Filename"))
	if disposition := r.Header.Get("Content-Disposition"); disposition != "" {
		if _, parameters, err := mime.ParseMediaType(disposition); err == nil && name == "" {
			name = parameters["filename"]
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, binarybroker.MaxArtifactBytes)
	result, err := s.binaryIntake.AdmitUpload(r.Context(), binaryintake.UploadOrigin{
		TaskID: taskID, Name: name, ContentType: contentType,
	}, r.Body, r.ContentLength)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, binaryintake.ErrNotConfigured) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	if result.Disposition == "skipped" {
		writeJSON(w, http.StatusUnsupportedMediaType, result)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (s *Server) listBinaryArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.binaryTask(w, r)
	if !ok {
		return
	}
	items, err := s.m.PG().ListBinaryArtifacts(r.Context(), taskID,
		atoiDefault(r.URL.Query().Get("limit"), 100), atoiDefault(r.URL.Query().Get("offset"), 0))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list binary artifacts: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": items})
}

func (s *Server) listBinaryTriageJobs(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.binaryTask(w, r)
	if !ok {
		return
	}
	items, err := s.m.PG().ListBinaryTriageJobs(r.Context(), taskID,
		atoiDefault(r.URL.Query().Get("limit"), 100), atoiDefault(r.URL.Query().Get("offset"), 0))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list binary triage jobs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items})
}

func (s *Server) listBinaryObservations(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.binaryTask(w, r)
	if !ok {
		return
	}
	items, err := s.m.PG().ListBinaryObservations(r.Context(), taskID, r.PathValue("jobID"),
		atoiDefault(r.URL.Query().Get("limit"), 100), atoiDefault(r.URL.Query().Get("offset"), 0))
	if err != nil {
		if errors.Is(err, db.ErrBinaryArtifactInput) {
			writeErr(w, http.StatusBadRequest, err.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, "list binary observations: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"observations": items})
}

func (s *Server) cancelBinaryTriageJob(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.binaryTask(w, r)
	if !ok {
		return
	}
	err := s.m.PG().CancelBinaryTriageJob(r.Context(), taskID, r.PathValue("jobID"))
	if err != nil {
		if errors.Is(err, db.ErrBinaryTriageState) {
			writeErr(w, http.StatusConflict, "only a queued binary triage job can be canceled")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"canceled": true})
}
