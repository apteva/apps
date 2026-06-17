// Channels app v0.2.
//
// This is the standalone version of the platform chat channel: it owns
// chat persistence, REST/SSE delivery, and the agent-facing MCP tools.
// The only platform dependency is app-sdk's PlatformAPI for forwarding
// inbound user messages to the owning agent.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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

var (
	ntfyHTTPClient      = &http.Client{Timeout: 10 * time.Second}
	telegramHTTPClient  = &http.Client{Timeout: 10 * time.Second}
	defaultNtfyProvider = "https://ntfy.sh"
	telegramAPIBase     = "https://api.telegram.org"
)

const manifestYAML = `schema: apteva-app/v1
name: channels
display_name: Channels
version: 0.6.0
description: |
  Agent-facing channel router with standalone dashboard chat, a visible inbox, project channel management, ntfy.sh notifications, and Telegram channels.
  Agents reply through respond(channel="chat", ...); the app stores chat
  history, streams updates to the dashboard, delivers provider-backed
  notifications, and forwards inbound user messages/actions to agents.
author: Apteva
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/channels/icon.svg
scopes: [global]
requires:
  permissions:
    - db.write.app
    - platform.instances.read
    - platform.instances.write
    - platform.connections.read
    - platform.connections.read_credentials
  integrations:
    - role: telegram_bot
      kind: integration
      compatible_slugs: [telegram, telegram-bot]
      capabilities: []
      required: false
      label: "Telegram bot (optional)"
      hint: "Bind a Telegram bot connection if available, or paste a BotFather token directly when creating a Telegram channel."
provides:
  http_routes:
    - prefix: /
    - prefix: /ntfy
      no_auth: true
    - prefix: /ntfy/
      no_auth: true
    - prefix: /telegram-webhook/
      no_auth: true
  mcp_tools:
    - name: respond
      description: Send a user-visible reply to a channel. Supports channel="chat", ntfy channel ids, and Telegram channel ids from list_channels.
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
config_schema:
  - name: ntfy_provider_url
    label: ntfy provider URL
    type: text
    default: https://ntfy.sh
    description: ntfy server used for notification delivery. Use https://ntfy.sh for native iOS notifications.
  - name: ntfy_provider_access_token
    label: ntfy provider access token
    type: password
    description: Optional bearer token for the configured ntfy provider.
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
		{Pattern: "/telegram", Handler: a.handleTelegram},
		{Pattern: "/telegram/", Handler: a.handleTelegram},
		{Pattern: "/telegram-webhook/", Handler: a.handleTelegramWebhook, NoAuth: true},
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
					"channel":  map[string]any{"type": "string", "description": `Target channel. Use "chat" or a channel id from list_channels, e.g. "ntfy:<topic>" or "telegram:<topic>".`},
					"text":     map[string]any{"type": "string", "description": "Message to deliver to the user."},
					"title":    map[string]any{"type": "string", "description": "Optional notification title."},
					"priority": map[string]any{"description": "Optional ntfy priority: min, low, default, high, urgent, or 1-5."},
					"tags":     map[string]any{"type": "array", "description": "Optional ntfy tags.", "items": map[string]any{"type": "string"}},
					"click":    map[string]any{"type": "string", "description": "Optional ntfy click URL."},
					"actions": map[string]any{
						"type":        "array",
						"description": `Optional buttons/actions. Telegram supports type="callback" and type="url"; ntfy supports view/http/copy when mapped later.`,
						"items": map[string]any{
							"type":     "object",
							"required": []string{"label", "type"},
							"properties": map[string]any{
								"type":   map[string]any{"type": "string", "enum": []string{"callback", "url", "view", "http", "copy"}},
								"label":  map[string]any{"type": "string"},
								"value":  map[string]any{"type": "string", "description": "Opaque callback value sent back to the agent."},
								"url":    map[string]any{"type": "string"},
								"method": map[string]any{"type": "string"},
								"body":   map[string]any{"type": "string"},
							},
						},
					},
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
		"Standalone Channels supports dashboard chat, ntfy channel ids, and Telegram channel ids returned by list_channels. Use channel=\"chat\" when the incoming event is tagged [chat].\n" +
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
			AgentID      int64  `json:"agent_id"`
			InstanceID   int64  `json:"instance_id"`
			ProjectID    string `json:"project_id"`
			Name         string `json:"name"`
			Type         string `json:"type"`
			Topic        string `json:"topic"`
			Regenerate   bool   `json:"regenerate"`
			ChatID       string `json:"chat_id"`
			BotToken     string `json:"bot_token"`
			ConnectionID int64  `json:"connection_id"`
			ParseMode    string `json:"parse_mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.AgentID == 0 {
			body.AgentID = body.InstanceID
		}
		if body.Type == "" {
			body.Type = "ntfy"
		}
		if body.Type != "ntfy" && body.Type != "chat" && body.Type != "telegram" {
			http.Error(w, `type must be "chat", "ntfy", or "telegram"`, http.StatusBadRequest)
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
		if body.Type == "telegram" {
			if strings.TrimSpace(body.ChatID) == "" {
				http.Error(w, "telegram chat_id required", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(body.BotToken) == "" && body.ConnectionID == 0 {
				http.Error(w, "telegram bot_token or connection_id required", http.StatusBadRequest)
				return
			}
			_, existingErr := a.store.GetTelegramByTopic(topic)
			ch, err := a.store.UpsertTelegramChannel(body.AgentID, projectID, body.Name, TelegramConfig{
				Topic:        topic,
				ChatID:       body.ChatID,
				BotToken:     body.BotToken,
				ConnectionID: body.ConnectionID,
				ParseMode:    body.ParseMode,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			a.emitChannelChange(existingErr != nil, *ch)
			writeJSON(w, a.channelSummary(*ch))
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
		case "auth":
			if _, err := a.store.GetNtfyByTopic(topic); err != nil {
				http.Error(w, "topic not found", http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"success": true})
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
				"urls":           a.ntfyURLFields(topic),
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
	if err := a.sendNtfyProvider(topic, meta, message); err != nil {
		http.Error(w, "ntfy provider delivery failed: "+err.Error(), http.StatusBadGateway)
		return
	}
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
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/x-ndjson")
	case "sse":
		w.Header().Set("Content-Type", "text/event-stream")
	case "raw":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	if id := queryInt64(r, "id"); id > 0 {
		m, err := a.store.GetMessage(id)
		if err != nil || m.ChatID != chat.ID {
			http.Error(w, "message not found", http.StatusNotFound)
			return
		}
		writeJSON(w, ntfyEventFromMessage(topic, *m))
		return
	}
	if r.URL.Query().Get("poll") == "1" {
		since := queryInt64(r, "since")
		backfill, err := a.store.ListMessages(chat.ID, since, 1000)
		if err == nil {
			for _, m := range backfill {
				writeNtfyEvent(w, format, ntfyEventFromMessage(topic, m))
			}
		}
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
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

func (a *App) handleTelegram(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/telegram"), "/")
	switch {
	case r.Method == http.MethodGet && rest == "connections":
		a.handleTelegramConnections(w, r)
	case r.Method == http.MethodPost && rest == "test":
		var body struct {
			Topic   string          `json:"topic"`
			Title   string          `json:"title"`
			Message string          `json:"message"`
			Actions []ChannelAction `json:"actions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		topic := strings.TrimSpace(body.Topic)
		message := strings.TrimSpace(body.Message)
		if !validTopic(topic) || message == "" {
			http.Error(w, "topic and message required", http.StatusBadRequest)
			return
		}
		chat, err := a.store.GetTelegramByTopic(topic)
		if err != nil {
			http.Error(w, "telegram channel not found", http.StatusNotFound)
			return
		}
		outText := message
		if strings.TrimSpace(body.Title) != "" {
			outText = strings.TrimSpace(body.Title) + "\n" + message
		}
		m, err := a.store.Append(chat.ID, "agent", message, nil, "", "final", telegramComponents(body.Actions, 0, body.Title))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.publish(*m)
		if err := a.sendTelegram(topic, outText, m.ID, body.Actions); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"status": "sent", "message_id": m.ID})
	case r.Method == http.MethodPost && rest == "register-webhook":
		var body struct {
			Topic string `json:"topic"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		topic := strings.TrimSpace(body.Topic)
		if !validTopic(topic) {
			http.Error(w, "valid topic required", http.StatusBadRequest)
			return
		}
		url, _ := a.telegramURLFields(topic)["webhook_url"].(string)
		if !strings.HasPrefix(url, "https://") {
			http.Error(w, "public https URL required for Telegram webhook", http.StatusBadRequest)
			return
		}
		cfg, err := a.store.GetTelegramConfigByTopic(topic)
		if err != nil {
			http.Error(w, "telegram channel not found", http.StatusNotFound)
			return
		}
		token, err := a.telegramBotToken(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := telegramSetWebhook(token, url); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"status": "registered", "webhook_url": url})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleTelegramConnections(w http.ResponseWriter, r *http.Request) {
	type conn struct {
		ID      int64  `json:"id"`
		AppSlug string `json:"app_slug"`
		Name    string `json:"name"`
		Status  string `json:"status"`
	}
	out := []conn{}
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		for _, slug := range []string{"telegram", "telegram-bot"} {
			rows, err := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: projectFromRequest(r), AppSlug: slug})
			if err != nil {
				continue
			}
			for _, c := range rows {
				out = append(out, conn{ID: c.ID, AppSlug: c.AppSlug, Name: c.Name, Status: c.Status})
			}
		}
	}
	writeJSON(w, map[string]any{"connections": out})
}

func (a *App) handleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	topic := strings.Trim(strings.TrimPrefix(r.URL.Path, "/telegram-webhook/"), "/")
	if !validTopic(topic) {
		http.Error(w, "invalid topic", http.StatusBadRequest)
		return
	}
	if err := a.processTelegramUpdate(topic, r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
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
		meta := ntfyMetaFromArgs(args)
		if err := a.sendNtfyProvider(topic, meta, text); err != nil {
			return nil, fmt.Errorf("ntfy provider delivery failed after saving message %d: %w", m.ID, err)
		}
		return map[string]any{"status": "delivered", "channel": "ntfy", "channel_id": "ntfy:" + topic, "topic": topic, "message_id": m.ID, "provider_url": ntfyProviderURL()}, nil
	}
	if topic := telegramTopicFromChannel(channel); topic != "" {
		chat, err := a.store.GetTelegramByTopic(topic)
		if err != nil {
			return nil, fmt.Errorf("telegram channel %q not found", channel)
		}
		projectID := a.projectForAgent(agentID, app.CurrentProject())
		if chat.ProjectID != "" && projectID != "" && chat.ProjectID != projectID {
			return nil, fmt.Errorf("telegram channel %q is not available in this project", channel)
		}
		if chat.AgentID != 0 && chat.AgentID != agentID {
			return nil, fmt.Errorf("telegram channel %q is not available for agent %d", channel, agentID)
		}
		title := strArg(args, "title")
		outText := text
		if title != "" {
			outText = title + "\n" + text
		}
		actions := actionsFromAny(args["actions"])
		m, err := a.store.Append(chat.ID, "agent", text, nil, strArg(args, "thread_id"), "final", telegramComponents(actions, agentID, title))
		if err != nil {
			return nil, err
		}
		a.publish(*m)
		if err := a.sendTelegram(topic, outText, m.ID, actions); err != nil {
			return nil, fmt.Errorf("telegram delivery failed after saving message %d: %w", m.ID, err)
		}
		return map[string]any{"status": "delivered", "channel": "telegram", "channel_id": "telegram:" + topic, "topic": topic, "message_id": m.ID}, nil
	}
	if channel != "chat" {
		return nil, fmt.Errorf("channel %q is not available; use channel=\"chat\" or a channel id from list_channels", channel)
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

type ChannelAction struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Value  string `json:"value,omitempty"`
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
	Body   string `json:"body,omitempty"`
}

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

type telegramUpdate struct {
	Message       *telegramMessage       `json:"message"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query"`
}

type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	Text      string       `json:"text"`
	From      telegramUser `json:"from"`
	Chat      telegramChat `json:"chat"`
}

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	Data    string           `json:"data"`
	From    telegramUser     `json:"from"`
	Message *telegramMessage `json:"message"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type telegramChat struct {
	ID int64 `json:"id"`
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
		for k, v := range a.ntfyURLFields(ch.Topic) {
			item[k] = v
		}
		item["capabilities"] = []string{"text", "title", "priority", "tags", "click"}
		return item
	}
	if ch.Type == "telegram" {
		item["id"] = "telegram:" + ch.Topic
		item["mcp_id"] = "telegram:" + ch.Topic
		item["topic"] = ch.Topic
		if cfg, err := a.store.GetTelegramConfigByTopic(ch.Topic); err == nil {
			item["telegram_chat_id"] = cfg.ChatID
			item["connection_id"] = cfg.ConnectionID
			item["parse_mode"] = cfg.ParseMode
		}
		for k, v := range a.telegramURLFields(ch.Topic) {
			item[k] = v
		}
		item["capabilities"] = []string{"text", "buttons", "links", "callback_actions"}
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
		for k, v := range a.ntfyURLFields(ch.ThreadID) {
			item[k] = v
		}
		item["capabilities"] = []string{"text", "title", "priority", "tags", "click"}
		return item
	}
	if ch.Channel == "telegram" {
		item["id"] = "telegram:" + ch.ThreadID
		item["mcp_id"] = "telegram:" + ch.ThreadID
		item["topic"] = ch.ThreadID
		if cfg, err := a.store.GetTelegramConfigByTopic(ch.ThreadID); err == nil {
			item["telegram_chat_id"] = cfg.ChatID
			item["connection_id"] = cfg.ConnectionID
			item["parse_mode"] = cfg.ParseMode
		}
		for k, v := range a.telegramURLFields(ch.ThreadID) {
			item[k] = v
		}
		item["capabilities"] = []string{"text", "buttons", "links", "callback_actions"}
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
	if topic := telegramTopicFromChannel(channelID); topic != "" {
		ch, err := a.store.GetTelegramByTopic(topic)
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
	case "telegram":
		return "telegram:" + ch.ThreadID
	case "chat":
		return "chat"
	default:
		return ch.Channel
	}
}

func channelTopic(ch Chat) string {
	if ch.Channel == "ntfy" || ch.Channel == "telegram" {
		return ch.ThreadID
	}
	return ""
}

func (a *App) telegramURLFields(topic string) map[string]any {
	path := "/api/apps/channels/telegram-webhook/" + topic
	out := map[string]any{"webhook_url": path}
	if globalCtx == nil {
		return out
	}
	info, err := globalCtx.PlatformInfo()
	if err != nil || info == nil || strings.TrimSpace(info.PublicURL) == "" {
		return out
	}
	out["webhook_url"] = strings.TrimRight(strings.TrimSpace(info.PublicURL), "/") + path
	return out
}

func (a *App) ntfyURLFields(topic string) map[string]any {
	root := "/api/apps/channels/ntfy/" + topic
	serverRoot := "/api/apps/channels/ntfy"
	provider := ntfyProviderURL()
	providerTopicURL := ""
	if provider != "" {
		providerTopicURL = provider + "/" + topic
	}
	out := map[string]any{
		"server_url":                provider,
		"subscribe_url":             providerTopicURL,
		"provider_url":              provider,
		"provider_topic_url":        providerTopicURL,
		"local_server_url":          serverRoot,
		"local_subscribe_url":       root,
		"local_stream_json_url":     root + "/json",
		"local_stream_sse_url":      root + "/sse",
		"stream_json_url":           root + "/json",
		"stream_sse_url":            root + "/sse",
		"delivery_provider":         "ntfy",
		"delivery_provider_default": provider == "https://ntfy.sh",
	}
	if globalCtx == nil {
		return out
	}
	info, err := globalCtx.PlatformInfo()
	if err != nil || info == nil || strings.TrimSpace(info.PublicURL) == "" {
		return out
	}
	base := strings.TrimRight(strings.TrimSpace(info.PublicURL), "/")
	out["local_server_url"] = base + serverRoot
	out["local_subscribe_url"] = base + root
	out["stream_json_url"] = base + root + "/json"
	out["stream_sse_url"] = base + root + "/sse"
	out["local_stream_json_url"] = base + root + "/json"
	out["local_stream_sse_url"] = base + root + "/sse"
	return out
}

func (a *App) sendNtfyProvider(topic string, meta ntfyMeta, message string) error {
	return sendNtfyProvider(ntfyProviderURL(), ntfyProviderToken(), topic, meta, message)
}

func (a *App) ntfyPublicTopicURL(topic string) string {
	fields := a.ntfyURLFields(topic)
	raw, _ := fields["subscribe_url"].(string)
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return raw
	}
	return ""
}

func ntfyProviderURL() string {
	provider := defaultNtfyProvider
	if globalCtx != nil && globalCtx.Config() != nil {
		if v := strings.TrimSpace(globalCtx.Config().Get("ntfy_provider_url")); v != "" {
			provider = v
		}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "off", "false", "disabled", "none":
		return ""
	default:
		return strings.TrimRight(strings.TrimSpace(provider), "/")
	}
}

func ntfyProviderToken() string {
	if globalCtx == nil || globalCtx.Config() == nil {
		return ""
	}
	return strings.TrimSpace(globalCtx.Config().Get("ntfy_provider_access_token"))
}

func sendNtfyProvider(provider, token, topic string, meta ntfyMeta, message string) error {
	provider = strings.TrimRight(strings.TrimSpace(provider), "/")
	if provider == "" {
		return errors.New("ntfy provider disabled")
	}
	if !validTopic(topic) {
		return errors.New("invalid topic")
	}
	req, err := http.NewRequest(http.MethodPost, provider+"/"+topic, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if meta.Title = strings.TrimSpace(meta.Title); meta.Title != "" {
		req.Header.Set("Title", meta.Title)
	}
	if meta.Priority = priorityString(meta.Priority); meta.Priority != "" {
		req.Header.Set("Priority", meta.Priority)
	}
	if tags := cleanTags(meta.Tags); len(tags) > 0 {
		req.Header.Set("Tags", strings.Join(tags, ","))
	}
	if meta.Click = strings.TrimSpace(meta.Click); meta.Click != "" {
		req.Header.Set("Click", meta.Click)
	}
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := ntfyHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func sendNtfyIOSPollRequest(upstream, token, topicURL string, messageID int64) error {
	sum := sha256.Sum256([]byte(strings.TrimSpace(topicURL)))
	upstreamTopic := fmt.Sprintf("%x", sum[:])
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(upstream, "/")+"/"+upstreamTopic, strings.NewReader("New message"))
	if err != nil {
		return err
	}
	req.Header.Set("X-Poll-ID", strconv.FormatInt(messageID, 10))
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := ntfyHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
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

func (a *App) sendTelegram(topic, message string, messageID int64, actions []ChannelAction) error {
	cfg, err := a.store.GetTelegramConfigByTopic(topic)
	if err != nil {
		return err
	}
	token, err := a.telegramBotToken(cfg)
	if err != nil {
		return err
	}
	return telegramSendMessage(token, cfg.ChatID, message, cfg.ParseMode, messageID, actions)
}

func (a *App) telegramBotToken(cfg TelegramConfig) (string, error) {
	if token := strings.TrimSpace(cfg.BotToken); token != "" {
		return token, nil
	}
	if cfg.ConnectionID <= 0 {
		return "", errors.New("telegram bot token is not configured")
	}
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		return "", errors.New("telegram connection credentials are unavailable")
	}
	creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(cfg.ConnectionID)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"bot_token", "telegram_bot_token", "token", "api_token", "access_token"} {
		if token := strings.TrimSpace(creds.Fields[key]); token != "" {
			return token, nil
		}
	}
	return "", errors.New("telegram connection has no bot_token credential")
}

func telegramSendMessage(token, chatID, text, parseMode string, messageID int64, actions []ChannelAction) error {
	token = strings.TrimSpace(token)
	chatID = strings.TrimSpace(chatID)
	text = strings.TrimSpace(text)
	if token == "" {
		return errors.New("telegram bot token required")
	}
	if chatID == "" {
		return errors.New("telegram chat_id required")
	}
	if text == "" {
		return errors.New("telegram message required")
	}
	body := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode = strings.TrimSpace(parseMode); parseMode != "" {
		body["parse_mode"] = parseMode
	}
	if keyboard := telegramInlineKeyboard(messageID, actions); len(keyboard) > 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}
	return telegramPost(token, "sendMessage", body)
}

func telegramSetWebhook(token, webhookURL string) error {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return errors.New("telegram webhook_url required")
	}
	return telegramPost(token, "setWebhook", map[string]any{"url": webhookURL})
}

func telegramAnswerCallback(token, callbackID, text string) error {
	callbackID = strings.TrimSpace(callbackID)
	if callbackID == "" {
		return nil
	}
	body := map[string]any{"callback_query_id": callbackID}
	if text = strings.TrimSpace(text); text != "" {
		body["text"] = text
	}
	return telegramPost(token, "answerCallbackQuery", body)
}

func telegramPost(token, method string, payload map[string]any) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("telegram bot token required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(telegramAPIBase, "/") + "/bot" + token + "/" + method
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var api telegramAPIResponse
	_ = json.Unmarshal(limited, &api)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, strings.TrimSpace(string(limited)))
	}
	if !api.OK {
		if api.Description != "" {
			return errors.New(api.Description)
		}
		return errors.New("telegram returned ok=false")
	}
	return nil
}

func telegramInlineKeyboard(messageID int64, actions []ChannelAction) [][]map[string]string {
	actions = cleanActions(actions)
	if len(actions) == 0 {
		return nil
	}
	row := make([]map[string]string, 0, len(actions))
	for i, action := range actions {
		switch action.Type {
		case "url", "view":
			if action.URL == "" {
				continue
			}
			row = append(row, map[string]string{"text": action.Label, "url": action.URL})
		case "callback":
			if messageID <= 0 {
				continue
			}
			row = append(row, map[string]string{"text": action.Label, "callback_data": fmt.Sprintf("act:%d:%d", messageID, i)})
		}
	}
	if len(row) == 0 {
		return nil
	}
	return [][]map[string]string{row}
}

func (a *App) processTelegramUpdate(topic string, r io.Reader) error {
	chat, err := a.store.GetTelegramByTopic(topic)
	if err != nil {
		return fmt.Errorf("telegram channel not found")
	}
	var update telegramUpdate
	if err := json.NewDecoder(r).Decode(&update); err != nil {
		return fmt.Errorf("invalid telegram update: %w", err)
	}
	switch {
	case update.CallbackQuery != nil:
		return a.processTelegramCallback(topic, *chat, *update.CallbackQuery)
	case update.Message != nil:
		return a.processTelegramMessage(topic, *chat, *update.Message)
	default:
		return nil
	}
}

func (a *App) processTelegramMessage(topic string, chat Chat, msg telegramMessage) error {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return nil
	}
	m, err := a.store.Append(chat.ID, "user", text, nil, "", "final", []ChatComponent{{App: "channels", Name: "telegram_message", Props: map[string]any{
		"telegram_user_id": msg.From.ID,
		"username":         msg.From.Username,
		"first_name":       msg.From.FirstName,
		"last_name":        msg.From.LastName,
		"telegram_chat_id": msg.Chat.ID,
		"telegram_message": msg.MessageID,
	}}})
	if err != nil {
		return err
	}
	a.publish(*m)
	if chat.AgentID > 0 && globalCtx != nil && globalCtx.PlatformAPI() != nil {
		ev := fmt.Sprintf("[telegram:%s] %s", topic, text)
		if err := globalCtx.PlatformAPI().SendEvent(chat.AgentID, ev); err != nil {
			notice := fmt.Sprintf("(could not reach agent; Telegram message is saved. err: %v)", err)
			if sm, sErr := a.store.Append(chat.ID, "system", notice, nil, "", "final", nil); sErr == nil {
				a.publish(*sm)
			}
		}
	}
	return nil
}

func (a *App) processTelegramCallback(topic string, chat Chat, cb telegramCallbackQuery) error {
	messageID, actionIndex, ok := parseTelegramCallbackData(cb.Data)
	if !ok {
		return nil
	}
	original, err := a.store.GetMessage(messageID)
	if err != nil {
		return err
	}
	actions, originalAgentID, _ := telegramActionsFromMessage(*original)
	if actionIndex < 0 || actionIndex >= len(actions) {
		return nil
	}
	action := actions[actionIndex]
	content := "Telegram action: " + action.Label
	if action.Value != "" {
		content += "\n" + action.Value
	}
	m, err := a.store.Append(chat.ID, "user", content, nil, "", "final", []ChatComponent{{App: "channels", Name: "telegram_callback", Props: map[string]any{
		"telegram_user_id":    cb.From.ID,
		"username":            cb.From.Username,
		"first_name":          cb.From.FirstName,
		"last_name":           cb.From.LastName,
		"original_message_id": original.ID,
		"action":              action,
	}}})
	if err != nil {
		return err
	}
	a.publish(*m)
	if token, err := a.telegramBotTokenFromTopic(topic); err == nil {
		_ = telegramAnswerCallback(token, cb.ID, "Received "+action.Label)
	}
	targetAgentID := originalAgentID
	if targetAgentID == 0 {
		targetAgentID = chat.AgentID
	}
	if targetAgentID > 0 && globalCtx != nil && globalCtx.PlatformAPI() != nil {
		ev := fmt.Sprintf("[telegram:%s action]\nUser tapped: %s", topic, action.Label)
		if action.Value != "" {
			ev += "\nAction value: " + action.Value
		}
		if original.Content != "" {
			ev += "\nOriginal message: " + original.Content
		}
		if err := globalCtx.PlatformAPI().SendEvent(targetAgentID, ev); err != nil {
			notice := fmt.Sprintf("(could not reach agent; Telegram action is saved. err: %v)", err)
			if sm, sErr := a.store.Append(chat.ID, "system", notice, nil, "", "final", nil); sErr == nil {
				a.publish(*sm)
			}
		}
	}
	return nil
}

func (a *App) telegramBotTokenFromTopic(topic string) (string, error) {
	cfg, err := a.store.GetTelegramConfigByTopic(topic)
	if err != nil {
		return "", err
	}
	return a.telegramBotToken(cfg)
}

func parseTelegramCallbackData(data string) (int64, int, bool) {
	parts := strings.Split(strings.TrimSpace(data), ":")
	if len(parts) != 3 || parts[0] != "act" {
		return 0, 0, false
	}
	messageID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || messageID <= 0 {
		return 0, 0, false
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil || idx < 0 {
		return 0, 0, false
	}
	return messageID, idx, true
}

func telegramTopicFromChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if strings.HasPrefix(channel, "telegram:") {
		topic := strings.TrimPrefix(channel, "telegram:")
		if validTopic(topic) {
			return topic
		}
	}
	return ""
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

func actionsFromAny(v any) []ChannelAction {
	switch x := v.(type) {
	case nil:
		return nil
	case []ChannelAction:
		return cleanActions(x)
	case []any:
		out := make([]ChannelAction, 0, len(x))
		for _, item := range x {
			switch obj := item.(type) {
			case ChannelAction:
				out = append(out, obj)
			case map[string]any:
				out = append(out, ChannelAction{
					Type:   stringFromProp(obj, "type"),
					Label:  stringFromProp(obj, "label"),
					Value:  stringFromProp(obj, "value"),
					URL:    stringFromProp(obj, "url"),
					Method: stringFromProp(obj, "method"),
					Body:   stringFromProp(obj, "body"),
				})
			}
		}
		return cleanActions(out)
	default:
		return nil
	}
}

func cleanActions(actions []ChannelAction) []ChannelAction {
	out := make([]ChannelAction, 0, len(actions))
	for _, action := range actions {
		action.Type = strings.ToLower(strings.TrimSpace(action.Type))
		action.Label = strings.TrimSpace(action.Label)
		action.Value = strings.TrimSpace(action.Value)
		action.URL = strings.TrimSpace(action.URL)
		action.Method = strings.ToUpper(strings.TrimSpace(action.Method))
		action.Body = strings.TrimSpace(action.Body)
		if action.Type == "" {
			action.Type = "callback"
		}
		if action.Type == "view" {
			action.Type = "url"
		}
		if action.Label == "" {
			continue
		}
		switch action.Type {
		case "callback":
			out = append(out, action)
		case "url":
			if action.URL != "" {
				out = append(out, action)
			}
		case "http", "copy":
			out = append(out, action)
		}
	}
	return out
}

func telegramComponents(actions []ChannelAction, agentID int64, title string) []ChatComponent {
	actions = cleanActions(actions)
	title = strings.TrimSpace(title)
	if len(actions) == 0 && agentID == 0 && title == "" {
		return nil
	}
	return []ChatComponent{{App: "channels", Name: "telegram", Props: map[string]any{
		"actions":  actions,
		"agent_id": agentID,
		"title":    title,
	}}}
}

func telegramActionsFromMessage(m Message) ([]ChannelAction, int64, string) {
	for _, c := range m.Components {
		if c.App != "channels" || c.Name != "telegram" {
			continue
		}
		return actionsFromAny(c.Props["actions"]), int64FromProp(c.Props, "agent_id"), stringFromProp(c.Props, "title")
	}
	return nil, 0, ""
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

func int64FromProp(props map[string]any, key string) int64 {
	if props == nil {
		return 0
	}
	return toInt64(props[key])
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
