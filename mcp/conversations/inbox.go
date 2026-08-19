package main

// inbox.go — the inbox is a query, not a table.
//
// Cards (approval/report/alert/status) are ordinary messages with a
// component_kind; this file owns the card constructors, the
// priority-ordered view, the action round-trip, and the sibling-app
// entry point (inbox_post). Because everything reads the one messages
// table, acting on a card anywhere updates the transcript, the inbox
// widget, and any external surface at once.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── cards ───────────────────────────────────────────────────────────

type approvalAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Style string `json:"style,omitempty"`
}

func defaultApprovalActions() []approvalAction {
	return []approvalAction{
		{ID: "approve", Label: "Approve", Style: "primary"},
		{ID: "deny", Label: "Deny", Style: "danger"},
	}
}

func approvalCard(title, body string, actions []approvalAction) Component {
	return Component{App: appName, Name: "approval-card", Props: map[string]any{
		"title": title, "body": body, "status": "pending", "actions": actions,
	}}
}

func reportCard(title, summary, period string) Component {
	return Component{App: appName, Name: "report-card", Props: map[string]any{
		"title": title, "summary": summary, "period": period, "status": "sent",
	}}
}

func alertCard(text, severity string) Component {
	return Component{App: appName, Name: "alert-card", Props: map[string]any{
		"text": text, "severity": severity,
	}}
}

// ─── the inbox view ──────────────────────────────────────────────────

type InboxItem struct {
	Message  Message `json:"message"`
	Priority int     `json:"priority"`
}

// inboxPriority mirrors the dashboard's ordering: pending approvals
// first, then alerts by severity, then reports. Status lines are a
// separate strip (latest per agent), not ranked here.
func inboxPriority(m *Message) int {
	switch m.ComponentKind {
	case kindApproval:
		return 0
	case kindAlert:
		switch m.Severity {
		case "error":
			return 1
		case "warn":
			return 2
		default:
			return 3
		}
	case kindReport:
		return 4
	default:
		return 9
	}
}

// Inbox returns the priority-ordered items. One indexed query — the
// component_kind column is the whole reason the LIKE-scan era ends
// here.
func (s *store) Inbox(limit int) ([]InboxItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT `+messageCols+` FROM messages
		WHERE component_kind IN (?, ?, ?)
		ORDER BY id DESC LIMIT ?`, kindApproval, kindAlert, kindReport, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []InboxItem{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		// Dismissed / already-resolved approvals fall out of the
		// pending view but stay in history.
		if m.ComponentKind == kindApproval && cardStatus(m) != "pending" {
			continue
		}
		if dismissed(m) {
			continue
		}
		items = append(items, InboxItem{Message: *m, Priority: inboxPriority(m)})
	}
	sortInbox(items)
	return items, rows.Err()
}

// sortInbox: priority ascending, then newest first. Insertion sort —
// the slice is at most a few hundred rows.
func sortInbox(items []InboxItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			if b.Priority < a.Priority || (b.Priority == a.Priority && b.Message.ID > a.Message.ID) {
				items[j-1], items[j] = b, a
				continue
			}
			break
		}
	}
}

func cardStatus(m *Message) string {
	for _, c := range m.Components {
		if status, ok := c.Props["status"].(string); ok {
			return status
		}
	}
	return ""
}

func dismissed(m *Message) bool {
	for _, c := range m.Components {
		if d, ok := c.Props["dismissed"].(bool); ok && d {
			return true
		}
	}
	return false
}

// ─── the action round-trip ───────────────────────────────────────────

// applyInboxAction resolves a pending approval card in place. Returns
// the updated components and the recorded verdict.
func applyInboxAction(m *Message, actionID, note string, userID int64) ([]Component, map[string]any, error) {
	if m.ComponentKind != kindApproval {
		return nil, nil, fmt.Errorf("message %d is not actionable", m.ID)
	}
	updated := make([]Component, len(m.Components))
	copy(updated, m.Components)
	for i, c := range updated {
		if c.Name != "approval-card" {
			continue
		}
		status, _ := c.Props["status"].(string)
		if status != "pending" {
			return nil, nil, fmt.Errorf("approval already resolved: %s", status)
		}
		if !actionAllowed(c, actionID) {
			return nil, nil, fmt.Errorf("unknown action %q", actionID)
		}
		props := map[string]any{}
		for k, v := range c.Props {
			props[k] = v
		}
		props["status"] = actionID
		props["resolved_by"] = userID
		props["resolved_at"] = time.Now().UTC().Format(time.RFC3339)
		if note != "" {
			props["note"] = note
		}
		updated[i] = Component{App: c.App, Name: c.Name, Props: props}
		return updated, map[string]any{"status": actionID, "note": note}, nil
	}
	return nil, nil, errors.New("no approval card on message")
}

func actionAllowed(c Component, actionID string) bool {
	raw, ok := c.Props["actions"]
	if !ok {
		return actionID == "approve" || actionID == "deny"
	}
	switch actions := raw.(type) {
	case []approvalAction:
		for _, a := range actions {
			if a.ID == actionID {
				return true
			}
		}
	case []any:
		for _, entry := range actions {
			if m, ok := entry.(map[string]any); ok {
				if id, _ := m["id"].(string); id == actionID {
					return true
				}
			}
		}
	}
	return false
}

// resolveApproval is the full round-trip used by the HTTP handler:
// mutate the card, republish to every surface, then tell whoever asked.
//
//   - Agent-raised approvals forward an approval.result event into the
//     owning agent's thread (SendThreadEvent when the thread is known,
//     SendEvent otherwise).
//   - inbox_post approvals with a callback_tool call back into the
//     posting app via the platform (CallAppResult) instead.
func (a *App) resolveApproval(app *sdk.AppCtx, m *Message, actionID, note string, userID int64) (*Message, error) {
	updatedComponents, _, err := applyInboxAction(m, actionID, note, userID)
	if err != nil {
		return nil, err
	}
	updated, err := a.store.UpdateMessageComponents(m.ID, updatedComponents)
	if err != nil {
		return nil, err
	}
	if conv, err := a.store.GetConversation(updated.ConversationID); err == nil {
		a.deliver(app, conv, updated)
	}

	switch {
	case updated.CallbackTool != "" && updated.SourceApp != "":
		var out map[string]any
		if err := app.PlatformAPI().CallAppResult(updated.SourceApp, updated.CallbackTool, map[string]any{
			"message_id": updated.ID, "action_id": actionID, "note": note,
		}, &out); err != nil {
			app.Logger().Warn("inbox callback failed", "app", updated.SourceApp, "tool", updated.CallbackTool, "err", err)
		}
	case updated.AgentID != 0:
		event := formatApprovalResult(updated.ID, actionID, note)
		forwarded := false
		// Thread addressing is an optional SDK surface — type-assert,
		// never assume (testkit stubs may implement only the base).
		if updated.ThreadID != "" {
			if tc, ok := app.PlatformAPI().(sdk.ThreadClient); ok {
				forwarded = tc.SendThreadEvent(
					sdk.ThreadRef{AgentID: updated.AgentID, ThreadID: updated.ThreadID}, event) == nil
			}
		}
		if !forwarded {
			if err := app.PlatformAPI().SendEvent(updated.AgentID, event); err != nil {
				app.Logger().Warn("approval result forward failed", "agent", updated.AgentID, "err", err)
			}
		}
	}
	return updated, nil
}

func formatApprovalResult(messageID int64, actionID, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[approval.result] message_id=%d action=%s", messageID, actionID)
	if note != "" {
		fmt.Fprintf(&b, "\nOperator note: %s", note)
	}
	return b.String()
}

// ─── sibling-app entry point ─────────────────────────────────────────

// inboxConversationKey names the per-app system conversation that holds
// inbox_post items. One conversation per posting app keeps the items
// grouped and gives them a place in the chat list without inventing a
// second storage path.
func inboxConversationKey(sourceApp string) string { return "app:" + sourceApp + ":inbox" }

func (a *App) toolInboxPost(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller := sdk.CallerFrom(ctx)
	kind := stringArg(args, "kind")
	title := strings.TrimSpace(stringArg(args, "title"))
	body := strings.TrimSpace(stringArg(args, "body"))
	if title == "" {
		return nil, errors.New("title required")
	}
	if body == "" {
		body = title
	}

	// An AGENT that reaches for inbox_post (the gateway exposes every
	// tool) is refused with the flow spelled out — there is no
	// fallback bucket to route into anymore, and silently picking a
	// conversation for the agent would reintroduce one.
	if caller != nil && caller.AgentID != 0 {
		return nil, errors.New("inbox_post is the sibling-app surface — agents raise items with conversations_alert / conversations_report / conversations_request_approval into a conversation (conversations_list to find one, conversations_create to make one)")
	}

	// App callers arrive without an agent id; the posting app
	// self-declares via source_app (the platform does not yet stamp
	// app identity on inter-app calls). Refuse anonymity rather than
	// invent an "unknown-app" bucket.
	sourceApp := stringArg(args, "source_app")
	if sourceApp == "" {
		return nil, errors.New("source_app required (calling app's name)")
	}

	agentID := int64(intArg(args, "agent_id", 0))
	projectID := ""
	if caller != nil {
		projectID = caller.ProjectID
	}
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID:       projectID,
		LeadAgentID:     firstNonZero(agentID, 1),
		Title:           "Inbox: " + sourceApp,
		Origin:          "app",
		ConversationKey: inboxConversationKey(sourceApp),
	})
	if err != nil {
		return nil, err
	}

	msg := &Message{
		ConversationID: conv.ID, Role: "system", Content: title,
		SourceApp: sourceApp, CallbackTool: stringArg(args, "callback_tool"),
		// InboxOnly is deliberately NOT set (0.5.1): an item must be
		// visible in the transcript of the conversation it lives in —
		// a "Reports" conversation whose reports are hidden from its
		// own transcript reads as broken. The inbox surfaces items by
		// component_kind regardless.
	}
	switch kind {
	case kindApproval:
		msg.ComponentKind = kindApproval
		msg.Components = []Component{approvalCard(title, body, defaultApprovalActions())}
	case kindAlert:
		severity := stringArg(args, "severity")
		if severity == "" {
			severity = "info"
		}
		msg.ComponentKind = kindAlert
		msg.Severity = severity
		msg.Components = []Component{alertCard(body, severity)}
	case kindReport:
		msg.ComponentKind = kindReport
		msg.Components = []Component{reportCard(title, body, "")}
	default:
		return nil, fmt.Errorf("invalid kind %q (report|alert|approval)", kind)
	}

	stored, err := a.store.AppendMessage(msg)
	if err != nil {
		return nil, err
	}
	a.deliver(app, conv, stored)
	return map[string]any{"message_id": stored.ID, "conversation_id": conv.ID}, nil
}

func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// ─── dismissal ───────────────────────────────────────────────────────

// DismissMessage marks an inbox card dismissed. History keeps the row;
// only the inbox view forgets it. Approvals must be resolved, not
// dismissed — silently hiding a pending question would strand the
// asking agent.
func (a *App) DismissMessage(messageID int64) (*Message, error) {
	m, err := a.store.GetMessage(messageID)
	if err != nil {
		return nil, fmt.Errorf("message not found: %d", messageID)
	}
	if m.ComponentKind == "" {
		return nil, fmt.Errorf("message %d is not an inbox item", messageID)
	}
	if m.ComponentKind == kindApproval && cardStatus(m) == "pending" {
		return nil, errors.New("pending approvals cannot be dismissed — approve or deny them")
	}
	if len(m.Components) == 0 {
		return nil, fmt.Errorf("message %d has no card to dismiss", messageID)
	}
	updated := make([]Component, len(m.Components))
	copy(updated, m.Components)
	for i, c := range updated {
		props := map[string]any{}
		for k, v := range c.Props {
			props[k] = v
		}
		props["dismissed"] = true
		updated[i] = Component{App: c.App, Name: c.Name, Props: props}
	}
	return a.store.UpdateMessageComponents(m.ID, updated)
}

// Agent status left this app in 0.5.0 — one status per agent is the
// status app's domain. Legacy 'status'-kind message rows remain inert
// history; nothing writes or queries them anymore.
