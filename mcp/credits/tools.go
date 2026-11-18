package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Wallets ─────────────────────────────────────────────────────────

func (a *App) toolWalletEnsure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	ref := walletRefFrom(args, "")
	if ref.SubjectID == "" || ref.Unit == "" {
		return nil, errors.New("subject_id and unit required")
	}
	low, err := optionalInt(args, "low_threshold")
	if err != nil {
		return nil, err
	}
	var w *Wallet
	var created bool
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		w, created, err = s.ensureWallet(pid, WalletRef{SubjectType: ref.SubjectType, SubjectID: ref.SubjectID, Unit: ref.Unit}, walletOpts{LowThreshold: low, BillingCustomerID: int64Arg(args, "billing_customer_id"), Metadata: args["metadata"]})
		return err
	})
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return map[string]any{"wallet": w, "created": created}, nil
}

func (a *App) toolWalletGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppReadDB()
	w, err := findWallet(db, pid, walletRefFrom(args, ""))
	if err != nil {
		return nil, err
	}
	if w == nil {
		return map[string]any{"wallet": nil}, nil
	}
	caps, err := listCaps(db, w.ID, "", false, nowFunc())
	if err != nil {
		return nil, err
	}
	return map[string]any{"wallet": w, "caps": caps, "auto_reload": autoReloadSettings(w)}, nil
}

func (a *App) toolWalletUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	low, err := optionalInt(args, "low_threshold")
	if err != nil {
		return nil, err
	}
	if low != nil && *low < 0 {
		return nil, errors.New("low_threshold must not be negative")
	}
	var w *Wallet
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		if w, err = mustWallet(s.tx, pid, walletRefFrom(args, "")); err != nil {
			return err
		}
		meta := map[string]any{}
		_ = json.Unmarshal(w.Metadata, &meta)
		if patch, ok := args["metadata"].(map[string]any); ok {
			for k, v := range patch {
				meta[k] = v
			}
		}
		if ar, ok := args["auto_reload"].(map[string]any); ok {
			cfg, err := parseAutoReload(ar, w)
			if err != nil {
				return err
			}
			meta["auto_reload"] = cfg
		}
		if low != nil {
			w.LowThreshold = *low
		}
		if _, ok := args["billing_customer_id"]; ok {
			w.BillingCustomerID = int64Arg(args, "billing_customer_id")
		}
		raw, _ := json.Marshal(meta)
		w.Metadata = raw
		if _, err := s.tx.Exec(`UPDATE credit_wallets SET low_threshold=?, billing_customer_id=?, metadata=?, updated_at=? WHERE id=?`, w.LowThreshold, w.BillingCustomerID, string(raw), s.nowS, w.ID); err != nil {
			return err
		}
		s.touch(w)
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return map[string]any{"wallet": w, "auto_reload": autoReloadSettings(w)}, nil
}

func (a *App) toolWalletsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	where := []string{"project_id=?"}
	q := []any{pid}
	for _, f := range []string{"subject_type", "subject_id", "unit", "status"} {
		if v := strArg(args, f); v != "" {
			where = append(where, f+"=?")
			q = append(q, v)
		}
	}
	q = append(q, clampLimit(int64Arg(args, "limit"), 200))
	rows, err := ctx.AppReadDB().Query(`SELECT `+walletCols+` FROM credit_wallets WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, q...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Wallet{}
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return map[string]any{"wallets": out, "count": len(out)}, rows.Err()
}

func (a *App) toolWalletFreeze(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setWalletStatus(ctx, args, "frozen")
}

func (a *App) toolWalletUnfreeze(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setWalletStatus(ctx, args, "active")
}

func (a *App) setWalletStatus(ctx *sdk.AppCtx, args map[string]any, status string) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	var w *Wallet
	_, err = newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		w, err = s.setStatus(pid, walletRefFrom(args, ""), status)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"wallet": w}, nil
}

// ─── Credit ──────────────────────────────────────────────────────────

func (a *App) toolGrant(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	source := strArg(args, "source")
	switch source {
	case "promo", "reward", "adjustment":
	case "purchase":
		return nil, errors.New("purchase lots are created only when billing reports a top-up invoice paid; use credits_topup_create")
	default:
		return nil, errors.New("source must be promo, reward or adjustment")
	}
	amount, err := requiredAmount(args, "amount")
	if err != nil {
		return nil, err
	}
	prio, err := optionalInt(args, "priority")
	if err != nil {
		return nil, err
	}
	exp, err := expiresFrom(args)
	if err != nil {
		return nil, err
	}
	var lot *Lot
	var w *Wallet
	var deduped bool
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		if w, _, err = s.ensureWallet(pid, walletRefFrom(args, ""), walletOpts{}); err != nil {
			return err
		}
		lot, deduped, err = s.grantLot(w, grantIn{Source: source, SourceRef: strArg(args, "source_ref"), Amount: amount, Priority: prio, ExpiresAt: exp, Metadata: args["metadata"], Actor: actorFrom(args)})
		return err
	})
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return map[string]any{"lot": lot, "wallet": w, "deduped": deduped}, nil
}

// ─── Spending ────────────────────────────────────────────────────────

func reserveInFrom(args map[string]any) (ReserveIn, error) {
	amount, err := requiredAmount(args, "amount")
	if err != nil {
		return ReserveIn{}, err
	}
	ttl, _, err := amountArg(args, "ttl_seconds")
	if err != nil {
		return ReserveIn{}, err
	}
	return ReserveIn{
		Wallet: walletRefFrom(args, ""), Amount: amount, Ref: referenceFrom(args), Scope: strArg(args, "scope"),
		TTL: time.Duration(ttl) * time.Second, Metadata: args["metadata"], Actor: actorFrom(args),
	}, nil
}

func (a *App) toolReserve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	in, err := reserveInFrom(args)
	if err != nil {
		return nil, err
	}
	out, s, err := newLedger(ctx.AppDB()).Reserve(pid, in)
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return out, nil
}

func (a *App) toolCommit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	holdID := int64Arg(args, "hold_id")
	if holdID == 0 {
		return nil, errors.New("hold_id required")
	}
	amount, given, err := amountArg(args, "amount")
	if err != nil {
		return nil, err
	}
	if !given {
		h, err := getHold(ctx.AppDB(), pid, holdID)
		if err != nil {
			return nil, err
		}
		amount = h.Amount
	}
	out, s, err := newLedger(ctx.AppDB()).Commit(pid, holdID, amount, actorFrom(args))
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return out, nil
}

func (a *App) toolRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	holdID := int64Arg(args, "hold_id")
	if holdID == 0 {
		return nil, errors.New("hold_id required")
	}
	out, s, err := newLedger(ctx.AppDB()).Release(pid, holdID, actorFrom(args))
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return out, nil
}

func (a *App) toolSpend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	in, err := reserveInFrom(args)
	if err != nil {
		return nil, err
	}
	out, s, err := newLedger(ctx.AppDB()).Spend(pid, in)
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return out, nil
}

// ─── Admin ───────────────────────────────────────────────────────────

func (a *App) toolAdjust(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	amount, err := requiredAmount(args, "amount")
	if err != nil {
		return nil, err
	}
	prio, err := optionalInt(args, "priority")
	if err != nil {
		return nil, err
	}
	exp, err := expiresFrom(args)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		out, err = s.adjust(pid, AdjustIn{Wallet: walletRefFrom(args, ""), Amount: amount, Reason: strArg(args, "reason"), Idem: strArg(args, "idempotency_key"), Actor: actorFrom(args), ExpiresAt: exp, Priority: prio})
		return err
	})
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return out, nil
}

func (a *App) toolTransfer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	amount, err := requiredAmount(args, "amount")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	s, err := newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		out, err = s.transfer(pid, TransferIn{From: walletRefFrom(args, "from_"), To: walletRefFrom(args, "to_"), Amount: amount, Reason: strArg(args, "reason"), Idem: strArg(args, "idempotency_key"), Actor: actorFrom(args)})
		return err
	})
	if err != nil {
		return nil, err
	}
	a.finish(ctx, s)
	return out, nil
}

// ─── Reading ─────────────────────────────────────────────────────────

func (a *App) readWallet(ctx *sdk.AppCtx, args map[string]any) (*Wallet, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	return mustWallet(ctx.AppReadDB(), pid, walletRefFrom(args, ""))
}

func (a *App) toolEntriesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.readWallet(ctx, args)
	if err != nil {
		return nil, err
	}
	where := "wallet_id=?"
	q := []any{w.ID}
	if before := int64Arg(args, "before_id"); before > 0 {
		where += " AND id < ?"
		q = append(q, before)
	}
	if kind := strArg(args, "kind"); kind != "" {
		where += " AND kind=?"
		q = append(q, kind)
	}
	limit := clampLimit(int64Arg(args, "limit"), 200)
	q = append(q, limit)
	rows, err := ctx.AppReadDB().Query(`SELECT `+entryCols+` FROM credit_entries WHERE `+where+` ORDER BY id DESC LIMIT ?`, q...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	resp := map[string]any{"wallet": w, "entries": out, "count": len(out)}
	if int64(len(out)) == limit {
		resp["next_before_id"] = out[len(out)-1].ID
	}
	return resp, rows.Err()
}

func (a *App) toolHoldsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.readWallet(ctx, args)
	if err != nil {
		return nil, err
	}
	where := "h.wallet_id=?"
	q := []any{w.ID}
	if st := strArg(args, "status"); st != "" {
		where += " AND h.status=?"
		q = append(q, st)
	}
	q = append(q, clampLimit(int64Arg(args, "limit"), 200))
	rows, err := ctx.AppReadDB().Query(`SELECT `+holdCols+` FROM credit_holds h WHERE `+where+` ORDER BY h.id DESC LIMIT ?`, q...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Hold{}
	for rows.Next() {
		h, err := scanHold(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return map[string]any{"wallet": w, "holds": out, "count": len(out)}, rows.Err()
}

func (a *App) toolLotsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.readWallet(ctx, args)
	if err != nil {
		return nil, err
	}
	where := "wallet_id=?"
	q := []any{w.ID}
	if st := strArg(args, "status"); st != "" {
		where += " AND status=?"
		q = append(q, st)
	}
	q = append(q, clampLimit(int64Arg(args, "limit"), 200))
	rows, err := ctx.AppReadDB().Query(`SELECT `+lotCols+` FROM credit_lots WHERE `+where+` ORDER BY (status='active') DESC, priority ASC, (expires_at IS NULL) ASC, expires_at ASC, id ASC LIMIT ?`, q...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Lot{}
	for rows.Next() {
		l, err := scanLot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return map[string]any{"wallet": w, "lots": out, "count": len(out)}, rows.Err()
}

func (a *App) toolCapsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	limit, err := requiredAmount(args, "limit_amount")
	if err != nil {
		return nil, err
	}
	var c *CapView
	_, err = newLedger(ctx.AppDB()).write(func(s *txState) error {
		var err error
		c, err = s.setCap(pid, walletRefFrom(args, ""), strArg(args, "scope"), strArg(args, "window"), limit, args["metadata"])
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"cap": c}, nil
}

func (a *App) toolCapsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.readWallet(ctx, args)
	if err != nil {
		return nil, err
	}
	caps, err := listCaps(ctx.AppReadDB(), w.ID, "", false, nowFunc())
	if err != nil {
		return nil, err
	}
	return map[string]any{"wallet_id": w.ID, "caps": caps}, nil
}

type Shortfall struct {
	ID         int64  `json:"id"`
	WalletID   int64  `json:"wallet_id"`
	Kind       string `json:"kind"`
	Amount     int64  `json:"amount"`
	Reference  string `json:"reference"`
	Status     string `json:"status"`
	Note       string `json:"note,omitempty"`
	CreatedAt  string `json:"created_at"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

func (a *App) toolShortfallsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	where := "w.project_id=?"
	q := []any{pid}
	ref := walletRefFrom(args, "")
	if ref.ID > 0 || ref.SubjectID != "" {
		w, err := mustWallet(ctx.AppReadDB(), pid, ref)
		if err != nil {
			return nil, err
		}
		where += " AND s.wallet_id=?"
		q = append(q, w.ID)
	}
	status := firstNonEmpty(strArg(args, "status"), "open")
	if status != "all" {
		where += " AND s.status=?"
		q = append(q, status)
	}
	q = append(q, clampLimit(int64Arg(args, "limit"), 200))
	rows, err := ctx.AppReadDB().Query(`SELECT s.id, s.wallet_id, s.kind, s.amount, s.reference, s.status, s.note, s.created_at, COALESCE(s.resolved_at,'')
		FROM credit_shortfalls s JOIN credit_wallets w ON w.id=s.wallet_id WHERE `+where+` ORDER BY s.id DESC LIMIT ?`, q...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Shortfall{}
	for rows.Next() {
		var sf Shortfall
		if err := rows.Scan(&sf.ID, &sf.WalletID, &sf.Kind, &sf.Amount, &sf.Reference, &sf.Status, &sf.Note, &sf.CreatedAt, &sf.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, sf)
	}
	return map[string]any{"shortfalls": out, "count": len(out)}, rows.Err()
}

func (a *App) toolShortfallResolve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	_, err = newLedger(ctx.AppDB()).write(func(s *txState) error {
		res, err := s.tx.Exec(`UPDATE credit_shortfalls SET status='resolved', note=?, resolved_at=?
			WHERE id=? AND status='open' AND wallet_id IN (SELECT id FROM credit_wallets WHERE project_id=?)`, strArg(args, "note"), s.nowS, id, pid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("open shortfall not found")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "status": "resolved"}, nil
}

func (a *App) toolReconcile(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	// Read on the writer so no write can land between the checks.
	writeMu.Lock()
	defer writeMu.Unlock()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	issues, n, err := reconcile(tx, pid, int64Arg(args, "wallet_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": len(issues) == 0, "wallets_checked": n, "issues": issues}, nil
}

func (a *App) toolLiability(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(ctx, args)
	if err != nil {
		return nil, err
	}
	asOf := fmtTS(nowFunc())
	if raw := strArg(args, "as_of"); raw != "" {
		if d, err := time.Parse("2006-01-02", raw); err == nil {
			asOf = fmtTS(d.Add(24*time.Hour - time.Second))
		} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
			asOf = fmtTS(t)
		} else {
			return nil, errors.New("as_of must be YYYY-MM-DD or RFC3339")
		}
	}
	rows, err := liability(ctx.AppReadDB(), pid, asOf)
	if err != nil {
		return nil, err
	}
	return map[string]any{"as_of": asOf, "units": rows, "note": "Unspent paid credit (purchase lots, and credit transferred from them). Currency units are micro-units: divide by 1000000."}, nil
}
