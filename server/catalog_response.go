package server

import "net/http"

// Catalog transports share completion while each endpoint supplies its original error policy.
func catalogResponse(w http.ResponseWriter, status int, value any, failure error, reject func(http.ResponseWriter, error)) {
	if failure != nil {
		reject(w, failure)
		return
	}
	writeJSON(w, status, value)
}
