package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Ledger rules (see migrations/001_init.sql):
//   - every change is one transaction that writes entries and updates the
//     wallet's stored available/held in the same transaction;
//   - lot.remaining counts held money; lot.held is the reserved part of it;
//   - available + held = SUM(lot.remaining), held = SUM(lot.held);
//   - nothing ever goes negative: a shortfall is recorded instead.

const tsLayout = "2006-01-02T15:04:05Z"

var (
	nowFunc  = time.Now
	writeMu  sync.Mutex // the SDK's writer pool is one connection; this also covers tests
	runAsync = func(f func()) { go f() }

	errNotFound = errors.New("not found")
)

func fmtTS(t time.Time) string { return t.UTC().Format(tsLayout) }

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

type rowScanner interface{ Scan(dest ...any) error }

type pendingEvent struct {
	Topic     string
	ProjectID string
	Data      map[string]any
}

type txState struct {
	tx         *sql.Tx
	now        time.Time
	nowS       string
	events     []pendingEvent
	touched    map[int64]*Wallet
	autoReload []int64
}

func (s *txState) emit(pid, topic string, data map[string]any) {
	s.events = append(s.events, pendingEvent{Topic: topic, ProjectID: pid, Data: data})
}

func (s *txState) touch(w *Wallet) { s.touched[w.ID] = w }

type Ledger struct{ db *sql.DB }

func newLedger(db *sql.DB) *Ledger { return &Ledger{db: db} }

// write runs fn in one transaction. Balance-state transitions (low / empty /
// recovered) are evaluated once per touched wallet just before commit.
func (l *Ledger) write(fn func(s *txState) error) (*txState, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	tx, err := l.db.Begin()
	if err != nil {
		return nil, err
	}
	now := nowFunc().UTC().Truncate(time.Second)
	s := &txState{tx: tx, now: now, nowS: fmtTS(now), touched: map[int64]*Wallet{}}
	if err := fn(s); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	for _, w := range s.touched {
		if err := s.checkLow(w); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

// ─── Wallets ─────────────────────────────────────────────────────────

type Wallet struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id"`
	SubjectType       string          `json:"subject_type"`
	SubjectID         string          `json:"subject_id"`
	Unit              string          `json:"unit"`
	Available         int64           `json:"available"`
	Held              int64           `json:"held"`
	Status            string          `json:"status"`
	BillingCustomerID int64           `json:"billing_customer_id,omitempty"`
	LowThreshold      int64           `json:"low_threshold"`
	LowState          string          `json:"low_state"`
	Metadata          json.RawMessage `json:"metadata"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

const walletCols = `id, project_id, subject_type, subject_id, unit, available, held, status, billing_customer_id, low_threshold, low_state, metadata, created_at, updated_at`

func scanWallet(r rowScanner) (*Wallet, error) {
	var w Wallet
	var meta string
	if err := r.Scan(&w.ID, &w.ProjectID, &w.SubjectType, &w.SubjectID, &w.Unit, &w.Available, &w.Held, &w.Status, &w.BillingCustomerID, &w.LowThreshold, &w.LowState, &meta, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	w.Metadata = json.RawMessage(meta)
	return &w, nil
}

// WalletRef names a wallet by id, or by (subject_type, subject_id, unit).
type WalletRef struct {
	ID          int64
	SubjectType string
	SubjectID   string
	Unit        string
}

func (r WalletRef) valid() error {
	if r.ID > 0 {
		return nil
	}
	if r.SubjectID == "" || r.Unit == "" {
		return errors.New("wallet_id, or subject_id and unit, required")
	}
	return nil
}

// findWallet returns (nil, nil) when a subject wallet does not exist yet, and
// errNotFound when an explicit wallet_id is unknown.
func findWallet(q querier, pid string, ref WalletRef) (*Wallet, error) {
	if err := ref.valid(); err != nil {
		return nil, err
	}
	var row *sql.Row
	if ref.ID > 0 {
		row = q.QueryRow(`SELECT `+walletCols+` FROM credit_wallets WHERE id=? AND project_id=?`, ref.ID, pid)
	} else {
		row = q.QueryRow(`SELECT `+walletCols+` FROM credit_wallets WHERE project_id=? AND subject_type=? AND subject_id=? AND unit=?`, pid, ref.SubjectType, ref.SubjectID, ref.Unit)
	}
	w, err := scanWallet(row)
	if errors.Is(err, sql.ErrNoRows) {
		if ref.ID > 0 {
			return nil, fmt.Errorf("wallet %d: %w", ref.ID, errNotFound)
		}
		return nil, nil
	}
	return w, err
}

func mustWallet(q querier, pid string, ref WalletRef) (*Wallet, error) {
	w, err := findWallet(q, pid, ref)
	if err == nil && w == nil {
		return nil, fmt.Errorf("wallet %s/%s/%s: %w", ref.SubjectType, ref.SubjectID, ref.Unit, errNotFound)
	}
	return w, err
}

type walletOpts struct {
	LowThreshold      *int64
	BillingCustomerID int64
	Metadata          any
}

func (s *txState) ensureWallet(pid string, ref WalletRef, opts walletOpts) (*Wallet, bool, error) {
	w, err := findWallet(s.tx, pid, ref)
	if err != nil || w != nil {
		return w, false, err
	}
	if !validUnit(ref.Unit) {
		return nil, false, fmt.Errorf("invalid unit %q: use a short lowercase name such as usd, minutes, points", ref.Unit)
	}
	low := int64(0)
	if opts.LowThreshold != nil {
		low = *opts.LowThreshold
	}
	_, err = s.tx.Exec(`INSERT INTO credit_wallets (project_id, subject_type, subject_id, unit, billing_customer_id, low_threshold, low_state, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'empty', ?, ?, ?)`,
		pid, ref.SubjectType, ref.SubjectID, ref.Unit, opts.BillingCustomerID, low, jsonOrEmpty(opts.Metadata, "{}"), s.nowS, s.nowS)
	if err != nil {
		return nil, false, err
	}
	w, err = findWallet(s.tx, pid, ref)
	return w, true, err
}

func validUnit(u string) bool {
	if u == "" || len(u) > 32 {
		return false
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (s *txState) applyBalance(w *Wallet, dA, dH int64) error {
	if w.Available+dA < 0 || w.Held+dH < 0 {
		return fmt.Errorf("credits invariant: wallet %d balance would go negative (available %d%+d, held %d%+d)", w.ID, w.Available, dA, w.Held, dH)
	}
	w.Available += dA
	w.Held += dH
	w.UpdatedAt = s.nowS
	_, err := s.tx.Exec(`UPDATE credit_wallets SET available=?, held=?, updated_at=? WHERE id=?`, w.Available, w.Held, s.nowS, w.ID)
	s.touch(w)
	return err
}

func balanceState(w *Wallet) string {
	switch {
	case w.Available <= 0:
		return "empty"
	case w.LowThreshold > 0 && w.Available < w.LowThreshold:
		return "low"
	}
	return "ok"
}

func stateRank(s string) int {
	switch s {
	case "empty":
		return 0
	case "low":
		return 1
	}
	return 2
}

// checkLow emits only on state changes: low / empty when the balance gets
// worse, recovered when it is back at or above the threshold.
func (s *txState) checkLow(w *Wallet) error {
	next := balanceState(w)
	if next == w.LowState {
		return nil
	}
	prev := w.LowState
	w.LowState = next
	if _, err := s.tx.Exec(`UPDATE credit_wallets SET low_state=? WHERE id=?`, next, w.ID); err != nil {
		return err
	}
	data := walletEventData(w)
	switch {
	case next == "ok":
		s.emit(w.ProjectID, "credits.balance.recovered", data)
	case stateRank(next) < stateRank(prev):
		s.emit(w.ProjectID, "credits.balance."+next, data)
		s.autoReload = append(s.autoReload, w.ID)
	}
	return nil
}

func walletEventData(w *Wallet) map[string]any {
	return map[string]any{
		"wallet_id": w.ID, "subject_type": w.SubjectType, "subject_id": w.SubjectID, "unit": w.Unit,
		"available": w.Available, "held": w.Held, "low_threshold": w.LowThreshold,
	}
}

// ─── Entries ─────────────────────────────────────────────────────────

type Entry struct {
	ID             int64           `json:"id"`
	WalletID       int64           `json:"wallet_id"`
	Kind           string          `json:"kind"`
	Amount         int64           `json:"amount"`
	DeltaAvailable int64           `json:"delta_available"`
	DeltaHeld      int64           `json:"delta_held"`
	AvailableAfter int64           `json:"available_after"`
	HeldAfter      int64           `json:"held_after"`
	LotID          int64           `json:"lot_id,omitempty"`
	HoldID         int64           `json:"hold_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Actor          string          `json:"actor,omitempty"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      string          `json:"created_at"`
}

const entryCols = `id, wallet_id, kind, amount, delta_available, delta_held, available_after, held_after, COALESCE(lot_id,0), COALESCE(hold_id,0), idempotency_key, actor, metadata, created_at`

func scanEntry(r rowScanner) (*Entry, error) {
	var e Entry
	var meta string
	if err := r.Scan(&e.ID, &e.WalletID, &e.Kind, &e.Amount, &e.DeltaAvailable, &e.DeltaHeld, &e.AvailableAfter, &e.HeldAfter, &e.LotID, &e.HoldID, &e.IdempotencyKey, &e.Actor, &meta, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Metadata = json.RawMessage(meta)
	return &e, nil
}

type entryIn struct {
	kind           string
	amount, dA, dH int64
	lotID, holdID  int64
	idem, actor    string
	meta           any
}

func (s *txState) entry(w *Wallet, e entryIn) (int64, error) {
	if err := s.applyBalance(w, e.dA, e.dH); err != nil {
		return 0, err
	}
	res, err := s.tx.Exec(`INSERT INTO credit_entries
		(wallet_id, kind, amount, delta_available, delta_held, available_after, held_after, lot_id, hold_id, idempotency_key, actor, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, e.kind, e.amount, e.dA, e.dH, w.Available, w.Held, nullID(e.lotID), nullID(e.holdID), e.idem, e.actor, jsonOrEmpty(e.meta, "{}"), s.nowS)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *txState) entryByIdem(walletID int64, key string) (*Entry, error) {
	if key == "" {
		return nil, nil
	}
	e, err := scanEntry(s.tx.QueryRow(`SELECT `+entryCols+` FROM credit_entries WHERE wallet_id=? AND idempotency_key=?`, walletID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// lotSpend lowers a lot's remaining (and optionally its held part), records
// the move for the liability report, and settles the lot's status.
func (s *txState) lotSpend(entryID, lotID, take, unhold int64) error {
	if take == 0 && unhold == 0 {
		return nil
	}
	if _, err := s.tx.Exec(`UPDATE credit_lots SET remaining = remaining - ?, held = held - ? WHERE id=?`, take, unhold, lotID); err != nil {
		return err
	}
	if take != 0 {
		if _, err := s.tx.Exec(`INSERT INTO credit_lot_moves (entry_id, lot_id, delta, created_at) VALUES (?, ?, ?, ?)`, entryID, lotID, -take, s.nowS); err != nil {
			return err
		}
	}
	_, err := s.tx.Exec(`UPDATE credit_lots SET status = CASE
			WHEN expires_at IS NOT NULL AND expires_at <= ? AND remaining = held AND held = 0 THEN 'expired'
			ELSE 'depleted' END
		WHERE id=? AND status='active' AND remaining = 0`, s.nowS, lotID)
	return err
}

// ─── Lots ────────────────────────────────────────────────────────────

type Lot struct {
	ID         int64           `json:"id"`
	WalletID   int64           `json:"wallet_id"`
	Source     string          `json:"source"`
	SourceRef  string          `json:"source_ref,omitempty"`
	Original   int64           `json:"original"`
	Remaining  int64           `json:"remaining"`
	Held       int64           `json:"held"`
	Refunded   int64           `json:"refunded,omitempty"`
	Priority   int64           `json:"priority"`
	ExpiresAt  string          `json:"expires_at,omitempty"`
	Refundable bool            `json:"refundable"`
	Paid       bool            `json:"paid"`
	Status     string          `json:"status"`
	Metadata   json.RawMessage `json:"metadata"`
	CreatedAt  string          `json:"created_at"`
}

const lotCols = `id, wallet_id, source, source_ref, original, remaining, held, refunded, priority, expires_at, refundable, paid, status, metadata, created_at`

func scanLot(r rowScanner) (*Lot, error) {
	var l Lot
	var exp sql.NullString
	var refundable, paid int
	var meta string
	if err := r.Scan(&l.ID, &l.WalletID, &l.Source, &l.SourceRef, &l.Original, &l.Remaining, &l.Held, &l.Refunded, &l.Priority, &exp, &refundable, &paid, &l.Status, &meta, &l.CreatedAt); err != nil {
		return nil, err
	}
	l.ExpiresAt = exp.String
	l.Refundable = refundable == 1
	l.Paid = paid == 1
	l.Metadata = json.RawMessage(meta)
	return &l, nil
}

var validSources = map[string]bool{"purchase": true, "promo": true, "refund": true, "reward": true, "adjustment": true, "transfer": true}

const (
	defaultPriority  = 100
	promoPriority    = 10
	purchasePriority = 100
)

type grantIn struct {
	Source     string
	SourceRef  string
	Amount     int64
	Priority   *int64
	ExpiresAt  string
	Refundable bool
	Paid       bool
	Metadata   any
	// ledger side
	Kind  string // credit (default) | adjust | transfer
	Idem  string
	Actor string
	Note  any
}

// grantLot creates a lot and its credit entry. Idempotent by source_ref.
func (s *txState) grantLot(w *Wallet, g grantIn) (*Lot, bool, error) {
	if !validSources[g.Source] {
		return nil, false, fmt.Errorf("invalid source %q", g.Source)
	}
	if g.Amount <= 0 {
		return nil, false, errors.New("amount must be a positive integer")
	}
	if g.SourceRef != "" {
		l, err := scanLot(s.tx.QueryRow(`SELECT `+lotCols+` FROM credit_lots WHERE wallet_id=? AND source_ref=?`, w.ID, g.SourceRef))
		if err == nil {
			return l, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	if g.ExpiresAt != "" && g.ExpiresAt <= s.nowS {
		return nil, false, errors.New("expires_at is in the past")
	}
	prio := int64(defaultPriority)
	if g.Source == "promo" {
		prio = promoPriority
	}
	if g.Priority != nil {
		prio = *g.Priority
	}
	res, err := s.tx.Exec(`INSERT INTO credit_lots (wallet_id, source, source_ref, original, remaining, priority, expires_at, refundable, paid, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, g.Source, g.SourceRef, g.Amount, g.Amount, prio, nullStr(g.ExpiresAt), boolInt(g.Refundable), boolInt(g.Paid), jsonOrEmpty(g.Metadata, "{}"), s.nowS)
	if err != nil {
		return nil, false, err
	}
	lotID, _ := res.LastInsertId()
	kind := firstNonEmpty(g.Kind, "credit")
	if _, err := s.entry(w, entryIn{kind: kind, amount: g.Amount, dA: g.Amount, lotID: lotID, idem: g.Idem, actor: g.Actor, meta: g.Note}); err != nil {
		return nil, false, err
	}
	l, err := scanLot(s.tx.QueryRow(`SELECT `+lotCols+` FROM credit_lots WHERE id=?`, lotID))
	if err != nil {
		return nil, false, err
	}
	if kind == "credit" {
		s.emit(w.ProjectID, "credits.granted", map[string]any{
			"wallet_id": w.ID, "subject_type": w.SubjectType, "subject_id": w.SubjectID, "unit": w.Unit,
			"lot_id": l.ID, "source": l.Source, "source_ref": l.SourceRef, "amount": l.Original, "expires_at": l.ExpiresAt, "available": w.Available,
		})
	}
	return l, false, nil
}

type draw struct {
	LotID  int64
	Amount int64
}

// drawPlan picks spendable credit in spend order: lowest priority number,
// then soonest expiry (never-expiring last), then oldest. Read-only.
func (s *txState) drawPlan(walletID, amount int64) ([]draw, int64, error) {
	rows, err := s.tx.Query(`SELECT id, remaining - held FROM credit_lots
		WHERE wallet_id=? AND status='active' AND remaining > held AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY priority ASC, (expires_at IS NULL) ASC, expires_at ASC, id ASC`, walletID, s.nowS)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var plan []draw
	var total int64
	for rows.Next() && total < amount {
		var id, spendable int64
		if err := rows.Scan(&id, &spendable); err != nil {
			return nil, 0, err
		}
		take := min(spendable, amount-total)
		plan = append(plan, draw{LotID: id, Amount: take})
		total += take
	}
	return plan, total, rows.Err()
}

// ─── Holds ───────────────────────────────────────────────────────────

type Hold struct {
	ID            int64           `json:"id"`
	WalletID      int64           `json:"wallet_id"`
	Amount        int64           `json:"amount"`
	Committed     int64           `json:"committed"`
	Shortfall     int64           `json:"shortfall,omitempty"`
	Status        string          `json:"status"`
	ReferenceApp  string          `json:"reference_app"`
	ReferenceType string          `json:"reference_type"`
	ReferenceID   string          `json:"reference_id"`
	Scope         string          `json:"scope,omitempty"`
	ExpiresAt     string          `json:"expires_at"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     string          `json:"created_at"`
	SettledAt     string          `json:"settled_at,omitempty"`
}

const holdCols = `h.id, h.wallet_id, h.amount, h.committed, h.shortfall, h.status, h.reference_app, h.reference_type, h.reference_id, h.scope, h.expires_at, h.metadata, h.created_at, h.settled_at`

func scanHold(r rowScanner) (*Hold, error) {
	var h Hold
	var meta string
	var settled sql.NullString
	if err := r.Scan(&h.ID, &h.WalletID, &h.Amount, &h.Committed, &h.Shortfall, &h.Status, &h.ReferenceApp, &h.ReferenceType, &h.ReferenceID, &h.Scope, &h.ExpiresAt, &meta, &h.CreatedAt, &settled); err != nil {
		return nil, err
	}
	h.Metadata = json.RawMessage(meta)
	h.SettledAt = settled.String
	return &h, nil
}

func getHold(q querier, pid string, id int64) (*Hold, error) {
	h, err := scanHold(q.QueryRow(`SELECT `+holdCols+` FROM credit_holds h JOIN credit_wallets w ON w.id=h.wallet_id WHERE h.id=? AND w.project_id=?`, id, pid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("hold %d: %w", id, errNotFound)
	}
	return h, err
}

type Reference struct{ App, Type, ID string }

func (r Reference) String() string { return r.App + "/" + r.Type + "/" + r.ID }

func (r Reference) valid() error {
	if r.App == "" || r.Type == "" || r.ID == "" {
		return errors.New("reference required: reference_app, reference_type and reference_id (or reference \"app/type/id\")")
	}
	return nil
}

const (
	defaultHoldTTL = 15 * time.Minute
	maxHoldTTL     = 7 * 24 * time.Hour
)

type ReserveIn struct {
	Wallet   WalletRef
	Amount   int64
	Ref      Reference
	Scope    string
	TTL      time.Duration
	Metadata any
	Actor    string
}

type ReserveOut struct {
	Denied         bool     `json:"denied"`
	Reason         string   `json:"reason,omitempty"`
	HoldID         int64    `json:"hold_id,omitempty"`
	Hold           *Hold    `json:"hold,omitempty"`
	AvailableAfter int64    `json:"available_after"`
	Cap            *CapView `json:"cap,omitempty"`
	Replayed       bool     `json:"replayed,omitempty"`
}

func (l *Ledger) Reserve(pid string, in ReserveIn) (*ReserveOut, *txState, error) {
	var out *ReserveOut
	s, err := l.write(func(s *txState) error {
		var err error
		out, err = s.reserve(pid, in)
		return err
	})
	return out, s, err
}

func (s *txState) reserve(pid string, in ReserveIn) (*ReserveOut, error) {
	if in.Amount <= 0 {
		return nil, errors.New("amount must be a positive integer")
	}
	if err := in.Ref.valid(); err != nil {
		return nil, err
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = defaultHoldTTL
	}
	if ttl > maxHoldTTL {
		return nil, fmt.Errorf("ttl may be at most %s", maxHoldTTL)
	}
	w, err := findWallet(s.tx, pid, in.Wallet)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return &ReserveOut{Denied: true, Reason: "insufficient_balance"}, nil
	}
	h, err := scanHold(s.tx.QueryRow(`SELECT `+holdCols+` FROM credit_holds h WHERE h.wallet_id=? AND h.reference_app=? AND h.reference_type=? AND h.reference_id=?`, w.ID, in.Ref.App, in.Ref.Type, in.Ref.ID))
	if err == nil {
		return &ReserveOut{HoldID: h.ID, Hold: h, AvailableAfter: w.Available, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if w.Status != "active" {
		return &ReserveOut{Denied: true, Reason: w.Status, AvailableAfter: w.Available}, nil
	}
	caps, err := s.applicableCaps(w.ID, in.Scope)
	if err != nil {
		return nil, err
	}
	for _, c := range caps {
		if c.Spent+c.HeldUsage+in.Amount > c.LimitAmount {
			if err := s.markCapReached(w, c); err != nil {
				return nil, err
			}
			return &ReserveOut{Denied: true, Reason: "cap_reached", AvailableAfter: w.Available, Cap: c}, nil
		}
	}
	plan, total, err := s.drawPlan(w.ID, in.Amount)
	if err != nil {
		return nil, err
	}
	if total < in.Amount {
		return &ReserveOut{Denied: true, Reason: "insufficient_balance", AvailableAfter: w.Available}, nil
	}
	res, err := s.tx.Exec(`INSERT INTO credit_holds (wallet_id, amount, reference_app, reference_type, reference_id, scope, expires_at, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, in.Amount, in.Ref.App, in.Ref.Type, in.Ref.ID, in.Scope, fmtTS(s.now.Add(ttl)), jsonOrEmpty(in.Metadata, "{}"), s.nowS)
	if err != nil {
		return nil, err
	}
	holdID, _ := res.LastInsertId()
	for i, d := range plan {
		if _, err := s.tx.Exec(`INSERT INTO credit_hold_lots (hold_id, lot_id, seq, amount) VALUES (?, ?, ?, ?)`, holdID, d.LotID, i, d.Amount); err != nil {
			return nil, err
		}
		if _, err := s.tx.Exec(`UPDATE credit_lots SET held = held + ? WHERE id=?`, d.Amount, d.LotID); err != nil {
			return nil, err
		}
	}
	for _, c := range caps {
		if _, err := s.tx.Exec(`INSERT INTO credit_cap_usage (cap_id, period, held) VALUES (?, ?, ?)
			ON CONFLICT(cap_id, period) DO UPDATE SET held = held + excluded.held`, c.ID, c.Period, in.Amount); err != nil {
			return nil, err
		}
		if _, err := s.tx.Exec(`INSERT INTO credit_hold_caps (hold_id, cap_id, period) VALUES (?, ?, ?)`, holdID, c.ID, c.Period); err != nil {
			return nil, err
		}
	}
	if _, err := s.entry(w, entryIn{kind: "hold", amount: in.Amount, dA: -in.Amount, dH: in.Amount, holdID: holdID, actor: in.Actor, meta: map[string]any{"reference": in.Ref.String(), "scope": in.Scope}}); err != nil {
		return nil, err
	}
	h, err = getHold(s.tx, pid, holdID)
	if err != nil {
		return nil, err
	}
	return &ReserveOut{HoldID: holdID, Hold: h, AvailableAfter: w.Available}, nil
}

type holdLot struct {
	LotID     int64
	Amount    int64
	ExpiresAt string
}

func (s *txState) holdLots(holdID int64) ([]holdLot, error) {
	rows, err := s.tx.Query(`SELECT hl.lot_id, hl.amount, COALESCE(l.expires_at,'') FROM credit_hold_lots hl JOIN credit_lots l ON l.id=hl.lot_id WHERE hl.hold_id=? ORDER BY hl.seq`, holdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []holdLot
	for rows.Next() {
		var hl holdLot
		if err := rows.Scan(&hl.LotID, &hl.Amount, &hl.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, hl)
	}
	return out, rows.Err()
}

func (s *txState) lotExpired(expiresAt string) bool { return expiresAt != "" && expiresAt <= s.nowS }

type SettleOut struct {
	Hold      *Hold   `json:"hold"`
	Wallet    *Wallet `json:"wallet"`
	Committed int64   `json:"committed"`
	Released  int64   `json:"released"`
	Expired   int64   `json:"expired,omitempty"`
	Shortfall int64   `json:"shortfall,omitempty"`
	Replayed  bool    `json:"replayed,omitempty"`
}

func (l *Ledger) Commit(pid string, holdID, amount int64, actor string) (*SettleOut, *txState, error) {
	var out *SettleOut
	s, err := l.write(func(s *txState) error {
		var err error
		out, err = s.commit(pid, holdID, amount, actor)
		return err
	})
	return out, s, err
}

// commit spends `amount` of a hold. Less than the hold releases the rest;
// more takes the extra from available balance, and whatever cannot be
// covered is recorded as a shortfall because the work already happened.
func (s *txState) commit(pid string, holdID, amount int64, actor string) (*SettleOut, error) {
	if amount < 0 {
		return nil, errors.New("amount must not be negative")
	}
	h, err := getHold(s.tx, pid, holdID)
	if err != nil {
		return nil, err
	}
	w, err := mustWallet(s.tx, pid, WalletRef{ID: h.WalletID})
	if err != nil {
		return nil, err
	}
	switch h.Status {
	case "committed":
		return &SettleOut{Hold: h, Wallet: w, Committed: h.Committed, Shortfall: h.Shortfall, Replayed: true}, nil
	case "released", "expired":
		return nil, fmt.Errorf("hold %d is already %s", h.ID, h.Status)
	}
	hls, err := s.holdLots(h.ID)
	if err != nil {
		return nil, err
	}
	consume := min(amount, h.Amount)
	type step struct{ lotID, take, back, exp, unhold int64 }
	var steps []step
	var back, exp int64
	left := consume
	for _, hl := range hls {
		take := min(left, hl.Amount)
		left -= take
		st := step{lotID: hl.LotID, take: take, unhold: hl.Amount}
		if rest := hl.Amount - take; rest > 0 {
			if s.lotExpired(hl.ExpiresAt) {
				st.exp = rest
				exp += rest
			} else {
				st.back = rest
				back += rest
			}
		}
		steps = append(steps, st)
	}
	ref := Reference{h.ReferenceApp, h.ReferenceType, h.ReferenceID}.String()
	var eCommit, eExpire int64
	if consume > 0 {
		if eCommit, err = s.entry(w, entryIn{kind: "commit", amount: consume, dH: -consume, holdID: h.ID, actor: actor, meta: map[string]any{"reference": ref}}); err != nil {
			return nil, err
		}
	}
	if back > 0 {
		if _, err = s.entry(w, entryIn{kind: "release", amount: back, dA: back, dH: -back, holdID: h.ID, actor: actor, meta: map[string]any{"reference": ref, "reason": "unused"}}); err != nil {
			return nil, err
		}
	}
	if exp > 0 {
		if eExpire, err = s.entry(w, entryIn{kind: "expire", amount: exp, dH: -exp, holdID: h.ID, actor: actor, meta: map[string]any{"reference": ref, "reason": "lot expired while held"}}); err != nil {
			return nil, err
		}
	}
	for _, st := range steps {
		if err := s.lotSpend(eCommit, st.lotID, st.take, st.unhold); err != nil {
			return nil, err
		}
		if st.exp > 0 {
			if err := s.lotSpend(eExpire, st.lotID, st.exp, 0); err != nil {
				return nil, err
			}
		}
	}
	var took, short int64
	if extra := amount - h.Amount; extra > 0 {
		plan, total, err := s.drawPlan(w.ID, extra)
		if err != nil {
			return nil, err
		}
		took = total
		if took > 0 {
			eOver, err := s.entry(w, entryIn{kind: "commit", amount: took, dA: -took, holdID: h.ID, actor: actor, meta: map[string]any{"reference": ref, "overrun": true}})
			if err != nil {
				return nil, err
			}
			for _, d := range plan {
				if err := s.lotSpend(eOver, d.LotID, d.Amount, 0); err != nil {
					return nil, err
				}
			}
		}
		if short = extra - took; short > 0 {
			if err := s.shortfall(w, "commit_overrun", short, ref); err != nil {
				return nil, err
			}
		}
	}
	if err := s.settleCaps(h, consume+took); err != nil {
		return nil, err
	}
	if _, err := s.tx.Exec(`UPDATE credit_holds SET status='committed', committed=?, shortfall=?, settled_at=? WHERE id=?`, consume+took, short, s.nowS, h.ID); err != nil {
		return nil, err
	}
	s.touch(w)
	if h, err = getHold(s.tx, pid, h.ID); err != nil {
		return nil, err
	}
	return &SettleOut{Hold: h, Wallet: w, Committed: consume + took, Released: back, Expired: exp, Shortfall: short}, nil
}

func (l *Ledger) Release(pid string, holdID int64, actor string) (*SettleOut, *txState, error) {
	var out *SettleOut
	s, err := l.write(func(s *txState) error {
		var err error
		out, err = s.release(pid, holdID, "released", actor)
		return err
	})
	return out, s, err
}

// release returns a hold's money to the lots it came from. Money from a lot
// that expired while held is expired instead of becoming available.
func (s *txState) release(pid string, holdID int64, status, actor string) (*SettleOut, error) {
	h, err := getHold(s.tx, pid, holdID)
	if err != nil {
		return nil, err
	}
	w, err := mustWallet(s.tx, pid, WalletRef{ID: h.WalletID})
	if err != nil {
		return nil, err
	}
	switch h.Status {
	case "released", "expired":
		return &SettleOut{Hold: h, Wallet: w, Replayed: true}, nil
	case "committed":
		return nil, fmt.Errorf("hold %d is already committed", h.ID)
	}
	hls, err := s.holdLots(h.ID)
	if err != nil {
		return nil, err
	}
	var back, exp int64
	for _, hl := range hls {
		if s.lotExpired(hl.ExpiresAt) {
			exp += hl.Amount
		} else {
			back += hl.Amount
		}
	}
	ref := Reference{h.ReferenceApp, h.ReferenceType, h.ReferenceID}.String()
	var eExpire int64
	if back > 0 {
		if _, err := s.entry(w, entryIn{kind: "release", amount: back, dA: back, dH: -back, holdID: h.ID, actor: actor, meta: map[string]any{"reference": ref, "reason": status}}); err != nil {
			return nil, err
		}
	}
	if exp > 0 {
		if eExpire, err = s.entry(w, entryIn{kind: "expire", amount: exp, dH: -exp, holdID: h.ID, actor: actor, meta: map[string]any{"reference": ref, "reason": "lot expired while held"}}); err != nil {
			return nil, err
		}
	}
	for _, hl := range hls {
		take := int64(0)
		if s.lotExpired(hl.ExpiresAt) {
			take = hl.Amount
		}
		if err := s.lotSpend(eExpire, hl.LotID, take, hl.Amount); err != nil {
			return nil, err
		}
		if take == 0 {
			continue
		}
		if _, err := s.tx.Exec(`UPDATE credit_lots SET status='expired' WHERE id=? AND status IN ('active','depleted') AND held=0 AND expires_at <= ?`, hl.LotID, s.nowS); err != nil {
			return nil, err
		}
	}
	if err := s.settleCaps(h, 0); err != nil {
		return nil, err
	}
	if _, err := s.tx.Exec(`UPDATE credit_holds SET status=?, settled_at=? WHERE id=?`, status, s.nowS, h.ID); err != nil {
		return nil, err
	}
	if h, err = getHold(s.tx, pid, h.ID); err != nil {
		return nil, err
	}
	if status == "expired" {
		s.emit(pid, "credits.hold.expired", map[string]any{"hold_id": h.ID, "wallet_id": w.ID, "amount": h.Amount, "reference": ref})
	}
	return &SettleOut{Hold: h, Wallet: w, Released: back, Expired: exp}, nil
}

func (l *Ledger) Spend(pid string, in ReserveIn) (map[string]any, *txState, error) {
	var out map[string]any
	s, err := l.write(func(s *txState) error {
		r, err := s.reserve(pid, in)
		if err != nil {
			return err
		}
		if r.Denied {
			out = map[string]any{"denied": true, "reason": r.Reason, "available_after": r.AvailableAfter, "cap": r.Cap}
			return nil
		}
		if r.Replayed && r.Hold.Status != "active" {
			out = map[string]any{"denied": false, "replayed": true, "hold": r.Hold, "committed": r.Hold.Committed, "available_after": r.AvailableAfter}
			return nil
		}
		c, err := s.commit(pid, r.HoldID, in.Amount, in.Actor)
		if err != nil {
			return err
		}
		out = map[string]any{"denied": false, "replayed": r.Replayed, "hold": c.Hold, "committed": c.Committed, "available_after": c.Wallet.Available}
		return nil
	})
	return out, s, err
}

func (s *txState) shortfall(w *Wallet, kind string, amount int64, ref string) error {
	res, err := s.tx.Exec(`INSERT OR IGNORE INTO credit_shortfalls (wallet_id, kind, amount, reference, created_at) VALUES (?, ?, ?, ?, ?)`, w.ID, kind, amount, ref, s.nowS)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.emit(w.ProjectID, "credits.shortfall", map[string]any{"wallet_id": w.ID, "subject_type": w.SubjectType, "subject_id": w.SubjectID, "unit": w.Unit, "kind": kind, "amount": amount, "reference": ref})
	}
	return nil
}

// ─── Expiry ──────────────────────────────────────────────────────────

// ExpireDue releases holds past expires_at and expires the unheld part of
// lots past expires_at. Each item runs in its own transaction.
func (l *Ledger) ExpireDue(pid string) (holds, lots int, events []pendingEvent, autoReload []int64, err error) {
	nowS := fmtTS(nowFunc())
	q := `SELECT h.id, w.project_id FROM credit_holds h JOIN credit_wallets w ON w.id=h.wallet_id WHERE h.status='active' AND h.expires_at <= ?`
	args := []any{nowS}
	if pid != "" {
		q += ` AND w.project_id=?`
		args = append(args, pid)
	}
	type item struct {
		id  int64
		pid string
	}
	collect := func(q string, args []any) ([]item, error) {
		rows, err := l.db.Query(q+` LIMIT 500`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.pid); err != nil {
				return nil, err
			}
			out = append(out, it)
		}
		return out, rows.Err()
	}
	due, err := collect(q, args)
	if err != nil {
		return
	}
	for _, it := range due {
		s, e := l.write(func(s *txState) error { _, err := s.release(it.pid, it.id, "expired", "system:expiry"); return err })
		if e != nil {
			err = e
			return
		}
		holds++
		events = append(events, s.events...)
		autoReload = append(autoReload, s.autoReload...)
	}
	q = `SELECT l.id, w.project_id FROM credit_lots l JOIN credit_wallets w ON w.id=l.wallet_id WHERE l.status='active' AND l.expires_at IS NOT NULL AND l.expires_at <= ? AND l.remaining > l.held`
	if pid != "" {
		q += ` AND w.project_id=?`
	}
	dueLots, err := collect(q, args)
	if err != nil {
		return
	}
	for _, it := range dueLots {
		s, e := l.write(func(s *txState) error { return s.expireLot(it.pid, it.id) })
		if e != nil {
			err = e
			return
		}
		lots++
		events = append(events, s.events...)
		autoReload = append(autoReload, s.autoReload...)
	}
	return
}

func (s *txState) expireLot(pid string, lotID int64) error {
	lot, err := scanLot(s.tx.QueryRow(`SELECT `+lotCols+` FROM credit_lots WHERE id=?`, lotID))
	if err != nil {
		return err
	}
	amount := lot.Remaining - lot.Held
	if lot.Status != "active" || amount <= 0 || !s.lotExpired(lot.ExpiresAt) {
		return nil
	}
	w, err := mustWallet(s.tx, pid, WalletRef{ID: lot.WalletID})
	if err != nil {
		return err
	}
	eid, err := s.entry(w, entryIn{kind: "expire", amount: amount, dA: -amount, lotID: lot.ID, actor: "system:expiry"})
	if err != nil {
		return err
	}
	if err := s.lotSpend(eid, lot.ID, amount, 0); err != nil {
		return err
	}
	if lot.Held == 0 {
		if _, err := s.tx.Exec(`UPDATE credit_lots SET status='expired' WHERE id=?`, lot.ID); err != nil {
			return err
		}
	}
	s.emit(pid, "credits.lot.expired", map[string]any{"wallet_id": w.ID, "lot_id": lot.ID, "source": lot.Source, "amount": amount, "unit": w.Unit, "subject_type": w.SubjectType, "subject_id": w.SubjectID})
	return nil
}

// ─── Admin ───────────────────────────────────────────────────────────

type AdjustIn struct {
	Wallet    WalletRef
	Amount    int64 // signed
	Reason    string
	Idem      string
	Actor     string
	ExpiresAt string
	Priority  *int64
}

func (s *txState) adjust(pid string, in AdjustIn) (map[string]any, error) {
	if in.Amount == 0 {
		return nil, errors.New("amount must be a non-zero integer")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, errors.New("reason required")
	}
	var w *Wallet
	var err error
	if in.Amount > 0 {
		w, _, err = s.ensureWallet(pid, in.Wallet, walletOpts{})
	} else {
		w, err = mustWallet(s.tx, pid, in.Wallet)
	}
	if err != nil {
		return nil, err
	}
	if e, err := s.entryByIdem(w.ID, in.Idem); err != nil || e != nil {
		return map[string]any{"entry": e, "wallet": w, "replayed": true}, err
	}
	note := map[string]any{"reason": in.Reason}
	if in.Amount > 0 {
		ref := ""
		if in.Idem != "" {
			ref = "adjust:" + in.Idem
		}
		lot, _, err := s.grantLot(w, grantIn{Source: "adjustment", SourceRef: ref, Amount: in.Amount, Priority: in.Priority, ExpiresAt: in.ExpiresAt, Kind: "adjust", Idem: in.Idem, Actor: in.Actor, Note: note, Metadata: note})
		if err != nil {
			return nil, err
		}
		return map[string]any{"lot": lot, "wallet": w}, nil
	}
	need := -in.Amount
	plan, total, err := s.drawPlan(w.ID, need)
	if err != nil {
		return nil, err
	}
	if total < need {
		return nil, fmt.Errorf("adjustment of -%d exceeds the available balance %d; balances never go negative", need, w.Available)
	}
	eid, err := s.entry(w, entryIn{kind: "adjust", amount: need, dA: -need, idem: in.Idem, actor: in.Actor, meta: note})
	if err != nil {
		return nil, err
	}
	for _, d := range plan {
		if err := s.lotSpend(eid, d.LotID, d.Amount, 0); err != nil {
			return nil, err
		}
	}
	return map[string]any{"entry_id": eid, "wallet": w}, nil
}

type TransferIn struct {
	From, To WalletRef
	Amount   int64
	Reason   string
	Idem     string
	Actor    string
}

// transfer moves spendable credit between two wallets of the same unit and
// the same billing customer. Each source lot becomes its own target lot so
// priority, expiry and the paid/refundable flags carry over.
func (s *txState) transfer(pid string, in TransferIn) (map[string]any, error) {
	if in.Amount <= 0 {
		return nil, errors.New("amount must be a positive integer")
	}
	from, err := mustWallet(s.tx, pid, in.From)
	if err != nil {
		return nil, err
	}
	if in.To.Unit == "" {
		in.To.Unit = from.Unit
	}
	to, _, err := s.ensureWallet(pid, in.To, walletOpts{BillingCustomerID: from.BillingCustomerID})
	if err != nil {
		return nil, err
	}
	if from.ID == to.ID {
		return nil, errors.New("cannot transfer to the same wallet")
	}
	if from.Unit != to.Unit {
		return nil, fmt.Errorf("units differ (%s vs %s); credits never converts units", from.Unit, to.Unit)
	}
	if from.BillingCustomerID != 0 && to.BillingCustomerID != 0 && from.BillingCustomerID != to.BillingCustomerID {
		return nil, errors.New("transfers between different billing customers are not supported")
	}
	if from.Status != "active" {
		return nil, fmt.Errorf("source wallet is %s", from.Status)
	}
	if to.Status == "closed" {
		return nil, errors.New("target wallet is closed")
	}
	if e, err := s.entryByIdem(from.ID, in.Idem); err != nil || e != nil {
		return map[string]any{"entry": e, "from": from, "to": to, "replayed": true}, err
	}
	plan, total, err := s.drawPlan(from.ID, in.Amount)
	if err != nil {
		return nil, err
	}
	if total < in.Amount {
		return nil, fmt.Errorf("insufficient balance: %d available", from.Available)
	}
	eid, err := s.entry(from, entryIn{kind: "transfer", amount: in.Amount, dA: -in.Amount, idem: in.Idem, actor: in.Actor, meta: map[string]any{"to_wallet_id": to.ID, "reason": in.Reason}})
	if err != nil {
		return nil, err
	}
	var lots []*Lot
	for _, d := range plan {
		src, err := scanLot(s.tx.QueryRow(`SELECT `+lotCols+` FROM credit_lots WHERE id=?`, d.LotID))
		if err != nil {
			return nil, err
		}
		if err := s.lotSpend(eid, d.LotID, d.Amount, 0); err != nil {
			return nil, err
		}
		prio := src.Priority
		lot, _, err := s.grantLot(to, grantIn{
			Source: "transfer", SourceRef: fmt.Sprintf("transfer:%d:%d", eid, src.ID), Amount: d.Amount, Priority: &prio,
			ExpiresAt: src.ExpiresAt, Refundable: src.Refundable, Paid: src.Paid,
			Metadata: map[string]any{"from_wallet_id": from.ID, "from_lot_id": src.ID, "from_source": src.Source},
			Kind:     "transfer", Actor: in.Actor, Note: map[string]any{"from_wallet_id": from.ID, "reason": in.Reason},
		})
		if err != nil {
			return nil, err
		}
		lots = append(lots, lot)
	}
	return map[string]any{"from": from, "to": to, "amount": in.Amount, "lots": lots}, nil
}

func (s *txState) setStatus(pid string, ref WalletRef, status string) (*Wallet, error) {
	w, err := mustWallet(s.tx, pid, ref)
	if err != nil {
		return nil, err
	}
	if w.Status == "closed" {
		return nil, errors.New("wallet is closed")
	}
	if _, err := s.tx.Exec(`UPDATE credit_wallets SET status=?, updated_at=? WHERE id=?`, status, s.nowS, w.ID); err != nil {
		return nil, err
	}
	w.Status = status
	w.UpdatedAt = s.nowS
	return w, nil
}

// ─── Caps ────────────────────────────────────────────────────────────

type CapView struct {
	ID          int64           `json:"id"`
	WalletID    int64           `json:"wallet_id"`
	Scope       string          `json:"scope"`
	Window      string          `json:"window"`
	LimitAmount int64           `json:"limit_amount"`
	Period      string          `json:"period"`
	Spent       int64           `json:"spent"`
	HeldUsage   int64           `json:"held"`
	Remaining   int64           `json:"remaining"`
	Metadata    json.RawMessage `json:"metadata"`
}

func capPeriod(window string, t time.Time) string {
	t = t.UTC()
	switch window {
	case "day":
		return t.Format("2006-01-02")
	case "week":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "month":
		return t.Format("2006-01")
	}
	return "lifetime"
}

var validWindows = map[string]bool{"day": true, "week": true, "month": true, "lifetime": true}

func listCaps(q querier, walletID int64, scope string, onlyApplicable bool, now time.Time) ([]*CapView, error) {
	query := `SELECT id, wallet_id, scope, cap_window, limit_amount, metadata FROM credit_caps WHERE wallet_id=?`
	args := []any{walletID}
	if onlyApplicable {
		query += ` AND (scope='' OR scope=?)`
		args = append(args, scope)
	}
	rows, err := q.Query(query+` ORDER BY scope, id`, args...)
	if err != nil {
		return nil, err
	}
	var caps []*CapView
	for rows.Next() {
		var c CapView
		var meta string
		if err := rows.Scan(&c.ID, &c.WalletID, &c.Scope, &c.Window, &c.LimitAmount, &meta); err != nil {
			rows.Close()
			return nil, err
		}
		c.Metadata = json.RawMessage(meta)
		c.Period = capPeriod(c.Window, now)
		caps = append(caps, &c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range caps {
		err := q.QueryRow(`SELECT spent, held FROM credit_cap_usage WHERE cap_id=? AND period=?`, c.ID, c.Period).Scan(&c.Spent, &c.HeldUsage)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		c.Remaining = max(c.LimitAmount-c.Spent-c.HeldUsage, 0)
	}
	return caps, nil
}

func (s *txState) applicableCaps(walletID int64, scope string) ([]*CapView, error) {
	return listCaps(s.tx, walletID, scope, true, s.now)
}

func (s *txState) markCapReached(w *Wallet, c *CapView) error {
	if _, err := s.tx.Exec(`INSERT INTO credit_cap_usage (cap_id, period) VALUES (?, ?) ON CONFLICT(cap_id, period) DO NOTHING`, c.ID, c.Period); err != nil {
		return err
	}
	res, err := s.tx.Exec(`UPDATE credit_cap_usage SET reached_at=? WHERE cap_id=? AND period=? AND reached_at IS NULL`, s.nowS, c.ID, c.Period)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.emit(w.ProjectID, "credits.cap.reached", map[string]any{"wallet_id": w.ID, "subject_type": w.SubjectType, "subject_id": w.SubjectID, "unit": w.Unit, "cap_id": c.ID, "scope": c.Scope, "window": c.Window, "period": c.Period, "limit_amount": c.LimitAmount, "spent": c.Spent, "held": c.HeldUsage})
	}
	return nil
}

// settleCaps moves a hold's cap usage from held to spent in the period the
// hold was created in.
func (s *txState) settleCaps(h *Hold, spent int64) error {
	_, err := s.tx.Exec(`UPDATE credit_cap_usage SET held = held - ?, spent = spent + ?
		WHERE (cap_id, period) IN (SELECT cap_id, period FROM credit_hold_caps WHERE hold_id=?)`, h.Amount, spent, h.ID)
	return err
}

func (s *txState) setCap(pid string, ref WalletRef, scope, window string, limit int64, meta any) (*CapView, error) {
	if !validWindows[window] {
		return nil, errors.New("window must be day, week, month or lifetime")
	}
	if limit < 0 {
		return nil, errors.New("limit_amount must not be negative")
	}
	w, err := mustWallet(s.tx, pid, ref)
	if err != nil {
		return nil, err
	}
	if _, err := s.tx.Exec(`INSERT INTO credit_caps (wallet_id, scope, cap_window, limit_amount, metadata, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(wallet_id, scope, cap_window) DO UPDATE SET limit_amount=excluded.limit_amount, metadata=excluded.metadata, updated_at=excluded.updated_at`,
		w.ID, scope, window, limit, jsonOrEmpty(meta, "{}"), s.nowS, s.nowS); err != nil {
		return nil, err
	}
	// A raised limit re-arms the cap.reached event for the current period.
	if _, err := s.tx.Exec(`UPDATE credit_cap_usage SET reached_at=NULL WHERE cap_id=(SELECT id FROM credit_caps WHERE wallet_id=? AND scope=? AND cap_window=?) AND spent + held < ?`, w.ID, scope, window, limit); err != nil {
		return nil, err
	}
	caps, err := listCaps(s.tx, w.ID, "", false, s.now)
	if err != nil {
		return nil, err
	}
	for _, c := range caps {
		if c.Scope == scope && c.Window == window {
			return c, nil
		}
	}
	return nil, errors.New("cap not found after upsert")
}

// ─── Reconciliation & reports ────────────────────────────────────────

type ReconcileIssue struct {
	WalletID int64  `json:"wallet_id"`
	LotID    int64  `json:"lot_id,omitempty"`
	Check    string `json:"check"`
	Detail   string `json:"detail"`
}

func reconcile(q querier, pid string, walletID int64) ([]ReconcileIssue, int, error) {
	query := `SELECT ` + walletCols + ` FROM credit_wallets WHERE project_id=?`
	args := []any{pid}
	if walletID > 0 {
		query += ` AND id=?`
		args = append(args, walletID)
	}
	rows, err := q.Query(query+` ORDER BY id`, args...)
	if err != nil {
		return nil, 0, err
	}
	var wallets []*Wallet
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		wallets = append(wallets, w)
	}
	rows.Close()
	issues := []ReconcileIssue{}
	add := func(w int64, lot int64, check, format string, a ...any) {
		issues = append(issues, ReconcileIssue{WalletID: w, LotID: lot, Check: check, Detail: fmt.Sprintf(format, a...)})
	}
	for _, w := range wallets {
		var lotRemaining, lotHeld, holdHeld, entA, entH, negLots int64
		if err := q.QueryRow(`SELECT COALESCE(SUM(remaining),0), COALESCE(SUM(held),0), COALESCE(SUM(CASE WHEN remaining < 0 OR held < 0 OR held > remaining THEN 1 ELSE 0 END),0) FROM credit_lots WHERE wallet_id=?`, w.ID).Scan(&lotRemaining, &lotHeld, &negLots); err != nil {
			return nil, 0, err
		}
		if err := q.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM credit_holds WHERE wallet_id=? AND status='active'`, w.ID).Scan(&holdHeld); err != nil {
			return nil, 0, err
		}
		if err := q.QueryRow(`SELECT COALESCE(SUM(delta_available),0), COALESCE(SUM(delta_held),0) FROM credit_entries WHERE wallet_id=?`, w.ID).Scan(&entA, &entH); err != nil {
			return nil, 0, err
		}
		if w.Available < 0 || w.Held < 0 {
			add(w.ID, 0, "non_negative", "available %d, held %d", w.Available, w.Held)
		}
		if w.Available+w.Held != lotRemaining {
			add(w.ID, 0, "lots_match_balance", "available+held = %d but lots remaining = %d", w.Available+w.Held, lotRemaining)
		}
		if w.Held != lotHeld || w.Held != holdHeld {
			add(w.ID, 0, "held_matches", "wallet held %d, lots held %d, active holds %d", w.Held, lotHeld, holdHeld)
		}
		if w.Available != entA || w.Held != entH {
			add(w.ID, 0, "entries_rebuild_balance", "entries sum to available %d, held %d; wallet has %d, %d", entA, entH, w.Available, w.Held)
		}
		if negLots > 0 {
			add(w.ID, 0, "lot_bounds", "%d lot(s) with negative or over-held amounts", negLots)
		}
		lrows, err := q.Query(`SELECT l.id, l.remaining, l.original + COALESCE((SELECT SUM(m.delta) FROM credit_lot_moves m WHERE m.lot_id=l.id),0),
				l.held, COALESCE((SELECT SUM(hl.amount) FROM credit_hold_lots hl JOIN credit_holds h ON h.id=hl.hold_id WHERE hl.lot_id=l.id AND h.status='active'),0)
			FROM credit_lots l WHERE l.wallet_id=?`, w.ID)
		if err != nil {
			return nil, 0, err
		}
		for lrows.Next() {
			var id, rem, rebuilt, held, holdSum int64
			if err := lrows.Scan(&id, &rem, &rebuilt, &held, &holdSum); err != nil {
				lrows.Close()
				return nil, 0, err
			}
			if rem != rebuilt {
				add(w.ID, id, "lot_moves_rebuild_remaining", "remaining %d, original+moves %d", rem, rebuilt)
			}
			if held != holdSum {
				add(w.ID, id, "lot_held_matches_holds", "lot held %d, active hold draws %d", held, holdSum)
			}
		}
		lrows.Close()
	}
	return issues, len(wallets), nil
}

type LiabilityRow struct {
	Unit      string `json:"unit"`
	Lots      int64  `json:"lots"`
	Wallets   int64  `json:"wallets"`
	Purchased int64  `json:"purchased"`
	Unspent   int64  `json:"unspent"`
}

// liability reports unspent paid credit (prepaid customer money) as of the
// end of the given timestamp, rebuilt from lot originals and lot moves.
func liability(q querier, pid, asOf string) ([]LiabilityRow, error) {
	rows, err := q.Query(`SELECT w.unit, COUNT(*), COUNT(DISTINCT w.id), COALESCE(SUM(l.original),0),
			COALESCE(SUM(l.original + COALESCE((SELECT SUM(m.delta) FROM credit_lot_moves m WHERE m.lot_id=l.id AND m.created_at <= ?),0)),0)
		FROM credit_lots l JOIN credit_wallets w ON w.id=l.wallet_id
		WHERE w.project_id=? AND l.paid=1 AND l.created_at <= ?
		GROUP BY w.unit ORDER BY w.unit`, asOf, pid, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiabilityRow{}
	for rows.Next() {
		var r LiabilityRow
		if err := rows.Scan(&r.Unit, &r.Lots, &r.Wallets, &r.Purchased, &r.Unspent); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
