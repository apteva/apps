// Channels app v0.2.
//
// This is the standalone version of the platform chat channel: it owns
// chat persistence, REST/SSE delivery, and the agent-facing MCP tools.
// The only platform dependency is app-sdk's PlatformAPI for forwarding
// inbound user messages to the owning agent.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: channels
display_name: Channels
version: 0.5.2
description: |
  Agent-facing channel router with standalone dashboard chat, a visible inbox, project channel management, and ntfy-compatible notifications.
  Agents reply through respond(channel="chat", ...); the app stores chat
  history, streams updates to the dashboard, exposes private ntfy topics,
  and forwards inbound user messages to the owning agent.
author: Apteva
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/channels/icon.svg
scopes: [global]
requires:
  permissions:
    - db.write.app
    - platform.instances.read
    - platform.instances.write
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: respond
      description: Send a user-visible reply to a channel. Supports channel="chat" and ntfy channel ids from list_channels.
    - name: status
      description: Send a status line to a channel. V1 writes chat status lines as system messages.
    - name: list_channels
      description: List channels currently reachable for the calling agent.
  ui_panels:
    - slot: project.page
      label: Channels
      icon: radio
      entry: /ui/ChannelsPanel.mjs
  publishes:
    - name: channel.created
      description: A channel was created.
    - name: channel.updated
      description: A channel was updated.
    - name: channel.deleted
      description: A channel was deleted.
    - name: conversation.updated
      description: A conversation changed, usually because a message was inserted.
    - name: conversation.seen
      description: A conversation read cursor changed.
    - name: conversation.deleted
      description: A conversation was deleted.
    - name: message.created
      description: A message was inserted in any channel conversation.
    - name: message.deleted
      description: Messages were deleted from a conversation.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/channels
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/channels.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct {
	store *store
	hub   *hub
}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("channels app requires a db block")
	}
	globalCtx = ctx
	a.store = newStore(ctx.AppDB())
	a.hub = newHub()
	ctx.Logger().Info("channels app mounted", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/chats", Handler: a.handleChats},
		{Pattern: "/channels", Handler: a.handleChannels},
		{Pattern: "/messages", Handler: a.handleMessages},
		{Pattern: "/stream", Handler: a.handleStream},
		{Pattern: "/unread-summary", Handler: a.handleUnreadSummary},
		{Pattern: "/seen", Handler: a.handleSeen},
		{Pattern: "/presence", Handler: a.handlePresence},
		{Pattern: "/ntfy", Handler: a.handleNtfyRoot, NoAuth: true},
		{Pattern: "/ntfy/", Handler: a.handleNtfy, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "respond",
			Description: respondDescription(),
			Meta:        map[string]any{"io.apteva/wakeOnResult": "on_error"},
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"channel", "text"},
				"properties": map[string]any{
					"channel":   map[string]any{"type": "string", "description": `Target channel. Use "chat" or an ntfy channel id from list_channels, e.g. "ntfy:<topic>".`},
					"text":      map[string]any{"type": "string", "description": "Message to deliver to the user."},
					"title":     map[string]any{"type": "string", "description": "Optional ntfy notification title."},
					"priority":  map[string]any{"description": "Optional ntfy priority: min, low, default, high, urgent, or 1-5."},
					"tags":      map[string]any{"type": "array", "description": "Optional ntfy tags.", "items": map[string]any{"type": "string"}},
					"click":     map[string]any{"type": "string", "description": "Optional ntfy click URL."},
					"agent_id":  map[string]any{"type": "integer", "description": "Optional fallback when caller metadata is unavailable."},
					"thread_id": map[string]any{"type": "string", "description": "Optional source thread id."},
					"components": map[string]any{
						"type":        "array",
						"description": "Optional chat attachments. V1 stores them with chat messages; dashboard support can mount them later.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"app", "name"},
							"properties": map[string]any{
								"app":   map[string]any{"type": "string"},
								"name":  map[string]any{"type": "string"},
								"props": map[string]any{"type": "object"},
							},
						},
					},
				},
			},
			HandlerCtx: a.toolRespond,
		},
		{
			Name:        "status",
			Description: "Send a status update to a channel. V1 writes status updates to chat as system messages.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"channel", "line"},
				"properties": map[string]any{
					"channel":  map[string]any{"type": "string"},
					"line":     map[string]any{"type": "string"},
					"level":    map[string]any{"type": "string", "enum": []string{"info", "warn", "alert"}},
					"agent_id": map[string]any{"type": "integer"},
				},
			},
			HandlerCtx: a.toolStatus,
		},
		{
			Name:        "list_channels",
			Description: "List currently reachable channels for the calling agent.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"agent_id": map[string]any{"type": "integer"}},
			},
			HandlerCtx: a.toolListChannels,
		},
	}
}

func respondDescription() string {
	return "Send a message to a user on a channel. Text in your thoughts is invisible; only this tool delivers messages.\n\n" +
		"Standalone Channels supports dashboard chat and ntfy channel ids returned by list_channels. Use channel=\"chat\" when the incoming event is tagged [chat].\n" +
		"The call is accepted only while the chat stream is connected. If rejected as unavailable, no user is currently reachable on chat.\n" +
		"The final response before going idle must include the actual outcome, not just an acknowledgement."
}

// --- HTTP routes -----------------------------------------------------------

func (a *App) handleChats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agentID := queryInt64(r, "agent_id", "instance_id")
		projectID := projectFromRequest(r)
		if agentID > 0 {
			projectID = a.projectForAgent(agentID, projectID)
			if _, err := a.store.EnsureDefaultChat(agentID, projectID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		chats, err := a.store.ListChats(agentID, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, chats)
	case http.MethodPost:
		var body struct {
			AgentID    int64  `json:"agent_id"`
			InstanceID int64  `json:"instance_id"`
			ProjectID  string `json:"project_id"`
			Title      string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.AgentID == 0 {
			body.AgentID = body.InstanceID
		}
		if body.AgentID == 0 {
			http.Error(w, "agent_id required", http.StatusBadRequest)
			return
		}
		projectID := strings.TrimSpace(body.ProjectID)
		if projectID == "" {
			projectID = projectFromRequest(r)
		}
		projectID = a.projectForAgent(body.AgentID, projectID)
		chat, err := a.store.EnsureDefaultChat(body.AgentID, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, chat)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agentID := queryInt64(r, "agent_id", "instance_id")
		projectID := projectFromRequest(r)
		if agentID > 0 {
			projectID = a.projectForAgent(agentID, projectID)
		}
		if _, err := a.store.EnsureProjectChatChannel(projectID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if agentID > 0 {
			if _, err := a.store.EnsureDefaultChat(agentID, projectID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		chats, err := a.store.ListChannelsForAgent(agentID, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"channels": a.channelSummaries(chats), "agent_id": agentID, "project_id": projectID})
	case http.MethodPost:
		var body struct {
			AgentID    int64  `json:"agent_id"`
			InstanceID int64  `json:"instance_id"`
			ProjectID  string `json:"project_id"`
			Name       string `json:"name"`
			Type       string `json:"type"`
			Topic      string `json:"topic"`
			Regenerate bool   `json:"regenerate"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.AgentID == 0 {
			body.AgentID = body.InstanceID
		}
		if body.Type == "" {
			body.Type = "ntfy"
		}
		if body.Type != "ntfy" && body.Type != "chat" {
			http.Error(w, `type must be "chat" or "ntfy"`, http.StatusBadRequest)
			return
		}
		projectID := strings.TrimSpace(body.ProjectID)
		if projectID == "" {
			projectID = projectFromRequest(r)
		}
		if body.AgentID > 0 {
			projectID = a.projectForAgent(body.AgentID, projectID)
		}
		if body.Type == "chat" {
			ch, err := a.store.EnsureProjectChatChannel(projectID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, a.channelRecordSummary(*ch))
			return
		}
		topic := strings.TrimSpace(body.Topic)
		if topic != "" && !validTopic(topic) {
			http.Error(w, "invalid topic", http.StatusBadRequest)
			return
		}
		_, existingErr := a.store.GetNtfyByTopic(topic)
		ch, err := a.store.UpsertNtfyChannel(body.AgentID, projectID, body.Name, topic)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.emitChannelChange(existingErr != nil, *ch)
		writeJSON(w, a.channelSummary(*ch))
	case http.MethodDelete:
		ch, ok := a.channelFromDeleteRequest(w, r)
		if !ok {
			return
		}
		deleted, err := a.store.DeleteChat(ch.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.emitChannelDeleted(*deleted)
		writeJSON(w, map[string]any{"deleted": true, "channel": a.channelSummary(*deleted)})
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		chatID, _, ok := a.authorizeChat(w, r)
		if !ok {
			return
		}
		since := queryInt64(r, "since")
		limit := int(queryInt64(r, "limit"))
		msgs, err := a.store.ListMessages(chatID, since, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, msgs)
	case http.MethodPost:
		a.postMessage(w, r)
	case http.MethodDelete:
		chatID, chat, ok := a.authorizeChat(w, r)
		if !ok {
			return
		}
		n, err := a.store.DeleteMessages(chatID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.emitMessageDeleted(*chat, n)
		writeJSON(w, map[string]int64{"deleted": n})
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) postMessage(w http.ResponseWriter, r *http.Request) {
	chatID, chat, ok := a.authorizeChat(w, r)
	if !ok {
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Content  string `json:"content"`
		ThreadID string `json:"thread_id"`
		Context  any    `json:"context"`
		UserID   int64  `json:"user_id"`
	}
	_ = json.Unmarshal(raw, &body)
	if strings.TrimSpace(body.Content) == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	var uid *int64
	if body.UserID > 0 {
		uid = &body.UserID
	}
	m, err := a.store.Append(chatID, "user", body.Content, uid, body.ThreadID, "final", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.publish(*m)

	ev := fmt.Sprintf("[chat] %s", body.Content)
	if ctx := formatDashboardContext(body.Context); ctx != "" {
		ev = fmt.Sprintf("[chat]\n%s\n\nUser message:\n%s", ctx, body.Content)
	}
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		if err := globalCtx.PlatformAPI().SendEvent(chat.AgentID, ev); err != nil {
			notice := fmt.Sprintf("(could not reach agent; your message is saved. err: %v)", err)
			if sm, sErr := a.store.Append(chatID, "system", notice, nil, "", "final", nil); sErr == nil {
				a.publish(*sm)
			}
		}
	}
	writeJSON(w, m)
}

func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	chatID, _, ok := a.authorizeChat(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	since := queryInt64(r, "since")
	backfill, err := a.store.ListMessages(chatID, since, 1000)
	if err == nil {
		for _, m := range backfill {
			writeSSE(w, m)
			if m.ID > since {
				since = m.ID
			}
		}
		flusher.Flush()
	}

	ch, cancel := a.hub.subscribe(chatID)
	defer cancel()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.ID <= since {
				continue
			}
			writeSSE(w, m)
			since = m.ID
			flusher.Flush()
		}
	}
}

func (a *App) handleUnreadSummary(w http.ResponseWriter, r *http.Request) {
	projectID := projectFromRequest(r)
	agentID := queryInt64(r, "agent_id", "instance_id")
	rows, err := a.store.Latest(projectID, agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}

func (a *App) handleSeen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID     string `json:"chat_id"`
		LastSeenID int64  `json:"last_seen_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.ChatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return
	}
	chat, err := a.store.GetChat(body.ChatID)
	if err != nil {
		http.Error(w, "chat not found", http.StatusNotFound)
		return
	}
	current, err := a.store.MarkSeen(body.ChatID, body.LastSeenID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.emitConversationSeen(*chat, current)
	writeJSON(w, map[string]int64{"last_seen_id": current})
}

func (a *App) handlePresence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID string `json:"chat_id"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	chat, err := a.store.GetChat(body.ChatID)
	if err != nil {
		http.Error(w, "chat not found", http.StatusNotFound)
		return
	}
	var text string
	switch body.Action {
	case "connected":
		text = "[chat] user connected to chat"
	case "disconnected":
		text = "[chat] user disconnected from chat"
	default:
		http.Error(w, `action must be "connected" or "disconnected"`, http.StatusBadRequest)
		return
	}
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		_ = globalCtx.PlatformAPI().SendEvent(chat.AgentID, text)
	}
	writeJSON(w, map[string]string{"status": "ok", "thread_id": chat.ThreadID})
}

func (a *App) handleNtfyRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "POST or PUT", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Topic    string   `json:"topic"`
		Message  string   `json:"message"`
		Title    string   `json:"title"`
		Priority string   `json:"priority"`
		Tags     []string `json:"tags"`
		Click    string   `json:"click"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	a.publishInboundNtfy(w, body.Topic, body.Message, ntfyMeta{
		Title:    body.Title,
		Priority: body.Priority,
		Tags:     cleanTags(body.Tags),
		Click:    body.Click,
	})
}

func (a *App) handleNtfy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/ntfy/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "topic required", http.StatusBadRequest)
		return
	}
	topic := parts[0]
	if !validTopic(topic) {
		http.Error(w, "invalid topic", http.StatusBadRequest)
		return
	}
	suffix := ""
	if len(parts) > 1 {
		suffix = parts[1]
	}
	switch r.Method {
	case http.MethodGet:
		switch suffix {
		case "json", "sse", "raw":
			a.streamNtfy(w, r, topic, suffix)
		case "":
			chat, err := a.store.GetNtfyByTopic(topic)
			if err != nil {
				http.Error(w, "topic not found", http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{
				"topic":          topic,
				"channel_id":     "ntfy:" + topic,
				"subscribe_json": "/ntfy/" + topic + "/json",
				"subscribe_sse":  "/ntfy/" + topic + "/sse",
				"agent_id":       chat.AgentID,
				"project_id":     chat.ProjectID,
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	case http.MethodPost, http.MethodPut:
		if suffix != "" {
			http.Error(w, "publish to /ntfy/{topic}", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		a.publishInboundNtfy(w, topic, string(body), ntfyMeta{
			Title:    headerAny(r, "Title", "X-Title"),
			Priority: headerAny(r, "Priority", "X-Priority", "X-Ntfy-Priority"),
			Tags:     splitTags(headerAny(r, "Tags", "Tag", "X-Tags")),
			Click:    headerAny(r, "Click", "X-Click"),
		})
	default:
		http.Error(w, "GET, POST or PUT", http.StatusMethodNotAllowed)
	}
}

func (a *App) publishInboundNtfy(w http.ResponseWriter, topic, message string, meta ntfyMeta) {
	topic = strings.TrimSpace(topic)
	message = strings.TrimSpace(message)
	if !validTopic(topic) {
		http.Error(w, "invalid topic", http.StatusBadRequest)
		return
	}
	if message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	chat, err := a.store.GetNtfyByTopic(topic)
	if err != nil {
		http.Error(w, "topic not found", http.StatusNotFound)
		return
	}
	m, err := a.store.Append(chat.ID, "user", message, nil, "", "final", ntfyComponents(meta))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.publish(*m)
	if chat.AgentID > 0 && globalCtx != nil && globalCtx.PlatformAPI() != nil {
		ev := fmt.Sprintf("[ntfy:%s] %s", topic, message)
		if meta.Title != "" {
			ev = fmt.Sprintf("[ntfy:%s] %s\n%s", topic, meta.Title, message)
		}
		if err := globalCtx.PlatformAPI().SendEvent(chat.AgentID, ev); err != nil {
			notice := fmt.Sprintf("(could not reach agent; ntfy message is saved. err: %v)", err)
			if sm, sErr := a.store.Append(chat.ID, "system", notice, nil, "", "final", nil); sErr == nil {
				a.publish(*sm)
			}
		}
	}
	w.Header().Set("X-Ntfy-Message-Id", strconv.FormatInt(m.ID, 10))
	writeJSON(w, ntfyEventFromMessage(topic, *m))
}

func (a *App) streamNtfy(w http.ResponseWriter, r *http.Request, topic, format string) {
	chat, err := a.store.GetNtfyByTopic(topic)
	if err != nil {
		http.Error(w, "topic not found", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/x-ndjson")
	case "sse":
		w.Header().Set("Content-Type", "text/event-stream")
	case "raw":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeNtfyEvent(w, format, ntfyEvent{Event: "open", Topic: topic})
	since := queryInt64(r, "since")
	backfill, err := a.store.ListMessages(chat.ID, since, 1000)
	if err == nil {
		for _, m := range backfill {
			writeNtfyEvent(w, format, ntfyEventFromMessage(topic, m))
			if m.ID > since {
				since = m.ID
			}
		}
	}
	flusher.Flush()

	ch, cancel := a.hub.subscribe(chat.ID)
	defer cancel()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			writeNtfyEvent(w, format, ntfyEvent{Event: "keepalive", Topic: topic, Time: time.Now().Unix()})
			flusher.Flush()
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.ID <= since {
				continue
			}
			writeNtfyEvent(w, format, ntfyEventFromMessage(topic, m))
			since = m.ID
			flusher.Flush()
		}
	}
}

// --- MCP handlers ----------------------------------------------------------

func (a *App) toolRespond(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	channel := normalizeChannel(strArg(args, "channel"))
	text := strArg(args, "text")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text required")
	}
	agentID := callerAgentID(ctx, args)
	if agentID == 0 {
		return nil, errors.New("agent_id required because caller metadata is unavailable")
	}
	if topic := ntfyTopicFromChannel(channel); topic != "" {
		chat, err := a.store.GetNtfyByTopic(topic)
		if err != nil {
			return nil, fmt.Errorf("ntfy channel %q not found", channel)
		}
		projectID := a.projectForAgent(agentID, app.CurrentProject())
		if chat.ProjectID != "" && projectID != "" && chat.ProjectID != projectID {
			return nil, fmt.Errorf("ntfy channel %q is not available in this project", channel)
		}
		if chat.AgentID != 0 && chat.AgentID != agentID {
			return nil, fmt.Errorf("ntfy channel %q is not available for agent %d", channel, agentID)
		}
		m, err := a.store.Append(chat.ID, "agent", text, nil, strArg(args, "thread_id"), "final", ntfyComponents(ntfyMetaFromArgs(args)))
		if err != nil {
			return nil, err
		}
		a.publish(*m)
		return map[string]any{"status": "published", "channel": "ntfy", "channel_id": "ntfy:" + topic, "topic": topic, "message_id": m.ID}, nil
	}
	if channel != "chat" {
		return nil, fmt.Errorf("channel %q is not available; use channel=\"chat\" or an ntfy channel id from list_channels", channel)
	}
	projectID := app.CurrentProject()
	projectID = a.projectForAgent(agentID, projectID)
	chat, err := a.store.EnsureDefaultChat(agentID, projectID)
	if err != nil {
		return nil, err
	}
	if !a.hub.hasSubscribers(chat.ID) {
		return nil, fmt.Errorf("channel %q is not currently connected for agent %d; no user is reachable", channel, agentID)
	}
	m, err := a.store.Append(chat.ID, "agent", text, nil, strArg(args, "thread_id"), "final", componentsFromArgs(args["components"]))
	if err != nil {
		return nil, err
	}
	a.publish(*m)
	return map[string]any{"status": "delivered", "channel": "chat", "message_id": m.ID}, nil
}

func (a *App) toolStatus(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	channel := normalizeChannel(strArg(args, "channel"))
	if channel != "chat" {
		return nil, fmt.Errorf("channel %q is not available in channels v1; use channel=\"chat\"", channel)
	}
	line := strArg(args, "line")
	if strings.TrimSpace(line) == "" {
		return nil, errors.New("line required")
	}
	level := strArg(args, "level")
	if level == "" {
		level = "info"
	}
	agentID := callerAgentID(ctx, args)
	if agentID == 0 {
		return nil, errors.New("agent_id required because caller metadata is unavailable")
	}
	projectID := a.projectForAgent(agentID, app.CurrentProject())
	chat, err := a.store.EnsureDefaultChat(agentID, projectID)
	if err != nil {
		return nil, err
	}
	m, err := a.store.Append(chat.ID, "system", "["+level+"] "+line, nil, "", "final", nil)
	if err != nil {
		return nil, err
	}
	a.publish(*m)
	return map[string]any{"status": "ok", "channel": "chat", "message_id": m.ID}, nil
}

func (a *App) toolListChannels(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(ctx, args)
	if agentID == 0 {
		return map[string]any{"channels": []any{}}, nil
	}
	projectID := a.projectForAgent(agentID, app.CurrentProject())
	if _, err := a.store.EnsureDefaultChat(agentID, projectID); err != nil {
		return nil, err
	}
	rows, err := a.store.ListChannelsForAgent(agentID, projectID)
	if err != nil {
		return nil, err
	}
	channels := []map[string]any{}
	seen := map[string]bool{}
	for _, row := range rows {
		summary := a.channelSummary(row)
		if row.Channel == "chat" {
			summary["active"] = a.hub.hasSubscribers(defaultChatID(agentID, projectID))
		}
		id, _ := summary["id"].(string)
		if seen[id] {
			continue
		}
		seen[id] = true
		channels = append(channels, summary)
	}
	var activeIDs []string
	if a.hub.hasSubscribers(defaultChatID(agentID, projectID)) {
		activeIDs = append(activeIDs, "chat")
	}
	for _, row := range rows {
		if row.Channel == "ntfy" && a.hub.hasSubscribers(row.ID) {
			activeIDs = append(activeIDs, "ntfy:"+row.ThreadID)
		}
	}
	return map[string]any{
		"channels":        channels,
		"active_channels": activeIDs,
		"agent_id":        agentID,
	}, nil
}

// --- helpers ---------------------------------------------------------------

type ntfyMeta struct {
	Title    string   `json:"title,omitempty"`
	Priority string   `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

type ntfyEvent struct {
	ID       string   `json:"id,omitempty"`
	Time     int64    `json:"time,omitempty"`
	Event    string   `json:"event"`
	Topic    string   `json:"topic"`
	Message  string   `json:"message,omitempty"`
	Title    string   `json:"title,omitempty"`
	Priority string   `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

func (a *App) channelSummaries(chats []Chat) []map[string]any {
	out := make([]map[string]any, 0, len(chats))
	for _, ch := range chats {
		out = append(out, a.channelSummary(ch))
	}
	return out
}

func (a *App) channelRecordSummary(ch ChannelRecord) map[string]any {
	item := map[string]any{
		"id":         ch.Type,
		"mcp_id":     ch.Type,
		"type":       ch.Type,
		"label":      ch.Name,
		"agent_id":   ch.DefaultAgentID,
		"project_id": ch.ProjectID,
		"active":     false,
		"channel_id": ch.ID,
		"chat_id":    "",
	}
	if ch.Type == "ntfy" {
		item["id"] = "ntfy:" + ch.Topic
		item["mcp_id"] = "ntfy:" + ch.Topic
		item["topic"] = ch.Topic
		item["subscribe_path"] = "/ntfy/" + ch.Topic
		item["stream_json"] = "/ntfy/" + ch.Topic + "/json"
		item["stream_sse"] = "/ntfy/" + ch.Topic + "/sse"
		item["capabilities"] = []string{"text", "title", "priority", "tags", "click"}
		return item
	}
	item["capabilities"] = []string{"text", "status", "components"}
	return item
}

func (a *App) channelSummary(ch Chat) map[string]any {
	item := map[string]any{
		"id":         channelIdentifier(ch),
		"mcp_id":     ch.Channel,
		"type":       ch.Channel,
		"label":      ch.Title,
		"agent_id":   ch.AgentID,
		"project_id": ch.ProjectID,
		"active":     a.hub.hasSubscribers(ch.ID),
		"channel_id": ch.ChannelID,
		"chat_id":    ch.ID,
	}
	if ch.Channel == "ntfy" {
		item["id"] = "ntfy:" + ch.ThreadID
		item["mcp_id"] = "ntfy:" + ch.ThreadID
		item["topic"] = ch.ThreadID
		item["subscribe_path"] = "/ntfy/" + ch.ThreadID
		item["stream_json"] = "/ntfy/" + ch.ThreadID + "/json"
		item["stream_sse"] = "/ntfy/" + ch.ThreadID + "/sse"
		item["capabilities"] = []string{"text", "title", "priority", "tags", "click"}
		return item
	}
	item["capabilities"] = []string{"text", "status", "components"}
	return item
}

func (a *App) authorizeChat(w http.ResponseWriter, r *http.Request) (string, *Chat, bool) {
	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		http.Error(w, "chat_id required", http.StatusBadRequest)
		return "", nil, false
	}
	chat, err := a.store.GetChat(chatID)
	if err != nil {
		http.Error(w, "chat not found", http.StatusNotFound)
		return "", nil, false
	}
	return chatID, chat, true
}

func (a *App) channelFromDeleteRequest(w http.ResponseWriter, r *http.Request) (*Chat, bool) {
	q := r.URL.Query()
	if chatID := strings.TrimSpace(q.Get("chat_id")); chatID != "" {
		ch, err := a.store.GetChat(chatID)
		if err != nil {
			http.Error(w, "channel not found", http.StatusNotFound)
			return nil, false
		}
		return ch, true
	}
	channelID := strings.TrimSpace(q.Get("channel_id"))
	if channelID == "" {
		channelID = strings.TrimSpace(q.Get("id"))
	}
	if topic := ntfyTopicFromChannel(channelID); topic != "" {
		ch, err := a.store.GetNtfyByTopic(topic)
		if err != nil {
			http.Error(w, "channel not found", http.StatusNotFound)
			return nil, false
		}
		return ch, true
	}
	if strings.HasPrefix(channelID, "chat:") {
		agentID := toInt64(strings.TrimPrefix(channelID, "chat:"))
		if agentID > 0 {
			ch, err := a.store.GetChat(defaultChatID(agentID, projectFromRequest(r)))
			if err != nil {
				http.Error(w, "channel not found", http.StatusNotFound)
				return nil, false
			}
			return ch, true
		}
	}
	http.Error(w, "chat_id or channel_id required", http.StatusBadRequest)
	return nil, false
}

func (a *App) projectForAgent(agentID int64, fallback string) string {
	if globalCtx == nil || globalCtx.PlatformAPI() == nil || agentID == 0 {
		return fallback
	}
	agent, err := globalCtx.PlatformAPI().GetAgent(agentID)
	if err != nil || agent == nil || agent.ProjectID == "" {
		return fallback
	}
	return agent.ProjectID
}

func (a *App) publish(m Message) {
	a.hub.publish(m)
	if globalCtx == nil {
		return
	}
	chat, err := a.store.GetChat(m.ChatID)
	if err != nil {
		globalCtx.Emit("message.created", m)
		return
	}
	globalCtx.Emit("message.created", messageCreatedPayload(*chat, m))
	globalCtx.Emit("conversation.updated", conversationPayload(*chat))
}

func (a *App) emitChannelChange(created bool, ch Chat) {
	if globalCtx == nil {
		return
	}
	topic := "channel.updated"
	if created {
		topic = "channel.created"
	}
	globalCtx.Emit(topic, channelPayload(ch))
}

func (a *App) emitChannelDeleted(ch Chat) {
	if globalCtx == nil {
		return
	}
	payload := channelPayload(ch)
	globalCtx.Emit("channel.deleted", payload)
	globalCtx.Emit("conversation.deleted", conversationPayload(ch))
}

func (a *App) emitConversationSeen(ch Chat, lastSeenID int64) {
	if globalCtx == nil {
		return
	}
	payload := conversationPayload(ch)
	payload["last_seen_id"] = lastSeenID
	globalCtx.Emit("conversation.seen", payload)
}

func (a *App) emitMessageDeleted(ch Chat, deleted int64) {
	if globalCtx == nil {
		return
	}
	payload := conversationPayload(ch)
	payload["deleted"] = deleted
	globalCtx.Emit("message.deleted", payload)
	globalCtx.Emit("conversation.updated", conversationPayload(ch))
}

func channelPayload(ch Chat) map[string]any {
	return map[string]any{
		"channel_id":       channelIdentifier(ch),
		"channel_type":     ch.Channel,
		"name":             ch.Title,
		"project_id":       ch.ProjectID,
		"visibility":       "project",
		"inbound_enabled":  ch.AgentID > 0,
		"default_agent_id": nullableAgentID(ch.AgentID),
		"conversation_id":  ch.ID,
		"topic":            channelTopic(ch),
	}
}

func conversationPayload(ch Chat) map[string]any {
	return map[string]any{
		"conversation_id": ch.ID,
		"channel_id":      channelIdentifier(ch),
		"channel_type":    ch.Channel,
		"project_id":      ch.ProjectID,
		"agent_id":        nullableAgentID(ch.AgentID),
		"title":           ch.Title,
		"topic":           channelTopic(ch),
		"updated_at":      ch.UpdatedAt,
	}
}

func messageCreatedPayload(ch Chat, m Message) map[string]any {
	return map[string]any{
		"message_id":      m.ID,
		"conversation_id": ch.ID,
		"channel_id":      channelIdentifier(ch),
		"channel_type":    ch.Channel,
		"project_id":      ch.ProjectID,
		"agent_id":        nullableAgentID(ch.AgentID),
		"role":            m.Role,
		"direction":       directionForRole(m.Role),
		"text":            m.Content,
		"status":          m.Status,
		"thread_id":       m.ThreadID,
		"created_at":      m.CreatedAt,
		"topic":           channelTopic(ch),
	}
}

func channelIdentifier(ch Chat) string {
	switch ch.Channel {
	case "ntfy":
		return "ntfy:" + ch.ThreadID
	case "chat":
		return "chat"
	default:
		return ch.Channel
	}
}

func channelTopic(ch Chat) string {
	if ch.Channel == "ntfy" {
		return ch.ThreadID
	}
	return ""
}

func nullableAgentID(agentID int64) any {
	if agentID <= 0 {
		return nil
	}
	return agentID
}

func directionForRole(role string) string {
	switch role {
	case "user":
		return "inbound"
	case "agent":
		return "outbound"
	default:
		return "internal"
	}
}

func callerAgentID(ctx context.Context, args map[string]any) int64 {
	if caller := sdk.CallerFrom(ctx); caller != nil && caller.AgentID > 0 {
		return caller.AgentID
	}
	return toInt64(args["agent_id"])
}

func normalizeChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return ""
	}
	if strings.HasPrefix(channel, "chat:") {
		return "chat"
	}
	return channel
}

func ntfyTopicFromChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if strings.HasPrefix(channel, "ntfy:") {
		topic := strings.TrimPrefix(channel, "ntfy:")
		if validTopic(topic) {
			return topic
		}
	}
	return ""
}

func randomTopic(agentID int64) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		if agentID > 0 {
			return fmt.Sprintf("agent-%d-%d", agentID, time.Now().UnixNano())
		}
		return fmt.Sprintf("channel-%d", time.Now().UnixNano())
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	if agentID > 0 {
		return fmt.Sprintf("agent-%d-%s", agentID, string(out))
	}
	return "channel-" + string(out)
}

func validTopic(topic string) bool {
	if len(topic) < 3 || len(topic) > 128 {
		return false
	}
	for _, r := range topic {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

func ntfyMetaFromArgs(args map[string]any) ntfyMeta {
	return ntfyMeta{
		Title:    strArg(args, "title"),
		Priority: priorityString(args["priority"]),
		Tags:     tagsFromAny(args["tags"]),
		Click:    strArg(args, "click"),
	}
}

func ntfyComponents(meta ntfyMeta) []ChatComponent {
	meta.Title = strings.TrimSpace(meta.Title)
	meta.Priority = priorityString(meta.Priority)
	meta.Tags = cleanTags(meta.Tags)
	meta.Click = strings.TrimSpace(meta.Click)
	if meta.Title == "" && meta.Priority == "" && len(meta.Tags) == 0 && meta.Click == "" {
		return nil
	}
	return []ChatComponent{{App: "channels", Name: "ntfy", Props: map[string]any{
		"title":    meta.Title,
		"priority": meta.Priority,
		"tags":     meta.Tags,
		"click":    meta.Click,
	}}}
}

func ntfyMetaFromMessage(m Message) ntfyMeta {
	for _, c := range m.Components {
		if c.App != "channels" || c.Name != "ntfy" {
			continue
		}
		return ntfyMeta{
			Title:    stringFromProp(c.Props, "title"),
			Priority: stringFromProp(c.Props, "priority"),
			Tags:     tagsFromAny(c.Props["tags"]),
			Click:    stringFromProp(c.Props, "click"),
		}
	}
	return ntfyMeta{}
}

func ntfyEventFromMessage(topic string, m Message) ntfyEvent {
	meta := ntfyMetaFromMessage(m)
	return ntfyEvent{
		ID:       strconv.FormatInt(m.ID, 10),
		Time:     m.CreatedAt.Unix(),
		Event:    "message",
		Topic:    topic,
		Message:  m.Content,
		Title:    meta.Title,
		Priority: meta.Priority,
		Tags:     meta.Tags,
		Click:    meta.Click,
	}
}

func writeNtfyEvent(w io.Writer, format string, ev ntfyEvent) {
	if ev.Time == 0 && ev.Event != "open" {
		ev.Time = time.Now().Unix()
	}
	switch format {
	case "raw":
		if ev.Event == "message" {
			_, _ = io.WriteString(w, ev.Message+"\n")
		}
	case "sse":
		body, _ := json.Marshal(ev)
		_, _ = io.WriteString(w, "event: "+ev.Event+"\n")
		_, _ = io.WriteString(w, "data: ")
		_, _ = w.Write(body)
		_, _ = io.WriteString(w, "\n\n")
	default:
		body, _ := json.Marshal(ev)
		_, _ = w.Write(body)
		_, _ = io.WriteString(w, "\n")
	}
}

func componentsFromArgs(raw any) []ChatComponent {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]ChatComponent, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		app, _ := obj["app"].(string)
		name, _ := obj["name"].(string)
		if app == "" || name == "" {
			continue
		}
		props, _ := obj["props"].(map[string]any)
		out = append(out, ChatComponent{App: app, Name: name, Props: props})
	}
	return out
}

func formatDashboardContext(v any) string {
	ctx, ok := v.(map[string]any)
	if !ok || len(ctx) == 0 {
		return ""
	}
	source, _ := ctx["source"].(string)
	if source != "dashboard-floating" {
		return ""
	}
	pick := func(key string) string {
		if s, ok := ctx[key].(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	var lines []string
	lines = append(lines, "Dashboard context:")
	for _, item := range []struct {
		label string
		key   string
	}{
		{"page", "title"},
		{"route", "route"},
		{"project", "project_name"},
		{"project_id", "project_id"},
		{"kind", "page_kind"},
		{"detail", "detail"},
	} {
		if val := pick(item.key); val != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", item.label, val))
		}
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func projectFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v
	}
	if globalCtx != nil && globalCtx.CurrentProject() != "" {
		return globalCtx.CurrentProject()
	}
	return os.Getenv("APTEVA_PROJECT_ID")
}

func queryInt64(r *http.Request, keys ...string) int64 {
	for _, key := range keys {
		raw := strings.TrimSpace(r.URL.Query().Get(key))
		if raw == "" {
			continue
		}
		n, _ := strconv.ParseInt(raw, 10, 64)
		return n
	}
	return 0
}

func strArg(args map[string]any, key string) string {
	if s, ok := args[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func headerAny(r *http.Request, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(r.Header.Get(key)); v != "" {
			return v
		}
	}
	return ""
}

func stringFromProp(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	if s, ok := props[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func splitTags(raw string) []string {
	if raw == "" {
		return nil
	}
	return cleanTags(strings.Split(raw, ","))
}

func tagsFromAny(v any) []string {
	switch x := v.(type) {
	case []string:
		return cleanTags(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return cleanTags(out)
	case string:
		return splitTags(x)
	default:
		return nil
	}
}

func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func priorityString(v any) string {
	switch x := v.(type) {
	case string:
		x = strings.TrimSpace(strings.ToLower(x))
		switch x {
		case "min", "1":
			return "1"
		case "low", "2":
			return "2"
		case "default", "", "3":
			return ""
		case "high", "4":
			return "4"
		case "urgent", "max", "5":
			return "5"
		default:
			return x
		}
	case float64:
		n := int(x)
		if n >= 1 && n <= 5 {
			return strconv.Itoa(n)
		}
	case int:
		if x >= 1 && x <= 5 {
			return strconv.Itoa(x)
		}
	case int64:
		if x >= 1 && x <= 5 {
			return strconv.FormatInt(x, 10)
		}
	}
	return ""
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, m Message) {
	body, _ := json.Marshal(m)
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(body)
	_, _ = io.WriteString(w, "\n\n")
}

func main() {
	sdk.Run(&App{})
}
