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
		{Method: "GET", Pattern: "/current-statuses", Handler: a.handleCurrentStatuses},
		{Method: "POST", Pattern: "/seen", Handler: a.handleSeen},
		{Method: "GET", Pattern: "/unread-summary", Handler: a.handleUnreadSummary},
		{Pattern: "/pairing", Handler: a.handlePairing},
	}
}

// requestUser resolves the delegated platform user, when present. The
// platform proxy stamps subject headers; standalone runs may omit them.
func requestUser(r *http.Request) int64 {
	raw := r.Header.Get("X-Apteva-Subject-ID")
	if raw == "" {
		raw = r.Header.Get("X-Apteva-User-ID")
	}
	id, _ := strconv.ParseInt(raw, 10, 64)
	return id
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
		userID := requestUser(r)
		var conversations []Conversation
		var err error
		if r.URL.Query().Get("archived") == "1" {
			conversations, err = a.store.ListArchivedForUser(userID, 100)
		} else {
			conversations, err = a.store.ListConversationsForUser(userID, 100)
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
	var body struct {
		AgentID     int64   `json:"agent_id"`
		AgentIDs    []int64 `json:"agent_ids"`
		LeadAgentID int64   `json:"lead_agent_id"`
		ProjectID   string  `json:"project_id"`
		Title       string  `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID: body.ProjectID, LeadAgentID: lead,
		Title: body.Title, OwnerUserID: requestUser(r),
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
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || id == "" {
		http.Error(w, "id and a title or archived field required", http.StatusBadRequest)
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
	if conv == nil {
		http.Error(w, "title or archived field required", http.StatusBadRequest)
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
	conv, err := a.store.GetConversation(id)
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
	app := a.appCtx(r)
	if app == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	agents, err := sdk.ListAgentsVia(app.PlatformAPI(), r.URL.Query().Get("project_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	type agentInfo struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	out := make([]agentInfo, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agentInfo{ID: agent.ID, Name: agent.Name, Status: agent.Status})
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
		ChatID   string `json:"chat_id"`
		Content  string `json:"content"`
		ClientID string `json:"client_message_id"`
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
	if strings.TrimSpace(body.Content) == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	conv, err := a.store.GetConversation(conversationID)
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	msg, inserted, err := a.store.AppendMessageIdempotent(&Message{
		ConversationID: conv.ID, Role: "user", Content: body.Content,
		UserID: requestUser(r), ClientID: body.ClientID,
	})
	if err != nil {
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	if !inserted {
		writeJSON(w, msg)
		return
	}
	a.deliver(a.appCtx(r), conv, msg)
	a.forwardToAgents(a.appCtx(r), conv, msg)
	writeJSON(w, msg)
}

// appCtx recovers the mounted AppCtx for HTTP handlers. The SDK routes
// carry it via closure at mount time in richer setups; keeping a single
// accessor makes the seam explicit and testable.
func (a *App) appCtx(_ *http.Request) *sdk.AppCtx { return mountedCtx }

var mountedCtx *sdk.AppCtx

// forwardToAgents delivers an inbound message to the lead agent on the
// conversation's OWN thread — spawned lazily via ensureConversationThread
// (channel-chat parity: main's context never mixes with conversation
// context). Every failure degrades to the main thread, and a total
// delivery failure is loud: a system row lands in the conversation
// instead of an indefinite quiet.
func (a *App) forwardToAgents(app *sdk.AppCtx, conv *Conversation, msg *Message) {
	if app == nil {
		return
	}
	event := fmt.Sprintf("[chat] %s", msg.Content)

	if threadID := a.ensureConversationThread(app, conv); threadID != "" {
		if tc, ok := app.PlatformAPI().(sdk.ThreadClient); ok {
			if err := tc.SendThreadEvent(sdk.ThreadRef{AgentID: conv.LeadAgentID, ThreadID: threadID}, event); err == nil {
				// Instant feedback: the thinking bubble appears the
				// moment the agent has the message. The telemetry feed
				// upgrades it to token text when the bridge is on; the
				// durable reply settles it either way (see toolSend).
				a.streamer.emitAck(conv.ID, threadID)
				return
			}
		}
	}
	if err := app.PlatformAPI().SendEvent(conv.LeadAgentID, event); err != nil {
		notice := fmt.Sprintf("(could not reach agent %d — your message is saved. err: %v)", conv.LeadAgentID, err)
		if sys, sysErr := a.store.AppendMessage(&Message{
			ConversationID: conv.ID, Role: "system", Content: notice,
		}); sysErr == nil {
			a.hub.publish(conv.ID, *sys)
		}
		return
	}
	a.streamer.emitAck(conv.ID, "")
}

// ─── SSE ─────────────────────────────────────────────────────────────

// handleStream serves both scopes: ?chat_id=<id> for one conversation
// panel, ?scope=user for the global bell/tray. Reconnects backfill via
// ?since=<last_id> before going live — the hub never replays.
func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	conversationID := r.URL.Query().Get("chat_id")
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
		ch, cancel = a.hub.subscribeUser(fmt.Sprint(requestUser(r)))
	default:
		http.Error(w, "chat_id or scope=user required", http.StatusBadRequest)
		return
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := a.store.Inbox(limit)
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
	msg, err := a.store.GetMessage(body.MessageID)
	if err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	updated, err := a.resolveApproval(a.appCtx(r), msg, body.ActionID, body.Note, requestUser(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"message": updated, "status": body.ActionID})
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

func (a *App) handleCurrentStatuses(w http.ResponseWriter, r *http.Request) {
	statuses, err := a.store.CurrentStatuses()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, statuses)
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
	if err := a.store.MarkSeen(requestUser(r), body.ChatID, body.LastSeenID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleUnreadSummary(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.UnreadSummary(requestUser(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []UnreadEntry{}
	}
	writeJSON(w, entries)
}

// ─── pairing ─────────────────────────────────────────────────────────

func (a *App) handlePairing(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		codes, err := a.store.PendingPairings()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, codes)
	case http.MethodPost:
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Code == "" {
			http.Error(w, "code required", http.StatusBadRequest)
			return
		}
		if err := a.store.ApprovePairing(body.Code); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
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
