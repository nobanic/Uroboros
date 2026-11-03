package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nobanic/UroborOS/db"
)

// companyScopeInputs accepts both the new [{kind,value}] contract and the
// historical ["example.com","203.0.113.10"] contract.
type companyScopeInputs []db.ScopeInput

const maxCompanyMutationBodyBytes = 2 << 20

func decodeCompanyMutationRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxCompanyMutationBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

func (items *companyScopeInputs) UnmarshalJSON(data []byte) error {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil {
		return err
	}
	out := make([]db.ScopeInput, 0, len(rawItems))
	for i, raw := range rawItems {
		var legacy string
		if err := json.Unmarshal(raw, &legacy); err == nil {
			out = append(out, db.ScopeInput{Value: legacy})
			continue
		}
		var structured db.ScopeInput
		if err := json.Unmarshal(raw, &structured); err != nil {
			return fmt.Errorf("scope[%d] must be a string or {kind,value}", i)
		}
		out = append(out, structured)
	}
	*items = out
	return nil
}

// assetStore returns the asset store.
func (s *Server) assetStore() *db.AssetStore {
	if s.m.pg == nil {
		return nil
	}
	return s.m.pg.Assets()
}

// companyStore returns the company store.
func (s *Server) companyStore() *db.CompanyStore {
	if s.m.pg == nil {
		return nil
	}
	return s.m.pg.Companies()
}

// =====================================================================
// GET /api/companies
// =====================================================================

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).companies()
}

// =====================================================================
// POST /api/companies
// =====================================================================

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).createCompany()
}

// =====================================================================
// GET /api/companies/{id}
// =====================================================================

func (s *Server) getCompany(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).company()
}

// =====================================================================
// POST /api/companies/{id}/scope
// =====================================================================

func (s *Server) addCompanyScope(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).changeCompanyScope()
}

// =====================================================================
// DELETE /api/companies/{id}
// =====================================================================

func (s *Server) deleteCompany(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).deleteCompany()
}

// =====================================================================
// POST /api/companies/reattribute
// =====================================================================

func (s *Server) reattribute(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).reattribute()
}

// =====================================================================
// GET /api/assets
// =====================================================================

const (
	defaultAssetPageSize = 50
	maxAssetPageSize     = 200
)

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).assets()
}

// =====================================================================
// GET /api/assets/counts
// =====================================================================

func (s *Server) assetCounts(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).counts()
}

// =====================================================================
// DELETE /api/assets
// =====================================================================

func (s *Server) deleteAssets(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).deleteAssets()
}

// =====================================================================
// POST /api/assets
// =====================================================================

func (s *Server) insertAssets(w http.ResponseWriter, r *http.Request) {
	(assetHTTP{server: s, writer: w, request: r}).insertAssets()
}
