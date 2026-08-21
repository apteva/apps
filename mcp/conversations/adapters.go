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

func (*agentAdapter) Deliver(app *sdk.AppCtx, target string, _ *Conversation, msg *Message) error {
	if app == nil {
		return errors.New("platform unavailable")
	}
	parts := strings.SplitN(target, ":", 2)
	agentID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || agentID <= 0 {
		return fmt.Errorf("malformed agent target %q", target)
	}
	event := formatApprovalResult(msg.ID, cardStatus(msg), cardNote(msg))
	if len(parts) == 2 && parts[1] != "" {
		rawThread, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return fmt.Errorf("malformed agent thread target: %w", err)
		}
		if tc, ok := app.PlatformAPI().(sdk.ThreadClient); ok {
			if err := tc.SendThreadEvent(sdk.ThreadRef{AgentID: agentID, ThreadID: string(rawThread)}, event); err == nil {
				return nil
			}
		}
	}
	return app.PlatformAPI().SendEvent(agentID, event)
}

// agentInboundAdapter is the user-message side of the same durable ledger.
// Every retry uses SpawnThread.Events with the same event id; accepted and
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
	d.app.streamer.emitAck(conv.ID, threadID)
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
		"message_id": msg.ID,
		"action_id":  cardStatus(msg),
		"note":       cardNote(msg),
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
		a.attemptDelivery(app, target, conv, msg)
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
		a.attemptDelivery(app, target, conv, stored)
	}
	return stored, true, nil
}

// deliveryTargets computes where a message goes:
//   - the conversation panel, always
//   - the owner's user-scope stream, when the conversation has an owner
//   - EVERY user-scope stream for inbox items — the bell must ring for
//     the operator whoever owns the source conversation, and inbox_post
//     system conversations have no owner at all (this was the user-0 bug)
func (a *App) deliveryTargets(conv *Conversation, msg *Message) ([]string, error) {
	targets := []string{"web:conv"}
	if conv.OwnerUserID != 0 {
		targets = append(targets, "web:user:"+fmt.Sprint(conv.OwnerUserID))
	}
	if msg.ComponentKind != "" {
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
	if !strings.HasPrefix(msg.ExternalSender, "telegram:") {
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

func (a *App) attemptDelivery(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) {
	err := a.adapters.dispatch(app, target, conv, msg)
	switch {
	case err == nil:
		_ = a.store.MarkDelivered(msg.ID, target)
	default:
		terminal := strings.Contains(err.Error(), "malformed") || strings.Contains(err.Error(), "no adapter")
		failed, _ := a.store.RecordDeliveryError(msg.ID, target, err.Error(), terminal)
		if failed && app != nil {
			app.Logger().Warn("delivery moved to dead letter", "message", msg.ID, "target", target, "err", err)
		}
	}
}

// redeliverPending is the crash-recovery half of the ledger: rows the
// previous process recorded but never confirmed go out again.
func (a *App) redeliverPending(app *sdk.AppCtx) (int, error) {
	redelivered := 0
	for {
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
				continue
			}
			a.attemptDelivery(app, d.Target, conv, msg)
			redelivered++
		}
		if len(pending) < 500 {
			return redelivered, nil
		}
	}
}
