// Channels app v0.1.
//
// This is the standalone version of the platform chat channel: it owns
// chat persistence, REST/SSE delivery, and the agent-facing MCP tools.
// The only platform dependency is app-sdk's PlatformAPI for forwarding
// inbound user messages to the owning agent.
package main

import (
	"context"
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
version: 0.1.0
description: |
  Agent-facing channel router with a standalone dashboard chat channel.
  Agents reply through respond(channel="chat", ...); the app stores chat
  history, streams updates to the dashboard, and forwards user messages to
  the owning agent.
author: Apteva
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/channels/icon.svg
scopes: [project, global]
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
      description: Send a user-visible reply to an active channel. V1 supports channel="chat".
    - name: status
      description: Send a status line to a channel. V1 writes chat status lines as system messages.
    - name: list_channels
      description: List channels currently reachable for the calling agent.
  publishes:
    - name: chat.message
      description: Emitted whenever a chat message is inserted.
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
		{Pattern: "/messages", Handler: a.handleMessages},
		{Pattern: "/stream", Handler: a.handleStream},
		{Pattern: "/unread-summary", Handler: a.handleUnreadSummary},
		{Pattern: "/seen", Handler: a.handleSeen},
		{Pattern: "/presence", Handler: a.handlePresence},
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
					"channel":   map[string]any{"type": "string", "description": `Target channel. V1 supports "chat".`},
					"text":      map[string]any{"type": "string", "description": "Message to deliver to the user."},
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
		"V1 standalone Channels app supports the dashboard chat channel. Use channel=\"chat\" when the incoming event is tagged [chat].\n" +
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
		chatID, _, ok := a.authorizeChat(w, r)
		if !ok {
			return
		}
		n, err := a.store.DeleteMessages(chatID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
	if _, err := a.store.GetChat(body.ChatID); err != nil {
		http.Error(w, "chat not found", http.StatusNotFound)
		return
	}
	current, err := a.store.MarkSeen(body.ChatID, body.LastSeenID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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

// --- MCP handlers ----------------------------------------------------------

func (a *App) toolRespond(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	channel := normalizeChannel(strArg(args, "channel"))
	if channel != "chat" {
		return nil, fmt.Errorf("channel %q is not available in channels v1; use channel=\"chat\"", channel)
	}
	text := strArg(args, "text")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text required")
	}
	agentID := callerAgentID(ctx, args)
	if agentID == 0 {
		return nil, errors.New("agent_id required because caller metadata is unavailable")
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
	a.hub.publish(*m)
	return map[string]any{"status": "ok", "channel": "chat", "message_id": m.ID}, nil
}

func (a *App) toolListChannels(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(ctx, args)
	if agentID == 0 {
		return map[string]any{"channels": []any{}}, nil
	}
	chatID := defaultChatID(agentID)
	active := a.hub.hasSubscribers(chatID)
	channels := []map[string]any{
		{
			"id":           "chat",
			"label":        "Dashboard chat",
			"active":       active,
			"capabilities": []string{"text", "status", "components"},
		},
	}
	var activeIDs []string
	if active {
		activeIDs = append(activeIDs, "chat")
	}
	return map[string]any{
		"channels":        channels,
		"active_channels": activeIDs,
		"agent_id":        agentID,
	}, nil
}

// --- helpers ---------------------------------------------------------------

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
	if globalCtx != nil {
		globalCtx.Emit("chat.message", m)
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
