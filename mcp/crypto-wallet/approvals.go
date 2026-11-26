package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Approval requests are plain records: the proposal (or recipient) stays
// pending until a person decides in the panel or an agent with the
// approvals.decide grant calls crypto_wallet_proposal_decide /
// crypto_wallet_recipient_decide. The app also publishes
// crypto_wallet.approval.requested so anything that wants to notify
// people can subscribe; nothing waits on a reply.

func shortAddr(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:6] + "..." + s[len(s)-4:]
}

func (a *App) announceTransferApproval(w *Wallet, p *Proposal) {
	a.emit("crypto_wallet.approval.requested", map[string]any{
		"kind": "transfer", "wallet_id": w.ID, "proposal_id": p.ID, "token": p.Token,
		"amount": p.Amount, "usd": usd(p.USDMicros), "to": p.ToAddress, "memo": p.Memo,
		"requested_by": requester(p),
	})
}

func (a *App) announceRecipientApproval(r *Recipient) {
	a.emit("crypto_wallet.approval.requested", map[string]any{
		"kind": "recipient", "wallet_id": r.WalletID, "recipient_id": r.ID, "address": r.Address,
		"label": r.Label, "reason": r.Reason, "requested_by": agentActor(r.ProposedByAgent),
	})
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

// toolDecider names who is deciding through an MCP tool call. Which agents
// may call the decide tools at all is the operator's grant on
// approvals.decide, enforced by the SDK before the handler runs.
func toolDecider(ctx context.Context) (actor string, agentID int64) {
	c := sdk.CallerFrom(ctx)
	switch {
	case c == nil:
		return "mcp", 0
	case c.AgentID > 0:
		return agentActor(c.AgentID), c.AgentID
	case c.AppName != "":
		return "app:" + c.AppName, 0
	}
	return "mcp", 0
}

func parseDecision(args map[string]any) (bool, error) {
	switch strings.ToLower(strArg(args, "decision")) {
	case "approve":
		return true, nil
	case "reject":
		return false, nil
	}
	return false, errors.New(`decision must be "approve" or "reject"`)
}

func (a *App) logWarn(msg string, kv ...any) {
	if a.ctx != nil {
		a.ctx.Logger().Warn(msg, kv...)
	}
}
