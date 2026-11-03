package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/sidequestion"
	"github.com/nobanic/norma/llm"
)

type sideRun struct {
	key    string
	parent sidequestion.Parent
	cancel context.CancelFunc
	done   chan struct{}
}
type sideQuestionState struct {
	mu           sync.Mutex
	commands     sync.Mutex // short admission / clear operations only, never inference
	pending      map[string]sidequestion.Snapshot
	latest       map[string]sidequestion.Snapshot
	seen         map[string][2]int64
	runs         map[string]sideRun
	done         chan struct{}
	outputTokens int
}

func sideModel(cfg agent.Config, id int64, name string) sidequestion.Model {
	b, _ := json.Marshal([]any{cfg.Format, cfg.BaseURL, cfg.Model, cfg.ThinkingType, cfg.ReasoningEffort, cfg.Stream, cfg.MaxTokensField, cfg.SessionHeaderKey})
	h := sha256.Sum256(b)
	return sidequestion.Model{ProfileID: id, Name: name, Format: string(cfg.Format), Model: cfg.Model, Identity: hex.EncodeToString(h[:]), Streaming: cfg.Stream, WindowTokens: cfg.CompactionWindow()}
}

func bindSideProvider(p llm.Provider, cfg agent.Config, id int64, name string) llm.Provider {
	return sidequestion.Bind(p, sideModel(cfg, id, name))
}

func (s *Server) initSideQuestions() { (sideQuestionLifecycle{server: s}).initialize() }

func (s *Server) flushSideSnapshots() { (sideQuestionLifecycle{server: s}).flush() }

func (s *Server) sideSnapshot(ctx context.Context, p sidequestion.Parent) (*sidequestion.Snapshot, error) {
	return (sideQuestionLifecycle{server: s}).snapshot(ctx, p)
}

func (s *Server) cancelSideWhere(match func(sidequestion.Parent) bool) []<-chan struct{} {
	return (sideQuestionLifecycle{server: s}).cancel(match)
}

// Called after the task admission barrier closes and the main loops drain.
// Wait for final answer/usage writes before taking the archive snapshot.
func (s *Server) drainTaskSideQuestions(ctx context.Context, taskID string) error {
	return (sideQuestionLifecycle{server: s}).drainTask(ctx, taskID)
}

func (s *Server) cancelWorkerSide(taskID string, expID, iid int64) {
	(sideQuestionLifecycle{server: s}).cancelWorker(taskID, expID, iid)
}

func (s *Server) sideProvider(model sidequestion.Model) (llm.Provider, error) {
	return (sideProviderSelection{server: s, model: model}).resolve()
}

func (s *Server) sideParent(w http.ResponseWriter, r *http.Request, kind string) (sidequestion.Parent, bool) {
	return (sideQuestionHTTP{server: s, writer: w, request: r}).parent(kind)
}

func (s *Server) registerSideRoutes(mux *http.ServeMux) {
	for _, route := range []struct{ path, kind string }{{"/api/conversations/{id}", "conversation"}, {"/api/tasks/{id}/chat", "main"}, {"/api/tasks/{id}/intents/{iid}", "worker"}} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			mux.HandleFunc(method+" "+route.path+"/side-questions", func(w http.ResponseWriter, r *http.Request) {
				p, ok := s.sideParent(w, r, route.kind)
				if ok {
					s.handleSideQuestions(w, r, p)
				}
			})
		}
	}
	mux.HandleFunc("GET /api/side-questions/{requestID}/events", s.sideEvents)
	mux.HandleFunc("POST /api/side-questions/{requestID}/cancel", s.cancelSideRequest)
}

func (s *Server) handleSideQuestions(w http.ResponseWriter, r *http.Request, p sidequestion.Parent) {
	(sideQuestionHTTP{server: s, writer: w, request: r}).questions(p)
}

func (s *Server) runSide(ctx context.Context, cancel context.CancelFunc, e sidequestion.Exchange, parent sidequestion.Parent, provider llm.Provider, snapshot sidequestion.Snapshot, agentQuestion string) {
	(&sideQuestionJob{server: s, ctx: ctx, cancel: cancel, exchange: e, parent: parent, provider: provider, snapshot: snapshot, question: agentQuestion}).execute()
}

func (s *Server) cancelSideRequest(w http.ResponseWriter, r *http.Request) {
	(sideQuestionHTTP{server: s, writer: w, request: r}).cancel().write(w)
}

func (s *Server) sideEvents(w http.ResponseWriter, r *http.Request) {
	(sideQuestionHTTP{server: s, writer: w, request: r}).events()
}
