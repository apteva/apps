package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func schema(required []string, props map[string]any) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

var walletIDProp = prop("integer", "Wallet id from crypto_wallet_list.")

// MCPTools: read, propose, and decide pending approvals. Which agent may
// call which tool is the operator's grant on the permission each tool
// requires (see apteva.yaml provides.permissions); the SDK enforces it
// before a handler runs. Creating wallets, changing policies, freezing and
// exporting keys stay panel-only.
func (a *App) MCPTools() []sdk.Tool {
	readOnly := map[string]any{"readOnlyHint": true}
	return []sdk.Tool{
		{
			Name:        "crypto_wallet_list",
			Description: "List the crypto wallets you may use: id, name, chain, address, frozen state and the current policy (caps in USD, approval threshold, allowed tokens).",
			InputSchema: schema(nil, map[string]any{}),
			Annotations: readOnly,
			HandlerCtx:  a.toolList,
		},
		{
			Name:        "crypto_wallet_balance",
			Description: "ETH and USDC balances of a wallet, read live from the chain, with USD values. ETH is also what pays gas for every transfer.",
			InputSchema: schema([]string{"wallet_id"}, map[string]any{"wallet_id": walletIDProp}),
			Annotations: readOnly,
			HandlerCtx:  a.toolBalance,
		},
		{
			Name:        "crypto_wallet_receive_address",
			Description: "The address and chain a payer should use to send funds to a wallet. Sending from another chain loses the funds, so always give the chain with the address.",
			InputSchema: schema([]string{"wallet_id"}, map[string]any{"wallet_id": walletIDProp}),
			Annotations: readOnly,
			HandlerCtx:  a.toolReceiveAddress,
		},
		{
			Name:        "crypto_wallet_history",
			Description: "Recent transfer proposals (with outcome and refusal rule) and on-chain transactions, in and out, newest first.",
			InputSchema: schema([]string{"wallet_id"}, map[string]any{"wallet_id": walletIDProp, "limit": prop("integer", "Max rows of each kind (default 20, max 100).")}),
			Annotations: readOnly,
			HandlerCtx:  a.toolHistory,
		},
		{
			Name: "crypto_wallet_transfer_propose",
			Description: "Propose sending ETH or USDC to an allowlisted address. The wallet's policy decides: outcome 'sent' (signed and broadcast; check crypto_wallet_proposal_status for confirmation), " +
				"'pending_approval' (a person must approve; do not resubmit), or 'refused' with a rule: frozen, token_not_allowed, recipient_not_allowed, recipient_cooling_off, per_transfer_cap, daily_cap, too_many_pending. " +
				"A refusal is final for that request; you cannot change policies, so report it rather than retrying with different amounts to fit under a cap. Transfers cannot be reversed.",
			InputSchema: schema([]string{"wallet_id", "token", "to", "amount"}, map[string]any{
				"wallet_id": walletIDProp,
				"token":     map[string]any{"type": "string", "enum": []string{"USDC", "ETH"}},
				"to":        prop("string", "Recipient address, 0x followed by 40 hex characters. Must already be on the wallet's allowlist."),
				"amount":    prop("string", "Decimal amount in token units, e.g. \"12.50\" for 12.50 USDC. Not cents, not wei."),
				"memo":      prop("string", "Why this payment is being made; shown to the person approving it."),
			}),
			Annotations: map[string]any{"destructiveHint": true},
			HandlerCtx:  a.toolTransferPropose,
		},
		{
			Name:        "crypto_wallet_recipient_propose",
			Description: "Ask a person to add an address to a wallet's allowlist. It stays pending until approved, then can receive funds only after the policy's waiting period (24 hours by default).",
			InputSchema: schema([]string{"wallet_id", "address", "reason"}, map[string]any{
				"wallet_id": walletIDProp,
				"address":   prop("string", "0x address to allow."),
				"label":     prop("string", "Who this is, e.g. \"Acme Ltd invoices\"."),
				"reason":    prop("string", "Why payments to this address are needed; shown to the approver."),
			}),
			HandlerCtx: a.toolRecipientPropose,
		},
		{
			Name:        "crypto_wallet_proposal_status",
			Description: "Status of a transfer proposal: pending_approval, rejected, refused, sending, sent, confirmed or failed, with the transaction hash and explorer link once sent.",
			InputSchema: schema([]string{"proposal_id"}, map[string]any{"proposal_id": prop("integer", "Proposal id returned by crypto_wallet_transfer_propose.")}),
			Annotations: readOnly,
			HandlerCtx:  a.toolProposalStatus,
		},
		{
			Name:        "crypto_wallet_approvals_pending",
			Description: "Transfers and recipients waiting for approval, oldest first. Omit wallet_id for every wallet.",
			InputSchema: schema(nil, map[string]any{"wallet_id": prop("integer", "Optional wallet id from crypto_wallet_list.")}),
			Annotations: readOnly,
			HandlerCtx:  a.toolApprovalsPending,
		},
		{
			Name: "crypto_wallet_proposal_decide",
			Description: "Approve or reject a transfer waiting for approval. Approving re-checks every rule (freeze, token list, allowlist, caps) and, if they pass, " +
				"signs and broadcasts it; that cannot be undone. Only decide when the person you work for asked you to.",
			InputSchema: schema([]string{"proposal_id", "decision"}, map[string]any{
				"proposal_id": prop("integer", "Proposal id in pending_approval."),
				"decision":    map[string]any{"type": "string", "enum": []string{"approve", "reject"}},
				"note":        prop("string", "Why, recorded in the log."),
			}),
			Annotations: map[string]any{"destructiveHint": true},
			HandlerCtx:  a.toolProposalDecide,
		},
		{
			Name: "crypto_wallet_recipient_decide",
			Description: "Approve or reject an address waiting to join a wallet's allowlist. An approved address proposed by an agent can receive funds only after the " +
				"policy's waiting period. Only decide when the person you work for asked you to.",
			InputSchema: schema([]string{"recipient_id", "decision"}, map[string]any{
				"recipient_id": prop("integer", "Recipient id from crypto_wallet_approvals_pending or crypto_wallet_recipient_propose."),
				"decision":     map[string]any{"type": "string", "enum": []string{"approve", "reject"}},
				"note":         prop("string", "Why, recorded in the log."),
			}),
			Annotations: map[string]any{"destructiveHint": true},
			HandlerCtx:  a.toolRecipientDecide,
		},
	}
}

// ─── Handlers ────────────────────────────────────────────────────────

func (a *App) toolList(_ context.Context, _ *sdk.AppCtx, _ map[string]any) (any, error) {
	wallets, err := listWallets(a.db)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, w := range wallets {
		pol, err := currentPolicy(a.db, w.ID)
		if err != nil {
			return nil, err
		}
		chain, _ := chainBySlug(w.Chain)
		out = append(out, map[string]any{
			"id": w.ID, "name": w.Name, "chain": w.Chain, "chain_name": chain.Name, "testnet": chain.Testnet,
			"address": w.Address, "frozen": w.Frozen,
			"policy": map[string]any{
				"per_transfer_cap_usd": usd(pol.PerTxUSDMicros), "daily_cap_usd": usd(pol.DailyUSDMicros),
				"approval_above_usd": usd(pol.ApprovalUSDMicros), "tokens": pol.Tokens,
			},
		})
	}
	return map[string]any{"wallets": out}, nil
}

func (a *App) toolBalance(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := getWallet(a.db, int64Arg(args, "wallet_id"))
	if err != nil {
		return nil, err
	}
	bals, err := a.balances(ctx, w)
	if err != nil {
		return nil, err
	}
	return map[string]any{"wallet_id": w.ID, "chain": w.Chain, "address": w.Address, "balances": bals}, nil
}

func (a *App) toolReceiveAddress(_ context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := getWallet(a.db, int64Arg(args, "wallet_id"))
	if err != nil {
		return nil, err
	}
	chain, _ := chainBySlug(w.Chain)
	return map[string]any{
		"address": w.Address, "chain": chain.Name, "chain_id": chain.ChainID, "testnet": chain.Testnet,
		"tokens": chain.Tokens,
		"note":   fmt.Sprintf("Send only on %s (chain id %d). Funds sent on another network are lost.", chain.Name, chain.ChainID),
	}, nil
}

func (a *App) toolHistory(_ context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := getWallet(a.db, int64Arg(args, "wallet_id"))
	if err != nil {
		return nil, err
	}
	limit := clampLimit(int64Arg(args, "limit"), 20, 100)
	props, err := listProposals(a.db, w.ID, nil, limit)
	if err != nil {
		return nil, err
	}
	txs, err := listTransactions(a.db, w.ID, limit)
	if err != nil {
		return nil, err
	}
	a.decorateTxs(w, txs)
	return map[string]any{"proposals": props, "transactions": txs}, nil
}

func (a *App) toolTransferPropose(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	req := transferRequest{
		WalletID: int64Arg(args, "wallet_id"), Token: strArg(args, "token"), To: strArg(args, "to"),
		Amount: strArg(args, "amount"), Memo: strArg(args, "memo"),
	}
	if c := sdk.CallerFrom(ctx); c != nil {
		req.AgentID, req.CallerApp, req.ToolCallID = c.AgentID, c.AppName, c.ToolCallID
	}
	p, err := a.proposeTransfer(ctx, req)
	if err != nil {
		return nil, err
	}
	return a.proposalView(p), nil
}

func (a *App) toolRecipientPropose(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	var agentID int64
	if c := sdk.CallerFrom(ctx); c != nil {
		agentID = c.AgentID
	}
	if strings.TrimSpace(strArg(args, "reason")) == "" {
		return nil, errors.New("reason required: the approver needs to know why")
	}
	r, created, err := a.proposeRecipient(int64Arg(args, "wallet_id"), strArg(args, "address"), strArg(args, "label"), strArg(args, "reason"), agentID)
	if err != nil {
		return nil, err
	}
	msg := "Waiting for approval in the panel or by an agent allowed to approve."
	switch {
	case !created && r.Status == "approved":
		msg = "Already on the allowlist."
		if parseTS(r.UsableAfter).After(a.now()) {
			msg += " It can receive funds from " + r.UsableAfter + "."
		}
	case !created && r.Status == "rejected":
		msg = "A person rejected this recipient. Do not propose it again unless asked."
	case !created:
		msg = "Already waiting for approval."
	}
	return map[string]any{"recipient": r, "message": msg}, nil
}

func (a *App) toolApprovalsPending(_ context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	wallets, err := listWallets(a.db)
	if err != nil {
		return nil, err
	}
	only := int64Arg(args, "wallet_id")
	transfers, recipients := []map[string]any{}, []*Recipient{}
	for _, w := range wallets {
		if only > 0 && w.ID != only {
			continue
		}
		props, err := listProposals(a.db, w.ID, []string{"pending_approval"}, 100)
		if err != nil {
			return nil, err
		}
		for i := len(props) - 1; i >= 0; i-- { // listed newest first
			v := a.proposalView(props[i])
			v["wallet_name"] = w.Name
			transfers = append(transfers, v)
		}
		rs, err := listRecipients(a.db, w.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if r.Status == "pending" {
				recipients = append(recipients, r)
			}
		}
	}
	return map[string]any{"transfers": transfers, "recipients": recipients}, nil
}

func (a *App) toolProposalDecide(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	approve, err := parseDecision(args)
	if err != nil {
		return nil, err
	}
	actor, agentID := toolDecider(ctx)
	bctx, cancel := bg()
	defer cancel()
	p, err := a.decideProposal(bctx, int64Arg(args, "proposal_id"), approve, actor, "mcp", strArg(args, "note"), 0, agentID)
	if err != nil {
		return nil, err
	}
	return a.proposalView(p), nil
}

func (a *App) toolRecipientDecide(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	approve, err := parseDecision(args)
	if err != nil {
		return nil, err
	}
	actor, agentID := toolDecider(ctx)
	r, err := a.decideRecipient(int64Arg(args, "recipient_id"), approve, actor, "mcp", strArg(args, "note"), 0, agentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"recipient": r}, nil
}

func (a *App) toolProposalStatus(_ context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := getProposal(a.db, int64Arg(args, "proposal_id"))
	if err != nil {
		return nil, err
	}
	return a.proposalView(p), nil
}

// proposalView adds a plain-language outcome and an explorer link.
func (a *App) proposalView(p *Proposal) map[string]any {
	out := map[string]any{"proposal": p, "outcome": p.State}
	if w, err := getWallet(a.db, p.WalletID); err == nil {
		if chain, err := chainBySlug(w.Chain); err == nil && p.TxHash != "" {
			out["explorer_url"] = chain.Explorer + "/tx/" + p.TxHash
		}
	}
	switch p.State {
	case "refused":
		out["message"] = "Refused by rule " + p.RefusalRule + ": " + p.RefusalDetail
	case "pending_approval":
		out["message"] = "Waiting for approval in the panel or by an agent allowed to approve. Do not submit it again."
	case "sent":
		out["message"] = "Broadcast. It confirms within a few seconds on Base; check crypto_wallet_proposal_status."
	case "confirmed":
		out["message"] = "Confirmed on-chain."
	case "failed":
		out["message"] = "Failed: " + p.Error
	case "rejected":
		out["message"] = "A person rejected this transfer."
	}
	return out
}

func (a *App) decorateTxs(w *Wallet, txs []*ChainTx) {
	chain, err := chainBySlug(w.Chain)
	if err != nil {
		return
	}
	for _, t := range txs {
		if tok, err := chain.token(t.Token); err == nil {
			if n, ok := parseBigDec(t.AmountBase); ok {
				t.Amount = formatUnits(n, tok.Decimals)
			}
		}
	}
}

// ─── Argument helpers ────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		if v == math.Trunc(v) {
			return int64(v)
		}
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func clampLimit(n int64, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > int64(max) {
		return max
	}
	return int(n)
}
