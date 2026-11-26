package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fixed-width UTC timestamps so string comparison in SQL is time order.
const tsLayout = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsLayout, s)
	return t
}

var errNotFound = errors.New("not found")

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// ─── Wallets ─────────────────────────────────────────────────────────

type Wallet struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Chain        string `json:"chain"`
	Address      string `json:"address"`
	Provider     string `json:"provider"`
	Frozen       bool   `json:"frozen"`
	FrozenReason string `json:"frozen_reason,omitempty"`
	CreatedBy    int64  `json:"created_by"`
	CreatedAt    string `json:"created_at"`
}

const walletCols = `id, project_id, name, chain, address, provider, frozen, frozen_reason, created_by, created_at`

func scanWallet(row interface{ Scan(...any) error }) (*Wallet, error) {
	var w Wallet
	var frozen int
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Chain, &w.Address, &w.Provider, &frozen, &w.FrozenReason, &w.CreatedBy, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	w.Frozen = frozen != 0
	return &w, err
}

func getWallet(q queryer, id int64) (*Wallet, error) {
	w, err := scanWallet(q.QueryRow(`SELECT `+walletCols+` FROM wallets WHERE id = ?`, id))
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("wallet %d not found", id)
	}
	return w, err
}

func listWallets(q queryer) ([]*Wallet, error) {
	rows, err := q.Query(`SELECT ` + walletCols + ` FROM wallets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Wallet
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func loadSealedKey(q queryer, walletID int64) (*sealedKey, error) {
	var s sealedKey
	err := q.QueryRow(`SELECT key_nonce, key_cipher, dek_nonce, dek_cipher, master_fp FROM wallet_keys WHERE wallet_id = ?`, walletID).
		Scan(&s.KeyNonce, &s.KeyCipher, &s.DEKNonce, &s.DEKCipher, &s.MasterFP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("wallet has no stored key")
	}
	return &s, err
}

// ─── Policies ────────────────────────────────────────────────────────

func currentPolicy(q queryer, walletID int64) (Policy, error) {
	var p Policy
	var tokens string
	err := q.QueryRow(`SELECT version, per_tx_usd_micros, daily_usd_micros, approval_usd_micros, tokens_json,
		new_recipient_delay_hours, updated_by, created_at FROM policies WHERE wallet_id = ? ORDER BY version DESC LIMIT 1`, walletID).
		Scan(&p.Version, &p.PerTxUSDMicros, &p.DailyUSDMicros, &p.ApprovalUSDMicros, &tokens, &p.NewRecipientDelayHours, &p.UpdatedBy, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("wallet %d has no policy", walletID)
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(tokens), &p.Tokens); err != nil {
		return p, err
	}
	return p, nil
}

func insertPolicy(q queryer, walletID int64, p Policy, by int64, now time.Time) (Policy, error) {
	var version int
	if err := q.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM policies WHERE wallet_id = ?`, walletID).Scan(&version); err != nil {
		return p, err
	}
	p.Version, p.UpdatedBy, p.CreatedAt = version, by, ts(now)
	_, err := q.Exec(`INSERT INTO policies (wallet_id, version, per_tx_usd_micros, daily_usd_micros, approval_usd_micros,
		tokens_json, new_recipient_delay_hours, updated_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		walletID, version, p.PerTxUSDMicros, p.DailyUSDMicros, p.ApprovalUSDMicros, p.tokensJSON(), p.NewRecipientDelayHours, by, p.CreatedAt)
	return p, err
}

// ─── Allowlist ───────────────────────────────────────────────────────

type Recipient struct {
	ID                int64  `json:"id"`
	WalletID          int64  `json:"wallet_id"`
	Address           string `json:"address"`
	Label             string `json:"label"`
	Reason            string `json:"reason,omitempty"`
	Status            string `json:"status"`
	ProposedByAgent   int64  `json:"proposed_by_agent,omitempty"`
	AddedByUser       int64  `json:"added_by_user,omitempty"`
	DecidedBy         int64  `json:"decided_by,omitempty"`
	DecidedAt         string `json:"decided_at,omitempty"`
	UsableAfter       string `json:"usable_after,omitempty"`
	ApprovalMessageID int64  `json:"approval_message_id,omitempty"`
	CreatedAt         string `json:"created_at"`
}

const recipientCols = `id, wallet_id, address, label, reason, status, proposed_by_agent, added_by_user, decided_by, decided_at, usable_after, approval_message_id, created_at`

func scanRecipient(row interface{ Scan(...any) error }) (*Recipient, error) {
	var r Recipient
	err := row.Scan(&r.ID, &r.WalletID, &r.Address, &r.Label, &r.Reason, &r.Status, &r.ProposedByAgent, &r.AddedByUser,
		&r.DecidedBy, &r.DecidedAt, &r.UsableAfter, &r.ApprovalMessageID, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &r, err
}

func getRecipientByAddress(q queryer, walletID int64, address string) (*Recipient, error) {
	return scanRecipient(q.QueryRow(`SELECT `+recipientCols+` FROM allowlist WHERE wallet_id = ? AND address = ?`, walletID, address))
}

func getRecipient(q queryer, id int64) (*Recipient, error) {
	r, err := scanRecipient(q.QueryRow(`SELECT `+recipientCols+` FROM allowlist WHERE id = ?`, id))
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("recipient %d not found", id)
	}
	return r, err
}

func listRecipients(q queryer, walletID int64) ([]*Recipient, error) {
	rows, err := q.Query(`SELECT `+recipientCols+` FROM allowlist WHERE wallet_id = ? AND status <> 'removed' ORDER BY id DESC`, walletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Recipient{}
	for rows.Next() {
		r, err := scanRecipient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r *Recipient) state() recipientState {
	if r == nil {
		return recipientState{}
	}
	return recipientState{Found: true, Status: r.Status, UsableAfter: parseTS(r.UsableAfter), Label: r.Label}
}

// ─── Proposals ───────────────────────────────────────────────────────

type Proposal struct {
	ID                int64  `json:"id"`
	WalletID          int64  `json:"wallet_id"`
	AgentID           int64  `json:"agent_id,omitempty"`
	CallerApp         string `json:"caller_app,omitempty"`
	Token             string `json:"token"`
	ToAddress         string `json:"to_address"`
	AmountBase        string `json:"amount_base"`
	Amount            string `json:"amount"`
	USDMicros         int64  `json:"usd_micros"`
	Memo              string `json:"memo,omitempty"`
	State             string `json:"state"`
	RefusalRule       string `json:"refusal_rule,omitempty"`
	RefusalDetail     string `json:"refusal_detail,omitempty"`
	ApprovalMessageID int64  `json:"approval_message_id,omitempty"`
	TxHash            string `json:"tx_hash,omitempty"`
	Nonce             int64  `json:"nonce"`
	Error             string `json:"error,omitempty"`
	ExecutedAt        string `json:"executed_at,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

const proposalCols = `id, wallet_id, agent_id, caller_app, token, to_address, amount_base, amount_display, usd_micros, memo, state,
	refusal_rule, refusal_detail, approval_message_id, tx_hash, nonce, error, executed_at, created_at, updated_at`

func scanProposal(row interface{ Scan(...any) error }) (*Proposal, error) {
	var p Proposal
	err := row.Scan(&p.ID, &p.WalletID, &p.AgentID, &p.CallerApp, &p.Token, &p.ToAddress, &p.AmountBase, &p.Amount, &p.USDMicros,
		&p.Memo, &p.State, &p.RefusalRule, &p.RefusalDetail, &p.ApprovalMessageID, &p.TxHash, &p.Nonce, &p.Error, &p.ExecutedAt,
		&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &p, err
}

func getProposal(q queryer, id int64) (*Proposal, error) {
	p, err := scanProposal(q.QueryRow(`SELECT `+proposalCols+` FROM proposals WHERE id = ?`, id))
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("proposal %d not found", id)
	}
	return p, err
}

func listProposals(q queryer, walletID int64, states []string, limit int) ([]*Proposal, error) {
	query := `SELECT ` + proposalCols + ` FROM proposals WHERE wallet_id = ?`
	args := []any{walletID}
	if len(states) > 0 {
		query += ` AND state IN (?` + strings.Repeat(",?", len(states)-1) + `)`
		for _, s := range states {
			args = append(args, s)
		}
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// spentSince sums the USD value of transfers that started executing after
// since and did not fail before reaching the chain. Read from the proposal
// rows themselves, never a cached counter, so racing proposals can't both
// see room under the cap (callers also hold the wallet lock).
func spentSince(q queryer, walletID int64, since time.Time) (int64, error) {
	var n int64
	err := q.QueryRow(`SELECT COALESCE(SUM(usd_micros), 0) FROM proposals
		WHERE wallet_id = ? AND executed_at <> '' AND executed_at >= ?`, walletID, ts(since)).Scan(&n)
	return n, err
}

func countPending(q queryer, walletID int64) (int, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM proposals WHERE wallet_id = ? AND state = 'pending_approval'`, walletID).Scan(&n)
	return n, err
}

// ─── Transactions ────────────────────────────────────────────────────

type ChainTx struct {
	ID          int64  `json:"id"`
	WalletID    int64  `json:"wallet_id"`
	ProposalID  int64  `json:"proposal_id,omitempty"`
	Direction   string `json:"direction"`
	Token       string `json:"token"`
	Hash        string `json:"hash"`
	From        string `json:"from"`
	To          string `json:"to"`
	AmountBase  string `json:"amount_base"`
	Amount      string `json:"amount"`
	BlockNumber int64  `json:"block_number,omitempty"`
	Status      string `json:"status"`
	FeeWei      string `json:"fee_wei,omitempty"`
	CreatedAt   string `json:"created_at"`
	ConfirmedAt string `json:"confirmed_at,omitempty"`
}

func listTransactions(q queryer, walletID int64, limit int) ([]*ChainTx, error) {
	rows, err := q.Query(`SELECT id, wallet_id, proposal_id, direction, token, hash, from_address, to_address, amount_base,
		block_number, status, fee_wei, created_at, confirmed_at FROM transactions WHERE wallet_id = ? ORDER BY id DESC LIMIT ?`, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ChainTx{}
	for rows.Next() {
		var t ChainTx
		if err := rows.Scan(&t.ID, &t.WalletID, &t.ProposalID, &t.Direction, &t.Token, &t.Hash, &t.From, &t.To, &t.AmountBase,
			&t.BlockNumber, &t.Status, &t.FeeWei, &t.CreatedAt, &t.ConfirmedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ─── Ledger ──────────────────────────────────────────────────────────

type LedgerEntry struct {
	ID         int64          `json:"id"`
	WalletID   int64          `json:"wallet_id"`
	Kind       string         `json:"kind"`
	Actor      string         `json:"actor"`
	ProposalID int64          `json:"proposal_id,omitempty"`
	Data       map[string]any `json:"data"`
	CreatedAt  string         `json:"created_at"`
}

func appendLedger(q queryer, walletID int64, kind, actor string, proposalID int64, data map[string]any, now time.Time) error {
	if data == nil {
		data = map[string]any{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO ledger (wallet_id, kind, actor, proposal_id, data_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		walletID, kind, actor, proposalID, string(b), ts(now))
	return err
}

func listLedger(q queryer, walletID int64, limit int) ([]*LedgerEntry, error) {
	rows, err := q.Query(`SELECT id, wallet_id, kind, actor, proposal_id, data_json, created_at FROM ledger
		WHERE wallet_id = ? ORDER BY id DESC LIMIT ?`, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LedgerEntry{}
	for rows.Next() {
		var e LedgerEntry
		var raw string
		if err := rows.Scan(&e.ID, &e.WalletID, &e.Kind, &e.Actor, &e.ProposalID, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &e.Data)
		out = append(out, &e)
	}
	return out, rows.Err()
}
