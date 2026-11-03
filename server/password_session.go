package server

import (
	"net/http"

	"github.com/nobanic/UroborOS/db"
	"golang.org/x/crypto/bcrypt"
)

type passwordAction uint8

const (
	passwordStatus passwordAction = iota
	passwordInitialize
	passwordLogin
	passwordChange
)

// Each action retains its original read/decode/validation/write ordering. Only
// identical admission, stored-hash, bcrypt, persistence and response steps share
// an owner. Initialization authority stays with INSERT ON CONFLICT DO NOTHING.
type passwordSession struct {
	server   *Server
	writer   http.ResponseWriter
	request  *http.Request
	database *db.DB
}

func (session passwordSession) reject(status int, message string) bool {
	writeErr(session.writer, status, message)
	return false
}

func (session passwordSession) serve(action passwordAction) {
	if session.server.authDisabled {
		if action == passwordStatus {
			writeJSON(session.writer, http.StatusOK, map[string]any{"initialized": false, "disabled": true})
		} else {
			session.reject(http.StatusForbidden, "Authentication is disabled for this local runtime")
		}
		return
	}
	session.database = session.server.pg(session.writer)
	if session.database == nil {
		return
	}
	switch action {
	case passwordStatus:
		if hash, ready := session.storedPassword(false); ready {
			writeJSON(session.writer, http.StatusOK, map[string]any{"initialized": hash != ""})
		}
	case passwordInitialize:
		session.initialize()
	case passwordLogin:
		session.login()
	case passwordChange:
		session.change()
	}
}

func (session passwordSession) storedPassword(required bool) (string, bool) {
	hash, present, failure := session.database.GetSetting(authPassKey)
	if failure != nil {
		return "", session.reject(http.StatusServiceUnavailable, errDataSourceUnavailable)
	}
	if required && (!present || hash == "") {
		return "", session.reject(http.StatusForbidden, "Password not initialized; please set a password first")
	}
	return hash, true
}

func (session passwordSession) acceptPassword(password string) bool {
	if message := validatePassword(password); message != "" {
		return session.reject(http.StatusBadRequest, message)
	}
	return true
}

func (session passwordSession) hashPassword(password string) (string, bool) {
	hash, failure := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if failure != nil {
		return "", session.reject(http.StatusInternalServerError, "Password encryption failed")
	}
	return string(hash), true
}

func (session passwordSession) matchesPassword(hash, password, message string) bool {
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return session.reject(http.StatusUnauthorized, message)
	}
	return true
}

func (session passwordSession) persist(hash string, initial bool) bool {
	stored := true
	var failure error
	if initial {
		stored, failure = session.database.InsertSettingIfAbsent(authPassKey, hash)
	} else {
		failure = session.database.SetSetting(authPassKey, hash)
	}
	if failure != nil {
		return session.reject(http.StatusInternalServerError, "Save failed: "+failure.Error())
	}
	if !stored {
		return session.reject(http.StatusForbidden, "Password already set")
	}
	return true
}

func (session passwordSession) issueSession() {
	token, failure := signJWT(session.server.jwtKey)
	if failure != nil {
		session.reject(http.StatusInternalServerError, "token generation failed")
		return
	}
	writeJSON(session.writer, http.StatusOK, map[string]any{"token": token})
}

func (session passwordSession) initialize() {
	existing, ready := session.storedPassword(false)
	if !ready {
		return
	}
	if existing != "" {
		session.reject(http.StatusForbidden, "Password already set")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if failure := decode(session.request, &input); failure != nil || input.Password == "" {
		session.reject(http.StatusBadRequest, "Password cannot be empty")
		return
	}
	if !session.acceptPassword(input.Password) {
		return
	}
	hash, ready := session.hashPassword(input.Password)
	if ready && session.persist(hash, true) {
		session.issueSession()
	}
}

func (session passwordSession) login() {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decode(session.request, &input) != nil {
		session.reject(http.StatusBadRequest, "Malformed request")
		return
	}
	if input.Username != "Uroboros" {
		session.reject(http.StatusUnauthorized, "Incorrect username or password")
		return
	}
	hash, ready := session.storedPassword(true)
	if ready && session.matchesPassword(hash, input.Password, "Incorrect username or password") {
		session.issueSession()
	}
}

func (session passwordSession) change() {
	if !verifyJWT(extractToken(session.request), session.server.jwtKey) {
		session.reject(http.StatusUnauthorized, "Unauthorized")
		return
	}
	var input struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if decode(session.request, &input) != nil {
		session.reject(http.StatusBadRequest, "Malformed request")
		return
	}
	if input.NewPassword == "" {
		session.reject(http.StatusBadRequest, "New password cannot be empty")
		return
	}
	if !session.acceptPassword(input.NewPassword) {
		return
	}
	existing, ready := session.storedPassword(true)
	if !ready || !session.matchesPassword(existing, input.OldPassword, "Current password is incorrect") {
		return
	}
	hash, ready := session.hashPassword(input.NewPassword)
	if ready && session.persist(hash, false) {
		writeJSON(session.writer, http.StatusOK, map[string]any{"ok": true})
	}
}
