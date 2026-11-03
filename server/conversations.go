package server

import (
	"context"
	"errors"

	"net/http"

	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

const (
	maxConversationRequestBytes  = 64 << 10
	maxConversationAgentKeyRunes = 120
	maxConversationTitleRunes    = 200
)

func decodeConversationRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxConversationRequestBytes)
	if err := decode(r, value); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return false
	}
	return true
}

// ---------- conversations (chat page) ----------
//
// A conversation is a ChatGPT-style thread bound to an agent key, independent of
// the pentest task graph. Turns run on ChatAgent; steps persist to
// conversation_activities and the browser POLLS ?since=cursor for live updates
// (no per-conversation SSE broadcaster needed).

type conversationListItem struct {
	*db.Conversation
	Running bool `json:"running"`
}

func (s *Server) pgListConversations(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).list().write(w)
}

func (s *Server) pgCreateConversation(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).create().write(w)
}

func (s *Server) pgUpdateConversation(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).profile().write(w)
}

// convByID resolves the {id} path value to a conversation (404 if absent).
func (s *Server) convByID(w http.ResponseWriter, r *http.Request) (*db.DB, *db.Conversation, bool) {
	return (conversationEndpoint{server: s, writer: w, request: r}).resolve()
}

func (s *Server) pgRenameConversation(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).rename().write(w)
}

func (s *Server) pgDeleteConversation(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).remove().write(w)
}

const maxConversationDeleteBatch = 100

type conversationDeleteItem struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) cancelConversation(id int64) {
	busyKey := s.convBusyKey(id)
	s.chatMu.Lock()
	cancel := s.chatCancel[busyKey]
	s.chatMu.Unlock()
	if cancel != nil {
		cancel(agent.AbortChatStoppedByUser)
	}
}

// pgDeleteConversationsBatch deletes up to 100 selected conversations in one
// database statement. Missing ids are reported per item so a stale list does not
// hide what was actually removed.
func (s *Server) pgDeleteConversationsBatch(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).removeBatch().write(w)
}

func (s *Server) convBusyKey(id int64) string { return "conv-" + strconv.FormatInt(id, 10) }

// pgConversationMessages returns steps after ?since=cursor plus whether a turn is
// still running (so the client knows to keep polling). Same item shape as the task
// activity stream, so the frontend transcript renderer is reused verbatim.
func (s *Server) pgConversationMessages(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).messages().write(w)
}

// pgConversationMsgDetail lazily returns one step's full detail blob.
func (s *Server) pgConversationMsgDetail(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).detail().write(w)
}

// pgStopConversation aborts the in-flight run for one conversation (manual stop —
// the run/stop button in the chat UI). It cancels only THIS session's agent run;
// the P3 trigger queue is untouched, so the agent's next queued fire still starts.
func (s *Server) pgStopConversation(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).stop().write(w)
}

// pgSendConversationMessage persists the human turn, then runs the agent in the
// background (steps stream to conversation_activities, polled by the client). One
// in-flight turn per conversation.
func (s *Server) pgSendConversationMessage(w http.ResponseWriter, r *http.Request) {
	(conversationEndpoint{server: s, writer: w, request: r}).send().write(w)
}

// runConversation runs ONE agent turn on a conversation in a background goroutine
// (each conversation is independent → parallel). Steps stream to
// conversation_activities. Shared by the chat HTTP handler and the P3 scheduler.
// busyKey clears when the run ends (best-effort in-flight marker).
func (s *Server) runConversation(c *db.Conversation, msg, busyKey string, userMessage ...string) {
	(&conversationTurn{server: s, conversation: c, message: msg, key: busyKey}).background(userMessage)
}

// runConversationSync runs ONE agent turn on a conversation and BLOCKS until the
// run ends (clearing busyKey). runConversation wraps it in a goroutine for the
// fire-and-forget chat path; the P3 trigger queue calls it directly so it can wait
// for completion before starting the next queued fire for the same agent.
func (s *Server) runConversationSync(c *db.Conversation, msg, busyKey string) {
	(&conversationTurn{server: s, conversation: c, message: msg, key: busyKey}).foreground()
}

// Register cancellation before returning 202, so an immediate stop/delete cannot
// miss a background goroutine which has not started yet.
func (s *Server) conversationRunContext(id int64, busyKey string) (context.Context, context.CancelCauseFunc) {
	return (conversationRegistration{server: s, id: id, key: busyKey}).open()
}

func (s *Server) runConversationTurn(ctx context.Context, cancel context.CancelCauseFunc, c *db.Conversation, msg, busyKey string) {
	(&conversationTurn{server: s, ctx: ctx, cancel: cancel, conversation: c, message: msg, key: busyKey}).execute()
}

// triggerBehavior is an agent's cached P3 trigger post-processing strategy (see the
// agents table trigger_* columns). Read once per fire in StartTriggeredRun so the
// pump never touches the DB while holding queueMu.
type triggerBehavior struct {
	runMode     string // serial | parallel
	mergeMode   string // by_task | all | none (used in serial; parallel ignores it, each fire gets its own conversation)
	maxParallel int    // per-agent concurrency cap for parallel; <=0 = unlimited
}

// readTriggerBehavior loads an agent's strategy, falling back to safe defaults
// (serial / all / 5) on any error or unknown enum value.
func (s *Server) readTriggerBehavior(agentKey string) triggerBehavior {
	return (agentTriggerQueue{server: s, key: agentKey}).strategy()
}

// StartTriggeredRun enqueues a P3 trigger fire for agentKey and pumps the queue. The
// agent's strategy decides concurrency + merge: serial → run one at a time (optionally
// merging by task / all / none); parallel → run each fire in its own concurrent
// conversation up to trigger_max_parallel. Distinct agents always run concurrently.
func (s *Server) StartTriggeredRun(agentKey, title, message string, taskID int64, mergeable bool, taskDesc, taskGoal string) {
	(agentTriggerQueue{server: s, key: agentKey}).enqueue(triggeredRun{agentKey: agentKey, title: title, message: message, taskID: taskID, taskDesc: taskDesc, taskGoal: taskGoal, mergeable: mergeable})
}

// pumpLocked launches queued fires for one agent up to its concurrency limit, then
// returns. Serial → limit 1; parallel → limit = maxParallel (<=0 → unlimited). Each
// launched run re-pumps on completion (runAndPump) to fill the freed slot. Caller
// holds queueMu.
func (s *Server) pumpLocked(agentKey string) { (agentTriggerQueue{server: s, key: agentKey}).refill() }

// runAndPump runs one fire to completion, then decrements the active counter and
// re-pumps to fill the freed slot (cleaning up the agent's maps when fully idle).
func (s *Server) runAndPump(agentKey string, item triggeredRun) {
	(agentTriggerQueue{server: s, key: agentKey}).run(item)
}

// nextTriggerRun pops the next run from the agent's queue per the merge mode:
//
//	parallel / none → one head item, no merge
//	by_task         → head + all queued same-task mergeable fires merged into one
//	all             → the ENTIRE queue merged into one run (regardless of task/type)
//
// Caller holds queueMu.
func (s *Server) nextTriggerRun(agentKey string, cfg triggerBehavior) triggeredRun {
	return (agentTriggerQueue{server: s, key: agentKey}).take(cfg)
}

// taskContextHeader renders a task's description/goal once. Same-task fires share
// this block, so the scheduler no longer repeats it per event (a long task goal
// times N fires was the dominant bloat). Returns "" for interval/none triggers
// (taskID==0, no task context). desc/goal are truncated to keep even a single copy
// bounded.
func taskContextHeader(taskID int64, desc, goal string) string {
	return triggerTaskContext(taskID, desc, goal)
}

// finalTriggerMessage renders the message actually sent to the agent for a single
// (non-merged) fire: the task-context header (written once) followed by the event
// body. Merged runs embed their per-task headers inline and clear taskDesc/taskGoal,
// so this returns their message unchanged.
func finalTriggerMessage(item triggeredRun) string { return triggerMessage(item) }

// mergeTriggeredRuns folds several same-task event fires into one run: the shared
// task-context header written ONCE, then each fire's event body, so the agent handles
// the task's burst in a single conversation without repeating the (possibly long)
// task description/goal per event.
func mergeTriggeredRuns(items []triggeredRun) triggeredRun {
	return (triggerBurst{events: items}).sameTask()
}

// mergeAllRuns folds the ENTIRE queued burst into one run (merge_mode='all'),
// regardless of type. Events are grouped by task (tasks in first-appearance order,
// events in their original order within a task), so each task's context header — and
// its possibly long description/goal — is written exactly ONCE even when fires from
// different tasks interleave in the queue. Cross-task chronology is not preserved
// (event bodies carry no timestamps, so per-task grouping reads better for the agent).
func mergeAllRuns(items []triggeredRun) triggeredRun { return (triggerBurst{events: items}).allTasks() }

// runTriggeredRun creates the conversation for one queued fire, records the human
// turn, and runs it synchronously (blocks until the run ends). recover keeps a
// panic from killing the drain loop and wedging the agent's queue.
func (s *Server) runTriggeredRun(item triggeredRun) {
	(triggerConversation{server: s, fire: item}).execute()
}

// firstLine returns a single-line, length-capped preview (shared with summaries).
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}
