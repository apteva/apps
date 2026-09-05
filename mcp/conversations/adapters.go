package main

// adapters.go — the transport seam.
//
// An adapter delivers messages to one internal surface. `web` (the SSE
// hub) is always configured; inbound agent turns, approval results, and
// sibling-app callbacks all use the durable ledger. Telegram implements the first external
// transport through a platform-managed integration connection.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const messageIntentSoftBreak = "soft_break"

type adapter interface {
	// ID is the target prefix this adapter owns (for example "web").
	ID() string
	// Deliver pushes one message to one target address (the part after
	// "<id>:" in a deliveries.target).
	Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error
}

type adapterRegistry struct {
	byID map[string]adapter
}

func newAdapterRegistry(app *App, h *hub) *adapterRegistry {
	r := &adapterRegistry{byID: map[string]adapter{}}
	r.register(&webAdapter{hub: h})
	r.register(&agentAdapter{})
	r.register(&agentInboundAdapter{app: app})
	r.register(&appCallbackAdapter{})
	r.register(&telegramAdapter{app: app})
	return r
}

func (r *adapterRegistry) register(a adapter) { r.byID[a.ID()] = a }

// dispatch routes "web:user:1" → webAdapter with target "user:1".
func (r *adapterRegistry) dispatch(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	prefix, rest, found := strings.Cut(target, ":")
	if !found {
		return fmt.Errorf("malformed delivery target %q", target)
	}
	a, ok := r.byID[prefix]
	if !ok {
		return fmt.Errorf("no adapter for target %q", target)
	}
	return a.Deliver(app, rest, conv, msg)
}

// ─── web (channel zero) ──────────────────────────────────────────────

// webAdapter delivers to dashboard SSE subscribers. It cannot fail in
// the ledger sense — the hub is in-process and reconnecting clients
// backfill from the store via ?since= — so Deliver always succeeds once
// published.
type webAdapter struct{ hub *hub }

func (w *webAdapter) ID() string { return "web" }

// Web targets are explicit scopes:
//
//	"conv"      → the open chat panel(s) for this conversation
//	"user:<id>" → that user's bell/tray stream
//	"project"   → every connected user-scope stream in this project
func (w *webAdapter) Deliver(_ *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	switch {
	case target == "conv":
		w.hub.publish(conv.ID, *msg)
	case target == "project":
		w.hub.publishProject(conv.ProjectID, *msg)
	default:
		if userID, ok := strings.CutPrefix(target, "user:"); ok && userID != "" && userID != "0" {
			w.hub.publishToUser(conv.ProjectID+":"+userID, *msg)
		}
	}
	return nil
}

// agentAdapter is the durable approval-result transport. Failed agent/thread
// delivery stays in the outbox and is retried by the worker.
type agentAdapter struct{}

func (*agentAdapter) ID() string { return "agent" }

func (*agentAdapter) Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	if app == nil {
		return errors.New("platform unavailable")
	}
	parts := strings.SplitN(target, ":", 2)
	agentID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || agentID <= 0 {
		return fmt.Errorf("malformed agent target %q", target)
	}
	event := sdk.ThreadEvent{ID: fmt.Sprintf("conversation:approval:%d:result", msg.ID), Message: formatApprovalResult(msg.ID, cardStatus(msg), cardNote(msg))}
	if conv != nil {
		event.Message = fmt.Sprint(event.Message) + "\nOriginating conversation_id=" + conv.ID + ". Acknowledge receipt of this decision to the operator in this conversation before continuing any approved work. Receipt does not mean the work has succeeded."
	}
	threadID := "main"
	if len(parts) == 2 && parts[1] != "" {
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return fmt.Errorf("malformed agent thread target: %w", err)
		}
		threadID = string(raw)
	}
	client := app.AgentEventsAPI()
	if client == nil {
		return errors.New("platform does not support idempotent approval receipts")
	}
	receipt, err := client.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: agentID, ThreadID: threadID, SourceEventID: event.ID, Message: event.Message})
	if err != nil {
		return err
	}
	if receipt == nil || receipt.SourceEventID != event.ID || receipt.ThreadID != threadID || (!receipt.Accepted && !receipt.Duplicate) {
		return errors.New("platform did not acknowledge the original approval destination")
	}
	return nil
}

// agentInboundAdapter is the user-message side of the same durable ledger.
// Every retry uses EnsureThread.Events with the same event id; accepted and
// duplicate receipts are both success, so a timeout cannot create two turns.
type agentInboundAdapter struct{ app *App }

func (*agentInboundAdapter) ID() string { return "agent-inbound" }

func (d *agentInboundAdapter) Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	if app == nil {
		return errors.New("platform unavailable")
	}
	agentID, err := strconv.ParseInt(target, 10, 64)
	if err != nil || agentID <= 0 {
		return fmt.Errorf("malformed inbound agent target %q", target)
	}
	eligible, err := d.app.store.IsParticipantAgent(conv.ID, agentID)
	if err != nil {
		return err
	}
	if !eligible {
		return errors.New("agent is no longer a participant")
	}
	targets := messageTargetAgentIDs(msg)
	event := sdk.ThreadEvent{
		ID:      conversationThreadEventID(conv.ID, msg.ID, agentID),
		Message: d.app.agentEventPayload(conv, msg, agentID, targets),
	}
	threadID, delivered, err := d.app.ensureConversationThreadForAgent(app, conv, agentID, &event)
	if err != nil {
		return err
	}
	if !delivered {
		return fmt.Errorf("platform did not confirm inbound event %q", event.ID)
	}
	// A soft break is sent only while an existing response is active. Keep that
	// response's acknowledgement/stream bubble authoritative instead of
	// replacing it with a second synthetic ack that could settle out of order.
	// The durable "Break requested" transcript row is the immediate feedback.
	if messageIntent(msg) != messageIntentSoftBreak {
		d.app.streamer.emitAck(conv.ID, threadID, agentID, msg.ID)
	}
	return nil
}

type appCallbackAdapter struct{}

func (*appCallbackAdapter) ID() string { return "callback-app" }

func (*appCallbackAdapter) Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	if app == nil {
		return errors.New("platform unavailable")
	}
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("malformed app callback target %q", target)
	}
	var out map[string]any
	return app.WithProject(conv.ProjectID).PlatformAPI().CallAppResult(parts[0], parts[1], map[string]any{
		"operation_id": fmt.Sprintf("conversations:approval:%d", msg.ID),
		"message_id":   msg.ID,
		"action_id":    cardStatus(msg),
		"note":         cardNote(msg),
	}, &out)
}

func approvalDeliveryTarget(m *Message) string {
	if m.SourceApp != "" && m.CallbackTool != "" {
		return "callback-app:" + m.SourceApp + ":" + m.CallbackTool
	}
	if m.AgentID <= 0 {
		return ""
	}
	target := "agent:" + strconv.FormatInt(m.AgentID, 10)
	if m.ThreadID != "" {
		target += ":" + base64.RawURLEncoding.EncodeToString([]byte(m.ThreadID))
	}
	return target
}

// ─── delivery fan-out ────────────────────────────────────────────────

// deliver writes ledger rows for every target a conversation is bound
// to, then attempts each one. Called after every append; also the
// replay path on mount (redeliverPending) for crash recovery.
func (a *App) deliver(app *sdk.AppCtx, conv *Conversation, msg *Message) {
	targets, err := a.deliveryTargets(conv, msg)
	if err != nil {
		if app != nil {
			app.Logger().Warn("delivery target lookup failed", "message", msg.ID, "err", err)
		}
	}
	for _, target := range targets {
		if err := a.store.EnsureDelivery(msg.ID, target); err != nil {
			if app != nil {
				app.Logger().Warn("delivery enqueue failed", "message", msg.ID, "target", target, "err", err)
			}
			continue
		}
		a.dispatchOrQueue(app, target, conv, msg)
	}
}

// appendAndDeliver is the only production append path. Message and initial
// outbox rows commit atomically; immediate dispatch happens after commit.
func (a *App) appendAndDeliver(app *sdk.AppCtx, conv *Conversation, msg *Message) (*Message, bool, error) {
	targets, err := a.deliveryTargets(conv, msg)
	if err != nil {
		return nil, false, err
	}
	stored, inserted, err := a.store.AppendMessageWithDeliveries(msg, targets)
	if err != nil || !inserted {
		return stored, inserted, err
	}
	for _, target := range targets {
		a.dispatchOrQueue(app, target, conv, stored)
	}
	return stored, true, nil
}

// deliveryTargets uses resource scopes: private rows go only to their owner
// and explicit participants; ownerless cards also reach the project inbox.
func (a *App) deliveryTargets(conv *Conversation, msg *Message) ([]string, error) {
	targets := []string{"web:conv"}
	if conv.OwnerUserID != 0 {
		targets = append(targets, "web:user:"+fmt.Sprint(conv.OwnerUserID))
	}
	users, err := a.store.db.Query(`SELECT DISTINCT user_id FROM participants WHERE conversation_id=? AND user_id>0 AND user_id!=?`, conv.ID, conv.OwnerUserID)
	if err != nil {
		return nil, err
	}
	for users.Next() {
		var user int64
		if err := users.Scan(&user); err != nil {
			users.Close()
			return nil, err
		}
		targets = append(targets, "web:user:"+fmt.Sprint(user))
	}
	err = users.Err()
	users.Close()
	if err != nil {
		return nil, err
	}
	if msg.ComponentKind != "" && conv.OwnerUserID == 0 {
		targets = append(targets, "web:project")
	}
	if msg.Role == "user" {
		agentIDs := messageTargetAgentIDs(msg)
		if len(agentIDs) == 0 && conv.LeadAgentID > 0 {
			agentIDs = []int64{conv.LeadAgentID}
		}
		for _, agentID := range agentIDs {
			targets = append(targets, "agent-inbound:"+strconv.FormatInt(agentID, 10))
		}
	}
	// Do not echo a Telegram user's inbound update back into Telegram. Every
	// other durable message fans out to each chat explicitly bound to this
	// conversation.
	if messageIntent(msg) != messageIntentSoftBreak && !strings.HasPrefix(msg.ExternalSender, "telegram:") {
		bindings, err := a.store.TelegramBindingsForConversation(conv.ID)
		if err != nil {
			return targets, err
		}
		for _, binding := range bindings {
			targets = append(targets, "telegram:"+binding.ID)
		}
	}
	return targets, nil
}

func messageTargetAgentIDs(msg *Message) []int64 {
	if msg == nil || msg.Metadata == nil {
		return nil
	}
	raw, exists := msg.Metadata["target_agent_ids"]
	if !exists {
		return nil
	}
	seen := map[int64]bool{}
	var out []int64
	appendID := func(id int64) {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	switch values := raw.(type) {
	case []int64:
		for _, id := range values {
			appendID(id)
		}
	case []any:
		for _, value := range values {
			switch id := value.(type) {
			case float64:
				appendID(int64(id))
			case int64:
				appendID(id)
			case int:
				appendID(int64(id))
			}
		}
	}
	return out
}

func messageIntent(msg *Message) string {
	if msg == nil || msg.Metadata == nil {
		return ""
	}
	intent, _ := msg.Metadata["intent"].(string)
	return strings.TrimSpace(intent)
}

func (a *App) attemptDelivery(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) {
	claim, err := a.store.ClaimDelivery(msg.ID, target)
	if err != nil {
		if app != nil {
			app.Logger().Warn("delivery claim failed", "err", err)
		}
		return
	}
	if claim == nil {
		return
	}
	stopLease := a.maintainLease(msg.ID, target, claim.Token)
	// Read the payload after claiming its generation. A mutation between the
	// recovery scan and claim must not confirm delivery of stale card content.
	current, readErr := a.store.GetMessage(msg.ID)
	if readErr != nil {
		err = readErr
	} else {
		err = a.adapters.dispatch(app, target, conv, current)
	}
	stopLease()
	if finishErr := a.store.FinishDelivery(msg.ID, target, claim, err); finishErr != nil && app != nil {
		app.Logger().Warn("delivery completion persistence failed", "message", msg.ID, "target", target, "err", finishErr)
	}
}

// redeliverPending is the crash-recovery half of the ledger: rows the
// previous process recorded but never confirmed go out again.
func (a *App) redeliverPending(app *sdk.AppCtx) (int, error) {
	redelivered := 0
	if err := a.store.RecoverExpiredDeliveries(); err != nil {
		return 0, err
	}
	for batch := 0; batch < 4; batch++ {
		if a.deliveryWorker != nil {
			select {
			case <-a.deliveryWorker.stop:
				return redelivered, nil
			default:
			}
		}
		pending, err := a.store.PendingDeliveries(500)
		if err != nil {
			return redelivered, err
		}
		if len(pending) == 0 {
			return redelivered, nil
		}
		for _, d := range pending {
			msg, err := a.store.GetMessage(d.MessageID)
			if err != nil {
				continue
			}
			conv, err := a.store.GetConversation(msg.ConversationID)
			if err != nil {
				_, cancelErr := a.store.db.Exec(`UPDATE deliveries SET status='cancelled',last_error='Conversation unavailable' WHERE id=? AND status='pending'`, d.ID)
				if cancelErr != nil {
					return redelivered, cancelErr
				}
				continue
			}
			a.attemptDelivery(app, d.Target, conv, msg)
			redelivered++
		}
		if len(pending) < 500 {
			return redelivered, nil
		}
	}
	return redelivered, nil
}
