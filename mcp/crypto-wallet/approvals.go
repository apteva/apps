package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const approvalCallbackTool = "crypto_wallet_approval_callback"

// approvalPoster posts an approval card and returns its message id. An
// interface so tests can capture cards without a Conversations install.
type approvalPoster func(projectID, title, body string) (int64, error)

func (a *App) postViaConversations(projectID, title, body string) (int64, error) {
	if a.ctx == nil {
		return 0, errors.New("platform unavailable")
	}
	var out struct {
		MessageID int64 `json:"message_id"`
	}
	err := a.ctx.WithProject(projectID).PlatformAPI().CallAppResult("conversations", "inbox_post", map[string]any{
		"kind":          "approval",
		"title":         title,
		"body":          body,
		"callback_tool": approvalCallbackTool,
		"actions": []map[string]any{
			{"id": "approve", "label": "Approve", "style": "primary"},
			{"id": "deny", "label": "Deny", "style": "danger"},
		},
	}, &out)
	if err != nil {
		return 0, err
	}
	if out.MessageID <= 0 {
		return 0, errors.New("conversations returned no message id")
	}
	return out.MessageID, nil
}

func (a *App) poster() approvalPoster {
	if a.postApproval != nil {
		return a.postApproval
	}
	return a.postViaConversations
}

func shortAddr(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:6] + "..." + s[len(s)-4:]
}

func (a *App) requestTransferApproval(w *Wallet, p *Proposal) {
	chain, _ := chainBySlug(w.Chain)
	label := ""
	if r, err := getRecipientByAddress(a.db, w.ID, p.ToAddress); err == nil && r.Label != "" {
		label = " (" + r.Label + ")"
	}
	title := fmt.Sprintf("Approve %s %s to %s%s?", p.Amount, p.Token, shortAddr(p.ToAddress), label)
	var b strings.Builder
	fmt.Fprintf(&b, "Wallet: %s on %s\n", w.Name, chain.Name)
	fmt.Fprintf(&b, "Amount: %s %s (about %s)\n", p.Amount, p.Token, usd(p.USDMicros))
	fmt.Fprintf(&b, "To: %s%s\n", p.ToAddress, label)
	fmt.Fprintf(&b, "Requested by: %s\n", requester(p))
	if p.Memo != "" {
		fmt.Fprintf(&b, "Reason given: %s\n", p.Memo)
	}
	fmt.Fprintf(&b, "Proposal #%d. Approving sends it on-chain; this cannot be reversed.", p.ID)
	id, err := a.poster()(w.ProjectID, title, b.String())
	if err != nil {
		a.logWarn("approval card not posted; approve from the panel", "proposal", p.ID, "err", err)
		_ = appendLedger(a.db, w.ID, "approval.card_failed", "system", p.ID, map[string]any{"error": err.Error()}, a.now())
		return
	}
	_, _ = a.db.Exec(`UPDATE proposals SET approval_message_id = ? WHERE id = ?`, id, p.ID)
	_ = appendLedger(a.db, w.ID, "approval.requested", "system", p.ID, map[string]any{"message_id": id}, a.now())
}

func (a *App) requestRecipientApproval(r *Recipient) {
	w, err := getWallet(a.db, r.WalletID)
	if err != nil {
		return
	}
	pol, _ := currentPolicy(a.db, w.ID)
	label := r.Label
	if label == "" {
		label = "no label"
	}
	title := fmt.Sprintf("Allow %s to receive from %s?", shortAddr(r.Address), w.Name)
	body := fmt.Sprintf("Address: %s (%s)\nProposed by: agent %d\nReason given: %s\nIf approved, it can receive funds after %d hours.",
		r.Address, label, r.ProposedByAgent, firstNonEmpty(r.Reason, "none"), pol.NewRecipientDelayHours)
	id, err := a.poster()(w.ProjectID, title, body)
	if err != nil {
		a.logWarn("recipient approval card not posted; approve from the panel", "recipient", r.ID, "err", err)
		return
	}
	_, _ = a.db.Exec(`UPDATE allowlist SET approval_message_id = ? WHERE id = ?`, id, r.ID)
}

func requester(p *Proposal) string {
	switch {
	case p.AgentID > 0:
		return fmt.Sprintf("agent %d", p.AgentID)
	case p.CallerApp != "":
		return "app " + p.CallerApp
	}
	return "unknown"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// toolApprovalCallback receives a person's decision from a Conversations
// approval card. It is app-only (never listed to agents) and additionally
// checks the authenticated caller is Conversations itself, so neither an
// agent nor another app can approve a transfer through it.
func (a *App) toolApprovalCallback(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AppName != "conversations" || caller.AppInstallID <= 0 || caller.AgentID != 0 {
		return nil, errors.New("only the Conversations app may deliver approval decisions")
	}
	messageID := int64Arg(args, "message_id")
	action := strings.ToLower(strArg(args, "action_id"))
	note := strArg(args, "note")
	if messageID <= 0 {
		return nil, errors.New("message_id required")
	}
	var approve bool
	switch action {
	case "approve", "approved":
		approve = true
	case "deny", "denied", "reject", "rejected":
	default:
		return map[string]any{"ignored": true, "reason": "action " + action + " is not a decision"}, nil
	}
	const via, actor = "conversations", "app:conversations"
	var proposalID int64
	if err := a.db.QueryRow(`SELECT id FROM proposals WHERE approval_message_id = ?`, messageID).Scan(&proposalID); err == nil {
		bctx, cancel := bg()
		defer cancel()
		p, err := a.decideProposal(bctx, proposalID, approve, actor, via, note, 0)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil // already decided elsewhere: not a delivery failure
		}
		return map[string]any{"ok": true, "proposal": p}, nil
	}
	var recipientID int64
	if err := a.db.QueryRow(`SELECT id FROM allowlist WHERE approval_message_id = ?`, messageID).Scan(&recipientID); err == nil {
		r, err := a.decideRecipient(recipientID, approve, actor, via, note, 0)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true, "recipient": r}, nil
	}
	return map[string]any{"ok": false, "error": "no proposal or recipient for this message"}, nil
}

func (a *App) logWarn(msg string, kv ...any) {
	if a.ctx != nil {
		a.ctx.Logger().Warn(msg, kv...)
	}
}
