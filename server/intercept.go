package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/guard"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/UroborOS/llmrec"
	"github.com/nobanic/norma/llm"
)

// judgeWorkerLane is the usage-ledger "worker" label for intercept fallback-judge
// calls, so judge spend can be queried apart from the worker/planner/main lanes.
const judgeWorkerLane = "judge"

// chatGuard returns a guard wired with the manager's interceptor, used for chat
// conversations. Called once per applyLLM so a new LLM config always gets a fresh guard.
func (s *Server) chatGuard() *guard.Guard {
	return guard.NewWithInterceptor(s.m.interceptor)
}

// wireInterceptReviewer installs the LLM fallback judge into the interceptor. The
// judge runs only on tool calls that matched no rule (see intercept.Judge). It
// resolves the configured judge profile (0 → active/default), builds a provider,
// runs a one-shot JSON classification with an explanation for every verdict.
func (s *Server) wireInterceptReviewer() {
	s.m.interceptor.SetReviewer(func(ctx context.Context, profileID int64, prompt string, input intercept.ReviewInput) (intercept.Decision, error) {
		if profileID == 0 {
			if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
				profileID = p.ID
			}
		}
		if profileID == 0 {
			return intercept.Decision{}, fmt.Errorf("no available judge model configured")
		}
		prov, _, ok := s.providerForProfile(profileID)
		if !ok {
			return intercept.Decision{ProfileID: profileID}, fmt.Errorf("judge model profile %d is unavailable", profileID)
		}
		// Tag this call's usage as the "judge" lane so the config page can report
		// how much the fallback approval has spent, separate from model profiles.
		ctx = llmrec.WithWorker(ctx, judgeWorkerLane)
		text, err := reviewCompletion(ctx, prov, prompt, input)
		if err != nil {
			return intercept.Decision{ProfileID: profileID}, err
		}
		v := intercept.ParseVerdict(text)
		if v.Action == "" {
			return intercept.Decision{ProfileID: profileID}, fmt.Errorf("invalid model verdict format: it must include the verdict, the actual action, the consequence on success, and the matched rule")
		}
		return intercept.Decision{Action: v.Action, Message: v.Reason, ProfileID: profileID}, nil
	})
}

func reviewCompletion(ctx context.Context, prov llm.Provider, prompt string, input intercept.ReviewInput) (string, error) {
	user, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return streamCollectText(ctx, prov, prompt, string(user))
}

// streamCollectText runs a single non-streaming-style completion (thinking off,
// low temperature, bounded output) and returns the concatenated text. The
// budget includes the explanation and complete closing JSON delimiters.
func streamCollectText(ctx context.Context, prov llm.Provider, system, user string) (string, error) {
	temp := 0.0
	req := llm.CompletionRequest{
		System:      []string{system},
		Messages:    []llm.Message{llm.UserText(user)},
		MaxTokens:   1024,
		Temperature: &temp,
		Thinking:    "disabled",
	}
	var sb strings.Builder
	for ev, err := range prov.Stream(ctx, req) {
		if err != nil {
			return "", err
		}
		if ev.Type == llm.SETextDelta {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String(), nil
}

// --- intercept rule CRUD ---

func (s *Server) interceptListRules(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).rules().write(w)
}

func (s *Server) interceptCreateRule(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).createRule().write(w)
}

func (s *Server) interceptUpdateRule(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).updateRule().write(w)
}

func (s *Server) interceptDeleteRule(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).deleteRule().write(w)
}

func (s *Server) interceptToggleRule(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).toggleRule().write(w)
}

// --- pending (ask) ---

func (s *Server) interceptListPending(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).pending().write(w)
}

func (s *Server) interceptGetOne(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).one().write(w)
}

func (s *Server) interceptListTaskItems(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).taskItems().write(w)
}

func (s *Server) interceptHistory(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).history().write(w)
}

func interceptFilterParams(q url.Values) (db.InterceptApprovalFilter, error) {
	filter := db.InterceptApprovalFilter{Status: q.Get("status"), DecisionSource: q.Get("decision_source")}
	switch filter.Status {
	case "", "pending", "allowed", "denied", "timeout":
	default:
		return filter, fmt.Errorf("status must be pending, allowed, denied, or timeout")
	}
	switch filter.DecisionSource {
	case "", "model", "rule", "unknown":
	default:
		return filter, fmt.Errorf("decision_source must be model, rule, or unknown")
	}
	return filter, nil
}

func interceptPageParams(q url.Values) (int, int) {
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 20)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func (s *Server) interceptDecide(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).decide().write(w)
}

// --- tool-config (global tool interception scope) ---

// interceptGetToolConfig returns the list of tool names that are currently
// configured to enter the intercept rule system.
func (s *Server) interceptGetToolConfig(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).tools().write(w)
}

// interceptSetToolConfig replaces the list of tool names that should enter
// the intercept rule system.
func (s *Server) interceptSetToolConfig(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).setTools().write(w)
}

// --- LLM fallback judge config (global model fallback) ---

// interceptGetJudgeConfig returns the resolved judge configuration. Prompt is the
// effective prompt (built-in template when unset), so the UI can prefill it.
func (s *Server) interceptGetJudgeConfig(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).judge().write(w)
}

// interceptSetJudgeConfig persists the judge configuration.
func (s *Server) interceptSetJudgeConfig(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).setJudge().write(w)
}

// interceptJudgeUsage returns the fallback judge's cumulative token spend plus a
// recent daily series, for the config page. ?days bounds the daily series (default 30).
func (s *Server) interceptJudgeUsage(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).usage().write(w)
}

// --- helpers ---

type interceptRuleReq struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Priority       int    `json:"priority"`
	MatchTarget    string `json:"match_target"`
	MatchType      string `json:"match_type"`
	Pattern        string `json:"pattern"`
	Action         string `json:"action"`
	Message        string `json:"message"`
	TimeoutEnabled bool   `json:"timeout_enabled"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	TimeoutAction  string `json:"timeout_action"`
}

func validateInterceptRuleReq(req interceptRuleReq) error {
	if req.Name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	switch req.MatchTarget {
	case "tool_name", "tool_input":
	default:
		return fmt.Errorf("match_target must be tool_name or tool_input")
	}
	switch req.MatchType {
	case "string", "regex":
	default:
		return fmt.Errorf("match_type must be string or regex")
	}
	if req.Pattern == "" {
		return fmt.Errorf("pattern cannot be empty")
	}
	switch req.Action {
	case "allow", "deny", "ask":
	default:
		return fmt.Errorf("action must be allow, deny, or ask")
	}
	if req.MatchType == "regex" {
		if _, err := regexp.Compile(req.Pattern); err != nil {
			return fmt.Errorf("pattern is not a valid regular expression: %w", err)
		}
	}
	return nil
}

func (s *Server) interceptDetail(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).detail().write(w)
}

// The navigation endpoint returns only the original call and its paired result.
func (s *Server) interceptExecution(w http.ResponseWriter, r *http.Request) {
	(interceptHTTP{server: s, writer: w, request: r}).execution().write(w)
}
