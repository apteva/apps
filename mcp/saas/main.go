// SaaS app: shared multi-tenant access control for Apteva apps.
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
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

const (
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusPastDue      = "past_due"
	StatusSuspended    = "suspended"
	StatusCancelled    = "cancelled"
	StatusFailed       = "failed"

	defaultUsageConcurrency = 8
	defaultUsagePageSize    = 100
	defaultUsageTimeout     = 30 * time.Second
	defaultUsageFreshness   = 5 * time.Minute
	fulfillmentClaimTimeout = 15 * time.Minute
	commerceClaimTimeout    = 15 * time.Minute
	paymentCompletionWait   = 10 * time.Second
	paymentCompletionPoll   = 25 * time.Millisecond
)

const (
	quotaStateBelow       = "below"
	quotaStateApproaching = "approaching"
	quotaStateReached     = "reached"
	quotaStateExceeded    = "exceeded"
	defaultQuotaThreshold = int64(80)
)

type App struct{}

var globalCtx *sdk.AppCtx
var usageCallSlots = make(chan struct{}, defaultUsageConcurrency)
var errFulfillmentInProgress = errors.New("fulfillment already in progress")

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("saas requires a db block")
	}
	if err := scrubFulfillmentHistory(ctx.AppDB()); err != nil {
		return fmt.Errorf("scrub fulfillment history: %w", err)
	}
	globalCtx = ctx
	pid := projectID(ctx, nil)
	if pid != "" {
		if err := seedPlans(ctx.AppDB(), pid); err != nil {
			return err
		}
	}
	ctx.Logger().Info("saas mounted", "version", a.Manifest().Version, "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Topic: "subscription.active", Handler: a.handleSubscriptionLifecycle},
		{Topic: "subscription.trialing", Handler: a.handleSubscriptionLifecycle},
		{Topic: "subscription.past_due", Handler: a.handleSubscriptionLifecycle},
		{Topic: "subscription.paused", Handler: a.handleSubscriptionLifecycle},
		{Topic: "subscription.cancelled", Handler: a.handleSubscriptionLifecycle},
		{Topic: "subscription.ended", Handler: a.handleSubscriptionLifecycle},
		{Topic: "subscription.cycle_due", Handler: a.handleSubscriptionCycleDue},
		{Topic: "subscription.change.applied", Handler: a.handleSubscriptionChangeApplied},
		{Topic: "invoice.paid", Handler: a.handleInvoicePaid},
		{Topic: "invoice.voided", Handler: a.handleInvoiceCollectionFailed},
		{Topic: "invoice.refunded", Handler: a.handleInvoiceCollectionFailed},
		{Topic: "invoice.payment_failed", Handler: a.handleInvoiceCollectionFailed},
		{Topic: "invoice.payment_action_required", Handler: a.handleInvoiceCollectionFailed},
		{Topic: "payment_method.attached", Handler: a.handlePaymentMethodAttached},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "usage-sync",
		Schedule: "@every 60s",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			if projectID(ctx, nil) == "" {
				return nil
			}
			_, err := a.toolUsageSync(ctx, map[string]any{})
			return err
		},
	}, {
		Name:     "checkout-recovery",
		Schedule: "@every 60s",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			return a.recoverExpiredCheckouts(ctx)
		},
	}, {
		Name:     "plan-change-recovery",
		Schedule: "@every 60s",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			return a.recoverPlanChanges(ctx)
		},
	}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/plans", Handler: a.handlePlans},
		{Pattern: "/accounts", Handler: a.handleAccounts},
		{Pattern: "/accounts/", Handler: a.handleAccountItem},
		{Pattern: "/customers", Handler: a.handleCustomers},
		{Pattern: "/usage", Handler: a.handleUsage},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "saas_plan_list", Description: "List SaaS plans with Catalog product identity.", InputSchema: schemaObject(nil, nil), Handler: a.toolPlanList},
		{Name: "saas_plan_get", Description: "Fetch one SaaS plan with Catalog product identity.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema()}, []string{"plan_key"}), Handler: a.toolPlanGet},
		{Name: "saas_plan_upsert", Description: "Create or update a SaaS plan.", InputSchema: schemaObject(map[string]any{
			"key": strSchema(), "name": strSchema(), "billing_mode": strSchema(), "catalog_product_id": intSchema(), "catalog_price_id": intSchema(),
			"catalog_discount_id": intSchema(), "subscription_required": boolSchema(), "metadata": objSchema(),
		}, []string{"key", "name"}), Handler: a.toolPlanUpsert},
		{Name: "saas_plan_feature_add", Description: "Add a feature grant to a SaaS plan.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "feature_key": strSchema(), "grant_type": strSchema(), "metadata": objSchema()}, []string{"plan_key", "feature_key"}), Handler: a.toolPlanFeatureAdd},
		{Name: "saas_plan_limit_set", Description: "Set a plan limit.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "feature_key": strSchema(), "limit_value": intSchema(), "reset_interval": strSchema(), "metadata": objSchema()}, []string{"plan_key", "feature_key"}), Handler: a.toolPlanLimitSet},
		{Name: "saas_plan_usage_source_add", Description: "Register a live usage source for a SaaS plan.", InputSchema: schemaObject(map[string]any{
			"plan_key": strSchema(), "app_name": strSchema(), "tool_name": strSchema(), "feature_prefix": strSchema(), "feature_key": strSchema(),
			"read_path": strSchema(), "quantity_path": strSchema(), "call_args": objSchema(), "metadata": objSchema(),
		}, []string{"plan_key", "app_name", "tool_name"}), Handler: a.toolPlanUsageSourceAdd},
		{Name: "saas_plan_action_add", Description: "Register a generic fulfillment action for a SaaS plan lifecycle event.", InputSchema: schemaObject(map[string]any{
			"plan_key": strSchema(), "event": strSchema(), "app_name": strSchema(), "tool_name": strSchema(),
			"args": objSchema(), "store": objSchema(), "failure_mode": strSchema(), "execution_policy": strSchema(),
			"persist_input": strSchema(), "persist_output": strSchema(), "sensitive_input_paths": strArraySchema(), "sensitive_output_paths": strArraySchema(),
			"enabled": boolSchema(), "metadata": objSchema(),
		}, []string{"plan_key", "event", "app_name", "tool_name"}), Handler: a.toolPlanActionAdd},
		{Name: "saas_plan_action_update", Description: "Update a generic fulfillment action by id.", InputSchema: schemaObject(map[string]any{
			"id": intSchema(), "args": objSchema(), "store": objSchema(), "failure_mode": strSchema(), "execution_policy": strSchema(),
			"persist_input": strSchema(), "persist_output": strSchema(), "sensitive_input_paths": strArraySchema(), "sensitive_output_paths": strArraySchema(),
			"enabled": boolSchema(), "metadata": objSchema(),
		}, []string{"id"}), Handler: a.toolPlanActionUpdate},
		{Name: "saas_plan_action_list", Description: "List generic fulfillment actions for a SaaS plan.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "event": strSchema()}, []string{"plan_key"}), Handler: a.toolPlanActionList},
		{Name: "saas_customer_create", Description: "Find or create a SaaS customer by email.", InputSchema: schemaObject(map[string]any{"email": strSchema(), "name": strSchema(), "billing_customer_id": intSchema(), "auth_user_id": intSchema(), "metadata": objSchema()}, []string{"email"}), Handler: a.toolCustomerCreate},
		{Name: "saas_checkout_create", Description: "Create a SaaS checkout: free activation, no-card trials, or paid Billing/Subscription checkout.", InputSchema: schemaObject(map[string]any{
			"owner_email": strSchema(), "customer_name": strSchema(), "slug": strSchema(), "plan_key": strSchema(),
			"create_owner_user": boolSchema(), "send_password_reset": boolSchema(),
			"idempotency_key": strSchema(), "payment_mode": strSchema(), "discount_code": strSchema(),
			"success_url": strSchema(), "cancel_url": strSchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolCheckoutCreate},
		{Name: "saas_checkout_get", Description: "Fetch durable checkout status and payment/setup URL.", InputSchema: schemaObject(map[string]any{"checkout_id": strSchema()}, []string{"checkout_id"}), Handler: a.toolCheckoutGet},
		{Name: "saas_checkout_mark_paid", Description: "Administratively record a manual checkout payment through Billing.", InputSchema: schemaObject(map[string]any{"checkout_id": strSchema(), "amount_cents": intSchema(), "method": strSchema(), "notes": strSchema()}, []string{"checkout_id"}), Handler: a.toolCheckoutMarkPaid},
		{Name: "saas_fulfillment_run", Description: "Run or retry configured fulfillment actions for a SaaS account lifecycle event.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "event": strSchema()}, []string{"account_id", "event"}), Handler: a.toolFulfillmentRun},
		{Name: "saas_account_create", Description: "Create a SaaS account and apply plan access.", InputSchema: schemaObject(map[string]any{
			"customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(), "owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(),
			"billing_customer_id": intSchema(), "auth_org_id": intSchema(), "auth_user_id": intSchema(), "subscription_id": intSchema(), "create_owner_user": boolSchema(), "send_password_reset": boolSchema(), "metadata": objSchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolAccountCreate},
		{Name: "saas_account_get", Description: "Fetch one SaaS account with customer and Billing summary.", InputSchema: accountIDSchema(), Handler: a.toolAccountGet},
		{Name: "saas_account_reconcile", Description: "Repair Subscription and Entitlements projections from the account's authoritative SaaS plan.", InputSchema: accountIDSchema(), Handler: a.toolAccountReconcile},
		{Name: "saas_account_change_plan", Description: "Upgrade or downgrade an active account within the same SaaS product, with durable proration and payment orchestration.", InputSchema: schemaObject(map[string]any{
			"account_id": strSchema(), "target_plan_key": strSchema(), "effective_at": strSchema(), "proration_policy": strSchema(),
			"discount_policy": strSchema(), "idempotency_key": strSchema(), "success_url": strSchema(), "cancel_url": strSchema(),
		}, []string{"account_id", "target_plan_key", "idempotency_key"}), Handler: a.toolAccountChangePlan},
		{Name: "saas_plan_change_get", Description: "Fetch a durable SaaS plan-change status and payment URL.", InputSchema: schemaObject(map[string]any{"change_id": strSchema()}, []string{"change_id"}), Handler: a.toolPlanChangeGet},
		{Name: "saas_account_list", Description: "List SaaS accounts with customer and Billing summaries.", InputSchema: schemaObject(map[string]any{
			"customer_id": intSchema(), "customer_email": strSchema(), "subscription_id": intSchema(), "status": strSchema(), "plan_key": strSchema(), "catalog_product_id": intSchema(),
			"created_before": strSchema(), "created_after": strSchema(), "has_paid": boolSchema(),
			"paid_since": strSchema(), "paid_until": strSchema(), "last_paid_before": strSchema(), "last_paid_after": strSchema(),
			"limit": intSchema(), "offset": intSchema(),
		}, nil), Handler: a.toolAccountList},
		{Name: "saas_billing_sync", Description: "Refresh linked Billing invoice/payment projections.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "limit": intSchema(), "offset": intSchema()}, nil), Handler: a.toolBillingSync},
		{Name: "saas_account_suspend", Description: "Suspend a SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountSuspend},
		{Name: "saas_account_resume", Description: "Resume a SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountResume},
		{Name: "saas_account_cancel", Description: "Cancel a SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountCancel},
		{Name: "saas_subscription_sync", Description: "Apply subscription status to a SaaS account.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "subscription_id": intSchema(), "subscription_status": strSchema(), "actor": strSchema(), "allow_cancelled_reactivation": boolSchema()}, []string{"subscription_status"}), Handler: a.toolSubscriptionSync},
		{Name: "saas_usage_sync", Description: "Pull live usage gauges from configured sources.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "status": strSchema()}, nil), Handler: a.toolUsageSync},
		{Name: "saas_usage_record", Description: "Upsert one live usage gauge.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "feature_key": strSchema(), "quantity": intSchema(), "source_app": strSchema(), "metadata": objSchema()}, []string{"account_id", "feature_key"}), Handler: a.toolUsageRecord},
		{Name: "saas_usage_get", Description: "Return live usage totals and plan limits.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "customer_id": intSchema(), "feature_key": strSchema()}, nil), Handler: a.toolUsageGet},
		{Name: "saas_access_check", Description: "Check account status and usage limit for a feature.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "feature_key": strSchema()}, []string{"account_id", "feature_key"}), Handler: a.toolAccessCheck},
	}
}

func main() { sdk.Run(&App{}) }

type Customer struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id"`
	Email             string          `json:"email"`
	Name              string          `json:"name"`
	BillingCustomerID *int64          `json:"billing_customer_id,omitempty"`
	AuthUserID        *int64          `json:"auth_user_id,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type Plan struct {
	Key                  string          `json:"key"`
	ProjectID            string          `json:"project_id"`
	Name                 string          `json:"name"`
	BillingMode          string          `json:"billing_mode"`
	CatalogProductID     *int64          `json:"catalog_product_id,omitempty"`
	CatalogProduct       *CatalogProduct `json:"catalog_product,omitempty"`
	CatalogPriceID       *int64          `json:"catalog_price_id,omitempty"`
	CatalogDiscountID    *int64          `json:"catalog_discount_id,omitempty"`
	SubscriptionRequired bool            `json:"subscription_required"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	Features             []PlanFeature   `json:"features,omitempty"`
	Limits               []PlanLimit     `json:"limits,omitempty"`
	UsageSources         []UsageSource   `json:"usage_sources,omitempty"`
	Actions              []PlanAction    `json:"actions,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

type CatalogProduct struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug,omitempty"`
	Type     string `json:"type,omitempty"`
	Category string `json:"category,omitempty"`
	Color    string `json:"color,omitempty"`
}

type PlanFeature struct {
	ID         int64           `json:"id"`
	ProjectID  string          `json:"project_id"`
	PlanKey    string          `json:"plan_key"`
	FeatureKey string          `json:"feature_key"`
	GrantType  string          `json:"grant_type"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

type PlanLimit struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id"`
	PlanKey       string          `json:"plan_key"`
	FeatureKey    string          `json:"feature_key"`
	LimitValue    int64           `json:"limit_value"`
	ResetInterval string          `json:"reset_interval"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

type UsageSource struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id"`
	PlanKey       string          `json:"plan_key"`
	AppName       string          `json:"app_name"`
	ToolName      string          `json:"tool_name"`
	FeaturePrefix string          `json:"feature_prefix,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

type PlanAction struct {
	ID                   int64           `json:"id"`
	ProjectID            string          `json:"project_id"`
	PlanKey              string          `json:"plan_key"`
	Event                string          `json:"event"`
	AppName              string          `json:"app_name"`
	ToolName             string          `json:"tool_name"`
	Args                 json.RawMessage `json:"args,omitempty"`
	Store                json.RawMessage `json:"store,omitempty"`
	FailureMode          string          `json:"failure_mode"`
	ExecutionPolicy      string          `json:"execution_policy"`
	PersistInput         string          `json:"persist_input"`
	PersistOutput        string          `json:"persist_output"`
	SensitiveInputPaths  json.RawMessage `json:"sensitive_input_paths,omitempty"`
	SensitiveOutputPaths json.RawMessage `json:"sensitive_output_paths,omitempty"`
	Enabled              bool            `json:"enabled"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

type FulfillmentRun struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id"`
	AccountID    string          `json:"account_id"`
	PlanActionID int64           `json:"plan_action_id"`
	TransitionID string          `json:"transition_id"`
	Event        string          `json:"event"`
	AppName      string          `json:"app_name"`
	ToolName     string          `json:"tool_name"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	Error        string          `json:"error,omitempty"`
	AttemptCount int64           `json:"attempt_count"`
	StartedAt    string          `json:"started_at,omitempty"`
	CompletedAt  string          `json:"completed_at,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

type LifecycleTransition struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	AccountID   string `json:"account_id"`
	Sequence    int64  `json:"sequence"`
	Event       string `json:"event"`
	FromStatus  string `json:"from_status"`
	ToStatus    string `json:"to_status"`
	SourceKey   string `json:"source_key,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	CompletedAt string `json:"completed_at,omitempty"`
}

type ProvisioningStep struct {
	Step         string          `json:"step"`
	Status       string          `json:"status"`
	AttemptCount int64           `json:"attempt_count"`
	Output       json.RawMessage `json:"output,omitempty"`
	LastError    string          `json:"last_error,omitempty"`
}

type UsageSourceState struct {
	UsageSourceID    int64  `json:"usage_source_id"`
	LastGenerationID string `json:"last_generation_id,omitempty"`
	LastSuccessAt    string `json:"last_success_at,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	FailureCount     int64  `json:"failure_count"`
}

type Account struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"project_id"`
	CustomerID      int64           `json:"customer_id"`
	AuthOrgID       *int64          `json:"auth_org_id,omitempty"`
	AuthUserID      *int64          `json:"auth_user_id,omitempty"`
	SubscriptionID  *int64          `json:"subscription_id,omitempty"`
	Slug            string          `json:"slug"`
	OwnerEmail      string          `json:"owner_email"`
	PlanKey         string          `json:"plan_key"`
	Status          string          `json:"status"`
	LastUsageSyncAt string          `json:"last_usage_sync_at,omitempty"`
	LastError       string          `json:"last_error"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	Customer        *Customer       `json:"customer,omitempty"`
	Billing         *BillingSummary `json:"billing,omitempty"`
}

type BillingSummary struct {
	BillingCustomerID *int64           `json:"billing_customer_id,omitempty"`
	HasPaid           bool             `json:"has_paid"`
	PaymentCount      int64            `json:"payment_count"`
	PaidInvoiceCount  int64            `json:"paid_invoice_count"`
	LatestInvoiceID   *int64           `json:"latest_invoice_id,omitempty"`
	FirstPaidAt       string           `json:"first_paid_at,omitempty"`
	LastPaidAt        string           `json:"last_paid_at,omitempty"`
	NetPaidByCurrency map[string]int64 `json:"net_paid_by_currency,omitempty"`
	DataComplete      bool             `json:"data_complete"`
	SyncedAt          string           `json:"synced_at,omitempty"`
}

type billingInvoiceProjection struct {
	InvoiceID         int64
	AccountID         string
	BillingCustomerID int64
	SubscriptionID    int64
	CycleID           int64
	Status            string
	Currency          string
	TotalCents        int64
	AmountPaidCents   int64
	PaidAt            string
	SourceCreatedAt   string
	SourceUpdatedAt   string
	Payments          []billingPaymentProjection
}

type billingPaymentProjection struct {
	PaymentID       int64
	AmountCents     int64
	Currency        string
	Method          string
	ReceivedAt      string
	SourceCreatedAt string
}

type CommerceOperation struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id"`
	OperationKey      string          `json:"operation_key"`
	AccountID         string          `json:"account_id"`
	SubscriptionID    int64           `json:"subscription_id"`
	CycleID           int64           `json:"cycle_id"`
	CheckoutID        string          `json:"checkout_id,omitempty"`
	BillingCustomerID *int64          `json:"billing_customer_id,omitempty"`
	InvoiceID         *int64          `json:"invoice_id,omitempty"`
	Status            string          `json:"status"`
	Stage             string          `json:"stage"`
	AttemptCount      int64           `json:"attempt_count"`
	Prepared          json.RawMessage `json:"prepared,omitempty"`
	PaymentLink       json.RawMessage `json:"payment_link,omitempty"`
	LastError         string          `json:"last_error,omitempty"`
	LeaseUntil        string          `json:"lease_until,omitempty"`
	StartedAt         string          `json:"started_at,omitempty"`
	CompletedAt       string          `json:"completed_at,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type Checkout struct {
	ID                 string          `json:"id"`
	ProjectID          string          `json:"project_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	RequestFingerprint string          `json:"-"`
	CustomerID         int64           `json:"customer_id"`
	AccountID          string          `json:"account_id,omitempty"`
	PlanKey            string          `json:"plan_key"`
	Slug               string          `json:"slug"`
	OwnerEmail         string          `json:"owner_email"`
	SubscriptionID     *int64          `json:"subscription_id,omitempty"`
	CycleID            *int64          `json:"cycle_id,omitempty"`
	BillingCustomerID  *int64          `json:"billing_customer_id,omitempty"`
	InvoiceID          *int64          `json:"invoice_id,omitempty"`
	PaymentMethodID    *int64          `json:"payment_method_id,omitempty"`
	Status             string          `json:"status"`
	Stage              string          `json:"stage"`
	PaymentMode        string          `json:"payment_mode,omitempty"`
	PaymentURL         string          `json:"payment_url,omitempty"`
	ProviderSessionID  string          `json:"provider_session_id,omitempty"`
	TrialEndsAt        string          `json:"trial_ends_at,omitempty"`
	SessionExpiresAt   string          `json:"session_expires_at,omitempty"`
	AttemptCount       int64           `json:"attempt_count"`
	Result             json.RawMessage `json:"result,omitempty"`
	LastError          string          `json:"last_error,omitempty"`
	LeaseUntil         string          `json:"lease_until,omitempty"`
	StartedAt          string          `json:"started_at,omitempty"`
	CompletedAt        string          `json:"completed_at,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

type UsageTotal struct {
	ProjectID   string `json:"project_id"`
	AccountID   string `json:"account_id"`
	CustomerID  int64  `json:"customer_id"`
	FeatureKey  string `json:"feature_key"`
	Quantity    int64  `json:"quantity"`
	LimitValue  *int64 `json:"limit_value,omitempty"`
	OverLimit   bool   `json:"over_limit"`
	ObservedAt  string `json:"observed_at,omitempty"`
	SourceCount int64  `json:"source_count"`
}

type quotaMeasurement struct {
	FeatureKey string
	Quantity   int64
	LimitValue int64
	Metadata   json.RawMessage
}

type quotaTransition struct {
	PreviousState    string
	State            string
	Quantity         int64
	LimitValue       int64
	ThresholdPercent int64
}

type usageSnapshotResponse struct {
	Usage []usageGauge `json:"usage"`
}

type usageSourceConfig struct {
	FeatureKey   string         `json:"feature_key,omitempty"`
	QuantityPath string         `json:"quantity_path,omitempty"`
	ReadPath     string         `json:"read_path,omitempty"`
	CallArgs     map[string]any `json:"call_args,omitempty"`
}

type entitlementCheckResponse struct {
	Allowed bool `json:"allowed"`
}

type usageGauge struct {
	FeatureKey string         `json:"feature_key"`
	Quantity   int64          `json:"quantity"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func parseUsageSourceConfig(raw json.RawMessage) usageSourceConfig {
	var cfg usageSourceConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.QuantityPath == "" {
		cfg.QuantityPath = cfg.ReadPath
	}
	return cfg
}

func usageGaugesFromResponse(src UsageSource, cfg usageSourceConfig, body map[string]any) ([]usageGauge, error) {
	path := firstNonEmpty(cfg.QuantityPath, cfg.ReadPath)
	if cfg.FeatureKey != "" || path != "" {
		feature := firstNonEmpty(cfg.FeatureKey, src.FeaturePrefix)
		if feature == "" {
			return nil, errors.New("usage source feature_key required when using read_path")
		}
		if path == "" {
			path = "quantity"
		}
		value, ok := valueAtPath(body, path)
		if !ok {
			return nil, fmt.Errorf("usage source read_path %q not found", path)
		}
		return []usageGauge{{FeatureKey: feature, Quantity: int64FromAny(value), Metadata: map[string]any{"read_path": path}}}, nil
	}

	rawUsage, ok := body["usage"]
	if !ok {
		return nil, nil
	}
	var parsed usageSnapshotResponse
	b, err := json.Marshal(map[string]any{"usage": rawUsage})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, err
	}
	return parsed.Usage, nil
}

func valueAtPath(v any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return v, true
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false
		}
		if part == "length" {
			switch t := cur.(type) {
			case []any:
				cur = len(t)
			case []map[string]any:
				cur = len(t)
			case string:
				cur = len(t)
			default:
				return nil, false
			}
			continue
		}
		m := mapFromAny(cur)
		if m == nil {
			return nil, false
		}
		next, ok := m[part]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func expandUsageArgs(args map[string]any, pid string, acct *Account) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = expandUsageValue(v, pid, acct)
	}
	return out
}

func expandUsageValue(v any, pid string, acct *Account) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			out[k] = expandUsageValue(v, pid, acct)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = expandUsageValue(v, pid, acct)
		}
		return out
	case string:
		return expandUsageString(t, pid, acct)
	default:
		return v
	}
}

func expandUsageString(s, pid string, acct *Account) any {
	values := map[string]any{
		"{{project.id}}":   pid,
		"{{account.id}}":   acct.ID,
		"{{account.slug}}": acct.Slug,
		"{{customer.id}}":  acct.CustomerID,
		"{{plan.key}}":     acct.PlanKey,
		"{{auth_org.id}}":  int64PtrValue(acct.AuthOrgID),
		"$project.id":      pid,
		"$account.id":      acct.ID,
		"$account.slug":    acct.Slug,
		"$customer.id":     acct.CustomerID,
		"$plan.key":        acct.PlanKey,
		"$auth_org.id":     int64PtrValue(acct.AuthOrgID),
	}
	if v, ok := values[s]; ok {
		return v
	}
	ctx := fulfillmentTemplateContext(pid, acct, nil, nil)
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") {
		path := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "{{"), "}}"))
		if v, ok := valueAtPath(ctx, path); ok {
			return v
		}
	}
	out := s
	for token, value := range values {
		out = strings.ReplaceAll(out, token, fmt.Sprint(value))
	}
	out = expandTemplateString(out, ctx).(string)
	return out
}

func (a *App) toolPlanList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	plans, err := dbPlanList(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	a.enrichPlansWithCatalog(ctx, pid, plans)
	return map[string]any{"plans": plans, "count": len(plans)}, nil
}

func (a *App) toolPlanGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	p, err := dbPlanGet(ctx.AppDB(), pid, firstNonEmpty(strArg(args, "plan_key"), strArg(args, "key")))
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("plan not found")
	}
	a.enrichPlansWithCatalog(ctx, pid, []*Plan{p})
	return map[string]any{"plan": p}, nil
}

// enrichPlansWithCatalog adds display data without copying Catalog ownership
// into SaaS. Plan reads remain available if Catalog is temporarily unavailable.
func (a *App) enrichPlansWithCatalog(ctx *sdk.AppCtx, pid string, plans []*Plan) {
	if len(plans) == 0 {
		return
	}
	var out struct {
		Products []*CatalogProduct `json:"products"`
	}
	if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_products_list", map[string]any{
		"_project_id": pid,
		"archived":    true,
		"limit":       200,
	}, &out); err != nil {
		return
	}
	products := make(map[int64]*CatalogProduct, len(out.Products))
	for _, product := range out.Products {
		if product != nil {
			products[product.ID] = product
		}
	}
	for _, plan := range plans {
		if plan != nil && plan.CatalogProductID != nil {
			plan.CatalogProduct = products[*plan.CatalogProductID]
		}
	}
}

func (a *App) toolPlanUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	p, err := dbPlanUpsert(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"plan": p}, nil
}

func (a *App) toolPlanFeatureAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	f, err := dbPlanFeatureUpsert(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"feature": f}, nil
}

func (a *App) toolPlanLimitSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	if metadata := mapFromAny(args["metadata"]); metadata != nil {
		if raw, ok := metadata["warning_threshold_percent"]; ok {
			threshold := int64FromAny(raw)
			if threshold < 1 || threshold > 99 {
				return nil, errors.New("warning_threshold_percent must be between 1 and 99")
			}
		}
	}
	l, err := dbPlanLimitUpsert(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"limit": l}, nil
}

func (a *App) toolPlanUsageSourceAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	s, err := dbUsageSourceUpsert(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage_source": s}, nil
}

func (a *App) toolPlanActionAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	action, err := dbPlanActionInsert(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"action": action}, nil
}

func (a *App) toolPlanActionUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	action, err := dbPlanActionUpdate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"action": action}, nil
}

func (a *App) toolPlanActionList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	planKey, err := normalizeKey(strArg(args, "plan_key"))
	if err != nil {
		return nil, err
	}
	actions, err := dbPlanActions(ctx.AppDB(), pid, planKey, normalizeFulfillmentEvent(strArg(args, "event")), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"actions": actions, "count": len(actions)}, nil
}

func (a *App) toolCustomerCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	c, err := dbCustomerUpsert(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"customer": c}, nil
}

func (a *App) toolCheckoutCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.createCheckout(ctx, args)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) toolCheckoutGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	checkout, err := dbCheckoutGet(ctx.AppDB(), pid, strArg(args, "checkout_id"))
	if err != nil || checkout == nil {
		return nil, firstErr(err, errors.New("checkout not found"))
	}
	return a.checkoutResponse(ctx, checkout)
}

func (a *App) toolCheckoutMarkPaid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	checkout, err := dbCheckoutGet(ctx.AppDB(), pid, strArg(args, "checkout_id"))
	if err != nil || checkout == nil {
		return nil, firstErr(err, errors.New("checkout not found"))
	}
	invoiceID := int64PtrValue(checkout.InvoiceID)
	if invoiceID == 0 {
		return nil, errors.New("checkout has no invoice")
	}
	amount := int64Arg(args, "amount_cents")
	if amount <= 0 {
		return nil, errors.New("amount_cents must be greater than zero")
	}
	var paymentOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "payments_record", map[string]any{
		"_project_id": pid, "invoice_id": invoiceID, "amount_cents": amount,
		"method": firstNonEmpty(strArg(args, "method"), "wire"),
		"notes":  firstNonEmpty(strArg(args, "notes"), "SaaS administrative payment"),
	}, &paymentOut); err != nil {
		return nil, fmt.Errorf("record checkout payment: %w", err)
	}
	invoice := unwrapMap(paymentOut, "invoice")
	paymentComplete := true
	if strArg(invoice, "status") == "paid" {
		if err := a.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: pid, Data: map[string]any{"id": invoiceID, "status": "paid"}}); err != nil {
			return nil, err
		}
		op, err := waitForCommercePayment(ctx.AppDB(), pid, invoiceID, paymentCompletionWait)
		if err != nil {
			return nil, err
		}
		paymentComplete = op != nil && op.Status == "paid"
	}
	checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
	response, err := a.checkoutResponse(ctx, checkout)
	if err != nil {
		return nil, err
	}
	response["payment"] = unwrapMap(paymentOut, "payment")
	response["invoice"] = invoice
	if !paymentComplete {
		response["status"] = "processing_payment"
		response["requires_payment"] = false
		response["payment_processing"] = true
	}
	return response, nil
}

func (a *App) toolFulfillmentRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	runs, err := a.runFulfillment(ctx, pid, acct, normalizeFulfillmentEvent(strArg(args, "event")))
	if err != nil {
		return nil, err
	}
	acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	return map[string]any{"account": acct, "runs": runs, "count": len(runs)}, nil
}

func (a *App) toolAccountCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	planKey := firstNonEmpty(strArg(args, "plan_key"), "free")
	plan, err := dbPlanGet(ctx.AppDB(), pid, planKey)
	if err != nil {
		return nil, err
	}
	if plan != nil && (plan.BillingMode != "free" || plan.SubscriptionRequired) {
		return nil, errors.New("paid and subscription plans must be created through saas_checkout_create")
	}
	acct, activated, err := a.createAccount(ctx, args)
	if err != nil {
		return nil, err
	}
	if activated && acct.Status == StatusActive {
		ctx.Emit("saas.account.active", map[string]any{"account_id": acct.ID, "customer_id": acct.CustomerID, "plan_key": acct.PlanKey})
	}
	return map[string]any{"account": acct}, nil
}

func (a *App) toolAccountGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil {
		return nil, err
	}
	if acct == nil {
		return nil, errors.New("account not found")
	}
	if err := dbHydrateAccounts(ctx.AppDB(), pid, []*Account{acct}); err != nil {
		return nil, err
	}
	return map[string]any{"account": acct}, nil
}

func (a *App) toolAccountList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := validateAccountSearchArgs(args); err != nil {
		return nil, err
	}
	out, total, limit, offset, err := dbAccountSearch(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	pending, err := dbBillingProjectionPendingCount(ctx.AppDB(), pid, "")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"accounts": out, "count": len(out), "total": total,
		"limit": limit, "offset": offset, "has_more": offset+len(out) < total,
		"billing_sync_pending": pending,
	}, nil
}

func (a *App) toolBillingSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	accountID := strArg(args, "account_id")
	if accountID != "" {
		account, err := dbAccountGet(ctx.AppDB(), pid, accountID)
		if err != nil || account == nil {
			return nil, firstErr(err, errors.New("account not found"))
		}
	}
	limit := int64Arg(args, "limit")
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := int64Arg(args, "offset")
	if offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	return a.syncBillingOperations(ctx, pid, accountID, accountID == "", int(limit), int(offset))
}

func validateAccountSearchArgs(args map[string]any) error {
	for _, key := range []string{"created_before", "created_after", "paid_since", "paid_until", "last_paid_before", "last_paid_after"} {
		value := strArg(args, key)
		if value == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("%s must be an RFC3339 timestamp", key)
		}
	}
	if _, ok := args["has_paid"]; ok && !boolArg(args, "has_paid") {
		if strArg(args, "paid_since") != "" || strArg(args, "paid_until") != "" || strArg(args, "last_paid_before") != "" || strArg(args, "last_paid_after") != "" {
			return errors.New("has_paid=false cannot be combined with payment date filters")
		}
	}
	if int64Arg(args, "offset") < 0 {
		return errors.New("offset must be non-negative")
	}
	return nil
}

func (a *App) toolAccountSuspend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setAccountStatus(ctx, args, StatusSuspended, "suspended")
}

func (a *App) toolAccountResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setAccountStatus(ctx, args, StatusActive, "resumed")
}

func (a *App) toolAccountCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setAccountStatus(ctx, args, StatusCancelled, "cancelled")
}

func (a *App) toolSubscriptionSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	status := strings.ToLower(strings.TrimSpace(strArg(args, "subscription_status")))
	if strArg(args, "account_id") == "" && int64Arg(args, "subscription_id") != 0 {
		pid, err := requireProject(ctx, args)
		if err != nil {
			return nil, err
		}
		accts, err := dbAccountList(ctx.AppDB(), pid, map[string]any{"subscription_id": int64Arg(args, "subscription_id")})
		if err != nil {
			return nil, err
		}
		var updated []*Account
		for _, acct := range accts {
			out, err := a.toolSubscriptionSync(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID, "subscription_id": int64Arg(args, "subscription_id"), "subscription_status": status, "actor": actor(args), "allow_cancelled_reactivation": boolArg(args, "allow_cancelled_reactivation")})
			if err != nil {
				return nil, err
			}
			updated = append(updated, out.(map[string]any)["account"].(*Account))
		}
		return map[string]any{"accounts": updated, "count": len(updated)}, nil
	}
	switch status {
	case "trialing", "active", "resumed":
		return a.setAccountStatus(ctx, args, StatusActive, "subscription."+status)
	case "past_due":
		return a.setAccountStatus(ctx, args, StatusPastDue, "subscription.past_due")
	case "paused":
		return a.setAccountStatus(ctx, args, StatusSuspended, "subscription.paused")
	case "cancelled", "ended":
		return a.setAccountStatus(ctx, args, StatusCancelled, "subscription."+status)
	default:
		return nil, fmt.Errorf("unsupported subscription_status %q", status)
	}
}

func (a *App) toolUsageSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	concurrency := configInt(ctx, "usage_sync_concurrency", defaultUsageConcurrency)
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > defaultUsageConcurrency {
		concurrency = defaultUsageConcurrency
	}
	timeout := time.Duration(configInt(ctx, "usage_sync_timeout_seconds", int(defaultUsageTimeout/time.Second))) * time.Second
	syncedAccounts, records, failed := 0, 0, 0
	var syncErrors []string
	if id := strArg(args, "account_id"); id != "" {
		acct, err := dbAccountGet(ctx.AppDB(), pid, id)
		if err != nil || acct == nil {
			return nil, firstErr(err, errors.New("account not found"))
		}
		records, failed, syncErrors = a.syncAccountBatch(ctx, pid, []*Account{acct}, concurrency, timeout)
		syncedAccounts = 1
	} else {
		statuses := []string{StatusActive, StatusPastDue}
		if status := strArg(args, "status"); status != "" {
			statuses = []string{status}
		}
		for offset := 0; ; offset += defaultUsagePageSize {
			page, pageErr := dbAccountPage(ctx.AppDB(), pid, statuses, defaultUsagePageSize, offset)
			if pageErr != nil {
				return nil, pageErr
			}
			pageRecords, pageFailed, pageErrors := a.syncAccountBatch(ctx, pid, page, concurrency, timeout)
			syncedAccounts += len(page)
			records += pageRecords
			failed += pageFailed
			syncErrors = append(syncErrors, pageErrors...)
			if len(page) < defaultUsagePageSize {
				break
			}
		}
	}
	return map[string]any{"synced_accounts": syncedAccounts, "records": records, "failed_sources": failed, "errors": syncErrors}, nil
}

func (a *App) syncAccountBatch(ctx *sdk.AppCtx, pid string, accounts []*Account, concurrency int, timeout time.Duration) (int, int, []string) {
	type syncResult struct {
		records int
		failed  int
		errors  []string
	}
	if len(accounts) == 0 {
		return 0, 0, nil
	}
	if concurrency > len(accounts) {
		concurrency = len(accounts)
	}
	jobs := make(chan *Account)
	results := make(chan syncResult, len(accounts))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for acct := range jobs {
				records, failed, errs := a.syncAccountUsage(ctx, pid, acct, timeout)
				results <- syncResult{records: records, failed: failed, errors: errs}
			}
		}()
	}
	go func() {
		for _, acct := range accounts {
			jobs <- acct
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	records, failed := 0, 0
	var syncErrors []string
	for result := range results {
		records += result.records
		failed += result.failed
		syncErrors = append(syncErrors, result.errors...)
	}
	return records, failed, syncErrors
}

func (a *App) syncAccountUsage(ctx *sdk.AppCtx, pid string, acct *Account, timeout time.Duration) (int, int, []string) {
	sources, err := dbUsageSources(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil {
		return 0, 1, []string{acct.ID + ": " + err.Error()}
	}
	records, failed, succeeded := 0, 0, 0
	var syncErrors []string
	for _, src := range sources {
		cfg := parseUsageSourceConfig(src.Metadata)
		input := map[string]any{"_project_id": pid, "account_id": acct.ID, "customer_id": acct.CustomerID, "auth_org_id": int64PtrValue(acct.AuthOrgID), "plan_key": acct.PlanKey}
		for k, v := range expandUsageArgs(cfg.CallArgs, pid, acct) {
			input[k] = v
		}
		out, callErr := callAppResultWithTimeout(ctx, src.AppName, src.ToolName, input, timeout)
		var gauges []usageGauge
		if callErr == nil {
			gauges, callErr = usageGaugesFromResponse(src, cfg, out)
		}
		if callErr != nil {
			failed++
			_ = dbUsageSourceFailure(ctx.AppDB(), pid, acct.ID, src.ID, callErr.Error())
			_ = recordEvent(ctx.AppDB(), pid, acct.ID, "usage_sync.failed", src.AppName, map[string]any{"tool": src.ToolName, "error": callErr.Error()})
			syncErrors = append(syncErrors, fmt.Sprintf("%s/%s.%s: %v", acct.ID, src.AppName, src.ToolName, callErr))
			continue
		}
		generation := newID("usg")
		if err := dbUsageSourceReplace(ctx.AppDB(), pid, acct, src, generation, gauges); err != nil {
			failed++
			_ = dbUsageSourceFailure(ctx.AppDB(), pid, acct.ID, src.ID, err.Error())
			syncErrors = append(syncErrors, fmt.Sprintf("%s/%s.%s: %v", acct.ID, src.AppName, src.ToolName, err))
			continue
		}
		succeeded++
		records += len(gauges)
	}
	if succeeded > 0 {
		if err := a.evaluateQuotaTransitions(ctx, pid, acct); err != nil {
			failed++
			syncErrors = append(syncErrors, fmt.Sprintf("%s/quota: %v", acct.ID, err))
		}
	}
	if failed == 0 {
		_ = dbAccountUsageSynced(ctx.AppDB(), pid, acct.ID)
	} else if len(syncErrors) > 0 {
		_ = dbAccountSetLastError(ctx.AppDB(), pid, acct.ID, strings.Join(syncErrors, "; "))
	}
	return records, failed, syncErrors
}

func callAppResultWithTimeout(ctx *sdk.AppCtx, appName, toolName string, input map[string]any, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = defaultUsageTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case usageCallSlots <- struct{}{}:
	case <-timer.C:
		return nil, fmt.Errorf("usage source %s.%s concurrency wait timed out", appName, toolName)
	}
	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-usageCallSlots }()
		var out map[string]any
		err := ctx.PlatformAPI().CallAppResult(appName, toolName, input, &out)
		done <- result{out: out, err: err}
	}()
	select {
	case result := <-done:
		return result.out, result.err
	case <-timer.C:
		return nil, fmt.Errorf("usage source %s.%s timed out after %s", appName, toolName, timeout)
	}
}

func (a *App) toolUsageRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	if err := dbUsageSnapshotUpsert(ctx.AppDB(), pid, acct, firstNonEmpty(strArg(args, "source_app"), "manual"), strArg(args, "feature_key"), int64Arg(args, "quantity"), mapFromAny(args["metadata"])); err != nil {
		return nil, err
	}
	if err := a.evaluateQuotaTransitions(ctx, pid, acct); err != nil {
		return nil, err
	}
	usage, err := dbUsageTotals(ctx.AppDB(), pid, map[string]any{"account_id": acct.ID, "feature_key": strArg(args, "feature_key")})
	return map[string]any{"usage": usage}, err
}

func (a *App) evaluateQuotaTransitions(ctx *sdk.AppCtx, pid string, acct *Account) error {
	measurements, err := dbQuotaMeasurements(ctx.AppDB(), pid, acct)
	if err != nil {
		return err
	}
	for _, measurement := range measurements {
		threshold := quotaWarningThreshold(measurement.Metadata)
		state := quotaStateFor(measurement.Quantity, measurement.LimitValue, threshold)
		transition, changed, err := dbQuotaStateApply(ctx.AppDB(), pid, acct, measurement, threshold, state)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		topic := quotaEventForTransition(transition.PreviousState, transition.State)
		if topic == "" {
			continue
		}
		percentage := float64(0)
		if transition.LimitValue > 0 {
			percentage = float64(transition.Quantity) * 100 / float64(transition.LimitValue)
		}
		payload := map[string]any{
			"account_id": acct.ID, "customer_id": acct.CustomerID,
			"auth_org_id": int64PtrValue(acct.AuthOrgID), "auth_user_id": int64PtrValue(acct.AuthUserID),
			"plan_key": acct.PlanKey, "feature_key": measurement.FeatureKey,
			"quantity": transition.Quantity, "limit": transition.LimitValue, "percentage": percentage,
			"threshold_percent": transition.ThresholdPercent,
			"previous_state":    transition.PreviousState, "state": transition.State,
		}
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, topic, "usage", payload)
		ctx.Emit(topic, payload)
	}
	return nil
}

func quotaWarningThreshold(metadata json.RawMessage) int64 {
	var values map[string]any
	if len(metadata) > 0 && json.Unmarshal(metadata, &values) == nil {
		if threshold := int64FromAny(values["warning_threshold_percent"]); threshold >= 1 && threshold <= 99 {
			return threshold
		}
	}
	return defaultQuotaThreshold
}

func quotaStateFor(quantity, limitValue, thresholdPercent int64) string {
	if limitValue <= 0 {
		return quotaStateBelow
	}
	if quantity > limitValue {
		return quotaStateExceeded
	}
	if quantity == limitValue {
		return quotaStateReached
	}
	if float64(quantity)*100 >= float64(limitValue)*float64(thresholdPercent) {
		return quotaStateApproaching
	}
	return quotaStateBelow
}

func quotaStateSeverity(state string) int {
	switch state {
	case quotaStateApproaching:
		return 1
	case quotaStateReached:
		return 2
	case quotaStateExceeded:
		return 3
	default:
		return 0
	}
}

func quotaEventForTransition(previous, current string) string {
	if previous == current {
		return ""
	}
	if quotaStateSeverity(current) < quotaStateSeverity(previous) {
		return "saas.quota.recovered"
	}
	switch current {
	case quotaStateApproaching:
		return "saas.quota.approaching"
	case quotaStateReached:
		return "saas.quota.reached"
	case quotaStateExceeded:
		return "saas.quota.exceeded"
	default:
		return ""
	}
}

func (a *App) toolUsageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbUsageTotals(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": out, "count": len(out)}, nil
}

func (a *App) toolAccessCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	feature := strArg(args, "feature_key")
	if feature == "" {
		return nil, errors.New("feature_key required")
	}
	var entitlement entitlementCheckResponse
	if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlements_check", map[string]any{
		"_project_id":  pid,
		"subject_type": "saas_account",
		"subject_id":   acct.ID,
		"feature_key":  feature,
	}, &entitlement); err != nil {
		return nil, fmt.Errorf("check entitlement %s: %w", feature, err)
	}
	allowedStatus := acct.Status == StatusActive
	usage, err := dbUsageTotals(ctx.AppDB(), pid, map[string]any{"account_id": acct.ID, "feature_key": feature})
	if err != nil {
		return nil, err
	}
	overLimit := false
	for _, u := range usage {
		if u.OverLimit {
			overLimit = true
			break
		}
	}
	freshness := time.Duration(configInt(ctx, "usage_freshness_seconds", int(defaultUsageFreshness/time.Second))) * time.Second
	staleSources, err := dbUsageStaleSources(ctx.AppDB(), pid, acct, time.Now().UTC(), freshness)
	if err != nil {
		return nil, err
	}
	usageUnknown := len(staleSources) > 0
	return map[string]any{
		"allowed":       allowedStatus && entitlement.Allowed && !overLimit && !usageUnknown,
		"entitled":      entitlement.Allowed,
		"status":        acct.Status,
		"over_limit":    overLimit,
		"usage_unknown": usageUnknown,
		"stale_sources": staleSources,
		"usage":         usage,
	}, nil
}

func (a *App) createAccount(ctx *sdk.AppCtx, args map[string]any) (*Account, bool, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, false, err
	}
	planKey := firstNonEmpty(strArg(args, "plan_key"), "free")
	plan, err := dbPlanGet(ctx.AppDB(), pid, planKey)
	if err != nil {
		return nil, false, err
	}
	if plan == nil {
		return nil, false, fmt.Errorf("plan %q not found", planKey)
	}
	slug, err := normalizeSlug(strArg(args, "slug"))
	if err != nil {
		return nil, false, err
	}
	owner := strings.ToLower(strings.TrimSpace(strArg(args, "owner_email")))
	if owner == "" {
		return nil, false, errors.New("owner_email required")
	}
	customer, err := resolveCustomer(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, false, err
	}

	acct, err := dbAccountBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		return nil, false, err
	}
	if acct != nil {
		if !strings.EqualFold(acct.OwnerEmail, owner) || acct.PlanKey != plan.Key || acct.CustomerID != customer.ID {
			return nil, false, fmt.Errorf("slug %q already belongs to another SaaS account", slug)
		}
		if err := dbAccountMergeProvisioningInput(ctx.AppDB(), pid, acct.ID, args); err != nil {
			return nil, false, err
		}
		acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
		if acct.Status == StatusActive || (acct.Status != StatusProvisioning && acct.Status != StatusFailed) {
			return acct, false, nil
		}
		if acct.Status == StatusFailed {
			acct, err = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusProvisioning, "")
			if err != nil {
				return nil, false, err
			}
		}
	} else {
		meta := mergeMetadata(args["metadata"], map[string]any{
			"provisioning": map[string]any{
				"create_owner_user":   boolArg(args, "create_owner_user"),
				"send_password_reset": boolArg(args, "send_password_reset"),
				"skip_fulfillment":    boolArg(args, "skip_fulfillment"),
			},
		})
		acct = &Account{
			ID:             newID("sac"),
			ProjectID:      pid,
			CustomerID:     customer.ID,
			AuthOrgID:      nullableInt64(int64Arg(args, "auth_org_id")),
			AuthUserID:     nullableInt64(firstNonZero(int64Arg(args, "auth_user_id"), int64PtrValue(customer.AuthUserID))),
			SubscriptionID: nullableInt64(int64Arg(args, "subscription_id")),
			Slug:           slug,
			OwnerEmail:     owner,
			PlanKey:        plan.Key,
			Status:         StatusProvisioning,
			Metadata:       jsonRaw(meta),
		}
		if err := dbAccountInsert(ctx.AppDB(), acct); err != nil {
			return nil, false, err
		}
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "provisioning.started", actor(args), map[string]any{"plan_key": plan.Key})
	}

	activated, err := a.resumeProvisioning(ctx, pid, acct, customer, plan)
	if err != nil {
		if errors.Is(err, errFulfillmentInProgress) {
			acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
			return acct, false, nil
		}
		_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusFailed, err.Error())
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "provisioning.failed", "provisioning", map[string]any{"error": err.Error()})
		return nil, false, err
	}
	acct, err = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	return acct, activated, err
}

func (a *App) resumeProvisioning(ctx *sdk.AppCtx, pid string, acct *Account, customer *Customer, plan *Plan) (bool, error) {
	transition, err := dbLifecycleTransitionReserve(ctx.AppDB(), pid, acct.ID, "account_active", acct.Status, StatusActive, "provisioning:activation")
	if err != nil {
		return false, err
	}
	if transition.Status == "completed" && acct.Status == StatusActive {
		return false, nil
	}
	meta := mapFromAny(acct.Metadata)
	provisioning := mapFromAny(meta["provisioning"])

	if err := runProvisioningStep(ctx.AppDB(), pid, acct.ID, "auth_org", func() (any, error) {
		if acct.AuthOrgID != nil {
			return map[string]any{"auth_org_id": *acct.AuthOrgID}, nil
		}
		id, err := createAuthOrg(ctx, pid, acct.Slug, firstNonEmpty(customer.Name, acct.Slug))
		if err != nil {
			return nil, err
		}
		if err := dbAccountSetAuthOrg(ctx.AppDB(), pid, acct.ID, *id); err != nil {
			return nil, err
		}
		acct.AuthOrgID = id
		return map[string]any{"auth_org_id": *id}, nil
	}); err != nil {
		return false, err
	}

	if err := runProvisioningStep(ctx.AppDB(), pid, acct.ID, "auth_user", func() (any, error) {
		if !boolArg(provisioning, "create_owner_user") || acct.AuthUserID != nil {
			return map[string]any{"auth_user_id": int64PtrValue(acct.AuthUserID), "skipped": !boolArg(provisioning, "create_owner_user")}, nil
		}
		id, err := createAuthUser(ctx, pid, acct.AuthOrgID, acct.OwnerEmail, firstNonEmpty(customer.Name, acct.OwnerEmail), boolArg(provisioning, "send_password_reset"))
		if err != nil {
			return nil, err
		}
		if err := dbAccountSetAuthUser(ctx.AppDB(), pid, acct.ID, *id); err != nil {
			return nil, err
		}
		acct.AuthUserID = id
		return map[string]any{"auth_user_id": *id}, nil
	}); err != nil {
		return false, err
	}

	if err := runProvisioningStep(ctx.AppDB(), pid, acct.ID, "entitlement_grants", func() (any, error) {
		return nil, a.applyPlanGrants(ctx, pid, acct, plan)
	}); err != nil {
		return false, err
	}
	if err := runProvisioningStep(ctx.AppDB(), pid, acct.ID, "entitlement_limits", func() (any, error) {
		return nil, a.applyPlanLimits(ctx, pid, acct, plan)
	}); err != nil {
		return false, err
	}
	if err := runProvisioningStep(ctx.AppDB(), pid, acct.ID, "fulfillment", func() (any, error) {
		if boolArg(provisioning, "skip_fulfillment") {
			return map[string]any{"skipped": true}, nil
		}
		runs, err := a.runFulfillment(ctx, pid, acct, "account_active", transition.ID)
		return map[string]any{"runs": runs}, err
	}); err != nil {
		return false, err
	}
	if err := runProvisioningStep(ctx.AppDB(), pid, acct.ID, "activation", func() (any, error) {
		if err := dbLifecycleTransitionComplete(ctx.AppDB(), pid, transition, StatusActive); err != nil {
			return nil, err
		}
		return map[string]any{"status": StatusActive}, nil
	}); err != nil {
		return false, err
	}
	transition, err = dbLifecycleTransitionGet(ctx.AppDB(), pid, transition.ID)
	if err != nil {
		return false, err
	}
	if transition.Status != "completed" {
		if err := dbLifecycleTransitionComplete(ctx.AppDB(), pid, transition, StatusActive); err != nil {
			return false, err
		}
	}
	current, err := dbAccountGet(ctx.AppDB(), pid, acct.ID)
	if err != nil {
		return false, err
	}
	if current.Status != StatusActive {
		if _, err := dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusActive, ""); err != nil {
			return false, err
		}
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "provisioning.active", "provisioning", nil)
	_, _ = a.toolUsageSync(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID})
	return true, nil
}

func runProvisioningStep(db *sql.DB, pid, accountID, step string, run func() (any, error)) error {
	state, err := dbProvisioningStepGet(db, pid, accountID, step)
	if err != nil {
		return err
	}
	if state != nil && state.Status == "succeeded" {
		return nil
	}
	if err := dbProvisioningStepStart(db, pid, accountID, step); err != nil {
		return err
	}
	out, runErr := run()
	if runErr != nil {
		if errors.Is(runErr, errFulfillmentInProgress) {
			return runErr
		}
		_ = dbProvisioningStepFinish(db, pid, accountID, step, "failed", out, runErr.Error())
		return runErr
	}
	return dbProvisioningStepFinish(db, pid, accountID, step, "succeeded", out, "")
}

func (a *App) createCheckout(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	for _, unsafe := range []string{
		"activate_without_payment", "record_payment", "manual_payment_method",
		"customer_id", "billing_customer_id", "auth_org_id", "auth_user_id", "subscription_id",
		"discount_id", "catalog_discount_id",
		"provider", "period_start", "period_end", "unit_amount_cents", "currency", "interval", "interval_count", "title",
		"metadata", "subscription_metadata", "trial_start", "trial_end", "trial_days", "trial_requires_payment_method",
	} {
		if _, ok := args[unsafe]; ok {
			return nil, fmt.Errorf("%s is not accepted by public checkout", unsafe)
		}
	}
	paymentMode := strings.ToLower(strings.TrimSpace(strArg(args, "payment_mode")))
	if paymentMode == "manual" || paymentMode == "none" {
		return nil, errors.New("manual activation requires saas_checkout_mark_paid")
	}
	planKey := firstNonEmpty(strArg(args, "plan_key"), "free")
	plan, err := dbPlanGet(ctx.AppDB(), pid, planKey)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found", planKey)
	}
	discountID, discountCode, err := checkoutDiscountIdentity(plan, strArg(args, "discount_code"))
	if err != nil {
		return nil, err
	}
	requiresCommerce := plan.BillingMode != "free" || plan.SubscriptionRequired
	if (discountID != 0 || discountCode != "") && !requiresCommerce {
		return nil, errors.New("discounts require a Catalog-backed subscription plan")
	}
	customer, err := resolveCustomer(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	slug := strings.ToLower(strings.TrimSpace(strArg(args, "slug")))
	idempotencyKey := firstNonEmpty(strArg(args, "idempotency_key"), "slug:"+slug)
	fingerprint := checkoutFingerprint(customer, plan, slug, discountID, discountCode)
	checkout, claimed, err := dbCheckoutClaim(ctx.AppDB(), pid, idempotencyKey, fingerprint, customer, plan, slug, firstNonEmpty(paymentMode, "plan_policy"))
	if err != nil {
		return nil, err
	}
	if !claimed {
		if checkout.Status == "processing" {
			return nil, errors.New("checkout is already in progress")
		}
		return a.checkoutResponse(ctx, checkout)
	}
	fail := func(cause error) (map[string]any, error) {
		if persistErr := dbCheckoutFail(ctx.AppDB(), pid, checkout.ID, cause.Error()); persistErr != nil {
			return nil, fmt.Errorf("%w; persist checkout failure: %v", cause, persistErr)
		}
		return nil, cause
	}
	if !requiresCommerce {
		acct, _, err := a.ensureCheckoutAccount(ctx, checkout, customer, plan, 0, false, args)
		if err != nil {
			return fail(err)
		}
		if err := dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "active", map[string]any{"account_id": acct.ID}); err != nil {
			return fail(err)
		}
		checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
		return a.checkoutResponse(ctx, checkout)
	}

	price, err := a.resolveCheckoutPrice(ctx, pid, plan, args)
	if err != nil {
		return fail(err)
	}
	if (discountID != 0 || discountCode != "") && price.PriceID == 0 {
		return fail(errors.New("discounts require a configured Catalog price"))
	}
	checkoutDiscount, err := a.ensureCheckoutDiscountReservation(ctx, checkout, customer, price, discountID, discountCode)
	if err != nil {
		return fail(err)
	}
	periodStart := time.Now().UTC().Format(time.RFC3339)
	periodEnd := periodEndFrom(periodStart, price.Interval, price.IntervalCount)
	trialDays, trialRequiresPaymentMethod := checkoutTrialConfig(plan, price)
	trialStart, trialEnd := periodStart, ""
	if trialDays > 0 {
		trialEnd = periodEndFrom(trialStart, "day", trialDays)
		if err := dbCheckoutSetTrial(ctx.AppDB(), pid, checkout.ID, trialEnd); err != nil {
			return fail(err)
		}
		checkout.TrialEndsAt = trialEnd
	}
	subStatus := StatusPastDue
	if plan.BillingMode == "free" {
		subStatus = StatusActive
	}
	if trialDays > 0 {
		subStatus = "trialing"
	}
	zeroNetCheckout := plan.BillingMode == "paid" && trialDays == 0 && checkoutDiscount != nil && checkoutDiscount.TotalCents == 0
	if zeroNetCheckout {
		subStatus = StatusActive
	}
	subID, _, err := a.ensureCheckoutSubscription(ctx, checkout, customer, plan, price, checkoutDiscount, subStatus, periodStart, periodEnd, trialStart, trialEnd, trialRequiresPaymentMethod, args)
	if err != nil {
		return fail(err)
	}
	if err := a.ensureCheckoutDiscountRedeemed(ctx, checkoutDiscount); err != nil {
		return fail(err)
	}
	if plan.BillingMode == "free" {
		acct, _, err := a.ensureCheckoutAccount(ctx, checkout, customer, plan, subID, false, args)
		if err != nil {
			return fail(err)
		}
		if err := dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "active", map[string]any{"account_id": acct.ID, "subscription_id": subID}); err != nil {
			return fail(err)
		}
		checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
		return a.checkoutResponse(ctx, checkout)
	}
	if zeroNetCheckout {
		acct, _, err := a.ensureCheckoutAccount(ctx, checkout, customer, plan, subID, false, args)
		if err != nil {
			return fail(err)
		}
		cycleID, err := a.ensureCheckoutCycle(ctx, checkout, subID, periodStart, periodEnd)
		if err != nil {
			return fail(err)
		}
		var cycleOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_update", map[string]any{
			"_project_id": pid, "id": cycleID, "payment_status": "paid",
		}, &cycleOut); err != nil {
			return fail(fmt.Errorf("mark zero-total subscription cycle paid: %w", err))
		}
		if err := dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "active", map[string]any{"account_id": acct.ID, "subscription_id": subID, "cycle_id": cycleID, "total_cents": 0}); err != nil {
			return fail(err)
		}
		checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
		return a.checkoutResponse(ctx, checkout)
	}
	if trialDays > 0 && !trialRequiresPaymentMethod {
		acct, _, err := a.ensureCheckoutAccount(ctx, checkout, customer, plan, subID, false, args)
		if err != nil {
			return fail(err)
		}
		if err := dbCheckoutSetTrial(ctx.AppDB(), pid, checkout.ID, trialEnd); err != nil {
			return fail(err)
		}
		if err := dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "trialing", map[string]any{"account_id": acct.ID, "subscription_id": subID}); err != nil {
			return fail(err)
		}
		checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
		return a.checkoutResponse(ctx, checkout)
	}

	billingCustomerID, _, err := a.ensureBillingCustomer(ctx, pid, customer, args)
	if err != nil {
		return fail(err)
	}
	if err := dbCheckoutSetBillingCustomer(ctx.AppDB(), pid, checkout.ID, billingCustomerID); err != nil {
		return fail(err)
	}
	acct, _, err := a.ensureCheckoutAccount(ctx, checkout, customer, plan, subID, true, args)
	if err != nil {
		return fail(err)
	}

	if trialDays > 0 {
		if acct.Status != StatusSuspended {
			statusOut, statusErr := a.setAccountStatus(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID, "subscription_id": subID, "actor": "checkout"}, StatusSuspended, "checkout.awaiting_payment_method")
			if statusErr != nil {
				return fail(statusErr)
			}
			acct = statusOut.(map[string]any)["account"].(*Account)
		}
		checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
		if checkout.PaymentURL == "" {
			setupArgs := copyMap(args)
			setupArgs["metadata"] = map[string]any{"source_app": "saas", "saas_checkout_id": checkout.ID, "saas_account_id": acct.ID, "subscription_id": subID}
			setupOut, err := a.createPaymentSetupSession(ctx, pid, billingCustomerID, setupArgs)
			if err != nil {
				return fail(err)
			}
			if err := dbCheckoutSetSetup(ctx.AppDB(), pid, checkout.ID, setupOut); err != nil {
				return fail(err)
			}
		}
		if err := dbCheckoutSetTrial(ctx.AppDB(), pid, checkout.ID, trialEnd); err != nil {
			return fail(err)
		}
		if err := dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "awaiting_payment_method", map[string]any{"account_id": acct.ID, "subscription_id": subID}); err != nil {
			return fail(err)
		}
		checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
		return a.checkoutResponse(ctx, checkout)
	}

	if acct.Status != StatusPastDue {
		statusOut, statusErr := a.setAccountStatus(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID, "subscription_id": subID, "actor": "checkout"}, StatusPastDue, "checkout.awaiting_payment")
		if statusErr != nil {
			return fail(statusErr)
		}
		acct = statusOut.(map[string]any)["account"].(*Account)
	}
	cycleID, err := a.ensureCheckoutCycle(ctx, checkout, subID, periodStart, periodEnd)
	if err != nil {
		return fail(err)
	}
	cycleEvent := sdk.Event{Event: "subscription.cycle_due", ProjectID: pid, Data: map[string]any{
		"subscription_id": subID, "cycle_id": cycleID, "currency": price.Currency,
		"period_start": periodStart, "period_end": periodEnd,
		"checkout_id": checkout.ID,
		"metadata":    map[string]any{"collection_method": checkoutCollectionMethod(plan, price)},
		"success_url": strArg(args, "success_url"), "cancel_url": strArg(args, "cancel_url"),
	}}
	if err := a.handleSubscriptionCycleDue(ctx, cycleEvent); err != nil {
		return fail(err)
	}
	op, err := dbCommerceOperationByKey(ctx.AppDB(), pid, fmt.Sprintf("subscription:%d:cycle:%d", subID, cycleID))
	if err != nil || op == nil {
		return fail(firstErr(err, errors.New("checkout billing operation not found")))
	}
	if err := dbCommerceOperationSetCheckout(ctx.AppDB(), pid, op.ID, checkout.ID); err != nil {
		return fail(err)
	}
	if err := dbCheckoutSetInvoice(ctx.AppDB(), pid, checkout.ID, cycleID, int64PtrValue(op.InvoiceID), mapFromAny(op.PaymentLink)); err != nil {
		return fail(err)
	}
	if err := dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "awaiting_payment", map[string]any{"account_id": acct.ID, "subscription_id": subID, "cycle_id": cycleID, "invoice_id": int64PtrValue(op.InvoiceID)}); err != nil {
		return fail(err)
	}
	checkout, _ = dbCheckoutGet(ctx.AppDB(), pid, checkout.ID)
	return a.checkoutResponse(ctx, checkout)
}

func checkoutFingerprint(customer *Customer, plan *Plan, slug string, discountID int64, discountCode string) string {
	return strings.Join([]string{
		strings.ToLower(customer.Email), slug, plan.Key, strconv.FormatInt(customer.ID, 10),
		strconv.FormatInt(discountID, 10), normalizeDiscountCode(discountCode),
	}, "|")
}

func (a *App) checkoutResponse(ctx *sdk.AppCtx, checkout *Checkout) (map[string]any, error) {
	if checkout == nil {
		return nil, errors.New("checkout not found")
	}
	out := map[string]any{
		"checkout": checkout, "status": checkout.Status,
		"requires_payment": checkout.Status == "awaiting_payment" || checkout.Status == "awaiting_payment_method",
	}
	if checkout.PaymentURL != "" {
		out["url"] = checkout.PaymentURL
		if checkout.Status == "awaiting_payment_method" {
			out["setup_session"] = map[string]any{"url": checkout.PaymentURL, "provider_session_id": checkout.ProviderSessionID, "expires_at": checkout.SessionExpiresAt}
		} else {
			out["payment_link"] = map[string]any{"url": checkout.PaymentURL, "stripe_session_id": checkout.ProviderSessionID, "expires_at": checkout.SessionExpiresAt}
		}
	}
	if subscriptionID := int64PtrValue(checkout.SubscriptionID); subscriptionID != 0 {
		out["subscription"] = map[string]any{"id": subscriptionID, "status": checkout.Status}
	}
	if checkout.TrialEndsAt != "" {
		out["trial"] = map[string]any{"trial_ends_at": checkout.TrialEndsAt, "payment_required_at": checkout.TrialEndsAt}
	}
	if discount, err := dbCheckoutDiscountGet(ctx.AppDB(), checkout.ProjectID, checkout.ID); err != nil {
		return nil, err
	} else if discount != nil {
		out["discount"] = discount
		out["pricing"] = map[string]any{
			"currency": discount.Currency, "subtotal_cents": discount.SubtotalCents,
			"discount_cents": discount.DiscountCents, "total_cents": discount.TotalCents,
		}
	}
	if customer, err := dbCustomerGet(ctx.AppDB(), checkout.ProjectID, checkout.CustomerID); err != nil {
		return nil, err
	} else if customer != nil {
		out["customer"] = customer
	}
	if checkout.AccountID != "" {
		account, err := dbAccountGet(ctx.AppDB(), checkout.ProjectID, checkout.AccountID)
		if err != nil {
			return nil, err
		}
		out["account"] = account
	}
	if plan, err := dbPlanGet(ctx.AppDB(), checkout.ProjectID, checkout.PlanKey); err != nil {
		return nil, err
	} else if plan != nil {
		out["plan"] = plan
	}
	return out, nil
}

func (a *App) ensureCheckoutSubscription(ctx *sdk.AppCtx, checkout *Checkout, customer *Customer, plan *Plan, price checkoutPrice, discount *CheckoutDiscount, status, periodStart, periodEnd, trialStart, trialEnd string, trialRequiresPaymentMethod bool, args map[string]any) (int64, map[string]any, error) {
	if id := int64PtrValue(checkout.SubscriptionID); id != 0 {
		return id, map[string]any{"id": id, "status": status}, nil
	}
	var searchOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_search", map[string]any{
		"_project_id": checkout.ProjectID, "q": checkout.ID, "kind": "saas", "limit": 10,
	}, &searchOut); err == nil {
		for _, raw := range sliceFromAny(searchOut["subscriptions"]) {
			sub := mapFromAny(raw)
			if strArg(sub, "external_id") == checkout.ID || strArg(sub, "source_ref") == checkout.ID {
				id := int64Arg(sub, "id")
				if id != 0 {
					if err := dbCheckoutSetSubscription(ctx.AppDB(), checkout.ProjectID, checkout.ID, id); err != nil {
						return 0, nil, err
					}
					checkout.SubscriptionID = &id
					return id, sub, nil
				}
			}
		}
	}
	subArgs := copyMap(args)
	subArgs["checkout_id"] = checkout.ID
	subArgs["trial_requires_payment_method"] = trialRequiresPaymentMethod
	if trialStart != "" {
		subArgs["trial_start"] = trialStart
	}
	if trialEnd != "" {
		subArgs["trial_end"] = trialEnd
		subArgs["subscription_metadata"] = mergeMetadata(args["subscription_metadata"], map[string]any{
			"trial_started_at": trialStart, "trial_ends_at": trialEnd, "payment_required_at": trialEnd,
			"trial_requires_payment_method": trialRequiresPaymentMethod,
			"payment_method_missing":        true,
			"catalog_product_id":            price.ProductID, "catalog_price_id": price.PriceID,
		})
	}
	subOut, err := a.createSubscription(ctx, checkout.ProjectID, customer, customer.ID, plan, price, discount, status, periodStart, periodEnd, subArgs)
	if err != nil {
		return 0, nil, err
	}
	sub := unwrapMap(subOut, "subscription")
	id := int64FromResult(subOut, "subscription", "id")
	if id == 0 {
		return 0, nil, errors.New("subscriptions_create returned no subscription id")
	}
	if err := dbCheckoutSetSubscription(ctx.AppDB(), checkout.ProjectID, checkout.ID, id); err != nil {
		return 0, nil, err
	}
	checkout.SubscriptionID = &id
	return id, sub, nil
}

func (a *App) ensureCheckoutAccount(ctx *sdk.AppCtx, checkout *Checkout, customer *Customer, plan *Plan, subscriptionID int64, skipFulfillment bool, args map[string]any) (*Account, bool, error) {
	if checkout.AccountID != "" {
		account, err := dbAccountGet(ctx.AppDB(), checkout.ProjectID, checkout.AccountID)
		return account, false, err
	}
	accountArgs := copyMap(args)
	accountArgs["customer_id"] = customer.ID
	accountArgs["subscription_id"] = subscriptionID
	accountArgs["skip_fulfillment"] = skipFulfillment
	accountMeta := map[string]any{"checkout_id": checkout.ID, "subscription_id": subscriptionID, "checkout_status": checkout.Status}
	if checkout.TrialEndsAt != "" {
		accountMeta["trial_ends_at"] = checkout.TrialEndsAt
		accountMeta["payment_required_at"] = checkout.TrialEndsAt
		accountMeta["payment_method_missing"] = true
	}
	accountArgs["metadata"] = mergeMetadata(args["metadata"], accountMeta)
	account, activated, err := a.createAccount(ctx, accountArgs)
	if err != nil {
		return nil, false, err
	}
	if err := dbCheckoutSetAccount(ctx.AppDB(), checkout.ProjectID, checkout.ID, account.ID); err != nil {
		return nil, false, err
	}
	checkout.AccountID = account.ID
	return account, activated, nil
}

func (a *App) ensureCheckoutCycle(ctx *sdk.AppCtx, checkout *Checkout, subscriptionID int64, periodStart, periodEnd string) (int64, error) {
	if id := int64PtrValue(checkout.CycleID); id != 0 {
		return id, nil
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_create", map[string]any{
		"_project_id": checkout.ProjectID, "subscription_id": subscriptionID,
		"period_start": periodStart, "period_end": periodEnd, "due_at": periodStart,
		"payment_status": "pending", "fulfillment_status": "none",
		"metadata": map[string]any{"source_app": "saas", "saas_checkout_id": checkout.ID},
	}, &out); err != nil {
		return 0, fmt.Errorf("create subscription cycle: %w", err)
	}
	id := int64FromResult(out, "cycle", "id")
	if id == 0 {
		return 0, errors.New("subscription_cycles_create returned no cycle id")
	}
	if err := dbCheckoutSetCycle(ctx.AppDB(), checkout.ProjectID, checkout.ID, id); err != nil {
		return 0, err
	}
	checkout.CycleID = &id
	return id, nil
}

type checkoutPrice struct {
	ProductID       int64
	PriceID         int64
	Title           string
	UnitAmountCents int64
	Currency        string
	Interval        string
	IntervalCount   int64
	TrialDays       int64
	Metadata        map[string]any
}

func (p checkoutPrice) asMap() map[string]any {
	out := map[string]any{
		"catalog_product_id": p.ProductID,
		"catalog_price_id":   p.PriceID,
		"title":              p.Title,
		"unit_amount_cents":  p.UnitAmountCents,
		"currency":           p.Currency,
		"interval":           p.Interval,
		"interval_count":     p.IntervalCount,
	}
	if p.TrialDays > 0 {
		out["trial_days"] = p.TrialDays
	}
	if len(p.Metadata) > 0 {
		out["metadata"] = p.Metadata
	}
	return out
}

func (a *App) ensureBillingCustomer(ctx *sdk.AppCtx, pid string, customer *Customer, args map[string]any) (int64, map[string]any, error) {
	if id := firstNonZero(int64Arg(args, "billing_customer_id"), int64PtrValue(customer.BillingCustomerID)); id != 0 {
		if err := dbCustomerSetBillingID(ctx.AppDB(), pid, customer.ID, id); err != nil {
			return 0, nil, err
		}
		return id, map[string]any{"id": id}, nil
	}
	input := map[string]any{
		"_project_id": pid,
		"email":       customer.Email,
		"defaults": map[string]any{
			"name":     customer.Name,
			"metadata": map[string]any{"source": "saas", "saas_customer_id": customer.ID},
		},
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "customers_upsert_by_email", input, &out); err != nil {
		return 0, nil, fmt.Errorf("create billing customer: %w", err)
	}
	billingCustomer := unwrapMap(out, "customer")
	id := firstNonZero(int64Arg(billingCustomer, "id"), int64Arg(out, "id"))
	if id == 0 {
		return 0, nil, errors.New("billing customer response missing id")
	}
	if err := dbCustomerSetBillingID(ctx.AppDB(), pid, customer.ID, id); err != nil {
		return 0, nil, err
	}
	return id, billingCustomer, nil
}

func (a *App) resolveCheckoutPrice(ctx *sdk.AppCtx, pid string, plan *Plan, args map[string]any) (checkoutPrice, error) {
	price := checkoutPrice{
		ProductID:     int64PtrValue(plan.CatalogProductID),
		PriceID:       int64PtrValue(plan.CatalogPriceID),
		Title:         plan.Name,
		Currency:      "USD",
		Interval:      "month",
		IntervalCount: 1,
		Metadata:      mapFromAny(plan.Metadata),
	}
	if price.Metadata == nil {
		price.Metadata = map[string]any{}
	}
	if price.PriceID != 0 {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("catalog", "catalog_prices_get", map[string]any{"_project_id": pid, "id": price.PriceID}, &out); err != nil {
			return price, fmt.Errorf("get catalog price: %w", err)
		}
		pm := unwrapMap(out, "price")
		price.ProductID = firstNonZero(int64Arg(pm, "product_id"), price.ProductID)
		price.UnitAmountCents = firstNonZero(int64Arg(pm, "unit_amount_cents"), price.UnitAmountCents)
		price.Currency = strings.ToUpper(firstNonEmpty(strArg(pm, "currency"), price.Currency))
		price.Interval = firstNonEmpty(strArg(pm, "interval"), price.Interval)
		price.IntervalCount = firstNonZero(int64Arg(pm, "interval_count"), price.IntervalCount)
		price.TrialDays = firstNonZero(int64Arg(pm, "trial_days"), price.TrialDays)
		for k, v := range mapFromAny(pm["metadata"]) {
			price.Metadata[k] = v
		}
	}
	price.TrialDays = firstNonZero(price.TrialDays, int64Arg(price.Metadata, "trial_days"))
	if plan.BillingMode == "paid" && price.UnitAmountCents == 0 {
		return price, errors.New("paid SaaS checkout requires a configured Catalog price")
	}
	if price.ProductID == 0 && price.PriceID != 0 {
		return price, errors.New("catalog price response missing product_id")
	}
	return price, nil
}

func checkoutTrialConfig(plan *Plan, price checkoutPrice) (int64, bool) {
	planMeta := mapFromAny(plan.Metadata)
	trialDays := firstNonZero(price.TrialDays, int64Arg(price.Metadata, "trial_days"), int64Arg(planMeta, "trial_days"))
	requiresPaymentMethod := true
	requiresPaymentMethod = boolArgDefault(planMeta, "trial_requires_payment_method", requiresPaymentMethod)
	requiresPaymentMethod = boolArgDefault(price.Metadata, "trial_requires_payment_method", requiresPaymentMethod)
	return trialDays, requiresPaymentMethod
}

func checkoutCollectionMethod(plan *Plan, price checkoutPrice) string {
	method := strings.ToLower(firstNonEmpty(
		strArg(price.Metadata, "collection_method"),
		strArg(mapFromAny(plan.Metadata), "collection_method"),
		"charge_automatically",
	))
	if method != "charge_automatically" {
		return "send_invoice"
	}
	return method
}

func (a *App) createSubscription(ctx *sdk.AppCtx, pid string, customer *Customer, subscriptionCustomerID int64, plan *Plan, price checkoutPrice, discount *CheckoutDiscount, status, periodStart, periodEnd string, args map[string]any) (map[string]any, error) {
	checkoutID := strArg(args, "checkout_id")
	input := map[string]any{
		"_project_id":          pid,
		"customer_id":          subscriptionCustomerID,
		"customer_email":       customer.Email,
		"customer_name":        customer.Name,
		"kind":                 "saas",
		"status":               status,
		"billing_provider":     "local",
		"currency":             price.Currency,
		"interval":             price.Interval,
		"interval_count":       price.IntervalCount,
		"current_period_start": periodStart,
		"current_period_end":   periodEnd,
		"next_renewal_at":      periodEnd,
		"source":               "saas",
		"source_ref":           firstNonEmpty(checkoutID, plan.Key),
		"metadata": map[string]any{
			"saas_plan_key":     plan.Key,
			"saas_customer_id":  customer.ID,
			"collection_method": checkoutCollectionMethod(plan, price),
		},
		"items": []any{map[string]any{
			"catalog_product_id": price.ProductID,
			"catalog_price_id":   price.PriceID,
			"title":              price.Title,
			"quantity":           1,
			"unit_amount_cents":  price.UnitAmountCents,
			"currency":           price.Currency,
			"metadata":           map[string]any{"saas_plan_key": plan.Key},
		}},
	}
	if checkoutID != "" {
		input["external_id"] = checkoutID
		input["metadata"] = mergeMetadata(input["metadata"], map[string]any{"saas_checkout_id": checkoutID})
	}
	if status == "trialing" {
		input["trial_end_behavior"] = "collect"
	}
	if v := strArg(args, "trial_start"); v != "" {
		input["trial_start"] = v
	}
	if v := strArg(args, "trial_end"); v != "" {
		input["trial_end"] = v
	}
	if extra := mapFromAny(args["subscription_metadata"]); len(extra) > 0 {
		input["metadata"] = mergeMetadata(input["metadata"], extra)
	}
	if discount != nil {
		input["discounts"] = []any{map[string]any{
			"source_app": "catalog", "source_ref": discount.ReservationID, "catalog_price_id": price.PriceID,
			"application": mapFromAny(discount.Application),
			"metadata":    map[string]any{"source_app": "saas", "saas_checkout_id": checkoutID},
		}}
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_create", input, &out); err != nil {
		return nil, fmt.Errorf("create subscription: %w", err)
	}
	return out, nil
}

func (a *App) createPaymentLink(ctx *sdk.AppCtx, pid string, invoiceID int64, args map[string]any) (map[string]any, error) {
	input := map[string]any{"_project_id": pid, "invoice_id": invoiceID}
	if v := strArg(args, "success_url"); v != "" {
		input["success_url"] = v
	}
	if v := strArg(args, "cancel_url"); v != "" {
		input["cancel_url"] = v
	}
	if boolArg(args, "save_payment_method") {
		input["save_payment_method"] = true
	}
	if boolArg(args, "set_default_payment_method") {
		input["set_default_payment_method"] = true
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_send_payment_link", input, &out); err != nil {
		return nil, fmt.Errorf("create payment link: %w", err)
	}
	return out, nil
}

func (a *App) collectInvoice(ctx *sdk.AppCtx, pid string, invoiceID int64, idempotencyKey string) (map[string]any, error) {
	input := map[string]any{
		"_project_id":     pid,
		"invoice_id":      invoiceID,
		"idempotency_key": idempotencyKey,
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_collect", input, &out); err != nil {
		return nil, fmt.Errorf("collect invoice: %w", err)
	}
	return out, nil
}

func (a *App) createPaymentSetupSession(ctx *sdk.AppCtx, pid string, billingCustomerID int64, args map[string]any) (map[string]any, error) {
	input := map[string]any{"_project_id": pid, "customer_id": billingCustomerID, "set_default": true}
	if metadata := mapFromAny(args["metadata"]); len(metadata) > 0 {
		input["metadata"] = metadata
	}
	if v := strArg(args, "success_url"); v != "" {
		input["success_url"] = v
	}
	if v := strArg(args, "cancel_url"); v != "" {
		input["cancel_url"] = v
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "payment_method_setup_create", input, &out); err != nil {
		return nil, fmt.Errorf("create payment setup session: %w", err)
	}
	return out, nil
}

func (a *App) runFulfillment(ctx *sdk.AppCtx, pid string, acct *Account, event string, transitionIDs ...string) ([]*FulfillmentRun, error) {
	return a.runFulfillmentForPlan(ctx, pid, acct, acct.PlanKey, event, transitionIDs...)
}

func (a *App) runFulfillmentForPlan(ctx *sdk.AppCtx, pid string, acct *Account, planKey, event string, transitionIDs ...string) ([]*FulfillmentRun, error) {
	event = normalizeFulfillmentEvent(event)
	if event == "" {
		return nil, errors.New("fulfillment event required")
	}
	transitionID := ""
	if len(transitionIDs) > 0 {
		transitionID = strings.TrimSpace(transitionIDs[0])
	}
	if transitionID == "" {
		transition, err := dbLatestLifecycleTransition(ctx.AppDB(), pid, acct.ID, event)
		if err != nil {
			return nil, err
		}
		if transition == nil {
			transition, err = dbLifecycleTransitionReserve(ctx.AppDB(), pid, acct.ID, event, acct.Status, acct.Status, "manual:"+event)
			if err != nil {
				return nil, err
			}
		}
		transitionID = transition.ID
	}
	actions, err := dbPlanActions(ctx.AppDB(), pid, planKey, event, true)
	if err != nil || len(actions) == 0 {
		return nil, err
	}
	customer, err := dbCustomerGet(ctx.AppDB(), pid, acct.CustomerID)
	if err != nil {
		return nil, err
	}
	plan, err := dbPlanGet(ctx.AppDB(), pid, planKey)
	if err != nil {
		return nil, err
	}
	var runs []*FulfillmentRun
	for _, action := range actions {
		if !action.Enabled {
			continue
		}
		templateAccount := *acct
		templateAccount.PlanKey = planKey
		args, _ := expandFulfillmentValue(mapFromAny(action.Args), fulfillmentTemplateContext(pid, &templateAccount, customer, plan)).(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		args["_project_id"] = pid
		runTransitionID := transitionID
		if action.ExecutionPolicy == "always" {
			runTransitionID = transitionID + ":" + newID("inv")
		}
		args["idempotency_key"] = fulfillmentIdempotencyKey(pid, acct.ID, action.ID, runTransitionID)
		run, phase, recErr := dbFulfillmentRunReserve(ctx.AppDB(), pid, acct.ID, &action, runTransitionID, args, fulfillmentClaimTimeout)
		if recErr != nil {
			return runs, recErr
		}
		runs = append(runs, run)
		if phase == "skip" {
			continue
		}
		if phase == "in_progress" {
			return runs, errFulfillmentInProgress
		}
		var out map[string]any
		if phase == "store" {
			out = mapFromAny(run.Output)
		} else {
			callErr := ctx.PlatformAPI().CallAppResult(action.AppName, action.ToolName, args, &out)
			if callErr != nil {
				errText := redactFulfillmentError(callErr.Error(), args, out, action.SensitiveInputPaths, action.SensitiveOutputPaths)
				run, _ = dbFulfillmentRunFinish(ctx.AppDB(), pid, run.ID, &action, "failed", out, errText)
				runs[len(runs)-1] = run
				_ = recordEvent(ctx.AppDB(), pid, acct.ID, "fulfillment.failed", action.AppName, map[string]any{"event": event, "tool": action.ToolName, "error": errText})
				switch action.FailureMode {
				case "ignore":
					continue
				case "mark_degraded":
					_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, acct.Status, errText)
					continue
				default:
					return runs, fmt.Errorf("fulfillment %s.%s failed: %s", action.AppName, action.ToolName, errText)
				}
			}
			run, recErr = dbFulfillmentRunFinish(ctx.AppDB(), pid, run.ID, &action, "call_succeeded", out, "")
			if recErr != nil {
				return runs, recErr
			}
			runs[len(runs)-1] = run
		}
		if err := a.applyFulfillmentStore(ctx, pid, acct, action, out); err != nil {
			run, _ = dbFulfillmentRunFinish(ctx.AppDB(), pid, run.ID, &action, "call_succeeded", out,
				redactFulfillmentError(err.Error(), args, out, action.SensitiveInputPaths, action.SensitiveOutputPaths))
			runs[len(runs)-1] = run
			return runs, err
		}
		run, recErr = dbFulfillmentRunFinish(ctx.AppDB(), pid, run.ID, &action, "succeeded", out, "")
		if recErr != nil {
			return runs, recErr
		}
		runs[len(runs)-1] = run
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "fulfillment.succeeded", action.AppName, map[string]any{"event": event, "tool": action.ToolName, "run_id": run.ID})
		acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	}
	return runs, nil
}

func fulfillmentIdempotencyKey(pid, accountID string, actionID int64, transitionID string) string {
	return fmt.Sprintf("saas:%s:%s:%d:%s", pid, accountID, actionID, transitionID)
}

func (a *App) applyFulfillmentStore(ctx *sdk.AppCtx, pid string, acct *Account, action PlanAction, out map[string]any) error {
	store := mapFromAny(action.Store)
	if len(store) == 0 {
		return nil
	}
	meta := mapFromAny(acct.Metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	changed := false
	for target, rawSource := range store {
		sourcePath := strFromAny(rawSource)
		if sourcePath == "" {
			continue
		}
		value, err := fulfillmentStoreValue(out, sourcePath, action.SensitiveOutputPaths)
		if err != nil {
			return err
		}
		target = normalizeStoreTarget(target)
		if target == "" {
			continue
		}
		setPathValue(meta, strings.Split(target, "."), value)
		changed = true
	}
	if !changed {
		return nil
	}
	return dbAccountSetMetadata(ctx.AppDB(), pid, acct.ID, meta)
}

func fulfillmentTemplateContext(pid string, acct *Account, customer *Customer, plan *Plan) map[string]any {
	accountMeta := mapFromAny(acct.Metadata)
	customerMeta := map[string]any{}
	if customer != nil {
		customerMeta = mapFromAny(customer.Metadata)
	}
	planMeta := map[string]any{}
	if plan != nil {
		planMeta = mapFromAny(plan.Metadata)
	}
	ctx := map[string]any{
		"project": map[string]any{"id": pid},
		"account": map[string]any{
			"id":              acct.ID,
			"slug":            acct.Slug,
			"owner_email":     acct.OwnerEmail,
			"customer_id":     acct.CustomerID,
			"auth_org_id":     int64PtrValue(acct.AuthOrgID),
			"auth_user_id":    int64PtrValue(acct.AuthUserID),
			"subscription_id": int64PtrValue(acct.SubscriptionID),
			"plan_key":        acct.PlanKey,
			"status":          acct.Status,
			"metadata":        accountMeta,
		},
		"customer": map[string]any{
			"id":                  acct.CustomerID,
			"email":               "",
			"name":                "",
			"billing_customer_id": int64(0),
			"auth_user_id":        int64(0),
			"metadata":            customerMeta,
		},
		"plan": map[string]any{
			"key":      acct.PlanKey,
			"name":     acct.PlanKey,
			"metadata": planMeta,
		},
	}
	if customer != nil {
		c := ctx["customer"].(map[string]any)
		c["id"] = customer.ID
		c["email"] = customer.Email
		c["name"] = customer.Name
		c["billing_customer_id"] = int64PtrValue(customer.BillingCustomerID)
		c["auth_user_id"] = int64PtrValue(customer.AuthUserID)
	}
	if plan != nil {
		p := ctx["plan"].(map[string]any)
		p["key"] = plan.Key
		p["name"] = plan.Name
		p["billing_mode"] = plan.BillingMode
	}
	return ctx
}

func expandFulfillmentValue(v any, ctx map[string]any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			out[k] = expandFulfillmentValue(v, ctx)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = expandFulfillmentValue(v, ctx)
		}
		return out
	case string:
		return expandTemplateString(t, ctx)
	default:
		return v
	}
}

func expandTemplateString(s string, ctx map[string]any) any {
	exact := regexp.MustCompile(`^\{\{\s*([^{}]+?)\s*\}\}$`)
	if m := exact.FindStringSubmatch(s); len(m) == 2 {
		if v, ok := valueAtPath(ctx, strings.TrimSpace(m[1])); ok {
			return v
		}
		return ""
	}
	re := regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	return re.ReplaceAllStringFunc(s, func(token string) string {
		m := re.FindStringSubmatch(token)
		if len(m) != 2 {
			return token
		}
		if v, ok := valueAtPath(ctx, strings.TrimSpace(m[1])); ok {
			return fmt.Sprint(v)
		}
		return ""
	})
}

func normalizeFulfillmentEvent(event string) string {
	event = strings.ToLower(strings.TrimSpace(event))
	event = strings.ReplaceAll(event, ".", "_")
	event = strings.ReplaceAll(event, "-", "_")
	switch event {
	case "active", "subscription_active", "subscription_trialing":
		return "account_active"
	case "past_due", "subscription_past_due":
		return "account_past_due"
	case "suspend", "suspended":
		return "account_suspended"
	case "resume", "resumed":
		return "account_resumed"
	case "cancel", "cancelled", "subscription_cancelled", "subscription_ended", "subscription_paused":
		return "account_cancelled"
	default:
		return event
	}
}

func fulfillmentEventFromLifecycle(event, status string) string {
	event = strings.ToLower(strings.TrimSpace(event))
	switch event {
	case "subscription.active", "subscription.trialing":
		return "account_active"
	case "subscription.resumed":
		return "account_resumed"
	case "subscription.past_due":
		return "account_past_due"
	case "subscription.cancelled", "subscription.ended", "cancelled":
		return "account_cancelled"
	case "subscription.paused":
		return "account_suspended"
	case "suspended":
		return "account_suspended"
	case "resumed":
		return "account_resumed"
	}
	switch status {
	case StatusPastDue:
		return "account_past_due"
	case StatusSuspended:
		return "account_suspended"
	case StatusCancelled:
		return "account_cancelled"
	}
	return ""
}

func normalizeStoreTarget(target string) string {
	target = strings.TrimSpace(target)
	target = strings.TrimPrefix(target, "account.")
	target = strings.TrimPrefix(target, "metadata.")
	target = strings.Trim(target, ".")
	if target == "" {
		return ""
	}
	return target
}

func setPathValue(m map[string]any, parts []string, value any) {
	if len(parts) == 0 {
		return
	}
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return
	}
	if len(parts) == 1 {
		m[key] = value
		return
	}
	child := mapFromAny(m[key])
	if child == nil {
		child = map[string]any{}
	}
	m[key] = child
	setPathValue(child, parts[1:], value)
}

func (a *App) applyPlanAccess(ctx *sdk.AppCtx, pid string, acct *Account, plan *Plan) error {
	if err := a.applyPlanGrants(ctx, pid, acct, plan); err != nil {
		return err
	}
	return a.applyPlanLimits(ctx, pid, acct, plan)
}

func (a *App) applyPlanGrants(ctx *sdk.AppCtx, pid string, acct *Account, plan *Plan) error {
	for _, f := range plan.Features {
		var out map[string]any
		input := map[string]any{
			"_project_id":  pid,
			"subject_type": "saas_account",
			"subject_id":   acct.ID,
			"feature_key":  f.FeatureKey,
			"source_type":  "saas",
			"source_id":    acct.ID,
			"metadata":     map[string]any{"plan_key": plan.Key, "grant_type": f.GrantType},
		}
		if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_grants_upsert", input, &out); err != nil {
			return fmt.Errorf("upsert grant %s: %w", f.FeatureKey, err)
		}
	}
	return nil
}

func (a *App) applyPlanLimits(ctx *sdk.AppCtx, pid string, acct *Account, plan *Plan) error {
	for _, l := range plan.Limits {
		var out map[string]any
		input := map[string]any{
			"_project_id":    pid,
			"subject_type":   "saas_account",
			"subject_id":     acct.ID,
			"feature_key":    l.FeatureKey,
			"limit_type":     "quota",
			"limit_value":    l.LimitValue,
			"reset_interval": l.ResetInterval,
			"metadata":       map[string]any{"plan_key": plan.Key},
		}
		if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_limits_set", input, &out); err != nil {
			return fmt.Errorf("limit %s: %w", l.FeatureKey, err)
		}
	}
	return nil
}

func (a *App) setAccountStatus(ctx *sdk.AppCtx, args map[string]any, status, event string) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	acct, err := dbAccountGet(ctx.AppDB(), pid, strArg(args, "account_id"))
	if err != nil || acct == nil {
		return nil, firstErr(err, errors.New("account not found"))
	}
	if acct.Status == status {
		return map[string]any{"account": acct, "changed": false}, nil
	}
	if acct.Status == StatusCancelled && status == StatusActive && !boolArg(args, "allow_cancelled_reactivation") {
		return nil, errors.New("cancelled account requires explicit reactivation policy")
	}
	sourceKey := event + ":" + status
	if subID := firstNonZero(int64Arg(args, "subscription_id"), int64PtrValue(acct.SubscriptionID)); subID != 0 && strings.HasPrefix(event, "subscription.") {
		sourceKey = fmt.Sprintf("subscription:%d:%s", subID, status)
	}
	fe := fulfillmentEventFromLifecycle(event, status)
	transition, err := dbLifecycleTransitionReserve(ctx.AppDB(), pid, acct.ID, fe, acct.Status, status, sourceKey)
	if err != nil {
		return nil, err
	}
	if transition.Status != "completed" && fe != "" {
		if _, err := a.runFulfillment(ctx, pid, acct, fe, transition.ID); err != nil {
			if errors.Is(err, errFulfillmentInProgress) {
				return map[string]any{"account": acct, "changed": false, "in_progress": true, "transition_id": transition.ID}, nil
			}
			_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusFailed, err.Error())
			return nil, err
		}
	}
	if transition.Status != "completed" {
		if err := dbLifecycleTransitionComplete(ctx.AppDB(), pid, transition, status); err != nil {
			return nil, err
		}
	}
	acct, err = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, event, actor(args), map[string]any{"transition_id": transition.ID})
	return map[string]any{"account": acct, "changed": true, "transition_id": transition.ID}, nil
}

func (a *App) handleSubscriptionLifecycle(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "subscriptions" {
		return nil
	}
	subID := int64FromAny(event.Data["subscription_id"])
	if subID <= 0 {
		subID = int64FromAny(event.Data["id"])
	}
	if subID <= 0 {
		return nil
	}
	status := strings.TrimPrefix(event.Name(), "subscription.")
	if s := strFromAny(event.Data["status"]); s != "" {
		status = s
	}
	if status == "past_due" {
		metadata := mapFromAny(event.Data["metadata"])
		graceDays := int64Arg(metadata, "unpaid_grace_days")
		pastDueSince := strArg(metadata, "past_due_since")
		if graceDays > 0 && pastDueSince != "" {
			if since, parseErr := time.Parse(time.RFC3339, pastDueSince); parseErr == nil {
				graceUntil := since.AddDate(0, 0, int(graceDays))
				if time.Now().UTC().Before(graceUntil) {
					accounts, listErr := dbAccountList(ctx.AppDB(), firstNonEmpty(event.ProjectID, projectID(ctx, nil)), map[string]any{"subscription_id": subID})
					if listErr != nil {
						return listErr
					}
					for _, account := range accounts {
						meta := mapFromAny(account.Metadata)
						meta["past_due_since"] = pastDueSince
						meta["unpaid_grace_until"] = graceUntil.Format(time.RFC3339)
						if err := dbAccountSetMetadata(ctx.AppDB(), account.ProjectID, account.ID, meta); err != nil {
							return err
						}
					}
					return nil
				}
			}
		}
	}
	body := map[string]any{
		"_project_id":         firstNonEmpty(event.ProjectID, projectID(ctx, nil)),
		"subscription_id":     subID,
		"subscription_status": status,
		"actor":               "subscription",
	}
	_, err := a.toolSubscriptionSync(ctx, body)
	return err
}

func (a *App) handleSubscriptionCycleDue(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "subscriptions" {
		return nil
	}
	pid := firstNonEmpty(event.ProjectID, projectID(ctx, nil))
	subID := int64FromAny(event.Data["subscription_id"])
	cycleID := int64FromAny(event.Data["cycle_id"])
	periodStart := strFromAny(event.Data["period_start"])
	periodEnd := strFromAny(event.Data["period_end"])
	if pid == "" || subID <= 0 || cycleID <= 0 || periodStart == "" || periodEnd == "" {
		return nil
	}

	accounts, err := dbAccountList(ctx.AppDB(), pid, map[string]any{"subscription_id": subID})
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no SaaS account found for subscription %d", subID)
	}
	if len(accounts) > 1 {
		return fmt.Errorf("subscription %d is linked to multiple SaaS accounts", subID)
	}
	acct := accounts[0]
	collectionMethod := strings.ToLower(strArg(mapFromAny(event.Data["metadata"]), "collection_method"))
	if collectionMethod == "" {
		plan, planErr := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
		if planErr != nil {
			return planErr
		}
		if plan != nil {
			collectionMethod = strings.ToLower(strArg(mapFromAny(plan.Metadata), "collection_method"))
		}
	}
	if collectionMethod != "charge_automatically" {
		collectionMethod = "send_invoice"
	}
	op, claimed, err := dbCommerceCycleClaim(ctx.AppDB(), pid, acct.ID, subID, cycleID)
	if err != nil || !claimed {
		return err
	}
	checkoutID := strFromAny(event.Data["checkout_id"])
	initialCheckout := checkoutID != ""
	if checkoutID == "" {
		if trialCheckout, lookupErr := dbCheckoutTrialBySubscription(ctx.AppDB(), pid, subID); lookupErr != nil {
			return lookupErr
		} else if trialCheckout != nil {
			checkoutID = trialCheckout.ID
		}
	}
	if checkoutID != "" {
		if err := dbCommerceOperationSetCheckout(ctx.AppDB(), pid, op.ID, checkoutID); err != nil {
			return err
		}
		op.CheckoutID = checkoutID
	}
	fail := func(cause error) error {
		if persistErr := dbCommerceOperationFail(ctx.AppDB(), pid, op.ID, "failed_billing", cause.Error()); persistErr != nil {
			return fmt.Errorf("%w; persist commerce failure: %v", cause, persistErr)
		}
		return cause
	}

	prepared := mapFromAny(op.Prepared)
	if len(prepared) == 0 {
		input := map[string]any{
			"_project_id":        pid,
			"subscription_id":    subID,
			"cycle_id":           cycleID,
			"period_start":       periodStart,
			"period_end":         periodEnd,
			"include_flat":       true,
			"include_metered":    true,
			"invoice_zero_usage": true,
		}
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_invoice_prepare", input, &prepared); err != nil {
			return fail(fmt.Errorf("prepare subscription invoice: %w", err))
		}
		if len(sliceFromAny(prepared["line_items"])) == 0 {
			return fail(errors.New("subscriptions_invoice_prepare returned no line items"))
		}
		if err := dbCommerceOperationSetPrepared(ctx.AppDB(), pid, op.ID, prepared); err != nil {
			return fail(err)
		}
	}
	if total, hasTotal := prepared["total_cents"]; hasTotal && int64FromAny(total) == 0 {
		var cycleOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_update", map[string]any{
			"_project_id": pid, "id": cycleID, "payment_status": "paid",
		}, &cycleOut); err != nil {
			return fail(fmt.Errorf("mark zero-total subscription cycle paid: %w", err))
		}
		statusInput := map[string]any{
			"_project_id": pid, "id": subID, "status": "active", "actor": "saas", "note": "Zero-total subscription cycle",
			"current_period_start": periodStart, "current_period_end": periodEnd, "next_renewal_at": periodEnd,
		}
		var statusOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_status", statusInput, &statusOut); err != nil {
			return fail(fmt.Errorf("activate zero-total subscription cycle: %w", err))
		}
		if _, err := a.toolSubscriptionSync(ctx, map[string]any{
			"_project_id": pid, "account_id": acct.ID, "subscription_id": subID, "subscription_status": "active", "actor": "saas",
		}); err != nil {
			return fail(err)
		}
		if err := dbCommerceOperationCompletePayment(ctx.AppDB(), pid, op.ID); err != nil {
			return fail(err)
		}
		if checkoutID != "" {
			if err := dbCheckoutComplete(ctx.AppDB(), pid, checkoutID, "active", map[string]any{"subscription_id": subID, "cycle_id": cycleID, "total_cents": 0}); err != nil {
				return fail(err)
			}
		}
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "subscription.cycle_paid", "saas", map[string]any{"subscription_id": subID, "cycle_id": cycleID, "total_cents": 0})
		return nil
	}

	customer, err := dbCustomerGet(ctx.AppDB(), pid, acct.CustomerID)
	if err != nil || customer == nil {
		return fail(firstErr(err, errors.New("SaaS customer not found")))
	}
	billingCustomerID, _, err := a.ensureBillingCustomer(ctx, pid, customer, map[string]any{})
	if err != nil {
		return fail(err)
	}
	if err := dbCommerceOperationSetCustomer(ctx.AppDB(), pid, op.ID, billingCustomerID); err != nil {
		return fail(err)
	}

	op, err = dbCommerceOperationGet(ctx.AppDB(), pid, op.ID)
	if err != nil {
		return fail(err)
	}
	invoiceID := int64PtrValue(op.InvoiceID)
	if invoiceID == 0 {
		existingInvoiceID, reconcileErr := findBillingInvoiceByOperation(ctx, pid, billingCustomerID, op.OperationKey)
		if reconcileErr != nil {
			return fail(reconcileErr)
		}
		if existingInvoiceID != 0 {
			invoiceID = existingInvoiceID
			if err := dbCommerceOperationSetInvoice(ctx.AppDB(), pid, op.ID, invoiceID); err != nil {
				return fail(err)
			}
		}
	}
	if invoiceID == 0 {
		invoiceInput := map[string]any{
			"_project_id": pid,
			"customer_id": billingCustomerID,
			"currency":    firstNonEmpty(strArg(prepared, "currency"), strFromAny(event.Data["currency"]), "USD"),
			"provider":    "local",
			"line_items":  sliceFromAny(prepared["line_items"]),
			"finalize":    true,
			"metadata": map[string]any{
				"source_app": "saas", "saas_account_id": acct.ID,
				"subscription_id": subID, "cycle_id": cycleID,
				"operation_key": op.OperationKey,
			},
		}
		var invoiceOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_create_from_prepared_lines", invoiceInput, &invoiceOut); err != nil {
			return fail(fmt.Errorf("create billing invoice: %w", err))
		}
		invoiceID = int64FromResult(invoiceOut, "invoice", "id")
		if invoiceID == 0 {
			return fail(errors.New("Billing invoice response missing id"))
		}
		if err := dbCommerceOperationSetInvoice(ctx.AppDB(), pid, op.ID, invoiceID); err != nil {
			return fail(err)
		}
	}

	if op.Stage != "cycle_linked" && op.Stage != "payment_link_created" {
		var cycleOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_update", map[string]any{
			"_project_id": pid, "id": cycleID, "invoice_id": invoiceID, "payment_status": "pending",
		}, &cycleOut); err != nil {
			return fail(fmt.Errorf("link invoice to subscription cycle: %w", err))
		}
		if err := dbCommerceOperationSetStage(ctx.AppDB(), pid, op.ID, "cycle_linked"); err != nil {
			return fail(err)
		}
	}

	op, err = dbCommerceOperationGet(ctx.AppDB(), pid, op.ID)
	if err != nil {
		return fail(err)
	}
	if len(mapFromAny(op.PaymentLink)) == 0 {
		var billingOut map[string]any
		if collectionMethod == "charge_automatically" && !initialCheckout {
			billingOut, err = a.collectInvoice(
				ctx, pid, invoiceID,
				fmt.Sprintf("subscription:%d:cycle:%d:collect", subID, cycleID),
			)
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "no reusable payment method") {
				billingOut, err = a.createPaymentLink(ctx, pid, invoiceID, map[string]any{
					"success_url":                strFromAny(event.Data["success_url"]),
					"cancel_url":                 strFromAny(event.Data["cancel_url"]),
					"save_payment_method":        true,
					"set_default_payment_method": true,
				})
			}
		} else {
			linkArgs := map[string]any{
				"success_url": strFromAny(event.Data["success_url"]),
				"cancel_url":  strFromAny(event.Data["cancel_url"]),
			}
			if collectionMethod == "charge_automatically" {
				linkArgs["save_payment_method"] = true
				linkArgs["set_default_payment_method"] = true
			}
			billingOut, err = a.createPaymentLink(ctx, pid, invoiceID, linkArgs)
		}
		if err != nil {
			return fail(err)
		}
		if err := dbCommerceOperationCompleteBilling(ctx.AppDB(), pid, op.ID, billingOut); err != nil {
			return fail(err)
		}
	} else if err := dbCommerceOperationCompleteBilling(ctx.AppDB(), pid, op.ID, mapFromAny(op.PaymentLink)); err != nil {
		return fail(err)
	}
	if checkoutID != "" {
		op, err = dbCommerceOperationGet(ctx.AppDB(), pid, op.ID)
		if err != nil {
			return fail(err)
		}
		if err := dbCheckoutSetInvoice(ctx.AppDB(), pid, checkoutID, cycleID, invoiceID, mapFromAny(op.PaymentLink)); err != nil {
			return fail(err)
		}
		if err := dbCheckoutComplete(ctx.AppDB(), pid, checkoutID, "awaiting_payment", map[string]any{"subscription_id": subID, "cycle_id": cycleID, "invoice_id": invoiceID}); err != nil {
			return fail(err)
		}
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "subscription.cycle_billed", "subscription", map[string]any{
		"subscription_id": subID, "cycle_id": cycleID, "invoice_id": invoiceID,
	})
	return nil
}

func findBillingInvoiceByOperation(ctx *sdk.AppCtx, pid string, billingCustomerID int64, operationKey string) (int64, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_search", map[string]any{
		"_project_id": pid, "customer_id": billingCustomerID, "limit": 200,
	}, &out); err != nil {
		return 0, fmt.Errorf("reconcile billing invoice: %w", err)
	}
	for _, raw := range sliceFromAny(out["invoices"]) {
		invoice := mapFromAny(raw)
		metadata := mapFromAny(invoice["metadata"])
		if strArg(metadata, "operation_key") == operationKey {
			return int64Arg(invoice, "id"), nil
		}
	}
	return 0, nil
}

func (a *App) syncBillingInvoiceProjection(ctx *sdk.AppCtx, pid string, op *CommerceOperation) (*billingInvoiceProjection, error) {
	projection, err := a.fetchBillingInvoiceProjection(ctx, pid, op)
	if err != nil {
		return nil, err
	}
	if err := persistBillingInvoiceProjection(ctx.AppDB(), pid, op, projection); err != nil {
		return nil, err
	}
	return projection, nil
}

func (a *App) fetchBillingInvoiceProjection(ctx *sdk.AppCtx, pid string, op *CommerceOperation) (*billingInvoiceProjection, error) {
	if op == nil || int64PtrValue(op.InvoiceID) == 0 {
		return nil, errors.New("linked Billing invoice required")
	}
	invoiceID := int64PtrValue(op.InvoiceID)
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{"_project_id": pid, "id": invoiceID}, &out); err != nil {
		return nil, fmt.Errorf("read Billing invoice %d: %w", invoiceID, err)
	}
	invoice := unwrapMap(out, "invoice")
	if len(invoice) == 0 || int64Arg(invoice, "id") != invoiceID {
		return nil, fmt.Errorf("Billing invoice %d not found", invoiceID)
	}
	projection := &billingInvoiceProjection{
		InvoiceID: invoiceID, AccountID: op.AccountID,
		BillingCustomerID: int64Arg(invoice, "customer_id"),
		SubscriptionID:    op.SubscriptionID, CycleID: op.CycleID,
		Status: strArg(invoice, "status"), Currency: strings.ToUpper(strArg(invoice, "currency")),
		TotalCents: int64Arg(invoice, "total_cents"), AmountPaidCents: int64Arg(invoice, "amount_paid_cents"),
		PaidAt: strArg(invoice, "paid_at"), SourceCreatedAt: strArg(invoice, "created_at"), SourceUpdatedAt: strArg(invoice, "updated_at"),
	}
	if projection.BillingCustomerID == 0 {
		projection.BillingCustomerID = int64PtrValue(op.BillingCustomerID)
	}
	if projection.BillingCustomerID == 0 {
		return nil, fmt.Errorf("Billing invoice %d has no customer", invoiceID)
	}
	if expected := int64PtrValue(op.BillingCustomerID); expected != 0 && projection.BillingCustomerID != expected {
		return nil, fmt.Errorf("Billing invoice %d customer does not match SaaS operation", invoiceID)
	}
	for _, raw := range sliceFromAny(invoice["payments"]) {
		payment := mapFromAny(raw)
		paymentID := int64Arg(payment, "id")
		receivedAt := strArg(payment, "received_at")
		if paymentID <= 0 || receivedAt == "" {
			return nil, fmt.Errorf("Billing invoice %d returned an invalid payment", invoiceID)
		}
		projection.Payments = append(projection.Payments, billingPaymentProjection{
			PaymentID: paymentID, AmountCents: int64Arg(payment, "amount_cents"),
			Currency: strings.ToUpper(firstNonEmpty(strArg(payment, "currency"), projection.Currency)),
			Method:   strArg(payment, "method"), ReceivedAt: receivedAt, SourceCreatedAt: strArg(payment, "created_at"),
		})
	}
	return projection, nil
}

func persistBillingInvoiceProjection(db *sql.DB, pid string, op *CommerceOperation, projection *billingInvoiceProjection) error {
	if err := dbBillingProjectionReplace(db, pid, projection); err != nil {
		return err
	}
	if err := dbCustomerSetBillingIDByAccount(db, pid, op.AccountID, projection.BillingCustomerID); err != nil {
		return err
	}
	return nil
}

func (a *App) syncBillingOperations(ctx *sdk.AppCtx, pid, accountID string, missingOnly bool, limit, offset int) (map[string]any, error) {
	operations, err := dbCommerceOperationsForBillingProjection(ctx.AppDB(), pid, accountID, missingOnly, limit, offset)
	if err != nil {
		return nil, err
	}
	synced, failed := 0, 0
	errorsOut := []string{}
	for _, operation := range operations {
		if _, err := a.syncBillingInvoiceProjection(ctx, pid, operation); err != nil {
			failed++
			errorsOut = append(errorsOut, fmt.Sprintf("invoice %d: %v", int64PtrValue(operation.InvoiceID), err))
			_ = dbCommerceOperationProjectionAttempt(ctx.AppDB(), pid, operation.ID, err.Error())
			continue
		}
		_ = dbCommerceOperationProjectionAttempt(ctx.AppDB(), pid, operation.ID, "")
		synced++
	}
	pending, err := dbBillingProjectionPendingCount(ctx.AppDB(), pid, accountID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"synced": synced, "failed": failed, "pending": pending, "errors": errorsOut,
		"limit": limit, "offset": offset, "has_more": len(operations) == limit,
	}, nil
}

func (a *App) handleInvoicePaid(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	pid := firstNonEmpty(event.ProjectID, projectID(ctx, nil))
	invoiceID := int64FromAny(event.Data["id"])
	if invoiceID <= 0 {
		invoiceID = int64FromAny(event.Data["invoice_id"])
	}
	if pid == "" || invoiceID <= 0 {
		return nil
	}
	if handled, err := a.handlePlanChangeInvoicePaid(ctx, pid, invoiceID); err != nil || handled {
		return err
	}
	op, err := dbCommerceOperationByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || op == nil {
		return err
	}
	invoice, err := a.fetchBillingInvoiceProjection(ctx, pid, op)
	if err != nil {
		return err
	}
	if invoice.Status != "paid" {
		if err := persistBillingInvoiceProjection(ctx.AppDB(), pid, op, invoice); err != nil {
			return err
		}
		_ = recordEvent(ctx.AppDB(), pid, op.AccountID, "invoice.payment_received", "billing", map[string]any{
			"subscription_id": op.SubscriptionID, "cycle_id": op.CycleID, "invoice_id": invoiceID,
			"invoice_status": invoice.Status, "amount_paid_cents": invoice.AmountPaidCents,
		})
		return nil
	}
	op, claimed, err := dbCommercePaymentClaim(ctx.AppDB(), pid, invoiceID)
	if err != nil || op == nil {
		return err
	}
	if !claimed {
		if op.Status == "paid" || op.Status == "processing_payment" {
			return nil
		}
		return fmt.Errorf("invoice %d payment operation is already in progress", invoiceID)
	}
	fail := func(cause error) error {
		if persistErr := dbCommerceOperationFail(ctx.AppDB(), pid, op.ID, "failed_payment", cause.Error()); persistErr != nil {
			return fmt.Errorf("%w; persist payment failure: %v", cause, persistErr)
		}
		return cause
	}
	if err := persistBillingInvoiceProjection(ctx.AppDB(), pid, op, invoice); err != nil {
		return fail(fmt.Errorf("project paid Billing invoice: %w", err))
	}

	var cycleOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_update", map[string]any{
		"_project_id": pid, "id": op.CycleID, "invoice_id": invoiceID, "payment_status": "paid",
	}, &cycleOut); err != nil {
		return fail(fmt.Errorf("mark subscription cycle paid: %w", err))
	}
	prepared := mapFromAny(op.Prepared)
	statusInput := map[string]any{
		"_project_id": pid, "id": op.SubscriptionID, "status": "active",
		"actor": "saas", "note": fmt.Sprintf("Billing invoice %d paid", invoiceID),
	}
	if v := strArg(prepared, "period_start"); v != "" {
		statusInput["current_period_start"] = v
	}
	if v := strArg(prepared, "period_end"); v != "" {
		statusInput["current_period_end"] = v
		statusInput["next_renewal_at"] = v
	}
	var statusOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_status", statusInput, &statusOut); err != nil {
		return fail(fmt.Errorf("activate paid subscription: %w", err))
	}
	if _, err := a.toolSubscriptionSync(ctx, map[string]any{
		"_project_id": pid, "account_id": op.AccountID, "subscription_id": op.SubscriptionID,
		"subscription_status": "active", "actor": "billing",
	}); err != nil {
		return fail(err)
	}
	if err := dbCommerceOperationCompletePayment(ctx.AppDB(), pid, op.ID); err != nil {
		return fail(err)
	}
	if op.CheckoutID != "" {
		if err := dbCheckoutComplete(ctx.AppDB(), pid, op.CheckoutID, "active", map[string]any{"invoice_id": invoiceID, "subscription_id": op.SubscriptionID, "cycle_id": op.CycleID}); err != nil {
			return fail(err)
		}
	}
	_ = recordEvent(ctx.AppDB(), pid, op.AccountID, "invoice.paid", "billing", map[string]any{
		"subscription_id": op.SubscriptionID, "cycle_id": op.CycleID, "invoice_id": invoiceID,
	})
	return nil
}

func waitForCommercePayment(db *sql.DB, pid string, invoiceID int64, timeout time.Duration) (*CommerceOperation, error) {
	deadline := time.Now().Add(timeout)
	for {
		op, err := dbCommerceOperationByInvoice(db, pid, invoiceID)
		if err != nil {
			return nil, err
		}
		if op == nil {
			return nil, fmt.Errorf("invoice %d payment operation not found", invoiceID)
		}
		switch op.Status {
		case "paid":
			return op, nil
		case "failed_payment":
			return op, fmt.Errorf("invoice %d payment activation failed: %s", invoiceID, firstNonEmpty(op.LastError, "unknown error"))
		}
		if !time.Now().Before(deadline) {
			return op, nil
		}
		time.Sleep(paymentCompletionPoll)
	}
}

func (a *App) handlePaymentMethodAttached(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	pid := firstNonEmpty(event.ProjectID, projectID(ctx, nil))
	metadata := mapFromAny(event.Data["metadata"])
	checkoutID := strArg(metadata, "saas_checkout_id")
	if pid == "" || checkoutID == "" {
		return nil
	}
	checkout, err := dbCheckoutGet(ctx.AppDB(), pid, checkoutID)
	if err != nil || checkout == nil {
		return err
	}
	if checkout.Status == "trialing" || checkout.Status == "active" {
		return nil
	}
	if checkout.Status != "awaiting_payment_method" && checkout.Status != "setup_expired" {
		return fmt.Errorf("checkout %s is not awaiting a payment method", checkout.ID)
	}
	if expected := int64PtrValue(checkout.BillingCustomerID); expected != 0 && int64FromAny(event.Data["customer_id"]) != expected {
		return errors.New("payment method customer does not match checkout")
	}
	paymentMethodID := int64FromAny(event.Data["id"])
	if err := dbCheckoutSetPaymentMethod(ctx.AppDB(), pid, checkout.ID, paymentMethodID); err != nil {
		return err
	}
	subID := int64PtrValue(checkout.SubscriptionID)
	if subID == 0 || checkout.AccountID == "" {
		return errors.New("checkout is missing subscription or account")
	}
	var statusOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_status", map[string]any{
		"_project_id": pid, "id": subID, "status": "trialing", "actor": "saas", "note": "Required payment method attached",
	}, &statusOut); err != nil {
		return fmt.Errorf("activate card-required trial: %w", err)
	}
	if _, err := a.toolSubscriptionSync(ctx, map[string]any{
		"_project_id": pid, "account_id": checkout.AccountID, "subscription_id": subID,
		"subscription_status": "trialing", "actor": "billing",
	}); err != nil {
		return err
	}
	account, _ := dbAccountGet(ctx.AppDB(), pid, checkout.AccountID)
	if account != nil {
		meta := mapFromAny(account.Metadata)
		meta["payment_method_missing"] = false
		meta["payment_method_id"] = paymentMethodID
		_ = dbAccountSetMetadata(ctx.AppDB(), pid, account.ID, meta)
	}
	return dbCheckoutComplete(ctx.AppDB(), pid, checkout.ID, "trialing", map[string]any{"payment_method_id": paymentMethodID})
}

func (a *App) handleInvoiceCollectionFailed(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "billing" {
		return nil
	}
	pid := firstNonEmpty(event.ProjectID, projectID(ctx, nil))
	invoiceID := int64FromAny(event.Data["id"])
	if pid == "" || invoiceID == 0 {
		return nil
	}
	op, err := dbCommerceOperationByInvoice(ctx.AppDB(), pid, invoiceID)
	if err != nil || op == nil {
		return err
	}
	if _, err := a.syncBillingInvoiceProjection(ctx, pid, op); err != nil {
		return err
	}
	eventName := event.Name()
	behavior := "past_due"
	if eventName == "invoice.refunded" {
		behavior = strings.ToLower(firstNonEmpty(ctx.Config().Get("refund_behavior"), "past_due"))
	}
	if behavior == "ignore" {
		return nil
	}
	cycleStatus := "failed"
	if eventName == "invoice.voided" || eventName == "invoice.void" {
		cycleStatus = "voided"
	}
	if eventName == "invoice.refunded" {
		cycleStatus = "refunded"
	}
	var cycleOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscription_cycles_update", map[string]any{
		"_project_id": pid, "id": op.CycleID, "invoice_id": invoiceID, "payment_status": cycleStatus,
	}, &cycleOut); err != nil {
		return err
	}
	target := "past_due"
	if behavior == "cancel" {
		target = "cancelled"
	}
	var statusOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_status", map[string]any{
		"_project_id": pid, "id": op.SubscriptionID, "status": target, "actor": "saas", "note": eventName,
	}, &statusOut); err != nil {
		return err
	}
	if _, err := a.toolSubscriptionSync(ctx, map[string]any{
		"_project_id": pid, "account_id": op.AccountID, "subscription_id": op.SubscriptionID,
		"subscription_status": target, "actor": "billing",
	}); err != nil {
		return err
	}
	if op.CheckoutID != "" {
		_ = dbCheckoutSetStatus(ctx.AppDB(), pid, op.CheckoutID, "payment_failed", eventName)
	}
	return dbCommerceOperationFail(ctx.AppDB(), pid, op.ID, "failed_payment", eventName)
}

func (a *App) recoverExpiredCheckouts(ctx *sdk.AppCtx) error {
	pid := projectID(ctx, nil)
	if pid == "" {
		return nil
	}
	checkouts, err := dbCheckoutExpiredSetups(ctx.AppDB(), pid, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, checkout := range checkouts {
		_ = dbCheckoutSetStatus(ctx.AppDB(), pid, checkout.ID, "setup_expired", "payment method setup session expired")
		if subID := int64PtrValue(checkout.SubscriptionID); subID != 0 {
			var out map[string]any
			_ = ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_update_status", map[string]any{
				"_project_id": pid, "id": subID, "status": "paused", "actor": "saas", "note": "payment method setup expired",
			}, &out)
		}
	}
	if err := a.reconcileCheckoutDiscounts(ctx, pid); err != nil {
		return err
	}
	if err := a.reconcilePendingInvoices(ctx, pid); err != nil {
		return err
	}
	result, err := a.syncBillingOperations(ctx, pid, "", true, 20, 0)
	if err == nil && int64FromAny(result["failed"]) > 0 {
		ctx.Logger().Warn("backfill SaaS Billing projections", "failed", result["failed"], "errors", result["errors"])
	}
	return err
}

func (a *App) reconcilePendingInvoices(ctx *sdk.AppCtx, pid string) error {
	operations, err := dbCommerceOperationsForReconciliation(ctx.AppDB(), pid, time.Now().UTC().Add(-5*time.Minute), 20)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		invoiceID := int64PtrValue(operation.InvoiceID)
		if invoiceID == 0 {
			continue
		}
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_get", map[string]any{"_project_id": pid, "id": invoiceID}, &out); err != nil {
			ctx.Logger().Warn("reconcile SaaS invoice", "invoice_id", invoiceID, "err", err)
			continue
		}
		invoice := unwrapMap(out, "invoice")
		status := strArg(invoice, "status")
		switch status {
		case "paid":
			if err := a.handleInvoicePaid(ctx, sdk.Event{Event: "invoice.paid", ProjectID: pid, Data: map[string]any{"id": invoiceID, "status": status}}); err != nil {
				return err
			}
		case "void", "uncollectible":
			if err := a.handleInvoiceCollectionFailed(ctx, sdk.Event{Event: "invoice." + status, ProjectID: pid, Data: map[string]any{"id": invoiceID, "status": status}}); err != nil {
				return err
			}
		default:
			_ = dbCommerceOperationTouch(ctx.AppDB(), pid, operation.ID)
		}
	}
	return nil
}

func createAuthOrg(ctx *sdk.AppCtx, pid, slug, name string) (*int64, error) {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("auth", "auth_orgs_create", map[string]any{"_project_id": pid, "slug": slug, "name": name}, &out); err != nil {
		return nil, fmt.Errorf("create auth org: %w", err)
	}
	id := int64Arg(unwrapMap(out, "organization"), "id")
	if id == 0 {
		id = int64Arg(out, "id")
	}
	if id == 0 {
		return nil, errors.New("auth_orgs_create returned no organization id")
	}
	return &id, nil
}

func createAuthUser(ctx *sdk.AppCtx, pid string, orgID *int64, email, name string, sendReset bool) (*int64, error) {
	if orgID == nil || *orgID == 0 {
		return nil, errors.New("auth_org_id required to create owner user")
	}
	var out map[string]any
	input := map[string]any{
		"_project_id":         pid,
		"organization_id":     *orgID,
		"email":               email,
		"display_name":        name,
		"send_password_reset": sendReset,
		"email_verified":      true,
	}
	if err := ctx.PlatformAPI().CallAppResult("auth", "auth_users_create", input, &out); err != nil {
		return nil, fmt.Errorf("create auth user: %w", err)
	}
	id := firstNonZero(int64Arg(unwrapMap(out, "user"), "id"), int64Arg(unwrapMap(out, "user"), "user_id"), int64Arg(out, "id"))
	if id == 0 {
		return nil, errors.New("auth_users_create returned no user id")
	}
	return &id, nil
}

func seedPlans(db *sql.DB, pid string) error {
	if _, err := db.Exec(`
		INSERT INTO saas_plans (project_id, key, name, billing_mode, metadata_json)
		VALUES (?, 'free', 'Free', 'free', '{"seeded":true}')
		ON CONFLICT(project_id, key) DO NOTHING`, pid); err != nil {
		return err
	}
	defaultFeatures := []string{"saas:access"}
	for _, feature := range defaultFeatures {
		if _, err := db.Exec(`
			INSERT INTO saas_plan_features (project_id, plan_key, feature_key, grant_type, metadata_json)
			VALUES (?, 'free', ?, 'access', '{"seeded":true}')
			ON CONFLICT(project_id, plan_key, feature_key) DO NOTHING`, pid, feature); err != nil {
			return err
		}
	}
	return nil
}

func dbPlanUpsert(db *sql.DB, pid string, args map[string]any) (*Plan, error) {
	key, err := normalizeKey(strArg(args, "key"))
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	billingMode := firstNonEmpty(strArg(args, "billing_mode"), "free")
	if billingMode != "free" && billingMode != "paid" {
		return nil, errors.New("billing_mode must be free or paid")
	}
	priceID := int64Arg(args, "catalog_price_id")
	discountID := int64Arg(args, "catalog_discount_id")
	subscriptionRequired := boolArg(args, "subscription_required")
	if discountID != 0 && priceID == 0 {
		return nil, errors.New("catalog_discount_id requires catalog_price_id")
	}
	if discountID != 0 && billingMode == "free" && !subscriptionRequired {
		return nil, errors.New("catalog_discount_id requires a subscription plan")
	}
	metadata := mapFromAny(args["metadata"])
	if metadata == nil {
		metadata = map[string]any{}
	}
	if billingMode == "paid" && strArg(metadata, "collection_method") == "" {
		var existingMetadata string
		err := db.QueryRow(`SELECT metadata_json FROM saas_plans WHERE project_id=? AND key=?`, pid, key).Scan(&existingMetadata)
		switch {
		case err == nil && strArg(mapFromAny(json.RawMessage(existingMetadata)), "collection_method") != "":
			metadata["collection_method"] = strArg(mapFromAny(json.RawMessage(existingMetadata)), "collection_method")
		case err == nil || errors.Is(err, sql.ErrNoRows):
			metadata["collection_method"] = "charge_automatically"
		default:
			return nil, err
		}
	}
	_, err = db.Exec(`
		INSERT INTO saas_plans
			(project_id, key, name, billing_mode, catalog_product_id, catalog_price_id, catalog_discount_id, subscription_required, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, key) DO UPDATE SET
			name=excluded.name,
			billing_mode=excluded.billing_mode,
			catalog_product_id=excluded.catalog_product_id,
			catalog_price_id=excluded.catalog_price_id,
			catalog_discount_id=excluded.catalog_discount_id,
			subscription_required=excluded.subscription_required,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, key, name, billingMode,
		nullableInt64(int64Arg(args, "catalog_product_id")), nullableInt64(priceID),
		nullableInt64(discountID), boolInt(subscriptionRequired), jsonOrEmpty(metadata, "{}"))
	if err != nil {
		return nil, err
	}
	return dbPlanGet(db, pid, key)
}

func dbPlanList(db *sql.DB, pid string) ([]*Plan, error) {
	rows, err := db.Query(planSelect()+` WHERE project_id=? ORDER BY key`, pid)
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
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := hydratePlanList(db, pid, out); err != nil {
		return nil, err
	}
	return out, nil
}

func hydratePlanList(db *sql.DB, pid string, plans []*Plan) error {
	byKey := make(map[string]*Plan, len(plans))
	for _, plan := range plans {
		byKey[plan.Key] = plan
	}
	features, err := dbPlanFeatures(db, pid, "")
	if err != nil {
		return err
	}
	for _, feature := range features {
		if plan := byKey[feature.PlanKey]; plan != nil {
			plan.Features = append(plan.Features, feature)
		}
	}
	limits, err := dbPlanLimits(db, pid, "")
	if err != nil {
		return err
	}
	for _, limit := range limits {
		if plan := byKey[limit.PlanKey]; plan != nil {
			plan.Limits = append(plan.Limits, limit)
		}
	}
	sources, err := dbUsageSources(db, pid, "")
	if err != nil {
		return err
	}
	for _, source := range sources {
		if plan := byKey[source.PlanKey]; plan != nil {
			plan.UsageSources = append(plan.UsageSources, source)
		}
	}
	actions, err := dbPlanActions(db, pid, "", "", false)
	if err != nil {
		return err
	}
	for _, action := range actions {
		if plan := byKey[action.PlanKey]; plan != nil {
			plan.Actions = append(plan.Actions, action)
		}
	}
	return nil
}

func dbPlanGet(db *sql.DB, pid, key string) (*Plan, error) {
	key, err := normalizeKey(key)
	if err != nil {
		return nil, err
	}
	p, err := scanPlan(db.QueryRow(planSelect()+` WHERE project_id=? AND key=?`, pid, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, hydratePlan(db, p)
}

func requirePlanExists(db *sql.DB, pid, key string) error {
	var exists int
	err := db.QueryRow(`SELECT 1 FROM saas_plans WHERE project_id=? AND key=?`, pid, key).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("plan %q not found", key)
	}
	return err
}

func hydratePlan(db *sql.DB, p *Plan) error {
	var err error
	p.Features, err = dbPlanFeatures(db, p.ProjectID, p.Key)
	if err != nil {
		return err
	}
	p.Limits, err = dbPlanLimits(db, p.ProjectID, p.Key)
	if err != nil {
		return err
	}
	p.UsageSources, err = dbUsageSources(db, p.ProjectID, p.Key)
	if err != nil {
		return err
	}
	p.Actions, err = dbPlanActions(db, p.ProjectID, p.Key, "", false)
	return err
}

func planSelect() string {
	return `SELECT project_id, key, name, billing_mode, catalog_product_id, catalog_price_id, catalog_discount_id, subscription_required, metadata_json, created_at, updated_at FROM saas_plans`
}

func scanPlan(row rowScanner) (*Plan, error) {
	var p Plan
	var product, price, discount sql.NullInt64
	var subReq int
	var meta string
	if err := row.Scan(&p.ProjectID, &p.Key, &p.Name, &p.BillingMode, &product, &price, &discount, &subReq, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if product.Valid {
		p.CatalogProductID = &product.Int64
	}
	if price.Valid {
		p.CatalogPriceID = &price.Int64
	}
	if discount.Valid {
		p.CatalogDiscountID = &discount.Int64
	}
	p.SubscriptionRequired = subReq != 0
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbPlanFeatureUpsert(db *sql.DB, pid string, args map[string]any) (*PlanFeature, error) {
	planKey, err := normalizeKey(strArg(args, "plan_key"))
	if err != nil {
		return nil, err
	}
	if err := requirePlanExists(db, pid, planKey); err != nil {
		return nil, err
	}
	feature := strArg(args, "feature_key")
	if feature == "" {
		return nil, errors.New("feature_key required")
	}
	_, err = db.Exec(`
		INSERT INTO saas_plan_features (project_id, plan_key, feature_key, grant_type, metadata_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, plan_key, feature_key) DO UPDATE SET
			grant_type=excluded.grant_type,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, planKey, feature, firstNonEmpty(strArg(args, "grant_type"), "access"), jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	return dbPlanFeatureGet(db, pid, planKey, feature)
}

func dbPlanFeatureGet(db *sql.DB, pid, planKey, feature string) (*PlanFeature, error) {
	var f PlanFeature
	var meta string
	err := db.QueryRow(`SELECT id, project_id, plan_key, feature_key, grant_type, metadata_json, created_at, updated_at FROM saas_plan_features WHERE project_id=? AND plan_key=? AND feature_key=?`, pid, planKey, feature).
		Scan(&f.ID, &f.ProjectID, &f.PlanKey, &f.FeatureKey, &f.GrantType, &meta, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	f.Metadata = json.RawMessage(meta)
	return &f, nil
}

func dbPlanFeatures(db *sql.DB, pid, planKey string) ([]PlanFeature, error) {
	query := `SELECT id, project_id, plan_key, feature_key, grant_type, metadata_json, created_at, updated_at FROM saas_plan_features WHERE project_id=?`
	args := []any{pid}
	if planKey != "" {
		query += ` AND plan_key=?`
		args = append(args, planKey)
	}
	query += ` ORDER BY plan_key, feature_key`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanFeature
	for rows.Next() {
		var f PlanFeature
		var meta string
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.PlanKey, &f.FeatureKey, &f.GrantType, &meta, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Metadata = json.RawMessage(meta)
		out = append(out, f)
	}
	return out, rows.Err()
}

func dbPlanLimitUpsert(db *sql.DB, pid string, args map[string]any) (*PlanLimit, error) {
	planKey, err := normalizeKey(strArg(args, "plan_key"))
	if err != nil {
		return nil, err
	}
	if err := requirePlanExists(db, pid, planKey); err != nil {
		return nil, err
	}
	feature := strArg(args, "feature_key")
	if feature == "" {
		return nil, errors.New("feature_key required")
	}
	_, err = db.Exec(`
		INSERT INTO saas_plan_limits (project_id, plan_key, feature_key, limit_value, reset_interval, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, plan_key, feature_key) DO UPDATE SET
			limit_value=excluded.limit_value,
			reset_interval=excluded.reset_interval,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, planKey, feature, int64Arg(args, "limit_value"), firstNonEmpty(strArg(args, "reset_interval"), "none"), jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	return dbPlanLimitGet(db, pid, planKey, feature)
}

func dbPlanLimitGet(db *sql.DB, pid, planKey, feature string) (*PlanLimit, error) {
	var l PlanLimit
	var meta string
	err := db.QueryRow(`SELECT id, project_id, plan_key, feature_key, limit_value, reset_interval, metadata_json, created_at, updated_at FROM saas_plan_limits WHERE project_id=? AND plan_key=? AND feature_key=?`, pid, planKey, feature).
		Scan(&l.ID, &l.ProjectID, &l.PlanKey, &l.FeatureKey, &l.LimitValue, &l.ResetInterval, &meta, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	l.Metadata = json.RawMessage(meta)
	return &l, nil
}

func dbPlanLimits(db *sql.DB, pid, planKey string) ([]PlanLimit, error) {
	query := `SELECT id, project_id, plan_key, feature_key, limit_value, reset_interval, metadata_json, created_at, updated_at FROM saas_plan_limits WHERE project_id=?`
	args := []any{pid}
	if planKey != "" {
		query += ` AND plan_key=?`
		args = append(args, planKey)
	}
	query += ` ORDER BY plan_key, feature_key`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanLimit
	for rows.Next() {
		var l PlanLimit
		var meta string
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.PlanKey, &l.FeatureKey, &l.LimitValue, &l.ResetInterval, &meta, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		l.Metadata = json.RawMessage(meta)
		out = append(out, l)
	}
	return out, rows.Err()
}

func dbUsageSourceUpsert(db *sql.DB, pid string, args map[string]any) (*UsageSource, error) {
	planKey, err := normalizeKey(strArg(args, "plan_key"))
	if err != nil {
		return nil, err
	}
	if err := requirePlanExists(db, pid, planKey); err != nil {
		return nil, err
	}
	app, tool := strArg(args, "app_name"), strArg(args, "tool_name")
	if app == "" || tool == "" {
		return nil, errors.New("app_name and tool_name required")
	}
	meta := mapFromAny(args["metadata"])
	if meta == nil {
		meta = map[string]any{}
	}
	for _, key := range []string{"feature_key", "read_path", "quantity_path"} {
		if v := strArg(args, key); v != "" {
			meta[key] = v
		}
	}
	if callArgs := mapFromAny(args["call_args"]); callArgs != nil {
		meta["call_args"] = callArgs
	}
	_, err = db.Exec(`
		INSERT INTO saas_usage_sources (project_id, plan_key, app_name, tool_name, feature_prefix, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, plan_key, app_name, tool_name) DO UPDATE SET
			feature_prefix=excluded.feature_prefix,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, planKey, app, tool, strArg(args, "feature_prefix"), jsonOrEmpty(meta, "{}"))
	if err != nil {
		return nil, err
	}
	return dbUsageSourceGet(db, pid, planKey, app, tool)
}

func dbUsageSourceGet(db *sql.DB, pid, planKey, app, tool string) (*UsageSource, error) {
	var s UsageSource
	var meta string
	err := db.QueryRow(`SELECT id, project_id, plan_key, app_name, tool_name, feature_prefix, metadata_json, created_at, updated_at FROM saas_usage_sources WHERE project_id=? AND plan_key=? AND app_name=? AND tool_name=?`, pid, planKey, app, tool).
		Scan(&s.ID, &s.ProjectID, &s.PlanKey, &s.AppName, &s.ToolName, &s.FeaturePrefix, &meta, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.Metadata = json.RawMessage(meta)
	return &s, nil
}

func dbUsageSources(db *sql.DB, pid, planKey string) ([]UsageSource, error) {
	query := `SELECT id, project_id, plan_key, app_name, tool_name, feature_prefix, metadata_json, created_at, updated_at FROM saas_usage_sources WHERE project_id=?`
	args := []any{pid}
	if planKey != "" {
		query += ` AND plan_key=?`
		args = append(args, planKey)
	}
	query += ` ORDER BY plan_key, app_name, tool_name`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSource
	for rows.Next() {
		var s UsageSource
		var meta string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.PlanKey, &s.AppName, &s.ToolName, &s.FeaturePrefix, &meta, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.Metadata = json.RawMessage(meta)
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbPlanActionInsert(db *sql.DB, pid string, args map[string]any) (*PlanAction, error) {
	planKey, err := normalizeKey(strArg(args, "plan_key"))
	if err != nil {
		return nil, err
	}
	if err := requirePlanExists(db, pid, planKey); err != nil {
		return nil, err
	}
	event := normalizeFulfillmentEvent(strArg(args, "event"))
	if event == "" {
		return nil, errors.New("event required")
	}
	app, tool := strArg(args, "app_name"), strArg(args, "tool_name")
	if app == "" || tool == "" {
		return nil, errors.New("app_name and tool_name required")
	}
	failureMode := firstNonEmpty(strArg(args, "failure_mode"), "fail_account")
	switch failureMode {
	case "fail_account", "mark_degraded", "ignore":
	default:
		return nil, fmt.Errorf("unsupported failure_mode %q", failureMode)
	}
	executionPolicy := firstNonEmpty(strArg(args, "execution_policy"), "once_per_transition")
	if executionPolicy != "once_per_transition" && executionPolicy != "always" {
		return nil, fmt.Errorf("unsupported execution_policy %q", executionPolicy)
	}
	persistInput, err := normalizePersistenceMode(strArg(args, "persist_input"))
	if err != nil {
		return nil, err
	}
	persistOutput, err := normalizePersistenceMode(strArg(args, "persist_output"))
	if err != nil {
		return nil, err
	}
	sensitiveInputPaths, err := normalizeSensitivePaths(args["sensitive_input_paths"])
	if err != nil {
		return nil, err
	}
	sensitiveOutputPaths, err := normalizeSensitivePaths(args["sensitive_output_paths"])
	if err != nil {
		return nil, err
	}
	store := mapFromAny(args["store"])
	if persistOutput == persistenceNone && len(store) > 0 {
		return nil, errors.New("persist_output none cannot be combined with store mappings")
	}
	enabled := 1
	if _, ok := args["enabled"]; ok {
		enabled = boolInt(boolArg(args, "enabled"))
	}
	res, err := db.Exec(`
		INSERT INTO saas_plan_actions
			(project_id, plan_key, event, app_name, tool_name, args_json, store_json, failure_mode, execution_policy,
			 persist_input, persist_output, sensitive_input_paths_json, sensitive_output_paths_json, enabled, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, planKey, event, app, tool, jsonOrEmpty(args["args"], "{}"), jsonOrEmpty(args["store"], "{}"), failureMode, executionPolicy,
		persistInput, persistOutput, jsonOrEmpty(sensitiveInputPaths, "[]"), jsonOrEmpty(sensitiveOutputPaths, "[]"), enabled, jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbPlanActionGet(db, pid, id)
}

func dbPlanActionUpdate(db *sql.DB, pid string, args map[string]any) (*PlanAction, error) {
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	existing, err := dbPlanActionGet(db, pid, id)
	if err != nil {
		return nil, err
	} else if existing == nil {
		return nil, sql.ErrNoRows
	}

	sets := []string{}
	vals := []any{}
	if _, ok := args["args"]; ok {
		sets = append(sets, "args_json=?")
		vals = append(vals, jsonOrEmpty(args["args"], "{}"))
	}
	if _, ok := args["store"]; ok {
		sets = append(sets, "store_json=?")
		vals = append(vals, jsonOrEmpty(args["store"], "{}"))
	}
	if _, ok := args["failure_mode"]; ok {
		failureMode := firstNonEmpty(strArg(args, "failure_mode"), "fail_account")
		switch failureMode {
		case "fail_account", "mark_degraded", "ignore":
		default:
			return nil, fmt.Errorf("unsupported failure_mode %q", failureMode)
		}
		sets = append(sets, "failure_mode=?")
		vals = append(vals, failureMode)
	}
	if _, ok := args["execution_policy"]; ok {
		executionPolicy := firstNonEmpty(strArg(args, "execution_policy"), "once_per_transition")
		if executionPolicy != "once_per_transition" && executionPolicy != "always" {
			return nil, fmt.Errorf("unsupported execution_policy %q", executionPolicy)
		}
		sets = append(sets, "execution_policy=?")
		vals = append(vals, executionPolicy)
	}
	persistOutput := existing.PersistOutput
	if _, ok := args["persist_input"]; ok {
		mode, err := normalizePersistenceMode(strArg(args, "persist_input"))
		if err != nil {
			return nil, err
		}
		sets = append(sets, "persist_input=?")
		vals = append(vals, mode)
	}
	if _, ok := args["persist_output"]; ok {
		mode, err := normalizePersistenceMode(strArg(args, "persist_output"))
		if err != nil {
			return nil, err
		}
		persistOutput = mode
		sets = append(sets, "persist_output=?")
		vals = append(vals, mode)
	}
	if _, ok := args["sensitive_input_paths"]; ok {
		paths, err := normalizeSensitivePaths(args["sensitive_input_paths"])
		if err != nil {
			return nil, err
		}
		sets = append(sets, "sensitive_input_paths_json=?")
		vals = append(vals, jsonOrEmpty(paths, "[]"))
	}
	if _, ok := args["sensitive_output_paths"]; ok {
		paths, err := normalizeSensitivePaths(args["sensitive_output_paths"])
		if err != nil {
			return nil, err
		}
		sets = append(sets, "sensitive_output_paths_json=?")
		vals = append(vals, jsonOrEmpty(paths, "[]"))
	}
	store := mapFromAny(existing.Store)
	if _, ok := args["store"]; ok {
		store = mapFromAny(args["store"])
	}
	if persistOutput == persistenceNone && len(store) > 0 {
		return nil, errors.New("persist_output none cannot be combined with store mappings")
	}
	if _, ok := args["enabled"]; ok {
		sets = append(sets, "enabled=?")
		vals = append(vals, boolInt(boolArg(args, "enabled")))
	}
	if _, ok := args["metadata"]; ok {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, jsonOrEmpty(args["metadata"], "{}"))
	}
	if len(sets) == 0 {
		return dbPlanActionGet(db, pid, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, pid, id)
	if _, err := db.Exec(`UPDATE saas_plan_actions SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, vals...); err != nil {
		return nil, err
	}
	return dbPlanActionGet(db, pid, id)
}

func dbPlanActionGet(db *sql.DB, pid string, id int64) (*PlanAction, error) {
	rows, err := db.Query(`
		SELECT id, project_id, plan_key, event, app_name, tool_name, args_json, store_json, failure_mode, execution_policy,
		       persist_input, persist_output, sensitive_input_paths_json, sensitive_output_paths_json, enabled, metadata_json, created_at, updated_at
		FROM saas_plan_actions WHERE project_id=? AND id=?`, pid, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	return scanPlanAction(rows)
}

func dbPlanActions(db *sql.DB, pid, planKey, event string, enabledOnly bool) ([]PlanAction, error) {
	where := []string{"project_id=?"}
	vals := []any{pid}
	if planKey != "" {
		where = append(where, "plan_key=?")
		vals = append(vals, planKey)
	}
	if event != "" {
		where = append(where, "event=?")
		vals = append(vals, event)
	}
	if enabledOnly {
		where = append(where, "enabled=1")
	}
	rows, err := db.Query(`
		SELECT id, project_id, plan_key, event, app_name, tool_name, args_json, store_json, failure_mode, execution_policy,
		       persist_input, persist_output, sensitive_input_paths_json, sensitive_output_paths_json, enabled, metadata_json, created_at, updated_at
		FROM saas_plan_actions WHERE `+strings.Join(where, " AND ")+` ORDER BY plan_key,id`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanAction
	for rows.Next() {
		action, err := scanPlanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *action)
	}
	return out, rows.Err()
}

func scanPlanAction(row rowScanner) (*PlanAction, error) {
	var a PlanAction
	var enabled int
	var args, store, sensitiveInputPaths, sensitiveOutputPaths, meta string
	if err := row.Scan(&a.ID, &a.ProjectID, &a.PlanKey, &a.Event, &a.AppName, &a.ToolName, &args, &store, &a.FailureMode, &a.ExecutionPolicy,
		&a.PersistInput, &a.PersistOutput, &sensitiveInputPaths, &sensitiveOutputPaths, &enabled, &meta, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.Args = json.RawMessage(args)
	a.Store = json.RawMessage(store)
	a.SensitiveInputPaths = json.RawMessage(sensitiveInputPaths)
	a.SensitiveOutputPaths = json.RawMessage(sensitiveOutputPaths)
	a.Enabled = enabled != 0
	a.Metadata = json.RawMessage(meta)
	return &a, nil
}

func dbCustomerUpsert(db *sql.DB, pid string, args map[string]any) (*Customer, error) {
	email := strings.ToLower(strings.TrimSpace(strArg(args, "email")))
	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.New("valid email required")
	}
	var id int64
	err := db.QueryRow(`
		INSERT INTO saas_customers (project_id, email, name, billing_customer_id, auth_user_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, email) DO UPDATE SET
			name=COALESCE(NULLIF(excluded.name,''), saas_customers.name),
			billing_customer_id=COALESCE(excluded.billing_customer_id, saas_customers.billing_customer_id),
			auth_user_id=COALESCE(excluded.auth_user_id, saas_customers.auth_user_id),
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP
		RETURNING id`,
		pid, email, strArg(args, "name"), nullableInt64(int64Arg(args, "billing_customer_id")), nullableInt64(int64Arg(args, "auth_user_id")), jsonOrEmpty(args["metadata"], "{}"),
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return dbCustomerGet(db, pid, id)
}

func resolveCustomer(db *sql.DB, pid string, args map[string]any) (*Customer, error) {
	if id := int64Arg(args, "customer_id"); id > 0 {
		c, err := dbCustomerGet(db, pid, id)
		if err != nil || c == nil {
			return c, firstErr(err, errors.New("customer not found"))
		}
		return c, nil
	}
	email := firstNonEmpty(strArg(args, "customer_email"), strArg(args, "owner_email"))
	body := map[string]any{"email": email, "name": strArg(args, "customer_name"), "billing_customer_id": int64Arg(args, "billing_customer_id"), "auth_user_id": int64Arg(args, "auth_user_id")}
	return dbCustomerUpsert(db, pid, body)
}

func dbCustomerGet(db *sql.DB, pid string, id int64) (*Customer, error) {
	var c Customer
	var billing, authUser sql.NullInt64
	var meta string
	err := db.QueryRow(`SELECT id, project_id, email, name, billing_customer_id, auth_user_id, metadata_json, created_at, updated_at FROM saas_customers WHERE project_id=? AND id=?`, pid, id).
		Scan(&c.ID, &c.ProjectID, &c.Email, &c.Name, &billing, &authUser, &meta, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if billing.Valid {
		c.BillingCustomerID = &billing.Int64
	}
	if authUser.Valid {
		c.AuthUserID = &authUser.Int64
	}
	c.Metadata = json.RawMessage(meta)
	return &c, nil
}

func dbCustomerSetBillingID(db *sql.DB, pid string, customerID, billingID int64) error {
	if billingID == 0 {
		return nil
	}
	res, err := db.Exec(`UPDATE saas_customers SET billing_customer_id=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, billingID, pid, customerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("customer not found")
	}
	return nil
}

func dbCustomerSetBillingIDByAccount(db *sql.DB, pid, accountID string, billingID int64) error {
	if billingID == 0 {
		return nil
	}
	res, err := db.Exec(`UPDATE saas_customers SET billing_customer_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=(SELECT customer_id FROM saas_accounts WHERE project_id=? AND id=?)`, billingID, pid, pid, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("SaaS customer not found for account")
	}
	return nil
}

func dbAccountInsert(db *sql.DB, a *Account) error {
	_, err := db.Exec(`
		INSERT INTO saas_accounts
			(id, project_id, customer_id, auth_org_id, auth_user_id, subscription_id, slug, owner_email, plan_key, status, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.CustomerID, a.AuthOrgID, a.AuthUserID, a.SubscriptionID, a.Slug, a.OwnerEmail, a.PlanKey, a.Status, stringOrJSON(a.Metadata, "{}"))
	return err
}

func dbAccountMergeProvisioningInput(db *sql.DB, pid, id string, args map[string]any) error {
	acct, err := dbAccountGet(db, pid, id)
	if err != nil || acct == nil {
		return firstErr(err, errors.New("account not found"))
	}
	requestedSubID := int64Arg(args, "subscription_id")
	if requestedSubID != 0 && acct.SubscriptionID != nil && *acct.SubscriptionID != requestedSubID {
		return errors.New("existing account belongs to a different subscription")
	}
	meta := mapFromAny(acct.Metadata)
	for k, v := range mapFromAny(args["metadata"]) {
		meta[k] = v
	}
	provisioning := mapFromAny(meta["provisioning"])
	if provisioning == nil {
		provisioning = map[string]any{}
	}
	for _, key := range []string{"create_owner_user", "send_password_reset", "skip_fulfillment"} {
		if _, ok := args[key]; ok {
			provisioning[key] = boolArg(args, key)
		}
	}
	meta["provisioning"] = provisioning
	_, err = db.Exec(`UPDATE saas_accounts SET
		subscription_id=COALESCE(subscription_id, ?), auth_org_id=COALESCE(auth_org_id, ?), auth_user_id=COALESCE(auth_user_id, ?),
		metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		nullableInt64(requestedSubID), nullableInt64(int64Arg(args, "auth_org_id")), nullableInt64(int64Arg(args, "auth_user_id")),
		jsonOrEmpty(meta, "{}"), pid, id)
	return err
}

func dbAccountSetAuthOrg(db *sql.DB, pid, id string, authOrgID int64) error {
	_, err := db.Exec(`UPDATE saas_accounts SET auth_org_id=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, authOrgID, pid, id)
	return err
}

func dbAccountSetAuthUser(db *sql.DB, pid, id string, authUserID int64) error {
	_, err := db.Exec(`UPDATE saas_accounts SET auth_user_id=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, authUserID, pid, id)
	return err
}

func dbProvisioningStepGet(db *sql.DB, pid, accountID, step string) (*ProvisioningStep, error) {
	var s ProvisioningStep
	var output string
	err := db.QueryRow(`SELECT step, status, attempt_count, output_json, last_error FROM saas_provisioning_steps
		WHERE project_id=? AND account_id=? AND step=?`, pid, accountID, step).
		Scan(&s.Step, &s.Status, &s.AttemptCount, &output, &s.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Output = json.RawMessage(output)
	return &s, nil
}

func dbProvisioningStepStart(db *sql.DB, pid, accountID, step string) error {
	_, err := db.Exec(`INSERT INTO saas_provisioning_steps
		(project_id, account_id, step, status, attempt_count, started_at, updated_at)
		VALUES (?, ?, ?, 'running', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, account_id, step) DO UPDATE SET
		status='running', attempt_count=attempt_count+1, last_error='', started_at=CURRENT_TIMESTAMP,
		completed_at=NULL, updated_at=CURRENT_TIMESTAMP`, pid, accountID, step)
	return err
}

func dbProvisioningStepFinish(db *sql.DB, pid, accountID, step, status string, output any, errText string) error {
	_, err := db.Exec(`UPDATE saas_provisioning_steps SET status=?, output_json=?, last_error=?,
		completed_at=CASE WHEN ?='succeeded' THEN CURRENT_TIMESTAMP ELSE completed_at END, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND account_id=? AND step=?`, status, jsonOrEmpty(output, "{}"), errText, status, pid, accountID, step)
	return err
}

func dbAccountSetStatus(db *sql.DB, pid, id, status, errMsg string) (*Account, error) {
	res, err := db.Exec(`UPDATE saas_accounts SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, errMsg, pid, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("account not found")
	}
	return dbAccountGet(db, pid, id)
}

func dbAccountSetMetadata(db *sql.DB, pid, id string, meta map[string]any) error {
	res, err := db.Exec(`UPDATE saas_accounts SET metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, jsonOrEmpty(meta, "{}"), pid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("account not found")
	}
	return nil
}

func dbAccountUsageSynced(db *sql.DB, pid, id string) error {
	_, err := db.Exec(`UPDATE saas_accounts SET last_usage_sync_at=CURRENT_TIMESTAMP, last_error='', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, id)
	return err
}

func dbAccountSetLastError(db *sql.DB, pid, id, errText string) error {
	_, err := db.Exec(`UPDATE saas_accounts SET last_error=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, errText, pid, id)
	return err
}

func dbAccountGet(db *sql.DB, pid, id string) (*Account, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("account_id required")
	}
	acct, err := scanAccount(db.QueryRow(accountSelect()+` WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return acct, err
}

func dbAccountBySlug(db *sql.DB, pid, slug string) (*Account, error) {
	acct, err := scanAccount(db.QueryRow(accountSelect()+` WHERE project_id=? AND slug=?`, pid, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return acct, err
}

func dbAccountList(db *sql.DB, pid string, args map[string]any) ([]*Account, error) {
	where := []string{"project_id=?"}
	vals := []any{pid}
	if id := int64Arg(args, "customer_id"); id != 0 {
		where = append(where, "customer_id=?")
		vals = append(vals, id)
	}
	if id := int64Arg(args, "subscription_id"); id != 0 {
		where = append(where, "subscription_id=?")
		vals = append(vals, id)
	}
	if status := strArg(args, "status"); status != "" {
		where = append(where, "status=?")
		vals = append(vals, status)
	}
	if plan := strArg(args, "plan_key"); plan != "" {
		where = append(where, "plan_key=?")
		vals = append(vals, plan)
	}
	rows, err := db.Query(accountSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		acct, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acct)
	}
	return out, rows.Err()
}

func dbAccountSearch(db *sql.DB, pid string, args map[string]any) ([]*Account, int, int, int, error) {
	where := []string{"a.project_id=?"}
	vals := []any{pid}
	if id := int64Arg(args, "customer_id"); id != 0 {
		where = append(where, "a.customer_id=?")
		vals = append(vals, id)
	}
	if id := int64Arg(args, "subscription_id"); id != 0 {
		where = append(where, "a.subscription_id=?")
		vals = append(vals, id)
	}
	if status := strArg(args, "status"); status != "" {
		where = append(where, "a.status=?")
		vals = append(vals, status)
	}
	if plan := strArg(args, "plan_key"); plan != "" {
		where = append(where, "a.plan_key=?")
		vals = append(vals, plan)
	}
	if _, ok := args["catalog_product_id"]; ok {
		if productID := int64Arg(args, "catalog_product_id"); productID > 0 {
			where = append(where, "EXISTS (SELECT 1 FROM saas_plans p WHERE p.project_id=a.project_id AND p.key=a.plan_key AND p.catalog_product_id=?)")
			vals = append(vals, productID)
		} else {
			where = append(where, "EXISTS (SELECT 1 FROM saas_plans p WHERE p.project_id=a.project_id AND p.key=a.plan_key AND p.catalog_product_id IS NULL)")
		}
	}
	if email := strings.ToLower(strArg(args, "customer_email")); email != "" {
		where = append(where, "c.email=?")
		vals = append(vals, email)
	}
	if value := strArg(args, "created_before"); value != "" {
		where = append(where, "datetime(a.created_at)<datetime(?)")
		vals = append(vals, value)
	}
	if value := strArg(args, "created_after"); value != "" {
		where = append(where, "datetime(a.created_at)>=datetime(?)")
		vals = append(vals, value)
	}
	paymentWhere := []string{"bp.project_id=a.project_id", "bp.account_id=a.id", "bp.amount_cents>0"}
	paymentVals := []any{}
	if value := strArg(args, "paid_since"); value != "" {
		paymentWhere = append(paymentWhere, "datetime(bp.received_at)>=datetime(?)")
		paymentVals = append(paymentVals, value)
	}
	if value := strArg(args, "paid_until"); value != "" {
		paymentWhere = append(paymentWhere, "datetime(bp.received_at)<datetime(?)")
		paymentVals = append(paymentVals, value)
	}
	hasPaymentDates := len(paymentVals) > 0
	if _, ok := args["has_paid"]; ok {
		if boolArg(args, "has_paid") {
			where = append(where, "EXISTS (SELECT 1 FROM saas_billing_payments bp WHERE "+strings.Join(paymentWhere, " AND ")+")")
			vals = append(vals, paymentVals...)
		} else {
			where = append(where, "NOT EXISTS (SELECT 1 FROM saas_billing_payments bp WHERE bp.project_id=a.project_id AND bp.account_id=a.id AND bp.amount_cents>0)")
			where = append(where, `NOT EXISTS (
				SELECT 1 FROM saas_commerce_operations co
				WHERE co.project_id=a.project_id AND co.account_id=a.id AND co.invoice_id IS NOT NULL
				  AND NOT EXISTS (SELECT 1 FROM saas_billing_invoices bi WHERE bi.project_id=co.project_id AND bi.invoice_id=co.invoice_id)
			)`)
		}
	} else if hasPaymentDates {
		where = append(where, "EXISTS (SELECT 1 FROM saas_billing_payments bp WHERE "+strings.Join(paymentWhere, " AND ")+")")
		vals = append(vals, paymentVals...)
	}
	if value := strArg(args, "last_paid_before"); value != "" {
		where = append(where, "datetime((SELECT MAX(bp.received_at) FROM saas_billing_payments bp WHERE bp.project_id=a.project_id AND bp.account_id=a.id AND bp.amount_cents>0))<datetime(?)")
		vals = append(vals, value)
	}
	if value := strArg(args, "last_paid_after"); value != "" {
		where = append(where, "datetime((SELECT MAX(bp.received_at) FROM saas_billing_payments bp WHERE bp.project_id=a.project_id AND bp.account_id=a.id AND bp.amount_cents>0))>=datetime(?)")
		vals = append(vals, value)
	}

	from := ` FROM saas_accounts a JOIN saas_customers c ON c.project_id=a.project_id AND c.id=a.customer_id WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := db.QueryRow(`SELECT COUNT(*)`+from, vals...).Scan(&total); err != nil {
		return nil, 0, 0, 0, err
	}
	limit := int(int64Arg(args, "limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := int(int64Arg(args, "offset"))
	queryVals := append(append([]any{}, vals...), limit, offset)
	rows, err := db.Query(`SELECT a.id, a.project_id, a.customer_id, a.auth_org_id, a.auth_user_id, a.subscription_id,
		a.slug, a.owner_email, a.plan_key, a.status, COALESCE(a.last_usage_sync_at,''), a.last_error,
		a.metadata_json, a.created_at, a.updated_at`+from+` ORDER BY a.created_at DESC, a.id DESC LIMIT ? OFFSET ?`, queryVals...)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer rows.Close()
	accounts := []*Account{}
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, 0, err
	}
	if err := dbHydrateAccounts(db, pid, accounts); err != nil {
		return nil, 0, 0, 0, err
	}
	return accounts, total, limit, offset, nil
}

func dbHydrateAccounts(db *sql.DB, pid string, accounts []*Account) error {
	if len(accounts) == 0 {
		return nil
	}
	byID := map[string]*Account{}
	customerIDs := map[int64]bool{}
	accountIDs := make([]any, 0, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
		customerIDs[account.CustomerID] = true
		accountIDs = append(accountIDs, account.ID)
	}
	customerVals := []any{pid}
	customerMarks := make([]string, 0, len(customerIDs))
	for id := range customerIDs {
		customerMarks = append(customerMarks, "?")
		customerVals = append(customerVals, id)
	}
	rows, err := db.Query(`SELECT id, project_id, email, name, billing_customer_id, auth_user_id, metadata_json, created_at, updated_at
		FROM saas_customers WHERE project_id=? AND id IN (`+strings.Join(customerMarks, ",")+`)`, customerVals...)
	if err != nil {
		return err
	}
	customers := map[int64]*Customer{}
	for rows.Next() {
		var customer Customer
		var billingID, authUserID sql.NullInt64
		var metadata string
		if err := rows.Scan(&customer.ID, &customer.ProjectID, &customer.Email, &customer.Name, &billingID, &authUserID, &metadata, &customer.CreatedAt, &customer.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		customer.BillingCustomerID = ptrIfValid(billingID)
		customer.AuthUserID = ptrIfValid(authUserID)
		customer.Metadata = json.RawMessage(metadata)
		customers[customer.ID] = &customer
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, account := range accounts {
		account.Customer = customers[account.CustomerID]
		summary := &BillingSummary{DataComplete: true, NetPaidByCurrency: map[string]int64{}}
		if account.Customer != nil {
			summary.BillingCustomerID = account.Customer.BillingCustomerID
		}
		account.Billing = summary
	}

	marks := strings.TrimSuffix(strings.Repeat("?,", len(accountIDs)), ",")
	vals := append([]any{pid}, accountIDs...)
	rows, err = db.Query(`SELECT bi.account_id, MAX(bi.billing_customer_id),
		COUNT(DISTINCT CASE WHEN bi.status='paid' THEN bi.invoice_id END), MAX(bi.invoice_id),
		COUNT(CASE WHEN bp.amount_cents>0 THEN 1 END),
		MIN(CASE WHEN bp.amount_cents>0 THEN bp.received_at END), MAX(CASE WHEN bp.amount_cents>0 THEN bp.received_at END),
		MAX(bi.synced_at)
		FROM saas_billing_invoices bi
		LEFT JOIN saas_billing_payments bp ON bp.project_id=bi.project_id AND bp.invoice_id=bi.invoice_id
		WHERE bi.project_id=? AND bi.account_id IN (`+marks+`)
		GROUP BY bi.account_id`, vals...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID string
		var billingCustomerID, latestInvoiceID sql.NullInt64
		var firstPaidAt, lastPaidAt, syncedAt sql.NullString
		var paidInvoiceCount, paymentCount int64
		if err := rows.Scan(&accountID, &billingCustomerID, &paidInvoiceCount, &latestInvoiceID, &paymentCount, &firstPaidAt, &lastPaidAt, &syncedAt); err != nil {
			rows.Close()
			return err
		}
		if account := byID[accountID]; account != nil {
			account.Billing.BillingCustomerID = ptrIfValid(billingCustomerID)
			account.Billing.PaidInvoiceCount = paidInvoiceCount
			account.Billing.LatestInvoiceID = ptrIfValid(latestInvoiceID)
			account.Billing.PaymentCount = paymentCount
			account.Billing.HasPaid = paymentCount > 0
			account.Billing.FirstPaidAt = firstPaidAt.String
			account.Billing.LastPaidAt = lastPaidAt.String
			account.Billing.SyncedAt = syncedAt.String
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT account_id, currency, COALESCE(SUM(amount_cents),0)
		FROM saas_billing_payments WHERE project_id=? AND account_id IN (`+marks+`)
		GROUP BY account_id, currency`, vals...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID, currency string
		var amount int64
		if err := rows.Scan(&accountID, &currency, &amount); err != nil {
			rows.Close()
			return err
		}
		if account := byID[accountID]; account != nil {
			account.Billing.NetPaidByCurrency[currency] = amount
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT co.account_id, COUNT(*) FROM saas_commerce_operations co
		WHERE co.project_id=? AND co.account_id IN (`+marks+`) AND co.invoice_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM saas_billing_invoices bi WHERE bi.project_id=co.project_id AND bi.invoice_id=co.invoice_id)
		GROUP BY co.account_id`, vals...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID string
		var missing int64
		if err := rows.Scan(&accountID, &missing); err != nil {
			rows.Close()
			return err
		}
		if account := byID[accountID]; account != nil && missing > 0 {
			account.Billing.DataComplete = false
		}
	}
	return rows.Close()
}

func dbAccountPage(db *sql.DB, pid string, statuses []string, limit, offset int) ([]*Account, error) {
	if limit <= 0 {
		limit = defaultUsagePageSize
	}
	where := []string{"project_id=?"}
	vals := []any{pid}
	if len(statuses) > 0 {
		marks := make([]string, len(statuses))
		for i, status := range statuses {
			marks[i] = "?"
			vals = append(vals, status)
		}
		where = append(where, "status IN ("+strings.Join(marks, ",")+")")
	}
	vals = append(vals, limit, offset)
	rows, err := db.Query(accountSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at,id LIMIT ? OFFSET ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		acct, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acct)
	}
	return out, rows.Err()
}

func accountSelect() string {
	return `SELECT id, project_id, customer_id, auth_org_id, auth_user_id, subscription_id, slug, owner_email, plan_key, status, COALESCE(last_usage_sync_at,''), last_error, metadata_json, created_at, updated_at FROM saas_accounts`
}

func scanAccount(row rowScanner) (*Account, error) {
	var a Account
	var orgID, userID, subID sql.NullInt64
	var meta string
	if err := row.Scan(&a.ID, &a.ProjectID, &a.CustomerID, &orgID, &userID, &subID, &a.Slug, &a.OwnerEmail, &a.PlanKey, &a.Status, &a.LastUsageSyncAt, &a.LastError, &meta, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if orgID.Valid {
		a.AuthOrgID = &orgID.Int64
	}
	if userID.Valid {
		a.AuthUserID = &userID.Int64
	}
	if subID.Valid {
		a.SubscriptionID = &subID.Int64
	}
	a.Metadata = json.RawMessage(meta)
	return &a, nil
}

func dbUsageSnapshotUpsert(db *sql.DB, pid string, acct *Account, source, feature string, qty int64, meta any) error {
	if acct == nil || acct.ID == "" {
		return errors.New("account required")
	}
	if strings.TrimSpace(feature) == "" {
		return errors.New("feature_key required")
	}
	source = firstNonEmpty(source, "manual")
	_, err := db.Exec(`
		INSERT INTO saas_usage_snapshots
			(project_id, account_id, customer_id, source_key, source_app, feature_key, quantity, generation_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'manual', ?)
		ON CONFLICT(project_id, account_id, source_key, feature_key) DO UPDATE SET
			quantity=excluded.quantity,
			customer_id=excluded.customer_id,
			metadata_json=excluded.metadata_json,
			generation_id=excluded.generation_id,
			observed_at=CURRENT_TIMESTAMP,
			updated_at=CURRENT_TIMESTAMP`,
		pid, acct.ID, acct.CustomerID, source, source, feature, qty, jsonOrEmpty(meta, "{}"))
	return err
}

func dbUsageSourceReplace(db *sql.DB, pid string, acct *Account, src UsageSource, generation string, gauges []usageGauge) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sourceKey := fmt.Sprintf("%s:%s:%d", src.AppName, src.ToolName, src.ID)
	for _, gauge := range gauges {
		feature := firstNonEmpty(gauge.FeatureKey, src.FeaturePrefix)
		if feature == "" {
			continue
		}
		if src.FeaturePrefix != "" && !strings.Contains(feature, ":") {
			feature = strings.TrimRight(src.FeaturePrefix, ":") + ":" + feature
		}
		if _, err := tx.Exec(`INSERT INTO saas_usage_snapshots
			(project_id, account_id, customer_id, usage_source_id, source_key, source_app, feature_key, quantity, generation_id, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, account_id, source_key, feature_key) DO UPDATE SET
			customer_id=excluded.customer_id, usage_source_id=excluded.usage_source_id, source_app=excluded.source_app,
			quantity=excluded.quantity, generation_id=excluded.generation_id, metadata_json=excluded.metadata_json,
			observed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP`,
			pid, acct.ID, acct.CustomerID, src.ID, sourceKey, src.AppName, feature, gauge.Quantity, generation, jsonOrEmpty(gauge.Metadata, "{}")); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM saas_usage_snapshots WHERE project_id=? AND account_id=? AND source_key=? AND generation_id<>?`, pid, acct.ID, sourceKey, generation); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO saas_usage_source_state
		(project_id, account_id, usage_source_id, last_generation_id, last_success_at, last_error, failure_count, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, '', 0, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, account_id, usage_source_id) DO UPDATE SET
		last_generation_id=excluded.last_generation_id, last_success_at=CURRENT_TIMESTAMP,
		last_error='', failure_count=0, updated_at=CURRENT_TIMESTAMP`, pid, acct.ID, src.ID, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func dbUsageSourceFailure(db *sql.DB, pid, accountID string, sourceID int64, errText string) error {
	_, err := db.Exec(`INSERT INTO saas_usage_source_state
		(project_id, account_id, usage_source_id, last_error, failure_count, updated_at)
		VALUES (?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, account_id, usage_source_id) DO UPDATE SET
		last_error=excluded.last_error, failure_count=failure_count+1, updated_at=CURRENT_TIMESTAMP`, pid, accountID, sourceID, errText)
	return err
}

func dbUsageStaleSources(db *sql.DB, pid string, acct *Account, now time.Time, defaultFreshness time.Duration) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT s.id, s.app_name, s.tool_name, s.metadata_json,
		COALESCE(st.last_success_at,''), COALESCE(st.last_error,''), COALESCE(st.failure_count,0)
		FROM saas_usage_sources s
		LEFT JOIN saas_usage_source_state st
		  ON st.project_id=s.project_id AND st.account_id=? AND st.usage_source_id=s.id
		WHERE s.project_id=? AND s.plan_key=? ORDER BY s.id`, acct.ID, pid, acct.PlanKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stale []map[string]any
	for rows.Next() {
		var id, failures int64
		var app, tool, metadata, lastSuccess, lastError string
		if err := rows.Scan(&id, &app, &tool, &metadata, &lastSuccess, &lastError, &failures); err != nil {
			return nil, err
		}
		freshness := defaultFreshness
		meta := mapFromAny(metadata)
		if seconds := int64Arg(meta, "freshness_seconds"); seconds > 0 {
			freshness = time.Duration(seconds) * time.Second
		}
		successAt, ok := parseTimestamp(lastSuccess)
		if ok && freshness > 0 && now.Sub(successAt) <= freshness {
			continue
		}
		stale = append(stale, map[string]any{
			"usage_source_id": id, "app_name": app, "tool_name": tool,
			"last_success_at": lastSuccess, "last_error": lastError, "failure_count": failures,
		})
	}
	return stale, rows.Err()
}

func dbUsageTotals(db *sql.DB, pid string, args map[string]any) ([]UsageTotal, error) {
	where := []string{"u.project_id=?"}
	vals := []any{pid}
	if id := strArg(args, "account_id"); id != "" {
		where = append(where, "u.account_id=?")
		vals = append(vals, id)
	}
	if id := int64Arg(args, "customer_id"); id != 0 {
		where = append(where, "u.customer_id=?")
		vals = append(vals, id)
	}
	if feature := strArg(args, "feature_key"); feature != "" {
		where = append(where, "u.feature_key=?")
		vals = append(vals, feature)
	}
	rows, err := db.Query(`
		SELECT u.project_id, u.account_id, u.customer_id, u.feature_key,
		       COALESCE(SUM(u.quantity),0) AS quantity,
		       l.limit_value,
		       MAX(u.observed_at) AS observed_at,
		       COUNT(*) AS source_count
		FROM saas_usage_snapshots u
		JOIN saas_accounts a ON a.project_id=u.project_id AND a.id=u.account_id
		LEFT JOIN saas_plan_limits l ON l.project_id=a.project_id AND l.plan_key=a.plan_key AND l.feature_key=u.feature_key
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY u.project_id, u.account_id, u.customer_id, u.feature_key, l.limit_value
		ORDER BY u.feature_key`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageTotal
	for rows.Next() {
		var u UsageTotal
		var limit sql.NullInt64
		if err := rows.Scan(&u.ProjectID, &u.AccountID, &u.CustomerID, &u.FeatureKey, &u.Quantity, &limit, &u.ObservedAt, &u.SourceCount); err != nil {
			return nil, err
		}
		if limit.Valid {
			u.LimitValue = &limit.Int64
			u.OverLimit = limit.Int64 > 0 && u.Quantity > limit.Int64
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func dbQuotaMeasurements(db *sql.DB, pid string, acct *Account) ([]quotaMeasurement, error) {
	rows, err := db.Query(`
		SELECT l.feature_key, l.limit_value, l.metadata_json, COALESCE(SUM(u.quantity), 0)
		FROM saas_plan_limits l
		LEFT JOIN saas_usage_snapshots u
		  ON u.project_id=l.project_id AND u.account_id=? AND u.feature_key=l.feature_key
		WHERE l.project_id=? AND l.plan_key=?
		GROUP BY l.feature_key, l.limit_value, l.metadata_json
		ORDER BY l.feature_key`, acct.ID, pid, acct.PlanKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quotaMeasurement
	for rows.Next() {
		var measurement quotaMeasurement
		var metadata string
		if err := rows.Scan(&measurement.FeatureKey, &measurement.LimitValue, &metadata, &measurement.Quantity); err != nil {
			return nil, err
		}
		measurement.Metadata = json.RawMessage(metadata)
		out = append(out, measurement)
	}
	return out, rows.Err()
}

func dbQuotaStateApply(db *sql.DB, pid string, acct *Account, measurement quotaMeasurement, threshold int64, state string) (*quotaTransition, bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	previous := quotaStateBelow
	err = tx.QueryRow(`SELECT state FROM saas_quota_states WHERE project_id=? AND account_id=? AND feature_key=?`, pid, acct.ID, measurement.FeatureKey).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	changed := previous != state
	changedAt := any(nil)
	if changed {
		changedAt = time.Now().UTC().Format("2006-01-02 15:04:05")
	}
	_, err = tx.Exec(`
		INSERT INTO saas_quota_states
			(project_id, account_id, plan_key, feature_key, state, quantity, limit_value, threshold_percent, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, account_id, feature_key) DO UPDATE SET
			plan_key=excluded.plan_key, state=excluded.state, quantity=excluded.quantity,
			limit_value=excluded.limit_value, threshold_percent=excluded.threshold_percent,
			changed_at=CASE WHEN saas_quota_states.state<>excluded.state THEN excluded.changed_at ELSE saas_quota_states.changed_at END,
			updated_at=CURRENT_TIMESTAMP`,
		pid, acct.ID, acct.PlanKey, measurement.FeatureKey, state, measurement.Quantity, measurement.LimitValue, threshold, changedAt)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &quotaTransition{
		PreviousState: previous, State: state, Quantity: measurement.Quantity,
		LimitValue: measurement.LimitValue, ThresholdPercent: threshold,
	}, changed, nil
}

func recordEvent(db *sql.DB, pid, accountID, eventType, actor string, payload any) error {
	_, err := db.Exec(`INSERT INTO saas_events (project_id, account_id, event_type, actor, payload_json) VALUES (?, ?, ?, ?, ?)`,
		pid, accountID, eventType, firstNonEmpty(actor, "system"), jsonOrEmpty(payload, "{}"))
	return err
}

func dbFulfillmentRunReserve(db *sql.DB, pid, accountID string, action *PlanAction, transitionID string, input any, staleAfter time.Duration) (*FulfillmentRun, string, error) {
	persistedInput := persistedFulfillmentValue(action.PersistInput, input, action.SensitiveInputPaths)
	res, err := db.Exec(`
		INSERT INTO saas_fulfillment_runs
			(project_id, account_id, plan_action_id, transition_id, event, app_name, tool_name, status, input_json, output_json, error, attempt_count, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, '{}', '', 1, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, account_id, plan_action_id, transition_id) DO NOTHING`,
		pid, accountID, action.ID, transitionID, action.Event, action.AppName, action.ToolName, jsonOrEmpty(persistedInput, "{}"))
	if err != nil {
		return nil, "", err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		run, err := dbFulfillmentRunByTransition(db, pid, accountID, action.ID, transitionID)
		return run, "call", err
	}
	run, err := dbFulfillmentRunByTransition(db, pid, accountID, action.ID, transitionID)
	if err != nil || run == nil {
		return nil, "", firstErr(err, errors.New("fulfillment run not found after claim"))
	}
	switch run.Status {
	case "succeeded":
		return run, "skip", nil
	case "call_succeeded":
		return run, "store", nil
	case "pending":
		started, ok := parseTimestamp(run.StartedAt)
		if ok && time.Since(started) < staleAfter {
			return run, "in_progress", nil
		}
	}
	_, err = db.Exec(`
		UPDATE saas_fulfillment_runs
		SET status='pending', input_json=?, error='', attempt_count=attempt_count+1,
		    started_at=CURRENT_TIMESTAMP, completed_at=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, jsonOrEmpty(persistedInput, "{}"), pid, run.ID)
	if err != nil {
		return nil, "", err
	}
	run, err = dbFulfillmentRunGet(db, pid, run.ID)
	return run, "call", err
}

func dbFulfillmentRunFinish(db *sql.DB, pid string, id int64, action *PlanAction, status string, output any, errText string) (*FulfillmentRun, error) {
	completed := "NULL"
	if status == "succeeded" || status == "failed" {
		completed = "CURRENT_TIMESTAMP"
	}
	persistedOutput := persistedFulfillmentValue(action.PersistOutput, output, action.SensitiveOutputPaths)
	_, err := db.Exec(`UPDATE saas_fulfillment_runs
		SET status=?, output_json=?, error=?, completed_at=`+completed+`, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, status, jsonOrEmpty(persistedOutput, "{}"), errText, pid, id)
	if err != nil {
		return nil, err
	}
	return dbFulfillmentRunGet(db, pid, id)
}

func dbFulfillmentRunByTransition(db *sql.DB, pid, accountID string, actionID int64, transitionID string) (*FulfillmentRun, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM saas_fulfillment_runs WHERE project_id=? AND account_id=? AND plan_action_id=? AND transition_id=?`, pid, accountID, actionID, transitionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbFulfillmentRunGet(db, pid, id)
}

func dbFulfillmentRunGet(db *sql.DB, pid string, id int64) (*FulfillmentRun, error) {
	var r FulfillmentRun
	var input, output string
	var started, completed sql.NullString
	err := db.QueryRow(`
		SELECT id, project_id, account_id, plan_action_id, transition_id, event, app_name, tool_name, status, input_json, output_json, error,
		       attempt_count, started_at, completed_at, created_at, updated_at
		FROM saas_fulfillment_runs WHERE project_id=? AND id=?`, pid, id).
		Scan(&r.ID, &r.ProjectID, &r.AccountID, &r.PlanActionID, &r.TransitionID, &r.Event, &r.AppName, &r.ToolName, &r.Status, &input, &output, &r.Error,
			&r.AttemptCount, &started, &completed, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.Input = json.RawMessage(input)
	r.Output = json.RawMessage(output)
	if started.Valid {
		r.StartedAt = started.String
	}
	if completed.Valid {
		r.CompletedAt = completed.String
	}
	return &r, nil
}

func dbLifecycleTransitionReserve(db *sql.DB, pid, accountID, event, fromStatus, toStatus, sourceKey string) (*LifecycleTransition, error) {
	if sourceKey != "" {
		if existing, err := dbLifecycleTransitionBySource(db, pid, accountID, sourceKey); err != nil || existing != nil {
			return existing, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence),0)+1 FROM saas_lifecycle_transitions WHERE project_id=? AND account_id=?`, pid, accountID).Scan(&seq); err != nil {
		return nil, err
	}
	id := newID("trn")
	_, err = tx.Exec(`INSERT INTO saas_lifecycle_transitions
		(id, project_id, account_id, sequence, event, from_status, to_status, source_key, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending')`, id, pid, accountID, seq, event, fromStatus, toStatus, sourceKey)
	if err != nil {
		if sourceKey != "" {
			_ = tx.Rollback()
			return dbLifecycleTransitionBySource(db, pid, accountID, sourceKey)
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbLifecycleTransitionGet(db, pid, id)
}

func dbLifecycleTransitionComplete(db *sql.DB, pid string, transition *LifecycleTransition, status string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE saas_accounts SET status=?, last_error='', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, pid, transition.AccountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE saas_lifecycle_transitions SET status='completed', completed_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, transition.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func dbLifecycleTransitionGet(db *sql.DB, pid, id string) (*LifecycleTransition, error) {
	var t LifecycleTransition
	var completed sql.NullString
	err := db.QueryRow(`SELECT id, project_id, account_id, sequence, event, from_status, to_status, source_key, status, created_at, completed_at
		FROM saas_lifecycle_transitions WHERE project_id=? AND id=?`, pid, id).
		Scan(&t.ID, &t.ProjectID, &t.AccountID, &t.Sequence, &t.Event, &t.FromStatus, &t.ToStatus, &t.SourceKey, &t.Status, &t.CreatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if completed.Valid {
		t.CompletedAt = completed.String
	}
	return &t, err
}

func dbLifecycleTransitionBySource(db *sql.DB, pid, accountID, sourceKey string) (*LifecycleTransition, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM saas_lifecycle_transitions
		WHERE project_id=? AND account_id=? AND source_key=? AND status='pending'
		ORDER BY sequence DESC LIMIT 1`, pid, accountID, sourceKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbLifecycleTransitionGet(db, pid, id)
}

func dbCheckoutClaim(db *sql.DB, pid, idempotencyKey, fingerprint string, customer *Customer, plan *Plan, slug, paymentMode string) (*Checkout, bool, error) {
	id := newID("chk")
	if _, err := db.Exec(`INSERT INTO saas_checkouts
		(id, project_id, idempotency_key, request_fingerprint, customer_id, plan_key, slug, owner_email, payment_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, idempotency_key) DO NOTHING`,
		id, pid, idempotencyKey, fingerprint, customer.ID, plan.Key, slug, customer.Email, paymentMode); err != nil {
		return nil, false, err
	}
	checkout, err := dbCheckoutByIdempotencyKey(db, pid, idempotencyKey)
	if err != nil || checkout == nil {
		return checkout, false, firstErr(err, errors.New("checkout reservation failed"))
	}
	if checkout.RequestFingerprint != fingerprint {
		return nil, false, errors.New("idempotency key was already used for a different checkout")
	}
	now := time.Now().UTC()
	res, err := db.Exec(`UPDATE saas_checkouts SET
		status='processing', attempt_count=attempt_count+1, last_error='',
		payment_url=CASE WHEN status='setup_expired' THEN '' ELSE payment_url END,
		provider_session_id=CASE WHEN status='setup_expired' THEN '' ELSE provider_session_id END,
		session_expires_at=CASE WHEN status='setup_expired' THEN NULL ELSE session_expires_at END,
		lease_until=?, started_at=COALESCE(started_at, ?), updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND (
			status IN ('pending','failed','payment_failed','setup_expired') OR
			(status='processing' AND (lease_until IS NULL OR lease_until < ?))
		)`, now.Add(commerceClaimTimeout).Format(time.RFC3339), now.Format(time.RFC3339), pid, checkout.ID, now.Format(time.RFC3339))
	if err != nil {
		return nil, false, err
	}
	checkout, err = dbCheckoutGet(db, pid, checkout.ID)
	if err != nil {
		return nil, false, err
	}
	claimed, _ := res.RowsAffected()
	return checkout, claimed == 1, nil
}

func dbCheckoutSetSubscription(db *sql.DB, pid, id string, subscriptionID int64) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET subscription_id=?, stage='subscription_created', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, subscriptionID, pid, id)
	return err
}

func dbCheckoutSetAccount(db *sql.DB, pid, id, accountID string) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET account_id=?, stage='account_created', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, accountID, pid, id)
	return err
}

func dbCheckoutSetBillingCustomer(db *sql.DB, pid, id string, billingCustomerID int64) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET billing_customer_id=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, billingCustomerID, pid, id)
	return err
}

func dbCheckoutSetCycle(db *sql.DB, pid, id string, cycleID int64) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET cycle_id=?, stage='cycle_created', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, cycleID, pid, id)
	return err
}

func dbCheckoutSetTrial(db *sql.DB, pid, id, trialEnd string) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET trial_ends_at=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, nullString(trialEnd), pid, id)
	return err
}

func dbCheckoutSetSetup(db *sql.DB, pid, id string, setupOut map[string]any) error {
	setup := unwrapMap(setupOut, "setup_session")
	url := firstNonEmpty(strArg(setupOut, "url"), strArg(setup, "url"))
	sessionID := firstNonEmpty(strArg(setup, "provider_session_id"), strArg(setup, "stripe_session_id"), strArg(setup, "id"))
	expiresAt := timeStringFromAny(setup["expires_at"])
	_, err := db.Exec(`UPDATE saas_checkouts SET payment_url=?, provider_session_id=?, session_expires_at=?, stage='setup_session_created', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, url, sessionID, nullString(expiresAt), pid, id)
	return err
}

func dbCheckoutSetInvoice(db *sql.DB, pid, id string, cycleID, invoiceID int64, paymentLink map[string]any) error {
	url := strArg(paymentLink, "url")
	sessionID := firstNonEmpty(strArg(paymentLink, "stripe_session_id"), strArg(paymentLink, "provider_session_id"))
	expiresAt := timeStringFromAny(paymentLink["expires_at"])
	_, err := db.Exec(`UPDATE saas_checkouts SET cycle_id=?, invoice_id=?, payment_url=?, provider_session_id=?, session_expires_at=?, stage='payment_link_created', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, cycleID, invoiceID, url, sessionID, nullString(expiresAt), pid, id)
	return err
}

func dbCheckoutSetPaymentMethod(db *sql.DB, pid, id string, paymentMethodID int64) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET payment_method_id=?, stage='payment_method_attached', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, paymentMethodID, pid, id)
	return err
}

func dbCheckoutComplete(db *sql.DB, pid, id, status string, result map[string]any) error {
	if _, err := db.Exec(`UPDATE saas_checkouts SET status=?, result_json=?, last_error='', lease_until=NULL, completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, jsonOrEmpty(result, "{}"), pid, id); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE saas_accounts SET metadata_json=json_set(metadata_json, '$.checkout_status', ?), updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=(SELECT account_id FROM saas_checkouts WHERE project_id=? AND id=?)`, status, pid, pid, id)
	return err
}

func dbCheckoutSetStatus(db *sql.DB, pid, id, status, message string) error {
	_, err := db.Exec(`UPDATE saas_checkouts SET status=?, last_error=?, lease_until=NULL, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, status, message, pid, id)
	return err
}

func dbCheckoutFail(db *sql.DB, pid, id, message string) error {
	return dbCheckoutSetStatus(db, pid, id, "failed", message)
}

func dbCheckoutLookup(db *sql.DB, pid string, args map[string]any) (*Checkout, error) {
	if id := strArg(args, "checkout_id"); id != "" {
		return dbCheckoutGet(db, pid, id)
	}
	if key := strArg(args, "idempotency_key"); key != "" {
		return dbCheckoutByIdempotencyKey(db, pid, key)
	}
	if slug := strArg(args, "slug"); slug != "" {
		return scanCheckout(db.QueryRow(checkoutSelect()+` WHERE project_id=? AND slug=?`, pid, slug))
	}
	return nil, errors.New("checkout_id, idempotency_key, or slug required")
}

func dbCheckoutGet(db *sql.DB, pid, id string) (*Checkout, error) {
	return scanCheckout(db.QueryRow(checkoutSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func dbCheckoutByIdempotencyKey(db *sql.DB, pid, key string) (*Checkout, error) {
	return scanCheckout(db.QueryRow(checkoutSelect()+` WHERE project_id=? AND idempotency_key=?`, pid, key))
}

func dbCheckoutTrialBySubscription(db *sql.DB, pid string, subscriptionID int64) (*Checkout, error) {
	return scanCheckout(db.QueryRow(checkoutSelect()+` WHERE project_id=? AND subscription_id=? AND status='trialing' ORDER BY created_at DESC LIMIT 1`, pid, subscriptionID))
}

func dbCheckoutExpiredSetups(db *sql.DB, pid string, now time.Time) ([]*Checkout, error) {
	rows, err := db.Query(checkoutSelect()+` WHERE project_id=? AND status='awaiting_payment_method' AND session_expires_at IS NOT NULL AND session_expires_at < ? ORDER BY id LIMIT 100`, pid, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Checkout
	for rows.Next() {
		checkout, err := scanCheckout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, checkout)
	}
	return out, rows.Err()
}

func checkoutSelect() string {
	return `SELECT id, project_id, idempotency_key, request_fingerprint, customer_id, COALESCE(account_id,''), plan_key, slug, owner_email,
		subscription_id, cycle_id, billing_customer_id, invoice_id, payment_method_id, status, stage, payment_mode,
		payment_url, provider_session_id, trial_ends_at, session_expires_at, attempt_count, result_json, last_error,
		lease_until, started_at, completed_at, created_at, updated_at FROM saas_checkouts`
}

func scanCheckout(row rowScanner) (*Checkout, error) {
	var checkout Checkout
	var subscriptionID, cycleID, billingCustomerID, invoiceID, paymentMethodID sql.NullInt64
	var trialEnd, sessionExpiry, leaseUntil, startedAt, completedAt sql.NullString
	var result string
	err := row.Scan(&checkout.ID, &checkout.ProjectID, &checkout.IdempotencyKey, &checkout.RequestFingerprint, &checkout.CustomerID, &checkout.AccountID,
		&checkout.PlanKey, &checkout.Slug, &checkout.OwnerEmail, &subscriptionID, &cycleID, &billingCustomerID, &invoiceID, &paymentMethodID,
		&checkout.Status, &checkout.Stage, &checkout.PaymentMode, &checkout.PaymentURL, &checkout.ProviderSessionID,
		&trialEnd, &sessionExpiry, &checkout.AttemptCount, &result, &checkout.LastError, &leaseUntil, &startedAt, &completedAt,
		&checkout.CreatedAt, &checkout.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	checkout.SubscriptionID = ptrIfValid(subscriptionID)
	checkout.CycleID = ptrIfValid(cycleID)
	checkout.BillingCustomerID = ptrIfValid(billingCustomerID)
	checkout.InvoiceID = ptrIfValid(invoiceID)
	checkout.PaymentMethodID = ptrIfValid(paymentMethodID)
	checkout.Result = json.RawMessage(result)
	if trialEnd.Valid {
		checkout.TrialEndsAt = trialEnd.String
	}
	if sessionExpiry.Valid {
		checkout.SessionExpiresAt = sessionExpiry.String
	}
	if leaseUntil.Valid {
		checkout.LeaseUntil = leaseUntil.String
	}
	if startedAt.Valid {
		checkout.StartedAt = startedAt.String
	}
	if completedAt.Valid {
		checkout.CompletedAt = completedAt.String
	}
	return &checkout, nil
}

func dbCommerceCycleClaim(db *sql.DB, pid, accountID string, subscriptionID, cycleID int64) (*CommerceOperation, bool, error) {
	key := fmt.Sprintf("subscription:%d:cycle:%d", subscriptionID, cycleID)
	if _, err := db.Exec(`INSERT INTO saas_commerce_operations
		(project_id, operation_key, account_id, subscription_id, cycle_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, operation_key) DO NOTHING`, pid, key, accountID, subscriptionID, cycleID); err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(commerceClaimTimeout).Format(time.RFC3339)
	res, err := db.Exec(`UPDATE saas_commerce_operations SET
		status='processing_billing', attempt_count=attempt_count+1, last_error='',
		lease_until=?, started_at=COALESCE(started_at, ?), updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND operation_key=? AND (
			status IN ('pending','failed_billing') OR
			(status='processing_billing' AND (lease_until IS NULL OR lease_until < ?))
		)`, leaseUntil, now.Format(time.RFC3339), pid, key, now.Format(time.RFC3339))
	if err != nil {
		return nil, false, err
	}
	op, err := dbCommerceOperationByKey(db, pid, key)
	if err != nil {
		return nil, false, err
	}
	claimed, _ := res.RowsAffected()
	return op, claimed == 1, nil
}

func dbCommercePaymentClaim(db *sql.DB, pid string, invoiceID int64) (*CommerceOperation, bool, error) {
	op, err := dbCommerceOperationByInvoice(db, pid, invoiceID)
	if err != nil || op == nil || op.Status == "paid" {
		return op, false, err
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(commerceClaimTimeout).Format(time.RFC3339)
	res, err := db.Exec(`UPDATE saas_commerce_operations SET
		status='processing_payment', attempt_count=attempt_count+1, last_error='', lease_until=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=? AND (
			status IN ('awaiting_payment','failed_billing','failed_payment') OR
			(status='processing_payment' AND (lease_until IS NULL OR lease_until < ?))
		)`, leaseUntil, pid, op.ID, now.Format(time.RFC3339))
	if err != nil {
		return nil, false, err
	}
	op, err = dbCommerceOperationGet(db, pid, op.ID)
	if err != nil {
		return nil, false, err
	}
	claimed, _ := res.RowsAffected()
	return op, claimed == 1, nil
}

func dbCommerceOperationSetCheckout(db *sql.DB, pid string, id int64, checkoutID string) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET checkout_id=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, checkoutID, pid, id)
	return err
}

func dbCommerceOperationSetCustomer(db *sql.DB, pid string, id, billingCustomerID int64) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET billing_customer_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, billingCustomerID, pid, id)
	return err
}

func dbCommerceOperationSetPrepared(db *sql.DB, pid string, id int64, prepared map[string]any) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET prepared_json=?, stage='prepared', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, jsonOrEmpty(prepared, "{}"), pid, id)
	return err
}

func dbCommerceOperationSetInvoice(db *sql.DB, pid string, id, invoiceID int64) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET invoice_id=?, stage='invoice_created', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, invoiceID, pid, id)
	return err
}

func dbCommerceOperationSetStage(db *sql.DB, pid string, id int64, stage string) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET stage=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, stage, pid, id)
	return err
}

func dbCommerceOperationCompleteBilling(db *sql.DB, pid string, id int64, paymentLink map[string]any) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET
		status='awaiting_payment', stage='payment_link_created', payment_link_json=?, last_error='',
		lease_until=NULL, completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, jsonOrEmpty(paymentLink, "{}"), pid, id)
	return err
}

func dbCommerceOperationCompletePayment(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET
		status='paid', stage='subscription_activated', last_error='', lease_until=NULL,
		completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, pid, id)
	return err
}

func dbCommerceOperationCompletePlanChangePayment(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET
		status='paid', stage='plan_changed', last_error='', lease_until=NULL,
		completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, pid, id)
	return err
}

func dbCommerceOperationFail(db *sql.DB, pid string, id int64, status, message string) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET status=?, last_error=?, lease_until=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, status, message, pid, id)
	return err
}

func dbCommerceOperationByKey(db *sql.DB, pid, key string) (*CommerceOperation, error) {
	return scanCommerceOperation(db.QueryRow(commerceOperationSelect()+` WHERE project_id=? AND operation_key=?`, pid, key))
}

func dbCommerceOperationByInvoice(db *sql.DB, pid string, invoiceID int64) (*CommerceOperation, error) {
	return scanCommerceOperation(db.QueryRow(commerceOperationSelect()+` WHERE project_id=? AND invoice_id=?`, pid, invoiceID))
}

func dbCommerceOperationGet(db *sql.DB, pid string, id int64) (*CommerceOperation, error) {
	return scanCommerceOperation(db.QueryRow(commerceOperationSelect()+` WHERE project_id=? AND id=?`, pid, id))
}

func dbCommerceOperationsForReconciliation(db *sql.DB, pid string, before time.Time, limit int) ([]*CommerceOperation, error) {
	rows, err := db.Query(commerceOperationSelect()+` WHERE project_id=? AND status='awaiting_payment' AND updated_at < ? ORDER BY updated_at LIMIT ?`, pid, before.Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CommerceOperation
	for rows.Next() {
		operation, err := scanCommerceOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, operation)
	}
	return out, rows.Err()
}

func dbCommerceOperationsForBillingProjection(db *sql.DB, pid, accountID string, missingOnly bool, limit, offset int) ([]*CommerceOperation, error) {
	where := []string{"co.project_id=?", "co.invoice_id IS NOT NULL"}
	vals := []any{pid}
	if accountID != "" {
		where = append(where, "co.account_id=?")
		vals = append(vals, accountID)
	}
	if missingOnly {
		where = append(where, "NOT EXISTS (SELECT 1 FROM saas_billing_invoices bi WHERE bi.project_id=co.project_id AND bi.invoice_id=co.invoice_id)")
	}
	orderBy := "co.id"
	if missingOnly {
		orderBy = "CASE WHEN co.billing_projection_attempted_at IS NULL THEN 0 ELSE 1 END, co.billing_projection_attempted_at, co.id"
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	vals = append(vals, limit, offset)
	rows, err := db.Query(`SELECT co.id, co.project_id, co.operation_key, co.account_id, co.subscription_id, co.cycle_id, COALESCE(co.checkout_id,''),
		co.billing_customer_id, co.invoice_id, co.status, co.stage, co.attempt_count, co.prepared_json, co.payment_link_json,
		co.last_error, co.lease_until, co.started_at, co.completed_at, co.created_at, co.updated_at
		FROM saas_commerce_operations co WHERE `+strings.Join(where, " AND ")+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CommerceOperation{}
	for rows.Next() {
		operation, err := scanCommerceOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, operation)
	}
	return out, rows.Err()
}

func dbCommerceOperationProjectionAttempt(db *sql.DB, pid string, id int64, errText string) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations
		SET billing_projection_attempted_at=CURRENT_TIMESTAMP, billing_projection_error=?
		WHERE project_id=? AND id=?`, errText, pid, id)
	return err
}

func dbBillingProjectionPendingCount(db *sql.DB, pid, accountID string) (int64, error) {
	query := `SELECT COUNT(*) FROM saas_commerce_operations co
		WHERE co.project_id=? AND co.invoice_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM saas_billing_invoices bi WHERE bi.project_id=co.project_id AND bi.invoice_id=co.invoice_id)`
	vals := []any{pid}
	if accountID != "" {
		query += " AND co.account_id=?"
		vals = append(vals, accountID)
	}
	var count int64
	err := db.QueryRow(query, vals...).Scan(&count)
	return count, err
}

func dbBillingProjectionReplace(db *sql.DB, pid string, projection *billingInvoiceProjection) error {
	if projection == nil || projection.InvoiceID <= 0 || projection.AccountID == "" || projection.BillingCustomerID <= 0 {
		return errors.New("complete Billing invoice projection required")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO saas_billing_invoices
		(project_id, invoice_id, account_id, billing_customer_id, subscription_id, cycle_id, status, currency,
		 total_cents, amount_paid_cents, paid_at, source_created_at, source_updated_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, invoice_id) DO UPDATE SET
		 account_id=excluded.account_id, billing_customer_id=excluded.billing_customer_id,
		 subscription_id=excluded.subscription_id, cycle_id=excluded.cycle_id, status=excluded.status,
		 currency=excluded.currency, total_cents=excluded.total_cents, amount_paid_cents=excluded.amount_paid_cents,
		 paid_at=excluded.paid_at, source_created_at=excluded.source_created_at,
		 source_updated_at=excluded.source_updated_at, synced_at=CURRENT_TIMESTAMP`,
		pid, projection.InvoiceID, projection.AccountID, projection.BillingCustomerID,
		projection.SubscriptionID, projection.CycleID, projection.Status, projection.Currency,
		projection.TotalCents, projection.AmountPaidCents, nullString(projection.PaidAt),
		nullString(projection.SourceCreatedAt), nullString(projection.SourceUpdatedAt))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM saas_billing_payments WHERE project_id=? AND invoice_id=?`, pid, projection.InvoiceID); err != nil {
		return err
	}
	for _, payment := range projection.Payments {
		_, err := tx.Exec(`INSERT INTO saas_billing_payments
			(project_id, payment_id, invoice_id, account_id, billing_customer_id, amount_cents, currency, method, received_at, source_created_at, synced_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(project_id, payment_id) DO UPDATE SET
			 invoice_id=excluded.invoice_id, account_id=excluded.account_id, billing_customer_id=excluded.billing_customer_id,
			 amount_cents=excluded.amount_cents, currency=excluded.currency, method=excluded.method,
			 received_at=excluded.received_at, source_created_at=excluded.source_created_at, synced_at=CURRENT_TIMESTAMP`,
			pid, payment.PaymentID, projection.InvoiceID, projection.AccountID, projection.BillingCustomerID,
			payment.AmountCents, payment.Currency, payment.Method, payment.ReceivedAt, nullString(payment.SourceCreatedAt))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func dbCommerceOperationTouch(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`UPDATE saas_commerce_operations SET updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, id)
	return err
}

func commerceOperationSelect() string {
	return `SELECT id, project_id, operation_key, account_id, subscription_id, cycle_id, COALESCE(checkout_id,''),
		billing_customer_id, invoice_id, status, stage, attempt_count, prepared_json, payment_link_json,
		last_error, lease_until, started_at, completed_at, created_at, updated_at
		FROM saas_commerce_operations`
}

func scanCommerceOperation(row rowScanner) (*CommerceOperation, error) {
	var op CommerceOperation
	var billingCustomerID, invoiceID sql.NullInt64
	var prepared, paymentLink string
	var leaseUntil, startedAt, completedAt sql.NullString
	err := row.Scan(
		&op.ID, &op.ProjectID, &op.OperationKey, &op.AccountID, &op.SubscriptionID, &op.CycleID, &op.CheckoutID,
		&billingCustomerID, &invoiceID, &op.Status, &op.Stage, &op.AttemptCount, &prepared, &paymentLink,
		&op.LastError, &leaseUntil, &startedAt, &completedAt, &op.CreatedAt, &op.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if billingCustomerID.Valid {
		op.BillingCustomerID = &billingCustomerID.Int64
	}
	if invoiceID.Valid {
		op.InvoiceID = &invoiceID.Int64
	}
	op.Prepared = json.RawMessage(prepared)
	op.PaymentLink = json.RawMessage(paymentLink)
	if leaseUntil.Valid {
		op.LeaseUntil = leaseUntil.String
	}
	if startedAt.Valid {
		op.StartedAt = startedAt.String
	}
	if completedAt.Valid {
		op.CompletedAt = completedAt.String
	}
	return &op, nil
}

func dbLatestLifecycleTransition(db *sql.DB, pid, accountID, event string) (*LifecycleTransition, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM saas_lifecycle_transitions WHERE project_id=? AND account_id=? AND event=? ORDER BY sequence DESC LIMIT 1`, pid, accountID, event).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbLifecycleTransitionGet(db, pid, id)
}

func (a *App) handlePlans(w http.ResponseWriter, r *http.Request) {
	ctx, ok := requireHTTPContext(w)
	if !ok {
		return
	}
	pid, err := requireProjectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		plans, err := dbPlanList(ctx.AppDB(), pid)
		if err == nil {
			a.enrichPlansWithCatalog(ctx, pid, plans)
		}
		handleJSONOrErr(w, map[string]any{"plans": plans, "count": len(plans)}, err)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleCustomers(w http.ResponseWriter, r *http.Request) {
	ctx, ok := requireHTTPContext(w)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := a.toolCustomerCreate(ctx, body)
	handleJSONOrErr(w, out, err)
}

func (a *App) handleAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, ok := requireHTTPContext(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		pid, err := requireProjectFromRequest(ctx, r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		args := map[string]any{}
		for _, key := range []string{"status", "plan_key", "catalog_product_id", "customer_id", "customer_email", "subscription_id", "created_before", "created_after", "paid_since", "paid_until", "last_paid_before", "last_paid_after", "limit", "offset"} {
			if value := r.URL.Query().Get(key); value != "" {
				args[key] = value
			}
		}
		if values, ok := r.URL.Query()["has_paid"]; ok && len(values) > 0 {
			args["has_paid"] = values[0]
		}
		if err := validateAccountSearchArgs(args); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		accounts, total, limit, offset, err := dbAccountSearch(ctx.AppDB(), pid, args)
		if err != nil {
			handleJSONOrErr(w, nil, err)
			return
		}
		pending, err := dbBillingProjectionPendingCount(ctx.AppDB(), pid, "")
		handleJSONOrErr(w, map[string]any{
			"accounts": accounts, "count": len(accounts), "total": total,
			"limit": limit, "offset": offset, "has_more": offset+len(accounts) < total,
			"billing_sync_pending": pending,
		}, err)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		out, err := a.toolAccountCreate(ctx, body)
		handleJSONOrErr(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAccountItem(w http.ResponseWriter, r *http.Request) {
	ctx, ok := requireHTTPContext(w)
	if !ok {
		return
	}
	pid, err := requireProjectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/accounts/")
	if r.Method == http.MethodGet {
		acct, err := dbAccountGet(ctx.AppDB(), pid, id)
		if err == nil && acct == nil {
			err = errors.New("account not found")
		} else if err == nil {
			err = dbHydrateAccounts(ctx.AppDB(), pid, []*Account{acct})
		}
		handleJSONOrErr(w, map[string]any{"account": acct}, err)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx, ok := requireHTTPContext(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := requireProjectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args := map[string]any{"account_id": r.URL.Query().Get("account_id"), "customer_id": r.URL.Query().Get("customer_id"), "feature_key": r.URL.Query().Get("feature_key")}
	usage, err := dbUsageTotals(ctx.AppDB(), pid, args)
	handleJSONOrErr(w, map[string]any{"usage": usage, "count": len(usage)}, err)
}

func requireHTTPContext(w http.ResponseWriter) (*sdk.AppCtx, bool) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "saas not mounted")
		return nil, false
	}
	return globalCtx, true
}

func handleJSONOrErr(w http.ResponseWriter, body any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, body)
}

func httpJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

type rowScanner interface{ Scan(...any) error }

func requireProject(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	pid := projectID(ctx, args)
	if pid == "" {
		return "", errors.New("project_id required")
	}
	return pid, nil
}

func projectID(ctx *sdk.AppCtx, args map[string]any) string {
	if args != nil {
		if pid := strArg(args, "_project_id"); pid != "" {
			return pid
		}
		if pid := strArg(args, "project_id"); pid != "" {
			return pid
		}
	}
	if ctx != nil {
		if pid := ctx.CurrentProject(); pid != "" {
			return pid
		}
	}
	return os.Getenv("APTEVA_PROJECT_ID")
}

func requireProjectFromRequest(ctx *sdk.AppCtx, r *http.Request) (string, error) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" && ctx != nil {
		pid = ctx.CurrentProject()
	}
	if pid == "" {
		pid = os.Getenv("APTEVA_PROJECT_ID")
	}
	if pid == "" {
		return "", errors.New("project_id query parameter required")
	}
	return pid, nil
}

func accountIDSchema() map[string]any {
	return schemaObject(map[string]any{"account_id": strSchema(), "actor": strSchema()}, []string{"account_id"})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strSchema() map[string]any  { return map[string]any{"type": "string"} }
func intSchema() map[string]any  { return map[string]any{"type": "integer"} }
func boolSchema() map[string]any { return map[string]any{"type": "boolean"} }
func objSchema() map[string]any  { return map[string]any{"type": "object"} }
func strArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return strFromAny(m[key])
}

func strFromAny(v any) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	}
	return ""
}

func boolArg(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.TrimSpace(v) == "1"
	}
	return false
}

func boolArgDefault(m map[string]any, key string, fallback bool) bool {
	if m == nil {
		return fallback
	}
	if _, ok := m[key]; !ok {
		return fallback
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		s := strings.TrimSpace(v)
		if strings.EqualFold(s, "true") || s == "1" {
			return true
		}
		if strings.EqualFold(s, "false") || s == "0" {
			return false
		}
	}
	return fallback
}

func int64Arg(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	return int64FromAny(m[key])
}

func int64FromAny(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case float32:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case int32:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	return 0
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

func int64PtrValue(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func actor(args map[string]any) string {
	return firstNonEmpty(strArg(args, "actor"), "tool")
}

func jsonRaw(v any) json.RawMessage {
	return json.RawMessage(jsonOrEmpty(v, "{}"))
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
	default:
		b, err := json.Marshal(v)
		if err != nil || len(b) == 0 {
			return sentinel
		}
		return string(b)
	}
}

func stringOrJSON(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

func mapFromAny(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func sliceFromAny(v any) []any {
	if v == nil {
		return nil
	}
	if values, ok := v.([]any); ok {
		return values
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []any
	_ = json.Unmarshal(b, &out)
	return out
}

func configInt(ctx *sdk.AppCtx, key string, fallback int) int {
	if ctx == nil {
		return fallback
	}
	value := strings.TrimSpace(ctx.Config().Get(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func parseTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func unwrapMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	return mapFromAny(m[key])
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mergeMetadata(raw any, extra map[string]any) map[string]any {
	out := mapFromAny(raw)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func int64FromResult(out map[string]any, objectKey, idKey string) int64 {
	if nested := unwrapMap(out, objectKey); nested != nil {
		if id := int64Arg(nested, idKey); id != 0 {
			return id
		}
	}
	return int64Arg(out, idKey)
}

func periodEndFrom(start, interval string, count int64) string {
	if count <= 0 {
		count = 1
	}
	t, err := time.Parse(time.RFC3339, start)
	if err != nil {
		t = time.Now().UTC()
	}
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "day", "daily":
		t = t.AddDate(0, 0, int(count))
	case "week", "weekly":
		t = t.AddDate(0, 0, int(7*count))
	case "year", "yearly", "annual", "annually":
		t = t.AddDate(int(count), 0, 0)
	default:
		t = t.AddDate(0, int(count), 0)
	}
	return t.UTC().Format(time.RFC3339)
}

var slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func normalizeSlug(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if !slugRe.MatchString(s) {
		return "", errors.New("slug must be lowercase letters, digits, and hyphens")
	}
	return s, nil
}

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

func normalizeKey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !keyRe.MatchString(s) {
		return "", errors.New("key must be lowercase letters, digits, dots, underscores, or hyphens")
	}
	return s, nil
}

func ptrIfValid(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func timeStringFromAny(value any) string {
	if unix := int64FromAny(value); unix > 0 {
		return time.Unix(unix, 0).UTC().Format(time.RFC3339)
	}
	if parsed, ok := parseTimestamp(strFromAny(value)); ok {
		return parsed.Format(time.RFC3339)
	}
	return ""
}

func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
