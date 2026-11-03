package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
)

// Alias the original anonymous request shape: decoder rules, tags and type
// identity stay independent of the turn that outlives this HTTP request.
type taskChatInput = struct {
	Message     string           `json:"message"`
	Attachments []chatAttachment `json:"attachments,omitempty"`
	Seg         *int             `json:"seg,omitempty"`
}

type taskChatRequest struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

type taskChatTurn struct {
	server       *Server
	task         *Task
	input        taskChatInput
	agentMessage string
	ctx          context.Context
	cancel       context.CancelCauseFunc
	segment      int
}

func (request taskChatRequest) serve() {
	task := request.server.m.ResolveTask(request.request.URL.Query().Get("task"))
	if task == nil {
		writeErr(request.writer, 404, "no active task")
		return
	}
	if request.server.engine.IsDeleting(task.ID) {
		request.deleting()
		return
	}
	var input taskChatInput
	if err := json.NewDecoder(request.request.Body).Decode(&input); err != nil {
		writeErr(request.writer, 400, err.Error())
		return
	}
	agentMessage, valid := request.server.prepareChatMentionMessage(request.writer, input.Message)
	if !valid {
		return
	}
	turn := &taskChatTurn{server: request.server, task: task, input: input, agentMessage: agentMessage}
	if !request.admit(turn) {
		return
	}
	turn.start(request.writer)
}

func (request taskChatRequest) deleting() {
	writeErr(request.writer, 409, "task is being deleted; a new message cannot be sent")
}

func (request taskChatRequest) admit(turn *taskChatTurn) bool {
	s := request.server
	s.chatMu.Lock()
	if s.engine.IsDeleting(turn.task.ID) {
		s.chatMu.Unlock()
		request.deleting()
		return false
	}
	if s.chatBusy[turn.task.ID] {
		s.chatMu.Unlock()
		writeErr(request.writer, 409, "the Main Agent is still processing the previous message, please wait")
		return false
	}
	// Turns belong to the service; a closed page cannot cancel a multi-tool run.
	ctx, cancel := context.WithCancelCause(s.ctx)
	ctx = intercept.WithReviewContext(ctx, "", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: turn.input.Message})
	s.chatBusy[turn.task.ID] = true
	s.chatCancel[turn.task.ID] = cancel
	s.chatMu.Unlock()
	turn.ctx, turn.cancel = ctx, cancel
	return true
}

func (turn *taskChatTurn) start(writer http.ResponseWriter) {
	if turn.input.Seg != nil && *turn.input.Seg >= 0 {
		turn.segment = *turn.input.Seg
	} else {
		turn.segment, _ = turn.task.Store.CurrentMainSeg()
	}
	human := userActivityWithAttachments("mainagent", turn.input.Message, turn.input.Attachments)
	human.MainSeg = &turn.segment
	turn.server.engine.emitActivity(turn.task, human)
	var main *agent.MainAgent
	if turn.server.taskRuntimeAvailable(turn.task, "mainagent") {
		main = turn.server.agentsForTask(turn.task).main
	}
	if main != nil {
		go turn.runAgent(main)
		writeJSON(writer, 202, map[string]any{"status": "accepted", "mode": "llm"})
		return
	}
	reply := turn.server.fallbackChat(turn.task, turn.input.Message)
	turn.server.engine.emitActivity(turn.task, db.Activity{Worker: "mainagent", Kind: "text", Summary: reply, MainSeg: &turn.segment})
	turn.server.finishTaskChat(turn.task.ID, turn.cancel)
	writeJSON(writer, 200, map[string]any{"reply": reply, "mode": "rule"})
}

func (turn *taskChatTurn) runAgent(main *agent.MainAgent) {
	defer func() { turn.server.finishTaskChat(turn.task.ID, turn.cancel) }()
	emit := func(event db.Activity) {
		event.MainSeg = &turn.segment
		turn.server.engine.emitActivity(turn.task, event)
	}
	taskID, _ := strconv.ParseInt(turn.task.ID, 10, 64)
	resume := func() { turn.server.reviveTask(turn.task) }
	directory := filepath.Join(turn.server.m.dir, "tasks", turn.task.ID)
	message := composeAgentMessage(turn.agentMessage, turn.input.Attachments, directory)
	turn.server.engine.BeginLLMCall(turn.task.ID)
	_, err := main.Chat(turn.ctx, taskID, turn.segment, turn.server.m.Assets(), turn.task.Store, turn.task.Goal, message, emit, turn.task.Notify, resume, turn.task.NotifyGoal, turn.task.NotifyHint)
	turn.server.engine.EndLLMCall(turn.task.ID)
	if err != nil && turn.ctx.Err() == nil {
		turn.server.engine.emitActivity(turn.task, db.Activity{Worker: "mainagent", Kind: "text", IsError: true, Summary: "(Main Agent error: " + err.Error() + ")", MainSeg: &turn.segment})
	}
}
