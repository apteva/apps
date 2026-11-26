package main

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Incoming USDC is found with eth_getLogs over at most this many blocks per
// pass (Base makes a block every 2 seconds, so 1,500 blocks is 50 minutes).
const maxLogRange = 1500

// A transfer with no receipt after this long is marked failed. It keeps
// counting against the caps: a transaction that may still be mined must
// not free up room for another one.
const sentTimeout = 30 * time.Minute

func parseBigDec(s string) (*big.Int, bool) { return new(big.Int).SetString(s, 10) }

func (a *App) runWatcher(ctx context.Context, _ *sdk.AppCtx) error {
	wallets, err := listWallets(a.db)
	if err != nil {
		return err
	}
	for _, w := range wallets {
		if err := ctx.Err(); err != nil {
			return err
		}
		chain, err := chainBySlug(w.Chain)
		if err != nil {
			continue
		}
		client := a.client(chain)
		if err := a.checkSent(ctx, w, chain, client); err != nil {
			a.logWarn("receipt check failed", "wallet", w.ID, "err", err)
		}
		if err := a.scanIncoming(ctx, w, chain, client); err != nil {
			a.logWarn("incoming scan failed", "wallet", w.ID, "err", err)
		}
	}
	return nil
}

// checkSent moves sent proposals to confirmed or failed from their receipt.
func (a *App) checkSent(ctx context.Context, w *Wallet, chain *chainInfo, client chainClient) error {
	props, err := listProposals(a.db, w.ID, []string{"sent", "sending"}, 100)
	if err != nil || len(props) == 0 {
		return err
	}
	head, err := client.BlockNumber(ctx)
	if err != nil {
		return err
	}
	now := a.now()
	for _, p := range props {
		if p.State == "sending" {
			// Only possible if the process died mid-send: the lock is held
			// for the whole send otherwise.
			if now.Sub(parseTS(p.UpdatedAt)) > 5*time.Minute {
				a.finishProposal(w, p, "failed", "interrupted while sending; check the explorer before retrying", nil, now)
			}
			continue
		}
		if p.TxHash == "" {
			continue
		}
		rc, err := client.Receipt(ctx, p.TxHash)
		if err != nil {
			return err
		}
		if rc == nil {
			if now.Sub(parseTS(p.UpdatedAt)) > sentTimeout {
				a.finishProposal(w, p, "failed", "no receipt after 30 minutes; the transaction was dropped or is stuck", nil, now)
			}
			continue
		}
		if head+1 < rc.BlockNumber+uint64(chain.Confirmations) {
			continue // mined, waiting for confirmations
		}
		if rc.Status == 1 {
			a.finishProposal(w, p, "confirmed", "", rc, now)
		} else {
			a.finishProposal(w, p, "failed", "reverted on-chain", rc, now)
		}
	}
	return nil
}

func (a *App) finishProposal(w *Wallet, p *Proposal, state, errMsg string, rc *txReceipt, now time.Time) {
	fee := ""
	var block uint64
	if rc != nil {
		block = rc.BlockNumber
		if rc.GasUsed != nil && rc.EffGasPrice != nil {
			fee = new(big.Int).Mul(rc.GasUsed, rc.EffGasPrice).String()
		}
	}
	txStatus := "confirmed"
	if state == "failed" {
		txStatus = "failed"
	}
	err := a.withTx(func(tx *sql.Tx) error {
		cur, err := getProposal(tx, p.ID)
		if err != nil || cur.State != p.State {
			return errors.New("proposal changed")
		}
		if _, err := tx.Exec(`UPDATE proposals SET state = ?, error = CASE WHEN ? <> '' THEN ? ELSE error END, updated_at = ? WHERE id = ?`,
			state, errMsg, errMsg, ts(now), p.ID); err != nil {
			return err
		}
		if p.TxHash != "" {
			if _, err := tx.Exec(`UPDATE transactions SET status = ?, block_number = ?, fee_wei = ?, confirmed_at = ? WHERE hash = ? AND direction = 'out'`,
				txStatus, block, fee, ts(now), p.TxHash); err != nil {
				return err
			}
		}
		return appendLedger(tx, w.ID, "transfer."+state, "system", p.ID, map[string]any{"tx_hash": p.TxHash, "block": block, "fee_wei": fee, "error": errMsg}, now)
	})
	if err == nil {
		a.emit("crypto_wallet.transfer."+state, map[string]any{"wallet_id": w.ID, "proposal_id": p.ID, "tx_hash": p.TxHash, "token": p.Token, "amount": p.Amount, "to": p.ToAddress})
	}
}

// scanIncoming records USDC transfers into the wallet. Incoming ETH shows
// in the balance but is not itemized in v0.1 (that needs a full block scan
// or an indexer).
func (a *App) scanIncoming(ctx context.Context, w *Wallet, chain *chainInfo, client chainClient) error {
	usdc := chain.Tokens["USDC"]
	contract, err := parseAddress(usdc.Contract)
	if err != nil {
		return err
	}
	owner, err := parseAddress(w.Address)
	if err != nil {
		return err
	}
	head, err := client.BlockNumber(ctx)
	if err != nil {
		return err
	}
	var last uint64
	err = a.db.QueryRow(`SELECT last_block FROM watch_state WHERE wallet_id = ?`, w.ID).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		// First pass: start from now. A new wallet has no history worth
		// backfilling, and an old address would need an indexer anyway.
		_, err = a.db.Exec(`INSERT INTO watch_state (wallet_id, last_block) VALUES (?, ?)`, w.ID, head)
		return err
	}
	if err != nil || head <= last {
		return err
	}
	from, to := last+1, head
	if to-from+1 > maxLogRange {
		to = from + maxLogRange - 1
	}
	logs, err := client.TransferLogs(ctx, contract, owner, from, to)
	if err != nil {
		return err
	}
	now := a.now()
	var received []map[string]any
	err = a.withTx(func(tx *sql.Tx) error {
		received = nil
		for _, l := range logs {
			res, err := tx.Exec(`INSERT OR IGNORE INTO transactions (wallet_id, direction, token, hash, log_index, from_address, to_address,
				amount_base, block_number, status, created_at, confirmed_at) VALUES (?, 'in', 'USDC', ?, ?, ?, ?, ?, ?, 'confirmed', ?, ?)`,
				w.ID, l.TxHash, l.LogIndex, checksumAddress(l.From), w.Address, l.Amount.String(), l.BlockNumber, ts(now), ts(now))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				amount := formatUnits(l.Amount, usdc.Decimals)
				if err := appendLedger(tx, w.ID, "transfer.received", "system", 0, map[string]any{
					"token": "USDC", "amount": amount, "from": checksumAddress(l.From), "tx_hash": l.TxHash,
				}, now); err != nil {
					return err
				}
				received = append(received, map[string]any{"wallet_id": w.ID, "token": "USDC", "amount": amount, "from": checksumAddress(l.From), "tx_hash": l.TxHash})
			}
		}
		_, err := tx.Exec(`UPDATE watch_state SET last_block = ? WHERE wallet_id = ?`, to, w.ID)
		return err
	})
	if err != nil {
		return err
	}
	for _, ev := range received {
		a.emit("crypto_wallet.transfer.received", ev)
	}
	return nil
}
