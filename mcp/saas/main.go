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
		return errors.New("saas requires a db block")
	}
	globalCtx = ctx
	pid := projectID(ctx, nil)
	if pid != "" {
		if err := seedPlans(ctx.AppDB(), pid); err != nil {
			return err
		}
	}
	ctx.Logger().Info("saas mounted", "version", "0.1.4", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
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
		{Name: "saas_plan_list", Description: "List SaaS plans.", InputSchema: schemaObject(nil, nil), Handler: a.toolPlanList},
		{Name: "saas_plan_get", Description: "Fetch one SaaS plan.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema()}, []string{"plan_key"}), Handler: a.toolPlanGet},
		{Name: "saas_plan_upsert", Description: "Create or update a SaaS plan.", InputSchema: schemaObject(map[string]any{
			"key": strSchema(), "name": strSchema(), "billing_mode": strSchema(), "catalog_product_id": intSchema(), "catalog_price_id": intSchema(),
			"subscription_required": boolSchema(), "metadata": objSchema(),
		}, []string{"key", "name"}), Handler: a.toolPlanUpsert},
		{Name: "saas_plan_feature_add", Description: "Add a feature grant to a SaaS plan.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "feature_key": strSchema(), "grant_type": strSchema(), "metadata": objSchema()}, []string{"plan_key", "feature_key"}), Handler: a.toolPlanFeatureAdd},
		{Name: "saas_plan_limit_set", Description: "Set a plan limit.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "feature_key": strSchema(), "limit_value": intSchema(), "reset_interval": strSchema(), "metadata": objSchema()}, []string{"plan_key", "feature_key"}), Handler: a.toolPlanLimitSet},
		{Name: "saas_plan_usage_source_add", Description: "Register a live usage source for a SaaS plan.", InputSchema: schemaObject(map[string]any{
			"plan_key": strSchema(), "app_name": strSchema(), "tool_name": strSchema(), "feature_prefix": strSchema(), "feature_key": strSchema(),
			"read_path": strSchema(), "quantity_path": strSchema(), "call_args": objSchema(), "metadata": objSchema(),
		}, []string{"plan_key", "app_name", "tool_name"}), Handler: a.toolPlanUsageSourceAdd},
		{Name: "saas_plan_action_add", Description: "Register a generic fulfillment action for a SaaS plan lifecycle event.", InputSchema: schemaObject(map[string]any{
			"plan_key": strSchema(), "event": strSchema(), "app_name": strSchema(), "tool_name": strSchema(),
			"args": objSchema(), "store": objSchema(), "failure_mode": strSchema(), "enabled": boolSchema(), "metadata": objSchema(),
		}, []string{"plan_key", "event", "app_name", "tool_name"}), Handler: a.toolPlanActionAdd},
		{Name: "saas_plan_action_list", Description: "List generic fulfillment actions for a SaaS plan.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "event": strSchema()}, []string{"plan_key"}), Handler: a.toolPlanActionList},
		{Name: "saas_customer_create", Description: "Find or create a SaaS customer by email.", InputSchema: schemaObject(map[string]any{"email": strSchema(), "name": strSchema(), "billing_customer_id": intSchema(), "auth_user_id": intSchema(), "metadata": objSchema()}, []string{"email"}), Handler: a.toolCustomerCreate},
		{Name: "saas_checkout_create", Description: "Create a paid SaaS checkout: Billing customer, Subscription, invoice/payment link or manual payment, then SaaS account.", InputSchema: schemaObject(map[string]any{
			"owner_email": strSchema(), "customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(), "slug": strSchema(), "plan_key": strSchema(),
			"billing_customer_id": intSchema(), "auth_org_id": intSchema(), "auth_user_id": intSchema(), "create_owner_user": boolSchema(), "send_password_reset": boolSchema(),
			"payment_mode": strSchema(), "record_payment": boolSchema(), "manual_payment_method": strSchema(), "activate_without_payment": boolSchema(),
			"success_url": strSchema(), "cancel_url": strSchema(), "period_start": strSchema(), "period_end": strSchema(), "provider": strSchema(), "metadata": objSchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolCheckoutCreate},
		{Name: "saas_fulfillment_run", Description: "Run or retry configured fulfillment actions for a SaaS account lifecycle event.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "event": strSchema()}, []string{"account_id", "event"}), Handler: a.toolFulfillmentRun},
		{Name: "saas_account_create", Description: "Create a SaaS account and apply plan access.", InputSchema: schemaObject(map[string]any{
			"customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(), "owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(),
			"billing_customer_id": intSchema(), "auth_org_id": intSchema(), "auth_user_id": intSchema(), "subscription_id": intSchema(), "create_owner_user": boolSchema(), "send_password_reset": boolSchema(), "metadata": objSchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolAccountCreate},
		{Name: "saas_account_get", Description: "Fetch one SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountGet},
		{Name: "saas_account_list", Description: "List SaaS accounts.", InputSchema: schemaObject(map[string]any{"customer_id": intSchema(), "status": strSchema(), "plan_key": strSchema()}, nil), Handler: a.toolAccountList},
		{Name: "saas_account_suspend", Description: "Suspend a SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountSuspend},
		{Name: "saas_account_resume", Description: "Resume a SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountResume},
		{Name: "saas_account_cancel", Description: "Cancel a SaaS account.", InputSchema: accountIDSchema(), Handler: a.toolAccountCancel},
		{Name: "saas_subscription_sync", Description: "Apply subscription status to a SaaS account.", InputSchema: schemaObject(map[string]any{"account_id": strSchema(), "subscription_id": intSchema(), "subscription_status": strSchema(), "actor": strSchema()}, []string{"subscription_status"}), Handler: a.toolSubscriptionSync},
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
	CatalogPriceID       *int64          `json:"catalog_price_id,omitempty"`
	SubscriptionRequired bool            `json:"subscription_required"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	Features             []PlanFeature   `json:"features,omitempty"`
	Limits               []PlanLimit     `json:"limits,omitempty"`
	UsageSources         []UsageSource   `json:"usage_sources,omitempty"`
	Actions              []PlanAction    `json:"actions,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
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
	ID          int64           `json:"id"`
	ProjectID   string          `json:"project_id"`
	PlanKey     string          `json:"plan_key"`
	Event       string          `json:"event"`
	AppName     string          `json:"app_name"`
	ToolName    string          `json:"tool_name"`
	Args        json.RawMessage `json:"args,omitempty"`
	Store       json.RawMessage `json:"store,omitempty"`
	FailureMode string          `json:"failure_mode"`
	Enabled     bool            `json:"enabled"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type FulfillmentRun struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id"`
	AccountID    string          `json:"account_id"`
	PlanActionID int64           `json:"plan_action_id"`
	Event        string          `json:"event"`
	AppName      string          `json:"app_name"`
	ToolName     string          `json:"tool_name"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
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
	return map[string]any{"plan": p}, nil
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
	acct, err := a.createAccount(ctx, args)
	if err != nil {
		return nil, err
	}
	ctx.Emit("saas.account.active", map[string]any{"account_id": acct.ID, "customer_id": acct.CustomerID, "plan_key": acct.PlanKey})
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
	return map[string]any{"account": acct}, nil
}

func (a *App) toolAccountList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := dbAccountList(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accounts": out, "count": len(out)}, nil
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
			out, err := a.toolSubscriptionSync(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID, "subscription_status": status, "actor": actor(args)})
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
	case "paused", "cancelled", "ended":
		return a.setAccountStatus(ctx, args, StatusSuspended, "subscription."+status)
	default:
		return nil, fmt.Errorf("unsupported subscription_status %q", status)
	}
}

func (a *App) toolUsageSync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	var accounts []*Account
	if id := strArg(args, "account_id"); id != "" {
		acct, err := dbAccountGet(ctx.AppDB(), pid, id)
		if err != nil || acct == nil {
			return nil, firstErr(err, errors.New("account not found"))
		}
		accounts = []*Account{acct}
	} else {
		if status := strArg(args, "status"); status != "" {
			accounts, err = dbAccountList(ctx.AppDB(), pid, map[string]any{"status": status})
			if err != nil {
				return nil, err
			}
		} else {
			accounts, err = dbAccountList(ctx.AppDB(), pid, map[string]any{"status": StatusActive})
			if err != nil {
				return nil, err
			}
			pastDue, err := dbAccountList(ctx.AppDB(), pid, map[string]any{"status": StatusPastDue})
			if err != nil {
				return nil, err
			}
			accounts = append(accounts, pastDue...)
		}
	}
	records := 0
	for _, acct := range accounts {
		sources, err := dbUsageSources(ctx.AppDB(), pid, acct.PlanKey)
		if err != nil {
			return nil, err
		}
		for _, src := range sources {
			cfg := parseUsageSourceConfig(src.Metadata)
			var out map[string]any
			input := map[string]any{
				"_project_id": pid,
				"account_id":  acct.ID,
				"customer_id": acct.CustomerID,
				"auth_org_id": int64PtrValue(acct.AuthOrgID),
				"plan_key":    acct.PlanKey,
			}
			for k, v := range expandUsageArgs(cfg.CallArgs, pid, acct) {
				input[k] = v
			}
			if err := ctx.PlatformAPI().CallAppResult(src.AppName, src.ToolName, input, &out); err != nil {
				_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, acct.Status, err.Error())
				_ = recordEvent(ctx.AppDB(), pid, acct.ID, "usage_sync.failed", src.AppName, map[string]any{"tool": src.ToolName, "error": err.Error()})
				return nil, err
			}
			gauges, err := usageGaugesFromResponse(src, cfg, out)
			if err != nil {
				_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, acct.Status, err.Error())
				_ = recordEvent(ctx.AppDB(), pid, acct.ID, "usage_sync.failed", src.AppName, map[string]any{"tool": src.ToolName, "error": err.Error()})
				return nil, err
			}
			for _, gauge := range gauges {
				feature := firstNonEmpty(gauge.FeatureKey, src.FeaturePrefix)
				if feature == "" {
					continue
				}
				if src.FeaturePrefix != "" && !strings.Contains(feature, ":") {
					feature = strings.TrimRight(src.FeaturePrefix, ":") + ":" + feature
				}
				if err := dbUsageSnapshotUpsert(ctx.AppDB(), pid, acct, src.AppName, feature, gauge.Quantity, gauge.Metadata); err != nil {
					return nil, err
				}
				records++
			}
		}
		if err := dbAccountUsageSynced(ctx.AppDB(), pid, acct.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"synced_accounts": len(accounts), "records": records}, nil
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
	usage, err := dbUsageTotals(ctx.AppDB(), pid, map[string]any{"account_id": acct.ID, "feature_key": strArg(args, "feature_key")})
	return map[string]any{"usage": usage}, err
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
	return map[string]any{
		"allowed":    allowedStatus && entitlement.Allowed && !overLimit,
		"entitled":   entitlement.Allowed,
		"status":     acct.Status,
		"over_limit": overLimit,
		"usage":      usage,
	}, nil
}

func (a *App) createAccount(ctx *sdk.AppCtx, args map[string]any) (*Account, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	planKey := firstNonEmpty(strArg(args, "plan_key"), "free")
	plan, err := dbPlanGet(ctx.AppDB(), pid, planKey)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found", planKey)
	}
	slug, err := normalizeSlug(strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	owner := strings.ToLower(strings.TrimSpace(strArg(args, "owner_email")))
	if owner == "" {
		return nil, errors.New("owner_email required")
	}
	if existing, _ := dbAccountBySlug(ctx.AppDB(), pid, slug); existing != nil {
		if strings.EqualFold(existing.OwnerEmail, owner) && existing.PlanKey == plan.Key {
			return existing, nil
		}
		return nil, fmt.Errorf("slug %q already belongs to another SaaS account", slug)
	}
	customer, err := resolveCustomer(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	authOrgID := nullableInt64(int64Arg(args, "auth_org_id"))
	if authOrgID == nil {
		authOrgID, err = createAuthOrg(ctx, pid, slug, firstNonEmpty(customer.Name, slug))
		if err != nil {
			return nil, err
		}
	}
	authUserID := nullableInt64(firstNonZero(int64Arg(args, "auth_user_id"), int64PtrValue(customer.AuthUserID)))
	if boolArg(args, "create_owner_user") && authUserID == nil {
		authUserID, err = createAuthUser(ctx, pid, authOrgID, owner, firstNonEmpty(customer.Name, owner), boolArg(args, "send_password_reset"))
		if err != nil {
			return nil, err
		}
	}
	acct := &Account{
		ID:             newID("sac"),
		ProjectID:      pid,
		CustomerID:     customer.ID,
		AuthOrgID:      authOrgID,
		AuthUserID:     authUserID,
		SubscriptionID: nullableInt64(int64Arg(args, "subscription_id")),
		Slug:           slug,
		OwnerEmail:     owner,
		PlanKey:        plan.Key,
		Status:         StatusProvisioning,
		Metadata:       jsonRaw(args["metadata"]),
	}
	if err := dbAccountInsert(ctx.AppDB(), acct); err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "provisioning.started", actor(args), map[string]any{"plan_key": plan.Key})
	if err := a.applyPlanAccess(ctx, pid, acct, plan); err != nil {
		_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusFailed, err.Error())
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "provisioning.failed", "entitlements", map[string]any{"error": err.Error()})
		return nil, err
	}
	acct, err = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusActive, "")
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, "provisioning.active", actor(args), nil)
	if !boolArg(args, "skip_fulfillment") {
		if _, err := a.runFulfillment(ctx, pid, acct, "account_active"); err != nil {
			_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusFailed, err.Error())
			_ = recordEvent(ctx.AppDB(), pid, acct.ID, "fulfillment.failed", "fulfillment", map[string]any{"event": "account_active", "error": err.Error()})
			return nil, err
		}
		acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	}
	_, _ = a.toolUsageSync(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID})
	return dbAccountGet(ctx.AppDB(), pid, acct.ID)
}

func (a *App) createCheckout(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	pid, err := requireProject(ctx, args)
	if err != nil {
		return nil, err
	}
	planKey := firstNonEmpty(strArg(args, "plan_key"), "free")
	plan, err := dbPlanGet(ctx.AppDB(), pid, planKey)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found", planKey)
	}
	customer, err := resolveCustomer(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"customer": customer, "plan": plan}
	requiresCommerce := plan.BillingMode != "free" || plan.SubscriptionRequired
	if !requiresCommerce {
		acct, err := a.createAccount(ctx, args)
		if err != nil {
			return nil, err
		}
		out["account"] = acct
		out["status"] = "active"
		return out, nil
	}

	billingCustomerID, billingCustomer, err := a.ensureBillingCustomer(ctx, pid, customer, args)
	if err != nil {
		return nil, err
	}
	out["billing_customer"] = billingCustomer
	customer, _ = dbCustomerGet(ctx.AppDB(), pid, customer.ID)
	out["customer"] = customer

	price, err := a.resolveCheckoutPrice(ctx, pid, plan, args)
	if err != nil {
		return nil, err
	}
	out["price"] = price.asMap()

	periodStart := firstNonEmpty(strArg(args, "period_start"), time.Now().UTC().Format(time.RFC3339))
	periodEnd := firstNonEmpty(strArg(args, "period_end"), periodEndFrom(periodStart, price.Interval, price.IntervalCount))
	paymentMode := strings.ToLower(firstNonEmpty(strArg(args, "payment_mode"), "payment_link"))
	recordPayment := boolArg(args, "record_payment")
	paidNow := boolArg(args, "activate_without_payment") || (paymentMode == "manual" && recordPayment)
	if paidNow && strArg(args, "payment_mode") == "" {
		paymentMode = "none"
	}
	subStatus := StatusActive
	if !paidNow {
		subStatus = StatusPastDue
	}

	subOut, err := a.createSubscription(ctx, pid, customer, billingCustomerID, plan, price, subStatus, periodStart, periodEnd, args)
	if err != nil {
		return nil, err
	}
	out["subscription"] = unwrapMap(subOut, "subscription")
	subID := int64FromResult(subOut, "subscription", "id")
	if subID == 0 {
		return nil, errors.New("subscriptions_create returned no subscription id")
	}

	invOut, err := a.createSubscriptionInvoice(ctx, pid, subID, periodStart, periodEnd, args)
	if err != nil {
		return nil, err
	}
	out["invoice"] = unwrapMap(invOut, "invoice")
	if cycle := unwrapMap(invOut, "cycle"); cycle != nil {
		out["cycle"] = cycle
	}
	invoiceID := int64FromResult(invOut, "invoice", "id")
	if invoiceID == 0 {
		return nil, errors.New("subscriptions_invoice_create returned no invoice id")
	}

	switch paymentMode {
	case "manual":
		if recordPayment {
			paymentOut, err := a.recordCheckoutPayment(ctx, pid, invoiceID, price, args)
			if err != nil {
				return nil, err
			}
			out["payment"] = unwrapMap(paymentOut, "payment")
			out["invoice"] = unwrapMap(paymentOut, "invoice")
		}
	case "payment_link", "stripe", "":
		linkOut, err := a.createPaymentLink(ctx, pid, invoiceID, args)
		if err != nil {
			return nil, err
		}
		out["payment_link"] = linkOut
	case "setup_session":
		setupOut, err := a.createPaymentSetupSession(ctx, pid, billingCustomerID, args)
		if err != nil {
			return nil, err
		}
		out["setup_session"] = unwrapMap(setupOut, "setup_session")
		if url := strArg(setupOut, "url"); url != "" {
			out["url"] = url
		}
	case "none":
	default:
		return nil, fmt.Errorf("unsupported payment_mode %q", paymentMode)
	}

	accountArgs := copyMap(args)
	accountArgs["skip_fulfillment"] = true
	accountArgs["customer_id"] = customer.ID
	accountArgs["billing_customer_id"] = billingCustomerID
	accountArgs["subscription_id"] = subID
	accountArgs["metadata"] = mergeMetadata(args["metadata"], map[string]any{
		"billing_customer_id": billingCustomerID,
		"subscription_id":     subID,
		"invoice_id":          invoiceID,
		"checkout_status":     map[bool]string{true: "paid", false: "awaiting_payment"}[paidNow],
	})
	acct, err := a.createAccount(ctx, accountArgs)
	if err != nil {
		return nil, err
	}
	if !paidNow {
		acct, err = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusPastDue, "")
		if err != nil {
			return nil, err
		}
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "checkout.awaiting_payment", actor(args), map[string]any{"invoice_id": invoiceID, "subscription_id": subID})
	} else {
		if _, err := a.runFulfillment(ctx, pid, acct, "account_active"); err != nil {
			_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusFailed, err.Error())
			return nil, err
		}
		acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	}
	out["account"] = acct
	if paidNow {
		out["status"] = "active"
	} else {
		out["status"] = "awaiting_payment"
	}
	return out, nil
}

type checkoutPrice struct {
	ProductID       int64
	PriceID         int64
	Title           string
	UnitAmountCents int64
	Currency        string
	Interval        string
	IntervalCount   int64
}

func (p checkoutPrice) asMap() map[string]any {
	return map[string]any{
		"catalog_product_id": p.ProductID,
		"catalog_price_id":   p.PriceID,
		"title":              p.Title,
		"unit_amount_cents":  p.UnitAmountCents,
		"currency":           p.Currency,
		"interval":           p.Interval,
		"interval_count":     p.IntervalCount,
	}
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
		"name":        customer.Name,
		"metadata":    map[string]any{"source": "saas", "saas_customer_id": customer.ID},
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
		ProductID:       int64PtrValue(plan.CatalogProductID),
		PriceID:         int64PtrValue(plan.CatalogPriceID),
		Title:           firstNonEmpty(strArg(args, "title"), plan.Name),
		UnitAmountCents: int64Arg(args, "unit_amount_cents"),
		Currency:        strings.ToUpper(firstNonEmpty(strArg(args, "currency"), "USD")),
		Interval:        firstNonEmpty(strArg(args, "interval"), "month"),
		IntervalCount:   firstNonZero(int64Arg(args, "interval_count"), 1),
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
	}
	if price.UnitAmountCents == 0 {
		return price, errors.New("paid SaaS checkout requires catalog_price_id or unit_amount_cents")
	}
	if price.ProductID == 0 && price.PriceID != 0 {
		return price, errors.New("catalog price response missing product_id")
	}
	return price, nil
}

func (a *App) createSubscription(ctx *sdk.AppCtx, pid string, customer *Customer, billingCustomerID int64, plan *Plan, price checkoutPrice, status, periodStart, periodEnd string, args map[string]any) (map[string]any, error) {
	input := map[string]any{
		"_project_id":          pid,
		"customer_id":          billingCustomerID,
		"customer_email":       customer.Email,
		"customer_name":        customer.Name,
		"kind":                 "saas",
		"status":               status,
		"billing_provider":     firstNonEmpty(strArg(args, "provider"), "local"),
		"currency":             price.Currency,
		"interval":             price.Interval,
		"interval_count":       price.IntervalCount,
		"current_period_start": periodStart,
		"current_period_end":   periodEnd,
		"next_renewal_at":      periodEnd,
		"source":               "saas",
		"source_ref":           plan.Key,
		"metadata":             map[string]any{"saas_plan_key": plan.Key, "saas_customer_id": customer.ID},
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
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_create", input, &out); err != nil {
		return nil, fmt.Errorf("create subscription: %w", err)
	}
	return out, nil
}

func (a *App) createSubscriptionInvoice(ctx *sdk.AppCtx, pid string, subID int64, periodStart, periodEnd string, args map[string]any) (map[string]any, error) {
	input := map[string]any{
		"_project_id":        pid,
		"subscription_id":    subID,
		"period_start":       periodStart,
		"period_end":         periodEnd,
		"include_flat":       true,
		"include_metered":    true,
		"finalize":           true,
		"provider":           firstNonEmpty(strArg(args, "provider"), "local"),
		"invoice_zero_usage": true,
		"metadata":           map[string]any{"source": "saas_checkout"},
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("subscriptions", "subscriptions_invoice_create", input, &out); err != nil {
		return nil, fmt.Errorf("create subscription invoice: %w", err)
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
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "invoices_send_payment_link", input, &out); err != nil {
		return nil, fmt.Errorf("create payment link: %w", err)
	}
	return out, nil
}

func (a *App) createPaymentSetupSession(ctx *sdk.AppCtx, pid string, billingCustomerID int64, args map[string]any) (map[string]any, error) {
	input := map[string]any{"_project_id": pid, "customer_id": billingCustomerID, "set_default": true}
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

func (a *App) recordCheckoutPayment(ctx *sdk.AppCtx, pid string, invoiceID int64, price checkoutPrice, args map[string]any) (map[string]any, error) {
	amount := firstNonZero(int64Arg(args, "amount_cents"), price.UnitAmountCents)
	input := map[string]any{
		"_project_id":  pid,
		"invoice_id":   invoiceID,
		"amount_cents": amount,
		"method":       firstNonEmpty(strArg(args, "manual_payment_method"), "wire"),
		"notes":        firstNonEmpty(strArg(args, "payment_notes"), "SaaS checkout payment"),
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("billing", "payments_record", input, &out); err != nil {
		return nil, fmt.Errorf("record payment: %w", err)
	}
	return out, nil
}

func (a *App) runFulfillment(ctx *sdk.AppCtx, pid string, acct *Account, event string) ([]*FulfillmentRun, error) {
	event = normalizeFulfillmentEvent(event)
	if event == "" {
		return nil, errors.New("fulfillment event required")
	}
	actions, err := dbPlanActions(ctx.AppDB(), pid, acct.PlanKey, event, true)
	if err != nil || len(actions) == 0 {
		return nil, err
	}
	customer, err := dbCustomerGet(ctx.AppDB(), pid, acct.CustomerID)
	if err != nil {
		return nil, err
	}
	plan, err := dbPlanGet(ctx.AppDB(), pid, acct.PlanKey)
	if err != nil {
		return nil, err
	}
	var runs []*FulfillmentRun
	for _, action := range actions {
		if !action.Enabled {
			continue
		}
		args, _ := expandFulfillmentValue(mapFromAny(action.Args), fulfillmentTemplateContext(pid, acct, customer, plan)).(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		args["_project_id"] = pid
		var out map[string]any
		callErr := ctx.PlatformAPI().CallAppResult(action.AppName, action.ToolName, args, &out)
		status := "succeeded"
		errText := ""
		if callErr != nil {
			status = "failed"
			errText = callErr.Error()
		}
		run, recErr := dbFulfillmentRunInsert(ctx.AppDB(), pid, acct.ID, &action, status, args, out, errText)
		if recErr != nil {
			return runs, recErr
		}
		runs = append(runs, run)
		if callErr != nil {
			_ = recordEvent(ctx.AppDB(), pid, acct.ID, "fulfillment.failed", action.AppName, map[string]any{"event": event, "tool": action.ToolName, "error": errText})
			switch action.FailureMode {
			case "ignore":
				continue
			case "mark_degraded":
				_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, acct.Status, errText)
				continue
			default:
				return runs, fmt.Errorf("fulfillment %s.%s failed: %w", action.AppName, action.ToolName, callErr)
			}
		}
		if err := a.applyFulfillmentStore(ctx, pid, acct, action, out); err != nil {
			return runs, err
		}
		_ = recordEvent(ctx.AppDB(), pid, acct.ID, "fulfillment.succeeded", action.AppName, map[string]any{"event": event, "tool": action.ToolName, "run_id": run.ID})
		acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	}
	return runs, nil
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
		value, ok := valueAtPath(out, sourcePath)
		if !ok {
			return fmt.Errorf("fulfillment store source %q not found", sourcePath)
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
	case "subscription.past_due":
		return "account_past_due"
	case "subscription.paused", "subscription.cancelled", "subscription.ended", "cancelled":
		return "account_cancelled"
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
		if err := ctx.PlatformAPI().CallAppResult("entitlements", "entitlement_grants_create", input, &out); err != nil {
			return fmt.Errorf("grant %s: %w", f.FeatureKey, err)
		}
	}
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
	acct, err := dbAccountSetStatus(ctx.AppDB(), pid, strArg(args, "account_id"), status, "")
	if err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), pid, acct.ID, event, actor(args), nil)
	if fe := fulfillmentEventFromLifecycle(event, status); fe != "" {
		if _, err := a.runFulfillment(ctx, pid, acct, fe); err != nil {
			_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, StatusFailed, err.Error())
			return nil, err
		}
		acct, _ = dbAccountGet(ctx.AppDB(), pid, acct.ID)
	}
	return map[string]any{"account": acct}, nil
}

func (a *App) handleSubscriptionLifecycle(ctx *sdk.AppCtx, event sdk.Event) error {
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
	body := map[string]any{
		"_project_id":         firstNonEmpty(event.ProjectID, projectID(ctx, nil)),
		"subscription_id":     subID,
		"subscription_status": status,
		"actor":               "subscription",
	}
	_, err := a.toolSubscriptionSync(ctx, body)
	return err
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
	_, err = db.Exec(`
		INSERT INTO saas_plans
			(project_id, key, name, billing_mode, catalog_product_id, catalog_price_id, subscription_required, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, key) DO UPDATE SET
			name=excluded.name,
			billing_mode=excluded.billing_mode,
			catalog_product_id=excluded.catalog_product_id,
			catalog_price_id=excluded.catalog_price_id,
			subscription_required=excluded.subscription_required,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, key, name, firstNonEmpty(strArg(args, "billing_mode"), "free"),
		nullableInt64(int64Arg(args, "catalog_product_id")), nullableInt64(int64Arg(args, "catalog_price_id")),
		boolInt(boolArg(args, "subscription_required")), jsonOrEmpty(args["metadata"], "{}"))
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
	defer rows.Close()
	var out []*Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		if err := hydratePlan(db, p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	return `SELECT project_id, key, name, billing_mode, catalog_product_id, catalog_price_id, subscription_required, metadata_json, created_at, updated_at FROM saas_plans`
}

func scanPlan(row rowScanner) (*Plan, error) {
	var p Plan
	var product, price sql.NullInt64
	var subReq int
	var meta string
	if err := row.Scan(&p.ProjectID, &p.Key, &p.Name, &p.BillingMode, &product, &price, &subReq, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if product.Valid {
		p.CatalogProductID = &product.Int64
	}
	if price.Valid {
		p.CatalogPriceID = &price.Int64
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
	rows, err := db.Query(`SELECT id, project_id, plan_key, feature_key, grant_type, metadata_json, created_at, updated_at FROM saas_plan_features WHERE project_id=? AND plan_key=? ORDER BY feature_key`, pid, planKey)
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
	rows, err := db.Query(`SELECT id, project_id, plan_key, feature_key, limit_value, reset_interval, metadata_json, created_at, updated_at FROM saas_plan_limits WHERE project_id=? AND plan_key=? ORDER BY feature_key`, pid, planKey)
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
	rows, err := db.Query(`SELECT id, project_id, plan_key, app_name, tool_name, feature_prefix, metadata_json, created_at, updated_at FROM saas_usage_sources WHERE project_id=? AND plan_key=? ORDER BY app_name, tool_name`, pid, planKey)
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
	enabled := 1
	if _, ok := args["enabled"]; ok {
		enabled = boolInt(boolArg(args, "enabled"))
	}
	res, err := db.Exec(`
		INSERT INTO saas_plan_actions
			(project_id, plan_key, event, app_name, tool_name, args_json, store_json, failure_mode, enabled, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, planKey, event, app, tool, jsonOrEmpty(args["args"], "{}"), jsonOrEmpty(args["store"], "{}"), failureMode, enabled, jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbPlanActionGet(db, pid, id)
}

func dbPlanActionGet(db *sql.DB, pid string, id int64) (*PlanAction, error) {
	rows, err := db.Query(`
		SELECT id, project_id, plan_key, event, app_name, tool_name, args_json, store_json, failure_mode, enabled, metadata_json, created_at, updated_at
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
	where := []string{"project_id=?", "plan_key=?"}
	vals := []any{pid, planKey}
	if event != "" {
		where = append(where, "event=?")
		vals = append(vals, event)
	}
	if enabledOnly {
		where = append(where, "enabled=1")
	}
	rows, err := db.Query(`
		SELECT id, project_id, plan_key, event, app_name, tool_name, args_json, store_json, failure_mode, enabled, metadata_json, created_at, updated_at
		FROM saas_plan_actions WHERE `+strings.Join(where, " AND ")+` ORDER BY id`, vals...)
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
	var args, store, meta string
	if err := row.Scan(&a.ID, &a.ProjectID, &a.PlanKey, &a.Event, &a.AppName, &a.ToolName, &args, &store, &a.FailureMode, &enabled, &meta, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.Args = json.RawMessage(args)
	a.Store = json.RawMessage(store)
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

func dbAccountInsert(db *sql.DB, a *Account) error {
	_, err := db.Exec(`
		INSERT INTO saas_accounts
			(id, project_id, customer_id, auth_org_id, auth_user_id, subscription_id, slug, owner_email, plan_key, status, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.CustomerID, a.AuthOrgID, a.AuthUserID, a.SubscriptionID, a.Slug, a.OwnerEmail, a.PlanKey, a.Status, stringOrJSON(a.Metadata, "{}"))
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
	_, err := db.Exec(`
		INSERT INTO saas_usage_snapshots
			(project_id, account_id, customer_id, source_app, feature_key, quantity, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, account_id, source_app, feature_key) DO UPDATE SET
			quantity=excluded.quantity,
			customer_id=excluded.customer_id,
			metadata_json=excluded.metadata_json,
			observed_at=CURRENT_TIMESTAMP,
			updated_at=CURRENT_TIMESTAMP`,
		pid, acct.ID, acct.CustomerID, firstNonEmpty(source, "manual"), feature, qty, jsonOrEmpty(meta, "{}"))
	return err
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

func recordEvent(db *sql.DB, pid, accountID, eventType, actor string, payload any) error {
	_, err := db.Exec(`INSERT INTO saas_events (project_id, account_id, event_type, actor, payload_json) VALUES (?, ?, ?, ?, ?)`,
		pid, accountID, eventType, firstNonEmpty(actor, "system"), jsonOrEmpty(payload, "{}"))
	return err
}

func dbFulfillmentRunInsert(db *sql.DB, pid, accountID string, action *PlanAction, status string, input, output any, errText string) (*FulfillmentRun, error) {
	res, err := db.Exec(`
		INSERT INTO saas_fulfillment_runs
			(project_id, account_id, plan_action_id, event, app_name, tool_name, status, input_json, output_json, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, accountID, action.ID, action.Event, action.AppName, action.ToolName, status, jsonOrEmpty(input, "{}"), jsonOrEmpty(output, "{}"), errText)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbFulfillmentRunGet(db, pid, id)
}

func dbFulfillmentRunGet(db *sql.DB, pid string, id int64) (*FulfillmentRun, error) {
	var r FulfillmentRun
	var input, output string
	err := db.QueryRow(`
		SELECT id, project_id, account_id, plan_action_id, event, app_name, tool_name, status, input_json, output_json, error, created_at, updated_at
		FROM saas_fulfillment_runs WHERE project_id=? AND id=?`, pid, id).
		Scan(&r.ID, &r.ProjectID, &r.AccountID, &r.PlanActionID, &r.Event, &r.AppName, &r.ToolName, &r.Status, &input, &output, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.Input = json.RawMessage(input)
	r.Output = json.RawMessage(output)
	return &r, nil
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
		args := map[string]any{"status": r.URL.Query().Get("status"), "plan_key": r.URL.Query().Get("plan_key"), "customer_id": r.URL.Query().Get("customer_id")}
		accounts, err := dbAccountList(ctx.AppDB(), pid, args)
		handleJSONOrErr(w, map[string]any{"accounts": accounts, "count": len(accounts)}, err)
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

func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
