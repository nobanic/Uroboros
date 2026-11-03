package stackupdate

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (c *Controller) githubToken() string {
	var saved struct {
		Token string `json:"token"`
	}
	if readJSON(c.path("github-token.json"), &saved) == nil {
		return saved.Token
	}
	return os.Getenv("UROBOROS_UPDATE_GITHUB_TOKEN")
}

func (c *Controller) credentials(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if status := c.status(); status.Running || status.RecoveryRequired {
		respond(w, 409, map[string]string{"error": "wait for the current update or recovery before changing GitHub access"})
		return
	}
	var request struct {
		Token *string `json:"token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Token == nil || decoder.Decode(new(any)) != io.EOF {
		respond(w, 400, map[string]string{"error": "invalid GitHub access request"})
		return
	}
	token := strings.TrimSpace(*request.Token)
	if len(token) > 4096 || strings.IndexFunc(token, func(value rune) bool { return value < 33 || value > 126 }) >= 0 {
		respond(w, 400, map[string]string{"error": "enter one GitHub token without whitespace"})
		return
	}
	if err := writeJSON(c.path("github-token.json"), map[string]string{"token": token}); err != nil {
		respond(w, 500, map[string]string{"error": "could not save the private GitHub credential"})
		return
	}
	c.latest = ""
	c.checked = time.Time{}
	respond(w, 200, map[string]bool{"configured": token != ""})
}
