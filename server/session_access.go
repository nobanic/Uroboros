package server

import (
	"net/http"
	"strings"
)

// Session admission borrows the existing credential extractor and JWT verifier.
// It leaves both the request and the admitted handler's lifetime untouched.
type sessionAccess struct {
	server *Server
	next   http.Handler
}

func (access sessionAccess) rejection(request *http.Request) string {
	if access.server.authDisabled {
		return ""
	}
	path := request.URL.Path
	if strings.HasPrefix(path, "/api/auth/") || path == "/api/health" {
		return ""
	}
	credential := extractToken(request)
	if credential == "" {
		return "Unauthorized"
	}
	if verifyJWT(credential, access.server.jwtKey) {
		return ""
	}
	return "token is invalid or expired"
}

func (access sessionAccess) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if message := access.rejection(request); message != "" {
		writeErr(writer, http.StatusUnauthorized, message)
		return
	}
	access.next.ServeHTTP(writer, request)
}
