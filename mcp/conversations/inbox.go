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

func reportCard(title, summary, period string, sections []map[string]any) Component {
	return Component{App: appName, Name: "report-card", Props: map[string]any{
		"title": title, "summary": summary, "period": period, "sections": sections, "status": "sent",
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
func (s *store) Inbox(projectID string, userID int64, limit int) ([]InboxItem, error) {
	return s.InboxForAgent(projectID, userID, 0, limit)
}

func (s *store) InboxForAgent(projectID string, userID, agentID int64, limit int) ([]InboxItem, error) {
	page, err := s.InboxPage(projectID, userID, agentID, limit, "")
	return page.Items, err
}

type InboxPage struct {
	Items      []InboxItem    `json:"items"`
	Total      int            `json:"total"`
	NextCursor string         `json:"next_cursor"`
	Attention  map[string]int `json:"attention"`
}

const inboxPrioritySQL = `CASE m.component_kind WHEN 'approval' THEN 0 WHEN 'alert' THEN CASE m.severity WHEN 'error' THEN 1 WHEN 'warn' THEN 2 ELSE 3 END WHEN 'report' THEN 4 ELSE 9 END`

func (s *store) InboxPage(projectID string, userID, agentID int64, limit int, cursor string) (InboxPage, error) {
	out := InboxPage{Items: []InboxItem{}}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	priority, lastID := -1, int64(0)
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d:%d", &priority, &lastID); err != nil || priority < 0 || lastID <= 0 {
			return out, errors.New("invalid inbox cursor")
		}
	}
	base := ` FROM messages m JOIN conversations c ON c.id=m.conversation_id
 WHERE c.project_id=? AND c.archived_at IS NULL
 AND (c.owner_user_id=0 OR c.owner_user_id=? OR EXISTS(SELECT 1 FROM participants p WHERE p.conversation_id=c.id AND p.user_id=?))
 AND (?=0 OR EXISTS(SELECT 1 FROM participants p WHERE p.conversation_id=c.id AND p.agent_id=?))
 AND m.component_kind IN('approval','alert','report') AND (m.component_kind!='approval' OR m.action_status='pending') AND m.dismissed=0`
	args := []any{projectID, userID, userID, agentID, agentID}
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT COUNT(*)`+base, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	out.Attention = map[string]int{}
	attentionRows, err := tx.Query(`SELECT c.id,MAX(CASE WHEN m.component_kind='alert' AND m.severity='error' THEN 4 WHEN m.component_kind='alert' AND m.severity='warn' THEN 3 WHEN m.component_kind='approval' THEN 2 WHEN m.component_kind='alert' THEN 1 ELSE 0 END)`+base+` GROUP BY c.id`, args...)
	if err != nil {
		return out, err
	}
	for attentionRows.Next() {
		var id string
		var rank int
		if err := attentionRows.Scan(&id, &rank); err != nil {
			attentionRows.Close()
			return out, err
		}
		if rank > 0 {
			out.Attention[id] = rank
		}
	}
	err = attentionRows.Err()
	attentionRows.Close()
	if err != nil {
		return out, err
	}
	paged := base + ` AND (?=-1 OR ` + inboxPrioritySQL + `>? OR (` + inboxPrioritySQL + `=? AND m.id<?)) ORDER BY ` + inboxPrioritySQL + `,m.id DESC LIMIT ?`
	rows, err := tx.Query(`SELECT `+prefixCols("m.", messageCols)+paged, append(args, priority, priority, priority, lastID, limit+1)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, InboxItem{Message: *m, Priority: inboxPriority(m)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = fmt.Sprintf("%d:%d", last.Priority, last.Message.ID)
	}
	return out, tx.Commit()
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
	if m.ActionStatus != "" && m.ActionStatus != "resolved" {
		return m.ActionStatus
	}
	for _, c := range m.Components {
		if status, ok := c.Props["status"].(string); ok {
			return status
		}
	}
	return ""
}

func cardNote(m *Message) string {
	for _, c := range m.Components {
		if note, ok := c.Props["note"].(string); ok {
			return note
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
	return applyInboxActionActor(m, actionID, note, userID, "")
}

func applyInboxActionActor(m *Message, actionID, note string, userID int64, externalActor string) ([]Component, map[string]any, error) {
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
		if userID > 0 {
			props["resolved_by"] = userID
		}
		if externalActor != "" {
			props["resolved_by_external"] = externalActor
		}
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
	return a.resolveApprovalActor(app, m, actionID, note, userID, "")
}

func (a *App) resolveApprovalExternal(app *sdk.AppCtx, m *Message, actionID, note, externalActor string) (*Message, error) {
	if strings.TrimSpace(externalActor) == "" {
		return nil, errors.New("external approval actor required")
	}
	return a.resolveApprovalActor(app, m, actionID, note, 0, externalActor)
}

func (a *App) resolveApprovalActor(app *sdk.AppCtx, m *Message, actionID, note string, userID int64, externalActor string) (*Message, error) {
	updatedComponents, _, err := applyInboxActionActor(m, actionID, note, userID, externalActor)
	if err != nil {
		return nil, err
	}
	target := approvalDeliveryTarget(m)
	updated, err := a.store.ResolveApproval(m.ID, updatedComponents, actionID, target)
	if err != nil {
		return nil, err
	}
	if conv, err := a.store.GetConversation(updated.ConversationID); err == nil {
		a.deliver(app, conv, updated)
		if target != "" {
			a.dispatchOrQueue(app, target, conv, updated)
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

	// App identity is stamped by the authenticated app-to-app bridge. Never
	// trust source_app supplied in the payload: doing so turns Conversations
	// into a confused deputy for approval callbacks.
	if caller == nil || caller.AppInstallID <= 0 || caller.AppName == "" {
		return nil, errors.New("authenticated sibling app identity required")
	}
	sourceApp := caller.AppName
	if claimed := strings.TrimSpace(stringArg(args, "source_app")); claimed != "" && claimed != sourceApp {
		return nil, errors.New("source_app does not match authenticated calling app")
	}

	agentID := int64(intArg(args, "agent_id", 0))
	projectID := strings.TrimSpace(caller.ProjectID)
	if projectID == "" {
		return nil, errors.New("project context required")
	}
	conv, err := a.store.CreateConversation(CreateConversationInput{
		ProjectID:       projectID,
		LeadAgentID:     agentID,
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
		ClientID: inboxClientID(caller.AppInstallID, stringArg(args, "client_message_id")),
		// InboxOnly is deliberately NOT set (0.5.1): an item must be
		// visible in the transcript of the conversation it lives in —
		// a "Reports" conversation whose reports are hidden from its
		// own transcript reads as broken. The inbox surfaces items by
		// component_kind regardless.
	}
	switch kind {
	case kindApproval:
		actions, actionErr := approvalActionsArg(args)
		if actionErr != nil {
			return nil, actionErr
		}
		msg.ComponentKind = kindApproval
		msg.Components = []Component{approvalCard(title, body, actions)}
	case kindAlert:
		severity := stringArg(args, "severity")
		if severity == "" {
			severity = "info"
		}
		if severity != "info" && severity != "warn" && severity != "error" {
			return nil, errors.New("severity must be info, warn, or error")
		}
		msg.ComponentKind = kindAlert
		msg.Severity = severity
		msg.Components = []Component{alertCard(body, severity)}
	case kindReport:
		sections, sectionErr := genericObjectsArg(args, "sections", 50)
		if sectionErr != nil {
			return nil, sectionErr
		}
		msg.ComponentKind = kindReport
		msg.Components = []Component{reportCard(title, body, "", sections)}
	default:
		return nil, fmt.Errorf("invalid kind %q (report|alert|approval)", kind)
	}

	stored, _, err := a.appendAndDeliver(app, conv, msg)
	if err != nil {
		return nil, err
	}
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
