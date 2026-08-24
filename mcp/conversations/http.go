package main

// http.go — the dashboard-facing surface. Reserved sidecar prefixes
// (/health /manifest /mcp /events /ui/) belong to the SDK; every route
// here avoids them, and TestHTTPRoutesAvoidReservedPrefixes guards it.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/chats", Handler: a.handleChats},
		{Pattern: "/participants", Handler: a.handleParticipants},
		{Method: "GET", Pattern: "/agents", Handler: a.handleAgents},
		{Pattern: "/messages", Handler: a.handleMessages},
		{Method: "GET", Pattern: "/stream", Handler: a.handleStream},
		{Method: "GET", Pattern: "/inbox", Handler: a.handleInbox},
		{Method: "POST", Pattern: "/message-action", Handler: a.handleMessageAction},
		{Method: "POST", Pattern: "/message-dismiss", Handler: a.handleMessageDismiss},
		{Method: "POST", Pattern: "/seen", Handler: a.handleSeen},
		{Method: "GET", Pattern: "/unread-summary", Handler: a.handleUnreadSummary},
		{Pattern: "/delivery-failures", Handler: a.handleDeliveryFailures},
		{Pattern: "/telegram-connections", Handler: a.handleTelegramConnections},
		{Pattern: "/telegram-bindings", Handler: a.handleTelegramBindings},
		{Pattern: "/telegram-intake", Handler: a.handleTelegramIntake},
		{Pattern: "/telegram-access", Handler: a.handleTelegramAccess},
		{Pattern: "/telegram-invites", Handler: a.handleTelegramInvites},
		{Pattern: "/telegram-webhook/", Handler: a.handleTelegramWebhook, NoAuth: true},
	}
}

// requestUser resolves the delegated platform user, when present. The
// platform proxy stamps subject headers; standalone runs may omit them.
func requestUser(r *http.Request) int64 {
	raw := r.Header.Get("X-User-ID")
	if raw == "" {
		raw = r.Header.Get("X-Apteva-User-ID")
	}
	id, _ := strconv.ParseInt(raw, 10, 64)
	return id
}

func requestProject(r *http.Request) string {
	if r == nil {
		return ""
	}
	if projectID := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); projectID != "" {
		return projectID
	}
	return strings.TrimSpace(r.URL.Query().Get("project_id"))
}

func requestIdentity(r *http.Request) (int64, string, error) {
	userID, projectID := requestUser(r), requestProject(r)
	if userID <= 0 {
		return 0, "", fmt.Errorf("authenticated user required")
	}
	if projectID == "" {
		return 0, "", fmt.Errorf("project_id required")
	}
	return userID, projectID, nil
}

// requestAgentScope parses the optional panel projection. It is only an
// additional filter: handlers must establish the authenticated user and
// trusted project first, and store queries intersect this id with explicit
// participants inside that authorized result set.
func requestAgentScope(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("agent_id must be a positive integer")
	}
	return id, nil
}

func (a *App) authorizeConversation(r *http.Request, id string) (*Conversation, error) {
	userID, projectID, err := requestIdentity(r)
	if err != nil {
		return nil, err
	}
	ok, err := a.store.UserCanAccessConversation(id, projectID, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("conversation not found")
	}
	return a.store.getConversationAny(id)
}

func (a *App) authorizeMessage(r *http.Request, id int64) (*Message, *Conversation, error) {
	msg, err := a.store.GetMessage(id)
	if err != nil {
		return nil, nil, fmt.Errorf("message not found")
	}
	conv, err := a.authorizeConversation(r, msg.ConversationID)
	if err != nil {
		return nil, nil, err
	}
	return msg, conv, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ─── conversations ───────────────────────────────────────────────────

// chatListEntry decorates a conversation with the display data the
// panel sidebar needs. Names come from the platform with a small
// cache — never stored, so renames show up on the next refresh.
type chatListEntry struct {
	Conversation
	LeadAgentName string `json:"lead_agent_name,omitempty"`
}

func (a *App) handleChats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		userID, projectID, identityErr := requestIdentity(r)
		if identityErr != nil {
			http.Error(w, identityErr.Error(), http.StatusUnauthorized)
			return
		}
		agentID, scopeErr := requestAgentScope(r)
		if scopeErr != nil {
			http.Error(w, scopeErr.Error(), http.StatusBadRequest)
			return
		}
		var conversations []Conversation
		var err error
		if r.URL.Query().Get("archived") == "1" {
			conversations, err = a.store.ListArchivedForUserAndAgent(projectID, userID, agentID, 100)
		} else {
			conversations, err = a.store.ListConversationsForUserAndAgent(projectID, userID, agentID, 100)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entries := make([]chatListEntry, 0, len(conversations))
		for _, conv := range conversations {
			entries = append(entries, chatListEntry{
				Conversation:  conv,
				LeadAgentName: a.agentName(a.appCtx(r), conv.LeadAgentID),
			})
		}
		writeJSON(w, entries)
	case http.MethodPost:
		a.handleCreateChat(w, r)
	case http.MethodPatch:
		a.handleUpdateChat(w, r)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if _, err := a.authorizeConversation(r, id); err != nil {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
		if err := a.store.DeleteConversation(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"deleted": true})
	default:
		http.Error(w, "GET, POST, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

// handleCreateChat accepts both the original single-agent shape
// ({agent_id}) and the dashboard-parity multi-agent shape
// ({agent_ids: [...], lead_agent_id}). Extra agents become participant
// rows; more than one flips kind to "room" (store recomputes).
func (a *App) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	var body struct {
		AgentID     int64   `json:"agent_id"`
		AgentIDs    []int64 `json:"agent_ids"`
		LeadAgentID int64   `json:"lead_agent_id"`
		ProjectID   string  `json:"project_id"`
		Title       string  `json:"title"`
		// ConversationKey makes creation find-or-create (one
		// conversation per external identity) — the surface gateway
		// apps use for public site chats: key "app:<gateway>:<visitor>",
		// post messages, subscribe /stream. Audience defaults to
		// "public" when a key is given, "operator" otherwise.
		ConversationKey string `json:"conversation_key"`
		Audience        string `json:"audience"`
		Directive       string `json:"directive"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.ProjectID != "" && strings.TrimSpace(body.ProjectID) != projectID {
		http.Error(w, "project_id does not match authenticated project", http.StatusForbidden)
		return
	}
	agents := body.AgentIDs
	if len(agents) == 0 && body.AgentID != 0 {
		agents = []int64{body.AgentID}
	}
	lead := body.LeadAgentID
	if lead == 0 && len(agents) > 0 {
		lead = agents[0]
	}
	if lead == 0 {
		http.Error(w, "agent_id or agent_ids required", http.StatusBadRequest)
		return
	}
	if len(agents) == 0 || len(agents) > 8 {
		http.Error(w, "conversation supports 1 to 8 agents", http.StatusBadRequest)
		return
	}
	leadFound := false
	requested := map[int64]bool{}
	for _, agentID := range agents {
		if agentID <= 0 || requested[agentID] {
			http.Error(w, "invalid or duplicate agent_ids", http.StatusBadRequest)
			return
		}
		requested[agentID] = true
		leadFound = leadFound || agentID == lead
	}
	if !leadFound {
		http.Error(w, "lead_agent_id must be a participant", http.StatusBadRequest)
		return
	}
	if len([]rune(strings.TrimSpace(body.Title))) > 120 {
		http.Error(w, "title is too long", http.StatusBadRequest)
		return
	}
	appCtx := a.appCtx(r)
	if appCtx == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	available, err := sdk.ListAgentsVia(appCtx.PlatformAPI(), projectID)
	if err != nil {
		http.Error(w, "agent directory unavailable", http.StatusBadGateway)
		return
	}
	for _, agent := range available {
		delete(requested, agent.ID)
	}
	if len(requested) != 0 {
		http.Error(w, "one or more agents are not available in this project", http.StatusNotFound)
		return
	}
	audience := body.Audience
	switch audience {
	case "":
		if body.ConversationKey != "" {
			audience = "public"
		} else {
			audience = "operator"
		}
	case "public", "operator":
	default:
		http.Error(w, "audience must be public or operator", http.StatusBadRequest)
		return
	}
	origin := ""
	if body.ConversationKey != "" {
		origin = "app"
	}
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID: projectID, LeadAgentID: lead,
		Title: body.Title, OwnerUserID: userID,
		ConversationKey: body.ConversationKey, Audience: audience, Origin: origin,
		Directive: body.Directive,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, agentID := range agents {
		if agentID == lead || agentID == 0 {
			continue
		}
		if err := a.store.AddAgentParticipant(conv.ID, agentID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	conv, _ = a.store.GetConversation(conv.ID)
	writeJSON(w, chatListEntry{Conversation: *conv, LeadAgentName: a.agentName(a.appCtx(r), conv.LeadAgentID)})
}

func (a *App) handleUpdateChat(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var body struct {
		Title     *string `json:"title"`
		Archived  *bool   `json:"archived"`
		Directive *string `json:"directive"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || id == "" {
		http.Error(w, "id and a title or archived field required", http.StatusBadRequest)
		return
	}
	if _, err := a.authorizeConversation(r, id); err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	var conv *Conversation
	var err error
	if body.Title != nil {
		title := strings.TrimSpace(*body.Title)
		if title == "" {
			http.Error(w, "title cannot be empty", http.StatusBadRequest)
			return
		}
		if conv, err = a.store.UpdateConversationTitle(id, title); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if body.Archived != nil {
		if conv, err = a.store.SetConversationArchived(id, *body.Archived); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if body.Directive != nil {
		if conv, err = a.store.UpdateConversationDirective(id, strings.TrimSpace(*body.Directive)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if conv == nil {
		http.Error(w, "title, archived, or directive field required", http.StatusBadRequest)
		return
	}
	writeJSON(w, chatListEntry{Conversation: *conv, LeadAgentName: a.agentName(a.appCtx(r), conv.LeadAgentID)})
}

// handleParticipants manages the agent roster of one conversation.
// The lead agent is fixed: removing it would orphan message forwarding,
// so that returns 400 (dashboard parity — the lead has no remove link).
func (a *App) handleParticipants(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	conv, err := a.authorizeConversation(r, id)
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	respond := func() {
		ids, err := a.store.AgentParticipants(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ids == nil {
			ids = []int64{}
		}
		writeJSON(w, map[string]any{"agent_ids": ids, "lead_agent_id": conv.LeadAgentID})
	}
	switch r.Method {
	case http.MethodGet:
		respond()
	case http.MethodPost:
		var body struct {
			AgentID int64 `json:"agent_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil || body.AgentID == 0 {
			http.Error(w, "agent_id required", http.StatusBadRequest)
			return
		}
		appCtx := a.appCtx(r)
		if appCtx == nil {
			http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
			return
		}
		agents, listErr := sdk.ListAgentsVia(appCtx.PlatformAPI(), conv.ProjectID)
		allowed := false
		for _, agent := range agents {
			allowed = allowed || agent.ID == body.AgentID
		}
		if listErr != nil || !allowed {
			http.Error(w, "agent not found in conversation project", http.StatusNotFound)
			return
		}
		if err := a.store.AddAgentParticipant(id, body.AgentID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respond()
	case http.MethodDelete:
		agentID, _ := strconv.ParseInt(r.URL.Query().Get("agent_id"), 10, 64)
		if agentID == 0 {
			http.Error(w, "agent_id required", http.StatusBadRequest)
			return
		}
		if agentID == conv.LeadAgentID {
			http.Error(w, "cannot remove the lead agent", http.StatusBadRequest)
			return
		}
		if err := a.store.RemoveAgentParticipant(id, agentID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respond()
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
	}
}

// handleAgents serves the picker in the panel's new-conversation and
// participants dialogs. Backed by the SDK's optional agent-directory
// capability (authorized by platform.instances.read, which this app
// already holds); older platforms get a clear error, not a guess.
func (a *App) handleAgents(w http.ResponseWriter, r *http.Request) {
	_, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	app := a.appCtx(r)
	if app == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	agents, err := sdk.ListAgentsVia(app.PlatformAPI(), projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// attached = this agent holds our MCP (app_agent_bindings), i.e. it
	// can actually reply with conversations_send. The panel scopes its
	// pickers to attached agents; servers that predate the annotation
	// report false for everyone, which the panel treats as "unknown"
	// rather than "none".
	type agentInfo struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Attached bool   `json:"attached"`
	}
	out := make([]agentInfo, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agentInfo{ID: agent.ID, Name: agent.Name, Status: agent.Status,
			Attached: agent.AttachedToCaller})
	}
	writeJSON(w, out)
}

// ─── messages ────────────────────────────────────────────────────────

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		conversationID := r.URL.Query().Get("chat_id")
		if conversationID == "" {
			http.Error(w, "chat_id required", http.StatusBadRequest)
			return
		}
		if _, err := a.authorizeConversation(r, conversationID); err != nil {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		messages, err := a.store.Transcript(conversationID, since, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if messages == nil {
			messages = []Message{}
		}
		writeJSON(w, messages)
	case http.MethodPost:
		a.handlePostMessage(w, r)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handlePostMessage inserts the user row, fans out delivery, and
// forwards the text to every participating agent's core /event — the
// insert and the forward both happen before the response so the caller
// cannot race the agent's first reaction. Forward failure is loud: a
// system row lands in the conversation instead of an indefinite quiet.
func (a *App) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	conversationID := r.URL.Query().Get("chat_id")
	var body struct {
		ChatID         string       `json:"chat_id"`
		Content        string       `json:"content"`
		ClientID       string       `json:"client_message_id"`
		Attachments    []Attachment `json:"attachments"`
		TargetAgentIDs []int64      `json:"target_agent_ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if conversationID == "" {
		conversationID = body.ChatID
	}
	if conversationID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Content) == "" && len(body.Attachments) == 0 {
		http.Error(w, "content or attachments required", http.StatusBadRequest)
		return
	}
	conv, err := a.authorizeConversation(r, conversationID)
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	if len(body.Attachments) > 10 {
		http.Error(w, "attachments supports at most 10 entries", http.StatusBadRequest)
		return
	}
	for _, attachment := range body.Attachments {
		if attachment.Type != "image" || strings.TrimSpace(attachment.DataURL) == "" {
			http.Error(w, "unsupported attachment type", http.StatusBadRequest)
			return
		}
	}
	targets, err := a.resolveAgentTargetsFromText(a.appCtx(r), conv, body.Content, body.TargetAgentIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg, inserted, err := a.appendAndDeliver(a.appCtx(r), conv, &Message{
		ConversationID: conv.ID, Role: "user", Content: body.Content,
		UserID: requestUser(r), ClientID: body.ClientID, Attachments: body.Attachments,
		Metadata: map[string]any{"target_agent_ids": targets},
	})
	if err != nil {
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	_ = inserted // duplicate posts reuse the durable row and delivery ledger
	writeJSON(w, msg)
}

// appCtx recovers the mounted AppCtx for HTTP handlers. The SDK routes
// carry it via closure at mount time in richer setups; keeping a single
// accessor makes the seam explicit and testable.
func (a *App) appCtx(_ *http.Request) *sdk.AppCtx { return mountedCtx }

var mountedCtx *sdk.AppCtx

func (a *App) resolveAgentTargets(conv *Conversation, requested []int64) ([]int64, error) {
	participants, err := a.store.AgentParticipants(conv.ID)
	if err != nil {
		return nil, err
	}
	allowed := map[int64]bool{}
	for _, id := range participants {
		allowed[id] = true
	}
	if len(requested) == 0 {
		requested = []int64{conv.LeadAgentID}
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(requested))
	for _, id := range requested {
		if id <= 0 || !allowed[id] {
			return nil, fmt.Errorf("agent %d is not a conversation participant", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// resolveAgentTargetsFromText adds human routing for rooms. Explicit API
// targets win; otherwise @all addresses every participant and @<agent name>
// addresses matching participants. Messages without a route go to the lead.
func (a *App) resolveAgentTargetsFromText(app *sdk.AppCtx, conv *Conversation, text string, requested []int64) ([]int64, error) {
	if len(requested) > 0 {
		return a.resolveAgentTargets(conv, requested)
	}
	participants, err := a.store.AgentParticipants(conv.ID)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(text)
	if containsRoutingMention(lower, "all") {
		return a.resolveAgentTargets(conv, participants)
	}
	if app == nil || len(participants) <= 1 {
		return a.resolveAgentTargets(conv, nil)
	}
	participant := map[int64]bool{}
	for _, id := range participants {
		participant[id] = true
	}
	agents, err := sdk.ListAgentsVia(app.PlatformAPI(), conv.ProjectID)
	if err != nil {
		return a.resolveAgentTargets(conv, nil)
	}
	var matched []int64
	for _, agent := range agents {
		if !participant[agent.ID] {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(agent.Name))
		if name != "" && (containsRoutingMention(lower, name) || containsRoutingMention(lower, fmt.Sprint(agent.ID))) {
			matched = append(matched, agent.ID)
		}
	}
	return a.resolveAgentTargets(conv, matched)
}

func containsRoutingMention(text, name string) bool {
	needle := "@" + strings.ToLower(strings.TrimSpace(name))
	if needle == "@" {
		return false
	}
	for start := 0; ; {
		idx := strings.Index(text[start:], needle)
		if idx < 0 {
			return false
		}
		idx += start
		end := idx + len(needle)
		beforeOK := idx == 0 || !isRouteWordByte(text[idx-1])
		afterOK := end == len(text) || !isRouteWordByte(text[end])
		if beforeOK && afterOK {
			return true
		}
		start = idx + 1
	}
}

func isRouteWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

func (a *App) agentEventPayload(conv *Conversation, msg *Message, agentID int64, targets []int64) any {
	text := "[chat] " + msg.Content
	if conv.Kind == "room" {
		text += fmt.Sprintf("\nConversation: %s (%s). Addressed agent: %d. Other addressed agent ids: %v. Reply with conversations_send to this conversation.",
			conv.Title, conv.ID, agentID, targets)
	}
	if len(msg.Attachments) == 0 {
		return text
	}
	parts := []map[string]any{{"type": "text", "text": text}}
	for _, attachment := range msg.Attachments {
		if attachment.Type == "image" && attachment.DataURL != "" {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": attachment.DataURL}})
		}
	}
	return parts
}

// ─── SSE ─────────────────────────────────────────────────────────────

// handleStream serves both scopes: ?chat_id=<id> for one conversation
// panel, ?scope=user for the global bell/tray. Reconnects backfill via
// ?since=<last_id> before going live — the hub never replays.
func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	conversationID := r.URL.Query().Get("chat_id")
	if conversationID != "" {
		if _, err := a.authorizeConversation(r, conversationID); err != nil {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
	} else if r.URL.Query().Get("scope") != "user" {
		http.Error(w, "chat_id or scope=user required", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	var ch <-chan Message
	var frames <-chan StreamFrame
	var cancel, cancelFrames func()
	switch {
	case conversationID != "":
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if since > 0 {
			backlog, err := a.store.Transcript(conversationID, since, 200)
			if err == nil {
				for _, m := range backlog {
					writeSSE(w, m)
				}
				flusher.Flush()
			}
		}
		ch, cancel = a.hub.subscribeConversation(conversationID)
		frames, cancelFrames = a.hub.subscribeFrames(conversationID)
	case r.URL.Query().Get("scope") == "user":
		ch, cancel = a.hub.subscribeUser(projectID + ":" + fmt.Sprint(userID))
	}
	defer cancel()
	if cancelFrames != nil {
		defer cancelFrames()
	}
	flusher.Flush()

	if frames == nil {
		frames = make(chan StreamFrame) // never fires for user scope
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case f := <-frames:
			// Named event: the client's `stream` listener gets ephemeral
			// bubbles; default-event listeners never see them.
			encoded, err := json.Marshal(f)
			if err == nil {
				fmt.Fprintf(w, "event: stream\ndata: %s\n\n", encoded)
				flusher.Flush()
			}
		case m, open := <-ch:
			if !open {
				return
			}
			writeSSE(w, m)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, m Message) {
	encoded, _ := json.Marshal(m)
	fmt.Fprintf(w, "data: %s\n\n", encoded)
}

// ─── inbox ───────────────────────────────────────────────────────────

func (a *App) handleInbox(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	agentID, scopeErr := requestAgentScope(r)
	if scopeErr != nil {
		http.Error(w, scopeErr.Error(), http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := a.store.InboxForAgent(projectID, userID, agentID, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

func (a *App) handleMessageAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID int64  `json:"message_id"`
		ActionID  string `json:"action_id"`
		Note      string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil ||
		body.MessageID == 0 || strings.TrimSpace(body.ActionID) == "" {
		http.Error(w, "message_id and action_id required", http.StatusBadRequest)
		return
	}
	msg, _, err := a.authorizeMessage(r, body.MessageID)
	if err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	updated, err := a.resolveApproval(a.appCtx(r), msg, body.ActionID, body.Note, requestUser(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	deliveryStatus := "not_required"
	if target := approvalDeliveryTarget(msg); target != "" {
		deliveryStatus = "pending"
		if delivery, deliveryErr := a.store.DeliveryFor(updated.ID, target); deliveryErr == nil {
			deliveryStatus = delivery.Status
		}
	}
	writeJSON(w, map[string]any{"message": updated, "status": body.ActionID, "result_delivery_status": deliveryStatus})
}

// handleMessageDismiss hides an inbox card from the pending view.
// History keeps the row; the updated card republishes so every open
// surface drops it together.
func (a *App) handleMessageDismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil || body.MessageID == 0 {
		http.Error(w, "message_id required", http.StatusBadRequest)
		return
	}
	if _, _, err := a.authorizeMessage(r, body.MessageID); err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	updated, err := a.DismissMessage(body.MessageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if conv, convErr := a.store.GetConversation(updated.ConversationID); convErr == nil {
		a.deliver(a.appCtx(r), conv, updated)
	}
	writeJSON(w, map[string]any{"message": updated, "dismissed": true})
}

func (a *App) handleSeen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID     string `json:"chat_id"`
		LastSeenID int64  `json:"last_seen_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil || body.ChatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	userID, _, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	if _, err := a.authorizeConversation(r, body.ChatID); err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if err := a.store.MarkSeen(userID, body.ChatID, body.LastSeenID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleUnreadSummary(w http.ResponseWriter, r *http.Request) {
	userID, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	agentID, scopeErr := requestAgentScope(r)
	if scopeErr != nil {
		http.Error(w, scopeErr.Error(), http.StatusBadRequest)
		return
	}
	entries, err := a.store.UnreadSummaryForAgent(projectID, userID, agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []UnreadEntry{}
	}
	writeJSON(w, entries)
}

func (a *App) handleDeliveryFailures(w http.ResponseWriter, r *http.Request) {
	_, projectID, identityErr := requestIdentity(r)
	if identityErr != nil {
		http.Error(w, identityErr.Error(), http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := a.store.FailedDeliveries(projectID, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, items)
	case http.MethodPost:
		var body struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil || body.ID <= 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := a.store.RetryFailedDelivery(projectID, body.ID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"queued": true})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// agentNameCache — 60s TTL, plenty for a sidebar label.
var agentNameCache sync.Map // int64 → agentNameEntry

type agentNameEntry struct {
	name    string
	fetched time.Time
}

func (a *App) agentName(app *sdk.AppCtx, agentID int64) string {
	if app == nil || agentID == 0 {
		return ""
	}
	if raw, ok := agentNameCache.Load(agentID); ok {
		if entry := raw.(agentNameEntry); time.Since(entry.fetched) < time.Minute {
			return entry.name
		}
	}
	name := ""
	if agent, err := app.PlatformAPI().GetAgent(agentID); err == nil && agent != nil {
		name = agent.Name
	}
	agentNameCache.Store(agentID, agentNameEntry{name: name, fetched: time.Now()})
	return name
}
