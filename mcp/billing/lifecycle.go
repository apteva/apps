package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
)

func (a *App) toolInvoicesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := tx.Exec("UPDATE invoices SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND status='draft' AND deleted_at IS NULL", id, pid)
	if err != nil {
		return nil, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return nil, errors.New("only an existing draft can be deleted")
	}
	if err = writeAuditTx(tx, id, callerActor(args), "delete", nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}
func (a *App) toolInvoicesResumeCollection(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := tx.Exec(`UPDATE invoices SET collection_hold=0 WHERE id=? AND project_id=? AND status IN ('open','uncollectible') AND NOT EXISTS(SELECT 1 FROM billing_refund_requests WHERE invoice_id=? AND status IN ('pending','submitted'))`, id, pid, id)
	if err != nil {
		return nil, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return nil, errors.New("invoice cannot resume collection while a refund is pending or no balance is due")
	}
	if err = writeAuditTx(tx, id, callerActor(args), "resume_collection", nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"collection_hold": false}, nil
}
func (a *App) toolInvoicesCancelPayment(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	iid := int64Arg(args, "invoice_id")
	var opID string
	err = ctx.AppDB().QueryRow(`SELECT l.operation_id FROM billing_payment_locks l JOIN invoices i ON i.id=l.invoice_id WHERE i.id=? AND i.project_id=?`, iid, pid).Scan(&opID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"active": false}, nil
	}
	if err != nil {
		return nil, err
	}
	var kind, id string
	var conn int64
	err = ctx.AppDB().QueryRow("SELECT kind,provider_id,connection_id FROM billing_provider_operations WHERE id=?", opID).Scan(&kind, &id, &conn)
	if err != nil {
		return nil, errors.New("legacy or unknown provider operation must be reconciled before cancellation")
	}
	if id == "" {
		return nil, errors.New("provider outcome unknown; reconciliation must recover its identity before cancellation")
	}
	b, err := requireProcessor(ctx)
	if err != nil {
		return nil, err
	}
	if conn != b.ConnectionID {
		return nil, errors.New("restore original processor connection before canceling")
	}
	get, idField := operationGetTool(kind)
	var result map[string]any
	if err = executeStripeRaw(ctx, b, get, map[string]any{idField: id}, &result); err != nil {
		return nil, err
	}
	status := strArg(result, "status")
	if status == "succeeded" || strArg(result, "payment_status") == "paid" {
		raw, _ := json.Marshal(result)
		if kind == "create_payment_intent" {
			err = a.handlePaymentIntent(ctx, raw)
		} else {
			err = a.handleCheckoutCompleted(ctx, raw)
		}
		if err != nil {
			return nil, err
		}
		return nil, errors.New("payment already succeeded and has been reconciled")
	}
	cancelTool := "cancel_payment_intent"
	terminal := "canceled"
	if kind == "create_checkout_session" {
		cancelTool = "expire_checkout_session"
		terminal = "expired"
	}
	if status != terminal {
		if err = executeStripeRaw(ctx, b, cancelTool, map[string]any{idField: id}, &result); err != nil {
			return nil, err
		}
		if strArg(result, "status") != terminal {
			return nil, errors.New("provider did not confirm cancellation")
		}
	}
	if err = finishProviderOperation(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	if _, err = ctx.AppDB().Exec("UPDATE billing_checkout_sessions SET status='expired' WHERE provider_session_id=? AND status<>'completed'", id); err != nil {
		return nil, err
	}
	if _, err = ctx.AppDB().Exec("UPDATE billing_collection_attempts SET status='failed',error_code='canceled' WHERE provider_payment_intent_id=? AND status<>'succeeded'", id); err != nil {
		return nil, err
	}
	return map[string]any{"canceled": true, "provider_id": id}, nil
}
func (a *App) handleHTTPLifecycle(w http.ResponseWriter, r *http.Request, action string) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args := map[string]any{"_project_id": pid, "invoice_id": pathIntSegment(r.URL.Path, "/invoices/", 0)}
	var out any
	switch action {
	case "delete":
		out, err = a.toolInvoicesDelete(getAppCtx(r), args)
	case "cancel-payment":
		out, err = a.toolInvoicesCancelPayment(getAppCtx(r), args)
	case "resume-collection":
		out, err = a.toolInvoicesResumeCollection(getAppCtx(r), args)
	default:
		err = fmt.Errorf("unknown action")
	}
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, out)
}
