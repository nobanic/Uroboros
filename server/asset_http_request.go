package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/nobanic/UroborOS/db"
)

// Borrowed request ownership; stores, authentication and manager lifetimes stay
// with the existing server. Optional company-delete JSON has its own EOF policy.
type assetHTTP struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

func (read assetHTTP) companyStore() *db.CompanyStore {
	store := read.server.companyStore()
	if store == nil {
		writeErr(read.writer, 503, "database unavailable")
	}
	return store
}

func (read assetHTTP) assetStore() *db.AssetStore {
	store := read.server.assetStore()
	if store == nil {
		writeErr(read.writer, 503, "database unavailable")
	}
	return store
}

func (read assetHTTP) complete(value any, err error) {
	if err != nil {
		read.fail(err)
		return
	}
	writeJSON(read.writer, 200, value)
}

func (read assetHTTP) fail(err error) { writeErr(read.writer, 500, err.Error()) }

func (read assetHTTP) companyID(message string) (int64, bool) {
	id, err := strconv.ParseInt(read.request.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(read.writer, 400, message)
		return 0, false
	}
	return id, true
}

func (read assetHTTP) assetInput(value any) bool {
	if err := json.NewDecoder(read.request.Body).Decode(value); err != nil {
		writeErr(read.writer, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func (read assetHTTP) optionalCompanyDelete(value any) bool {
	decoder := json.NewDecoder(read.request.Body)
	if err := decoder.Decode(value); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeErr(read.writer, 400, "invalid JSON: "+err.Error())
		return false
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err == nil {
		writeErr(read.writer, 400, "invalid JSON: multiple values")
	} else {
		writeErr(read.writer, 400, "invalid JSON: "+err.Error())
	}
	return false
}
