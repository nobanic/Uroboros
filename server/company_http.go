package server

import (
	"errors"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

func (read assetHTTP) companies() {
	store := read.companyStore()
	if store == nil {
		return
	}
	read.complete(store.ListCompanies())
}

func (read assetHTTP) createCompany() {
	store := read.companyStore()
	if store == nil {
		return
	}
	var input companyCreateHTTPInput
	if !decodeCompanyMutationRequest(read.writer, read.request, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeErr(read.writer, 400, "name required")
		return
	}
	if err := db.ValidateCompanyScopeInputBounds(input.Scope); err != nil {
		writeErr(read.writer, 400, err.Error())
		return
	}
	id, added, skipped, invalid, problems, err := store.CreateCompanyWithScope(input.Name, input.Logo, input.Scope, "api")
	if err != nil {
		if errors.Is(err, db.ErrCompanyNameConflict) {
			writeErr(read.writer, 409, "company name already exists")
			return
		}
		read.companyMutationFailure(err)
		return
	}
	result := map[string]any{"id": id, "created": true, "scope_added": added, "scope_skipped": skipped, "scope_invalid": invalid}
	if len(problems) != 0 {
		result["scope_errors"] = problems
	}
	writeJSON(read.writer, 201, result)
}

func (read assetHTTP) company() {
	store := read.companyStore()
	if store == nil {
		return
	}
	id, ok := read.companyID("invalid id")
	if !ok {
		return
	}
	company, err := store.GetCompany(id)
	if err != nil {
		read.fail(err)
		return
	}
	if company == nil {
		writeErr(read.writer, 404, "company not found")
		return
	}
	scope, err := store.GetScope(id)
	read.complete(map[string]any{"company": company, "scope": scope}, err)
}

func (read assetHTTP) companyMutationFailure(err error) {
	var validation *db.CompanyScopeValidationError
	if errors.As(err, &validation) {
		writeErr(read.writer, 400, validation.Error())
		return
	}
	read.fail(err)
}

func (read assetHTTP) changeCompanyScope() {
	store := read.companyStore()
	if store == nil {
		return
	}
	id, ok := read.companyID("invalid company id")
	if !ok {
		return
	}
	var input companyScopeHTTPInput
	if !decodeCompanyMutationRequest(read.writer, read.request, &input) {
		return
	}
	if err := db.ValidateCompanyScopeInputBounds(input.Scope); err != nil {
		writeErr(read.writer, 400, err.Error())
		return
	}
	var added, skipped, invalid int
	var problems []string
	var err error
	if input.Reset {
		added, invalid, problems, err = store.UpdateScopeInputsChecked(id, input.Scope, input.Reason)
	} else {
		added, skipped, invalid, problems, err = store.AddScopeInputsChecked(id, input.Scope, input.Reason)
	}
	if err != nil {
		if errors.Is(err, db.ErrCompanyNotFound) {
			writeErr(read.writer, 404, "company not found")
			return
		}
		read.companyMutationFailure(err)
		return
	}
	result := map[string]any{"added": added, "skipped": skipped, "invalid": invalid}
	if len(problems) != 0 {
		result["errors"] = problems
	}
	// A warning read cannot turn an already committed mutation into a failure.
	if warning, err := store.MalformedIPAssetWarning(); err == nil && warning != "" {
		result["warnings"] = []string{warning}
	}
	read.complete(result, nil)
}

func (read assetHTTP) deleteCompany() {
	if read.companyStore() == nil {
		return
	}
	id, ok := read.companyID("invalid id")
	if !ok {
		return
	}
	var input companyDeleteHTTPInput
	if !read.optionalCompanyDelete(&input) {
		return
	}
	deleted, err := read.server.m.DeleteCompanyWithAssets(id, input.DeleteAssets)
	if err != nil {
		if errors.Is(err, db.ErrCompanyNotFound) {
			writeErr(read.writer, 404, "company not found")
			return
		}
		read.fail(err)
		return
	}
	read.complete(map[string]any{"deleted": 1, "assets_deleted": deleted}, nil)
}

func (read assetHTTP) reattribute() {
	store := read.companyStore()
	if store == nil {
		return
	}
	read.complete(map[string]any{"ok": true}, store.RecomputeAttribution())
}
