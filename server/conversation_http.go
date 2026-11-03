package server

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/sidequestion"
)

// A document owns one HTTP outcome. A zero document means a shared decoder or
// resolver already wrote the response; successful empty data is still explicit.
type conversationDocument struct {
	status int
	value  any
}

func (document conversationDocument) write(writer http.ResponseWriter) {
	if document.status != 0 {
		writeJSON(writer, document.status, document.value)
	}
}
func conversationFailure(status int, text string) conversationDocument {
	return conversationDocument{status: status, value: map[string]any{"error": text}}
}
func conversationOK(value any) conversationDocument {
	return conversationDocument{status: 200, value: value}
}

type conversationEndpoint struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

type conversationCreateInput = struct {
	AgentKey     string `json:"agent_key"`
	Title        string `json:"title"`
	LLMProfileID *int64 `json:"llm_profile_id"`
}
type conversationProfileInput = struct {
	LLMProfileID *int64 `json:"llm_profile_id"`
}
type conversationRenameInput = struct {
	Title  *string `json:"title"`
	Pinned *bool   `json:"pinned"`
}
type conversationMessageInput = struct {
	Message     string           `json:"message"`
	Attachments []chatAttachment `json:"attachments,omitempty"`
}
type conversationDeleteInput = struct {
	IDs []int64 `json:"ids"`
}

func (endpoint conversationEndpoint) resolve() (*db.DB, *db.Conversation, bool) {
	database := endpoint.server.pg(endpoint.writer)
	if database == nil {
		return nil, nil, false
	}
	id, valid := pathInt(endpoint.request, "id")
	if !valid {
		writeErr(endpoint.writer, 400, "bad conversation id")
		return nil, nil, false
	}
	conversation, err := database.GetConversation(id)
	if err != nil {
		writeErr(endpoint.writer, 500, err.Error())
		return nil, nil, false
	}
	if conversation == nil {
		writeErr(endpoint.writer, 404, "conversation not found")
		return nil, nil, false
	}
	return database, conversation, true
}

func (endpoint conversationEndpoint) list() conversationDocument {
	database := endpoint.server.pg(endpoint.writer)
	if database == nil {
		return conversationDocument{}
	}
	rows, err := database.ListConversations()
	if err != nil {
		return conversationFailure(500, err.Error())
	}
	items := make([]conversationListItem, 0, len(rows))
	endpoint.server.chatMu.Lock()
	for _, row := range rows {
		items = append(items, conversationListItem{Conversation: row, Running: endpoint.server.chatBusy[endpoint.server.convBusyKey(row.ID)]})
	}
	endpoint.server.chatMu.Unlock()
	return conversationOK(map[string]any{"conversations": items})
}

func (endpoint conversationEndpoint) create() conversationDocument {
	database := endpoint.server.pg(endpoint.writer)
	if database == nil {
		return conversationDocument{}
	}
	var input conversationCreateInput
	if !decodeConversationRequest(endpoint.writer, endpoint.request, &input) {
		return conversationDocument{}
	}
	input.AgentKey = strings.TrimSpace(input.AgentKey)
	if input.AgentKey == "" {
		return conversationFailure(400, "agent_key must not be empty")
	}
	if utf8.RuneCountInString(input.AgentKey) > maxConversationAgentKeyRunes {
		return conversationFailure(400, fmt.Sprintf("agent_key may be at most %d characters", maxConversationAgentKeyRunes))
	}
	role, err := database.GetAgentByKey(input.AgentKey)
	if err != nil {
		return conversationFailure(500, err.Error())
	}
	if role == nil {
		return conversationFailure(404, "agent does not exist")
	}
	if input.LLMProfileID != nil {
		if _, valid := endpoint.server.loadProfileConfig(*input.LLMProfileID); !valid {
			return conversationFailure(400, "the specified LLM profile does not exist or has no API key set")
		}
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "New conversation"
	}
	if utf8.RuneCountInString(title) > maxConversationTitleRunes {
		return conversationFailure(400, fmt.Sprintf("title may be at most %d characters", maxConversationTitleRunes))
	}
	conversation, err := database.CreateConversation(input.AgentKey, title, input.LLMProfileID)
	if err != nil {
		return conversationFailure(500, err.Error())
	}
	return conversationOK(conversation)
}

func (endpoint conversationEndpoint) profile() conversationDocument {
	database, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	var input conversationProfileInput
	if !decodeConversationRequest(endpoint.writer, endpoint.request, &input) {
		return conversationDocument{}
	}
	if input.LLMProfileID != nil {
		if _, valid := endpoint.server.loadProfileConfig(*input.LLMProfileID); !valid {
			return conversationFailure(400, "the specified LLM profile does not exist or has no API key set")
		}
	}
	if err := database.UpdateConversationProfile(conversation.ID, input.LLMProfileID); err != nil {
		return conversationFailure(500, err.Error())
	}
	return conversationOK(map[string]any{"ok": true})
}

func (endpoint conversationEndpoint) rename() conversationDocument {
	database, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	var input conversationRenameInput
	if !decodeConversationRequest(endpoint.writer, endpoint.request, &input) {
		return conversationDocument{}
	}
	if input.Title == nil && input.Pinned == nil {
		return conversationFailure(400, "must provide at least title or pinned")
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			return conversationFailure(400, "title must not be empty")
		}
		if utf8.RuneCountInString(title) > maxConversationTitleRunes {
			return conversationFailure(400, fmt.Sprintf("title may be at most %d characters", maxConversationTitleRunes))
		}
		input.Title = &title
	}
	updated, err := database.UpdateConversation(conversation.ID, db.ConversationPatch{Title: input.Title, Pinned: input.Pinned})
	if err != nil {
		return conversationFailure(500, err.Error())
	}
	if updated == nil {
		return conversationFailure(404, "conversation not found")
	}
	return conversationOK(updated)
}

func (endpoint conversationEndpoint) remove() conversationDocument {
	database, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	endpoint.server.cancelConversation(conversation.ID)
	endpoint.server.cancelSideWhere(func(parent sidequestion.Parent) bool { return parent.ConversationID == conversation.ID })
	if err := database.DeleteConversation(conversation.ID); err != nil {
		return conversationFailure(500, err.Error())
	}
	return conversationOK(map[string]any{"deleted": conversation.ID})
}

func (endpoint conversationEndpoint) removeBatch() conversationDocument {
	database := endpoint.server.pg(endpoint.writer)
	if database == nil {
		return conversationDocument{}
	}
	var input conversationDeleteInput
	if !decodeConversationRequest(endpoint.writer, endpoint.request, &input) {
		return conversationDocument{}
	}
	ids := make([]int64, 0, len(input.IDs))
	seen := make(map[int64]struct{}, len(input.IDs))
	for _, id := range input.IDs {
		if id <= 0 {
			return conversationFailure(http.StatusBadRequest, "invalid conversation id")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > maxConversationDeleteBatch {
		return conversationFailure(http.StatusBadRequest, fmt.Sprintf("the number of ids must be 1-%d", maxConversationDeleteBatch))
	}
	for _, id := range ids {
		endpoint.server.cancelConversation(id)
		endpoint.server.cancelSideWhere(func(parent sidequestion.Parent) bool { return parent.ConversationID == id })
	}
	deleted, err := database.DeleteConversations(ids)
	if err != nil {
		return conversationFailure(http.StatusInternalServerError, err.Error())
	}
	deletedSet := make(map[int64]struct{}, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = struct{}{}
	}
	items := make([]conversationDeleteItem, 0, len(ids))
	for _, id := range ids {
		_, found := deletedSet[id]
		item := conversationDeleteItem{ID: id, OK: found}
		if !found {
			item.Error = "conversation not found"
		}
		items = append(items, item)
	}
	return conversationOK(map[string]any{"items": items})
}

func (endpoint conversationEndpoint) messages() conversationDocument {
	database, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	query := endpoint.request.URL.Query()
	limit := atoiDefault(query.Get("limit"), 200)
	endpoint.server.chatMu.Lock()
	running := endpoint.server.chatBusy[endpoint.server.convBusyKey(conversation.ID)]
	endpoint.server.chatMu.Unlock()
	if raw := query.Get("since"); raw != "" {
		since, _ := strconv.ParseInt(raw, 10, 64)
		items, cursor, err := database.ConvActivityList(conversation.ID, since, max(limit, 1000))
		if err != nil {
			return conversationFailure(500, err.Error())
		}
		return conversationOK(map[string]any{"items": activityDTOs(items), "cursor": cursor, "running": running, "hasMore": false})
	}
	before, _ := strconv.ParseInt(query.Get("before"), 10, 64)
	items, hasMore, err := database.ConvActivityPage(conversation.ID, before, limit)
	if err != nil {
		return conversationFailure(500, err.Error())
	}
	cursor := before
	if count := len(items); count > 0 {
		cursor = items[count-1].ID
	}
	return conversationOK(map[string]any{"items": activityDTOs(items), "cursor": cursor, "running": running, "hasMore": hasMore})
}

func (endpoint conversationEndpoint) detail() conversationDocument {
	database, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	sequence, valid := pathInt(endpoint.request, "seq")
	if !valid {
		return conversationFailure(400, "bad seq")
	}
	detail, err := database.ConvActivityDetail(conversation.ID, sequence)
	if err != nil {
		return conversationFailure(500, err.Error())
	}
	return conversationOK(map[string]any{"detail": detail})
}

func (endpoint conversationEndpoint) stop() conversationDocument {
	_, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	key := endpoint.server.convBusyKey(conversation.ID)
	endpoint.server.chatMu.Lock()
	cancel := endpoint.server.chatCancel[key]
	endpoint.server.chatMu.Unlock()
	if cancel == nil {
		return conversationOK(map[string]any{"status": "idle"})
	}
	cancel(agent.AbortChatStoppedByUser)
	return conversationOK(map[string]any{"status": "stopping"})
}

func (endpoint conversationEndpoint) send() conversationDocument {
	database, conversation, valid := endpoint.resolve()
	if !valid {
		return conversationDocument{}
	}
	var input conversationMessageInput
	if err := decode(endpoint.request, &input); err != nil {
		return conversationFailure(400, err.Error())
	}
	message := strings.TrimSpace(input.Message)
	if message == "" && len(input.Attachments) == 0 {
		return conversationFailure(400, "message must not be empty")
	}
	agentMessage, valid := endpoint.server.prepareChatMentionMessage(endpoint.writer, message)
	if !valid {
		return conversationDocument{}
	}
	if endpoint.server.resolveChatAgent(conversation) == nil {
		return conversationFailure(503, endpoint.server.chatUnavailableReason())
	}
	key := endpoint.server.convBusyKey(conversation.ID)
	endpoint.server.chatMu.Lock()
	if endpoint.server.chatBusy[key] {
		endpoint.server.chatMu.Unlock()
		return conversationFailure(409, "this conversation is still processing the previous message, please wait")
	}
	endpoint.server.chatBusy[key] = true
	endpoint.server.chatMu.Unlock()
	human := userActivityWithAttachments(conversation.AgentKey, message, input.Attachments)
	human.Summary = firstLine(message, 200)
	if human.Detail == "" {
		human.Detail = message
	}
	if _, err := database.AppendConvActivity(conversation.ID, human); err != nil {
		log.Printf("[conv %d] append user msg failed: %v", conversation.ID, err)
	}
	if conversation.Title == "" || conversation.Title == "New conversation" {
		title := firstLine(message, 40)
		if title == "" {
			title = "Attachment message"
		}
		_ = database.RenameConversation(conversation.ID, title)
	}
	_ = database.TouchConversation(conversation.ID)
	directory := filepath.Join(endpoint.server.m.dir, "sessions", key)
	endpoint.server.runConversation(conversation, composeAgentMessage(agentMessage, input.Attachments, directory), key, message)
	return conversationDocument{status: 202, value: map[string]any{"status": "accepted"}}
}
