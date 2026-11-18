package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const appVersion = "0.1.0"

// Keep in sync with apteva.yaml (TestEmbeddedManifest_MatchesFile).
const manifestYAML = `schema: apteva-app/v1
name: credits
display_name: Credits
version: 0.1.0
description: Prepaid credit wallets with lots, holds, caps and an append-only ledger.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: billing
      version: ">=0.13.0"
      optional: true
      events:
        - invoice.paid
        - invoice.refunded
provides:
  http_routes:
    - prefix: /
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/credits
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/credits.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("credits requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("credits mounted", "version", appVersion, "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "expire-holds-and-lots", Schedule: "@every 1m", Run: a.runExpiry}}
}

func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Topic: "invoice.paid", Handler: a.handleInvoicePaid},
		{Topic: "invoice.refunded", Handler: a.handleInvoiceRefunded},
	}
}

func main() { sdk.Run(&App{}) }

// finish publishes a committed transaction's events and starts any
// auto-reloads it triggered. Never called for a rolled-back transaction.
func (a *App) finish(ctx *sdk.AppCtx, s *txState) {
	if s == nil || ctx == nil {
		return
	}
	for _, ev := range s.events {
		ctx.EmitWithProject(ev.Topic, ev.ProjectID, ev.Data)
	}
	for _, id := range s.autoReload {
		walletID := id
		runAsync(func() { a.autoReload(ctx, walletID) })
	}
}

// ─── Tool definitions ────────────────────────────────────────────────

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

var walletProps = map[string]any{
	"wallet_id":    prop("integer", "Wallet id. Alternatively name it by subject_type + subject_id + unit."),
	"subject_type": prop("string", "Who owns the wallet, e.g. saas_account, customer, project. Default customer."),
	"subject_id":   prop("string", "Owner id, e.g. acct_42."),
	"unit":         prop("string", "Free-text unit: usd/eur (micro-units, 1 usd = 1000000), minutes, points, tokens."),
}

var referenceProps = map[string]any{
	"reference_app":  prop("string", "App that owns the operation, e.g. llm."),
	"reference_type": prop("string", "Operation type, e.g. request."),
	"reference_id":   prop("string", "Operation id. The same reference always maps to the same hold."),
	"reference":      prop("string", "Shorthand for the three fields above: app/type/id."),
}

func schema(required []string, groups ...map[string]any) map[string]any {
	props := map[string]any{}
	for _, g := range groups {
		for k, v := range g {
			props[k] = v
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func (a *App) MCPTools() []sdk.Tool {
	amount := map[string]any{"amount": prop("integer", "Integer amount in the wallet's unit.")}
	listing := map[string]any{"limit": prop("integer", "Max rows (default 50, max 200)."), "status": prop("string", "Filter by status.")}
	return []sdk.Tool{
		{Name: "credits_wallet_ensure", Description: "Create a wallet for a subject and unit if it does not exist, and return it. One wallet per (subject_type, subject_id, unit).", InputSchema: schema([]string{"subject_id", "unit"}, walletProps, map[string]any{"low_threshold": prop("integer", "Emit credits.balance.low when available drops below this."), "billing_customer_id": prop("integer", "Billing customer charged for top-ups."), "metadata": prop("object", "Free-form metadata.")}), Handler: a.toolWalletEnsure},
		{Name: "credits_wallet_get", Description: "Read a wallet's available and held balance (one row read), its caps with current usage, and auto-reload settings. Returns wallet:null when a subject has no wallet yet.", InputSchema: schema(nil, walletProps), Handler: a.toolWalletGet},
		{Name: "credits_wallet_update", Description: "Change a wallet's low_threshold, billing_customer_id, metadata (merged) or auto-reload. auto_reload = {enabled, amount, price_cents}: when the balance crosses low_threshold, charge the saved card off-session through billing; at most one attempt in flight per wallet. Off by default.", InputSchema: schema(nil, walletProps, map[string]any{"low_threshold": prop("integer", ""), "billing_customer_id": prop("integer", ""), "metadata": prop("object", "Merged into existing metadata."), "auto_reload": prop("object", "{enabled: bool, amount: integer (wallet units), price_cents: integer}")}), Handler: a.toolWalletUpdate},
		{Name: "credits_wallets_list", Description: "List wallets, optionally filtered by subject_type, subject_id, unit or status.", InputSchema: schema(nil, walletProps, listing), Handler: a.toolWalletsList},
		{Name: "credits_wallet_freeze", Description: "Freeze a wallet: new reserves and transfers out are denied (reason frozen). Existing holds can still be committed or released.", InputSchema: schema(nil, walletProps, map[string]any{"reason": prop("string", "")}), Handler: a.toolWalletFreeze},
		{Name: "credits_wallet_unfreeze", Description: "Unfreeze a wallet.", InputSchema: schema(nil, walletProps), Handler: a.toolWalletUnfreeze},

		{Name: "credits_grant", Description: "Add non-paid credit as a new lot: source promo, reward or adjustment (paid purchase lots come only from billing's invoice.paid via credits_topup_create). Idempotent by source_ref, so a retried webhook can't credit twice. Creates the wallet if needed. Promo lots default to priority 10 (spent first); others 100.", InputSchema: schema([]string{"amount", "source"}, walletProps, amount, map[string]any{"source": map[string]any{"type": "string", "enum": []string{"promo", "reward", "adjustment"}}, "source_ref": prop("string", "Idempotency reference, e.g. saas:signup:acct_42."), "priority": prop("integer", "Lower is spent first."), "expires_at": prop("string", "RFC3339."), "expires_in_days": prop("integer", "Alternative to expires_at."), "metadata": prop("object", "")}), Handler: a.toolGrant},
		{Name: "credits_topup_create", Description: "Start a paid top-up: billing creates a one-off invoice (VAT applies here) and a payment session; the purchase lot is created only when billing emits invoice.paid. For a currency wallet (unit = invoice currency) pass price_cents alone; otherwise pass amount (wallet units) and price_cents. Idempotent by idempotency_key.", InputSchema: schema([]string{"idempotency_key"}, walletProps, map[string]any{"amount": prop("integer", "Credit to add, in wallet units. Defaults to price_cents * 10000 for currency wallets."), "price_cents": prop("integer", "Net price billed, in cents of currency."), "currency": prop("string", "Invoice currency. Defaults to the wallet unit."), "customer_id": prop("integer", "Billing customer. Defaults to the wallet's billing_customer_id and is saved on the wallet."), "description": prop("string", "Invoice line description."), "presentation": map[string]any{"type": "string", "enum": []string{"hosted", "elements", "none"}, "description": "Payment session type; none returns only the invoice."}, "success_url": prop("string", ""), "cancel_url": prop("string", ""), "return_url": prop("string", ""), "save_payment_method": prop("boolean", "Save the card for auto-reload."), "idempotency_key": prop("string", "Required; retries return the same top-up.")}), Handler: a.toolTopupCreate},
		{Name: "credits_topups_list", Description: "List top-ups for a wallet.", InputSchema: schema(nil, walletProps, listing), Handler: a.toolTopupsList},

		{Name: "credits_reserve", Description: "Set money aside for an operation that hasn't finished. Checks wallet status, available balance and every applicable cap; returns {hold_id, available_after} or {denied: true, reason: insufficient_balance | cap_reached | frozen} without spending anything. The same reference returns the same hold. Default ttl 15 minutes, max 7 days; expired holds are released automatically.", InputSchema: schema([]string{"amount"}, walletProps, amount, referenceProps, map[string]any{"scope": prop("string", "Cap scope, e.g. project:proj_prod."), "ttl_seconds": prop("integer", "Hold lifetime."), "metadata": prop("object", "")}), Handler: a.toolReserve},
		{Name: "credits_commit", Description: "Spend a hold. amount may be less than the hold (the rest is released) or more (the extra comes from available balance; anything not covered is recorded as a shortfall, since the work already happened). Omit amount to commit the full hold. Idempotent.", InputSchema: schema([]string{"hold_id"}, map[string]any{"hold_id": prop("integer", ""), "amount": prop("integer", "Actual cost in wallet units.")}), Handler: a.toolCommit},
		{Name: "credits_release", Description: "Give a hold back in full, e.g. when the request failed. Idempotent.", InputSchema: schema([]string{"hold_id"}, map[string]any{"hold_id": prop("integer", "")}), Handler: a.toolRelease},
		{Name: "credits_spend", Description: "Reserve and commit in one call, for operations that can't fail halfway (a single SMS). Same denials as credits_reserve; reference required and idempotent.", InputSchema: schema([]string{"amount"}, walletProps, amount, referenceProps, map[string]any{"scope": prop("string", ""), "metadata": prop("object", "")}), Handler: a.toolSpend},

		{Name: "credits_adjust", Description: "Manual correction with a required reason. Positive adds an adjustment lot; negative removes spendable credit and fails rather than go negative. Idempotent by idempotency_key.", InputSchema: schema([]string{"amount", "reason"}, walletProps, map[string]any{"amount": prop("integer", "Signed amount."), "reason": prop("string", "Required."), "idempotency_key": prop("string", ""), "expires_at": prop("string", "For positive adjustments."), "priority": prop("integer", "")}), Handler: a.toolAdjust},
		{Name: "credits_transfer", Description: "Move spendable credit between two wallets of the same unit (e.g. org to project). Not allowed between different billing customers. Each source lot keeps its priority, expiry and paid flag. Idempotent by idempotency_key.", InputSchema: schema([]string{"amount"}, map[string]any{"from_wallet_id": prop("integer", ""), "from_subject_type": prop("string", ""), "from_subject_id": prop("string", ""), "to_wallet_id": prop("integer", ""), "to_subject_type": prop("string", ""), "to_subject_id": prop("string", ""), "unit": prop("string", "")}, amount, map[string]any{"reason": prop("string", ""), "idempotency_key": prop("string", "")}), Handler: a.toolTransfer},

		{Name: "credits_entries_list", Description: "Paginated ledger for a wallet, newest first: the customer-facing statement. Each entry has kind (credit, hold, commit, release, expire, void, adjust, transfer), amount, and balances after. Page with before_id.", InputSchema: schema(nil, walletProps, map[string]any{"before_id": prop("integer", "Return entries older than this id."), "kind": prop("string", ""), "limit": prop("integer", "")}), Handler: a.toolEntriesList},
		{Name: "credits_holds_list", Description: "List holds for a wallet (status active|committed|released|expired).", InputSchema: schema(nil, walletProps, listing), Handler: a.toolHoldsList},
		{Name: "credits_lots_list", Description: "List lots for a wallet in spend order (status active|depleted|expired|voided).", InputSchema: schema(nil, walletProps, listing), Handler: a.toolLotsList},
		{Name: "credits_caps_set", Description: "Set a spending ceiling over a window (day|week|month|lifetime), for the whole wallet (scope '') or one scope such as project:proj_prod. Separate from the balance. Upserts by (wallet, scope, window).", InputSchema: schema([]string{"window", "limit_amount"}, walletProps, map[string]any{"scope": prop("string", ""), "window": map[string]any{"type": "string", "enum": []string{"day", "week", "month", "lifetime"}}, "limit_amount": prop("integer", ""), "metadata": prop("object", "")}), Handler: a.toolCapsSet},
		{Name: "credits_caps_get", Description: "List a wallet's caps with spent, held and remaining for the current period.", InputSchema: schema(nil, walletProps), Handler: a.toolCapsGet},
		{Name: "credits_shortfalls_list", Description: "Money the ledger could not take: commits larger than the hold and the balance, and refunds of already-spent purchase credit. For a person to review.", InputSchema: schema(nil, walletProps, listing), Handler: a.toolShortfallsList},
		{Name: "credits_shortfall_resolve", Description: "Mark a shortfall reviewed, with a note.", InputSchema: schema([]string{"id"}, map[string]any{"id": prop("integer", ""), "note": prop("string", "")}), Handler: a.toolShortfallResolve},
		{Name: "credits_reconcile", Description: "Check the ledger invariants for one wallet or every wallet in the project: available + held = lots remaining, held = active holds, entries rebuild the balances, lot moves rebuild each lot, nothing negative. Returns ok and a list of issues.", InputSchema: schema(nil, map[string]any{"wallet_id": prop("integer", "")}), Handler: a.toolReconcile},
		{Name: "credits_liability_report", Description: "Unspent paid credit (prepaid customer money, a liability until spent) at the end of a given date, per unit. Promo and reward credit are excluded.", InputSchema: schema(nil, map[string]any{"as_of": prop("string", "YYYY-MM-DD (end of that day, UTC) or RFC3339. Default now.")}), Handler: a.toolLiability},
	}
}

// ─── Argument helpers ────────────────────────────────────────────────

func resolveProject(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	pid := strings.TrimSpace(strArg(args, "_project_id"))
	if pid == "" && ctx != nil {
		pid = ctx.CurrentProject()
	}
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id required")
	}
	return pid, nil
}

func walletRefFrom(args map[string]any, prefix string) WalletRef {
	unit := strings.ToLower(strArg(args, prefix+"unit"))
	if unit == "" && prefix != "" {
		unit = strings.ToLower(strArg(args, "unit"))
	}
	return WalletRef{
		ID:          int64Arg(args, prefix+"wallet_id"),
		SubjectType: firstNonEmpty(strArg(args, prefix+"subject_type"), "customer"),
		SubjectID:   strArg(args, prefix+"subject_id"),
		Unit:        unit,
	}
}

func referenceFrom(args map[string]any) Reference {
	r := Reference{App: strArg(args, "reference_app"), Type: strArg(args, "reference_type"), ID: strArg(args, "reference_id")}
	if short := strArg(args, "reference"); short != "" && r.App == "" && r.Type == "" && r.ID == "" {
		if parts := strings.SplitN(short, "/", 3); len(parts) == 3 {
			r = Reference{App: parts[0], Type: parts[1], ID: parts[2]}
		}
	}
	return r
}

// amountArg reads an integer amount. JSON numbers arrive as float64; a
// fractional value is an error rather than a silent truncation.
func amountArg(args map[string]any, key string) (int64, bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false, nil
	}
	switch t := v.(type) {
	case float64:
		if t != math.Trunc(t) || math.Abs(t) > 1<<53 {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		return int64(t), true, nil
	case int:
		return int64(t), true, nil
	case int64:
		return t, true, nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		return n, true, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, false, nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		return n, true, nil
	}
	return 0, true, fmt.Errorf("%s must be an integer", key)
}

func requiredAmount(args map[string]any, key string) (int64, error) {
	n, ok, err := amountArg(args, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("%s required", key)
	}
	return n, nil
}

func optionalInt(args map[string]any, key string) (*int64, error) {
	n, ok, err := amountArg(args, key)
	if err != nil || !ok {
		return nil, err
	}
	return &n, nil
}

func expiresFrom(args map[string]any) (string, error) {
	if raw := strArg(args, "expires_at"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", errors.New("expires_at must be RFC3339")
		}
		return fmtTS(t), nil
	}
	days, ok, err := amountArg(args, "expires_in_days")
	if err != nil || !ok {
		return "", err
	}
	if days <= 0 {
		return "", errors.New("expires_in_days must be positive")
	}
	return fmtTS(nowFunc().Add(time.Duration(days) * 24 * time.Hour)), nil
}

func actorFrom(args map[string]any) string {
	return firstNonEmpty(strArg(args, "actor"), strArg(args, "_caller"), strArg(args, "_caller_app"))
}

func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

func int64Arg(m map[string]any, key string) int64 {
	n, _, _ := amountArg(m, key)
	return n
}

func boolArg(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func jsonOrEmpty(v any, sentinel string) string {
	switch t := v.(type) {
	case nil:
		return sentinel
	case string:
		if strings.TrimSpace(t) == "" {
			return sentinel
		}
		return t
	case json.RawMessage:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return sentinel
	}
	return string(raw)
}

func clampLimit(n int64, max int64) int64 {
	if n <= 0 {
		return 50
	}
	if n > max {
		return max
	}
	return n
}
