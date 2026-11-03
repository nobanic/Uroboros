package server

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey    = "auth.password_hash"
	jwtTTL         = 7 * 24 * time.Hour
	keyChars       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// the lower bound matches the setup page's front-end validation -- validation only on the front end is no validation at all, since hitting the API directly
	// bypasses it. The upper bound is bcrypt's hard limit: beyond 72 bytes GenerateFromPassword returns
	// ErrPasswordTooLong, so blocking it early beats giving the user a cryptic 'password encryption failed'.
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// errDataSourceUnavailable is the unified reply when password-related read operations fail. These handlers must never
// treat 'cannot read' as 'not set': authInit once, for this reason, let requests through on a database error, allowing unauthenticated requests to overwrite
// an existing admin password.
const errDataSourceUnavailable = "The data source is temporarily unavailable; please try again later"

// validatePassword returns an empty string to indicate success; otherwise it returns a reason that can be shown directly to the user.
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("Password must be at least %d characters", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("Password must not exceed %d bytes", maxPasswordBytes)
	}
	return ""
}

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy := filepath.Join(dataDir, jwtKeyFilename); legacy != path {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if data, rerr := os.ReadFile(legacy); rerr == nil {
				if werr := os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf("[auth] JWT key migrated from %s to %s (moved out of the browsable workspace)", legacy, path)
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	buf := make([]byte, 32)
	for i := range buf {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err != nil {
			return nil, fmt.Errorf("generate jwt key: %w", err)
		}
		buf[i] = keyChars[n.Int64()]
	}
	if err := os.WriteFile(path, buf, 0600); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf("[auth] new JWT key written to %s", path)
	return buf, nil
}

// signJWT issues a 7-day HS256 token for user Uroboros.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "Uroboros",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// extractToken prefers explicit credentials over a host-wide legacy cookie.
// EventSource supplies ?token= because it cannot set an Authorization header;
// a cookie from another local installation must not replace that credential.
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}
	if c, err := r.Cookie("uroboros_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return sessionAccess{server: s, next: h}
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// a read failure must return 503, not initialized:false: when initialized:false the frontend sends the user
// to /setup to set a password (login/page.tsx); wrapping a database failure as 200 pushes the user toward
// overwriting an existing password.
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	(passwordSession{server: s, writer: w, request: r}).serve(passwordStatus)
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	(passwordSession{server: s, writer: w, request: r}).serve(passwordInitialize)
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	(passwordSession{server: s, writer: w, request: r}).serve(passwordChange)
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	(passwordSession{server: s, writer: w, request: r}).serve(passwordLogin)
}
