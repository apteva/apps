package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Resume the stored request before consulting mutable invoice state.
func (a *App) replayPayment(ctx *sdk.AppCtx, pid string, iid int64, kind string, args map[string]any) (any, bool, error) {
	key := strings.TrimSpace(strArg(args, "idempotency_key"))
	var raw, storedKey string
	var conn, owner int64
	query := `SELECT request_json,caller_key,connection_id,invoice_id FROM billing_provider_operations WHERE project_id=? AND kind=? AND caller_key=?`
	params := []any{pid, kind, key}
	if key == "" && kind == "create_checkout_session" {
		query = `SELECT request_json,caller_key,connection_id,invoice_id FROM billing_provider_operations WHERE project_id=? AND kind=? AND invoice_id=? AND state IN ('pending','submitted') ORDER BY created_at DESC LIMIT 1`
		params = []any{pid, kind, iid}
	}
	err := ctx.AppDB().QueryRow(query, params...).Scan(&raw, &storedKey, &conn, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if owner != iid {
		return nil, true, errors.New("idempotency key belongs to another invoice")
	}
	var input map[string]any
	if err = json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, true, err
	}
	presentation := "intent"
	if kind == "create_checkout_session" {
		presentation = "hosted"
		if strArg(input, "ui_mode") == "elements" {
			presentation = "elements"
		}
	}
	if v := strArg(args, "presentation"); v != "" && v != presentation {
		return nil, true, errors.New("idempotency key reused with different presentation")
	}
	save := strArg(input, "setup_future_usage") == "off_session" || strArg(mapFromAny(input["payment_intent_data"]), "setup_future_usage") == "off_session"
	def := strArg(mapFromAny(input["metadata"]), "apteva_set_default") == "true"
	for name, want := range map[string]bool{"save_payment_method": save, "set_default_payment_method": def} {
		if _, ok := args[name]; ok && boolFromArg(args, name) != want {
			return nil, true, fmt.Errorf("idempotency key reused with different %s", name)
		}
	}
	for _, name := range []string{"success_url", "cancel_url", "return_url"} {
		if v := strArg(args, name); v != "" && v != strArg(input, name) {
			return nil, true, fmt.Errorf("idempotency key reused with different %s", name)
		}
	}
	bound, err := requireProcessor(ctx)
	if err != nil {
		return nil, true, err
	}
	if bound.ConnectionID != conn {
		return nil, true, errors.New("original processor connection must be restored to resume this payment")
	}
	input["idempotency_key"] = storedKey
	var result map[string]any
	if err = executeStripe(ctx, bound, kind, input, &result); err != nil {
		return nil, true, err
	}
	rawResult, _ := json.Marshal(result)
	if err = a.reconcileRecoveredOperation(ctx, kind, input, result, rawResult); err != nil {
		return nil, true, err
	}
	out := map[string]any{"provider": "stripe", "presentation": presentation, "status": result["status"], "replayed": true, "client_secret": result["client_secret"], "save_payment_method": save, "set_default_payment_method": def}
	if kind == "create_payment_intent" {
		out["payment_intent_id"] = result["id"]
	} else {
		out["stripe_session_id"] = result["id"]
		out["url"] = result["url"]
		out["expires_at"] = result["expires_at"]
	}
	if presentation != "hosted" {
		pk, e := stripePublishableKey(ctx, bound)
		if e != nil {
			return nil, true, e
		}
		out["publishable_key"] = pk
	}
	return out, true, nil
}
