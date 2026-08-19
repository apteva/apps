package main

// conversations — one app for every conversation surface.
//
// Internal dashboard chat, the inbox (approvals / reports / alerts /
// status), and external channels as optional integration bindings.
// The organizing rule: inbox items ARE messages. An agent's approval
// request is a typed card inside its conversation; the inbox is a
// priority-ordered query over those rows; acting on a card anywhere
// mutates the one row every surface renders. External threads are
// ordinary conversations with a transport binding (origin +
// conversation_key), never a special type — features must not branch
// on origin.
//
// Developed in parallel with apteva-server's built-in channel-chat and
// the deprecated channels sidecar. Both are donors; neither is
// modified. See DESIGN.md for the migration phases that eventually
// retire them.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// manifestYAML mirrors apteva.yaml — keep both in sync (version too);
// TestManifestMatchesAptevaYAML enforces it.
const manifestYAML = `schema: apteva-app/v1

name: conversations
display_name: Conversations
version: 0.5.3
description: |
  One conversation system: internal dashboard chat, the inbox
  (approvals, reports, alerts, status), and external channels as
  optional integration bindings. Inbox items are messages — an
  agent's approval request is a typed card in its conversation, and
  the inbox is a priority-ordered view over those rows, so acting on
  a card anywhere updates every surface at once. External threads
  (Telegram first) are ordinary conversations with a transport
  binding, never a special type. Other apps raise inbox items via
  inbox_post; deliveries go through a crash-safe ledger.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/conversations
icon: /ui/icon.svg
icon_style: monochrome
tags: [chat, inbox, channels, approvals, notifications]

scopes: [global]
min_apteva_version: "0.10.0"

requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
    - platform.instances.read
    - platform.instances.write
    - platform.threads.write
    - platform.telemetry.read
    - platform.connections.read
    - platform.connections.execute
  integrations:
    - role: telegram_bot
      kind: integration
      compatible_slugs: [telegram]
      capabilities: []
      required: false
      label: "Telegram bot (optional)"
      hint: "Bind the Telegram integration connection whose bot should send and receive messages. Unknown senders go through pairing by default."
  apps:
    - name: messaging
      version: ">=0.13.0"
      optional: true
      reason: "email/SMS fan-out of inbox items (report digests, approval nudges)"

provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: conversations_send,             description: "Send a message into a conversation. Args: conversation_id, text, components?. The calling agent must be a participant; delivery fans out to every bound surface (dashboard SSE, external channels). Never creates conversations." }
    - { name: conversations_request_approval, description: "Ask the operator to approve something. Args: conversation_id (required - list or create a conversation first), title, body?, actions? (default Approve/Deny). Renders as an actionable card in the conversation AND the inbox; the verdict comes back to this agent as an approval.result event." }
    - { name: conversations_report,           description: "File a report. Args: conversation_id (required - keep one standing conversation such as Reports and reuse it), title, summary, period?, sections?. Shown in its conversation AND the inbox." }
    - { name: conversations_alert,            description: "Raise an alert. Args: conversation_id (required - reuse the conversation the problem belongs to, or create a topical one), text, severity? (info|warn|error, default info). Shown in the inbox, severity-ranked." }
    - { name: conversations_create,           description: "Create a conversation led by this agent for an ongoing topic. Args: title. Title-idempotent per agent: an existing conversation with the same title is returned with created=false. Titles name ongoing topics - never put timestamps, ids, or per-item detail in them." }
    - { name: conversations_list,             description: "List conversations this agent participates in. Args: limit?, query? (case-insensitive title filter - search before creating)." }
    - { name: conversations_history,          description: "Read a conversation's transcript. Args: conversation_id, since_id?, limit?." }
    - { name: inbox_post,                     description: "INTERNAL - sibling-app entry point via CallAppResult; agent calls are refused. Agents use conversations_alert / conversations_report / conversations_request_approval into a conversation (conversations_list to find one, conversations_create to make one). Args: kind (report|alert|approval), title, body?, severity?, actions?, source_app (required for app callers), callback_tool?, agent_id?. When callback_tool is set, actions call back into the posting app." }
  ui_panels:
    - slot: project.page
      label: Conversations
      icon: message-circle
      entry: /ui/ConversationsPanel.mjs

  skills:
    - name: using-conversations
      command: /conversations
      body_file: skills/using-conversations.md
      description: |
        Reply discipline (acknowledge, work, one final outcome), where
        inbox items live (list, reuse, create with stable topic
        titles), alert/report cadence, main-thread output ownership,
        the approval round-trip, and room etiquette. Load before
        messaging people or raising inbox items.
      metadata:
        category: communication
        icon: message-circle

runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/conversations }
  port: 8080
  health_check: /health

db:
  driver: sqlite
  path: /data/conversations.db
  migrations: migrations/

upgrade_policy: auto-patch
`

type App struct {
	store    *store
	hub      *hub
	adapters *adapterRegistry
	streamer *streamer
	// telemetryStop cancels the bridge subscription on unmount.
	telemetryStop func()
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("conversations requires a db block")
	}
	a.store = newStore(ctx.AppDB())
	a.hub = newHub()
	a.adapters = newAdapterRegistry(a.hub)
	a.streamer = newStreamer(a.hub)
	mountedCtx = ctx
	// Token-level streaming when the platform grants it; Stage-1 phase
	// frames otherwise. The panel renders either without knowing which.
	if a.runTelemetryFeed(ctx) {
		ctx.Logger().Info("telemetry bridge active — token-level streaming on")
	}
	// Crash recovery: anything the ledger recorded but never confirmed
	// goes out again. Adapters that are not configured (no binding)
	// leave their rows pending rather than failing them — the binding
	// may appear later.
	if redelivered, err := a.redeliverPending(ctx); err != nil {
		ctx.Logger().Warn("pending redelivery incomplete", "err", err)
	} else if redelivered > 0 {
		ctx.Logger().Info("redelivered pending messages", "count", redelivered)
	}
	ctx.Logger().Info("conversations mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.telemetryStop != nil {
		a.telemetryStop()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "conversations_send",
			Description: "Send a message into a conversation you participate in. Delivery fans out to every " +
				"surface bound to the conversation: the dashboard stream and any external channel.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"text":            map[string]any{"type": "string"},
			}, []string{"conversation_id", "text"}),
			HandlerCtx: a.toolSend,
		},
		{
			Name: "conversations_request_approval",
			Description: "Ask the operator to approve something. Requires conversation_id — find one with " +
				"conversations_list or create one with conversations_create. The card is actionable in the " +
				"conversation, the inbox, and any bound external channel; the verdict returns to you as an " +
				"approval.result event.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"title":           map[string]any{"type": "string"},
				"body":            map[string]any{"type": "string"},
			}, []string{"conversation_id", "title"}),
			HandlerCtx: a.toolRequestApproval,
		},
		{
			Name: "conversations_report",
			Description: "File a report. Requires conversation_id — keep one standing conversation (e.g. " +
				"\"Reports\") via conversations_create and reuse it. Shown in its conversation and the inbox.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"title":           map[string]any{"type": "string"},
				"summary":         map[string]any{"type": "string"},
				"period":          map[string]any{"type": "string"},
			}, []string{"conversation_id", "title", "summary"}),
			HandlerCtx: a.toolReport,
		},
		{
			Name: "conversations_alert",
			Description: "Raise an alert (info|warn|error), severity-ranked in the inbox. Requires " +
				"conversation_id — reuse the conversation the problem belongs to (conversations_list) or " +
				"create a topical one (conversations_create).",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"text":            map[string]any{"type": "string"},
				"severity":        map[string]any{"type": "string", "enum": []string{"info", "warn", "error"}},
			}, []string{"conversation_id", "text"}),
			HandlerCtx: a.toolAlert,
		},
		{
			Name: "conversations_create",
			Description: "Create a conversation you lead, for an ongoing topic (\"Reports\", " +
				"\"Infra monitoring\", an incident name). Title-idempotent: if you already have a conversation " +
				"with this title it is returned with created=false, so reusing a stable title never duplicates. " +
				"Titles name ongoing topics — never put timestamps, ids, or per-item detail in them.",
			InputSchema: schemaObject(map[string]any{
				"title": map[string]any{"type": "string"},
			}, []string{"title"}),
			HandlerCtx: a.toolCreate,
		},
		{
			Name: "conversations_list",
			Description: "List conversations this agent participates in. Pass query to filter by title " +
				"(case-insensitive substring) — search before creating.",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer"},
				"query": map[string]any{"type": "string"},
			}, nil),
			HandlerCtx: a.toolList,
		},
		{
			Name:        "conversations_history",
			Description: "Read a conversation transcript.",
			InputSchema: schemaObject(map[string]any{
				"conversation_id": map[string]any{"type": "string"},
				"since_id":        map[string]any{"type": "integer"},
				"limit":           map[string]any{"type": "integer"},
			}, []string{"conversation_id"}),
			HandlerCtx: a.toolHistory,
		},
		{
			Name: "inbox_post",
			Description: "INTERNAL sibling-app entry point (CallAppResult) — not for agents; agent calls are " +
				"refused. Agents raise items with conversations_alert, conversations_report, or " +
				"conversations_request_approval into a conversation (conversations_list to find one, " +
				"conversations_create to make one). With callback_tool set, inbox actions call back into the " +
				"posting app.",
			InputSchema: schemaObject(map[string]any{
				"kind":          map[string]any{"type": "string", "enum": []string{"report", "alert", "approval"}},
				"title":         map[string]any{"type": "string"},
				"body":          map[string]any{"type": "string"},
				"severity":      map[string]any{"type": "string"},
				"callback_tool": map[string]any{"type": "string"},
				"agent_id":      map[string]any{"type": "integer"},
			}, []string{"kind", "title"}),
			HandlerCtx: a.toolInboxPost,
		},
	}
}

// ─── agent identity ──────────────────────────────────────────────────

type callIdentity struct {
	AgentID   int64
	ThreadID  string
	ProjectID string
}

// requireAgentCaller fails closed: a tool call without caller identity
// cannot write into conversations. (The SDK treats nil caller as full
// access for back-compat; a conversation store must not.)
func requireAgentCaller(ctx context.Context) (*callIdentity, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AgentID == 0 {
		return nil, errors.New("caller identity required: this tool must be called by an agent")
	}
	return &callIdentity{AgentID: caller.AgentID, ThreadID: caller.ThreadID, ProjectID: caller.ProjectID}, nil
}

// requireConversationArg reads the mandatory conversation_id. The
// item tools have no fallback bucket — an agent must say where the
// item lives, and the error teaches the flow when it doesn't.
func (a *App) requireConversationArg(from *callIdentity, args map[string]any) (*Conversation, error) {
	conversationID := strings.TrimSpace(stringArg(args, "conversation_id"))
	if conversationID == "" {
		return nil, errors.New("conversation_id required — find a conversation with conversations_list (query filters by title) or create a topical one with conversations_create")
	}
	return a.requireParticipant(from, conversationID)
}

// requireParticipant loads the conversation and verifies the calling
// agent belongs to it.
func (a *App) requireParticipant(from *callIdentity, conversationID string) (*Conversation, error) {
	conv, err := a.store.GetConversation(conversationID)
	if err != nil {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	ok, err := a.store.IsParticipantAgent(conversationID, from.AgentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("agent %d is not a participant of %s", from.AgentID, conversationID)
	}
	return conv, nil
}

// ─── tools ───────────────────────────────────────────────────────────

func (a *App) toolSend(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conversationID, _ := args["conversation_id"].(string)
	text, _ := args["text"].(string)
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text required")
	}
	conv, err := a.requireParticipant(from, conversationID)
	if err != nil {
		return nil, err
	}
	msg, err := a.store.AppendMessage(&Message{
		ConversationID: conv.ID, Role: "agent", Content: text,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
	})
	if err != nil {
		return nil, err
	}
	a.deliver(app, conv, msg)
	// The durable reply supersedes any pending thinking bubble.
	a.streamer.settleAck(conv.ID)
	return map[string]any{"message_id": msg.ID, "conversation_id": conv.ID}, nil
}

func (a *App) toolRequestApproval(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	body := strings.TrimSpace(stringArg(args, "body"))
	if title == "" {
		return nil, errors.New("title required")
	}
	if body == "" {
		body = title
	}
	conv, err := a.requireConversationArg(from, args)
	if err != nil {
		return nil, err
	}
	msg, err := a.store.AppendMessage(&Message{
		ConversationID: conv.ID, Role: "agent",
		Content: "Approval requested: " + title,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ComponentKind: kindApproval,
		Components:    []Component{approvalCard(title, body, defaultApprovalActions())},
	})
	if err != nil {
		return nil, err
	}
	a.deliver(app, conv, msg)
	return map[string]any{"message_id": msg.ID, "status": "pending"}, nil
}

func (a *App) toolReport(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.requireConversationArg(from, args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	summary := strings.TrimSpace(stringArg(args, "summary"))
	if title == "" || summary == "" {
		return nil, errors.New("title and summary required")
	}
	msg, err := a.store.AppendMessage(&Message{
		ConversationID: conv.ID, Role: "agent",
		Content: "Report: " + title,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ComponentKind: kindReport,
		Components: []Component{reportCard(title, summary, stringArg(args, "period"))},
	})
	if err != nil {
		return nil, err
	}
	a.deliver(app, conv, msg)
	return map[string]any{"message_id": msg.ID, "status": "sent"}, nil
}

func (a *App) toolAlert(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.requireConversationArg(from, args)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(stringArg(args, "text"))
	if text == "" {
		return nil, errors.New("text required")
	}
	severity := stringArg(args, "severity")
	switch severity {
	case "info", "warn", "error":
	case "":
		severity = "info"
	default:
		return nil, fmt.Errorf("invalid severity %q", severity)
	}
	msg, err := a.store.AppendMessage(&Message{
		ConversationID: conv.ID, Role: "agent", Content: text,
		AgentID: from.AgentID, ThreadID: from.ThreadID,
		ComponentKind: kindAlert, Severity: severity,
		Components: []Component{alertCard(text, severity)},
	})
	if err != nil {
		return nil, err
	}
	a.deliver(app, conv, msg)
	return map[string]any{"message_id": msg.ID}, nil
}

// toolCreate is the deliberate conversation-creation surface — the
// replacement for the removed hardcoded activity bucket. It is
// title-idempotent per agent so the laziest calling pattern
// (create("Reports") before every report) converges on one
// conversation instead of sprawling.
func (a *App) toolCreate(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	if existing, err := a.store.FindAgentConversationByTitle(from.AgentID, title); err != nil {
		return nil, err
	} else if existing != nil {
		return map[string]any{"conversation_id": existing.ID, "title": existing.Title, "created": false}, nil
	}
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID:   from.ProjectID,
		LeadAgentID: from.AgentID,
		Title:       title,
		Origin:      "agent",
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversation_id": conv.ID, "title": conv.Title, "created": true}, nil
}

func (a *App) toolList(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	conversations, err := a.store.ListConversationsForAgent(from.AgentID, limit)
	if err != nil {
		return nil, err
	}
	if query := strings.ToLower(strings.TrimSpace(stringArg(args, "query"))); query != "" {
		filtered := conversations[:0]
		for _, c := range conversations {
			if strings.Contains(strings.ToLower(c.Title), query) {
				filtered = append(filtered, c)
			}
		}
		conversations = filtered
	}
	return map[string]any{"conversations": conversations}, nil
}

func (a *App) toolHistory(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.requireParticipant(from, stringArg(args, "conversation_id"))
	if err != nil {
		return nil, err
	}
	messages, err := a.store.Transcript(conv.ID, int64(intArg(args, "since_id", 0)), intArg(args, "limit", 100))
	if err != nil {
		return nil, err
	}
	return map[string]any{"messages": messages}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func intArg(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return fallback
}

func schemaObject(props map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
