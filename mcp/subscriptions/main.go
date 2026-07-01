package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: subscriptions
display_name: Subscriptions
version: 0.2.0
description: Generic recurring-commerce lifecycle, subscription items, and metered usage for SaaS, physical subscriptions, and services.
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
  ui_panels:
    - slot: project.page
      label: Subscriptions
      icon: repeat
      entry: /ui/SubscriptionsPanel.mjs
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
    - name: subscription.item.created
      description: A subscription item was created.
    - name: subscription.usage.recorded
      description: Metered subscription usage was recorded.
    - name: subscription.invoice.requested
      description: A subscription period invoice was requested.
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
	ctx.Logger().Info("subscriptions mounted", "version", "0.2.0", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
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
		{Pattern: "/metrics", Handler: a.handleMetrics},
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
		{Name: "subscriptions_metrics_get", Description: "Return recurring subscription metrics such as MRR by currency. Args: source, statuses, include_trialing.", InputSchema: schemaObject(map[string]any{
			"source": map[string]any{"type": "string"}, "statuses": map[string]any{"type": "array"}, "include_trialing": map[string]any{"type": "boolean"},
		}, nil), Handler: a.toolSubscriptionsMetricsGet},
		{Name: "subscriptions_update_status", Description: "Update subscription status/periods.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "current_period_start": map[string]any{"type": "string"}, "current_period_end": map[string]any{"type": "string"}, "next_renewal_at": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"},
		}, []string{"id"}), Handler: a.toolSubscriptionsUpdateStatus},
		{Name: "subscriptions_cancel", Description: "Cancel a subscription.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "at_period_end": map[string]any{"type": "boolean"}, "reason": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolSubscriptionsCancel},
		{Name: "subscription_items_create", Description: "Add a flat or metered item to a subscription.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "product_id": map[string]any{"type": "integer"}, "price_id": map[string]any{"type": "integer"}, "sku": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "quantity": map[string]any{"type": "number"}, "unit_amount_cents": map[string]any{"type": "integer"}, "currency": map[string]any{"type": "string"}, "billing_scheme": map[string]any{"type": "string"}, "meter_key": map[string]any{"type": "string"}, "included_units": map[string]any{"type": "integer"}, "unit_size": map[string]any{"type": "integer"}, "metadata": map[string]any{"type": "object"},
		}, []string{"subscription_id", "title"}), Handler: a.toolSubscriptionItemsCreate},
		{Name: "subscription_items_list", Description: "List items for a subscription.", InputSchema: schemaObject(map[string]any{"subscription_id": map[string]any{"type": "integer"}}, []string{"subscription_id"}), Handler: a.toolSubscriptionItemsList},
		{Name: "subscription_items_update", Description: "Patch a subscription item status, quantity, included_units, unit_size, unit_amount_cents, or metadata.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "quantity": map[string]any{"type": "number"}, "included_units": map[string]any{"type": "integer"}, "unit_size": map[string]any{"type": "integer"}, "unit_amount_cents": map[string]any{"type": "integer"}, "metadata": map[string]any{"type": "object"},
		}, []string{"id"}), Handler: a.toolSubscriptionItemsUpdate},
		{Name: "subscription_items_cancel", Description: "Cancel a subscription item.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "reason": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolSubscriptionItemsCancel},
		{Name: "subscriptions_usage_record", Description: "Record metered usage for a subscription item. Args: subscription_item_id OR subscription_id+meter_key, quantity, subject_type, subject_id, occurred_at, idempotency_key, metadata.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "subscription_item_id": map[string]any{"type": "integer"}, "meter_key": map[string]any{"type": "string"}, "subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"}, "quantity": map[string]any{"type": "integer"}, "occurred_at": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"},
		}, []string{"quantity", "idempotency_key"}), Handler: a.toolSubscriptionsUsageRecord},
		{Name: "subscriptions_usage_summary", Description: "Summarize metered usage and billable overage for a subscription item and period.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "subscription_item_id": map[string]any{"type": "integer"}, "meter_key": map[string]any{"type": "string"}, "period_start": map[string]any{"type": "string"}, "period_end": map[string]any{"type": "string"},
		}, []string{"period_start", "period_end"}), Handler: a.toolSubscriptionsUsageSummary},
		{Name: "subscriptions_invoice_prepare", Description: "Prepare generic Billing line items for a subscription period, including flat items and metered overage.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "period_start": map[string]any{"type": "string"}, "period_end": map[string]any{"type": "string"}, "include_flat": map[string]any{"type": "boolean"}, "include_metered": map[string]any{"type": "boolean"}, "invoice_zero_usage": map[string]any{"type": "boolean"},
		}, []string{"subscription_id", "period_start", "period_end"}), Handler: a.toolSubscriptionsInvoicePrepare},
		{Name: "subscriptions_invoice_create", Description: "Create a Billing invoice from prepared subscription period lines.", InputSchema: schemaObject(map[string]any{
			"subscription_id": map[string]any{"type": "integer"}, "period_start": map[string]any{"type": "string"}, "period_end": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "due_date": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"}, "finalize": map[string]any{"type": "boolean"}, "include_flat": map[string]any{"type": "boolean"}, "include_metered": map[string]any{"type": "boolean"}, "invoice_zero_usage": map[string]any{"type": "boolean"}, "metadata": map[string]any{"type": "object"},
		}, []string{"subscription_id", "period_start", "period_end"}), Handler: a.toolSubscriptionsInvoiceCreate},
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
	BillingScheme    string          `json:"billing_scheme"`
	MeterKey         string          `json:"meter_key,omitempty"`
	IncludedUnits    int64           `json:"included_units"`
	UnitSize         int64           `json:"unit_size"`
	Status           string          `json:"status"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type UsageRecord struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	SubscriptionID     int64           `json:"subscription_id"`
	SubscriptionItemID int64           `json:"subscription_item_id"`
	MeterKey           string          `json:"meter_key"`
	SubjectType        string          `json:"subject_type,omitempty"`
	SubjectID          string          `json:"subject_id,omitempty"`
	Quantity           int64           `json:"quantity"`
	OccurredAt         string          `json:"occurred_at"`
	IdempotencyKey     string          `json:"idempotency_key"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          string          `json:"created_at"`
	Deduped            bool            `json:"deduped,omitempty"`
}

type UsageSummary struct {
	ProjectID          string   `json:"project_id"`
	SubscriptionID     int64    `json:"subscription_id"`
	SubscriptionItemID int64    `json:"subscription_item_id"`
	MeterKey           string   `json:"meter_key"`
	PeriodStart        string   `json:"period_start"`
	PeriodEnd          string   `json:"period_end"`
	IncludedUnits      int64    `json:"included_units"`
	TotalQuantity      int64    `json:"total_quantity"`
	BillableQuantity   int64    `json:"billable_quantity"`
	UnitAmountCents    int64    `json:"unit_amount_cents"`
	UnitSize           int64    `json:"unit_size"`
	QuantityUnits      float64  `json:"quantity_units"`
	Currency           string   `json:"currency"`
	Item               *SubItem `json:"item,omitempty"`
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

type SubscriptionMetricCurrency struct {
	Currency             string `json:"currency"`
	MRRCents             int64  `json:"mrr_cents"`
	RecurringAmountCents int64  `json:"recurring_amount_cents"`
	Subscriptions        int64  `json:"subscriptions"`
}

type SubscriptionMetrics struct {
	ProjectID     string                       `json:"project_id"`
	Source        string                       `json:"source,omitempty"`
	Statuses      []string                     `json:"statuses"`
	Currencies    []SubscriptionMetricCurrency `json:"currencies"`
	Subscriptions int64                        `json:"subscriptions"`
	GeneratedAt   string                       `json:"generated_at"`
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

func (a *App) toolSubscriptionsMetricsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	metrics, err := dbSubscriptionMetrics(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"metrics": metrics}, nil
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

func (a *App) toolSubscriptionItemsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	item, err := dbSubscriptionItemCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.item.created", map[string]any{"subscription_id": item.SubscriptionID, "subscription_item_id": item.ID, "billing_scheme": item.BillingScheme, "meter_key": item.MeterKey})
	return map[string]any{"item": item}, nil
}

func (a *App) toolSubscriptionItemsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sub, err := dbSubscriptionGet(ctx.AppDB(), pid, int64Arg(args, "subscription_id"), false)
	if err != nil || sub == nil {
		return nil, firstErr(err, errors.New("subscription not found"))
	}
	items, err := dbItemsList(ctx.AppDB(), sub.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "count": len(items)}, nil
}

func (a *App) toolSubscriptionItemsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	item, err := dbSubscriptionItemUpdate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": item}, nil
}

func (a *App) toolSubscriptionItemsCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	args["status"] = "cancelled"
	return a.toolSubscriptionItemsUpdate(ctx, args)
}

func (a *App) toolSubscriptionsUsageRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rec, err := dbUsageRecord(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.usage.recorded", map[string]any{"subscription_id": rec.SubscriptionID, "subscription_item_id": rec.SubscriptionItemID, "meter_key": rec.MeterKey, "quantity": rec.Quantity, "deduped": rec.Deduped})
	return map[string]any{"usage": rec}, nil
}

func (a *App) toolSubscriptionsUsageSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	summary, err := dbUsageSummary(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"summary": summary}, nil
}

func (a *App) toolSubscriptionsInvoicePrepare(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareSubscriptionInvoice(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (a *App) toolSubscriptionsInvoiceCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareSubscriptionInvoice(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	sub := prepared["subscription"].(*Subscription)
	if sub.CustomerID == nil || *sub.CustomerID == 0 {
		return nil, errors.New("subscription has no billing customer_id")
	}
	lineItems, _ := prepared["line_items"].([]any)
	if len(lineItems) == 0 {
		return nil, errors.New("no invoice lines for period")
	}
	meta := mapFromAny(args["metadata"])
	meta["source_app"] = "subscriptions"
	meta["subscription_id"] = sub.ID
	meta["period_start"] = prepared["period_start"]
	meta["period_end"] = prepared["period_end"]
	invoiceArgs := map[string]any{
		"_project_id": pid,
		"customer_id": *sub.CustomerID,
		"currency":    sub.Currency,
		"provider":    firstNonEmpty(strArg(args, "provider"), "local"),
		"due_date":    strArg(args, "due_date"),
		"notes":       strArg(args, "notes"),
		"line_items":  lineItems,
		"metadata":    meta,
	}
	invOut, err := callBillingInvoiceCreate(ctx, invoiceArgs, boolArg(args, "finalize"))
	if err != nil {
		return nil, err
	}
	ctx.Emit("subscription.invoice.requested", map[string]any{"subscription_id": sub.ID, "period_start": prepared["period_start"], "period_end": prepared["period_end"]})
	return map[string]any{"subscription": sub, "prepared": prepared, "invoice": invOut["invoice"]}, nil
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
		if err := validateItem(it); err != nil {
			return nil, err
		}
		_, err := tx.Exec(
			`INSERT INTO subscription_items
			   (subscription_id, position, catalog_product_id, catalog_price_id, sku, title, quantity, unit_amount_cents, currency,
			    billing_scheme, meter_key, included_units, unit_size, status, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, nullablePtr(it.CatalogProductID), nullablePtr(it.CatalogPriceID), nullStr(it.SKU), it.Title,
			it.Quantity, it.UnitAmountCents, strings.ToUpper(firstNonEmpty(it.Currency, currency)),
			it.BillingScheme, it.MeterKey, it.IncludedUnits, it.UnitSize, it.Status, jsonOrEmpty(it.Metadata, "{}"))
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

func dbSubscriptionMetrics(db *sql.DB, pid string, args map[string]any) (*SubscriptionMetrics, error) {
	statuses := metricStatuses(args)
	where := []string{"s.project_id = ?"}
	qargs := []any{pid}
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			placeholders = append(placeholders, "?")
			qargs = append(qargs, status)
		}
		where = append(where, "s.status IN ("+strings.Join(placeholders, ",")+")")
	}
	source := strings.TrimSpace(strArg(args, "source"))
	if source != "" {
		where = append(where, "s.source = ?")
		qargs = append(qargs, source)
	}
	rows, err := db.Query(`
		SELECT s.id, s.currency, s.interval, s.interval_count, COALESCE(SUM(si.unit_amount_cents * si.quantity), 0)
		  FROM subscriptions s
		  JOIN subscription_items si ON si.subscription_id = s.id
		 WHERE `+strings.Join(where, " AND ")+`
		   AND si.status = 'active'
		   AND si.billing_scheme = 'flat'
		 GROUP BY s.id, s.currency, s.interval, s.interval_count
		 ORDER BY s.id`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type bucket struct {
		mrrCents       int64
		recurringCents int64
		subscriptions  int64
	}
	byCurrency := map[string]*bucket{}
	var subscriptionCount int64
	for rows.Next() {
		var id int64
		var currency, interval string
		var intervalCount int64
		var amount float64
		if err := rows.Scan(&id, &currency, &interval, &intervalCount, &amount); err != nil {
			return nil, err
		}
		monthly := monthlyNormalizedCents(amount, interval, intervalCount)
		if monthly == 0 {
			continue
		}
		cur := strings.ToUpper(firstNonEmpty(currency, "USD"))
		b := byCurrency[cur]
		if b == nil {
			b = &bucket{}
			byCurrency[cur] = b
		}
		b.mrrCents += monthly
		b.recurringCents += int64(math.Round(amount))
		b.subscriptions++
		subscriptionCount++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	currencies := make([]SubscriptionMetricCurrency, 0, len(byCurrency))
	for cur, b := range byCurrency {
		currencies = append(currencies, SubscriptionMetricCurrency{
			Currency:             cur,
			MRRCents:             b.mrrCents,
			RecurringAmountCents: b.recurringCents,
			Subscriptions:        b.subscriptions,
		})
	}
	sortMetricCurrencies(currencies)
	return &SubscriptionMetrics{
		ProjectID:     pid,
		Source:        source,
		Statuses:      statuses,
		Currencies:    currencies,
		Subscriptions: subscriptionCount,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
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

func dbSubscriptionItemCreate(db *sql.DB, pid string, args map[string]any) (*SubItem, error) {
	subID := int64Arg(args, "subscription_id")
	sub, err := dbSubscriptionGet(db, pid, subID, false)
	if err != nil || sub == nil {
		return nil, firstErr(err, errors.New("subscription not found"))
	}
	it := normalizeItemMap(args, sub.Currency)
	if it.Title == "" {
		return nil, errors.New("title required")
	}
	if it.Quantity <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	if err := validateItem(it); err != nil {
		return nil, err
	}
	if id := firstNonZero(int64Arg(args, "catalog_product_id"), int64Arg(args, "product_id")); id != 0 {
		it.CatalogProductID = &id
	}
	if id := firstNonZero(int64Arg(args, "catalog_price_id"), int64Arg(args, "price_id")); id != 0 {
		it.CatalogPriceID = &id
	}
	next := 0
	_ = db.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM subscription_items WHERE subscription_id=?`, subID).Scan(&next)
	var id int64
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(
		`INSERT INTO subscription_items
		   (subscription_id, position, catalog_product_id, catalog_price_id, sku, title, quantity, unit_amount_cents, currency,
		    billing_scheme, meter_key, included_units, unit_size, status, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		subID, next, nullablePtr(it.CatalogProductID), nullablePtr(it.CatalogPriceID), nullStr(it.SKU), it.Title, it.Quantity,
		it.UnitAmountCents, it.Currency, it.BillingScheme, it.MeterKey, it.IncludedUnits, it.UnitSize, it.Status, jsonOrEmpty(it.Metadata, "{}"),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	if err := writeEventTx(tx, pid, subID, "system", "subscription.item_created", map[string]any{"subscription_item_id": id, "billing_scheme": it.BillingScheme, "meter_key": it.MeterKey}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbSubscriptionItemGet(db, pid, id)
}

func validateItem(it itemIn) error {
	switch it.BillingScheme {
	case "flat":
		if it.MeterKey != "" {
			return errors.New("flat items cannot set meter_key")
		}
	case "metered":
		if strings.TrimSpace(it.MeterKey) == "" {
			return errors.New("meter_key required for metered items")
		}
	default:
		return fmt.Errorf("invalid billing_scheme %q", it.BillingScheme)
	}
	if it.UnitSize <= 0 {
		return errors.New("unit_size must be > 0")
	}
	if it.IncludedUnits < 0 {
		return errors.New("included_units cannot be negative")
	}
	return nil
}

func dbSubscriptionItemUpdate(db *sql.DB, pid string, args map[string]any) (*SubItem, error) {
	id := int64Arg(args, "id")
	item, err := dbSubscriptionItemGet(db, pid, id)
	if err != nil || item == nil {
		return nil, firstErr(err, errors.New("subscription item not found"))
	}
	status := firstNonEmpty(strArg(args, "status"), item.Status)
	switch status {
	case "active", "paused", "cancelled":
	default:
		return nil, fmt.Errorf("invalid item status %q", status)
	}
	quantity := item.Quantity
	if _, ok := args["quantity"]; ok {
		quantity = float64Arg(args, "quantity", item.Quantity)
	}
	if quantity <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	included := item.IncludedUnits
	if _, ok := args["included_units"]; ok {
		included = int64Arg(args, "included_units")
	}
	if included < 0 {
		return nil, errors.New("included_units cannot be negative")
	}
	unitSize := item.UnitSize
	if _, ok := args["unit_size"]; ok {
		unitSize = int64Arg(args, "unit_size")
	}
	if unitSize <= 0 {
		return nil, errors.New("unit_size must be > 0")
	}
	unitAmount := item.UnitAmountCents
	if _, ok := args["unit_amount_cents"]; ok {
		unitAmount = int64Arg(args, "unit_amount_cents")
	}
	meta := item.Metadata
	if _, ok := args["metadata"]; ok {
		meta = json.RawMessage(jsonOrEmpty(args["metadata"], "{}"))
	}
	_, err = db.Exec(
		`UPDATE subscription_items
		    SET status=?, quantity=?, included_units=?, unit_size=?, unit_amount_cents=?, metadata=?, updated_at=CURRENT_TIMESTAMP
		  WHERE id=? AND subscription_id IN (SELECT id FROM subscriptions WHERE project_id=?)`,
		status, quantity, included, unitSize, unitAmount, string(meta), id, pid)
	if err != nil {
		return nil, err
	}
	return dbSubscriptionItemGet(db, pid, id)
}

func dbSubscriptionItemGet(db *sql.DB, pid string, id int64) (*SubItem, error) {
	if id == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT si.id, si.subscription_id, si.position, si.catalog_product_id, si.catalog_price_id, COALESCE(si.sku,''), si.title, si.quantity, si.unit_amount_cents, si.currency, si.billing_scheme, si.meter_key, si.included_units, si.unit_size, si.status, si.metadata
		FROM subscription_items si JOIN subscriptions s ON s.id=si.subscription_id
		WHERE si.id=? AND s.project_id=?`, id, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	item, err := scanItemRows(rows)
	if err != nil {
		return nil, err
	}
	return item, rows.Err()
}

func dbSubscriptionItemResolve(db *sql.DB, pid string, args map[string]any) (*SubItem, error) {
	if id := int64Arg(args, "subscription_item_id"); id != 0 {
		return dbSubscriptionItemGet(db, pid, id)
	}
	subID := int64Arg(args, "subscription_id")
	meter := strArg(args, "meter_key")
	if subID == 0 || meter == "" {
		return nil, errors.New("subscription_item_id or subscription_id+meter_key required")
	}
	rows, err := db.Query(`SELECT si.id, si.subscription_id, si.position, si.catalog_product_id, si.catalog_price_id, COALESCE(si.sku,''), si.title, si.quantity, si.unit_amount_cents, si.currency, si.billing_scheme, si.meter_key, si.included_units, si.unit_size, si.status, si.metadata
		FROM subscription_items si JOIN subscriptions s ON s.id=si.subscription_id
		WHERE s.project_id=? AND si.subscription_id=? AND si.meter_key=? AND si.billing_scheme='metered' AND si.status='active'
		ORDER BY si.id LIMIT 1`, pid, subID, meter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	item, err := scanItemRows(rows)
	if err != nil {
		return nil, err
	}
	return item, rows.Err()
}

func dbUsageRecord(db *sql.DB, pid string, args map[string]any) (*UsageRecord, error) {
	item, err := dbSubscriptionItemResolve(db, pid, args)
	if err != nil || item == nil {
		return nil, firstErr(err, errors.New("metered subscription item not found"))
	}
	if item.BillingScheme != "metered" {
		return nil, errors.New("usage can only be recorded against metered items")
	}
	if item.Status != "active" {
		return nil, fmt.Errorf("subscription item is %s", item.Status)
	}
	qty := int64Arg(args, "quantity")
	if qty <= 0 {
		return nil, errors.New("quantity must be > 0")
	}
	idem := strArg(args, "idempotency_key")
	if idem == "" {
		return nil, errors.New("idempotency_key required")
	}
	occurred := firstNonEmpty(strArg(args, "occurred_at"), time.Now().UTC().Format(time.RFC3339))
	if _, ok := parseTime(occurred); !ok {
		return nil, errors.New("occurred_at must be RFC3339")
	}
	res, err := db.Exec(
		`INSERT INTO subscription_usage_records
		   (project_id, subscription_id, subscription_item_id, meter_key, subject_type, subject_id, quantity, occurred_at, idempotency_key, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, idempotency_key) DO NOTHING`,
		pid, item.SubscriptionID, item.ID, item.MeterKey, strArg(args, "subject_type"), strArg(args, "subject_id"), qty, occurred, idem, jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	rec, err := dbUsageGetByIdempotency(db, pid, idem)
	if err != nil {
		return nil, err
	}
	if rec != nil && (rec.SubscriptionItemID != item.ID || rec.Quantity != qty) {
		rec.Deduped = true
	} else if rec != nil {
		n, _ := res.RowsAffected()
		rec.Deduped = n == 0
	}
	return rec, nil
}

func dbUsageGetByIdempotency(db *sql.DB, pid, key string) (*UsageRecord, error) {
	return scanUsage(db.QueryRow(`SELECT id, project_id, subscription_id, subscription_item_id, meter_key, subject_type, subject_id, quantity, occurred_at, idempotency_key, metadata, created_at FROM subscription_usage_records WHERE project_id=? AND idempotency_key=?`, pid, key))
}

func dbUsageSummary(db *sql.DB, pid string, args map[string]any) (*UsageSummary, error) {
	item, err := dbSubscriptionItemResolve(db, pid, args)
	if err != nil || item == nil {
		return nil, firstErr(err, errors.New("metered subscription item not found"))
	}
	start, end, err := periodArgs(args)
	if err != nil {
		return nil, err
	}
	var total int64
	err = db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM subscription_usage_records WHERE project_id=? AND subscription_item_id=? AND occurred_at >= ? AND occurred_at < ?`, pid, item.ID, start.Format(time.RFC3339), end.Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return nil, err
	}
	billable := total - item.IncludedUnits
	if billable < 0 {
		billable = 0
	}
	qtyUnits := float64(billable) / float64(item.UnitSize)
	if billable > 0 {
		qtyUnits = math.Ceil(qtyUnits)
	}
	return &UsageSummary{
		ProjectID:          pid,
		SubscriptionID:     item.SubscriptionID,
		SubscriptionItemID: item.ID,
		MeterKey:           item.MeterKey,
		PeriodStart:        start.Format(time.RFC3339),
		PeriodEnd:          end.Format(time.RFC3339),
		IncludedUnits:      item.IncludedUnits,
		TotalQuantity:      total,
		BillableQuantity:   billable,
		UnitAmountCents:    item.UnitAmountCents,
		UnitSize:           item.UnitSize,
		QuantityUnits:      qtyUnits,
		Currency:           item.Currency,
		Item:               item,
	}, nil
}

func prepareSubscriptionInvoice(db *sql.DB, pid string, args map[string]any) (map[string]any, error) {
	sub, err := dbSubscriptionGet(db, pid, int64Arg(args, "subscription_id"), true)
	if err != nil || sub == nil {
		return nil, firstErr(err, errors.New("subscription not found"))
	}
	start, end, err := periodArgs(args)
	if err != nil {
		return nil, err
	}
	includeFlat := true
	if _, ok := args["include_flat"]; ok {
		includeFlat = boolArg(args, "include_flat")
	}
	includeMetered := true
	if _, ok := args["include_metered"]; ok {
		includeMetered = boolArg(args, "include_metered")
	}
	lines := []any{}
	summaries := []any{}
	for _, item := range sub.Items {
		if item.Status != "active" {
			continue
		}
		if item.BillingScheme == "flat" && includeFlat {
			line := map[string]any{
				"description":      item.Title,
				"quantity":         item.Quantity,
				"unit_price_cents": item.UnitAmountCents,
				"price_id":         derefInt64(item.CatalogPriceID),
				"product_id":       derefInt64(item.CatalogProductID),
				"metadata": map[string]any{
					"source_app":           "subscriptions",
					"subscription_id":      sub.ID,
					"subscription_item_id": item.ID,
					"billing_scheme":       item.BillingScheme,
					"period_start":         start.Format(time.RFC3339),
					"period_end":           end.Format(time.RFC3339),
				},
			}
			lines = append(lines, line)
			continue
		}
		if item.BillingScheme != "metered" || !includeMetered {
			continue
		}
		summary, err := dbUsageSummary(db, pid, map[string]any{"subscription_item_id": item.ID, "period_start": start.Format(time.RFC3339), "period_end": end.Format(time.RFC3339)})
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
		if summary.BillableQuantity == 0 && !boolArg(args, "invoice_zero_usage") {
			continue
		}
		line := map[string]any{
			"description":      fmt.Sprintf("%s overage %s to %s", item.Title, start.Format("2006-01-02"), end.Format("2006-01-02")),
			"quantity":         summary.QuantityUnits,
			"unit_price_cents": item.UnitAmountCents,
			"price_id":         derefInt64(item.CatalogPriceID),
			"product_id":       derefInt64(item.CatalogProductID),
			"metadata": map[string]any{
				"source_app":           "subscriptions",
				"subscription_id":      sub.ID,
				"subscription_item_id": item.ID,
				"billing_scheme":       item.BillingScheme,
				"meter_key":            item.MeterKey,
				"period_start":         start.Format(time.RFC3339),
				"period_end":           end.Format(time.RFC3339),
				"included_units":       item.IncludedUnits,
				"total_quantity":       summary.TotalQuantity,
				"billable_quantity":    summary.BillableQuantity,
				"unit_size":            item.UnitSize,
			},
		}
		lines = append(lines, line)
	}
	return map[string]any{"subscription": sub, "line_items": lines, "usage_summaries": summaries, "period_start": start.Format(time.RFC3339), "period_end": end.Format(time.RFC3339), "currency": sub.Currency}, nil
}

func callBillingInvoiceCreate(ctx *sdk.AppCtx, args map[string]any, finalize bool) (map[string]any, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("billing app call requires platform API")
	}
	args["finalize"] = finalize
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_create_from_prepared_lines", withProject(ctx.CurrentProject(), args), &out); err != nil {
		return nil, fmt.Errorf("create billing invoice: %w", err)
	}
	return out, nil
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
		if it.Status == "active" && it.BillingScheme == "flat" {
			subtotal += int64(float64(it.UnitAmountCents) * it.Quantity)
		}
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

func metricStatuses(args map[string]any) []string {
	raw := arrayArg(args, "statuses")
	statuses := make([]string, 0, len(raw)+2)
	seen := map[string]bool{}
	for _, v := range raw {
		status := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		if validSubStatus[status] && !seen[status] {
			statuses = append(statuses, status)
			seen[status] = true
		}
	}
	if status := strings.ToLower(strings.TrimSpace(strArg(args, "status"))); status != "" && validSubStatus[status] && !seen[status] {
		statuses = append(statuses, status)
		seen[status] = true
	}
	if len(statuses) == 0 {
		statuses = append(statuses, "active")
		seen["active"] = true
	}
	if boolArg(args, "include_trialing") && !seen["trialing"] {
		statuses = append(statuses, "trialing")
	}
	return statuses
}

func monthlyNormalizedCents(amount float64, interval string, intervalCount int64) int64 {
	if amount <= 0 {
		return 0
	}
	if intervalCount <= 0 {
		intervalCount = 1
	}
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "month", "monthly":
		return int64(math.Round(amount / float64(intervalCount)))
	case "year", "yearly", "annual", "annually":
		return int64(math.Round(amount / 12 / float64(intervalCount)))
	case "week", "weekly":
		return int64(math.Round(amount * 52 / 12 / float64(intervalCount)))
	case "day", "daily":
		return int64(math.Round(amount * 365 / 12 / float64(intervalCount)))
	default:
		return 0
	}
}

func sortMetricCurrencies(currencies []SubscriptionMetricCurrency) {
	sort.Slice(currencies, func(i, j int) bool {
		return currencies[i].Currency < currencies[j].Currency
	})
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
	rows, err := db.Query(`SELECT id, subscription_id, position, catalog_product_id, catalog_price_id, COALESCE(sku,''), title, quantity, unit_amount_cents, currency, billing_scheme, meter_key, included_units, unit_size, status, metadata FROM subscription_items WHERE subscription_id=? ORDER BY position,id`, subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SubItem
	for rows.Next() {
		var it SubItem
		var pid, price sql.NullInt64
		var meta string
		if err := rows.Scan(&it.ID, &it.SubscriptionID, &it.Position, &pid, &price, &it.SKU, &it.Title, &it.Quantity, &it.UnitAmountCents, &it.Currency, &it.BillingScheme, &it.MeterKey, &it.IncludedUnits, &it.UnitSize, &it.Status, &meta); err != nil {
			return nil, err
		}
		it.CatalogProductID = ptrIfValid(pid)
		it.CatalogPriceID = ptrIfValid(price)
		if it.BillingScheme == "" {
			it.BillingScheme = "flat"
		}
		if it.UnitSize <= 0 {
			it.UnitSize = 1
		}
		if it.Status == "" {
			it.Status = "active"
		}
		it.Metadata = json.RawMessage(meta)
		out = append(out, &it)
	}
	return out, rows.Err()
}

func scanItemRows(rows *sql.Rows) (*SubItem, error) {
	var it SubItem
	var pid, price sql.NullInt64
	var meta string
	if err := rows.Scan(&it.ID, &it.SubscriptionID, &it.Position, &pid, &price, &it.SKU, &it.Title, &it.Quantity, &it.UnitAmountCents, &it.Currency, &it.BillingScheme, &it.MeterKey, &it.IncludedUnits, &it.UnitSize, &it.Status, &meta); err != nil {
		return nil, err
	}
	it.CatalogProductID = ptrIfValid(pid)
	it.CatalogPriceID = ptrIfValid(price)
	if it.BillingScheme == "" {
		it.BillingScheme = "flat"
	}
	if it.UnitSize <= 0 {
		it.UnitSize = 1
	}
	if it.Status == "" {
		it.Status = "active"
	}
	it.Metadata = json.RawMessage(firstNonEmpty(meta, "{}"))
	return &it, nil
}

func scanUsage(row rowScanner) (*UsageRecord, error) {
	var rec UsageRecord
	var meta string
	if err := row.Scan(&rec.ID, &rec.ProjectID, &rec.SubscriptionID, &rec.SubscriptionItemID, &rec.MeterKey, &rec.SubjectType, &rec.SubjectID, &rec.Quantity, &rec.OccurredAt, &rec.IdempotencyKey, &meta, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rec.Metadata = json.RawMessage(firstNonEmpty(meta, "{}"))
	return &rec, nil
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

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, 405, "method not allowed")
		return
	}
	q := r.URL.Query()
	args := map[string]any{
		"source":           q.Get("source"),
		"status":           q.Get("status"),
		"include_trialing": q.Get("include_trialing"),
	}
	if statuses := q["statuses"]; len(statuses) > 0 {
		raw := make([]any, 0, len(statuses))
		for _, status := range statuses {
			for _, part := range strings.Split(status, ",") {
				if strings.TrimSpace(part) != "" {
					raw = append(raw, strings.TrimSpace(part))
				}
			}
		}
		args["statuses"] = raw
	}
	metrics, err := dbSubscriptionMetrics(ctx.AppDB(), pid, args)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	httpJSON(w, map[string]any{"metrics": metrics})
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
	BillingScheme                    string
	MeterKey                         string
	IncludedUnits                    int64
	UnitSize                         int64
	Status                           string
	Metadata                         any
}

func normalizeItems(raw []any, currency string) []itemIn {
	out := []itemIn{}
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		it := normalizeItemMap(m, currency)
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

func normalizeItemMap(m map[string]any, currency string) itemIn {
	scheme := strings.ToLower(firstNonEmpty(strArg(m, "billing_scheme"), "flat"))
	if scheme != "metered" {
		scheme = "flat"
	}
	unitSize := int64Arg(m, "unit_size")
	if unitSize <= 0 {
		unitSize = 1
	}
	status := firstNonEmpty(strArg(m, "status"), "active")
	return itemIn{
		SKU:             strArg(m, "sku"),
		Title:           firstNonEmpty(strArg(m, "title"), strArg(m, "description"), strArg(m, "name")),
		Quantity:        float64Arg(m, "quantity", 1),
		UnitAmountCents: firstNonZero(int64Arg(m, "unit_amount_cents"), int64Arg(m, "unit_price_cents")),
		Currency:        strings.ToUpper(firstNonEmpty(strArg(m, "currency"), currency)),
		BillingScheme:   scheme,
		MeterKey:        strArg(m, "meter_key"),
		IncludedUnits:   int64Arg(m, "included_units"),
		UnitSize:        unitSize,
		Status:          status,
		Metadata:        m["metadata"],
	}
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
func arrayArg(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	switch v := m[key].(type) {
	case []any:
		return v
	case []string:
		out := make([]any, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			if strings.TrimSpace(part) != "" {
				out = append(out, strings.TrimSpace(part))
			}
		}
		return out
	default:
		return nil
	}
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
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		t, err := time.Parse(layout, value)
		if err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
func periodArgs(args map[string]any) (time.Time, time.Time, error) {
	start, ok := parseTime(strArg(args, "period_start"))
	if !ok {
		return time.Time{}, time.Time{}, errors.New("period_start must be RFC3339 or YYYY-MM-DD")
	}
	end, ok := parseTime(strArg(args, "period_end"))
	if !ok {
		return time.Time{}, time.Time{}, errors.New("period_end must be RFC3339 or YYYY-MM-DD")
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, errors.New("period_end must be after period_start")
	}
	return start, end, nil
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
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
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

func withProject(projectID string, args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		out[k] = v
	}
	if strings.TrimSpace(projectID) != "" {
		out["_project_id"] = projectID
	}
	return out
}
