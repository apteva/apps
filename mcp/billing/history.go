package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
)

func (a *App) toolInvoicesHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	inv, err := dbInvoiceGetByID(ctx.AppReadDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, errors.New("invoice not found")
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	offset := max(intArg(args, "offset", 0), 0)
	payments, err := dbPaymentList(ctx.AppReadDB(), pid, paymentFilters{invoiceID: id, limit: limit + 1, offset: offset})
	if err != nil {
		return nil, err
	}
	morePayments := len(payments) > limit
	if morePayments {
		payments = payments[:limit]
	}
	rows, err := ctx.AppReadDB().Query(`SELECT id,invoice_id,actor,action,details,created_at FROM invoice_audit_log WHERE invoice_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, id, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	audit := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		var raw string
		if err = rows.Scan(&entry.ID, &entry.InvoiceID, &entry.Actor, &entry.Action, &raw, &entry.CreatedAt); err != nil {
			return nil, err
		}
		entry.Details = json.RawMessage(raw)
		audit = append(audit, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	moreAudit := len(audit) > limit
	if moreAudit {
		audit = audit[:limit]
	}
	rows.Close()
	refundRows, err := ctx.AppReadDB().Query(refundSelect()+` WHERE project_id=? AND invoice_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, pid, id, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer refundRows.Close()
	refunds := []*RefundRequest{}
	for refundRows.Next() {
		r, e := scanRefund(refundRows)
		if e != nil {
			return nil, e
		}
		refunds = append(refunds, r)
	}
	if err = refundRows.Err(); err != nil {
		return nil, err
	}
	moreRefunds := len(refunds) > limit
	if moreRefunds {
		refunds = refunds[:limit]
	}
	return map[string]any{"refunds": refunds, "refunds_has_more": moreRefunds, "payments": payments, "audit_log": audit, "payments_has_more": morePayments, "audit_has_more": moreAudit, "next_offset": offset + limit}, nil
}
func (a *App) handleHTTPInvoiceHistory(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	out, err := a.toolInvoicesHistory(getAppCtx(r), map[string]any{"_project_id": pid, "invoice_id": pathIntSegment(r.URL.Path, "/invoices/", 0), "limit": int(atoi64(r.URL.Query().Get("limit"))), "offset": int(atoi64(r.URL.Query().Get("offset")))})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, out)
}
