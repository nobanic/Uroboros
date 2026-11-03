package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"

	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/db"
)

// validSkillName checks the agentskills.io name constraints, widened so a skill can
// also be named in Chinese (or any other script): ASCII must stay lowercase
// alphanumeric + hyphens, while non-ASCII letters/digits are accepted as-is.
// 1-64 runes, must start with a letter, no leading/trailing/consecutive hyphens.
// The name doubles as a directory name under skillDir, so nothing that could carry a
// path (separators, dots, spaces, control characters) is allowed through.
func validSkillName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	rs := []rune(name)
	if len(rs) > 64 {
		return false
	}
	isLetter := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r > unicode.MaxASCII && unicode.IsLetter(r))
	}
	if !isLetter(rs[0]) || rs[len(rs)-1] == '-' {
		return false
	}
	for _, r := range rs {
		switch {
		case isLetter(r), r >= '0' && r <= '9', r == '-':
		case r > unicode.MaxASCII && unicode.IsDigit(r):
		default:
			return false
		}
	}
	return !strings.Contains(name, "--")
}

// reAgentKey mirrors the agents.key DB check: lowercase letter start, then
// lowercase letters / digits / underscores.
var reAgentKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// pgReady returns the PG handle, or writes 503 and returns nil if unavailable.
func (s *Server) pg(w http.ResponseWriter) *db.DB {
	if s.m.pg == nil {
		writeErr(w, 503, "Admin backend data source (PostgreSQL) is not connected")
		return nil
	}
	return s.m.pg
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// ---------- tasks (delete) ----------

const taskDeleteDrainTimeout = 10 * time.Second

func canonicalTaskID(raw string) (string, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

// beginTaskDelete serializes the delete barrier with pause, resume, admission,
// and FIFO reconciliation. Every lifecycle path rechecks deleting after taking
// concMu, so none can commit a contradictory state once this returns.
func (s *Server) beginTaskDelete(taskID string) bool {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	if s.engine.IsDeleting(taskID) {
		return false
	}
	return s.engine.BeginDelete(taskID)
}

// abortTaskDelete restores the execution barrier from the committed task row,
// not from the in-memory state observed before deletion began. Keeping the task
// paused on an unreadable row is conservative: a later explicit resume can
// safely recover it without allowing work to escape an uncertain delete.
func (s *Server) abortTaskDelete(taskID string) {
	(taskDeleteControl{server: s, taskID: taskID}).abort()
}

func (s *Server) pgDeleteTask(w http.ResponseWriter, r *http.Request) {
	(taskDeleteHTTP{server: s, writer: w, request: r}).run()
}

func writeCommittedTaskDelete(w http.ResponseWriter, result DeleteTaskResult, err error) {
	result.CleanupWarning = err.Error()
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) waitTaskQuiescent(ctx context.Context, taskID string) error {
	return (taskDeleteControl{server: s, taskID: taskID}).await(ctx)
}

// ---------- agents ----------

func (s *Server) pgListAgents(w http.ResponseWriter, r *http.Request) {
	(capabilityAgentRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).list().write(w)
}

// pgCreateAgent creates a CUSTOM conversational agent (builtin=false, role
// 'assistant') and seeds it a starter prompt so its editor isn't blank.
func (s *Server) pgCreateAgent(w http.ResponseWriter, r *http.Request) {
	(capabilityAgentRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).create().write(w)
}

// pgUpdateAgent updates a custom agent's name/description (built-in agents rejected).
func (s *Server) pgUpdateAgent(w http.ResponseWriter, r *http.Request) {
	(capabilityAgentRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).update().write(w)
}

// pgDeleteAgent removes a custom agent (built-in rejected). Prompts/vars/visibility
// cascade via FK; tool-binding cleanup is best-effort.
func (s *Server) pgDeleteAgent(w http.ResponseWriter, r *http.Request) {
	(capabilityAgentRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).remove().write(w)
}

func (s *Server) agentByKey(w http.ResponseWriter, r *http.Request) (*db.DB, *db.Agent, bool) {
	pg := s.pg(w)
	if pg == nil {
		return nil, nil, false
	}
	a, err := pg.GetAgentByKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, nil, false
	}
	if a == nil {
		writeErr(w, 404, "agent not found")
		return nil, nil, false
	}
	return pg, a, true
}

// pgSaveAgentConfig updates an agent's runtime config (currently max_turns) and
// re-applies the live LLM so the change takes effect immediately.
func (s *Server) pgSaveAgentConfig(w http.ResponseWriter, r *http.Request) {
	agentAdministrationInput(s, w, r, (*agentAdministration).configure)
}

func (s *Server) pgGetAgent(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).detail)
}

func (s *Server) pgSavePrompt(w http.ResponseWriter, r *http.Request) {
	agentAdministrationInput(s, w, r, (*agentAdministration).savePrompt)
}

// pgResetPrompt restores an agent's prompt body to the in-code built-in default
// (section [A]). Only built-in agents have a code default; custom agents have none.
func (s *Server) pgResetPrompt(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).resetPrompt)
}

// pgSaveWrapup stores an agent's wrap-up (settlement) prompt — the text injected on
// timeout / step-exhaustion. Empty body clears the override → built-in default.
func (s *Server) pgSaveWrapup(w http.ResponseWriter, r *http.Request) {
	agentAdministrationInput(s, w, r, (*agentAdministration).saveWrapup)
}

// pgResetWrapup clears an agent's wrap-up prompt override so the code built-in
// default is used again.
func (s *Server) pgResetWrapup(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).resetWrapup)
}

// pgSaveTaskTimeoutWrapup stores an agent's TASK-TIMEOUT wrap-up prompt + turn budget
// (worker/planner only). Empty prompt / 0 turns clear the override → built-in default.
func (s *Server) pgSaveTaskTimeoutWrapup(w http.ResponseWriter, r *http.Request) {
	agentAdministrationInput(s, w, r, (*agentAdministration).saveDeadlineWrapup)
}

// pgResetTaskTimeoutWrapup clears the task-timeout wrap-up override → built-in default.
func (s *Server) pgResetTaskTimeoutWrapup(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).resetDeadlineWrapup)
}

func (s *Server) pgListPromptVersions(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).versions)
}

func (s *Server) pgPromptVars(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).variables)
}

func (s *Server) pgPreviewPrompt(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, func(owner *agentAdministration) (any, error) {
		var input agentPreviewInput
		// Preview intentionally accepts partially decoded input, retaining the original best-effort contract.
		_ = decode(r, &input)
		return owner.preview(input)
	})
}

func (s *Server) pgGetAgentVisibility(w http.ResponseWriter, r *http.Request) {
	s.runAgentAdministration(w, r, (*agentAdministration).visibility)
}

func (s *Server) pgSetAgentVisibility(w http.ResponseWriter, r *http.Request) {
	agentAdministrationInput(s, w, r, (*agentAdministration).saveVisibility)
}

// ---------- tools (built-in tool catalog) ----------

// toolRuntimeFamily turns low-level callable contracts into operator-facing
// capability families. The catalog rows remain the source of truth; this value
// is response-only metadata, so adding a new family never requires a migration.
func toolRuntimeFamily(key string, system bool) string {
	if !system {
		return "Custom"
	}
	switch {
	case strings.HasPrefix(key, "katana_"):
		return "Katana"
	case strings.HasPrefix(key, "arsenal_"), key == "arjun_discover", key == "waf_fingerprint", key == "archive_discover":
		return "Arsenal"
	case strings.HasPrefix(key, "traffic_"), key == "http_request", key == "artifact_transform":
		return "Traffic & evidence"
	case strings.Contains(key, "finding"), strings.Contains(key, "retest"), strings.Contains(key, "experiment"):
		return "Finding lifecycle"
	case key == "discovery_review", key == "record_discovery_decision":
		return "Discovery governance"
	case key == "list_tasks", key == "list_llm_profiles", key == "spawn_task", key == "pause_task",
		key == "get_task_graph", key == "list_task_findings", key == "add_task_hint",
		key == "get_worker_trace", key == "list_worker_traces", key == "search_all_worker_traces",
		key == "get_task_node_detail":
		return "Orchestration"
	case key == "delete_assets_by_host", key == "create_skill", key == "update_skill_file",
		key == "create_custom_tool", key == "update_custom_tool", key == "create_mcp", key == "update_mcp":
		return "Platform"
	default:
		return "Agent core"
	}
}

func (s *Server) pgListTools(w http.ResponseWriter, r *http.Request) {
	(capabilityToolRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).list().write(w)
}

// pgUpdateTool saves the page-editable fields of a built-in tool: description,
// parameter schema (structure is expected unchanged — only per-param description/
// default move), agent binding, and enabled. key is taken from the path and never
// changes (it is welded to the Go handler).
func (s *Server) pgUpdateTool(w http.ResponseWriter, r *http.Request) {
	(capabilityToolRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).update().write(w)
}

// pgResetTool overwrites a tool row with its code-defined defaults (description,
// schema, agent binding) and re-enables it — the explicit "restore defaults" action, since
// startup seeding is first-insert-only and never overwrites edits.
func (s *Server) pgResetTool(w http.ResponseWriter, r *http.Request) {
	(capabilityToolRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).reset().write(w)
}

// ---------- mcp ----------

func (s *Server) pgListMCP(w http.ResponseWriter, r *http.Request) {
	(capabilityMCPRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).list().write(w)
}

func (s *Server) pgSaveMCP(w http.ResponseWriter, r *http.Request) {
	(capabilityMCPRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).save().write(w)
}

func (s *Server) pgDeleteMCP(w http.ResponseWriter, r *http.Request) {
	(capabilityMCPRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).remove().write(w)
}

// pgRefreshMCP re-discovers one MCP's tools on demand and re-caches them, so a
// config change or an earlier discovery failure can be fixed without a restart.
func (s *Server) pgRefreshMCP(w http.ResponseWriter, r *http.Request) {
	(capabilityMCPRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).refresh()
}

// pgMCPTools returns one MCP's cached tools (name + description) for the detail UI.
func (s *Server) pgMCPTools(w http.ResponseWriter, r *http.Request) {
	(capabilityMCPRegistry{exchange: capabilityRequest{server: s, writer: w, request: r}}).tools().write(w)
}

// ---------- skills (filesystem) ----------

type skillFileNode struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	License       string   `json:"license,omitempty"`
	Compatibility string   `json:"compatibility,omitempty"`
	MCPs          []string `json:"mcps,omitempty"`
	Files         []string `json:"files"`
	// Usage ledger (db/skill_usage.go). Calls is 0 and LastUsed nil for a skill that
	// was never invoked — the ledger only has rows for skills agents actually loaded.
	Calls    int        `json:"calls"`
	Tasks    int        `json:"tasks"`
	Agents   []string   `json:"usage_agents"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

func (s *Server) fsListSkills(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).library.list())
}

// fsSkillUsage returns one skill's recent invocations, newest first (detail panel).
func (s *Server) fsSkillUsage(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	writeSkillReply(w, s.exchangeSkills(r).usage(pg))
}

// fsMissingSkills lists skill names agents asked for that do not exist — the gap
// list, i.e. which procedures are worth writing next.
func (s *Server) fsMissingSkills(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	writeSkillReply(w, s.exchangeSkills(r).missing(pg))
}

// cleanStrs trims each string and drops empties.
func cleanStrs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) fsCreateSkill(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).create())
}

// fsUpdateSkillMeta rewrites the SKILL.md frontmatter fields that are safe to
// change without touching the instruction body: mcps, license, compatibility,
// description. Only fields present in the request body are updated; omitted
// fields are left as-is (the whole frontmatter is reconstructed from the
// parsed values, so the write is a clean replace, not a line-patch).
func (s *Server) fsUpdateSkillMeta(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).edit())
}

// zip-upload safety caps (defeat zip bombs / runaway archives).
const (
	maxSkillZipBytes   = 20 << 20  // 20MB compressed request body
	maxSkillTotalBytes = 100 << 20 // 100MB total uncompressed
	maxSkillFileBytes  = 20 << 20  // 20MB per extracted file
	maxSkillEntries    = 4000      // max files in the archive
)

// skillNameFromFrontmatter extracts the `name:` value from a SKILL.md's YAML
// frontmatter (the block between the first two `---` lines). "" if absent.
func skillNameFromFrontmatter(md []byte) string {
	lines := strings.Split(string(md), "\n")
	inFM := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "---" {
			if !inFM {
				inFM = true
				continue
			}
			break // end of frontmatter
		}
		if inFM && strings.HasPrefix(t, "name:") {
			v := strings.TrimSpace(strings.TrimPrefix(t, "name:"))
			return strings.Trim(v, `"'`) // quoted name: values are also accepted
		}
	}
	return ""
}

// fsUploadSkill installs a skill from an uploaded .zip. The archive must contain a
// SKILL.md (at the root or under a single top-level dir); the skill name is taken
// from that file's `name:` frontmatter (falling back to the top dir / zip name).
// Zip-slip is defeated by validating every entry path with skillRelPath, and a set
// of size/count caps guard against zip bombs. POST ?overwrite=true replaces an
// existing skill of the same name.
func (s *Server) fsUploadSkill(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).upload(w))
}

// readZipEntry reads a single entry's bytes (capped). Takes the *zip.File rather than
// a name because zr.Open rejects names that aren't valid UTF-8 (GBK-named archives).
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxSkillFileBytes))
}

func (s *Server) fsDeleteSkill(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	writeSkillReply(w, s.exchangeSkills(r).deleteSkill(pg))
}

// skillPathBlocked lists the ASCII characters a skill-relative path may not contain.
// '\' is a Windows separator; '%' keeps double-URL-encoding tricks from surviving
// (Go's net/http URL-decodes PathValue once — %2F→/ %2e→. — so an attacker sending
// %252e%252e arrives here as the literal "%2e%2e" and is rejected on the '%'); '#'
// and '?' would truncate the path when it travels back through a URL; the rest are
// reserved on Windows filesystems.
const skillPathBlocked = `\%#?*:"<>|`

// skillPathRune reports whether r may appear in a client-supplied skill path.
// It is a blacklist over Unicode rather than an ASCII whitelist so that Chinese (and any
// other script) file names work, while everything that makes path validation hard is
// still refused: control/format characters, look-alike whitespace, separators.
func skillPathRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r == utf8.RuneError:
		return false // NUL & control characters, invalid UTF-8
	case strings.ContainsRune(skillPathBlocked, r):
		return false
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
		return false // zero-width joiners, bidi overrides (RLO filename spoofing), private use
	case r != ' ' && unicode.IsSpace(r):
		return false // NBSP / full-width space and the like: they look like spaces but are not
	}
	return true
}

// maxSkillPathLen caps a relative path so a pathological name can't reach the syscall.
const maxSkillPathLen = 512

// skillRelPath validates a relative file path supplied by the client.
// Returns the cleaned path and an empty error string on success.
// Validation order matters: character check first (before Clean) so encoding tricks
// can't survive normalization.
func skillRelPath(file string) (string, string) {
	// 1. Character check — before normalization. Defeats null bytes, backslash,
	//    '%', control characters and unicode look-alikes; CJK names pass through.
	if file == "" || len(file) > maxSkillPathLen {
		return "", "invalid path: empty or too long"
	}
	if !utf8.ValidString(file) {
		return "", "invalid path: not valid UTF-8"
	}
	for _, r := range file {
		if !skillPathRune(r) {
			return "", "invalid path: illegal character " + strconv.QuoteRune(r)
		}
	}
	// 2. Reject ".." explicitly. With the whitelist above, encoding bypass is already
	//    impossible, but we keep this check so the intent is obvious to reviewers.
	if strings.Contains(file, "..") {
		return "", "invalid path: '..' not allowed"
	}
	// 3. Reject leading slash and empty segments (double slash).
	//    Leading slash would survive filepath.Clean as an absolute path.
	if strings.HasPrefix(file, "/") || strings.Contains(file, "//") {
		return "", "invalid path: must be relative with no empty segments"
	}
	// 4. Normalize and final safety re-check after Clean.
	//    filepath.Clean removes redundant separators and resolves single dots.
	clean := filepath.Clean(file)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", "invalid path"
	}
	return clean, ""
}

// walkSkillFiles returns all files under root (relative to root), sorted,
// including files in subdirectories (scripts/, references/, assets/, etc.).
// walkSkillFiles returns all entries under root relative to root.
// Directories are included with a trailing "/" so the frontend can distinguish
// them from files and render empty folders in the tree.
func walkSkillFiles(root string) ([]string, error) {
	var entries []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			entries = append(entries, rel+"/")
		} else {
			entries = append(entries, rel)
		}
		return nil
	})
	return entries, err
}

func (s *Server) fsListFiles(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).listFiles())
}

func (s *Server) fsReadFile(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).read())
}

func (s *Server) fsWriteFile(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).write())
}

func (s *Server) fsCreateDir(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).directory())
}

func (s *Server) fsDeletePath(w http.ResponseWriter, r *http.Request) {
	writeSkillReply(w, s.exchangeSkills(r).deletePath())
}

// ---------- skill visibility ----------

func (s *Server) pgSkillVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	agents, err := pg.SkillAgents(r.PathValue("name"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"agents": idStrings(agents)})
}

func (s *Server) pgToggleSkillVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		AgentID   int64  `json:"agent_id,string"`
		SkillName string `json:"skill_name"`
		Visible   bool   `json:"visible"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.ToggleSkillVisibility(body.AgentID, body.SkillName, body.Visible); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- visibility (MCP resource side + toggle) ----------

func (s *Server) pgResourceVisibility(w http.ResponseWriter, r *http.Request) {
	(capabilityVisibility{exchange: capabilityRequest{server: s, writer: w, request: r}}).agents().write(w)
}

func (s *Server) pgToggleVisibility(w http.ResponseWriter, r *http.Request) {
	(capabilityVisibility{exchange: capabilityRequest{server: s, writer: w, request: r}}).toggle().write(w)
}

// ---------- llm profiles ----------

func (s *Server) pgListProfiles(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).profiles()
}

func (s *Server) pgSaveProfile(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).saveProfile()
}

// pgGetLLMRetryPolicy returns the global retry policy (counts + intervals for each of the five layers). Never configured -> all zeros,
// the frontend displays zero as 'default'.
func (s *Server) pgGetLLMRetryPolicy(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).retryPolicy(false)
}

// pgSaveLLMRetryPolicy saves the global retry policy. The three 'follow the endpoint' layers (connect/empty-response/same-
// provider safety window) are the provider's build or call parameters; after changing them, the cached provider must be provider
// rebuilt; circuit-breaker parameters are pushed directly to the process-level Registry.
func (s *Server) pgSaveLLMRetryPolicy(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).retryPolicy(true)
}

func (s *Server) pgDeleteProfile(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).deleteProfile()
}

func (s *Server) restoreTasksAfterProfileDelete(pg *db.DB) {
	for _, task := range s.m.List() {
		if taskID, err := strconv.ParseInt(task.ID, 10, 64); err == nil {
			if pt, err := pg.GetTask(taskID); err == nil && pt != nil {
				s.syncTaskLLMState(pt)
				// Deleting the active entry may advance the task to a ready successor,
				// or clear the explicit chain and restore its Agent/global fallback.
				// Resume only intents that were blocked by the exhausted chain; a
				// paused task remains paused and terminal tasks remain immutable.
				if !isTerminalStatus(task.lifecycleSnapshot().Status) && s.taskRuntimeAvailable(task, "planner", "worker") {
					if _, reopenErr := task.Store.ReopenIntentsByBlockedReason(db.IntentBlockedLLMQuota); reopenErr != nil {
						log.Printf("[llm-profile] task %s reopen quota-blocked intents after profile delete: %v", task.ID, reopenErr)
					}
				}
				task.Notify()
			}
		}
	}
}

func (s *Server) pgActivateProfile(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).activateProfile()
}

// pgLLMPoolStatus reports the failover ("round-robin") switches, the resolved chain order
// and every profile's circuit-breaker state — what the LLM page renders as the
// "round-robin order" strip and the per-card health badges.
func (s *Server) pgLLMPoolStatus(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).pool(false)
}

// pgLLMPoolReset clears a tripped profile's circuit breaker so the next call
// tries it again immediately ("restore now"). id=0 clears every profile.
func (s *Server) pgLLMPoolReset(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).pool(true)
}

// pgListModels fetches available models from the provider's API endpoint.
// Supports OpenAI-format (GET /models) and Anthropic-format (GET /v1/models); for
// Anthropic-compatible third parties (e.g. DeepSeek) whose model list lives only on
// the OpenAI path, it falls back to the OpenAI endpoint at the stripped root.
func (s *Server) pgListModels(w http.ResponseWriter, r *http.Request) {
	s.llmAdmin(w, r).models()
}

// --- prompt template helpers (Go text/template + catalog allowlist) ---

// globalPromptVars are runtime variables available to EVERY agent (built-in and
// custom) regardless of its per-agent catalog. Each agent's render path fills them
// (see agent.nowStr, rendered fresh each turn), so a prompt may always reference
// {{.Now}} — e.g. subtract it from a fixed start stamp to reason about elapsed time.
var globalPromptVars = []db.PromptVar{
	{Name: "Now", Description: "the server's current time (refreshed live each run; subtract a fixed start time to gauge elapsed duration)", Example: "2026-08-11 14:30:00 CST", Source: "runtime"},
	{Name: "DataDir", Description: "the server's data root directory (the root of all task/session artifacts; each agent writes to a subdirectory under it, e.g. <DataDir>/<taskID>)", Example: "/app/data", Source: "runtime"},
}

// withGlobalVars appends the universal runtime vars onto an agent's own catalog,
// so validation / the UI variable list / preview all recognize {{.Now}} etc.
// A stored catalog entry that collides with a global name is dropped: the global
// runtime var is authoritative (it's what the render path actually resolves), and
// this keeps the returned list name-unique so the UI never sees duplicate keys.
func withGlobalVars(vars []db.PromptVar) []db.PromptVar {
	globalNames := make(map[string]bool, len(globalPromptVars))
	for _, g := range globalPromptVars {
		globalNames[g.Name] = true
	}
	out := make([]db.PromptVar, 0, len(vars)+len(globalPromptVars))
	for _, v := range vars {
		if globalNames[v.Name] {
			continue // shadowed by the authoritative global runtime var
		}
		out = append(out, v)
	}
	return append(out, globalPromptVars...)
}

// validateTemplate parses the template and rejects any {{.Var}} not in the catalog.
// Returns "" if valid, otherwise an error message.
func validateTemplate(tmpl string, catalog []db.PromptVar) string {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "template syntax error: " + err.Error()
	}
	allowed := map[string]bool{}
	for _, v := range catalog {
		allowed[v.Name] = true
	}
	for _, name := range templateFields(t) {
		if !allowed[name] {
			return "variable {{." + name + "}} is not in this agent's allow list"
		}
	}
	return ""
}

// renderPrompt renders with example values (catalog example overridden by sample).
func renderPrompt(tmpl string, catalog []db.PromptVar, sample map[string]string) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	data := map[string]any{}
	for _, v := range catalog {
		data[v.Name] = v.Example
	}
	for k, val := range sample {
		data[k] = val
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// templateFields returns the distinct top-level {{.X}} field names referenced by
// the template (used to validate against the catalog whitelist).
func templateFields(t *template.Template) []string {
	seen := map[string]bool{}
	var out []string
	collect := func(p *parse.PipeNode) {
		if p == nil {
			return
		}
		for _, cmd := range p.Cmds {
			for _, arg := range cmd.Args {
				if f, ok := arg.(*parse.FieldNode); ok && len(f.Ident) > 0 && !seen[f.Ident[0]] {
					seen[f.Ident[0]] = true
					out = append(out, f.Ident[0])
				}
			}
		}
	}
	var walk func(n parse.Node)
	walk = func(n parse.Node) {
		switch x := n.(type) {
		case *parse.ListNode:
			if x == nil {
				return
			}
			for _, c := range x.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			collect(x.Pipe)
		case *parse.IfNode:
			collect(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.RangeNode:
			collect(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.WithNode:
			collect(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		}
	}
	if t.Tree != nil {
		walk(t.Tree.Root)
	}
	return out
}
