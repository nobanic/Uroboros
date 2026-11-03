package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

const (
	// Enough for a repository landing/tree page plus several focused files, but
	// small enough to force fingerprinting before a worker spends its run reading
	// a moving branch as though it were the deployed application.
	maxUnboundSourceFetches = 5
	maxSourceBindingLength  = 800
)

type sourceProvenanceClass string

const (
	sourceUnversioned sourceProvenanceClass = "unversioned"
	sourceMutableRef  sourceProvenanceClass = "mutable_ref"
	sourcePinnedRef   sourceProvenanceClass = "immutable_ref"
	sourceMetadata    sourceProvenanceClass = "repository_metadata"
)

type sourceURLInfo struct {
	Repository string
	Ref        string
	Path       string
	Canonical  string
	Class      sourceProvenanceClass
}

func (s sourceURLInfo) recognized() bool { return s.Repository != "" }
func (s sourceURLInfo) unbound() bool {
	return s.Class == sourceUnversioned || s.Class == sourceMutableRef
}

type sourceBinding struct {
	TargetVersionEvidence  string `json:"target_version_evidence"`
	SourceRevisionEvidence string `json:"source_revision_evidence"`
	RuntimeValidation      string `json:"runtime_validation"`
}

func (b sourceBinding) status() string {
	present := 0
	for _, value := range []string{b.TargetVersionEvidence, b.SourceRevisionEvidence, b.RuntimeValidation} {
		if strings.TrimSpace(value) != "" {
			present++
		}
	}
	switch present {
	case 0:
		return "not_provided"
	case 3:
		return "declared_unverified"
	default:
		return "incomplete"
	}
}

type sourceFetchInput struct {
	URL           string        `json:"url"`
	SourceBinding sourceBinding `json:"source_binding"`
}

type sourceToolHistory interface {
	ExplorationToolCalls(explorationID int64, tool, excludeToolUseID string, limit int) ([]db.ExplorationToolCall, error)
}

type sourceFetchRecord struct {
	ID       string
	IntentID int64
	URL      string
	Info     sourceURLInfo
	Enforced bool
}

type sourceScopeState struct {
	Updated time.Time
	Calls   map[string]sourceFetchRecord
}

// sourceProvenanceGovernor is shared by every agent assembled by one server.
// Persisted activity supplies cross-worker/restart history for task runs; Live
// closes the small concurrency window between a tool_use event and its result and
// provides session-wide behavior for standalone chat agents.
type sourceProvenanceGovernor struct {
	history sourceToolHistory
	mu      sync.Mutex
	live    map[string]*sourceScopeState
	nextID  uint64
}

func newSourceProvenanceGovernor(history sourceToolHistory) *sourceProvenanceGovernor {
	return &sourceProvenanceGovernor{history: history, live: make(map[string]*sourceScopeState)}
}

func (g *sourceProvenanceGovernor) wrap(t actool.CoreTool, agentKey string, ri agent.RunInfo) actool.CoreTool {
	if g == nil || t == nil || t.Name() != "WebFetch" {
		return t
	}
	return newSourceProvenanceTool(t, g, agentKey, ri)
}

type sourceProvenanceTool struct {
	actool.CoreTool
	governor *sourceProvenanceGovernor
	agentKey string
	ri       agent.RunInfo
	desc     string
	schema   map[string]any
}

func newSourceProvenanceTool(t actool.CoreTool, governor *sourceProvenanceGovernor, agentKey string, ri agent.RunInfo) actool.CoreTool {
	desc := t.Description() + " Repository source URLs are provenance-classified. A moving/unversioned ref is lead-only and autonomous agents receive a small task-wide budget before they must use an exact tag/commit or return to target-runtime validation. Optional source_binding carries target_version_evidence, source_revision_evidence, and runtime_validation; it records context but never turns a mutable URL into immutable evidence."
	schema := cloneToolSchema(t.InputSchema())
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = make(map[string]any)
		schema["properties"] = properties
	}
	properties["source_binding"] = map[string]any{
		"type":        "object",
		"description": "Optional deployed-target binding context. All three fields are needed for a version-matched conclusion; the platform labels them declared/unverified and still requires an immutable source URL.",
		"properties": map[string]any{
			"target_version_evidence":  map[string]any{"type": "string", "description": "Target-observed version/build/hash evidence."},
			"source_revision_evidence": map[string]any{"type": "string", "description": "Exact matching tag/commit/snapshot evidence."},
			"runtime_validation":       map[string]any{"type": "string", "description": "Request/response or other validation against the deployed target."},
		},
	}
	return &sourceProvenanceTool{CoreTool: t, governor: governor, agentKey: agentKey, ri: ri, desc: desc, schema: schema}
}

func cloneToolSchema(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	if properties, ok := in["properties"].(map[string]any); ok {
		cloned := make(map[string]any, len(properties)+1)
		for key, value := range properties {
			cloned[key] = value
		}
		out["properties"] = cloned
	}
	return out
}

func (t *sourceProvenanceTool) Description() string         { return t.desc }
func (t *sourceProvenanceTool) InputSchema() map[string]any { return t.schema }

func (t *sourceProvenanceTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	var args sourceFetchInput
	if err := json.Unmarshal(input, &args); err != nil {
		return t.CoreTool.Call(ctx, input, tc)
	}
	info := classifySourceURL(args.URL)
	if !info.recognized() {
		return t.CoreTool.Call(ctx, input, tc)
	}
	if err := validateSourceBinding(args.SourceBinding); err != nil {
		return actool.Errorf("source_provenance_error=true: " + err.Error()), nil
	}

	toolUseID := ""
	if tc != nil {
		toolUseID = tc.ToolUseID
	}
	enforce := t.ri.IntentID > 0 || t.agentKey == "planner" || t.agentKey == "pentest"
	reservation, used, err := t.governor.reserve(t.ri, toolUseID, info, enforce)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}

	result, callErr := t.CoreTool.Call(ctx, input, tc)
	success := callErr == nil && !result.IsError
	t.governor.finish(reservation, success)
	if !success {
		return result, callErr
	}
	note := sourceProvenanceNote(info, args.SourceBinding, enforce, used)
	prefix := actool.Text(note + "\n\n")
	result.Content = append(prefix.Content, result.Content...)
	return result, callErr
}

func validateSourceBinding(binding sourceBinding) error {
	for name, value := range map[string]string{
		"target_version_evidence":  binding.TargetVersionEvidence,
		"source_revision_evidence": binding.SourceRevisionEvidence,
		"runtime_validation":       binding.RuntimeValidation,
	} {
		if len(strings.TrimSpace(value)) > maxSourceBindingLength {
			return fmt.Errorf("source_binding.%s must be at most %d characters", name, maxSourceBindingLength)
		}
	}
	return nil
}

func autonomousHistoryCall(call db.ExplorationToolCall) bool {
	return call.NodeID != nil || call.Worker == "planner" || call.Worker == "pentest"
}

func sourceScopeKey(ri agent.RunInfo) string {
	switch {
	case ri.ExplorationID > 0:
		return fmt.Sprintf("exploration:%d", ri.ExplorationID)
	case ri.TaskID > 0:
		return fmt.Sprintf("task:%d", ri.TaskID)
	case ri.SessionID != "":
		return "session:" + ri.SessionID
	default:
		return ""
	}
}

func (g *sourceProvenanceGovernor) persisted(ri agent.RunInfo, currentToolUseID string) []sourceFetchRecord {
	if g.history == nil || ri.ExplorationID <= 0 {
		return nil
	}
	calls, err := g.history.ExplorationToolCalls(ri.ExplorationID, "WebFetch", currentToolUseID, 2000)
	if err != nil {
		log.Printf("[source-provenance] read exploration %d history: %v", ri.ExplorationID, err)
		return nil // fail open; live reservations still prevent a concurrent stampede
	}
	out := make([]sourceFetchRecord, 0, len(calls))
	for index, call := range calls {
		var input sourceFetchInput
		if json.Unmarshal([]byte(call.Input), &input) != nil {
			continue
		}
		info := classifySourceURL(input.URL)
		if !info.recognized() {
			continue
		}
		intentID := int64(0)
		if call.NodeID != nil {
			intentID = *call.NodeID
		}
		id := call.ToolUseID
		if id == "" {
			id = fmt.Sprintf("history:%d:%s", index, info.Canonical)
		}
		out = append(out, sourceFetchRecord{
			ID: id, IntentID: intentID, URL: info.Canonical, Info: info, Enforced: autonomousHistoryCall(call),
		})
	}
	return out
}

// reserve atomically evaluates persisted + in-flight calls and reserves the
// current fetch before network I/O. used is the unbound-source count including
// the current allowed call, for the annotation returned to the model.
func (g *sourceProvenanceGovernor) reserve(ri agent.RunInfo, toolUseID string, info sourceURLInfo, enforce bool) (string, int, error) {
	history := g.persisted(ri, toolUseID)
	scope := sourceScopeKey(ri)
	if scope == "" {
		// Unknown attribution still gets an annotation, but cannot safely share a
		// budget with unrelated calls.
		return "", 0, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if len(g.live) > 2048 {
		for key, state := range g.live {
			if now.Sub(state.Updated) > 6*time.Hour {
				delete(g.live, key)
			}
		}
	}
	state := g.live[scope]
	if state == nil {
		state = &sourceScopeState{Calls: make(map[string]sourceFetchRecord)}
		g.live[scope] = state
	}
	state.Updated = now

	combined := make(map[string]sourceFetchRecord, len(history)+len(state.Calls))
	for _, call := range history {
		combined[call.ID] = call
	}
	for id, call := range state.Calls {
		combined[id] = call
	}

	priorUnbound := 0
	for _, call := range combined {
		if !call.Enforced || !strings.EqualFold(call.Info.Repository, info.Repository) {
			continue
		}
		if enforce && call.IntentID == ri.IntentID && call.URL == info.Canonical {
			return "", priorUnbound, fmt.Errorf("source_duplicate=true: this autonomous session already fetched %s; use the existing result or choose a materially different file/ref instead of spending another request", info.Canonical)
		}
		if call.Info.unbound() {
			priorUnbound++
		}
	}
	if enforce && info.unbound() && priorUnbound >= maxUnboundSourceFetches {
		return "", priorUnbound, fmt.Errorf("source_budget_exhausted=true: repository %s already used %d moving/unversioned source fetches in this task; fetch an exact release tag or commit URL, establish the deployed version/revision, or pivot to live-target contract/exploit validation. A default/current branch remains lead-only", info.Repository, priorUnbound)
	}

	g.nextID++
	reservation := toolUseID
	if reservation == "" {
		reservation = fmt.Sprintf("live:%d", g.nextID)
	}
	state.Calls[reservation] = sourceFetchRecord{
		ID: reservation, IntentID: ri.IntentID, URL: info.Canonical, Info: info, Enforced: enforce,
	}
	used := priorUnbound
	if enforce && info.unbound() {
		used++
	}
	return scope + "\x00" + reservation, used, nil
}

func (g *sourceProvenanceGovernor) finish(reservation string, success bool) {
	if g == nil || reservation == "" || success {
		return
	}
	parts := strings.SplitN(reservation, "\x00", 2)
	if len(parts) != 2 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if state := g.live[parts[0]]; state != nil {
		delete(state.Calls, parts[1])
		state.Updated = time.Now()
	}
}

func sourceProvenanceNote(info sourceURLInfo, binding sourceBinding, enforce bool, used int) string {
	admissibility := "metadata_only"
	switch info.Class {
	case sourceUnversioned, sourceMutableRef:
		admissibility = "lead_only"
	case sourcePinnedRef:
		admissibility = "external_reference_requires_target_binding"
	}
	payload := map[string]any{
		"class":          info.Class,
		"repository":     info.Repository,
		"requested_ref":  info.Ref,
		"source_path":    info.Path,
		"admissibility":  admissibility,
		"target_binding": binding.status(),
	}
	if enforce && info.unbound() {
		payload["unbound_budget"] = map[string]any{"used": used, "limit": maxUnboundSourceFetches, "scope": "task"}
	}
	encoded, _ := json.Marshal(payload)
	return "[UROBOROS_SOURCE_PROVENANCE] " + string(encoded) + "\nThis marker is machine-owned. Repository source can generate hypotheses, but it does not prove deployed-target behavior by itself; bind an exact revision to target evidence and validate the relevant behavior at runtime."
}

var (
	sourceCommitRef  = regexp.MustCompile(`(?i)^[0-9a-f]{7,64}$`)
	sourceVersionRef = regexp.MustCompile(`(?i)^v?\d+(?:\.\d+){1,3}(?:[-+][0-9a-z._-]+)?$`)
)

func classifySourceRef(ref, explicitKind string) sourceProvenanceClass {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "refs/heads/"))
	if ref == "" {
		return sourceUnversioned
	}
	if explicitKind == "tag" || explicitKind == "commit" || strings.HasPrefix(ref, "refs/tags/") {
		return sourcePinnedRef
	}
	if explicitKind == "branch" {
		return sourceMutableRef
	}
	lower := strings.ToLower(ref)
	switch lower {
	case "head", "main", "master", "trunk", "develop", "development", "default", "latest", "stable":
		return sourceMutableRef
	}
	if sourceCommitRef.MatchString(ref) || sourceVersionRef.MatchString(ref) {
		return sourcePinnedRef
	}
	// An arbitrary named ref may be a branch or a tag. Treating it as mutable is
	// conservative; an exact commit URL remains universally available.
	return sourceMutableRef
}

func canonicalSourceURL(parsed *url.URL) string {
	copy := *parsed
	copy.Scheme = strings.ToLower(copy.Scheme)
	copy.Host = strings.ToLower(copy.Host)
	copy.Fragment = ""
	return copy.String()
}

func sourceSegments(parsed *url.URL) []string {
	raw := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	out := make([]string, 0, len(raw))
	for _, segment := range raw {
		if segment == "" {
			continue
		}
		if decoded, err := url.PathUnescape(segment); err == nil {
			segment = decoded
		}
		out = append(out, segment)
	}
	return out
}

func repositoryKey(host string, parts ...string) string {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, strings.ToLower(host))
	for _, part := range parts {
		part = strings.TrimSuffix(strings.TrimSpace(part), ".git")
		if part != "" {
			clean = append(clean, strings.ToLower(part))
		}
	}
	if len(clean) < 3 {
		return ""
	}
	return strings.Join(clean, "/")
}

func sourceInfo(parsed *url.URL, repo, ref, path, kind string) sourceURLInfo {
	return sourceURLInfo{
		Repository: repo,
		Ref:        ref,
		Path:       path,
		Canonical:  canonicalSourceURL(parsed),
		Class:      classifySourceRef(ref, kind),
	}
}

// classifySourceURL recognizes source/version-control URL structure rather than
// product names. Unknown hosts and ordinary target/docs URLs are left untouched.
func classifySourceURL(raw string) sourceURLInfo {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return sourceURLInfo{}
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	segments := sourceSegments(parsed)
	if len(segments) == 0 {
		return sourceURLInfo{}
	}

	switch host {
	case "raw.githubusercontent.com":
		if len(segments) >= 4 {
			return sourceInfo(parsed, repositoryKey("github.com", segments[0], segments[1]), segments[2], strings.Join(segments[3:], "/"), "")
		}
	case "github.com":
		if len(segments) < 2 {
			return sourceURLInfo{}
		}
		repo := repositoryKey(host, segments[0], segments[1])
		if len(segments) == 2 {
			return sourceURLInfo{Repository: repo, Canonical: canonicalSourceURL(parsed), Class: sourceMetadata}
		}
		marker := strings.ToLower(segments[2])
		switch marker {
		case "blob", "tree", "raw", "commits":
			if len(segments) >= 4 {
				return sourceInfo(parsed, repo, segments[3], strings.Join(segments[4:], "/"), "")
			}
			if marker == "commits" {
				return sourceURLInfo{Repository: repo, Path: marker, Canonical: canonicalSourceURL(parsed), Class: sourceMetadata}
			}
		case "tags", "releases", "branches", "search", "issues", "pulls":
			return sourceURLInfo{Repository: repo, Path: strings.Join(segments[2:], "/"), Canonical: canonicalSourceURL(parsed), Class: sourceMetadata}
		}
	case "api.github.com":
		if len(segments) < 4 || strings.ToLower(segments[0]) != "repos" {
			return sourceURLInfo{}
		}
		repo := repositoryKey("github.com", segments[1], segments[2])
		marker := strings.ToLower(segments[3])
		switch marker {
		case "contents":
			ref := parsed.Query().Get("ref")
			return sourceInfo(parsed, repo, ref, strings.Join(segments[4:], "/"), "")
		case "git":
			if len(segments) >= 6 && (segments[4] == "blobs" || segments[4] == "trees") {
				return sourceInfo(parsed, repo, segments[5], strings.Join(segments[4:], "/"), "commit")
			}
		case "commits":
			if len(segments) >= 5 {
				return sourceInfo(parsed, repo, segments[4], strings.Join(segments[3:], "/"), "")
			}
		case "branches":
			if len(segments) >= 5 {
				return sourceInfo(parsed, repo, segments[4], strings.Join(segments[3:], "/"), "branch")
			}
		case "tags", "releases":
			return sourceURLInfo{Repository: repo, Path: strings.Join(segments[3:], "/"), Canonical: canonicalSourceURL(parsed), Class: sourceMetadata}
		}
	case "cdn.jsdelivr.net":
		if len(segments) >= 4 && strings.ToLower(segments[0]) == "gh" {
			projectAndRef := strings.SplitN(segments[2], "@", 2)
			if len(projectAndRef) == 2 {
				return sourceInfo(parsed, repositoryKey("github.com", segments[1], projectAndRef[0]), projectAndRef[1], strings.Join(segments[3:], "/"), "")
			}
		}
	case "bitbucket.org":
		if len(segments) >= 4 && strings.ToLower(segments[2]) == "src" {
			return sourceInfo(parsed, repositoryKey(host, segments[0], segments[1]), segments[3], strings.Join(segments[4:], "/"), "")
		}
	}

	if strings.Contains(host, "gitlab") {
		for dash := 2; dash < len(segments); dash++ {
			if segments[dash] != "-" || dash+2 >= len(segments) {
				continue
			}
			marker := strings.ToLower(segments[dash+1])
			if marker == "blob" || marker == "raw" || marker == "tree" || marker == "commits" {
				return sourceInfo(parsed, repositoryKey(host, segments[:dash]...), segments[dash+2], strings.Join(segments[dash+3:], "/"), "")
			}
		}
	}

	// Common Gitea/Forgejo layout: /owner/repo/src/{branch|tag|commit}/{ref}/path.
	// Limit the fallback to forge-like hosts so an ordinary target route named
	// /src/branch is never mistaken for external version control.
	if looksLikeForgeHost(host) && len(segments) >= 6 && strings.ToLower(segments[2]) == "src" {
		kind := strings.ToLower(segments[3])
		if kind == "branch" || kind == "tag" || kind == "commit" {
			return sourceInfo(parsed, repositoryKey(host, segments[0], segments[1]), segments[4], strings.Join(segments[5:], "/"), kind)
		}
	}
	// Conservative generic forge fallback for /owner/repo/{blob|raw|tree}/ref/path.
	if looksLikeForgeHost(host) && len(segments) >= 4 {
		marker := strings.ToLower(segments[2])
		if marker == "blob" || marker == "raw" || marker == "tree" {
			return sourceInfo(parsed, repositoryKey(host, segments[0], segments[1]), segments[3], strings.Join(segments[4:], "/"), "")
		}
	}
	return sourceURLInfo{}
}

func looksLikeForgeHost(host string) bool {
	for _, marker := range []string{"git", "code", "forge", "scm", "source"} {
		if strings.Contains(host, marker) {
			return true
		}
	}
	return false
}
