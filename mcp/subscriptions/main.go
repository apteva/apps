package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: subscriptions
display_name: Subscriptions
version: 0.1.2
description: Generic recurring-commerce lifecycle for SaaS, physical subscriptions, and services.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: catalog
      optional: false
    - name: billing
      optional: true
    - name: orders
      optional: true
    - name: entitlements
      optional: true
provides:
  http_routes:
    - prefix: /
  publishes:
    - name: subscription.active
      description: Subscription entered active status.
    - name: subscription.trialing
      description: Subscription entered trialing status.
    - name: subscription.past_due
      description: Subscription entered past_due status.
    - name: subscription.cancelled
      description: Subscription was cancelled.
    - name: subscription.paused
      description: Subscription entered paused status.
    - name: subscription.ended
      description: Subscription ended.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/subscriptions
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/subscriptions.db
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
		return errors.New("subscriptions requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("subscriptions mounted", "version", "0.1.2", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "subscription-lifecycle",
		Schedule: "@every 15m",
		Run: func(_ context.Context, appCtx *sdk.AppCtx) error {
			if appCtx == nil {
				appCtx = globalCtx
			}
			return runSubscriptionLifecycle(appCtx, time.Now().UTC())
		},
	}}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/subscriptions", Handler: a.handleSubscriptions},
		{Pattern: "/subscriptions/", Handler: a.handleSubscriptionItem},
		{Pattern: "/cycles", Handler: a.handleCycles},
		{Pattern: "/cycles/", Handler: a.handleCycleItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "subscriptions_create", Description: "Create a subscription.", InputSchema: schemaObject(map[string]any{
			"customer_id": map[string]any{"type": "integer"}, "customer_email": map[string]any{"type": "string"}, "customer_name": map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "billing_provider": map[string]any{"type": "string"}, "external_id": map[string]any{"type": "string"},
			"currency": map[string]any{"type": "string"}, "interval": map[string]any{"type": "string"}, "interval_count": map[string]any{"type": "integer"}, "quantity": map[string]any{"type": "number"},
			"trial_start": map[string]any{"type": "string"}, "trial_end": map[string]any{"type": "string"}, "current_period_start": map[string]any{"type": "string"}, "current_period_end": map[string]any{"type": "string"}, "next_renewal_at": map[string]any{"type": "string"},
			"source": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"}, "items": map[string]any{"type": "array"}, "metadata": map[string]any{"type": "object"},
		}, []string{"items"}), Handler: a.toolSubscriptionsCreate},
		{Name: "subscriptions_get", Description: "Fetch one subscription.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolSubscriptionsGet},
		{Name: "subscriptions_search", Description: "Search subscriptions.", InputSchema: schemaObject(map[string]any{
			"q": map[string]any{"type": "string"}, "customer_id": map[string]any{"type": "integer"}, "customer_email": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}, nil), Handler: a.toolSubscriptionsSearch},
		{Name: "subscriptions_update_status", Description: "Update subscription status/periods.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "current_period_start": map[string]any{"type": "string"}, "current_period_end": map[string]any{"type": "string"}, "next_renewal_at": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"},
		}, []string{"id"}), Handler: a.toolSubscriptionsUpdateStatus},
		{Name: "subscriptions_cancel", Description: "Cancel a subscription.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "at_period_end": map[string]any{"type": "boolean"}, "reason": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolSubscriptionsCancel},
		{Name: "subscription_cycles_create", Description: "Create a renewal cycle.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "period_start": map[string]any{"type": "string"}, "period_end": map[string]any{"type": "string"}, "due_at": map[string]any{"type": "string"}, "invoice_id": map[string]any{"type": "integer"}, "order_id": map[string]any{"type": "integer"}, "entitlement_grant_id": map[string]any{"type": "integer"}, "payment_status": map[string]any{"type": "string"}, "fulfillment_status": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"},
		}, []string{"subscription_id", "period_start", "period_end"}), Handler: a.toolCyclesCreate},
		{Name: "subscription_cycles_update", Description: "Update a renewal cycle.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "payment_status": map[string]any{"type": "string"}, "fulfillment_status": map[string]any{"type": "string"}, "invoice_id": map[string]any{"type": "integer"}, "order_id": map[string]any{"type": "integer"}, "entitlement_grant_id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolCyclesUpdate},
		{Name: "subscription_cycles_list", Description: "List cycles.", InputSchema: schemaObject(map[string]any{"subscription_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, []string{"subscription_id"}), Handler: a.toolCyclesList},
		{Name: "subscription_events_list", Description: "List subscription events.", InputSchema: schemaObject(map[string]any{"subscription_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, []string{"subscription_id"}), Handler: a.toolEventsList},
	}
}

func main() { sdk.Run(&App{}) }

type Subscription struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	CustomerID         *int64          `json:"customer_id,omitempty"`
	CustomerEmail      string          `json:"customer_email,omitempty"`
	CustomerName       string          `json:"customer_name,omitempty"`
	Kind               string          `json:"kind"`
	Status             string          `json:"status"`
	BillingProvider    string          `json:"billing_provider"`
	ExternalID         string          `json:"external_id,omitempty"`
	Currency           string          `json:"currency"`
	Interval           string          `json:"interval"`
	IntervalCount      int64           `json:"interval_count"`
	Quantity           float64         `json:"quantity"`
	TrialStart         string          `json:"trial_start,omitempty"`
	TrialEnd           string          `json:"trial_end,omitempty"`
	CurrentPeriodStart string          `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   string          `json:"current_period_end,omitempty"`
	NextRenewalAt      string          `json:"next_renewal_at,omitempty"`
	CancelAt           string          `json:"cancel_at,omitempty"`
	CancelledAt        string          `json:"cancelled_at,omitempty"`
	EndedAt            string          `json:"ended_at,omitempty"`
	Source             string          `json:"source"`
	SourceRef          string          `json:"source_ref,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	Items              []*SubItem      `json:"items,omitempty"`
	Cycles             []*Cycle        `json:"cycles,omitempty"`
	Events             []*Event        `json:"events,omitempty"`
}

type SubItem struct {
	ID               int64           `json:"id"`
	SubscriptionID   int64           `json:"subscription_id"`
	Position         int             `json:"position"`
	CatalogProductID *int64          `json:"catalog_product_id,omitempty"`
	CatalogPriceID   *int64          `json:"catalog_price_id,omitempty"`
	SKU              string          `json:"sku,omitempty"`
	Title            string          `json:"title"`
	Quantity         float64         `json:"quantity"`
	UnitAmountCents  int64           `json:"unit_amount_cents"`
	Currency         string          `json:"currency"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type Cycle struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	SubscriptionID     int64           `json:"subscription_id"`
	CycleNumber        int64           `json:"cycle_number"`
	PeriodStart        string          `json:"period_start"`
	PeriodEnd          string          `json:"period_end"`
	DueAt              string          `json:"due_at,omitempty"`
	InvoiceID          *int64          `json:"invoice_id,omitempty"`
	OrderID            *int64          `json:"order_id,omitempty"`
	EntitlementGrantID *int64          `json:"entitlement_grant_id,omitempty"`
	PaymentStatus      string          `json:"payment_status"`
	FulfillmentStatus  string          `json:"fulfillment_status"`
	SubtotalCents      int64           `json:"subtotal_cents"`
	TaxCents           int64           `json:"tax_cents"`
	ShippingCents      int64           `json:"shipping_cents"`
	TotalCents         int64           `json:"total_cents"`
	Currency           string          `json:"currency"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	PaidAt             string          `json:"paid_at,omitempty"`
	CompletedAt        string          `json:"completed_at,omitempty"`
}

type Event struct {
	ID             int64           `json:"id"`
	ProjectID      string          `json:"project_id"`
	SubscriptionID int64           `json:"subscription_id"`
	Actor          string          `json:"actor"`
	Action         string          `json:"action"`
	Details        json.RawMessage `json:"details,omitempty"`
	CreatedAt      string          `json:"created_at"`
}

func (a *App) toolSubscriptionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sub, err := dbSubscriptionCreate(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.created", map[string]any{"subscription_id": sub.ID, "kind": sub.Kind, "status": sub.Status})
	return map[string]any{"subscription": sub}, nil
}

func (a *App) toolSubscriptionsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sub, err := dbSubscriptionGet(ctx.AppDB(), pid, int64Arg(args, "id"), true)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, errors.New("subscription not found")
	}
	return map[string]any{"subscription": sub}, nil
}

func (a *App) toolSubscriptionsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbSubscriptionsSearch(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"subscriptions": out, "count": len(out)}, nil
}

func (a *App) toolSubscriptionsUpdateStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sub, err := dbSubscriptionUpdateStatus(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.updated", map[string]any{"subscription_id": sub.ID, "status": sub.Status})
	emitSubscriptionLifecycle(ctx, sub)
	return map[string]any{"subscription": sub}, nil
}

func (a *App) toolSubscriptionsCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sub, err := dbSubscriptionCancel(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.cancelled", map[string]any{"subscription_id": sub.ID})
	return map[string]any{"subscription": sub}, nil
}

func emitSubscriptionLifecycle(ctx *sdk.AppCtx, sub *Subscription) {
	if ctx == nil || sub == nil || !validSubStatus[sub.Status] {
		return
	}
	ctx.Emit("subscription."+sub.Status, map[string]any{
		"id":              sub.ID,
		"subscription_id": sub.ID,
		"status":          sub.Status,
		"customer_id":     sub.CustomerID,
		"customer_email":  sub.CustomerEmail,
		"kind":            sub.Kind,
		"source":          sub.Source,
		"source_ref":      sub.SourceRef,
		"trial_start":     sub.TrialStart,
		"trial_end":       sub.TrialEnd,
		"metadata":        mapFromAny(sub.Metadata),
	})
}

func runSubscriptionLifecycle(ctx *sdk.AppCtx, now time.Time) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	expiredTrials, err := dbSubscriptionsExpiredTrials(ctx.AppDB(), now)
	if err != nil {
		return err
	}
	nowStr := now.UTC().Format(time.RFC3339)
	for _, sub := range expiredTrials {
		meta := mapFromAny(sub.Metadata)
		targetStatus := trialExpiredStatus(meta)
		meta["trial_ended_at"] = nowStr
		if targetStatus == "past_due" || targetStatus == "paused" {
			meta["past_due_since"] = nowStr
		}
		updated, err := dbSubscriptionSetStatusMetadata(ctx.AppDB(), sub.ProjectID, sub.ID, targetStatus, meta, "subscription.trial_expired", map[string]any{
			"from_status": "trialing",
			"to_status":   targetStatus,
			"trial_end":   sub.TrialEnd,
		}, now)
		if err != nil {
			return err
		}
		emitSubscriptionLifecycle(ctx, updated)
	}

	graceExpired, err := dbSubscriptionsGraceExpired(ctx.AppDB(), now)
	if err != nil {
		return err
	}
	for _, sub := range graceExpired {
		meta := mapFromAny(sub.Metadata)
		meta["unpaid_grace_expired_at"] = nowStr
		updated, err := dbSubscriptionSetStatusMetadata(ctx.AppDB(), sub.ProjectID, sub.ID, "ended", meta, "subscription.unpaid_grace_expired", map[string]any{
			"from_status": sub.Status,
			"to_status":   "ended",
		}, now)
		if err != nil {
			return err
		}
		emitSubscriptionLifecycle(ctx, updated)
	}
	return nil
}

func (a *App) toolCyclesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cycle, sub, err := dbCycleCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cycle": cycle, "subscription": sub}, nil
}

func (a *App) toolCyclesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cycle, err := dbCycleUpdate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cycle": cycle}, nil
}

func (a *App) toolCyclesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbCyclesList(ctx.AppDB(), pid, int64Arg(args, "subscription_id"), clampLimit(int(int64Arg(args, "limit")), 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"cycles": out, "count": len(out)}, nil
}

func (a *App) toolEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbEventsList(ctx.AppDB(), pid, int64Arg(args, "subscription_id"), clampLimit(int(int64Arg(args, "limit")), 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": out, "count": len(out)}, nil
}

func dbSubscriptionCreate(ctx *sdk.AppCtx, pid string, args map[string]any) (*Subscription, error) {
	itemsRaw, _ := args["items"].([]any)
	if len(itemsRaw) == 0 {
		return nil, errors.New("items required")
	}
	kind := firstNonEmpty(strArg(args, "kind"), "saas")
	if !validKind[kind] {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	status := firstNonEmpty(strArg(args, "status"), "active")
	if !validSubStatus[status] {
		return nil, fmt.Errorf("invalid status %q", status)
	}
	currency := strings.ToUpper(firstNonEmpty(strArg(args, "currency"), configString(ctx, "default_currency", "USD")))
	items := normalizeItems(itemsRaw, currency)
	if len(items) == 0 {
		return nil, errors.New("at least one valid item required")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(
		`INSERT INTO subscriptions
		   (project_id, customer_id, customer_email, customer_name, kind, status, billing_provider,
		    external_id, currency, interval, interval_count, quantity, trial_start, trial_end,
		    current_period_start, current_period_end, next_renewal_at, source, source_ref, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		pid, nullableInt64(int64Arg(args, "customer_id")), nullStr(strArg(args, "customer_email")), nullStr(strArg(args, "customer_name")),
		kind, status, firstNonEmpty(strArg(args, "billing_provider"), "local"), nullStr(strArg(args, "external_id")),
		currency, firstNonEmpty(strArg(args, "interval"), "month"), firstNonZero(int64Arg(args, "interval_count"), 1),
		float64Arg(args, "quantity", 1), nullStr(strArg(args, "trial_start")), nullStr(strArg(args, "trial_end")),
		nullStr(strArg(args, "current_period_start")), nullStr(strArg(args, "current_period_end")), nullStr(strArg(args, "next_renewal_at")),
		firstNonEmpty(strArg(args, "source"), "manual"), nullStr(strArg(args, "source_ref")), jsonOrEmpty(args["metadata"], "{}"),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	for i, it := range items {
		_, err := tx.Exec(
			`INSERT INTO subscription_items
			   (subscription_id, position, catalog_product_id, catalog_price_id, sku, title, quantity, unit_amount_cents, currency, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, nullablePtr(it.CatalogProductID), nullablePtr(it.CatalogPriceID), nullStr(it.SKU), it.Title,
			it.Quantity, it.UnitAmountCents, strings.ToUpper(firstNonEmpty(it.Currency, currency)), jsonOrEmpty(it.Metadata, "{}"))
		if err != nil {
			return nil, err
		}
	}
	if err := writeEventTx(tx, pid, id, "system", "subscription.created", map[string]any{"kind": kind, "status": status}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbSubscriptionGet(ctx.AppDB(), pid, id, true)
}

func dbSubscriptionsSearch(db *sql.DB, pid string, args map[string]any) ([]*Subscription, error) {
	where := []string{"project_id = ?"}
	qargs := []any{pid}
	if v := int64Arg(args, "customer_id"); v != 0 {
		where = append(where, "customer_id = ?")
		qargs = append(qargs, v)
	}
	for _, key := range []string{"customer_email", "kind", "status"} {
		if v := strArg(args, key); v != "" {
			where = append(where, key+" = ?")
			qargs = append(qargs, v)
		}
	}
	if q := strArg(args, "q"); q != "" {
		where = append(where, "(customer_email LIKE ? OR customer_name LIKE ? OR source_ref LIKE ?)")
		like := "%" + q + "%"
		qargs = append(qargs, like, like, like)
	}
	qargs = append(qargs, clampLimit(int(int64Arg(args, "limit")), 200))
	rows, err := db.Query(`SELECT `+subCols()+` FROM subscriptions WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbSubscriptionGet(db *sql.DB, pid string, id int64, nested bool) (*Subscription, error) {
	if id == 0 {
		return nil, nil
	}
	s, err := scanSub(db.QueryRow(`SELECT `+subCols()+` FROM subscriptions WHERE id = ? AND project_id = ?`, id, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if nested {
		s.Items, _ = dbItemsList(db, id)
		s.Cycles, _ = dbCyclesList(db, pid, id, 50)
		s.Events, _ = dbEventsList(db, pid, id, 50)
	}
	return s, nil
}

func dbSubscriptionUpdateStatus(db *sql.DB, pid string, args map[string]any) (*Subscription, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	sub, err := dbSubscriptionGet(db, pid, id, false)
	if err != nil || sub == nil {
		return nil, firstErr(err, errors.New("subscription not found"))
	}
	status := firstNonEmpty(strArg(args, "status"), sub.Status)
	if !validSubStatus[status] {
		return nil, fmt.Errorf("invalid status %q", status)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`UPDATE subscriptions SET status = ?,
		        current_period_start = COALESCE(?, current_period_start),
		        current_period_end = COALESCE(?, current_period_end),
		        next_renewal_at = COALESCE(?, next_renewal_at),
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ? AND project_id = ?`,
		status, nullStr(strArg(args, "current_period_start")), nullStr(strArg(args, "current_period_end")), nullStr(strArg(args, "next_renewal_at")), id, pid)
	if err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, id, actorOrSystem(strArg(args, "actor")), "subscription.status_updated", map[string]any{"status": status, "note": strArg(args, "note")}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbSubscriptionGet(db, pid, id, true)
}

func dbSubscriptionsExpiredTrials(db *sql.DB, now time.Time) ([]*Subscription, error) {
	rows, err := db.Query(`SELECT `+subCols()+` FROM subscriptions WHERE status='trialing' AND trial_end IS NOT NULL AND trial_end <= ? ORDER BY trial_end ASC`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func dbSubscriptionsGraceExpired(db *sql.DB, now time.Time) ([]*Subscription, error) {
	rows, err := db.Query(`SELECT ` + subCols() + ` FROM subscriptions WHERE status IN ('past_due','paused') ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subscription
	for rows.Next() {
		sub, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		meta := mapFromAny(sub.Metadata)
		graceDays := int64Arg(meta, "unpaid_grace_days")
		if graceDays <= 0 {
			continue
		}
		since, ok := parseTime(strArg(meta, "past_due_since"))
		if !ok || now.Before(since.AddDate(0, 0, int(graceDays))) {
			continue
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func dbSubscriptionSetStatusMetadata(db *sql.DB, pid string, id int64, status string, metadata map[string]any, action string, details map[string]any, now time.Time) (*Subscription, error) {
	if !validSubStatus[status] {
		return nil, fmt.Errorf("invalid status %q", status)
	}
	nowStr := now.UTC().Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`UPDATE subscriptions
		    SET status=?,
		        metadata=?,
		        ended_at = CASE WHEN ?='ended' THEN COALESCE(ended_at, ?) ELSE ended_at END,
		        updated_at=CURRENT_TIMESTAMP
		  WHERE id=? AND project_id=?`,
		status, jsonOrEmpty(metadata, "{}"), status, nowStr, id, pid)
	if err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, id, "system", action, details); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbSubscriptionGet(db, pid, id, true)
}

func dbSubscriptionCancel(db *sql.DB, pid string, args map[string]any) (*Subscription, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if boolArg(args, "at_period_end") {
		_, err = tx.Exec(`UPDATE subscriptions SET status='active', cancel_at=current_period_end, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, id, pid)
	} else {
		_, err = tx.Exec(`UPDATE subscriptions SET status='cancelled', cancelled_at=CURRENT_TIMESTAMP, ended_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, id, pid)
	}
	if err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, id, actorOrSystem(strArg(args, "actor")), "subscription.cancelled", map[string]any{"at_period_end": boolArg(args, "at_period_end"), "reason": strArg(args, "reason")}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbSubscriptionGet(db, pid, id, true)
}

func dbCycleCreate(db *sql.DB, pid string, args map[string]any) (*Cycle, *Subscription, error) {
	subID := int64Arg(args, "subscription_id")
	sub, err := dbSubscriptionGet(db, pid, subID, true)
	if err != nil || sub == nil {
		return nil, nil, firstErr(err, errors.New("subscription not found"))
	}
	next := int64(1)
	_ = db.QueryRow(`SELECT COALESCE(MAX(cycle_number),0)+1 FROM subscription_cycles WHERE subscription_id=?`, subID).Scan(&next)
	subtotal := int64(0)
	for _, it := range sub.Items {
		subtotal += int64(float64(it.UnitAmountCents) * it.Quantity)
	}
	tax := int64Arg(args, "tax_cents")
	ship := int64Arg(args, "shipping_cents")
	total := firstNonZero(int64Arg(args, "total_cents"), subtotal+tax+ship)
	var id int64
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(
		`INSERT INTO subscription_cycles
		   (project_id, subscription_id, cycle_number, period_start, period_end, due_at,
		    invoice_id, order_id, entitlement_grant_id, payment_status, fulfillment_status,
		    subtotal_cents, tax_cents, shipping_cents, total_cents, currency, metadata, paid_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		pid, subID, next, strArg(args, "period_start"), strArg(args, "period_end"), nullStr(strArg(args, "due_at")),
		nullableInt64(int64Arg(args, "invoice_id")), nullableInt64(int64Arg(args, "order_id")), nullableInt64(int64Arg(args, "entitlement_grant_id")),
		firstNonEmpty(strArg(args, "payment_status"), "pending"), firstNonEmpty(strArg(args, "fulfillment_status"), "none"),
		subtotal, tax, ship, total, sub.Currency, jsonOrEmpty(args["metadata"], "{}"),
		nullableTime(strArg(args, "payment_status") == "paid", time.Now().UTC().Format(time.RFC3339)),
	).Scan(&id)
	if err != nil {
		return nil, nil, err
	}
	if err := writeEventTx(tx, pid, subID, "system", "subscription.cycle_created", map[string]any{"cycle_id": id, "cycle_number": next}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	c, err := dbCycleGet(db, pid, id)
	sub, _ = dbSubscriptionGet(db, pid, subID, true)
	return c, sub, err
}

func dbCycleUpdate(db *sql.DB, pid string, args map[string]any) (*Cycle, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	c, err := dbCycleGet(db, pid, id)
	if err != nil || c == nil {
		return nil, firstErr(err, errors.New("cycle not found"))
	}
	payment := firstNonEmpty(strArg(args, "payment_status"), c.PaymentStatus)
	fulfillment := firstNonEmpty(strArg(args, "fulfillment_status"), c.FulfillmentStatus)
	_, err = db.Exec(
		`UPDATE subscription_cycles
		    SET payment_status=?, fulfillment_status=?,
		        invoice_id=COALESCE(?, invoice_id), order_id=COALESCE(?, order_id), entitlement_grant_id=COALESCE(?, entitlement_grant_id),
		        paid_at = CASE WHEN ?='paid' AND paid_at IS NULL THEN CURRENT_TIMESTAMP ELSE paid_at END,
		        completed_at = CASE WHEN (?='paid' AND ? IN ('none','fulfilled','delivered')) AND completed_at IS NULL THEN CURRENT_TIMESTAMP ELSE completed_at END,
		        updated_at=CURRENT_TIMESTAMP
		  WHERE id=? AND project_id=?`,
		payment, fulfillment, nullableInt64(int64Arg(args, "invoice_id")), nullableInt64(int64Arg(args, "order_id")), nullableInt64(int64Arg(args, "entitlement_grant_id")),
		payment, payment, fulfillment, id, pid)
	if err != nil {
		return nil, err
	}
	return dbCycleGet(db, pid, id)
}

func dbCycleGet(db *sql.DB, pid string, id int64) (*Cycle, error) {
	c, err := scanCycle(db.QueryRow(cycleSelect()+` WHERE id=? AND project_id=?`, id, pid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func dbCyclesList(db *sql.DB, pid string, subID int64, limit int) ([]*Cycle, error) {
	rows, err := db.Query(cycleSelect()+` WHERE subscription_id=? AND project_id=? ORDER BY cycle_number DESC LIMIT ?`, subID, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Cycle
	for rows.Next() {
		c, err := scanCycle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func subCols() string {
	return `id, project_id, customer_id, COALESCE(customer_email,''), COALESCE(customer_name,''), kind, status, billing_provider, COALESCE(external_id,''), currency, interval, interval_count, quantity, trial_start, trial_end, current_period_start, current_period_end, next_renewal_at, cancel_at, cancelled_at, ended_at, source, COALESCE(source_ref,''), metadata, created_at, updated_at`
}

func scanSub(row rowScanner) (*Subscription, error) {
	var s Subscription
	var cid sql.NullInt64
	var trialStart, trialEnd, curStart, curEnd, next, cancelAt, cancelledAt, endedAt sql.NullString
	var meta string
	err := row.Scan(&s.ID, &s.ProjectID, &cid, &s.CustomerEmail, &s.CustomerName, &s.Kind, &s.Status, &s.BillingProvider, &s.ExternalID, &s.Currency, &s.Interval, &s.IntervalCount, &s.Quantity, &trialStart, &trialEnd, &curStart, &curEnd, &next, &cancelAt, &cancelledAt, &endedAt, &s.Source, &s.SourceRef, &meta, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.CustomerID = ptrIfValid(cid)
	s.Metadata = json.RawMessage(meta)
	if trialStart.Valid {
		s.TrialStart = trialStart.String
	}
	if trialEnd.Valid {
		s.TrialEnd = trialEnd.String
	}
	if curStart.Valid {
		s.CurrentPeriodStart = curStart.String
	}
	if curEnd.Valid {
		s.CurrentPeriodEnd = curEnd.String
	}
	if next.Valid {
		s.NextRenewalAt = next.String
	}
	if cancelAt.Valid {
		s.CancelAt = cancelAt.String
	}
	if cancelledAt.Valid {
		s.CancelledAt = cancelledAt.String
	}
	if endedAt.Valid {
		s.EndedAt = endedAt.String
	}
	return &s, nil
}

func dbItemsList(db *sql.DB, subID int64) ([]*SubItem, error) {
	rows, err := db.Query(`SELECT id, subscription_id, position, catalog_product_id, catalog_price_id, COALESCE(sku,''), title, quantity, unit_amount_cents, currency, metadata FROM subscription_items WHERE subscription_id=? ORDER BY position,id`, subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SubItem
	for rows.Next() {
		var it SubItem
		var pid, price sql.NullInt64
		var meta string
		if err := rows.Scan(&it.ID, &it.SubscriptionID, &it.Position, &pid, &price, &it.SKU, &it.Title, &it.Quantity, &it.UnitAmountCents, &it.Currency, &meta); err != nil {
			return nil, err
		}
		it.CatalogProductID = ptrIfValid(pid)
		it.CatalogPriceID = ptrIfValid(price)
		it.Metadata = json.RawMessage(meta)
		out = append(out, &it)
	}
	return out, rows.Err()
}

func cycleSelect() string {
	return `SELECT id, project_id, subscription_id, cycle_number, period_start, period_end, due_at, invoice_id, order_id, entitlement_grant_id, payment_status, fulfillment_status, subtotal_cents, tax_cents, shipping_cents, total_cents, currency, metadata, created_at, updated_at, paid_at, completed_at FROM subscription_cycles`
}

func scanCycle(row rowScanner) (*Cycle, error) {
	var c Cycle
	var due, paid, completed sql.NullString
	var inv, ord, grant sql.NullInt64
	var meta string
	err := row.Scan(&c.ID, &c.ProjectID, &c.SubscriptionID, &c.CycleNumber, &c.PeriodStart, &c.PeriodEnd, &due, &inv, &ord, &grant, &c.PaymentStatus, &c.FulfillmentStatus, &c.SubtotalCents, &c.TaxCents, &c.ShippingCents, &c.TotalCents, &c.Currency, &meta, &c.CreatedAt, &c.UpdatedAt, &paid, &completed)
	if err != nil {
		return nil, err
	}
	c.InvoiceID = ptrIfValid(inv)
	c.OrderID = ptrIfValid(ord)
	c.EntitlementGrantID = ptrIfValid(grant)
	c.Metadata = json.RawMessage(meta)
	if due.Valid {
		c.DueAt = due.String
	}
	if paid.Valid {
		c.PaidAt = paid.String
	}
	if completed.Valid {
		c.CompletedAt = completed.String
	}
	return &c, nil
}

func dbEventsList(db *sql.DB, pid string, subID int64, limit int) ([]*Event, error) {
	rows, err := db.Query(`SELECT id, project_id, subscription_id, actor, action, details, created_at FROM subscription_events WHERE subscription_id=? AND project_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, subID, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var details string
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.SubscriptionID, &e.Actor, &e.Action, &details, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Details = json.RawMessage(details)
		out = append(out, &e)
	}
	return out, rows.Err()
}

func writeEventTx(tx *sql.Tx, pid string, subID int64, actor, action string, details map[string]any) error {
	_, err := tx.Exec(`INSERT INTO subscription_events (project_id, subscription_id, actor, action, details) VALUES (?, ?, ?, ?, ?)`, pid, subID, actorOrSystem(actor), action, jsonOrEmpty(details, "{}"))
	return err
}

func (a *App) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args := map[string]any{"q": r.URL.Query().Get("q"), "customer_email": r.URL.Query().Get("customer_email"), "kind": r.URL.Query().Get("kind"), "status": r.URL.Query().Get("status"), "limit": r.URL.Query().Get("limit")}
		out, err := dbSubscriptionsSearch(ctx.AppDB(), pid, args)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, map[string]any{"subscriptions": out, "count": len(out)})
		return
	}
	if r.Method == http.MethodPost {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid JSON body")
			return
		}
		sub, err := dbSubscriptionCreate(ctx, pid, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"subscription": sub})
		return
	}
	httpErr(w, 405, "method not allowed")
}

func (a *App) handleSubscriptionItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	id := pathInt(r.URL.Path, "/subscriptions/")
	if strings.HasSuffix(r.URL.Path, "/cancel") && r.Method == http.MethodPost {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		sub, err := dbSubscriptionCancel(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"subscription": sub})
		return
	}
	if r.Method == http.MethodGet {
		sub, err := dbSubscriptionGet(ctx.AppDB(), pid, id, true)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		if sub == nil {
			httpErr(w, 404, "subscription not found")
			return
		}
		httpJSON(w, map[string]any{"subscription": sub})
		return
	}
	if r.Method == http.MethodPatch {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		sub, err := dbSubscriptionUpdateStatus(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"subscription": sub})
		return
	}
	httpErr(w, 405, "method not allowed")
}

func (a *App) handleCycles(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		subID, _ := strconv.ParseInt(r.URL.Query().Get("subscription_id"), 10, 64)
		out, err := dbCyclesList(ctx.AppDB(), pid, subID, 100)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, map[string]any{"cycles": out, "count": len(out)})
		return
	}
	if r.Method == http.MethodPost {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid JSON body")
			return
		}
		c, sub, err := dbCycleCreate(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"cycle": c, "subscription": sub})
		return
	}
	httpErr(w, 405, "method not allowed")
}

func (a *App) handleCycleItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method != http.MethodPatch {
		httpErr(w, 405, "method not allowed")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	body["id"] = pathInt(r.URL.Path, "/cycles/")
	c, err := dbCycleUpdate(ctx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, map[string]any{"cycle": c})
}

type rowScanner interface{ Scan(dest ...any) error }

type itemIn struct {
	CatalogProductID, CatalogPriceID *int64
	SKU, Title                       string
	Quantity                         float64
	UnitAmountCents                  int64
	Currency                         string
	Metadata                         any
}

func normalizeItems(raw []any, currency string) []itemIn {
	out := []itemIn{}
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		it := itemIn{SKU: strArg(m, "sku"), Title: firstNonEmpty(strArg(m, "title"), strArg(m, "description"), strArg(m, "name")), Quantity: float64Arg(m, "quantity", 1), UnitAmountCents: firstNonZero(int64Arg(m, "unit_amount_cents"), int64Arg(m, "unit_price_cents")), Currency: strings.ToUpper(firstNonEmpty(strArg(m, "currency"), currency)), Metadata: m["metadata"]}
		if id := firstNonZero(int64Arg(m, "catalog_product_id"), int64Arg(m, "product_id")); id != 0 {
			it.CatalogProductID = &id
		}
		if id := firstNonZero(int64Arg(m, "catalog_price_id"), int64Arg(m, "price_id")); id != 0 {
			it.CatalogPriceID = &id
		}
		if it.Title != "" && it.Quantity > 0 {
			out = append(out, it)
		}
	}
	return out
}

var validKind = map[string]bool{"saas": true, "physical": true, "service": true}
var validSubStatus = map[string]bool{"trialing": true, "active": true, "past_due": true, "paused": true, "cancelled": true, "ended": true}

func resolveProjectFromArgs(args map[string]any) (string, error) {
	pid := strings.TrimSpace(strArg(args, "_project_id"))
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id required")
	}
	return pid, nil
}
func resolveProjectFromRequest(r *http.Request) (string, error) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id query parameter required")
	}
	return pid, nil
}
func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
func int64Arg(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}
func float64Arg(m map[string]any, key string, def float64) float64 {
	if m == nil {
		return def
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return n
		}
	}
	return def
}
func boolArg(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}
func mapFromAny(v any) map[string]any {
	out := map[string]any{}
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			out[k] = vv
		}
	case map[string]string:
		for k, vv := range x {
			out[k] = vv
		}
	case json.RawMessage:
		_ = json.Unmarshal(x, &out)
	case []byte:
		_ = json.Unmarshal(x, &out)
	case string:
		if strings.TrimSpace(x) != "" {
			_ = json.Unmarshal([]byte(x), &out)
		}
	}
	return out
}
func trialExpiredStatus(meta map[string]any) string {
	switch strings.ToLower(firstNonEmpty(strArg(meta, "trial_end_status"), strArg(meta, "on_trial_end_unpaid"))) {
	case "pause", "paused", "suspend", "suspended":
		return "paused"
	case "end", "ended":
		return "ended"
	default:
		return "past_due"
	}
}
func parseTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		t, err := time.Parse(layout, value)
		if err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}
func nullableInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
func nullablePtr(p *int64) any {
	if p == nil || *p == 0 {
		return nil
	}
	return *p
}
func nullableTime(ok bool, value string) any {
	if !ok {
		return nil
	}
	return value
}
func ptrIfValid(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
func actorOrSystem(s string) string {
	if strings.TrimSpace(s) == "" {
		return "system"
	}
	return s
}
func jsonOrEmpty(v any, sentinel string) string {
	if v == nil {
		return sentinel
	}
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return sentinel
		}
		return t
	case []byte:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	case json.RawMessage:
		if len(t) == 0 {
			return sentinel
		}
		return string(t)
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return sentinel
	}
	return string(raw)
}
func firstErr(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
func clampLimit(n, max int) int {
	if n <= 0 {
		return 50
	}
	if n > max {
		return max
	}
	return n
}
func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.SplitN(rest, "/", 2)[0]
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}
func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil || ctx.Config() == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}
func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }
