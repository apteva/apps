package main

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func userActor(id int64) string  { return fmt.Sprintf("user:%d", id) }
func agentActor(id int64) string { return fmt.Sprintf("agent:%d", id) }

// ─── Wallet lifecycle (people only) ─────────────────────────────────

func (a *App) createWallet(projectID, name, chainSlug string, userID int64) (*Wallet, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name required")
	}
	if len(name) > 80 {
		return nil, errors.New("name is too long")
	}
	chain, err := chainBySlug(chainSlug)
	if err != nil {
		return nil, err
	}
	address, sealed, err := a.keys.newKey()
	if err != nil {
		return nil, err
	}
	now := a.now()
	var w *Wallet
	err = a.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO wallets (project_id, name, chain, address, provider, created_by, created_at)
			VALUES (?, ?, ?, ?, 'builtin', ?, ?)`, projectID, name, chain.Slug, address, userID, ts(now))
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO wallet_keys (wallet_id, key_nonce, key_cipher, dek_nonce, dek_cipher, master_fp, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sealed.KeyNonce, sealed.KeyCipher, sealed.DEKNonce, sealed.DEKCipher, sealed.MasterFP, ts(now)); err != nil {
			return err
		}
		if _, err := insertPolicy(tx, id, defaultPolicy(), userID, now); err != nil {
			return err
		}
		if err := appendLedger(tx, id, "wallet.created", userActor(userID), 0, map[string]any{
			"name": name, "chain": chain.Slug, "address": address, "provider": "builtin",
		}, now); err != nil {
			return err
		}
		w, err = getWallet(tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	a.emit("crypto_wallet.created", map[string]any{"wallet_id": w.ID, "chain": w.Chain, "address": w.Address})
	return w, nil
}

func (a *App) setPolicy(walletID int64, p Policy, userID int64) (Policy, error) {
	l := a.walletLock(walletID)
	l.Lock()
	defer l.Unlock()
	w, err := getWallet(a.db, walletID)
	if err != nil {
		return p, err
	}
	chain, err := chainBySlug(w.Chain)
	if err != nil {
		return p, err
	}
	for i, t := range p.Tokens {
		p.Tokens[i] = strings.ToUpper(strings.TrimSpace(t))
	}
	if err := p.validate(chain); err != nil {
		return p, err
	}
	now := a.now()
	err = a.withTx(func(tx *sql.Tx) error {
		prev, err := currentPolicy(tx, walletID)
		if err != nil {
			return err
		}
		if p, err = insertPolicy(tx, walletID, p, userID, now); err != nil {
			return err
		}
		return appendLedger(tx, walletID, "policy.changed", userActor(userID), 0, map[string]any{"from": prev, "to": p}, now)
	})
	return p, err
}

func (a *App) setFrozen(walletID int64, frozen bool, reason, actor string) (*Wallet, error) {
	l := a.walletLock(walletID)
	l.Lock()
	defer l.Unlock()
	now := a.now()
	var w *Wallet
	err := a.withTx(func(tx *sql.Tx) error {
		if _, err := getWallet(tx, walletID); err != nil {
			return err
		}
		if !frozen {
			reason = ""
		}
		if _, err := tx.Exec(`UPDATE wallets SET frozen = ?, frozen_reason = ? WHERE id = ?`, boolInt(frozen), reason, walletID); err != nil {
			return err
		}
		kind := "wallet.unfrozen"
		if frozen {
			kind = "wallet.frozen"
		}
		if err := appendLedger(tx, walletID, kind, actor, 0, map[string]any{"reason": reason}, now); err != nil {
			return err
		}
		var err error
		w, err = getWallet(tx, walletID)
		return err
	})
	return w, err
}

// exportKey returns the raw private key. Panel only, and only when the
// person types the wallet's address back as confirmation.
func (a *App) exportKey(walletID int64, confirm string, userID int64) (string, error) {
	w, err := getWallet(a.db, walletID)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(confirm), w.Address) {
		return "", errors.New("type the wallet's full address to confirm the export")
	}
	sealed, err := loadSealedKey(a.db, walletID)
	if err != nil {
		return "", err
	}
	var out string
	if err := a.keys.withKey(w.Address, sealed, func(priv *secp256k1.PrivateKey) error {
		b := priv.Serialize()
		out = "0x" + hex.EncodeToString(b)
		wipe(b)
		return nil
	}); err != nil {
		return "", err
	}
	if err := appendLedger(a.db, walletID, "key.exported", userActor(userID), 0, nil, a.now()); err != nil {
		return "", err
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ─── Allowlist ───────────────────────────────────────────────────────

// addRecipient is the panel path: a person adding an address approves it
// directly, with no cooling-off period.
func (a *App) addRecipient(walletID int64, address, label string, userID int64) (*Recipient, error) {
	addr, err := normalizeAddress(address)
	if err != nil {
		return nil, err
	}
	now := a.now()
	var r *Recipient
	err = a.withTx(func(tx *sql.Tx) error {
		w, err := getWallet(tx, walletID)
		if err != nil {
			return err
		}
		if strings.EqualFold(w.Address, addr) {
			return errors.New("a wallet cannot send to itself")
		}
		existing, err := getRecipientByAddress(tx, walletID, addr)
		switch {
		case err == nil:
			if _, err := tx.Exec(`UPDATE allowlist SET label = ?, status = 'approved', added_by_user = ?, decided_by = ?,
				decided_at = ?, usable_after = ? WHERE id = ?`, strings.TrimSpace(label), userID, userID, ts(now), ts(now), existing.ID); err != nil {
				return err
			}
		case errors.Is(err, errNotFound):
			if _, err := tx.Exec(`INSERT INTO allowlist (wallet_id, address, label, status, added_by_user, decided_by, decided_at, usable_after, created_at)
				VALUES (?, ?, ?, 'approved', ?, ?, ?, ?, ?)`, walletID, addr, strings.TrimSpace(label), userID, userID, ts(now), ts(now), ts(now)); err != nil {
				return err
			}
		default:
			return err
		}
		if err := appendLedger(tx, walletID, "recipient.added", userActor(userID), 0, map[string]any{"address": addr, "label": label}, now); err != nil {
			return err
		}
		r, err = getRecipientByAddress(tx, walletID, addr)
		return err
	})
	return r, err
}

// proposeRecipient is the agent path: the address waits for a person, and
// once approved it can only receive funds after the policy's delay.
func (a *App) proposeRecipient(walletID int64, address, label, reason string, agentID int64) (*Recipient, bool, error) {
	addr, err := normalizeAddress(address)
	if err != nil {
		return nil, false, err
	}
	now := a.now()
	var r *Recipient
	created := false
	err = a.withTx(func(tx *sql.Tx) error {
		w, err := getWallet(tx, walletID)
		if err != nil {
			return err
		}
		if strings.EqualFold(w.Address, addr) {
			return errors.New("a wallet cannot send to itself")
		}
		existing, err := getRecipientByAddress(tx, walletID, addr)
		if err == nil && existing.Status != "removed" {
			r = existing // already known: report its state instead of re-asking
			return nil
		}
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		label, reason = clip(label, 80), clip(reason, 500)
		if existing != nil {
			_, err = tx.Exec(`UPDATE allowlist SET label = ?, reason = ?, status = 'pending', proposed_by_agent = ?, added_by_user = 0,
				decided_by = 0, decided_at = '', usable_after = '', approval_message_id = 0 WHERE id = ?`, label, reason, agentID, existing.ID)
		} else {
			_, err = tx.Exec(`INSERT INTO allowlist (wallet_id, address, label, reason, status, proposed_by_agent, created_at)
				VALUES (?, ?, ?, ?, 'pending', ?, ?)`, walletID, addr, label, reason, agentID, ts(now))
		}
		if err != nil {
			return err
		}
		created = true
		if err := appendLedger(tx, walletID, "recipient.proposed", agentActor(agentID), 0, map[string]any{"address": addr, "label": label, "reason": reason}, now); err != nil {
			return err
		}
		r, err = getRecipientByAddress(tx, walletID, addr)
		return err
	})
	if err != nil || !created {
		return r, false, err
	}
	a.announceRecipientApproval(r)
	return r, true, nil
}

func (a *App) decideRecipient(id int64, approve bool, actor, via, note string, userID, agentID int64) (*Recipient, error) {
	now := a.now()
	var r *Recipient
	err := a.withTx(func(tx *sql.Tx) error {
		cur, err := getRecipient(tx, id)
		if err != nil {
			return err
		}
		if cur.Status != "pending" {
			return fmt.Errorf("recipient is already %s", cur.Status)
		}
		status, usable := "rejected", ""
		if approve {
			p, err := currentPolicy(tx, cur.WalletID)
			if err != nil {
				return err
			}
			status, usable = "approved", ts(now.Add(time.Duration(p.NewRecipientDelayHours)*time.Hour))
		}
		if _, err := tx.Exec(`UPDATE allowlist SET status = ?, decided_by = ?, decided_by_agent = ?, decided_at = ?, usable_after = ? WHERE id = ?`,
			status, userID, agentID, ts(now), usable, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO approvals (subject, subject_id, decision, decided_by, decided_by_agent, via, note, created_at)
			VALUES ('recipient', ?, ?, ?, ?, ?, ?, ?)`, id, decisionWord(approve), userID, agentID, via, clip(note, 500), ts(now)); err != nil {
			return err
		}
		if err := appendLedger(tx, cur.WalletID, "recipient."+status, actor, 0, map[string]any{"address": cur.Address, "via": via, "usable_after": usable, "note": note}, now); err != nil {
			return err
		}
		r, err = getRecipient(tx, id)
		return err
	})
	return r, err
}

func (a *App) removeRecipient(id int64, userID int64) error {
	now := a.now()
	return a.withTx(func(tx *sql.Tx) error {
		cur, err := getRecipient(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE allowlist SET status = 'removed', decided_by = ?, decided_at = ? WHERE id = ?`, userID, ts(now), id); err != nil {
			return err
		}
		return appendLedger(tx, cur.WalletID, "recipient.removed", userActor(userID), 0, map[string]any{"address": cur.Address}, now)
	})
}

func decisionWord(approve bool) string {
	if approve {
		return "approve"
	}
	return "reject"
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
