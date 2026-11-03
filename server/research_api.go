package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
)

const maxResearchGrantRequestBytes = 16 << 10

func researchDisclosureNotice(provider string, identities []string) string {
	return "Allow Uroboros to send research requests that identify this task's current explicit scope to " +
		provider + ". Frozen identities: " + strings.Join(identities, ", ") +
		". Search and vulnerability results remain untrusted leads and receive no asset, fact, scope or finding authority."
}

func researchEgressNotice(seeds []string) string {
	return "Allow bounded read-only GET/HEAD research through the fixed Tor profile on the origins derived from these seeds: " +
		strings.Join(seeds, ", ") + ". Redirects to other origins, direct fallback, forms, uploads and active scanning remain denied."
}

func (s *Server) researchTask(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return s.archiveProviderTask(w, r)
}

func (s *Server) listResearchGrants(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	grants, err := s.m.pg.ListResearchDisclosureGrants(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read research grants: "+err.Error())
		return
	}
	identities, err := s.m.pg.ResearchTaskIdentities(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read research disclosure scope: "+err.Error())
		return
	}
	config := s.m.ResearchRuntimeConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"contract_version": db.ResearchContractVersion,
		"contract_versions": map[string]string{
			"web_search":     db.ResearchContractVersion,
			"vulnerability":  db.VulnerabilityContractVersion,
			"research_fetch": db.TorResearchContractVersion,
		},
		"notice_version":    db.ResearchNoticeVersion,
		"current_provider":  config.Primary,
		"fallback_provider": config.Fallback,
		"providers": []string{
			"brave-free", "tavily", "ddgs", "cve-list-v5", "nvd", "osv", "ghsa", "cisa-kev", "first-epss",
		},
		"disclosure_identities": identities,
		"notice_template":       researchDisclosureNotice("{provider}", identities),
		"grants":                grants,
	})
}

func (s *Server) createResearchGrant(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResearchGrantRequestBytes)
	var request struct {
		Provider      string    `json:"provider"`
		ExpiresAt     time.Time `json:"expires_at"`
		MaxRequests   int       `json:"max_requests"`
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
			writeErr(w, http.StatusBadRequest, "invalid research grant: "+err.Error())
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid research grant: request must contain one JSON object")
		return
	}
	if !request.Acknowledged || request.NoticeVersion != db.ResearchNoticeVersion {
		writeErr(w, http.StatusBadRequest, "the current research disclosure notice must be acknowledged")
		return
	}
	config := s.m.ResearchRuntimeConfig()
	if strings.TrimSpace(request.Provider) == "" {
		request.Provider = config.Primary
	}
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	if request.MaxRequests == 0 {
		request.MaxRequests = 20
	}
	grant, err := s.m.pg.CreateResearchDisclosureGrant(r.Context(), taskID,
		strings.TrimSpace(request.Provider), "Uroboros", request.ExpiresAt.UTC(), request.MaxRequests)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (s *Server) revokeResearchGrant(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	grantID, ok := pathInt(r, "grantID")
	if !ok || grantID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid research grant id")
		return
	}
	revoked, err := s.m.pg.RevokeResearchDisclosureGrant(r.Context(), taskID, grantID, "Uroboros")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "revoke research grant: "+err.Error())
		return
	}
	if !revoked {
		writeErr(w, http.StatusNotFound, "research grant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": grantID})
}

func (s *Server) listResearchEgressGrants(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	grants, err := s.m.pg.ListResearchEgressGrants(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read research egress grants: "+err.Error())
		return
	}
	config := s.m.ResearchRuntimeConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"profile_id":           db.TorResearchProfileID,
		"contract_version":     db.TorResearchContractVersion,
		"notice_version":       db.ResearchEgressNoticeVersion,
		"enabled":              config.TorEnabled,
		"endpoint_configured":  strings.TrimSpace(config.TorSOCKSURL) != "",
		"raw_endpoint_exposed": false,
		"notice_template":      researchEgressNotice([]string{"{seed}"}),
		"grants":               grants,
	})
}

func (s *Server) createResearchEgressGrant(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResearchGrantRequestBytes)
	var request struct {
		ProfileID     string    `json:"profile_id"`
		Seeds         []string  `json:"seeds"`
		ExpiresAt     time.Time `json:"expires_at"`
		MaxRequests   int       `json:"max_requests"`
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
			writeErr(w, http.StatusBadRequest, "invalid research egress grant: "+err.Error())
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid research egress grant: request must contain one JSON object")
		return
	}
	if !request.Acknowledged || request.NoticeVersion != db.ResearchEgressNoticeVersion {
		writeErr(w, http.StatusBadRequest, "the current research egress notice must be acknowledged")
		return
	}
	if request.ProfileID == "" {
		request.ProfileID = db.TorResearchProfileID
	}
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	if request.MaxRequests == 0 {
		request.MaxRequests = 20
	}
	grant, err := s.m.pg.CreateResearchEgressGrant(r.Context(), taskID, request.ProfileID,
		request.Seeds, "Uroboros", request.ExpiresAt.UTC(), request.MaxRequests)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (s *Server) revokeResearchEgressGrant(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	grantID, ok := pathInt(r, "grantID")
	if !ok || grantID <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid research egress grant id")
		return
	}
	revoked, err := s.m.pg.RevokeResearchEgressGrant(r.Context(), taskID, grantID, "Uroboros")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "revoke research egress grant: "+err.Error())
		return
	}
	if !revoked {
		writeErr(w, http.StatusNotFound, "research egress grant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": grantID})
}

func (s *Server) listResearchArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.researchTask(w, r)
	if !ok {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			writeErr(w, http.StatusBadRequest, "research artifact limit must be between 1 and 500")
			return
		}
		limit = value
	}
	artifacts, err := s.m.pg.ListResearchArtifacts(r.Context(), taskID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read research artifacts: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"raw_retained":      false,
		"finding_authority": false,
		"artifacts":         artifacts,
	})
}
