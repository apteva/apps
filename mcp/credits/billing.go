package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Currency wallets hold micro-units (1/1,000,000); billing works in cents.
const microPerCent = 10_000

var topupMu sync.Mutex // serialises invoice creation so one top-up never gets two invoices

type Topup struct {
	ID             int64  `json:"id"`
	WalletID       int64  `json:"wallet_id"`
	Kind           string `json:"kind"`
	Amount         int64  `json:"amount"`
	PriceCents     int64  `json:"price_cents"`
	Currency       string `json:"currency"`
	InvoiceID      int64  `json:"invoice_id,omitempty"`
	Status         string `json:"status"`
	LotID          int64  `json:"lot_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Error          string `json:"error,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

const topupCols = `t.id, t.wallet_id, t.kind, t.amount, t.price_cents, t.currency, t.invoice_id, t.status, COALESCE(t.lot_id,0), t.idempotency_key, t.error, t.created_at, t.updated_at`

func scanTopup(r rowScanner) (*Topup, error) {
	var t Topup
	err := r.Scan(&t.ID, &t.WalletID, &t.Kind, &t.Amount, &t.PriceCents, &t.Currency, &t.InvoiceID, &t.Status, &t.LotID, &t.IdempotencyKey, &t.Error, &t.CreatedAt, &t.UpdatedAt)
	return &t, err
}

func isCurrencyUnit(u string) bool {
	if len(u) != 3 {
		return false
	}
	for _, r := range u {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// resolvePricing fills in whichever of amount / price_cents is missing for a
// currency wallet (1 cent = 10,000 micro-units). Other units need both.
func resolvePricing(unit, currency string, amount, price int64) (int64, int64, string, error) {
	currency = strings.ToLower(currency)
	if currency == "" && isCurrencyUnit(unit) {
		currency = unit
	}
	if currency == "" {
		return 0, 0, "", errors.New("currency required for a non-currency wallet")
	}
	same := currency == unit
	switch {
	case amount == 0 && price > 0 && same:
		amount = price * microPerCent
	case price == 0 && amount > 0 && same:
		if amount%microPerCent != 0 {
			return 0, 0, "", errors.New("amount must be a whole number of cents (multiple of 10000 micro-units)")
		}
		price = amount / microPerCent
	}
	if amount <= 0 || price <= 0 {
		return 0, 0, "", errors.New("amount and price_cents must be positive (a currency wallet needs only one of them)")
	}
	return amount, price, currency, nil
}

func (a *App) toolTopupCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	idem := strArg(args, "idempotency_key")
	if idem == "" {
		return nil, errors.New("idempotency_key required")
	}
	amount, _, err := amountArg(args, "amount")
	if err != nil {
		return nil, err
	}
	price, _, err := amountArg(args, "price_cents")
	if err != nil {
		return nil, err
	}
	presentation := firstNonEmpty(strArg(args, "presentation"), "hosted")
	if presentation != "hosted" && presentation != "elements" && presentation != "none" {
		return nil, errors.New("presentation must be hosted, elements or none")
	}
	ref := walletRefFrom(args, "")
	var t *Topup
	var w *Wallet
	_, err = newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		if w, _, err = s.ensureWallet(pid, ref, walletOpts{}); err != nil {
			return err
		}
		if existing, err := scanTopup(s.tx.QueryRow(`SELECT `+topupCols+` FROM credit_topups t WHERE t.wallet_id=? AND t.idempotency_key=?`, w.ID, idem)); err == nil {
			t = existing
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if w.Status != "active" {
			return fmt.Errorf("wallet is %s", w.Status)
		}
		customer := int64Arg(args, "customer_id")
		if customer == 0 {
			customer = w.BillingCustomerID
		}
		if customer == 0 {
			return errors.New("customer_id required: the billing customer to invoice")
		}
		if customer != w.BillingCustomerID {
			if _, err := s.tx.Exec(`UPDATE credit_wallets SET billing_customer_id=?, updated_at=? WHERE id=?`, customer, s.nowS, w.ID); err != nil {
				return err
			}
			w.BillingCustomerID = customer
		}
		amt, pr, cur, err := resolvePricing(w.Unit, strArg(args, "currency"), amount, price)
		if err != nil {
			return err
		}
		t, err = s.insertTopup(w, "manual", amt, pr, cur, idem)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"topup": t}
	if t.Status != "pending" {
		return out, nil
	}
	invoiceID, err := a.ensureTopupInvoice(ctx, pid, w, t, strArg(args, "description"))
	if err != nil {
		return nil, fmt.Errorf("billing invoice for top-up %d: %w", t.ID, err)
	}
	t.InvoiceID = invoiceID
	out["invoice_id"] = invoiceID
	if presentation == "none" {
		return out, nil
	}
	session := map[string]any{}
	sessionArgs := map[string]any{
		"invoice_id": invoiceID, "presentation": presentation, "idempotency_key": fmt.Sprintf("credits-topup-%d", t.ID),
		"save_payment_method": boolArg(args, "save_payment_method"),
	}
	for _, k := range []string{"success_url", "cancel_url", "return_url"} {
		if v := strArg(args, k); v != "" {
			sessionArgs[k] = v
		}
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("billing", "invoices_create_payment_session", sessionArgs, &session); err != nil {
		return nil, fmt.Errorf("billing payment session for invoice %d: %w", invoiceID, err)
	}
	out["payment_session"] = session
	return out, nil
}

func (s *txState) insertTopup(w *Wallet, kind string, amount, price int64, currency, idem string) (*Topup, error) {
	res, err := s.tx.Exec(`INSERT INTO credit_topups (wallet_id, kind, amount, price_cents, currency, idempotency_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, kind, amount, price, currency, idem, s.nowS, s.nowS)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return scanTopup(s.tx.QueryRow(`SELECT `+topupCols+` FROM credit_topups t WHERE t.id=?`, id))
}

// ensureTopupInvoice creates the finalized one-off invoice once per top-up.
// The invoice metadata carries the top-up id, so a paid invoice is matched
// even if the process died before invoice_id was saved here.
func (a *App) ensureTopupInvoice(ctx *sdk.AppCtx, pid string, w *Wallet, t *Topup, description string) (int64, error) {
	topupMu.Lock()
	defer topupMu.Unlock()
	var invoiceID int64
	if err := ctx.AppDB().QueryRow(`SELECT invoice_id FROM credit_topups WHERE id=?`, t.ID).Scan(&invoiceID); err != nil {
		return 0, err
	}
	if invoiceID != 0 {
		return invoiceID, nil
	}
	if description == "" {
		description = fmt.Sprintf("Prepaid credit (%s)", formatAmount(t.Amount, w.Unit))
	}
	var resp struct {
		Invoice struct {
			ID int64 `json:"id"`
		} `json:"invoice"`
	}
	err := ctx.WithProject(pid).PlatformAPI().CallAppResult("billing", "invoices_create_from_prepared_lines", map[string]any{
		"customer_id": w.BillingCustomerID,
		"currency":    t.Currency,
		"finalize":    true,
		"line_items":  []any{map[string]any{"description": description, "quantity": 1, "unit_price_cents": t.PriceCents, "metadata": map[string]any{"source": "credits"}}},
		"metadata":    map[string]any{"source": "credits", "credits_topup_id": t.ID, "credits_wallet_id": w.ID, "credits_kind": t.Kind},
	}, &resp)
	if err != nil {
		return 0, err
	}
	if resp.Invoice.ID == 0 {
		return 0, errors.New("billing returned no invoice id")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE credit_topups SET invoice_id=?, updated_at=? WHERE id=? AND invoice_id=0`, resp.Invoice.ID, fmtTS(nowFunc()), t.ID); err != nil {
		return 0, err
	}
	return resp.Invoice.ID, nil
}

func formatAmount(amount int64, unit string) string {
	if isCurrencyUnit(unit) {
		return fmt.Sprintf("%.2f %s", float64(amount)/1_000_000, strings.ToUpper(unit))
	}
	return fmt.Sprintf("%d %s", amount, unit)
}

func (a *App) toolTopupsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.readWallet(ctx, args)
	if err != nil {
		return nil, err
	}
	where := "t.wallet_id=?"
	q := []any{w.ID}
	if st := strArg(args, "status"); st != "" {
		where += " AND t.status=?"
		q = append(q, st)
	}
	q = append(q, clampLimit(int64Arg(args, "limit"), 200))
	rows, err := ctx.AppReadDB().Query(`SELECT `+topupCols+` FROM credit_topups t WHERE `+where+` ORDER BY t.id DESC LIMIT ?`, q...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Topup{}
	for rows.Next() {
		t, err := scanTopup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return map[string]any{"wallet": w, "topups": out, "count": len(out)}, rows.Err()
}

// ─── Billing events ──────────────────────────────────────────────────

func eventProject(ctx *sdk.AppCtx, ev sdk.Event) string {
	if pid := strings.TrimSpace(ev.ProjectID); pid != "" {
		return pid
	}
	return ctx.CurrentProject()
}

func eventMetadata(data map[string]any) map[string]any {
	switch m := data["metadata"].(type) {
	case map[string]any:
		return m
	case string:
		var out map[string]any
		_ = json.Unmarshal([]byte(m), &out)
		return out
	}
	return nil
}

// handleInvoicePaid is the only path that creates a purchase lot: credit
// exists only once the money is in. Idempotent by the lot's source_ref.
func (a *App) handleInvoicePaid(ctx *sdk.AppCtx, ev sdk.Event) error {
	pid := eventProject(ctx, ev)
	invoiceID := int64Arg(ev.Data, "id")
	if pid == "" || invoiceID == 0 || strArg(ev.Data, "status") != "paid" {
		return nil
	}
	topupID := int64Arg(eventMetadata(ev.Data), "credits_topup_id")
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		t, err := scanTopup(s.tx.QueryRow(`SELECT `+topupCols+` FROM credit_topups t JOIN credit_wallets w ON w.id=t.wallet_id
			WHERE w.project_id=? AND (t.invoice_id=? OR (t.id=? AND t.invoice_id=0)) ORDER BY t.invoice_id DESC LIMIT 1`, pid, invoiceID, topupID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not a credits invoice
		}
		if err != nil {
			return err
		}
		if t.Status == "paid" {
			if t.InvoiceID != invoiceID {
				ctx.Logger().Warn("credits: second paid invoice for one top-up; not credited, review in billing", "topup_id", t.ID, "invoice_id", invoiceID)
			}
			return nil
		}
		w, err := mustWallet(s.tx, pid, WalletRef{ID: t.WalletID})
		if err != nil {
			return err
		}
		prio := int64(purchasePriority)
		lot, _, err := s.grantLot(w, grantIn{
			Source: "purchase", SourceRef: fmt.Sprintf("billing:invoice:%d", invoiceID), Amount: t.Amount, Priority: &prio,
			Refundable: true, Paid: true, Actor: "billing",
			Metadata: map[string]any{"topup_id": t.ID, "invoice_id": invoiceID, "price_cents": t.PriceCents, "currency": t.Currency},
			Note:     map[string]any{"topup_id": t.ID, "invoice_id": invoiceID},
		})
		if err != nil {
			return err
		}
		if _, err := s.tx.Exec(`UPDATE credit_topups SET status='paid', lot_id=?, invoice_id=?, error='', updated_at=? WHERE id=?`, lot.ID, invoiceID, s.nowS, t.ID); err != nil {
			return err
		}
		s.emit(pid, "credits.topup.paid", map[string]any{
			"topup_id": t.ID, "wallet_id": w.ID, "subject_type": w.SubjectType, "subject_id": w.SubjectID, "unit": w.Unit,
			"amount": t.Amount, "price_cents": t.PriceCents, "currency": t.Currency, "invoice_id": invoiceID, "lot_id": lot.ID, "kind": t.Kind, "available": w.Available,
		})
		return nil
	})
	if err != nil {
		return err
	}
	a.finish(ctx, s)
	return nil
}

// handleInvoiceRefunded removes the unspent share of a refunded purchase.
// Credit already spent (or currently held) cannot be taken back without
// going negative, so that part is recorded as a shortfall for review.
func (a *App) handleInvoiceRefunded(ctx *sdk.AppCtx, ev sdk.Event) error {
	pid := eventProject(ctx, ev)
	invoiceID := int64Arg(ev.Data, "id")
	if pid == "" || invoiceID == 0 {
		return nil
	}
	key := firstNonEmpty(strArg(ev.Data, "event_id"), ev.DeliveryID)
	if key == "" {
		if pay := int64Arg(ev.Data, "payment_id"); pay != 0 {
			key = fmt.Sprintf("payment:%d", pay)
		} else {
			key = fmt.Sprintf("invoice:%d:paid:%d", invoiceID, int64Arg(ev.Data, "amount_paid_cents"))
		}
	}
	total := int64Arg(ev.Data, "total_cents")
	refunded := total
	if p, ok, _ := amountArg(ev.Data, "payment_amount_cents"); ok && p < 0 {
		refunded = -p
	}
	if total <= 0 || refunded <= 0 {
		return nil
	}
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		lot, err := scanLot(s.tx.QueryRow(`SELECT l.`+strings.ReplaceAll(lotCols, ", ", ", l.")+` FROM credit_lots l JOIN credit_wallets w ON w.id=l.wallet_id
			WHERE w.project_id=? AND l.source='purchase' AND l.source_ref=?`, pid, fmt.Sprintf("billing:invoice:%d", invoiceID)))
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not a credits invoice
		}
		if err != nil {
			return err
		}
		res, err := s.tx.Exec(`INSERT OR IGNORE INTO credit_billing_events (event_key, created_at) VALUES (?, ?)`, "refund:"+key, s.nowS)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // already applied
		}
		target := proportion(lot.Original, refunded, total)
		target = min(target, lot.Original-lot.Refunded)
		if target <= 0 {
			return nil
		}
		w, err := mustWallet(s.tx, pid, WalletRef{ID: lot.WalletID})
		if err != nil {
			return err
		}
		take := min(target, lot.Remaining-lot.Held)
		ref := fmt.Sprintf("billing:invoice:%d:%s", invoiceID, key)
		if take > 0 {
			eid, err := s.entry(w, entryIn{kind: "void", amount: take, dA: -take, lotID: lot.ID, idem: "billing:refund:" + key, actor: "billing",
				meta: map[string]any{"invoice_id": invoiceID, "refunded_cents": refunded, "reason": "purchase refunded"}})
			if err != nil {
				return err
			}
			if err := s.lotSpend(eid, lot.ID, take, 0); err != nil {
				return err
			}
		}
		if _, err := s.tx.Exec(`UPDATE credit_lots SET refunded = refunded + ?, status = CASE WHEN remaining = 0 THEN 'voided' ELSE status END WHERE id=?`, target, lot.ID); err != nil {
			return err
		}
		if short := target - take; short > 0 {
			return s.shortfall(w, "refund_spent", short, ref)
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.finish(ctx, s)
	return nil
}

// proportion returns original * part / whole without int64 overflow.
func proportion(original, part, whole int64) int64 {
	n := new(big.Int).Mul(big.NewInt(original), big.NewInt(part))
	n.Quo(n, big.NewInt(whole))
	if !n.IsInt64() {
		return original
	}
	return n.Int64()
}

// ─── Auto-reload ─────────────────────────────────────────────────────

type AutoReload struct {
	Enabled    bool  `json:"enabled"`
	Amount     int64 `json:"amount,omitempty"`
	PriceCents int64 `json:"price_cents,omitempty"`
}

func autoReloadSettings(w *Wallet) AutoReload {
	var meta struct {
		AutoReload AutoReload `json:"auto_reload"`
	}
	_ = json.Unmarshal(w.Metadata, &meta)
	return meta.AutoReload
}

func parseAutoReload(in map[string]any, w *Wallet) (AutoReload, error) {
	ar := AutoReload{Enabled: boolArg(in, "enabled")}
	amount, _, err := amountArg(in, "amount")
	if err != nil {
		return ar, err
	}
	price, _, err := amountArg(in, "price_cents")
	if err != nil {
		return ar, err
	}
	if !ar.Enabled && amount == 0 && price == 0 {
		return ar, nil
	}
	if ar.Amount, ar.PriceCents, _, err = resolvePricing(w.Unit, "", amount, price); err != nil {
		return ar, fmt.Errorf("auto_reload: %w", err)
	}
	return ar, nil
}

// autoReload charges the saved card off-session when a wallet crosses its
// low threshold. A partial unique index allows one attempt in flight per
// wallet; invoice.paid adds the lot like any other top-up.
func (a *App) autoReload(ctx *sdk.AppCtx, walletID int64) {
	var w *Wallet
	var t *Topup
	_, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		w, err = scanWallet(s.tx.QueryRow(`SELECT `+walletCols+` FROM credit_wallets WHERE id=?`, walletID))
		if err != nil {
			return err
		}
		ar := autoReloadSettings(w)
		if !ar.Enabled || w.Status != "active" || w.BillingCustomerID == 0 || balanceState(w) == "ok" {
			return nil
		}
		amt, price, cur, err := resolvePricing(w.Unit, "", ar.Amount, ar.PriceCents)
		if err != nil {
			return err
		}
		t, err = s.insertTopup(w, "auto", amt, price, cur, fmt.Sprintf("auto:%s", s.nowS))
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			t = nil // an auto-reload is already in flight
			return nil
		}
		return err
	})
	if err != nil {
		ctx.Logger().Warn("credits auto-reload skipped", "wallet_id", walletID, "error", err.Error())
		return
	}
	if t == nil {
		return
	}
	fail := func(err error) {
		ctx.Logger().Warn("credits auto-reload failed", "wallet_id", walletID, "topup_id", t.ID, "error", err.Error())
		_, _ = ctx.AppDB().Exec(`UPDATE credit_topups SET status='failed', error=?, updated_at=? WHERE id=? AND status='pending'`, err.Error(), fmtTS(nowFunc()), t.ID)
	}
	invoiceID, err := a.ensureTopupInvoice(ctx, w.ProjectID, w, t, "")
	if err != nil {
		fail(err)
		return
	}
	var collected map[string]any
	if err := ctx.WithProject(w.ProjectID).PlatformAPI().CallAppResult("billing", "invoices_collect", map[string]any{
		"invoice_id": invoiceID, "idempotency_key": fmt.Sprintf("credits-autoreload-%d", t.ID),
	}, &collected); err != nil {
		fail(err)
	}
}

// ─── Worker ──────────────────────────────────────────────────────────

const autoReloadTimeout = 24 * time.Hour

func (a *App) runExpiry(_ context.Context, ctx *sdk.AppCtx) error {
	pid := ctx.CurrentProject()
	holds, lots, events, reload, err := newLedger(ctx.AppDB()).ExpireDue(pid)
	for _, ev := range events {
		ctx.EmitWithProject(ev.Topic, ev.ProjectID, ev.Data)
	}
	for _, id := range reload {
		walletID := id
		runAsync(func() { a.autoReload(ctx, walletID) })
	}
	if err != nil {
		return err
	}
	// An auto-reload whose invoice never got paid stops blocking the next one.
	cutoff := fmtTS(nowFunc().Add(-autoReloadTimeout))
	if _, err := ctx.AppDB().Exec(`UPDATE credit_topups SET status='failed', error='not paid within 24h', updated_at=? WHERE kind='auto' AND status='pending' AND created_at < ?`, fmtTS(nowFunc()), cutoff); err != nil {
		return err
	}
	if holds+lots > 0 {
		ctx.Logger().Info("credits expiry", "holds_released", holds, "lots_expired", lots)
	}
	return nil
}
