// Hosting app: managed container-backed Apteva tenants.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

const appVersion = "1.9.0"

const (
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusSuspended    = "suspended"
	StatusStopped      = "stopped"
	StatusFailed       = "failed"
	StatusDeleted      = "deleted"
)

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
		return errors.New("hosting requires a db block")
	}
	globalCtx = ctx
	if err := seedProducts(ctx.AppDB(), configString(ctx, "default_image", "ghcr.io/apteva/apteva:latest")); err != nil {
		return err
	}
	if err := seedPlans(ctx.AppDB(), configString(ctx, "default_image", "ghcr.io/apteva/apteva:latest")); err != nil {
		return err
	}
	ctx.Logger().Info("hosting mounted", "version", appVersion, "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "invoice.paid", Handler: a.handleInvoicePaidEvent},
		{Event: "subscription.active", Handler: a.handleSubscriptionEvent},
		{Event: "subscription.trialing", Handler: a.handleSubscriptionEvent},
		{Event: "subscription.past_due", Handler: a.handleSubscriptionEvent},
		{Event: "subscription.cancelled", Handler: a.handleSubscriptionEvent},
		{Event: "subscription.paused", Handler: a.handleSubscriptionEvent},
		{Event: "subscription.ended", Handler: a.handleSubscriptionEvent},
		{Event: "llm.usage.recorded", Handler: a.handleLLMUsageRecorded},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "usage-sync",
		Schedule: "@every 5m",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			if ctx == nil {
				ctx = globalCtx
			}
			_, err := a.toolUsageSync(ctx, map[string]any{"actor": "worker"})
			return err
		},
	}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/products", Handler: a.handleProducts},
		{Pattern: "/plans", Handler: a.handlePlans},
		{Pattern: "/customers", Handler: a.handleCustomers},
		{Pattern: "/tenants", Handler: a.handleTenants},
		{Pattern: "/tenants/", Handler: a.handleTenantItem},
		{Pattern: "/addons", Handler: a.handleAddons},
		{Pattern: "/usage", Handler: a.handleUsage},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "hosting_product_list", Description: "List hosting products and their plans.", InputSchema: schemaObject(nil, nil), Handler: a.toolProductList},
		{Name: "hosting_plan_list", Description: "List hosting plans.", InputSchema: schemaObject(nil, nil), Handler: a.toolPlanList},
		{Name: "hosting_customer_create", Description: "Find or create a hosting customer by email.", InputSchema: schemaObject(map[string]any{"email": strSchema(), "name": strSchema(), "billing_customer_id": intSchema(), "metadata": objSchema()}, []string{"email"}), Handler: a.toolCustomerCreate},
		{Name: "hosting_checkout_create", Description: "Create a Hosting-owned sale/checkout from a Catalog price or free hosting plan. Paid plans create Billing and Subscription records; free plans provision immediately.", InputSchema: schemaObject(map[string]any{
			"catalog_price_id": intSchema(), "price_id": intSchema(), "owner_email": strSchema(), "customer_email": strSchema(), "customer_name": strSchema(),
			"slug": strSchema(), "plan_key": strSchema(), "product_key": strSchema(), "runtime_config": objSchema(), "metadata": objSchema(), "addons": arraySchema(),
			"success_url": strSchema(), "cancel_url": strSchema(), "provider": strSchema(), "create_payment_link": boolSchema(),
			"trial_days": intSchema(), "trial_requires_payment_method": boolSchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolCheckoutCreate},
		{Name: "hosting_tenant_create", Description: "Provision a hosted Apteva tenant container and default hostname.", InputSchema: schemaObject(map[string]any{
			"customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(),
			"owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(), "product_key": strSchema(),
			"subscription_id": intSchema(), "order_id": intSchema(), "fulfillment_id": intSchema(), "apteva_version": strSchema(), "image": strSchema(), "runtime_config": objSchema(), "metadata": objSchema(), "addons": arraySchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolTenantCreate},
		{Name: "hosting_paid_invoice_provision", Description: "Given a paid Billing invoice for a hosting product, create/claim the generic Orders record and provision the tenant. Args: invoice_id, owner_email, slug, plan_key, product_key, runtime_config, metadata.", InputSchema: schemaObject(map[string]any{
			"invoice_id": intSchema(), "owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(), "product_key": strSchema(),
			"image": strSchema(), "runtime_config": objSchema(), "metadata": objSchema(),
		}, []string{"invoice_id"}), Handler: a.toolPaidInvoiceProvision},
		{Name: "hosting_fulfillment_provision", Description: "Provision a tenant for an Orders generic fulfillment and report success/failure back to Orders. Args: order_id, fulfillment_id, owner_email, slug, plan_key, product_key, runtime_config, metadata.", InputSchema: schemaObject(map[string]any{
			"order_id": intSchema(), "fulfillment_id": intSchema(), "customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(),
			"owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(), "product_key": strSchema(),
			"subscription_id": intSchema(), "apteva_version": strSchema(), "image": strSchema(), "runtime_config": objSchema(), "metadata": objSchema(), "addons": arraySchema(),
		}, []string{"order_id", "fulfillment_id", "owner_email", "slug"}), Handler: a.toolFulfillmentProvision},
		{Name: "hosting_tenant_get", Description: "Fetch one hosted tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantGet},
		{Name: "hosting_tenant_list", Description: "List hosted tenants.", InputSchema: schemaObject(map[string]any{"customer_id": intSchema(), "status": strSchema(), "plan_key": strSchema()}, nil), Handler: a.toolTenantList},
		{Name: "hosting_tenant_suspend", Description: "Stop and suspend a tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantSuspend},
		{Name: "hosting_tenant_resume", Description: "Start a suspended/stopped tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantResume},
		{Name: "hosting_tenant_restart", Description: "Restart a hosted tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantRestart},
		{Name: "hosting_tenant_delete", Description: "Destroy a hosted tenant.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "delete_volumes": boolSchema()}, []string{"tenant_id"}), Handler: a.toolTenantDelete},
		{Name: "hosting_tenant_logs", Description: "Tail hosted tenant logs.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "tail": intSchema()}, []string{"tenant_id"}), Handler: a.toolTenantLogs},
		{Name: "hosting_tenant_health", Description: "Probe hosted tenant health.", InputSchema: tenantIDSchema(), Handler: a.toolTenantHealth},
		{Name: "hosting_usage_get", Description: "Return usage totals.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "customer_id": intSchema(), "feature_key": strSchema()}, nil), Handler: a.toolUsageGet},
		{Name: "hosting_usage_record", Description: "Record generic tenant usage and mirror it to Entitlements when installed.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "feature_key": strSchema(), "quantity": intSchema(), "idempotency_key": strSchema(), "metadata": objSchema()}, []string{"tenant_id", "feature_key"}), Handler: a.toolUsageRecord},
		{Name: "hosting_usage_sync", Description: "Sync generic runtime usage gauges from dependent apps and enforce hosting plan limits.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "actor": strSchema()}, nil), Handler: a.toolUsageSync},
		{Name: "hosting_subscription_sync", Description: "Apply a subscription status to a tenant.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "subscription_status": strSchema(), "actor": strSchema()}, []string{"tenant_id", "subscription_status"}), Handler: a.toolSubscriptionSync},
		{Name: "hosting_addon_enable", Description: "Enable a generic tenant add-on and orchestrate external app access when needed.", InputSchema: schemaObject(map[string]any{
			"tenant_id": strSchema(), "addon_key": strSchema(), "feature_key": strSchema(), "included_quantity": intSchema(), "reset_interval": strSchema(),
			"catalog_product_id": intSchema(), "catalog_price_id": intSchema(), "overage_product_id": intSchema(), "overage_price_id": intSchema(),
			"subscription_id": intSchema(), "subscription_item_id": intSchema(), "unit_amount_cents": intSchema(), "unit_size": intSchema(), "currency": strSchema(),
			"external_app": strSchema(), "external_subject_type": strSchema(), "external_subject_id": strSchema(), "limits": objSchema(), "llm_policy": objSchema(), "metadata": objSchema(),
		}, []string{"tenant_id", "feature_key"}), Handler: a.toolAddonEnable},
		{Name: "hosting_addon_list", Description: "List tenant add-ons.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "customer_id": intSchema(), "subscription_id": intSchema(), "status": strSchema()}, nil), Handler: a.toolAddonList},
		{Name: "hosting_addon_suspend", Description: "Suspend one tenant add-on and its external subject where supported.", InputSchema: schemaObject(map[string]any{"addon_id": intSchema(), "actor": strSchema()}, []string{"addon_id"}), Handler: a.toolAddonSuspend},
		{Name: "hosting_addon_resume", Description: "Resume one tenant add-on and its external subject where supported.", InputSchema: schemaObject(map[string]any{"addon_id": intSchema(), "actor": strSchema()}, []string{"addon_id"}), Handler: a.toolAddonResume},
		{Name: "hosting_metered_invoice_create", Description: "Aggregate a tenant add-on's metered overage for a period and create a generic Billing invoice line item.", InputSchema: schemaObject(map[string]any{"addon_id": intSchema(), "period_start": strSchema(), "period_end": strSchema(), "included_quantity": intSchema(), "unit_amount_cents": intSchema(), "unit_size": intSchema(), "currency": strSchema(), "description": strSchema(), "provider": strSchema(), "finalize": boolSchema(), "round_up_units": boolSchema(), "invoice_zero_usage": boolSchema(), "metadata": objSchema()}, []string{"addon_id"}), Handler: a.toolMeteredInvoiceCreate},
	}
}

func main() { sdk.Run(&App{}) }

type Customer struct {
	ID                int64           `json:"id"`
	Email             string          `json:"email"`
	Name              string          `json:"name"`
	BillingCustomerID *int64          `json:"billing_customer_id,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type Product struct {
	Key              string           `json:"key"`
	CatalogProductID *int64           `json:"catalog_product_id,omitempty"`
	Name             string           `json:"name"`
	Description      string           `json:"description"`
	RuntimeKind      string           `json:"runtime_kind"`
	Status           string           `json:"status"`
	Template         json.RawMessage  `json:"template,omitempty"`
	Metadata         json.RawMessage  `json:"metadata,omitempty"`
	Versions         []ProductVersion `json:"versions,omitempty"`
	Plans            []*Plan          `json:"plans,omitempty"`
	CreatedAt        string           `json:"created_at"`
	UpdatedAt        string           `json:"updated_at"`
}

type ProductVersion struct {
	ID                int64           `json:"id"`
	ProductKey        string          `json:"product_key"`
	Version           string          `json:"version"`
	Image             string          `json:"image"`
	DefaultPort       int             `json:"default_port"`
	DefaultHealthPath string          `json:"default_health_path"`
	Template          json.RawMessage `json:"template,omitempty"`
	Status            string          `json:"status"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type Plan struct {
	Key                  string          `json:"key"`
	Name                 string          `json:"name"`
	BillingMode          string          `json:"billing_mode"`
	PriceCents           *int64          `json:"price_cents,omitempty"`
	Interval             string          `json:"interval,omitempty"`
	Image                string          `json:"image"`
	CPU                  float64         `json:"cpu"`
	MemoryMB             int64           `json:"memory_mb"`
	StorageMB            int64           `json:"storage_mb"`
	ProductKey           string          `json:"product_key,omitempty"`
	CatalogPriceID       *int64          `json:"catalog_price_id,omitempty"`
	SubscriptionRequired bool            `json:"subscription_required"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	Limits               []PlanLimit     `json:"limits,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

type PlanLimit struct {
	PlanKey       string `json:"plan_key"`
	FeatureKey    string `json:"feature_key"`
	LimitValue    int64  `json:"limit_value"`
	ResetInterval string `json:"reset_interval"`
}

type Tenant struct {
	ID               string          `json:"id"`
	CustomerID       int64           `json:"customer_id"`
	SubscriptionID   *int64          `json:"subscription_id,omitempty"`
	WorkloadID       string          `json:"workload_id"`
	Slug             string          `json:"slug"`
	DefaultHostname  string          `json:"default_hostname"`
	OwnerEmail       string          `json:"owner_email"`
	PlanKey          string          `json:"plan_key"`
	Status           string          `json:"status"`
	AptevaVersion    string          `json:"apteva_version,omitempty"`
	Image            string          `json:"image"`
	LastHealthStatus string          `json:"last_health_status"`
	LastError        string          `json:"last_error"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

type UsageTotal struct {
	CustomerID int64  `json:"customer_id"`
	TenantID   string `json:"tenant_id,omitempty"`
	FeatureKey string `json:"feature_key"`
	Quantity   int64  `json:"quantity"`
}

type containerWorkload struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	PublicURL    string          `json:"public_url"`
	HealthStatus string          `json:"health_status"`
	Ports        []containerPort `json:"ports"`
}

type containerPort struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	BindAddr      string `json:"bind_addr"`
	Protocol      string `json:"protocol"`
}

type workloadResp struct {
	Workload containerWorkload `json:"workload"`
}

func (a *App) toolProductList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	products, err := dbProductList(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	return map[string]any{"products": products, "count": len(products)}, nil
}

func (a *App) toolPlanList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	plans, err := dbPlanList(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	return map[string]any{"plans": plans, "count": len(plans)}, nil
}

func (a *App) toolCustomerCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	c, err := dbCustomerUpsert(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"customer": c}, nil
}

func (a *App) toolCheckoutCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	projectID := firstNonEmpty(strArg(args, "_project_id"), ctx.CurrentProject())
	ownerEmail := strings.ToLower(firstNonEmpty(strArg(args, "owner_email"), strArg(args, "customer_email")))
	if ownerEmail == "" || !strings.Contains(ownerEmail, "@") {
		return nil, errors.New("valid owner_email required")
	}
	slug, err := normalizeSlug(strArg(args, "slug"))
	if err != nil {
		return nil, err
	}

	priceID := firstPositive(int64Arg(args, "catalog_price_id"), int64Arg(args, "price_id"))
	price, productFromCatalog, err := fetchCatalogCheckoutRefs(ctx, projectID, priceID)
	if err != nil {
		return nil, err
	}
	catalogMeta := mergeMaps(mapFromAny(productFromCatalog["metadata"]), mapFromAny(price["metadata"]))
	productKey := firstNonEmpty(strArg(args, "product_key"), strArg(catalogMeta, "product_key"), "apteva")
	planKey := firstNonEmpty(strArg(args, "plan_key"), strArg(catalogMeta, "plan_key"), defaultPlanForProduct(productKey))

	plan, err := dbPlanGet(ctx.AppDB(), planKey)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found", planKey)
	}
	if plan.ProductKey != "" && plan.ProductKey != productKey {
		return nil, fmt.Errorf("plan %q is bound to product %q, not %q", plan.Key, plan.ProductKey, productKey)
	}
	hostingProduct, err := dbProductGet(ctx.AppDB(), productKey)
	if err != nil {
		return nil, err
	}
	if hostingProduct == nil {
		return nil, fmt.Errorf("product %q not found", productKey)
	}

	runtimeConfig := mergeMaps(mapFromAny(catalogMeta["runtime_config"]), mapArg(args, "runtime_config"))
	saleMeta := mergeMaps(catalogMeta, mapArg(args, "metadata"), map[string]any{
		"fulfillment_app":  "hosting",
		"fulfillment_type": "hosting_tenant_provision",
		"order_type":       "hosting",
		"owner_email":      ownerEmail,
		"slug":             slug,
		"product_key":      productKey,
		"plan_key":         planKey,
	})
	if len(runtimeConfig) > 0 {
		saleMeta["runtime_config"] = runtimeConfig
	}
	if addons := arrayFromAny(args["addons"]); len(addons) > 0 {
		saleMeta["addons"] = addons
	}
	if priceID != 0 {
		saleMeta["catalog_price_id"] = priceID
	}
	if productID := int64Arg(price, "product_id"); productID != 0 {
		saleMeta["catalog_product_id"] = productID
	}

	customerName := firstNonEmpty(strArg(args, "customer_name"), ownerEmail)
	billingCustomer, err := callAppMap(ctx, "billing", "customers_upsert_by_email", withProject(projectID, map[string]any{
		"email": ownerEmail,
		"defaults": map[string]any{
			"name":     customerName,
			"metadata": map[string]any{"source_app": "hosting"},
		},
	}))
	if err != nil {
		return nil, fmt.Errorf("billing customer: %w", err)
	}
	billingCustomer = unwrapMap(billingCustomer, "customer")
	billingCustomerID := int64Arg(billingCustomer, "id")
	if billingCustomerID == 0 {
		return nil, errors.New("billing customers_upsert_by_email returned no customer id")
	}

	hostingCustomer, err := dbCustomerUpsert(ctx.AppDB(), map[string]any{
		"email":               ownerEmail,
		"name":                customerName,
		"billing_customer_id": billingCustomerID,
		"metadata":            map[string]any{"source_app": "hosting"},
	})
	if err != nil {
		return nil, err
	}

	priceCents := firstPositive(int64Arg(price, "unit_amount_cents"), derefInt64(plan.PriceCents))
	currency := strings.ToUpper(firstNonEmpty(strArg(price, "currency"), "USD"))
	interval := firstNonEmpty(strArg(price, "interval"), plan.Interval, "month")
	intervalCount := firstPositive(int64Arg(price, "interval_count"), 1)
	title := firstNonEmpty(strArg(price, "nickname"), strArg(productFromCatalog, "name"), plan.Name)
	now := time.Now().UTC()
	isPaid := priceCents > 0 && plan.BillingMode != "free"
	trialDays := firstPositive(int64Arg(args, "trial_days"), int64Arg(price, "trial_days"), int64Arg(catalogMeta, "trial_days"))
	trialRequiresPaymentMethod := boolArgDefault(args, "trial_requires_payment_method", boolFromAny(catalogMeta["trial_requires_payment_method"], false))
	if isPaid && trialDays > 0 && trialRequiresPaymentMethod {
		return nil, errors.New("card-required trials are not supported yet; use a no-card trial or a paid checkout")
	}
	var trialStart, trialEnd string
	if trialDays > 0 {
		trialStart = now.Format(time.RFC3339)
		trialEnd = now.AddDate(0, 0, int(trialDays)).Format(time.RFC3339)
		saleMeta["trial_days"] = trialDays
		saleMeta["trial_start"] = trialStart
		saleMeta["trial_end"] = trialEnd
		saleMeta["trial_requires_payment_method"] = trialRequiresPaymentMethod
	}
	subStatus := "active"
	if isPaid {
		if trialDays > 0 {
			subStatus = "trialing"
		} else {
			subStatus = "past_due"
		}
	}
	subscription, err := callAppMap(ctx, "subscriptions", "subscriptions_create", withProject(projectID, map[string]any{
		"customer_id":          billingCustomerID,
		"customer_email":       ownerEmail,
		"customer_name":        customerName,
		"kind":                 "service",
		"status":               subStatus,
		"billing_provider":     firstNonEmpty(strArg(args, "provider"), "local"),
		"currency":             currency,
		"interval":             interval,
		"interval_count":       intervalCount,
		"source":               "hosting",
		"source_ref":           slug,
		"metadata":             saleMeta,
		"items":                []any{subscriptionItemFromCheckout(priceID, price, productFromCatalog, title, priceCents, currency, saleMeta)},
		"trial_start":          trialStart,
		"trial_end":            trialEnd,
		"current_period_start": now.Format(time.RFC3339),
	}))
	if err != nil {
		return nil, fmt.Errorf("subscriptions create: %w", err)
	}
	subscription = unwrapMap(subscription, "subscription")
	subscriptionID := int64Arg(subscription, "id")
	if subscriptionID == 0 {
		return nil, errors.New("subscriptions_create returned no subscription id")
	}
	saleMeta["subscription_id"] = subscriptionID

	result := map[string]any{
		"checkout": map[string]any{
			"status":           "created",
			"requires_payment": isPaid,
			"product_key":      productKey,
			"plan_key":         planKey,
		},
		"billing_customer": billingCustomer,
		"customer":         hostingCustomer,
		"subscription":     subscription,
	}

	provisionNow := !isPaid || (trialDays > 0 && !trialRequiresPaymentMethod)
	if provisionNow {
		provisioned, err := a.toolTenantCreate(ctx, map[string]any{
			"customer_id":     hostingCustomer.ID,
			"customer_email":  ownerEmail,
			"customer_name":   customerName,
			"owner_email":     ownerEmail,
			"slug":            slug,
			"plan_key":        planKey,
			"product_key":     productKey,
			"subscription_id": subscriptionID,
			"runtime_config":  runtimeConfig,
			"metadata":        saleMeta,
			"addons":          saleMeta["addons"],
			"_project_id":     projectID,
		})
		if err != nil {
			return nil, err
		}
		result["tenant"] = provisioned.(map[string]any)["tenant"]
		result["checkout"].(map[string]any)["status"] = "provisioned"
		if isPaid && trialDays > 0 {
			result["checkout"].(map[string]any)["status"] = "trialing"
			result["checkout"].(map[string]any)["trial_end"] = trialEnd
			result["checkout"].(map[string]any)["requires_payment"] = false
		}
		return result, nil
	}

	invoice, err := callAppMap(ctx, "billing", "invoices_create", withProject(projectID, map[string]any{
		"customer_id": billingCustomerID,
		"currency":    currency,
		"provider":    strArg(args, "provider"),
		"line_items":  []any{invoiceLineFromCheckout(priceID, title, priceCents, saleMeta)},
		"metadata":    saleMeta,
	}))
	if err != nil {
		return nil, fmt.Errorf("billing invoice create: %w", err)
	}
	invoice = unwrapMap(invoice, "invoice")
	invoiceID := int64Arg(invoice, "id")
	if invoiceID == 0 {
		return nil, errors.New("invoices_create returned no invoice id")
	}
	finalized, err := callAppMap(ctx, "billing", "invoices_finalize", withProject(projectID, map[string]any{"invoice_id": invoiceID}))
	if err != nil {
		return nil, fmt.Errorf("billing invoice finalize: %w", err)
	}
	result["invoice"] = unwrapMap(finalized, "invoice")

	if boolArgDefault(args, "create_payment_link", true) {
		linkInput := map[string]any{"invoice_id": invoiceID}
		if v := strArg(args, "success_url"); v != "" {
			linkInput["success_url"] = v
		}
		if v := strArg(args, "cancel_url"); v != "" {
			linkInput["cancel_url"] = v
		}
		paymentLink, err := callAppMap(ctx, "billing", "invoices_send_payment_link", withProject(projectID, linkInput))
		if err != nil {
			return nil, fmt.Errorf("billing payment link: %w", err)
		}
		result["payment_link"] = paymentLink
		if url := strArg(paymentLink, "url"); url != "" {
			result["checkout"].(map[string]any)["url"] = url
		}
	}

	return result, nil
}

func (a *App) handleInvoicePaidEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	args := map[string]any{
		"invoice_id":  firstPositive(int64Arg(event.Data, "invoice_id"), int64Arg(event.Data, "id")),
		"_project_id": event.ProjectID,
		"metadata":    mapArg(event.Data, "metadata"),
	}
	if args["invoice_id"].(int64) == 0 {
		return errors.New("invoice.paid event missing invoice id")
	}
	_, err := a.toolPaidInvoiceProvision(ctx, args)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not marked as a hosting product") || strings.Contains(msg, "not paid") {
			ctx.Logger().Info("hosting ignored invoice.paid event", "invoice_id", args["invoice_id"], "reason", msg)
			return nil
		}
	}
	return err
}

func (a *App) handleSubscriptionEvent(ctx *sdk.AppCtx, event sdk.Event) error {
	status := strings.TrimPrefix(event.Name(), "subscription.")
	if status == "" || status == event.Name() {
		status = strArg(event.Data, "status")
	}
	tenantID := firstNonEmpty(
		strArg(event.Data, "tenant_id"),
		strArg(event.Data, "hosting_tenant_id"),
		strArg(event.Data, "external_ref"),
	)
	if tenantID == "" {
		subID := firstPositive(int64Arg(event.Data, "subscription_id"), int64Arg(event.Data, "id"))
		if subID == 0 {
			return nil
		}
		t, err := dbTenantGetBySubscriptionID(ctx.AppDB(), subID)
		if err != nil || t == nil {
			return err
		}
		tenantID = t.ID
	}
	_, err := a.toolSubscriptionSync(ctx, map[string]any{
		"tenant_id":             tenantID,
		"subscription_status":   status,
		"actor":                 firstNonEmpty(event.SourceApp, "subscriptions"),
		"_source_install_id":    event.SourceInstallID,
		"_source_event":         event.Name(),
		"_source_project_id":    event.ProjectID,
		"_subscription_payload": event.Data,
	})
	return err
}

func (a *App) toolTenantCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := a.provisionTenant(ctx, args)
	if err != nil {
		_ = reportOrderFulfillment(ctx, args, "failed", "", err.Error(), nil)
		return nil, err
	}
	_ = reportOrderFulfillment(ctx, args, "succeeded", t.ID, "", map[string]any{
		"tenant_id":        t.ID,
		"default_hostname": t.DefaultHostname,
		"workload_id":      t.WorkloadID,
		"status":           t.Status,
	})
	ctx.Emit("hosting.tenant.active", map[string]any{"tenant_id": t.ID, "customer_id": t.CustomerID, "hostname": t.DefaultHostname})
	return map[string]any{"tenant": t}, nil
}

func (a *App) toolPaidInvoiceProvision(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	invoiceID := int64Arg(args, "invoice_id")
	if invoiceID == 0 {
		return nil, errors.New("invoice_id required")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	projectID := firstNonEmpty(strArg(args, "_project_id"), ctx.CurrentProject())
	invoice, err := fetchBillingInvoice(ctx, projectID, invoiceID)
	if err != nil {
		return nil, err
	}
	meta := mergeMaps(hostingMetadataFromInvoiceLines(invoice), mapFromAny(invoice["metadata"]), mapArg(args, "metadata"))
	if strings.ToLower(strArg(invoice, "status")) != "paid" {
		return nil, fmt.Errorf("invoice %d is %s, not paid", invoiceID, strArg(invoice, "status"))
	}
	if !isHostingInvoice(meta, args) {
		return nil, errors.New("invoice is not marked as a hosting product")
	}
	provisionArgs := hostingProvisionArgsFromInvoice(invoiceID, invoice, meta, args)

	order, fulfillment, err := createHostingOrderFromInvoice(ctx, projectID, invoiceID, provisionArgs)
	if err != nil {
		return nil, err
	}
	orderID := int64Arg(order, "id")
	fulfillmentID := int64Arg(fulfillment, "id")
	if orderID == 0 || fulfillmentID == 0 {
		return nil, errors.New("orders_create_from_invoice did not return order and fulfillment ids")
	}
	provisionArgs["order_id"] = orderID
	provisionArgs["fulfillment_id"] = fulfillmentID
	provisionArgs["metadata"] = mergeMaps(mapArg(provisionArgs, "metadata"), map[string]any{
		"invoice_id":      invoiceID,
		"order_id":        orderID,
		"fulfillment_id":  fulfillmentID,
		"fulfillment_app": "hosting",
	})

	if existing, err := dbTenantGetBySlug(ctx.AppDB(), strArg(provisionArgs, "slug")); err != nil {
		return nil, err
	} else if existing != nil {
		_ = markSubscriptionActive(ctx, existing.ID, int64Arg(provisionArgs, "subscription_id"))
		if err := reportOrderFulfillment(ctx, provisionArgs, "succeeded", existing.ID, "", map[string]any{
			"tenant_id":        existing.ID,
			"default_hostname": existing.DefaultHostname,
			"workload_id":      existing.WorkloadID,
			"status":           existing.Status,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"invoice": invoice, "order": order, "fulfillment": fulfillment, "tenant": existing, "idempotent": true}, nil
	}

	out, err := a.toolFulfillmentProvision(ctx, provisionArgs)
	if err != nil {
		return nil, err
	}
	result := out.(map[string]any)
	if tenant, ok := result["tenant"].(*Tenant); ok {
		_ = markSubscriptionActive(ctx, tenant.ID, int64Arg(provisionArgs, "subscription_id"))
	}
	result["invoice"] = invoice
	result["order"] = order
	result["fulfillment"] = fulfillment
	return result, nil
}

func (a *App) toolFulfillmentProvision(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if int64Arg(args, "order_id") == 0 {
		return nil, errors.New("order_id required")
	}
	if int64Arg(args, "fulfillment_id") == 0 {
		return nil, errors.New("fulfillment_id required")
	}
	t, err := a.provisionTenant(ctx, args)
	if err != nil {
		_ = reportOrderFulfillment(ctx, args, "failed", "", err.Error(), nil)
		return nil, err
	}
	if err := reportOrderFulfillment(ctx, args, "succeeded", t.ID, "", map[string]any{
		"tenant_id":        t.ID,
		"default_hostname": t.DefaultHostname,
		"workload_id":      t.WorkloadID,
		"status":           t.Status,
	}); err != nil {
		return nil, err
	}
	ctx.Emit("hosting.tenant.active", map[string]any{"tenant_id": t.ID, "customer_id": t.CustomerID, "hostname": t.DefaultHostname})
	return map[string]any{"tenant": t}, nil
}

func (a *App) toolTenantGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := dbTenantGet(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errors.New("tenant not found")
	}
	return map[string]any{"tenant": t}, nil
}

func (a *App) toolTenantList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbTenantList(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tenants": out, "count": len(out)}, nil
}

func (a *App) toolTenantSuspend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	if t.WorkloadID != "" {
		var out workloadResp
		if err := ctx.PlatformAPI().CallAppResult("containers", "containers_stop", map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
			return nil, err
		}
	}
	t, err = dbTenantSetStatus(ctx.AppDB(), t.ID, StatusSuspended, "")
	if err != nil {
		return nil, err
	}
	_ = suspendTenantAddons(ctx, t.ID)
	_ = recordEvent(ctx.AppDB(), t.ID, "suspended", "tool", nil)
	return map[string]any{"tenant": t}, nil
}

func (a *App) toolTenantResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	if t.WorkloadID != "" {
		var out workloadResp
		if err := ctx.PlatformAPI().CallAppResult("containers", "containers_start", map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
			return nil, err
		}
	}
	t, err = dbTenantSetStatus(ctx.AppDB(), t.ID, StatusActive, "")
	if err != nil {
		return nil, err
	}
	_ = resumeTenantAddons(ctx, t.ID)
	_ = recordEvent(ctx.AppDB(), t.ID, "resumed", "tool", nil)
	return map[string]any{"tenant": t}, nil
}

func (a *App) toolTenantRestart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	var out workloadResp
	if err := ctx.PlatformAPI().CallAppResult("containers", "containers_restart", map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
		return nil, err
	}
	t, err = dbTenantSetStatus(ctx.AppDB(), t.ID, StatusActive, "")
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), t.ID, "restarted", "tool", nil)
	return map[string]any{"tenant": t, "workload": out.Workload}, nil
}

func (a *App) toolTenantDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	if t.DefaultHostname != "" {
		_ = ctx.PlatformAPI().UnexposeIngress(t.DefaultHostname)
	}
	if t.WorkloadID != "" {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("containers", "containers_destroy", map[string]any{"workload_id": t.WorkloadID, "delete_volumes": boolArg(args, "delete_volumes")}, &out); err != nil {
			return nil, err
		}
	}
	t, err = dbTenantSetStatus(ctx.AppDB(), t.ID, StatusDeleted, "")
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), t.ID, "deleted", "tool", nil)
	return map[string]any{"tenant": t}, nil
}

func (a *App) toolTenantLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("containers", "containers_logs", map[string]any{"workload_id": t.WorkloadID, "tail": intArg(args, "tail", 200)}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) toolTenantHealth(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	var out workloadResp
	if err := ctx.PlatformAPI().CallAppResult("containers", "containers_health", map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
		t, _ = dbTenantHealth(ctx.AppDB(), t.ID, "error", err.Error())
		return nil, err
	}
	t, err = dbTenantHealth(ctx.AppDB(), t.ID, firstNonEmpty(out.Workload.HealthStatus, out.Workload.Status), "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"tenant": t, "workload": out.Workload}, nil
}

func (a *App) toolUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbUsageTotals(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": out, "count": len(out)}, nil
}

func (a *App) toolUsageSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("hosting context unavailable")
	}
	var tenants []*Tenant
	if tenantID := strArg(args, "tenant_id"); tenantID != "" {
		t, err := requireTenant(ctx.AppDB(), tenantID)
		if err != nil {
			return nil, err
		}
		tenants = []*Tenant{t}
	} else {
		rows, err := dbTenantList(ctx.AppDB(), nil)
		if err != nil {
			return nil, err
		}
		tenants = rows
	}
	var synced []map[string]any
	var errs []string
	for _, tenant := range tenants {
		if tenant.WorkloadID == "" || tenant.Status == StatusDeleted {
			continue
		}
		out, err := a.syncTenantUsage(ctx, tenant, actor(args))
		if err != nil {
			errs = append(errs, tenant.ID+": "+err.Error())
			_ = recordEvent(ctx.AppDB(), tenant.ID, "usage.sync_failed", actor(args), map[string]any{"error": err.Error()})
			continue
		}
		synced = append(synced, out)
	}
	return map[string]any{"synced": synced, "count": len(synced), "errors": errs}, nil
}

func (a *App) syncTenantUsage(ctx *sdk.AppCtx, tenant *Tenant, actorName string) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	projectID := ctx.CurrentProject()
	usageResp, err := callAppMap(ctx, "containers", "containers_usage_get", withProject(projectID, map[string]any{"workload_id": tenant.WorkloadID}))
	if err != nil {
		return nil, err
	}
	metrics := arrayFromAny(usageResp["metrics"])
	if len(metrics) == 0 {
		metrics = arrayFromAny(mapFromAny(usageResp["usage"])["metrics"])
	}
	result := map[string]any{"tenant_id": tenant.ID, "workload_id": tenant.WorkloadID, "metrics": []any{}, "enforced": []any{}}
	for _, raw := range metrics {
		metric := mapFromAny(raw)
		if strArg(metric, "kind") != "gauge" {
			continue
		}
		if strArg(metric, "source") == "docker_volume" {
			continue
		}
		feature := strArg(metric, "feature_key")
		quantity := int64Arg(metric, "quantity")
		if feature == "" {
			continue
		}
		meta := map[string]any{
			"source_app":  "containers",
			"workload_id": tenant.WorkloadID,
			"usage_kind":  "gauge",
			"unit":        strArg(metric, "unit"),
			"metric":      metric,
		}
		if err := recordUsageGauge(ctx.AppDB(), tenant.ID, tenant.CustomerID, feature, quantity, meta); err != nil {
			return nil, err
		}
		limit, hasLimit, err := dbPlanLimitValue(ctx.AppDB(), tenant.PlanKey, feature)
		if err != nil {
			return nil, err
		}
		_ = a.syncEntitlementGauge(ctx, tenant, feature, quantity, limit, hasLimit, meta)
		result["metrics"] = append(result["metrics"].([]any), map[string]any{"feature_key": feature, "quantity": quantity, "limit": limit, "has_limit": hasLimit})
		if hasLimit && limit > 0 && quantity > limit {
			enforced, err := a.enforceUsageLimit(ctx, tenant, feature, quantity, limit, actorName)
			if err != nil {
				return nil, err
			}
			result["enforced"] = append(result["enforced"].([]any), enforced)
		}
	}
	return result, nil
}

func (a *App) syncEntitlementGauge(ctx *sdk.AppCtx, tenant *Tenant, feature string, quantity, limit int64, hasLimit bool, meta map[string]any) error {
	if ctx.PlatformAPI() == nil {
		return nil
	}
	projectID := ctx.CurrentProject()
	if hasLimit {
		_, _ = callAppMap(ctx, "entitlements", "entitlement_limits_set", withProject(projectID, map[string]any{
			"subject_type": "tenant",
			"subject_id":   tenant.ID,
			"feature_key":  feature,
			"limit_type":   "gauge",
			"limit_value":  limit,
			"metadata":     map[string]any{"source_app": "hosting", "plan_key": tenant.PlanKey},
		}))
	}
	_, _ = callAppMap(ctx, "entitlements", "usage_record", withProject(projectID, map[string]any{
		"subject_type": "tenant",
		"subject_id":   tenant.ID,
		"feature_key":  feature,
		"quantity":     quantity,
		"usage_kind":   "gauge",
		"unit":         meta["unit"],
		"metadata":     meta,
	}))
	return nil
}

func (a *App) enforceUsageLimit(ctx *sdk.AppCtx, tenant *Tenant, feature string, quantity, limit int64, actorName string) (map[string]any, error) {
	payload := map[string]any{"feature_key": feature, "quantity": quantity, "limit": limit}
	_ = recordEvent(ctx.AppDB(), tenant.ID, "usage.over_limit", actorName, payload)
	if tenant.Status == StatusActive || tenant.Status == StatusProvisioning {
		_, err := a.toolTenantSuspend(ctx, map[string]any{"tenant_id": tenant.ID, "actor": actorName})
		if err != nil {
			return nil, err
		}
		_, _ = dbTenantSetStatus(ctx.AppDB(), tenant.ID, StatusSuspended, fmt.Sprintf("usage over limit: %s %d > %d", feature, quantity, limit))
		payload["action"] = "suspended"
	} else {
		payload["action"] = "recorded"
	}
	return payload, nil
}

func (a *App) toolSubscriptionSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	status := strings.ToLower(strings.TrimSpace(strArg(args, "subscription_status")))
	switch status {
	case "trialing", "active":
		return a.toolTenantResume(ctx, args)
	case "past_due":
		t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
		if err != nil {
			return nil, err
		}
		_ = recordEvent(ctx.AppDB(), t.ID, "subscription.past_due", actor(args), nil)
		return a.toolTenantSuspend(ctx, args)
	case "paused", "cancelled":
		return a.toolTenantSuspend(ctx, args)
	case "ended":
		t, err := requireTenant(ctx.AppDB(), strArg(args, "tenant_id"))
		if err != nil {
			return nil, err
		}
		policy := subscriptionPolicyMetadata(t, args)
		if shouldDeleteOnSubscriptionEnded(policy) {
			deleteArgs := mergeMaps(args, map[string]any{"delete_volumes": boolFromAny(policy["delete_volumes_on_expiry"], false)})
			return a.toolTenantDelete(ctx, deleteArgs)
		}
		return a.toolTenantSuspend(ctx, args)
	default:
		return nil, fmt.Errorf("unsupported subscription_status %q", status)
	}
}

func subscriptionPolicyMetadata(t *Tenant, args map[string]any) map[string]any {
	policy := map[string]any{}
	if t != nil {
		policy = mergeMaps(policy, mapFromAny(t.Metadata))
	}
	payload := mapArg(args, "_subscription_payload")
	policy = mergeMaps(policy, mapFromAny(payload["metadata"]), mapArg(args, "metadata"))
	return policy
}

func shouldDeleteOnSubscriptionEnded(policy map[string]any) bool {
	switch strings.ToLower(firstNonEmpty(
		strArg(policy, "on_subscription_ended"),
		strArg(policy, "on_unpaid_grace_expired"),
		strArg(policy, "on_trial_expired"),
	)) {
	case "delete", "destroy", "remove":
		return true
	default:
		return false
	}
}

func reportOrderFulfillment(ctx *sdk.AppCtx, args map[string]any, status, externalRef, errText string, response map[string]any) error {
	fulfillmentID := int64Arg(args, "fulfillment_id")
	if fulfillmentID == 0 || ctx.PlatformAPI() == nil {
		return nil
	}
	input := map[string]any{
		"id":           fulfillmentID,
		"status":       status,
		"external_ref": externalRef,
		"error":        errText,
		"actor":        "hosting",
	}
	if response != nil {
		input["response_payload"] = response
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("orders", "fulfillments_update", input, &out); err != nil {
		return fmt.Errorf("orders fulfillments_update: %w", err)
	}
	return nil
}

func callAppMap(ctx *sdk.AppCtx, appName, tool string, input map[string]any) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult(appName, tool, input, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func withProject(projectID string, input map[string]any) map[string]any {
	if projectID != "" {
		input["_project_id"] = projectID
	}
	return input
}

func unwrapMap(in map[string]any, key string) map[string]any {
	if out := mapFromAny(in[key]); len(out) > 0 {
		return out
	}
	return in
}

func fetchCatalogCheckoutRefs(ctx *sdk.AppCtx, projectID string, priceID int64) (map[string]any, map[string]any, error) {
	if priceID == 0 {
		return map[string]any{}, map[string]any{}, nil
	}
	priceResp, err := callAppMap(ctx, "catalog", "catalog_prices_get", withProject(projectID, map[string]any{"id": priceID}))
	if err != nil {
		return nil, nil, fmt.Errorf("catalog price: %w", err)
	}
	price := unwrapMap(priceResp, "price")
	productID := int64Arg(price, "product_id")
	if productID == 0 {
		return nil, nil, fmt.Errorf("catalog price %d returned no product_id", priceID)
	}
	productResp, err := callAppMap(ctx, "catalog", "catalog_products_get", withProject(projectID, map[string]any{"id": productID}))
	if err != nil {
		return nil, nil, fmt.Errorf("catalog product: %w", err)
	}
	return price, unwrapMap(productResp, "product"), nil
}

func subscriptionItemFromCheckout(priceID int64, price, product map[string]any, title string, priceCents int64, currency string, metadata map[string]any) map[string]any {
	item := map[string]any{
		"title":             title,
		"quantity":          1,
		"unit_amount_cents": priceCents,
		"currency":          currency,
		"metadata":          metadata,
	}
	if priceID != 0 {
		item["catalog_price_id"] = priceID
	}
	if productID := int64Arg(price, "product_id"); productID != 0 {
		item["catalog_product_id"] = productID
	}
	if sku := firstNonEmpty(strArg(price, "external_id"), strArg(product, "slug")); sku != "" {
		item["sku"] = sku
	}
	return item
}

func invoiceLineFromCheckout(priceID int64, title string, priceCents int64, metadata map[string]any) map[string]any {
	line := map[string]any{
		"description":      title,
		"quantity":         1,
		"unit_price_cents": priceCents,
		"metadata":         metadata,
	}
	if priceID != 0 {
		line["price_id"] = priceID
	}
	return line
}

func fetchBillingInvoice(ctx *sdk.AppCtx, projectID string, invoiceID int64) (map[string]any, error) {
	input := map[string]any{"id": invoiceID}
	if projectID != "" {
		input["_project_id"] = projectID
	}
	var out struct {
		Invoice map[string]any `json:"invoice"`
		Found   bool           `json:"found"`
	}
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", input, &out); err != nil {
		return nil, fmt.Errorf("billing invoices_get: %w", err)
	}
	if out.Invoice == nil {
		return nil, fmt.Errorf("invoice %d not found", invoiceID)
	}
	return out.Invoice, nil
}

func hostingMetadataFromInvoiceLines(invoice map[string]any) map[string]any {
	var lines []any
	switch v := invoice["line_items"].(type) {
	case []any:
		lines = v
	case []map[string]any:
		for _, line := range v {
			lines = append(lines, line)
		}
	}
	for _, raw := range lines {
		line := mapFromAny(raw)
		meta := mapFromAny(line["metadata"])
		if isHostingInvoice(meta, nil) {
			return meta
		}
	}
	return map[string]any{}
}

func markSubscriptionActive(ctx *sdk.AppCtx, tenantID string, subscriptionID int64) error {
	if subscriptionID == 0 || ctx.PlatformAPI() == nil {
		return nil
	}
	input := map[string]any{
		"id":     subscriptionID,
		"status": "active",
		"actor":  "hosting",
		"note":   "tenant provisioned: " + tenantID,
	}
	if projectID := ctx.CurrentProject(); projectID != "" {
		input["_project_id"] = projectID
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_status", input, &out); err != nil {
		_ = recordEvent(ctx.AppDB(), tenantID, "subscription.sync_failed", "hosting", map[string]any{"subscription_id": subscriptionID, "error": err.Error()})
		return err
	}
	return nil
}

func createHostingOrderFromInvoice(ctx *sdk.AppCtx, projectID string, invoiceID int64, provisionArgs map[string]any) (map[string]any, map[string]any, error) {
	meta := mapArg(provisionArgs, "metadata")
	input := map[string]any{
		"invoice_id":       invoiceID,
		"order_type":       "hosting",
		"fulfillment_type": "hosting_tenant_provision",
		"fulfillment_app":  "hosting",
		"metadata":         meta,
		"fulfillment_meta": meta,
	}
	if projectID != "" {
		input["_project_id"] = projectID
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("orders", "orders_create_from_invoice", input, &out); err != nil {
		return nil, nil, fmt.Errorf("orders create from invoice: %w", err)
	}
	order := mapFromAny(out["order"])
	fulfillment := mapFromAny(out["fulfillment"])
	if len(order) == 0 || len(fulfillment) == 0 {
		return nil, nil, errors.New("orders_create_from_invoice returned no order or fulfillment")
	}
	return order, fulfillment, nil
}

func hostingProvisionArgsFromInvoice(invoiceID int64, invoice, meta, args map[string]any) map[string]any {
	productKey := firstNonEmpty(strArg(args, "product_key"), strArg(meta, "product_key"), "wordpress-single")
	planKey := firstNonEmpty(strArg(args, "plan_key"), strArg(meta, "plan_key"), defaultPlanForProduct(productKey))
	customerEmail := firstNonEmpty(strArg(invoice, "customer_email"), strArg(meta, "customer_email"), strArg(args, "customer_email"))
	slug := firstNonEmpty(strArg(args, "slug"), strArg(meta, "slug"), fmt.Sprintf("hosting-inv-%d", invoiceID))
	ownerEmail := firstNonEmpty(strArg(args, "owner_email"), strArg(meta, "owner_email"), customerEmail)
	subscriptionID := firstPositive(int64Arg(args, "subscription_id"), int64Arg(meta, "subscription_id"), int64Arg(invoice, "subscription_id"))
	out := map[string]any{
		"customer_email":      customerEmail,
		"customer_name":       firstNonEmpty(strArg(invoice, "customer_name"), strArg(meta, "customer_name"), strArg(args, "customer_name")),
		"billing_customer_id": int64Arg(invoice, "customer_id"),
		"owner_email":         ownerEmail,
		"slug":                slug,
		"plan_key":            planKey,
		"product_key":         productKey,
		"metadata": mergeMaps(meta, map[string]any{
			"invoice_id":       invoiceID,
			"product_key":      productKey,
			"plan_key":         planKey,
			"fulfillment_app":  "hosting",
			"fulfillment_type": "hosting_tenant_provision",
		}),
	}
	if subscriptionID != 0 {
		out["subscription_id"] = subscriptionID
		out["metadata"] = mergeMaps(mapArg(out, "metadata"), map[string]any{"subscription_id": subscriptionID})
	}
	if runtimeConfig := mergeMaps(mapFromAny(meta["runtime_config"]), mapArg(args, "runtime_config")); len(runtimeConfig) > 0 {
		out["runtime_config"] = runtimeConfig
	}
	if image := firstNonEmpty(strArg(args, "image"), strArg(meta, "image")); image != "" {
		out["image"] = image
	}
	return out
}

func isHostingInvoice(meta, args map[string]any) bool {
	if strArg(args, "product_key") != "" || strArg(args, "plan_key") != "" {
		return true
	}
	if strings.EqualFold(strArg(meta, "fulfillment_app"), "hosting") || strings.EqualFold(strArg(meta, "order_type"), "hosting") {
		return true
	}
	productType := strings.ToLower(firstNonEmpty(strArg(meta, "product_type"), strArg(meta, "type")))
	if strings.Contains(productType, "hosting") || strings.Contains(productType, "wordpress") || strings.Contains(productType, "container") {
		return true
	}
	productKey := strArg(meta, "product_key")
	return productKey == "wordpress-single" || productKey == "custom-docker" || productKey == "apteva"
}

func defaultPlanForProduct(productKey string) string {
	switch productKey {
	case "wordpress-single":
		return "wordpress-starter"
	case "custom-docker":
		return "docker-starter"
	default:
		return "starter"
	}
}

func (a *App) provisionTenant(ctx *sdk.AppCtx, args map[string]any) (*Tenant, error) {
	planKey := firstNonEmpty(strArg(args, "plan_key"), "free")
	plan, err := dbPlanGet(ctx.AppDB(), planKey)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found", planKey)
	}
	productKey := firstNonEmpty(strArg(args, "product_key"), plan.ProductKey, "apteva")
	if plan.ProductKey != "" && productKey != plan.ProductKey {
		return nil, fmt.Errorf("plan %q is bound to product %q, not %q", plan.Key, plan.ProductKey, productKey)
	}
	product, err := dbProductGet(ctx.AppDB(), productKey)
	if err != nil {
		return nil, err
	}
	if product == nil {
		return nil, fmt.Errorf("product %q not found", productKey)
	}
	runtimeConfig := mapArg(args, "runtime_config")
	customer, err := resolveCustomer(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	if err := enforceTenantLimit(ctx.AppDB(), customer.ID, plan.Key); err != nil {
		return nil, err
	}
	slug, err := normalizeSlug(strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	host := slug + "." + strings.Trim(strings.ToLower(configString(ctx, "default_base_domain", "hosted.apteva.local")), ".")
	image, err := resolveRuntimeImage(ctx, product, plan, runtimeConfig, strArg(args, "image"))
	if err != nil {
		return nil, err
	}
	metadata := mapArg(args, "metadata")
	metadata["product_key"] = product.Key
	metadata["runtime_kind"] = product.RuntimeKind
	if orderID := int64Arg(args, "order_id"); orderID != 0 {
		metadata["order_id"] = orderID
	}
	if fulfillmentID := int64Arg(args, "fulfillment_id"); fulfillmentID != 0 {
		metadata["fulfillment_id"] = fulfillmentID
	}
	tenant := &Tenant{
		ID:              newID("htn"),
		CustomerID:      customer.ID,
		SubscriptionID:  nullableInt64(int64Arg(args, "subscription_id")),
		Slug:            slug,
		DefaultHostname: host,
		OwnerEmail:      strings.ToLower(strings.TrimSpace(strArg(args, "owner_email"))),
		PlanKey:         plan.Key,
		Status:          StatusProvisioning,
		AptevaVersion:   strArg(args, "apteva_version"),
		Image:           image,
		Metadata:        jsonRaw(metadata),
	}
	if tenant.OwnerEmail == "" {
		return nil, errors.New("owner_email required")
	}
	if err := dbTenantInsert(ctx.AppDB(), tenant); err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), tenant.ID, "provisioning.started", "tool", map[string]any{"plan_key": plan.Key})

	workload, err := createContainer(ctx, tenant, plan, product, runtimeConfig)
	if err != nil {
		_, _ = dbTenantSetStatus(ctx.AppDB(), tenant.ID, StatusFailed, err.Error())
		_ = recordEvent(ctx.AppDB(), tenant.ID, "provisioning.failed", "containers", map[string]any{"error": err.Error()})
		return nil, err
	}
	target := workloadTarget(workload)
	if target == "" {
		err := errors.New("containers_run returned no reachable workload URL")
		_, _ = dbTenantSetStatus(ctx.AppDB(), tenant.ID, StatusFailed, err.Error())
		return nil, err
	}
	route, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname: host,
		Target:   target,
		TLSMode:  "auto",
	})
	if err != nil {
		_, _ = dbTenantSetStatus(ctx.AppDB(), tenant.ID, StatusFailed, err.Error())
		_ = recordEvent(ctx.AppDB(), tenant.ID, "ingress.failed", "platform", map[string]any{"error": err.Error(), "target": target})
		return nil, err
	}
	tenant.WorkloadID = workload.ID
	tenant.LastHealthStatus = firstNonEmpty(workload.HealthStatus, workload.Status, "unknown")
	tenant.Status = StatusActive
	if err := dbTenantActivate(ctx.AppDB(), tenant); err != nil {
		return nil, err
	}
	_ = recordUsage(ctx.AppDB(), tenant.ID, tenant.CustomerID, "hosting.tenants", 1, tenant.ID+":created", nil)
	_ = recordEvent(ctx.AppDB(), tenant.ID, "ingress.active", "platform", map[string]any{"hostname": route.Hostname, "target": route.Target})
	_ = recordEvent(ctx.AppDB(), tenant.ID, "provisioning.active", "tool", map[string]any{"workload_id": workload.ID})
	_ = a.ensureTenantAuth(ctx, tenant, customer, metadata, runtimeConfig)
	addons := arrayFromAny(args["addons"])
	if len(addons) == 0 {
		addons = arrayFromAny(metadata["addons"])
	}
	if len(addons) > 0 {
		addonBase := map[string]any{
			"subscription_id": derefInt64(tenant.SubscriptionID),
			"metadata":        map[string]any{"source_app": "hosting", "tenant_id": tenant.ID, "plan_key": tenant.PlanKey, "product_key": product.Key},
		}
		if _, err := a.enableTenantAddons(ctx, tenant, addons, addonBase); err != nil {
			_, _ = dbTenantSetStatus(ctx.AppDB(), tenant.ID, StatusFailed, err.Error())
			_ = recordEvent(ctx.AppDB(), tenant.ID, "addon.provision_failed", "hosting", map[string]any{"error": err.Error()})
			return nil, err
		}
	}
	return dbTenantGet(ctx.AppDB(), tenant.ID)
}

func (a *App) ensureTenantAuth(ctx *sdk.AppCtx, tenant *Tenant, customer *Customer, metadata, runtimeConfig map[string]any) error {
	if tenant == nil || ctx == nil {
		return nil
	}
	authEnabled := boolValue(firstPresent(metadata, runtimeConfig, "auth_enabled"), true)
	if !authEnabled {
		_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.skipped", "hosting", map[string]any{"reason": "disabled"})
		return nil
	}
	if strings.TrimSpace(tenant.OwnerEmail) == "" {
		_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.skipped", "hosting", map[string]any{"reason": "owner_email_missing"})
		return nil
	}
	projectID := ctx.CurrentProject()
	orgSlug := firstNonEmpty(strArg(metadata, "auth_org_slug"), authOrgSlugForTenant(tenant.Slug, tenant.ID))
	orgName := firstNonEmpty(strArg(metadata, "auth_org_name"), customerDisplayName(customer, tenant))
	patch := map[string]any{
		"auth_enabled":  true,
		"auth_org_slug": orgSlug,
	}

	orgResp, err := callAppMap(ctx, "auth", "auth_orgs_create", withProject(projectID, map[string]any{
		"slug": orgSlug,
		"name": orgName,
	}))
	if err != nil {
		if isOptionalAuthMissing(err) {
			_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.skipped", "hosting", map[string]any{"reason": "auth_unavailable"})
			return nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "slug already in use") {
			orgResp, err = findAuthOrg(ctx, projectID, orgSlug)
		}
		if err != nil {
			patch["auth_error"] = err.Error()
			_ = dbTenantMergeMetadata(ctx.AppDB(), tenant.ID, patch)
			_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.provision_failed", "auth", map[string]any{"error": err.Error(), "stage": "organization"})
			return nil
		}
	}
	org := mapFromAny(orgResp["organization"])
	if orgID := int64Arg(org, "id"); orgID > 0 {
		patch["auth_org_id"] = orgID
	}

	userResp, err := callAppMap(ctx, "auth", "auth_users_create", withProject(projectID, map[string]any{
		"organization_slug":   orgSlug,
		"email":               tenant.OwnerEmail,
		"display_name":        firstNonEmpty(strArg(metadata, "auth_user_display_name"), customerDisplayName(customer, tenant)),
		"email_verified":      boolValue(firstPresent(metadata, runtimeConfig, "auth_email_verified"), true),
		"send_password_reset": boolValue(firstPresent(metadata, runtimeConfig, "auth_send_password_reset"), true),
	}))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already") || strings.Contains(strings.ToLower(err.Error()), "exists") {
			userResp, err = findAuthUser(ctx, projectID, orgSlug, tenant.OwnerEmail)
		}
		if err != nil {
			patch["auth_error"] = err.Error()
			_ = dbTenantMergeMetadata(ctx.AppDB(), tenant.ID, patch)
			_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.provision_failed", "auth", map[string]any{"error": err.Error(), "stage": "user"})
			return nil
		}
	}
	user := mapFromAny(userResp["user"])
	if userID := int64Arg(user, "id"); userID > 0 {
		patch["auth_user_id"] = userID
	}
	if _, ok := userResp["password_reset_sent"]; ok {
		patch["auth_password_reset_sent"] = boolValue(userResp["password_reset_sent"], false)
	}

	if boolValue(firstPresent(metadata, runtimeConfig, "auth_create_client"), true) {
		clientResp, err := callAppMap(ctx, "auth", "auth_clients_create", withProject(projectID, map[string]any{
			"organization_slug": orgSlug,
			"name":              "Hosting Admin - " + tenant.Slug,
			"type":              "spa",
			"redirect_uris":     stringSliceFromAny(firstPresent(metadata, runtimeConfig, "auth_redirect_uris")),
			"allowed_origins":   stringSliceFromAny(firstPresent(metadata, runtimeConfig, "auth_allowed_origins")),
		}))
		if err != nil {
			patch["auth_client_error"] = err.Error()
			_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.client_failed", "auth", map[string]any{"error": err.Error()})
		} else if clientID := firstNonEmpty(strArg(clientResp, "client_id"), strArg(mapFromAny(clientResp["client"]), "client_id")); clientID != "" {
			patch["auth_client_id"] = clientID
		}
	}
	delete(patch, "auth_error")
	if err := dbTenantMergeMetadata(ctx.AppDB(), tenant.ID, patch); err != nil {
		return err
	}
	_ = recordEvent(ctx.AppDB(), tenant.ID, "auth.provisioned", "auth", patch)
	return nil
}

type runtimeTemplate struct {
	BlueprintSlug string            `json:"blueprint_slug"`
	Image         string            `json:"image"`
	Port          int               `json:"port"`
	HealthPath    string            `json:"health_path"`
	Env           map[string]string `json:"env"`
	Volumes       []runtimeVolume   `json:"volumes"`
	Labels        map[string]string `json:"labels"`
}

type runtimeVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	SizeMB    int64  `json:"size_mb,omitempty"`
}

func createContainer(ctx *sdk.AppCtx, tenant *Tenant, plan *Plan, product *Product, runtimeConfig map[string]any) (*containerWorkload, error) {
	input, err := containerRunInput(ctx, tenant, plan, product, runtimeConfig)
	if err != nil {
		return nil, err
	}
	var out workloadResp
	if err := ctx.PlatformAPI().CallAppResult("containers", "containers_run", input, &out); err != nil {
		return nil, err
	}
	if out.Workload.ID == "" {
		return nil, errors.New("containers_run returned empty workload id")
	}
	return &out.Workload, nil
}

func containerRunInput(ctx *sdk.AppCtx, tenant *Tenant, plan *Plan, product *Product, runtimeConfig map[string]any) (map[string]any, error) {
	tpl := productTemplate(product)
	blueprint := firstNonEmpty(strArg(runtimeConfig, "blueprint_slug"), tpl.BlueprintSlug, "custom-image")
	port := intArg(runtimeConfig, "port", tpl.Port)
	if port <= 0 {
		port = 80
	}
	env := map[string]any{}
	for k, v := range tpl.Env {
		env[k] = v
	}
	for k, v := range stringMapArg(runtimeConfig, "env") {
		env[k] = v
	}
	if product.Key == "apteva" {
		env["PORT"] = firstNonEmpty(mapValueString(env, "PORT"), "5280")
		env["APTEVA_BIND"] = firstNonEmpty(mapValueString(env, "APTEVA_BIND"), "0.0.0.0")
		env["APTEVA_HOSTED"] = "1"
		env["APTEVA_HOSTING_ID"] = tenant.ID
		env["APTEVA_OWNER_EMAIL"] = tenant.OwnerEmail
		if tenant.AptevaVersion != "" {
			env["APTEVA_VERSION"] = tenant.AptevaVersion
		}
	}

	input := map[string]any{
		"name":           "hosting-" + tenant.Slug,
		"blueprint_slug": blueprint,
		"image":          tenant.Image,
		"env":            env,
		"resources": map[string]any{
			"cpu":        plan.CPU,
			"memory_mb":  plan.MemoryMB,
			"storage_mb": plan.StorageMB,
		},
		"labels": mergeStringMaps(tpl.Labels, map[string]string{
			"apteva.hosting.tenant_id":   tenant.ID,
			"apteva.hosting.product_key": product.Key,
			"apteva.hosting.plan_key":    plan.Key,
		}),
	}
	if blueprint != "apteva" {
		input["ports"] = []map[string]any{{
			"container_port": port,
			"bind_addr":      "127.0.0.1",
			"protocol":       "tcp",
		}}
		input["health_path"] = firstNonEmpty(strArg(runtimeConfig, "health_path"), tpl.HealthPath, "/")
		if len(tpl.Volumes) > 0 {
			volumes := make([]map[string]any, 0, len(tpl.Volumes))
			for _, v := range tpl.Volumes {
				volumes = append(volumes, map[string]any{
					"name":       firstNonEmpty(v.Name, tenant.Slug+"-data"),
					"mount_path": v.MountPath,
					"size_mb":    firstPositive(v.SizeMB, plan.StorageMB),
				})
			}
			input["volumes"] = volumes
		}
	}
	return input, nil
}

func resolveRuntimeImage(ctx *sdk.AppCtx, product *Product, plan *Plan, runtimeConfig map[string]any, explicitImage string) (string, error) {
	tpl := productTemplate(product)
	image := firstNonEmpty(strArg(runtimeConfig, "image"), explicitImage, plan.Image, tpl.Image)
	if image == "" && product.Key == "apteva" {
		image = configString(ctx, "default_image", "apteva:latest")
	}
	if image == "" {
		return "", fmt.Errorf("image required for product %q", product.Key)
	}
	return image, nil
}

func productTemplate(product *Product) runtimeTemplate {
	var tpl runtimeTemplate
	if product != nil && len(product.Template) > 0 {
		_ = json.Unmarshal(product.Template, &tpl)
	}
	return tpl
}

func workloadTarget(w *containerWorkload) string {
	if strings.HasPrefix(w.PublicURL, "http://") || strings.HasPrefix(w.PublicURL, "https://") {
		return w.PublicURL
	}
	for _, p := range w.Ports {
		if p.ContainerPort == 5280 && p.HostPort > 0 {
			host := firstNonEmpty(p.BindAddr, "127.0.0.1")
			return fmt.Sprintf("http://%s:%d", host, p.HostPort)
		}
	}
	if len(w.Ports) > 0 && w.Ports[0].HostPort > 0 {
		host := firstNonEmpty(w.Ports[0].BindAddr, "127.0.0.1")
		return fmt.Sprintf("http://%s:%d", host, w.Ports[0].HostPort)
	}
	return ""
}

func seedProducts(db *sql.DB, defaultImage string) error {
	products := []Product{
		{
			Key:         "apteva",
			Name:        "Apteva Tenant",
			Description: "Managed Apteva tenant instance.",
			RuntimeKind: "single_container",
			Status:      "active",
			Template: jsonRaw(runtimeTemplate{
				BlueprintSlug: "apteva",
				Image:         defaultImage,
				Port:          5280,
				HealthPath:    "/health",
				Env: map[string]string{
					"PORT":              "5280",
					"APTEVA_BIND":       "0.0.0.0",
					"DB_PATH":           "/data/apteva.db",
					"DATA_DIR":          "/data",
					"APTEVA_APPS_CACHE": "/data/apps",
					"CORE_CMD":          "/usr/local/bin/apteva-core",
				},
				Volumes: []runtimeVolume{{Name: "data", MountPath: "/data"}},
			}),
		},
		{
			Key:         "custom-docker",
			Name:        "Custom Docker Image",
			Description: "Single-container hosting for an arbitrary Docker image.",
			RuntimeKind: "single_container",
			Status:      "active",
			Template: jsonRaw(runtimeTemplate{
				BlueprintSlug: "custom-image",
				Port:          80,
				HealthPath:    "/",
				Volumes:       []runtimeVolume{{Name: "data", MountPath: "/data"}},
			}),
		},
		{
			Key:         "wordpress-single",
			Name:        "WordPress",
			Description: "Single-container WordPress hosting template.",
			RuntimeKind: "single_container",
			Status:      "active",
			Template: jsonRaw(runtimeTemplate{
				BlueprintSlug: "custom-image",
				Image:         "wordpress:php8.3-apache",
				Port:          80,
				HealthPath:    "/wp-admin/setup-config.php",
				Volumes:       []runtimeVolume{{Name: "html", MountPath: "/var/www/html"}},
			}),
		},
	}
	for _, p := range products {
		if _, err := db.Exec(`
			INSERT INTO hosting_products (key, name, description, runtime_kind, status, template_json, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?, '{}')
			ON CONFLICT(key) DO UPDATE SET
				name=excluded.name,
				description=excluded.description,
				runtime_kind=excluded.runtime_kind,
				status=excluded.status,
				template_json=excluded.template_json,
				updated_at=CURRENT_TIMESTAMP`,
			p.Key, p.Name, p.Description, p.RuntimeKind, p.Status, stringOrJSON(p.Template, "{}")); err != nil {
			return err
		}
		tpl := productTemplate(&p)
		if _, err := db.Exec(`
			INSERT INTO hosting_product_versions (product_key, version, image, default_port, default_health_path, template_json, status)
			VALUES (?, 'default', ?, ?, ?, ?, 'active')
			ON CONFLICT(product_key, version) DO UPDATE SET
				image=excluded.image,
				default_port=excluded.default_port,
				default_health_path=excluded.default_health_path,
				template_json=excluded.template_json,
				status=excluded.status,
				updated_at=CURRENT_TIMESTAMP`,
			p.Key, tpl.Image, firstPositiveInt(tpl.Port, 80), firstNonEmpty(tpl.HealthPath, "/"), stringOrJSON(p.Template, "{}")); err != nil {
			return err
		}
	}
	return nil
}

func seedPlans(db *sql.DB, defaultImage string) error {
	plans := []Plan{
		{Key: "free", Name: "Free", BillingMode: "free", Image: defaultImage, CPU: 0.5, MemoryMB: 512, StorageMB: 512, ProductKey: "apteva"},
		{Key: "starter", Name: "Starter", BillingMode: "paid", Image: defaultImage, CPU: 1, MemoryMB: 1024, StorageMB: 10240, ProductKey: "apteva", SubscriptionRequired: true},
		{Key: "pro", Name: "Pro", BillingMode: "paid", Image: defaultImage, CPU: 2, MemoryMB: 2048, StorageMB: 51200, ProductKey: "apteva", SubscriptionRequired: true},
		{Key: "docker-free", Name: "Docker Free", BillingMode: "free", CPU: 0.25, MemoryMB: 256, StorageMB: 512, ProductKey: "custom-docker"},
		{Key: "docker-starter", Name: "Docker Starter", BillingMode: "paid", CPU: 1, MemoryMB: 1024, StorageMB: 10240, ProductKey: "custom-docker", SubscriptionRequired: true},
		{Key: "wordpress-free", Name: "WordPress Free", BillingMode: "free", Image: "wordpress:php8.3-apache", CPU: 0.5, MemoryMB: 512, StorageMB: 1024, ProductKey: "wordpress-single"},
		{Key: "wordpress-starter", Name: "WordPress Starter", BillingMode: "paid", Image: "wordpress:php8.3-apache", CPU: 1, MemoryMB: 1024, StorageMB: 10240, ProductKey: "wordpress-single", SubscriptionRequired: true},
	}
	limits := map[string]map[string]int64{
		"free":              {"hosting.tenants": 1, "hosting.custom_domains": 0, "containers.storage_mb": 512, "containers.storage.bytes": 512 * 1024 * 1024},
		"starter":           {"hosting.tenants": 1, "hosting.custom_domains": 1, "containers.storage_mb": 10240, "containers.storage.bytes": 10240 * 1024 * 1024},
		"pro":               {"hosting.tenants": 5, "hosting.custom_domains": 10, "containers.storage_mb": 51200, "containers.storage.bytes": 51200 * 1024 * 1024},
		"docker-free":       {"hosting.tenants": 1, "hosting.custom_domains": 0, "containers.storage_mb": 512, "containers.storage.bytes": 512 * 1024 * 1024},
		"docker-starter":    {"hosting.tenants": 3, "hosting.custom_domains": 1, "containers.storage_mb": 10240, "containers.storage.bytes": 10240 * 1024 * 1024},
		"wordpress-free":    {"hosting.tenants": 1, "hosting.custom_domains": 0, "containers.storage_mb": 1024, "containers.storage.bytes": 1024 * 1024 * 1024},
		"wordpress-starter": {"hosting.tenants": 3, "hosting.custom_domains": 1, "containers.storage_mb": 10240, "containers.storage.bytes": 10240 * 1024 * 1024},
	}
	for _, p := range plans {
		if _, err := db.Exec(`
			INSERT INTO hosting_plans (key, name, billing_mode, price_cents, interval, image, cpu, memory_mb, storage_mb, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')
			ON CONFLICT(key) DO UPDATE SET
				name=excluded.name,
				billing_mode=excluded.billing_mode,
				image=excluded.image,
				cpu=excluded.cpu,
				memory_mb=excluded.memory_mb,
				storage_mb=excluded.storage_mb,
				updated_at=CURRENT_TIMESTAMP`,
			p.Key, p.Name, p.BillingMode, p.PriceCents, nullStr(p.Interval), p.Image, p.CPU, p.MemoryMB, p.StorageMB); err != nil {
			return err
		}
		if _, err := db.Exec(`
			INSERT INTO hosting_plan_bindings (plan_key, product_key, catalog_price_id, subscription_required)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(plan_key) DO UPDATE SET
				product_key=excluded.product_key,
				catalog_price_id=excluded.catalog_price_id,
				subscription_required=excluded.subscription_required,
				updated_at=CURRENT_TIMESTAMP`,
			p.Key, p.ProductKey, p.CatalogPriceID, boolToInt(p.SubscriptionRequired)); err != nil {
			return err
		}
		for feature, limit := range limits[p.Key] {
			if _, err := db.Exec(`
				INSERT INTO hosting_plan_limits (plan_key, feature_key, limit_value, reset_interval)
				VALUES (?, ?, ?, 'none')
				ON CONFLICT(plan_key, feature_key) DO UPDATE SET
					limit_value=excluded.limit_value,
					updated_at=CURRENT_TIMESTAMP`,
				p.Key, feature, limit); err != nil {
				return err
			}
		}
	}
	return nil
}

func dbCustomerUpsert(db *sql.DB, args map[string]any) (*Customer, error) {
	email := strings.ToLower(strings.TrimSpace(strArg(args, "email")))
	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.New("valid email required")
	}
	var id int64
	err := db.QueryRow(`
		INSERT INTO hosting_customers (email, name, billing_customer_id, metadata_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			name=COALESCE(NULLIF(excluded.name,''), hosting_customers.name),
			billing_customer_id=COALESCE(excluded.billing_customer_id, hosting_customers.billing_customer_id),
			updated_at=CURRENT_TIMESTAMP
		RETURNING id`,
		email, strArg(args, "name"), nullableInt64(int64Arg(args, "billing_customer_id")), jsonOrEmpty(args["metadata"], "{}"),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return dbCustomerGet(db, id)
}

func resolveCustomer(db *sql.DB, args map[string]any) (*Customer, error) {
	if id := int64Arg(args, "customer_id"); id > 0 {
		c, err := dbCustomerGet(db, id)
		if err != nil || c == nil {
			return c, firstErr(err, errors.New("customer not found"))
		}
		return c, nil
	}
	email := firstNonEmpty(strArg(args, "customer_email"), strArg(args, "owner_email"))
	body := map[string]any{"email": email, "name": strArg(args, "customer_name"), "billing_customer_id": int64Arg(args, "billing_customer_id")}
	return dbCustomerUpsert(db, body)
}

func dbCustomerGet(db *sql.DB, id int64) (*Customer, error) {
	var c Customer
	var billing sql.NullInt64
	var meta string
	err := db.QueryRow(`SELECT id, email, name, billing_customer_id, metadata_json, created_at, updated_at FROM hosting_customers WHERE id=?`, id).
		Scan(&c.ID, &c.Email, &c.Name, &billing, &meta, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if billing.Valid {
		c.BillingCustomerID = &billing.Int64
	}
	c.Metadata = json.RawMessage(meta)
	return &c, nil
}

func dbProductList(db *sql.DB) ([]*Product, error) {
	rows, err := db.Query(`SELECT key, catalog_product_id, name, description, runtime_kind, status, template_json, metadata_json, created_at, updated_at FROM hosting_products ORDER BY key`)
	if err != nil {
		return nil, err
	}
	var out []*Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, p := range out {
		if p.Versions, err = dbProductVersions(db, p.Key); err != nil {
			return nil, err
		}
		if p.Plans, err = dbPlansForProduct(db, p.Key); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func dbProductGet(db *sql.DB, key string) (*Product, error) {
	p, err := scanProduct(db.QueryRow(`SELECT key, catalog_product_id, name, description, runtime_kind, status, template_json, metadata_json, created_at, updated_at FROM hosting_products WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Versions, err = dbProductVersions(db, p.Key)
	return p, err
}

func scanProduct(row rowScanner) (*Product, error) {
	var p Product
	var catalogID sql.NullInt64
	var tpl, meta string
	if err := row.Scan(&p.Key, &catalogID, &p.Name, &p.Description, &p.RuntimeKind, &p.Status, &tpl, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if catalogID.Valid {
		p.CatalogProductID = &catalogID.Int64
	}
	p.Template = json.RawMessage(tpl)
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbProductVersions(db *sql.DB, productKey string) ([]ProductVersion, error) {
	rows, err := db.Query(`SELECT id, product_key, version, image, default_port, default_health_path, template_json, status, created_at, updated_at FROM hosting_product_versions WHERE product_key=? ORDER BY version`, productKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProductVersion
	for rows.Next() {
		var v ProductVersion
		var tpl string
		if err := rows.Scan(&v.ID, &v.ProductKey, &v.Version, &v.Image, &v.DefaultPort, &v.DefaultHealthPath, &tpl, &v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Template = json.RawMessage(tpl)
		out = append(out, v)
	}
	return out, rows.Err()
}

func dbPlanList(db *sql.DB) ([]*Plan, error) {
	rows, err := db.Query(planSelect() + ` ORDER BY p.key`)
	if err != nil {
		return nil, err
	}
	var out []*Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, p := range out {
		limits, err := dbPlanLimits(db, p.Key)
		if err != nil {
			return nil, err
		}
		p.Limits = limits
	}
	return out, nil
}

func dbPlanGet(db *sql.DB, key string) (*Plan, error) {
	p, err := scanPlan(db.QueryRow(planSelect()+` WHERE p.key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Limits, err = dbPlanLimits(db, key)
	return p, err
}

func dbPlansForProduct(db *sql.DB, productKey string) ([]*Plan, error) {
	rows, err := db.Query(planSelect()+` WHERE b.product_key=? ORDER BY p.key`, productKey)
	if err != nil {
		return nil, err
	}
	var out []*Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, p := range out {
		p.Limits, err = dbPlanLimits(db, p.Key)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func planSelect() string {
	return `SELECT p.key, p.name, p.billing_mode, p.price_cents, COALESCE(p.interval,''), p.image,
		p.cpu, p.memory_mb, p.storage_mb, COALESCE(b.product_key,''), b.catalog_price_id,
		COALESCE(b.subscription_required,0), p.metadata_json, p.created_at, p.updated_at
		FROM hosting_plans p
		LEFT JOIN hosting_plan_bindings b ON b.plan_key=p.key`
}

type rowScanner interface{ Scan(...any) error }

func scanPlan(row rowScanner) (*Plan, error) {
	var p Plan
	var price, catalogPrice sql.NullInt64
	var meta string
	var subscriptionRequired int
	if err := row.Scan(&p.Key, &p.Name, &p.BillingMode, &price, &p.Interval, &p.Image, &p.CPU, &p.MemoryMB, &p.StorageMB, &p.ProductKey, &catalogPrice, &subscriptionRequired, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if price.Valid {
		p.PriceCents = &price.Int64
	}
	if catalogPrice.Valid {
		p.CatalogPriceID = &catalogPrice.Int64
	}
	p.SubscriptionRequired = subscriptionRequired != 0
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbPlanLimits(db *sql.DB, planKey string) ([]PlanLimit, error) {
	rows, err := db.Query(`SELECT plan_key, feature_key, limit_value, reset_interval FROM hosting_plan_limits WHERE plan_key=? ORDER BY feature_key`, planKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanLimit
	for rows.Next() {
		var l PlanLimit
		if err := rows.Scan(&l.PlanKey, &l.FeatureKey, &l.LimitValue, &l.ResetInterval); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func dbPlanLimitValue(db *sql.DB, planKey, feature string) (int64, bool, error) {
	var limit int64
	err := db.QueryRow(`SELECT limit_value FROM hosting_plan_limits WHERE plan_key=? AND feature_key=?`, planKey, feature).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return limit, true, nil
}

func enforceTenantLimit(db *sql.DB, customerID int64, planKey string) error {
	var limit int64
	err := db.QueryRow(`SELECT limit_value FROM hosting_plan_limits WHERE plan_key=? AND feature_key='hosting.tenants'`, planKey).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) || limit <= 0 {
		return errors.New("plan does not allow hosted tenants")
	}
	if err != nil {
		return err
	}
	var count int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosting_tenants WHERE customer_id=? AND status != ?`, customerID, StatusDeleted).Scan(&count); err != nil {
		return err
	}
	if count >= limit {
		return fmt.Errorf("tenant limit exceeded for plan %s: %d/%d", planKey, count, limit)
	}
	return nil
}

func dbTenantInsert(db *sql.DB, t *Tenant) error {
	_, err := db.Exec(`
		INSERT INTO hosting_tenants
			(id, customer_id, subscription_id, workload_id, slug, default_hostname, owner_email,
			 plan_key, status, apteva_version, image, last_health_status, last_error, metadata_json)
		VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, 'unknown', '', ?)`,
		t.ID, t.CustomerID, t.SubscriptionID, t.Slug, t.DefaultHostname, t.OwnerEmail, t.PlanKey,
		t.Status, t.AptevaVersion, t.Image, stringOrJSON(t.Metadata, "{}"))
	return err
}

func dbTenantActivate(db *sql.DB, t *Tenant) error {
	_, err := db.Exec(`
		UPDATE hosting_tenants
		SET workload_id=?, status=?, last_health_status=?, last_error='', updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		t.WorkloadID, StatusActive, t.LastHealthStatus, t.ID)
	return err
}

func dbTenantMergeMetadata(db *sql.DB, tenantID string, patch map[string]any) error {
	if len(patch) == 0 {
		return nil
	}
	t, err := dbTenantGet(db, tenantID)
	if err != nil {
		return err
	}
	if t == nil {
		return errors.New("tenant not found")
	}
	meta := mapFromAny(t.Metadata)
	for k, v := range patch {
		meta[k] = v
	}
	res, err := db.Exec(`UPDATE hosting_tenants SET metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, jsonOrEmpty(meta, "{}"), tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("tenant not found")
	}
	return nil
}

func dbTenantSetStatus(db *sql.DB, id, status, errMsg string) (*Tenant, error) {
	res, err := db.Exec(`UPDATE hosting_tenants SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, errMsg, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("tenant not found")
	}
	return dbTenantGet(db, id)
}

func dbTenantHealth(db *sql.DB, id, health, errMsg string) (*Tenant, error) {
	res, err := db.Exec(`UPDATE hosting_tenants SET last_health_status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, health, errMsg, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("tenant not found")
	}
	return dbTenantGet(db, id)
}

func requireTenant(db *sql.DB, id string) (*Tenant, error) {
	t, err := dbTenantGet(db, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, errors.New("tenant not found")
	}
	return t, nil
}

func dbTenantGet(db *sql.DB, id string) (*Tenant, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("tenant_id required")
	}
	t, err := scanTenant(db.QueryRow(tenantSelect()+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func dbTenantGetBySlug(db *sql.DB, slug string) (*Tenant, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, nil
	}
	t, err := scanTenant(db.QueryRow(tenantSelect()+` WHERE slug=? AND status != ?`, slug, StatusDeleted))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func dbTenantGetBySubscriptionID(db *sql.DB, subscriptionID int64) (*Tenant, error) {
	if subscriptionID <= 0 {
		return nil, nil
	}
	t, err := scanTenant(db.QueryRow(tenantSelect()+` WHERE subscription_id=? AND status != ?`, subscriptionID, StatusDeleted))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func dbTenantList(db *sql.DB, args map[string]any) ([]*Tenant, error) {
	where := []string{"status != ?"}
	qargs := []any{StatusDeleted}
	if id := int64Arg(args, "customer_id"); id > 0 {
		where = append(where, "customer_id=?")
		qargs = append(qargs, id)
	}
	if s := strArg(args, "status"); s != "" {
		where[0] = "status=?"
		qargs[0] = s
	}
	if p := strArg(args, "plan_key"); p != "" {
		where = append(where, "plan_key=?")
		qargs = append(qargs, p)
	}
	rows, err := db.Query(tenantSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func tenantSelect() string {
	return `SELECT id, customer_id, subscription_id, workload_id, slug, default_hostname, owner_email,
		plan_key, status, apteva_version, image, last_health_status, last_error, metadata_json, created_at, updated_at
		FROM hosting_tenants`
}

func scanTenant(row rowScanner) (*Tenant, error) {
	var t Tenant
	var sub sql.NullInt64
	var meta string
	if err := row.Scan(&t.ID, &t.CustomerID, &sub, &t.WorkloadID, &t.Slug, &t.DefaultHostname, &t.OwnerEmail,
		&t.PlanKey, &t.Status, &t.AptevaVersion, &t.Image, &t.LastHealthStatus, &t.LastError, &meta, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	if sub.Valid {
		t.SubscriptionID = &sub.Int64
	}
	t.Metadata = json.RawMessage(meta)
	return &t, nil
}

func recordUsage(db *sql.DB, tenantID string, customerID int64, feature string, qty int64, idem string, meta any) error {
	if feature == "" {
		return errors.New("feature_key required")
	}
	_, err := db.Exec(`INSERT INTO hosting_usage_events (tenant_id, customer_id, feature_key, quantity, idempotency_key, metadata_json, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nullStr(tenantID), customerID, feature, qty, nullStr(idem), jsonOrEmpty(meta, "{}"), time.Now().UTC().Format(time.RFC3339))
	if err != nil && (strings.Contains(err.Error(), "ux_hosting_usage_idempotency") || strings.Contains(err.Error(), "UNIQUE constraint failed: hosting_usage_events.customer_id, hosting_usage_events.idempotency_key")) {
		return nil
	}
	return err
}

func recordUsageGauge(db *sql.DB, tenantID string, customerID int64, feature string, qty int64, meta any) error {
	if feature == "" {
		return errors.New("feature_key required")
	}
	idem := "gauge:" + tenantID + ":" + feature
	res, err := db.Exec(`UPDATE hosting_usage_events
		SET quantity=?, metadata_json=?, occurred_at=?
		WHERE customer_id=? AND idempotency_key=?`,
		qty, jsonOrEmpty(meta, "{}"), time.Now().UTC().Format(time.RFC3339), customerID, idem)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	return recordUsage(db, tenantID, customerID, feature, qty, idem, meta)
}

func dbUsageTotals(db *sql.DB, args map[string]any) ([]UsageTotal, error) {
	where := []string{"1=1"}
	qargs := []any{}
	if id := int64Arg(args, "customer_id"); id > 0 {
		where = append(where, "customer_id=?")
		qargs = append(qargs, id)
	}
	if id := strArg(args, "tenant_id"); id != "" {
		where = append(where, "tenant_id=?")
		qargs = append(qargs, id)
	}
	if f := strArg(args, "feature_key"); f != "" {
		where = append(where, "feature_key=?")
		qargs = append(qargs, f)
	}
	rows, err := db.Query(`
		SELECT customer_id, COALESCE(tenant_id,''), feature_key, COALESCE(SUM(quantity),0)
		FROM hosting_usage_events
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY customer_id, tenant_id, feature_key
		ORDER BY feature_key`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageTotal
	for rows.Next() {
		var u UsageTotal
		if err := rows.Scan(&u.CustomerID, &u.TenantID, &u.FeatureKey, &u.Quantity); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func recordEvent(db *sql.DB, tenantID, kind, actorName string, payload any) error {
	_, err := db.Exec(`INSERT INTO hosting_events (tenant_id, kind, actor, payload_json) VALUES (?, ?, ?, ?)`,
		nullStr(tenantID), kind, actorName, jsonOrEmpty(payload, "{}"))
	return err
}

func handleJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

func (a *App) handleProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	products, err := dbProductList(globalCtx.AppDB())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	handleJSON(w, map[string]any{"products": products, "count": len(products)})
}

func (a *App) handlePlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	plans, err := dbPlanList(globalCtx.AppDB())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	handleJSON(w, map[string]any{"plans": plans, "count": len(plans)})
}

func (a *App) handleCustomers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var args map[string]any
	_ = json.NewDecoder(r.Body).Decode(&args)
	c, err := dbCustomerUpsert(globalCtx.AppDB(), args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	handleJSON(w, map[string]any{"customer": c})
}

func (a *App) handleTenants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"status": r.URL.Query().Get("status"), "plan_key": r.URL.Query().Get("plan_key")}
		if v := r.URL.Query().Get("customer_id"); v != "" {
			args["customer_id"] = v
		}
		out, err := dbTenantList(globalCtx.AppDB(), args)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		handleJSON(w, map[string]any{"tenants": out, "count": len(out)})
	case http.MethodPost:
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)
		t, err := a.provisionTenant(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		handleJSON(w, map[string]any{"tenant": t})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleTenantItem(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tenants/"), "/")
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	t, err := dbTenantGet(globalCtx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		httpErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	handleJSON(w, map[string]any{"tenant": t})
}

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	args := map[string]any{"tenant_id": r.URL.Query().Get("tenant_id"), "feature_key": r.URL.Query().Get("feature_key")}
	if v := r.URL.Query().Get("customer_id"); v != "" {
		args["customer_id"] = v
	}
	out, err := dbUsageTotals(globalCtx.AppDB(), args)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	handleJSON(w, map[string]any{"usage": out, "count": len(out)})
}

func (a *App) handleAddons(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"tenant_id": r.URL.Query().Get("tenant_id"), "status": r.URL.Query().Get("status")}
		if v := r.URL.Query().Get("customer_id"); v != "" {
			args["customer_id"] = v
		}
		out, err := dbAddonList(globalCtx.AppDB(), args)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		handleJSON(w, map[string]any{"addons": out, "count": len(out)})
	case http.MethodPost:
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)
		addon, credentials, err := a.enableAddon(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out := map[string]any{"addon": addon}
		if len(credentials) > 0 {
			out["credentials"] = credentials
		}
		handleJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func normalizeSlug(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !slugRE.MatchString(s) {
		return "", errors.New("slug must be 1-63 chars: lowercase letters, numbers, dashes, starting with a letter/number")
	}
	return s, nil
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func configString(ctx *sdk.AppCtx, key, fallback string) string {
	if ctx == nil || ctx.Config() == nil {
		return fallback
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return fallback
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func tenantIDSchema() map[string]any {
	return schemaObject(map[string]any{"tenant_id": strSchema()}, []string{"tenant_id"})
}

func strSchema() map[string]any   { return map[string]any{"type": "string"} }
func intSchema() map[string]any   { return map[string]any{"type": "integer"} }
func boolSchema() map[string]any  { return map[string]any{"type": "boolean"} }
func objSchema() map[string]any   { return map[string]any{"type": "object"} }
func arraySchema() map[string]any { return map[string]any{"type": "array"} }

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func intArg(args map[string]any, key string, fallback int) int {
	if n := int64Arg(args, key); n > 0 {
		return int(n)
	}
	return fallback
}

func mapArg(args map[string]any, key string) map[string]any {
	out := map[string]any{}
	if args == nil {
		return out
	}
	switch v := args[key].(type) {
	case map[string]any:
		for k, vv := range v {
			out[k] = vv
		}
	case map[string]string:
		for k, vv := range v {
			out[k] = vv
		}
	case json.RawMessage:
		_ = json.Unmarshal(v, &out)
	case []byte:
		_ = json.Unmarshal(v, &out)
	case string:
		if strings.TrimSpace(v) != "" {
			_ = json.Unmarshal([]byte(v), &out)
		}
	}
	return out
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

func firstPresent(first, second map[string]any, key string) any {
	if first != nil {
		if v, ok := first[key]; ok {
			return v
		}
	}
	if second != nil {
		if v, ok := second[key]; ok {
			return v
		}
	}
	return nil
}

func boolValue(v any, fallback bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		default:
			return fallback
		}
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case nil:
		return fallback
	default:
		return fallback
	}
}

func stringSliceFromAny(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		var parsed []string
		if json.Unmarshal([]byte(x), &parsed) == nil {
			return parsed
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func authOrgSlugForTenant(slug, tenantID string) string {
	base := strings.ToLower(strings.TrimSpace(slug))
	replacer := regexp.MustCompile(`[^a-z0-9-]+`)
	base = replacer.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "tenant"
	}
	suffix := strings.TrimPrefix(tenantID, "htn_")
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	if suffix == "" {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	maxBase := 32 - len(suffix) - 1
	if maxBase < 3 {
		maxBase = 3
	}
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	out := strings.Trim(base+"-"+suffix, "-")
	if len(out) < 3 {
		out += strings.Repeat("0", 3-len(out))
	}
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
}

func customerDisplayName(customer *Customer, tenant *Tenant) string {
	if customer != nil {
		if s := strings.TrimSpace(customer.Name); s != "" {
			return s
		}
		if s := strings.TrimSpace(customer.Email); s != "" {
			return s
		}
	}
	if tenant != nil {
		if s := strings.TrimSpace(tenant.OwnerEmail); s != "" {
			return s
		}
		if s := strings.TrimSpace(tenant.Slug); s != "" {
			return s
		}
	}
	return "Hosting Customer"
}

func isOptionalAuthMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "auth") && (strings.Contains(msg, "not installed") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "missing dependency") ||
		strings.Contains(msg, "dependency"))
}

func findAuthOrg(ctx *sdk.AppCtx, projectID, slug string) (map[string]any, error) {
	resp, err := callAppMap(ctx, "auth", "auth_orgs_list", withProject(projectID, map[string]any{}))
	if err != nil {
		return nil, err
	}
	for _, item := range arrayFromAny(resp["organizations"]) {
		org := mapFromAny(item)
		if strArg(org, "slug") == slug {
			return map[string]any{"organization": org}, nil
		}
	}
	return nil, fmt.Errorf("auth organization %q already exists but could not be loaded", slug)
}

func findAuthUser(ctx *sdk.AppCtx, projectID, orgSlug, email string) (map[string]any, error) {
	resp, err := callAppMap(ctx, "auth", "auth_users_search", withProject(projectID, map[string]any{
		"organization_slug": orgSlug,
		"q":                 email,
		"limit":             10,
	}))
	if err != nil {
		return nil, err
	}
	for _, item := range arrayFromAny(resp["users"]) {
		user := mapFromAny(item)
		if strings.EqualFold(strArg(user, "email"), email) {
			return map[string]any{"user": user}, nil
		}
	}
	return nil, fmt.Errorf("auth user %q already exists but could not be loaded", email)
}

func arrayFromAny(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, item)
		}
		return out
	case json.RawMessage:
		var out []any
		_ = json.Unmarshal(x, &out)
		return out
	case []byte:
		var out []any
		_ = json.Unmarshal(x, &out)
		return out
	case string:
		if strings.TrimSpace(x) != "" {
			var out []any
			_ = json.Unmarshal([]byte(x), &out)
			return out
		}
	}
	return nil
}

func mergeMaps(base map[string]any, overlays ...map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for _, overlay := range overlays {
		for k, v := range overlay {
			out[k] = v
		}
	}
	return out
}

func stringMapArg(args map[string]any, key string) map[string]string {
	out := map[string]string{}
	raw := mapArg(args, key)
	for k, v := range raw {
		if strings.TrimSpace(k) != "" {
			out[k] = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return out
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	default:
		return false
	}
}

func boolArgDefault(args map[string]any, key string, fallback bool) bool {
	if args == nil {
		return fallback
	}
	if _, ok := args[key]; !ok {
		return fallback
	}
	return boolArg(args, key)
}

func boolFromAny(v any, fallback bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		if strings.TrimSpace(x) == "" {
			return fallback
		}
		return strings.EqualFold(x, "true") || x == "1" || strings.EqualFold(x, "yes")
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	default:
		return fallback
	}
}

func actor(args map[string]any) string {
	return firstNonEmpty(strArg(args, "actor"), "tool")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func firstPositiveInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func mergeStringMaps(base map[string]string, override map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func mapValueString(vals map[string]any, key string) string {
	v, ok := vals[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func firstErr(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func nullableInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nullStr(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func jsonOrEmpty(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return fallback
	}
	return string(b)
}

func jsonRaw(v any) json.RawMessage {
	return json.RawMessage(jsonOrEmpty(v, "{}"))
}

func stringOrJSON(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}
