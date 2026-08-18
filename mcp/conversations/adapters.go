package main

// adapters.go — the transport seam.
//
// An adapter delivers messages to one kind of surface. `web` (the SSE
// hub) is channel zero: always configured, always first. External
// platforms (telegram, slack, …) register here when their optional
// integration binding is present.
//
// The rule that keeps external channels from becoming conversation
// *types*: adapters receive ordinary messages and express platform
// differences (buttons, edits, attachment limits) internally. Nothing
// upstream branches on origin.

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// errAdapterNotConfigured marks a delivery target whose transport has
// no binding yet. The ledger keeps such rows pending rather than
// failing them: bind the integration later and the backlog drains on
// the next mount.
var errAdapterNotConfigured = errors.New("adapter not configured")

type adapter interface {
	// ID is the target prefix this adapter owns ("web", "telegram").
	ID() string
	// Deliver pushes one message to one target address (the part after
	// "<id>:" in a deliveries.target).
	Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error
}

type adapterRegistry struct {
	byID map[string]adapter
}

func newAdapterRegistry(h *hub) *adapterRegistry {
	r := &adapterRegistry{byID: map[string]adapter{}}
	r.register(&webAdapter{hub: h})
	r.register(&telegramAdapter{})
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
//	"broadcast" → every connected user-scope stream — inbox items are
//	              operator-facing regardless of who owns the source
//	              conversation (and inbox_post system conversations
//	              have no owner at all)
func (w *webAdapter) Deliver(_ *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	switch {
	case target == "conv":
		w.hub.publish(conv.ID, *msg)
	case target == "broadcast":
		w.hub.publishBroadcast(*msg)
	default:
		if userID, ok := strings.CutPrefix(target, "user:"); ok && userID != "" && userID != "0" {
			w.hub.publishToUser(userID, *msg)
		}
	}
	return nil
}

// ─── telegram (stub, gated on the optional binding) ──────────────────

// telegramAdapter is the compiling skeleton for the first external
// transport. v0 ships it unconfigured: Deliver reports
// errAdapterNotConfigured so ledger rows stay pending, and the inbound
// webhook route replies 503. The full implementation (long-poll +
// webhook, pairing enforcement, message-edit streaming, media) is
// phase 4 in DESIGN.md — it slots in behind this exact interface
// without touching the store, the inbox, or the tools.
type telegramAdapter struct{}

func (t *telegramAdapter) ID() string { return "telegram" }

func (t *telegramAdapter) Deliver(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) error {
	// When implemented: resolve the telegram_bot binding →
	// platform.connections.execute sendMessage (or editMessageText for
	// streaming updates) against the bound bot, chat id = target.
	return fmt.Errorf("telegram %q: %w", target, errAdapterNotConfigured)
}

// ─── delivery fan-out ────────────────────────────────────────────────

// deliver writes ledger rows for every target a conversation is bound
// to, then attempts each one. Called after every append; also the
// replay path on mount (redeliverPending) for crash recovery.
func (a *App) deliver(app *sdk.AppCtx, conv *Conversation, msg *Message) {
	for _, target := range a.deliveryTargets(conv, msg) {
		_ = a.store.EnsureDelivery(msg.ID, target)
		a.attemptDelivery(app, target, conv, msg)
	}
}

// deliveryTargets computes where a message goes:
//   - the conversation panel, always
//   - the owner's user-scope stream, when the conversation has an owner
//   - EVERY user-scope stream for inbox items — the bell must ring for
//     the operator whoever owns the source conversation, and inbox_post
//     system conversations have no owner at all (this was the user-0 bug)
//   - the external binding, when one exists — but never echoed back to
//     the transport the message arrived from
func (a *App) deliveryTargets(conv *Conversation, msg *Message) []string {
	targets := []string{"web:conv"}
	if conv.OwnerUserID != 0 {
		targets = append(targets, "web:user:"+fmt.Sprint(conv.OwnerUserID))
	}
	if msg.ComponentKind != "" {
		targets = append(targets, "web:broadcast")
	}
	if conv.ConversationKey != "" && msg.ExternalSender == "" && conv.Origin != "app" {
		// conversation_key is already "<adapter>:<binding>:<chat>" —
		// a valid delivery target by construction. App-origin system
		// conversations have an "app:" key that is not a transport.
		targets = append(targets, conv.ConversationKey)
	}
	return targets
}

func (a *App) attemptDelivery(app *sdk.AppCtx, target string, conv *Conversation, msg *Message) {
	err := a.adapters.dispatch(app, target, conv, msg)
	switch {
	case err == nil:
		_ = a.store.MarkDelivered(msg.ID, target)
	case errors.Is(err, errAdapterNotConfigured):
		// Leave pending: the binding may appear later and the mount
		// replay will drain the backlog.
		_ = a.store.RecordDeliveryError(msg.ID, target, err.Error(), false)
	default:
		_ = a.store.RecordDeliveryError(msg.ID, target, err.Error(), true)
	}
}

// redeliverPending is the crash-recovery half of the ledger: rows the
// previous process recorded but never confirmed go out again.
func (a *App) redeliverPending(app *sdk.AppCtx) (int, error) {
	pending, err := a.store.PendingDeliveries(500)
	if err != nil {
		return 0, err
	}
	redelivered := 0
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
	return redelivered, nil
}
