package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type checkoutInvoice struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	TotalCents      int64  `json:"total_cents"`
	AmountPaidCents int64  `json:"amount_paid_cents"`
	Currency        string `json:"currency"`
	Payments        []struct {
		AmountCents int64 `json:"amount_cents"`
	} `json:"payments"`
}
type ReceiptItem struct {
	CartItem
	NetCents      int64  `json:"net_cents"`
	RefundedCents int64  `json:"refunded_cents"`
	Status        string `json:"status"`
}
type CheckoutReceipt struct {
	Session                *CheckoutSession `json:"session"`
	Items                  []ReceiptItem    `json:"items"`
	UnallocatedRefundCents int64            `json:"unallocated_refund_cents"`
}

func readCheckoutReceipt(ctx *sdk.AppCtx, pid string, id int64) (*CheckoutReceipt, error) {
	s, e := dbCheckoutGet(ctx.AppDB(), pid, id)
	if e != nil || s == nil {
		return nil, firstCheckoutErr(e, errors.New("session not found"))
	}
	cart, e := dbCartGetByID(ctx.AppDB(), pid, s.CartID)
	if e != nil || cart == nil {
		return nil, firstCheckoutErr(e, errors.New("cart not found"))
	}
	r := &CheckoutReceipt{Session: s, Items: []ReceiptItem{}}
	var inv checkoutInvoice
	if s.InvoiceID != nil {
		var out struct {
			Invoice checkoutInvoice `json:"invoice"`
		}
		if e = ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{"_project_id": pid, "id": *s.InvoiceID}, &out); e != nil {
			return nil, e
		}
		inv = out.Invoice
		if inv.ID != *s.InvoiceID || inv.TotalCents != s.TotalCents || !strings.EqualFold(inv.Currency, s.Currency) {
			return nil, errors.New("Billing invoice differs from frozen checkout")
		}
	}
	var totalRefunds int64
	for _, p := range inv.Payments {
		if p.AmountCents < 0 {
			totalRefunds -= p.AmountCents
		}
	}
	// Explicitly allocate the existing checkout discount to the original items;
	// add-on prices never inherit a main-product promotion.
	var baseTotal int64
	for _, it := range cart.Items {
		if int64Arg(mapFromAny(it.Metadata), "order_bump_id") == 0 {
			baseTotal += int64(float64(it.UnitAmountCents) * it.Quantity)
		}
	}
	remainingBase := baseTotal
	remainingDiscount := s.DiscountCents
	for _, it := range cart.Items {
		amount := int64(float64(it.UnitAmountCents) * it.Quantity)
		if int64Arg(mapFromAny(it.Metadata), "order_bump_id") == 0 && baseTotal > 0 {
			d := s.DiscountCents * amount / baseTotal
			if amount == remainingBase {
				d = remainingDiscount
			}
			remainingBase -= amount
			if d > remainingDiscount {
				d = remainingDiscount
			}
			remainingDiscount -= d
			amount -= d
		}
		item := ReceiptItem{CartItem: *it, NetCents: amount, Status: "pending"}
		if e = ctx.AppDB().QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM checkout_item_refunds WHERE project_id=? AND session_id=? AND item_id=? AND status='succeeded'`, pid, id, it.ID).Scan(&item.RefundedCents); e != nil {
			return nil, e
		}
		if totalRefunds >= inv.TotalCents && inv.TotalCents > 0 {
			item.RefundedCents = amount
		}
		wasPaid := s.Status == "paid" || inv.Status == "paid" || inv.AmountPaidCents+totalRefunds >= inv.TotalCents && inv.TotalCents > 0
		if wasPaid {
			item.Status = "paid"
			if item.RefundedCents >= amount {
				item.Status = "refunded"
			} else if item.RefundedCents > 0 {
				item.Status = "partially_refunded"
			}
		}
		if inv.Status == "void" {
			item.Status = "cancelled"
		}
		r.Items = append(r.Items, item)
	}
	allocated := int64(0)
	for _, it := range r.Items {
		allocated += it.RefundedCents
	}
	if totalRefunds > allocated {
		r.UnallocatedRefundCents = totalRefunds - allocated
	}
	return r, nil
}
func checkoutReceipt(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	return readCheckoutReceipt(ctx, pid, int64Arg(args, "session_id"))
}

func refundCheckoutItem(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	id := int64Arg(args, "session_id")
	itemID := int64Arg(args, "item_id")
	key := strArg(args, "idempotency_key")
	if key == "" || len(key) > 120 {
		return nil, errors.New("idempotency_key required (max 120 characters)")
	}
	unlock := lockCheckout(id)
	defer unlock()
	r, e := readCheckoutReceipt(ctx, pid, id)
	if e != nil {
		return nil, e
	}
	var it ReceiptItem
	for _, v := range r.Items {
		if v.ID == itemID {
			it = v
		}
	}
	if it.ID == 0 || r.Session.InvoiceID == nil {
		return nil, errors.New("paid checkout item not found")
	}
	var existingItem, amount int64
	var status string
	e = ctx.AppDB().QueryRow(`SELECT item_id,amount_cents,status FROM checkout_item_refunds WHERE project_id=? AND idempotency_key=?`, pid, key).Scan(&existingItem, &amount, &status)
	if e == nil {
		if existingItem != itemID || (int64Arg(args, "amount_cents") > 0 && int64Arg(args, "amount_cents") != amount) {
			return nil, errors.New("refund key reused for another item or amount")
		}
	} else {
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if it.Status != "paid" && it.Status != "partially_refunded" {
			return nil, errors.New("item has no paid balance")
		}
		if r.UnallocatedRefundCents > 0 {
			return nil, errors.New("allocate external refunds before requesting another item refund")
		}
		var reserved int64
		if e = ctx.AppDB().QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM checkout_item_refunds WHERE project_id=? AND session_id=? AND item_id=? AND status='pending'`, pid, id, itemID).Scan(&reserved); e != nil {
			return nil, e
		}
		amount = int64Arg(args, "amount_cents")
		available := it.NetCents - it.RefundedCents - reserved
		if amount == 0 {
			amount = available
		}
		if amount <= 0 || amount > available {
			return nil, errors.New("refund exceeds this item's remaining paid balance")
		}
		if _, e = ctx.AppDB().Exec(`INSERT INTO checkout_item_refunds(project_id,session_id,item_id,idempotency_key,amount_cents) VALUES(?,?,?,?,?)`, pid, id, itemID, key, amount); e != nil {
			return nil, e
		}
	}
	if status != "succeeded" && status != "failed" {
		var out struct {
			Refund struct {
				Status string `json:"status"`
			} `json:"refund"`
		}
		if e = ctx.PlatformAPI().CallAppResult("billing", "invoices_refund", map[string]any{"_project_id": pid, "invoice_id": *r.Session.InvoiceID, "amount_cents": amount, "idempotency_key": fmt.Sprintf("checkout-item:%d:%s", id, key)}, &out); e != nil {
			return nil, e
		}
		status = out.Refund.Status
		if status != "succeeded" && status != "failed" {
			status = "pending"
		}
		if _, e = ctx.AppDB().Exec(`UPDATE checkout_item_refunds SET status=? WHERE project_id=? AND idempotency_key=?`, status, pid, key); e != nil {
			return nil, e
		}
	}
	receipt, e := readCheckoutReceipt(ctx, pid, id)
	if e == nil {
		emitSession(ctx, "checkout.refunded", r.Session)
	}
	return map[string]any{"status": status, "receipt": receipt}, e
}

func reconcileItemRefunds(ctx *sdk.AppCtx, pid string, sessionID int64) error {
	rows, e := ctx.AppDB().Query(`SELECT item_id,idempotency_key,amount_cents FROM checkout_item_refunds WHERE project_id=? AND session_id=? AND status='pending'`, pid, sessionID)
	if e != nil {
		return e
	}
	args := []map[string]any{}
	for rows.Next() {
		var id, amount int64
		var key string
		if e = rows.Scan(&id, &key, &amount); e != nil {
			rows.Close()
			return e
		}
		args = append(args, map[string]any{"_project_id": pid, "session_id": sessionID, "item_id": id, "amount_cents": amount, "idempotency_key": key})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range args {
		if _, e = refundCheckoutItem(ctx, a); e != nil {
			return e
		}
	}
	return nil
}

func (a *App) handleCheckoutRefund(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	pid := event.ProjectID
	if pid == "" {
		pid = ctx.CurrentProject()
	}
	id := int64Arg(event.Data, "id")
	if id == 0 {
		id = int64Arg(event.Data, "invoice_id")
	}
	s, e := dbCheckoutGetByInvoice(ctx.AppDB(), pid, id)
	if e != nil || s == nil {
		return e
	}
	if e = reconcileItemRefunds(ctx, pid, s.ID); e != nil {
		return e
	}
	emitSession(ctx.WithProject(pid), "checkout.refunded", s)
	return nil
}

func (a *App) httpCheckoutReceipt(w http.ResponseWriter, r *http.Request) {
	if caller := sdk.CallerFrom(r.Context()); caller != nil && caller.SubjectType == "user" {
		httpErr(w, 403, "use the selling app buyer API")
		return
	}
	pid, e := resolveProjectFromRequest(r)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sessions/"), "/")
	id, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		httpErr(w, 400, "invalid session")
		return
	}
	args := map[string]any{}
	var result any
	if r.Method == http.MethodPost {
		if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&args); e != nil {
			httpErr(w, 400, "invalid refund")
			return
		}
		args["_project_id"] = pid
		args["session_id"] = id
		result, e = refundCheckoutItem(getAppCtx(r), args)
	} else {
		result, e = readCheckoutReceipt(getAppCtx(r), pid, id)
	}
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, result)
}
