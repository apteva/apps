package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

type transferRequest struct {
	WalletID   int64
	Token      string
	To         string
	Amount     string
	Memo       string
	AgentID    int64
	CallerApp  string
	ToolCallID string
}

func (r transferRequest) actor() string {
	switch {
	case r.AgentID > 0:
		return agentActor(r.AgentID)
	case r.CallerApp != "":
		return "app:" + r.CallerApp
	}
	return "unknown"
}

// proposeTransfer checks a transfer against the wallet's policy and then
// signs it, queues it for approval, or records the refusal. Every outcome is
// a stored proposal, so refusals are as visible in the log as payments.
func (a *App) proposeTransfer(ctx context.Context, req transferRequest) (*Proposal, error) {
	if req.ToolCallID != "" {
		if p, err := scanProposal(a.db.QueryRow(`SELECT `+proposalCols+` FROM proposals WHERE tool_call_id = ?`, req.ToolCallID)); err == nil {
			return p, nil // a retried tool call gets the original answer, never a second transfer
		}
	}
	w, err := getWallet(a.db, req.WalletID)
	if err != nil {
		return nil, err
	}
	chain, err := chainBySlug(w.Chain)
	if err != nil {
		return nil, err
	}
	token, err := chain.token(req.Token)
	if err != nil {
		return nil, err
	}
	to, err := normalizeAddress(req.To)
	if err != nil {
		return nil, err
	}
	amount, err := parseUnits(req.Amount, token.Decimals)
	if err != nil {
		return nil, err
	}
	usdValue, err := a.prices.USDMicros(ctx, token.Symbol, amount, token.Decimals)
	if err != nil {
		return nil, fmt.Errorf("cannot value the transfer in USD, so it cannot be checked against the caps: %w", err)
	}

	l := a.walletLock(w.ID)
	l.Lock()
	defer l.Unlock()

	now := a.now()
	var p *Proposal
	var v verdict
	err = a.withTx(func(tx *sql.Tx) error {
		w, err := getWallet(tx, req.WalletID) // re-read under the lock
		if err != nil {
			return err
		}
		pol, err := currentPolicy(tx, w.ID)
		if err != nil {
			return err
		}
		rcpt, err := getRecipientByAddress(tx, w.ID, to)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		spent, err := spentSince(tx, w.ID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		pending, err := countPending(tx, w.ID)
		if err != nil {
			return err
		}
		v = evaluate(evalInput{
			Frozen: w.Frozen, Policy: pol, Token: token.Symbol, USDMicros: usdValue,
			Recipient: rcpt.state(), Spent24h: spent, Pending: pending, Now: now,
		})
		state, executedAt := "refused", ""
		switch v.Outcome {
		case "execute":
			state, executedAt = "sending", ts(now)
		case "needs_approval":
			state = "pending_approval"
		}
		res, err := tx.Exec(`INSERT INTO proposals (wallet_id, agent_id, caller_app, tool_call_id, token, to_address, amount_base,
			amount_display, usd_micros, memo, state, refusal_rule, refusal_detail, executed_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.ID, req.AgentID, req.CallerApp, req.ToolCallID, token.Symbol, to, amount.String(),
			formatUnits(amount, token.Decimals), usdValue, clip(req.Memo, 500), state, v.Rule, v.Detail, executedAt, ts(now), ts(now))
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if err := appendLedger(tx, w.ID, "transfer.proposed", req.actor(), id, map[string]any{
			"token": token.Symbol, "to": to, "amount": formatUnits(amount, token.Decimals), "usd_micros": usdValue,
			"outcome": v.Outcome, "rule": v.Rule, "detail": v.Detail, "memo": req.Memo,
		}, now); err != nil {
			return err
		}
		p, err = getProposal(tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	switch v.Outcome {
	case "execute":
		p = a.executeLocked(ctx, w, chain, p)
	case "needs_approval":
		a.announceTransferApproval(w, p)
	}
	return p, nil
}

// decideProposal records a person's decision. Approval re-runs every rule
// except the approval threshold, so a freeze or a cap reached in the
// meantime still stops the transfer.
func (a *App) decideProposal(ctx context.Context, id int64, approve bool, actor, via, note string, userID, agentID int64) (*Proposal, error) {
	p, err := getProposal(a.db, id)
	if err != nil {
		return nil, err
	}
	w, err := getWallet(a.db, p.WalletID)
	if err != nil {
		return nil, err
	}
	chain, err := chainBySlug(w.Chain)
	if err != nil {
		return nil, err
	}
	l := a.walletLock(w.ID)
	l.Lock()
	defer l.Unlock()

	now := a.now()
	execute := false
	err = a.withTx(func(tx *sql.Tx) error {
		cur, err := getProposal(tx, id)
		if err != nil {
			return err
		}
		if cur.State != "pending_approval" {
			return fmt.Errorf("proposal is already %s", cur.State)
		}
		if _, err := tx.Exec(`INSERT INTO approvals (subject, subject_id, decision, decided_by, decided_by_agent, via, note, created_at)
			VALUES ('proposal', ?, ?, ?, ?, ?, ?, ?)`, id, decisionWord(approve), userID, agentID, via, clip(note, 500), ts(now)); err != nil {
			return err
		}
		if !approve {
			if _, err := tx.Exec(`UPDATE proposals SET state = 'rejected', updated_at = ? WHERE id = ?`, ts(now), id); err != nil {
				return err
			}
			return appendLedger(tx, w.ID, "transfer.rejected", actor, id, map[string]any{"via": via, "note": note}, now)
		}
		w, err := getWallet(tx, cur.WalletID)
		if err != nil {
			return err
		}
		pol, err := currentPolicy(tx, w.ID)
		if err != nil {
			return err
		}
		rcpt, err := getRecipientByAddress(tx, w.ID, cur.ToAddress)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		spent, err := spentSince(tx, w.ID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		v := evaluate(evalInput{
			Frozen: w.Frozen, Policy: pol, Token: cur.Token, USDMicros: cur.USDMicros,
			Recipient: rcpt.state(), Spent24h: spent, Now: now, AlreadyApproved: true,
		})
		if v.Outcome != "execute" {
			if _, err := tx.Exec(`UPDATE proposals SET state = 'refused', refusal_rule = ?, refusal_detail = ?, updated_at = ? WHERE id = ?`,
				v.Rule, "approved, but "+v.Detail, ts(now), id); err != nil {
				return err
			}
			return appendLedger(tx, w.ID, "transfer.refused_after_approval", actor, id, map[string]any{"via": via, "rule": v.Rule, "detail": v.Detail}, now)
		}
		if _, err := tx.Exec(`UPDATE proposals SET state = 'sending', executed_at = ?, updated_at = ? WHERE id = ?`, ts(now), ts(now), id); err != nil {
			return err
		}
		execute = true
		return appendLedger(tx, w.ID, "transfer.approved", actor, id, map[string]any{"via": via, "note": note}, now)
	})
	if err != nil {
		return nil, err
	}
	p, err = getProposal(a.db, id)
	if err != nil {
		return nil, err
	}
	if execute {
		p = a.executeLocked(ctx, w, chain, p)
	}
	return p, nil
}

// executeLocked signs and broadcasts a proposal already in state "sending".
// The caller holds the wallet lock, so nonces never collide.
func (a *App) executeLocked(ctx context.Context, w *Wallet, chain *chainInfo, p *Proposal) *Proposal {
	hash, nonce, err := a.signAndSend(ctx, w, chain, p)
	now := a.now()
	var rpcErr *rpcCallError
	switch {
	case err == nil:
		_ = a.withTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE proposals SET state = 'sent', tx_hash = ?, nonce = ?, updated_at = ? WHERE id = ?`, hash, nonce, ts(now), p.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO transactions (wallet_id, proposal_id, direction, token, hash, from_address, to_address,
				amount_base, status, created_at) VALUES (?, ?, 'out', ?, ?, ?, ?, ?, 'pending', ?)`,
				w.ID, p.ID, p.Token, hash, w.Address, p.ToAddress, p.AmountBase, ts(now)); err != nil {
				return err
			}
			return appendLedger(tx, w.ID, "transfer.sent", "system", p.ID, map[string]any{"tx_hash": hash, "nonce": nonce}, now)
		})
		a.emit("crypto_wallet.transfer.sent", map[string]any{"wallet_id": w.ID, "proposal_id": p.ID, "tx_hash": hash, "token": p.Token, "amount": p.Amount, "to": p.ToAddress})
	case errors.As(err, &rpcErr) || errors.Is(err, errNotSent):
		// The node answered no (or we never got as far as broadcasting):
		// nothing left the wallet, so the amount stops counting against caps.
		_ = a.withTx(func(tx *sql.Tx) error {
			if _, err2 := tx.Exec(`UPDATE proposals SET state = 'failed', error = ?, executed_at = '', updated_at = ? WHERE id = ?`, err.Error(), ts(now), p.ID); err2 != nil {
				return err2
			}
			return appendLedger(tx, w.ID, "transfer.failed", "system", p.ID, map[string]any{"error": err.Error(), "sent": false}, now)
		})
	default:
		// Transport failure after the signed transaction may have reached
		// the node. Keep it counted and let the watcher look for a receipt.
		_ = a.withTx(func(tx *sql.Tx) error {
			if _, err2 := tx.Exec(`UPDATE proposals SET state = 'sent', tx_hash = ?, nonce = ?, error = ?, updated_at = ? WHERE id = ?`,
				hash, nonce, "broadcast result unknown: "+err.Error(), ts(now), p.ID); err2 != nil {
				return err2
			}
			return appendLedger(tx, w.ID, "transfer.broadcast_unknown", "system", p.ID, map[string]any{"tx_hash": hash, "error": err.Error()}, now)
		})
	}
	if out, err := getProposal(a.db, p.ID); err == nil {
		return out
	}
	return p
}

var errNotSent = errors.New("not sent")

func (a *App) signAndSend(ctx context.Context, w *Wallet, chain *chainInfo, p *Proposal) (hash string, nonce int64, err error) {
	notSent := func(e error) (string, int64, error) { return "", -1, fmt.Errorf("%w: %v", errNotSent, e) }
	token, err := chain.token(p.Token)
	if err != nil {
		return notSent(err)
	}
	from, err := parseAddress(w.Address)
	if err != nil {
		return notSent(err)
	}
	to, err := parseAddress(p.ToAddress)
	if err != nil {
		return notSent(err)
	}
	amount, ok := new(big.Int).SetString(p.AmountBase, 10)
	if !ok {
		return notSent(errors.New("invalid stored amount"))
	}
	tx := &dynamicFeeTx{ChainID: big.NewInt(chain.ChainID), Value: new(big.Int)}
	if token.Contract == "" {
		tx.To, tx.Value = to, amount
	} else {
		contract, _ := parseAddress(token.Contract)
		tx.To, tx.Data = contract, erc20TransferData(to, amount)
	}
	client := a.client(chain)
	if id, err := client.ChainID(ctx); err != nil {
		return notSent(err)
	} else if id.Int64() != chain.ChainID {
		return notSent(fmt.Errorf("RPC endpoint is on chain %s, expected %d", id, chain.ChainID))
	}
	n, err := client.PendingNonce(ctx, from)
	if err != nil {
		return notSent(err)
	}
	tx.Nonce = n
	if tx.MaxPriorityFeePerGas, tx.MaxFeePerGas, err = client.SuggestFees(ctx); err != nil {
		return notSent(err)
	}
	gas, err := client.EstimateGas(ctx, from, tx.To, tx.Value, tx.Data)
	if err != nil {
		return notSent(fmt.Errorf("gas estimate failed (is there enough ETH for gas and enough %s?): %v", token.Symbol, err))
	}
	tx.Gas = gas + gas/4
	sealed, err := loadSealedKey(a.db, w.ID)
	if err != nil {
		return notSent(err)
	}
	var raw []byte
	if err := a.keys.withKey(w.Address, sealed, func(priv *secp256k1.PrivateKey) error {
		var err error
		raw, hash, err = tx.sign(priv)
		return err
	}); err != nil {
		return notSent(err)
	}
	got, err := client.SendRawTransaction(ctx, raw)
	if err != nil {
		var rpcErr *rpcCallError
		if errors.As(err, &rpcErr) && strings.Contains(strings.ToLower(rpcErr.Message), "already known") {
			return hash, int64(n), nil
		}
		return hash, int64(n), err
	}
	if got != "" && !strings.EqualFold(got, hash) {
		return hash, int64(n), fmt.Errorf("node returned hash %s, expected %s", got, hash)
	}
	return hash, int64(n), nil
}

// ─── Balances ────────────────────────────────────────────────────────

type tokenBalance struct {
	Token     string `json:"token"`
	Amount    string `json:"amount"`
	Base      string `json:"base"`
	USDMicros *int64 `json:"usd_micros,omitempty"`
	USD       string `json:"usd,omitempty"`
}

func (a *App) balances(ctx context.Context, w *Wallet) ([]tokenBalance, error) {
	chain, err := chainBySlug(w.Chain)
	if err != nil {
		return nil, err
	}
	owner, err := parseAddress(w.Address)
	if err != nil {
		return nil, err
	}
	client := a.client(chain)
	out := []tokenBalance{}
	for _, sym := range []string{"ETH", "USDC"} {
		t := chain.Tokens[sym]
		var bal *big.Int
		if t.Contract == "" {
			bal, err = client.Balance(ctx, owner)
		} else {
			contract, _ := parseAddress(t.Contract)
			bal, err = client.TokenBalance(ctx, contract, owner)
		}
		if err != nil {
			return nil, err
		}
		b := tokenBalance{Token: sym, Amount: formatUnits(bal, t.Decimals), Base: bal.String()}
		if v, err := a.prices.USDMicros(ctx, sym, bal, t.Decimals); err == nil {
			b.USDMicros, b.USD = &v, usd(v)
		}
		out = append(out, b)
	}
	return out, nil
}
