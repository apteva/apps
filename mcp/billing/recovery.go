package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Recover claims that were committed immediately before a process exit, before
// the provider operation could be persisted. These rows have never been sent.
func (a *App) recoverUnsentClaims(c context.Context, ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT project_id,invoice_id,idempotency_key,payment_method_id FROM billing_collection_attempts ca WHERE status='pending' AND COALESCE(provider_payment_intent_id,'')='' AND NOT EXISTS(SELECT 1 FROM billing_provider_operations op WHERE json_extract(op.request_json,'$.metadata.apteva_collection_attempt')=CAST(ca.id AS TEXT)) LIMIT 50`)
	if err != nil {
		return err
	}
	var claims []map[string]any
	for rows.Next() {
		var pid, key string
		var iid, pm int64
		if err = rows.Scan(&pid, &iid, &key, &pm); err != nil {
			rows.Close()
			return err
		}
		claims = append(claims, map[string]any{"_project_id": pid, "invoice_id": iid, "idempotency_key": key, "payment_method_id": pm})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, args := range claims {
		if c.Err() != nil {
			return c.Err()
		}
		if _, err = a.toolInvoicesCollect(ctx.WithProject(strArg(args, "_project_id")), args); err != nil {
			ctx.Logger().Warn("unsent collection recovery", "invoice", args["invoice_id"], "error", err.Error())
		}
	}
	rows, err = ctx.AppDB().Query(`SELECT project_id,invoice_id,idempotency_key FROM billing_refund_requests rr WHERE status='pending' AND provider_refund_id='' AND NOT EXISTS(SELECT 1 FROM billing_provider_operations op WHERE op.kind='create_refund' AND op.project_id=rr.project_id AND op.caller_key=rr.idempotency_key) LIMIT 50`)
	if err != nil {
		return err
	}
	claims = nil
	for rows.Next() {
		var pid, key string
		var iid int64
		if err = rows.Scan(&pid, &iid, &key); err != nil {
			rows.Close()
			return err
		}
		claims = append(claims, map[string]any{"_project_id": pid, "invoice_id": iid, "idempotency_key": key})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, args := range claims {
		if c.Err() != nil {
			return c.Err()
		}
		if _, err = a.toolInvoicesRefund(ctx.WithProject(strArg(args, "_project_id")), args); err != nil {
			ctx.Logger().Warn("unsent refund recovery", "invoice", args["invoice_id"], "error", err.Error())
		}
	}
	return nil
}
func (a *App) recoverLegacyPayments(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT i.project_id,l.operation_id,COALESCE(cs.provider_session_id,ca.provider_payment_intent_id,''),COALESCE(cs.presentation,'intent') FROM billing_payment_locks l JOIN invoices i ON i.id=l.invoice_id LEFT JOIN billing_checkout_sessions cs ON l.operation_id='legacy-checkout:'||cs.id LEFT JOIN billing_collection_attempts ca ON l.operation_id='legacy-collection:'||ca.id WHERE l.operation_id LIKE 'legacy-%' LIMIT 50`)
	if err != nil {
		return err
	}
	type pending struct{ pid, op, id, presentation string }
	var list []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.pid, &p.op, &p.id, &p.presentation); err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.id == "" {
			continue
		}
		scoped := ctx.WithProject(p.pid)
		b := scoped.IntegrationFor("payment_processor")
		if b == nil {
			continue
		}
		kind := "create_checkout_session"
		if p.presentation == "intent" {
			kind = "create_payment_intent"
		}
		get, field := operationGetTool(kind)
		var result map[string]any
		if err = executeStripeRaw(scoped, b, get, map[string]any{field: p.id}, &result); err != nil {
			continue
		}
		raw, _ := json.Marshal(result)
		if kind == "create_payment_intent" {
			err = a.handlePaymentIntent(scoped, raw)
		} else if strArg(result, "status") == "expired" {
			err = a.handleCheckoutSessionTerminal(scoped, raw, "expired")
		} else {
			err = a.handleCheckoutCompleted(scoped, raw)
		}
		if err != nil {
			ctx.Logger().Warn("legacy payment recovery", "operation", p.op, "error", fmt.Sprint(err))
		}
	}
	return nil
}
