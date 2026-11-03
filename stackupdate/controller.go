// Package stackupdate owns source-based updates of one clean Docker installation.
// Only its separate controller has access to Docker; agents and the application do not.
package stackupdate

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const Repository = "nobanic/Uroboros"
const Branch = "main"
const repositoryURL = "https://github.com/" + Repository + ".git"
const stateRoot = "/state"
const installRoot = "/install"

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,55}$`)

type Config struct {
	Project, Token, Revision, Seed, Port string
}

func ConfigFromEnvironment() (Config, error) {
	c := Config{Project: os.Getenv("UROBOROS_UPDATE_PROJECT"), Token: os.Getenv("UROBOROS_UPDATE_TOKEN"),
		Revision: os.Getenv("UROBOROS_SOURCE_REVISION"), Seed: os.Getenv("UROBOROS_UPDATE_SEED"), Port: os.Getenv("UROBOROS_PORT")}
	if !projectPattern.MatchString(c.Project) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(c.Token) || !revisionPattern.MatchString(c.Revision) {
		return c, errors.New("invalid private updater configuration")
	}
	if c.Seed == "" {
		c.Seed = c.Revision
	}
	if !revisionPattern.MatchString(c.Seed) {
		return c, errors.New("invalid installation source revision")
	}
	if c.Port == "" {
		c.Port = "8805"
	}
	return c, nil
}

type Status struct {
	RecoveryRequired bool      `json:"recovery_required,omitempty"`
	FailedAt         string    `json:"failed_at,omitempty"`
	Phase            string    `json:"phase"`
	Message          string    `json:"message"`
	Error            string    `json:"error,omitempty"`
	Running          bool      `json:"running"`
	Current          string    `json:"current_revision"`
	Target           string    `json:"target_revision,omitempty"`
	Job              string    `json:"job,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Active struct {
	Compose  string `json:"compose"`
	Revision string `json:"revision"`
	Seed     string `json:"seed_revision"`
	Source   string `json:"source"`
}

// Controller exposes a deliberately small authenticated protocol, not a Docker
// API or command execution endpoint. The repository, branch and operations are fixed.
type Controller struct {
	Config         Config
	root           string
	run            executor
	mu             sync.Mutex
	latest         string
	checked        time.Time
	sourceURL      string // dependency injection for isolated tests; never configured by HTTP or environment
	healthyTimeout time.Duration
}

func New(c Config) *Controller {
	return &Controller{Config: c, root: stateRoot, run: commandExecutor{}, sourceURL: repositoryURL}
}

func (c *Controller) path(parts ...string) string {
	return filepath.Join(append([]string{c.root}, parts...)...)
}

func (c *Controller) current() string {
	var active Active
	if readJSON(c.path("active.json"), &active) == nil && active.Seed == c.Config.Seed && revisionPattern.MatchString(active.Revision) {
		return active.Revision
	}
	return c.Config.Revision
}

func (c *Controller) status() Status {
	var status Status
	if readJSON(c.path("status.json"), &status) != nil {
		status = Status{Phase: "idle", Message: "Ready to check for source updates."}
	}
	status.Current = c.current()
	return status
}

func (c *Controller) writeStatus(status Status) error {
	status.UpdatedAt = time.Now().UTC()
	if err := writeJSON(c.path("status.json"), status); err != nil {
		return err
	}
	if err := os.MkdirAll(c.path("public"), 0755); err != nil {
		return err
	}
	if err := os.Chmod(c.path("public"), 0755); err != nil {
		return err
	}
	return writeJSONMode(c.path("public", "status.json"), status, 0644)
}

func (c *Controller) latestRevision(ctx context.Context, force bool) (string, error) {
	if !force && c.latest != "" && time.Since(c.checked) < 2*time.Minute {
		return c.latest, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := c.command(ctx, "git", []string{"ls-remote", "--exit-code", c.sourceURL, "refs/heads/" + Branch}, commandOptions{})
	if err != nil {
		return "", fmt.Errorf("cannot read %s/%s: %w", Repository, Branch, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || !revisionPattern.MatchString(fields[0]) || fields[1] != "refs/heads/"+Branch {
		return "", errors.New("Git returned an invalid branch revision")
	}
	c.latest, c.checked = fields[0], time.Now()
	return c.latest, nil
}

func (c *Controller) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]bool{"ok": true}) })
	api := http.NewServeMux()
	api.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, c.status()) })
	api.HandleFunc("GET /check", c.check)
	api.HandleFunc("POST /apply", c.apply)
	api.HandleFunc("POST /credentials", c.credentials)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if c.Config.Token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(c.Config.Token)) != 1 {
			respond(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		api.ServeHTTP(w, r)
	}))
	return mux
}

func (c *Controller) check(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	latest, err := c.latestRevision(r.Context(), r.URL.Query().Get("force") == "1")
	status := c.status()
	out := map[string]any{"mode": "docker-source", "repo": Repository, "branch": Branch, "current_revision": c.current(), "comparable": true,
		"has_backup": false, "status": status, "asset_available": true}
	if current := r.URL.Query().Get("current"); revisionPattern.MatchString(current) {
		out["current_revision"] = current
	}
	if err != nil {
		out["error"] = err.Error()
	} else {
		out["latest_revision"], out["latest"] = latest, latest[:12]
		out["has_update"] = out["current_revision"] != latest
		out["html_url"] = "https://github.com/" + Repository + "/commit/" + latest
	}
	out["github_token_configured"] = c.githubToken() != ""
	respond(w, 200, out)
}

func (c *Controller) apply(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var request struct {
		Target string `json:"target"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || !revisionPattern.MatchString(request.Target) {
		respond(w, 400, map[string]string{"error": "select a valid source revision first"})
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		respond(w, 400, map[string]string{"error": "invalid update request"})
		return
	}
	if request.Target == c.current() {
		respond(w, 409, map[string]string{"error": "this source revision is already installed"})
		return
	}
	latest, err := c.latestRevision(r.Context(), true)
	if err != nil {
		respond(w, 502, map[string]string{"error": err.Error()})
		return
	}
	if request.Target != latest {
		respond(w, 409, map[string]string{"error": "main changed since the check; check for updates again"})
		return
	}
	if err := os.Mkdir(c.path("job.lock"), 0700); err != nil {
		respond(w, 409, map[string]string{"error": "another update is running or needs recovery"})
		return
	}
	job := c.Config.Project + "-update-" + time.Now().UTC().Format("20060102-150405.000000000")
	status := Status{Phase: "queued", Message: "Preparing the selected source revision.", Running: true, Current: c.current(), Target: request.Target, Job: job}
	if err := c.writeStatus(status); err != nil {
		_ = os.Remove(c.path("job.lock"))
		respond(w, 500, map[string]string{"error": "cannot persist update status"})
		return
	}
	if err := c.launch(r.Context(), job); err != nil {
		status.Phase, status.Message, status.Error, status.Running = "failed", "Could not start the update worker.", err.Error(), false
		_ = c.writeStatus(status)
		_ = os.Remove(c.path("job.lock"))
		respond(w, 502, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 202, map[string]any{"ok": true, "target": request.Target})
}

func (c *Controller) Serve(ctx context.Context) error {
	if err := os.MkdirAll(c.root, 0700); err != nil {
		return err
	}
	if err := c.initialize(ctx); err != nil {
		return err
	}
	server := &http.Server{Addr: ":8790", Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	return server.ListenAndServe()
}

func respond(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func readJSON(path string, body any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(body)
}

func writeJSON(path string, body any) error {
	return writeJSONMode(path, body, 0600)
}

func writeJSONMode(path string, body any, mode os.FileMode) error {
	b, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type commandOptions struct {
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
}
type executor interface {
	execute(context.Context, string, []string, commandOptions) ([]byte, error)
}
type commandExecutor struct{}

func (commandExecutor) execute(ctx context.Context, name string, args []string, opts commandOptions) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdin = opts.Dir, opts.Stdin
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"HOME=/tmp", "DOCKER_CONFIG=/tmp/uroboros-docker", "BUILDX_CONFIG=/tmp/uroboros-buildx")
	cmd.Env = append(cmd.Env, opts.Env...)
	var output limitedBuffer
	cmd.Stderr = &output
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	} else {
		cmd.Stdout = &output
	}
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %s", name, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

type limitedBuffer struct {
	mu    sync.Mutex
	bytes []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if left := (16 << 20) - len(b.bytes); left > 0 {
		if left < len(p) {
			p = p[:left]
		}
		b.bytes = append(b.bytes, p...)
	}
	return n, nil
}
func (b *limitedBuffer) String() string { return string(b.bytes) }
func (b *limitedBuffer) Bytes() []byte  { return b.bytes }

func (c *Controller) command(ctx context.Context, name string, args []string, opts commandOptions) ([]byte, error) {
	if name == "git" {
		if token := c.githubToken(); token != "" {
			opts.Env = append(opts.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http."+repositoryURL+".extraheader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
		}
	}
	b, err := c.run.execute(ctx, name, args, opts)
	if err != nil {
		message := err.Error()
		if token := c.githubToken(); token != "" {
			message = strings.ReplaceAll(message, token, "[redacted]")
			message = strings.ReplaceAll(message, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)), "[redacted]")
		}
		for _, name := range []string{"POSTGRES_PASSWORD", "ARSENAL_TOKEN", "API_LAB_TOKEN", "UROBOROS_UPDATE_TOKEN", "UROBOROS_UPDATE_GITHUB_TOKEN"} {
			if secret := os.Getenv(name); secret != "" {
				message = strings.ReplaceAll(message, secret, "[redacted]")
			}
		}
		if len(message) > 1600 {
			message = message[:300] + "\n[earlier output omitted]\n" + message[len(message)-1250:]
		}
		return nil, errors.New(message)
	}
	return b, nil
}
