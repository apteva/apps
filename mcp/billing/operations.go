package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"strings"
	"sync"
	"time"
)

var operationLocks = struct {
	sync.Mutex
	entries map[string]*operationMutex
}{entries: make(map[string]*operationMutex)}

type operationMutex struct {
	sync.Mutex
	users int
}

func operationLock(key string) func() {
	operationLocks.Lock()
	m := operationLocks.entries[key]
	if m == nil {
		m = &operationMutex{}
		operationLocks.entries[key] = m
	}
	m.users++
	operationLocks.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		operationLocks.Lock()
		m.users--
		if m.users == 0 {
			delete(operationLocks.entries, key)
		}
		operationLocks.Unlock()
	}
}
func providerKey(ctx *sdk.AppCtx, connectionID int64, kind, pid, key string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("billing:%s:%d:%s:%s:%s", os.Getenv("APTEVA_INSTALL_ID"), connectionID, kind, pid, key)))
	return "billing-" + hex.EncodeToString(h[:])
}
func operationGetTool(kind string) (string, string) {
	switch kind {
	case "create_payment_intent":
		return "get_payment_intent", "payment_intent_id"
	case "create_checkout_session":
		return "get_checkout_session", "checkout_session_id"
	case "create_refund":
		return "get_refund", "refund_id"
	case "create_customer":
		return "get_customer", "customer_id"
	}
	return "", ""
}
func executeStripe(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, input map[string]any, out any) error {
	if !strings.HasPrefix(tool, "create_") {
		return executeStripeRaw(ctx, bound, tool, input, out)
	}
	if get, _ := operationGetTool(tool); get == "" {
		return executeStripeRaw(ctx, bound, tool, input, out)
	}
	meta := mapFromAny(input["metadata"])
	pid := firstString(strArg(meta, "apteva_project_id"), ctx.CurrentProject())
	key := strArg(input, "idempotency_key")
	if key == "" {
		if tool == "create_customer" {
			key = "customer:" + strArg(meta, "apteva_customer_id")
		} else {
			key = fmt.Sprintf("request:%d", time.Now().UnixNano())
		}
	}
	opID := providerKey(ctx, bound.ConnectionID, tool, pid, key)
	unlock := operationLock(opID)
	defer unlock()
	req := mergeMaps(input)
	req["idempotency_key"] = opID
	meta = mergeMaps(meta, map[string]any{"apteva_operation_id": opID})
	req["metadata"] = meta
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	invoiceID := atoi64(strArg(meta, "apteva_invoice_id"))
	isPayment := invoiceID != 0 && (tool == "create_payment_intent" || tool == "create_checkout_session")
	now := time.Now().Unix()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing, providerID, response, state string
	var conn, created int64
	err = tx.QueryRow(`SELECT request_json,provider_id,response_json,state,connection_id,created_at FROM billing_provider_operations WHERE id=?`, opID).Scan(&existing, &providerID, &response, &state, &conn, &created)
	if err == sql.ErrNoRows {
		if isPayment {
			var status string
			var hold bool
			var total, paid int64
			if err = tx.QueryRow("SELECT status,total_cents,amount_paid_cents,collection_hold FROM invoices WHERE id=? AND project_id=?", invoiceID, pid).Scan(&status, &total, &paid, &hold); err != nil {
				return err
			}
			requested := int64Arg(input, "amount")
			if tool == "create_checkout_session" {
				for _, raw := range sliceFromAny(input["line_items"]) {
					line := mapFromAny(raw)
					requested += int64Arg(mapFromAny(line["price_data"]), "unit_amount") * int64Arg(line, "quantity")
				}
			}
			if requested != total-paid {
				return errors.New("invoice balance changed; reload before starting payment")
			}
			if (status != "open" && status != "uncollectible") || total <= paid || hold {
				return errors.New("invoice is not collectible or is on refund hold")
			}
			if _, err = tx.Exec("INSERT INTO billing_payment_locks(invoice_id,operation_id) VALUES(?,?)", invoiceID, opID); err != nil {
				return errors.New("invoice already has an active provider payment; reuse or cancel it first")
			}
		}
		_, err = tx.Exec(`INSERT INTO billing_provider_operations(id,project_id,invoice_id,kind,caller_key,connection_id,request_json,created_at,updated_at) VALUES(?,?,NULLIF(?,0),?,?,?,?,?,?)`, opID, pid, invoiceID, tool, key, bound.ConnectionID, string(raw), now, now)
		if err != nil {
			return err
		}
		created = now
		state = "pending"
	} else if err != nil {
		return err
	} else if existing != string(raw) {
		return errors.New("idempotency key was already used with different request parameters")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if state == "needs_review" {
		return errors.New("provider outcome requires reconciliation; no new charge has been submitted")
	}
	if providerID != "" {
		if tool == "create_customer" {
			return json.Unmarshal([]byte(response), out)
		}
		_, _ = ctx.AppDB().Exec("UPDATE billing_provider_operations SET updated_at=? WHERE id=?", now, opID)
		get, idField := operationGetTool(tool)
		var current map[string]any
		if err = executeStripeRaw(ctx, bound, get, map[string]any{idField: providerID}, &current); err != nil {
			return err
		}
		return decodeProvider(current, out)
	}
	if now-created > 23*3600 {
		ctx.AppDB().Exec("UPDATE billing_provider_operations SET state='needs_review',error='provider idempotency recovery window elapsed' WHERE id=?", opID)
		return errors.New("provider result unknown past safe retry window; reconcile before retrying")
	}
	var result map[string]any
	if err = executeStripeRaw(ctx, bound, tool, req, &result); err != nil {
		ctx.AppDB().Exec("UPDATE billing_provider_operations SET error=?,updated_at=? WHERE id=?", err.Error(), now, opID)
		return err
	}
	providerID = strArg(result, "id")
	if providerID == "" {
		return errors.New("Stripe returned no object id; operation retained for recovery")
	}
	saved := mergeMaps(result)
	delete(saved, "client_secret")
	savedRaw, e := json.Marshal(saved)
	if e != nil {
		return e
	}
	_, err = ctx.AppDB().Exec(`UPDATE billing_provider_operations SET provider_id=?,response_json=?,state='submitted',error='',updated_at=? WHERE id=?`, providerID, string(savedRaw), now, opID)
	if err != nil {
		return err
	}
	return decodeProvider(result, out)
}
func decodeProvider(v any, out any) error {
	if out == nil {
		return nil
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func finishProviderOperation(db *sql.DB, providerID string) error {
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM billing_payment_locks WHERE operation_id IN (SELECT id FROM billing_provider_operations WHERE provider_id=?)", providerID); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE billing_provider_operations SET state='completed' WHERE provider_id=?", providerID); e != nil {
		return e
	}
	// Legacy locks are removed only for a matching reconciled provider object.
	if _, e = tx.Exec(`DELETE FROM billing_payment_locks WHERE operation_id IN (SELECT 'legacy-checkout:'||id FROM billing_checkout_sessions WHERE provider_session_id=? UNION SELECT 'legacy-collection:'||id FROM billing_collection_attempts WHERE provider_payment_intent_id=?)`, providerID, providerID); e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) recoverProviderOperations(c context.Context, ctx *sdk.AppCtx) error {
	rows, e := ctx.AppDB().Query(`SELECT id,project_id,kind,caller_key,request_json,connection_id FROM billing_provider_operations WHERE state IN ('pending','submitted') ORDER BY updated_at LIMIT 50`)
	if e != nil {
		return e
	}
	type op struct {
		id, pid, kind, key, raw string
		conn                    int64
	}
	var ops []op
	for rows.Next() {
		var o op
		if e = rows.Scan(&o.id, &o.pid, &o.kind, &o.key, &o.raw, &o.conn); e != nil {
			rows.Close()
			return e
		}
		ops = append(ops, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, o := range ops {
		if c.Err() != nil {
			return c.Err()
		}
		scoped := ctx.WithProject(o.pid)
		bound := scoped.IntegrationFor("payment_processor")
		if bound == nil || bound.ConnectionID != o.conn {
			continue
		}
		var input map[string]any
		if e = json.Unmarshal([]byte(o.raw), &input); e != nil {
			return e
		}
		input["idempotency_key"] = o.key
		var result map[string]any
		if e = executeStripe(scoped, bound, o.kind, input, &result); e != nil {
			continue
		}
		raw, _ := json.Marshal(result)
		if e = a.reconcileRecoveredOperation(scoped, o.kind, input, result, raw); e != nil {
			ctx.Logger().Warn("billing operation reconciliation", "operation", o.id, "error", e.Error())
		}
	}
	return nil
}
func (a *App) reconcileRecoveredOperation(ctx *sdk.AppCtx, kind string, input, result map[string]any, raw json.RawMessage) error {
	meta := mapFromAny(input["metadata"])
	pid := strArg(meta, "apteva_project_id")
	iid := atoi64(strArg(meta, "apteva_invoice_id"))
	id := strArg(result, "id")
	switch kind {
	case "create_customer":
		cid := atoi64(strArg(meta, "apteva_customer_id"))
		b := ctx.IntegrationFor("payment_processor")
		if b == nil {
			return errors.New("processor unavailable")
		}
		if _, e := ctx.AppDB().Exec(`INSERT INTO billing_customer_provider_ids(customer_id,connection_id,provider_id) VALUES(?,?,?) ON CONFLICT(customer_id,connection_id) DO UPDATE SET provider_id=excluded.provider_id`, cid, b.ConnectionID, id); e != nil {
			return e
		}
		return finishProviderOperation(ctx.AppDB(), id)
	case "create_payment_intent":
		if aid := atoi64(strArg(meta, "apteva_collection_attempt")); aid != 0 {
			ctx.AppDB().Exec("UPDATE billing_collection_attempts SET provider_payment_intent_id=? WHERE id=? AND project_id=? AND provider_payment_intent_id IS NULL", id, aid, pid)
			return a.handleCollectionPaymentIntent(ctx, raw)
		}
		save := strArg(input, "setup_future_usage") == "off_session"
		if _, e := ctx.AppDB().Exec(`INSERT OR IGNORE INTO billing_checkout_sessions(project_id,invoice_id,provider,provider_session_id,amount_cents,currency,status,presentation,save_payment_method,set_default_payment_method) VALUES(?,?,'stripe',?,?,?,'pending','intent',?,?)`, pid, iid, id, int64Arg(input, "amount"), strings.ToUpper(strArg(input, "currency")), boolInt(save), boolInt(strArg(meta, "apteva_set_default") == "true")); e != nil {
			return e
		}
		return a.handlePaymentIntent(ctx, raw)
	case "create_checkout_session":
		if strArg(input, "mode") == "setup" {
			_, e := ctx.AppDB().Exec(`INSERT INTO billing_setup_sessions(project_id,customer_id,provider,provider_customer_id,provider_session_id,provider_setup_intent_id,status,success_url,cancel_url,url,payment_method_types,metadata) VALUES(?,?,'stripe',?,?,?,'pending',?,?,?,'[]',?) ON CONFLICT(provider,provider_session_id) DO UPDATE SET provider_setup_intent_id=excluded.provider_setup_intent_id`, pid, atoi64(strArg(meta, "apteva_customer_id")), strArg(input, "customer"), id, strArg(result, "setup_intent"), strArg(input, "success_url"), strArg(input, "cancel_url"), strArg(result, "url"), jsonOrEmpty(meta, "{}"))
			if e != nil {
				return e
			}
			if si := strArg(result, "setup_intent"); si != "" {
				var setup map[string]any
				b := ctx.IntegrationFor("payment_processor")
				if e = executeStripeRaw(ctx, b, "get_setup_intent", map[string]any{"setup_intent_id": si}, &setup); e != nil {
					return e
				}
				raw, _ := json.Marshal(setup)
				if strArg(setup, "status") == "succeeded" {
					if e = a.handleSetupIntentSucceeded(ctx, raw); e != nil {
						return e
					}
					return finishProviderOperation(ctx.AppDB(), id)
				}
			}
			if strArg(result, "status") == "expired" {
				return finishProviderOperation(ctx.AppDB(), id)
			}
			return nil
		}
		var due int64
		var currency string
		if lines, ok := input["line_items"].([]any); ok && len(lines) > 0 {
			price := mapFromAny(mapFromAny(lines[0])["price_data"])
			due = int64Arg(price, "unit_amount")
			currency = strArg(price, "currency")
		}
		if due == 0 {
			var lineInput []any
			blob, _ := json.Marshal(input["line_items"])
			json.Unmarshal(blob, &lineInput)
			if len(lineInput) > 0 {
				price := mapFromAny(mapFromAny(lineInput[0])["price_data"])
				due = int64Arg(price, "unit_amount")
				currency = strArg(price, "currency")
			}
		}
		pd := mapFromAny(input["payment_intent_data"])
		save := strArg(pd, "setup_future_usage") == "off_session"
		if _, e := ctx.AppDB().Exec(`INSERT OR IGNORE INTO billing_checkout_sessions(project_id,invoice_id,provider,provider_session_id,amount_cents,currency,status,save_payment_method,set_default_payment_method) VALUES(?,?,'stripe',?,?,?,'pending',?,?)`, pid, iid, id, due, strings.ToUpper(currency), boolInt(save), boolInt(strArg(meta, "apteva_set_default") == "true")); e != nil {
			return e
		}
		if strArg(result, "status") == "expired" {
			if e := a.handleCheckoutSessionTerminal(ctx, raw, "expired"); e != nil {
				return e
			}
			return finishProviderOperation(ctx.AppDB(), id)
		}
		if strArg(result, "payment_status") == "paid" {
			return a.handleCheckoutCompleted(ctx, raw)
		}
	case "create_refund":
		return a.handleRefundObject(ctx, raw)
	}
	return nil
}
func (a *App) drainWebhookInbox(ctx *sdk.AppCtx) error {
	rows, e := ctx.AppDB().Query("SELECT id,event_type,object_json,connection_id FROM billing_webhook_inbox WHERE state='pending' AND next_attempt<=unixepoch() ORDER BY next_attempt,created_at LIMIT 100")
	if e != nil {
		return e
	}
	type event struct {
		id, kind, raw string
		conn          int64
	}
	var events []event
	for rows.Next() {
		var ev event
		if e = rows.Scan(&ev.id, &ev.kind, &ev.raw, &ev.conn); e != nil {
			rows.Close()
			return e
		}
		events = append(events, ev)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, ev := range events {
		b := ctx.IntegrationFor("payment_processor")
		if b == nil || b.ConnectionID != ev.conn {
			continue
		}
		unlock := operationLock("event:" + ev.id)
		err := a.dispatchStripeEvent(ctx, ev.id, ev.kind, json.RawMessage(ev.raw))
		if err == nil {
			_, err = ctx.AppDB().Exec("UPDATE billing_webhook_inbox SET state='completed',attempts=attempts+1,last_error='' WHERE id=?", ev.id)
		} else {
			ctx.AppDB().Exec("UPDATE billing_webhook_inbox SET attempts=attempts+1,next_attempt=unixepoch()+min(300,1 << min(attempts,8)),last_error=? WHERE id=?", err.Error(), ev.id)
		}
		unlock()
	}
	_ = flushOutbox(ctx)
	return nil
}
