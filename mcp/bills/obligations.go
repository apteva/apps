package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strconv"
	"strings"
)

func payloadHash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }

// This generic API never accepts paid-on-create or schedules a payment.
func (a *App) toolBillsCreateObligation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	key := strArg(args, "source_key")
	if key == "" || len(key) > 500 {
		return nil, errors.New("source_key required (maximum 500 characters)")
	}
	for _, k := range []string{"paid", "status", "amount_paid_cents"} {
		if _, ok := args[k]; ok {
			return nil, fmt.Errorf("%s is not allowed for an obligation", k)
		}
	}
	allowed := map[string]bool{"_project_id": true, "source_key": true, "vendor_id": true, "currency": true, "line_items": true, "due_date": true, "notes": true, "metadata": true, "category": true, "existing_bill_id": true}
	clean := map[string]any{}
	for k, v := range args {
		if allowed[k] {
			clean[k] = v
		}
	}
	hash := payloadHash(clean)
	if b, e := billBySource(ctx.AppDB(), pid, key, hash); e != nil || b != nil {
		return map[string]any{"bill": b, "was_existing": true}, e
	}
	if existing := int64Arg(args, "existing_bill_id"); existing > 0 {
		rawItems, ok := args["line_items"].([]any)
		if !ok {
			return nil, errors.New("line_items required")
		}
		items, err := normaliseLineItems(rawItems, 0)
		if err != nil {
			return nil, err
		}
		_, _, total := computeTotals(items)
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		var actualVendor, actualTotal int64
		var actualCurrency, status string
		if err = tx.QueryRow(`SELECT vendor_id,total_cents,currency,status FROM bills WHERE id=? AND project_id=? AND deleted_at IS NULL`, existing, pid).Scan(&actualVendor, &actualTotal, &actualCurrency, &status); err != nil {
			return nil, err
		}
		if actualVendor != int64Arg(args, "vendor_id") || actualTotal != total || actualCurrency != strings.ToUpper(strArg(args, "currency")) || status == "void" {
			return nil, errors.New("existing bill does not match the approved payee, amount or currency")
		}
		if _, err = tx.Exec(`INSERT INTO bill_sources(project_id,source_key,payload_hash,bill_id) VALUES(?,?,?,?)`, pid, key, hash, existing); err != nil {
			return nil, err
		}
		if err = writeAuditTx(tx, existing, callerActor(args), "link_obligation", map[string]any{"source_key": key}); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		b, err := billBySource(ctx.AppDB(), pid, key, hash)
		return map[string]any{"bill": b, "was_existing": true}, err
	}

	clean["_source_key"] = key
	clean["_source_hash"] = hash
	out, err := a.toolBillsCreate(ctx, clean)
	if err != nil {
		if b, e := billBySource(ctx.AppDB(), pid, key, hash); e != nil || b != nil {
			return map[string]any{"bill": b, "was_existing": true}, e
		}
	}
	return out, err
}
func billBySource(db *sql.DB, pid, key, hash string) (*Bill, error) {
	var id int64
	var stored string
	err := db.QueryRow(`SELECT bill_id,payload_hash FROM bill_sources WHERE project_id=? AND source_key=?`, pid, key).Scan(&id, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != "" && stored != hash {
		return nil, errors.New("source identity already exists with different financial details")
	}
	b, err := dbBillGetByID(db, pid, id)
	if err == nil && b != nil {
		err = loadBillChildren(db, pid, b)
	}
	return b, err
}
func (a *App) toolBillsGetObligation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	b, err := billBySource(ctx.AppDB(), pid, strArg(args, "source_key"), "")
	if err == nil && b == nil {
		err = errors.New("obligation bill not found")
	}
	return map[string]any{"bill": b}, err
}
func (a *App) toolBillsAdjust(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "bill_id")
	amt := int64Arg(args, "amount_minor")
	kind := strArg(args, "kind")
	key := strArg(args, "request_key")
	reason := strings.TrimSpace(strArg(args, "reason"))
	if id <= 0 || amt <= 0 || key == "" || reason == "" || (kind != "credit" && kind != "refund" && kind != "reversal") {
		return nil, errors.New("bill_id, positive amount_minor, request_key, reason and kind credit/refund/reversal required")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var oldID, oldAmt int64
	var oldKind, oldReason string
	e := tx.QueryRow(`SELECT bill_id,amount_minor,kind,reason FROM bill_adjustments WHERE project_id=? AND request_key=?`, pid, key).Scan(&oldID, &oldAmt, &oldKind, &oldReason)
	if e == nil {
		if oldID != id || oldAmt != amt || oldKind != kind || oldReason != reason {
			return nil, errors.New("adjustment request conflicts with existing record")
		}
		tx.Rollback()
		b, e := dbBillGetByID(ctx.AppDB(), pid, id)
		if e == nil {
			e = loadBillChildren(ctx.AppDB(), pid, b)
		}
		return map[string]any{"bill": b, "was_existing": true}, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	var total, paid, vendor, credit int64
	var status, currency string
	err = tx.QueryRow(`SELECT total_cents,amount_paid_cents,vendor_id,status,currency FROM bills WHERE id=? AND project_id=? AND deleted_at IS NULL`, id, pid).Scan(&total, &paid, &vendor, &status, &currency)
	if err != nil {
		return nil, err
	}
	if status == "void" {
		return nil, errors.New("cannot adjust a void bill")
	}
	if err = tx.QueryRow(`SELECT COALESCE(SUM(amount_minor),0) FROM bill_adjustments WHERE bill_id=? AND kind='credit'`, id).Scan(&credit); err != nil {
		return nil, err
	}
	var payment any
	if kind == "credit" {
		if amt > total-credit {
			return nil, errors.New("credit exceeds remaining bill total")
		}
		credit += amt
	} else {
		if amt > paid {
			return nil, errors.New("refund or reversal exceeds recorded net payments")
		}
		res, e := tx.Exec(`INSERT INTO bill_payments(project_id,bill_id,vendor_id,amount_cents,currency,method,sent_at,notes) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP,?)`, pid, id, vendor, -amt, currency, kind, reason)
		if e != nil {
			return nil, e
		}
		payment, _ = res.LastInsertId()
		paid -= amt
	}
	_, err = tx.Exec(`INSERT INTO bill_adjustments(project_id,bill_id,request_key,kind,amount_minor,reason,actor,payment_id) VALUES(?,?,?,?,?,?,?,?)`, pid, id, key, kind, amt, reason, callerActor(args), payment)
	if err != nil {
		return nil, err
	}
	if status == "paid" && paid < total-credit {
		status = "approved"
	}
	// A credit is settlement by adjustment, not evidence of payment.
	_, err = tx.Exec(`UPDATE bills SET amount_paid_cents=?,status=?,paid_at=CASE WHEN ?='paid' THEN paid_at ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, paid, status, status, id)
	if err != nil {
		return nil, err
	}
	if err = writeAuditTx(tx, id, callerActor(args), kind, map[string]any{"amount_minor": amt, "reason": reason, "request_key": key}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	b, err := dbBillGetByID(ctx.AppDB(), pid, id)
	if err == nil {
		err = loadBillChildren(ctx.AppDB(), pid, b)
	}
	if err == nil {
		emitBill(ctx, "bill.adjusted", b)
	}
	return map[string]any{"bill": b}, err
}
func (a *App) obligationTools() []sdk.Tool {
	props := map[string]any{"existing_bill_id": map[string]any{"type": "integer"}, "source_key": map[string]any{"type": "string"}, "vendor_id": map[string]any{"type": "integer"}, "currency": map[string]any{"type": "string"}, "line_items": map[string]any{"type": "array"}, "metadata": map[string]any{"type": "object"}, "due_date": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"}, "category": map[string]any{"type": "string"}}
	return []sdk.Tool{
		{Name: "bills_create_obligation", Description: "Create or retrieve an unapproved supplier bill using an immutable source_key. Replays cannot pay or duplicate bills; changed payloads conflict.", InputSchema: schemaObject(props, []string{"source_key", "vendor_id", "currency", "line_items"}), Handler: a.toolBillsCreateObligation},
		{Name: "bills_get_obligation", Description: "Read a bill, balance, documents and payments by its immutable source_key.", InputSchema: schemaObject(map[string]any{"source_key": map[string]any{"type": "string"}}, []string{"source_key"}), Handler: a.toolBillsGetObligation},
		{Name: "bills_record_adjustment", Description: "Record an explicit credit, received refund, or payment reversal without rewriting financial history. Does not move money. request_key prevents duplicate adjustments.", InputSchema: schemaObject(map[string]any{"bill_id": map[string]any{"type": "integer"}, "amount_minor": map[string]any{"type": "integer"}, "kind": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}, "request_key": map[string]any{"type": "string"}}, []string{"bill_id", "amount_minor", "kind", "reason", "request_key"}), Handler: a.toolBillsAdjust},
	}
}

func (a *App) handleHTTPBillAdjustment(w http.ResponseWriter, r *http.Request) {
	pid, e := resolveProjectFromRequest(r)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/bills/"), "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	var args map[string]any
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&args); e != nil || args == nil {
		httpErr(w, 400, "invalid adjustment")
		return
	}
	args["_project_id"] = pid
	args["bill_id"] = id
	args["_caller"] = actorFromRequest(r)
	out, e := a.toolBillsAdjust(getAppCtx(r), args)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, out)
}

func appendBillDocument(ctx *sdk.AppCtx, pid string, billID, fileID int64, actor string) (any, error) {
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var status string
	if e = tx.QueryRow(`SELECT status FROM bills WHERE id=? AND project_id=? AND deleted_at IS NULL`, billID, pid).Scan(&status); e != nil {
		return nil, e
	}
	if status == "void" {
		return nil, errors.New("cannot attach to a void bill")
	}
	res, e := tx.Exec(`INSERT OR IGNORE INTO bill_documents(bill_id,file_id,label) VALUES(?,?,'Supporting document')`, billID, fileID)
	if e != nil {
		return nil, e
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if e = writeAuditTx(tx, billID, actor, "add_document", map[string]any{"file_id": fileID}); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return map[string]any{"bill_id": billID, "file_id": fileID}, nil
}
