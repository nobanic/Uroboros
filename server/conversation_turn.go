package server

import (
	"context"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
	"log"
	"time"
)

// One turn owns its service context, registration and final retest/execution
// lifecycle. Human background is attached before the asynchronous launch only.
type conversationTurn struct {
	server       *Server
	conversation *db.Conversation
	message, key string
	ctx          context.Context
	cancel       context.CancelCauseFunc
}
type conversationRegistration struct {
	server *Server
	id     int64
	key    string
}

func (registration conversationRegistration) open() (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(intercept.WithConvID(registration.server.ctx, registration.id))
	registration.server.chatMu.Lock()
	registration.server.chatCancel[registration.key] = cancel
	registration.server.chatMu.Unlock()
	return ctx, cancel
}
func (turn *conversationTurn) register() {
	turn.ctx, turn.cancel = turn.server.conversationRunContext(turn.conversation.ID, turn.key)
}
func (turn *conversationTurn) background(user []string) {
	turn.register()
	if len(user) == 1 {
		turn.ctx = intercept.WithReviewContext(turn.ctx, "", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: user[0]})
	}
	go turn.execute()
}
func (turn *conversationTurn) foreground() { turn.register(); turn.execute() }
func (turn *conversationTurn) execute() {
	defer func() {
		turn.cancel(agent.AbortChatTurnFinished)
		turn.server.chatMu.Lock()
		delete(turn.server.chatBusy, turn.key)
		delete(turn.server.chatCancel, turn.key)
		turn.server.chatMu.Unlock()
	}()
	// Only the first turn executes a historical retest. Follow-up conversation
	// turns may explain the sealed result; the result tool refuses to overwrite it.
	finishStatus, finishReason := "failed", "retest failed to start"
	if turn.conversation.AgentKey == db.FindingRetestAgentKey {
		// Read without the run cancellation so an immediate stop still seals pending.
		r, err := turn.server.m.pg.FindingRetestForConversation(context.Background(), turn.conversation.ID)
		if err != nil {
			// The sealing defer below needs r.ID, which we do not have here. Seal by
			// conversation instead, otherwise the row stays 'pending' forever.
			log.Printf("[conv %d] load retest: %v", turn.conversation.ID, err)
			if err := turn.server.m.pg.FailPendingRetestForConversation(turn.conversation.ID, "failed to read retest status; please retry"); err != nil {
				log.Printf("[conv %d] seal retest: %v", turn.conversation.ID, err)
			}
			return
		}
		if r != nil && r.Status == "pending" {
			defer func() {
				if turn.ctx.Err() != nil {
					finishStatus, finishReason = "stopped", "retest was stopped or the service shut down"
				}
				turn.server.finishRetest(r.ID, finishStatus, finishReason)
			}()
			if turn.ctx.Err() != nil {
				return
			}
			started, err := turn.server.m.pg.StartFindingRetest(turn.ctx, r.ID)
			if err != nil {
				finishReason = err.Error()
				return
			}
			if !started {
				return
			}
		}
	}
	if turn.ctx.Err() != nil {
		return
	}
	// precedence: the conversation agent's own binding → this conversation's pin → global.
	ca := turn.server.resolveChatAgent(turn.conversation)
	if ca == nil {
		finishReason = turn.server.chatUnavailableReason()
		return
	}
	pg := turn.server.m.pg
	maxTurns := turn.server.agentMaxTurns(turn.conversation.AgentKey)
	maxDuration := time.Duration(turn.server.agentRunSeconds(turn.conversation.AgentKey)) * time.Second
	webSearch := false
	if a, err := pg.GetAgentByKey(turn.conversation.AgentKey); err == nil && a != nil {
		webSearch = a.WebSearch
	}
	sessionID := turn.server.convBusyKey(turn.conversation.ID) // "conv-<id>" transcript session
	emit := func(rec db.Activity) {
		if _, err := pg.AppendConvActivity(turn.conversation.ID, rec); err != nil {
			log.Printf("[conv %d] append activity failed: %v", turn.conversation.ID, err)
		}
	}
	// On a manual stop ctx is cancelled; Chat already emits a clean "manually stopped"
	// step, so skip the raw-error entry — only surface genuine failures.
	if _, err := ca.Chat(turn.ctx, turn.conversation.AgentKey, sessionID, turn.message, maxTurns, maxDuration, webSearch, emit); err != nil {
		finishReason = err.Error()
		if turn.ctx.Err() == nil {
			_, _ = pg.AppendConvActivity(turn.conversation.ID, db.Activity{Worker: turn.conversation.AgentKey, Kind: "text", IsError: true,
				Summary: "(error: " + err.Error() + ")", Detail: err.Error()})
		}
	} else {
		finishStatus, finishReason = "completed", ""
	}
	_ = pg.TouchConversation(turn.conversation.ID)
}
