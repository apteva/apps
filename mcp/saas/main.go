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
	ctx.Logger().Info("saas mounted", "version", "0.1.0", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
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
		{Name: "saas_plan_usage_source_add", Description: "Register a live usage source for a SaaS plan.", InputSchema: schemaObject(map[string]any{"plan_key": strSchema(), "app_name": strSchema(), "tool_name": strSchema(), "feature_prefix": strSchema(), "metadata": objSchema()}, []string{"plan_key", "app_name", "tool_name"}), Handler: a.toolPlanUsageSourceAdd},
		{Name: "saas_customer_create", Description: "Find or create a SaaS customer by email.", InputSchema: schemaObject(map[string]any{"email": strSchema(), "name": strSchema(), "billing_customer_id": intSchema(), "auth_user_id": intSchema(), "metadata": objSchema()}, []string{"email"}), Handler: a.toolCustomerCreate},
		{Name: "saas_account_create", Description: "Create a SaaS account and apply plan access.", InputSchema: schemaObject(map[string]any{
			"customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(), "owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(),
			"auth_org_id": intSchema(), "auth_user_id": intSchema(), "subscription_id": intSchema(), "create_owner_user": boolSchema(), "send_password_reset": boolSchema(), "metadata": objSchema(),
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

type entitlementCheckResponse struct {
	Allowed bool `json:"allowed"`
}

type usageGauge struct {
	FeatureKey string         `json:"feature_key"`
	Quantity   int64          `json:"quantity"`
	Metadata   map[string]any `json:"metadata,omitempty"`
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
			var out usageSnapshotResponse
			input := map[string]any{
				"account_id":  acct.ID,
				"customer_id": acct.CustomerID,
				"auth_org_id": int64PtrValue(acct.AuthOrgID),
				"plan_key":    acct.PlanKey,
			}
			if err := ctx.PlatformAPI().CallAppResult(src.AppName, src.ToolName, input, &out); err != nil {
				_, _ = dbAccountSetStatus(ctx.AppDB(), pid, acct.ID, acct.Status, err.Error())
				_ = recordEvent(ctx.AppDB(), pid, acct.ID, "usage_sync.failed", src.AppName, map[string]any{"tool": src.ToolName, "error": err.Error()})
				return nil, err
			}
			for _, gauge := range out.Usage {
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
	allowedStatus := acct.Status == StatusActive || acct.Status == StatusPastDue
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
	_, _ = a.toolUsageSync(ctx, map[string]any{"_project_id": pid, "account_id": acct.ID})
	return dbAccountGet(ctx.AppDB(), pid, acct.ID)
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
	_, err = db.Exec(`
		INSERT INTO saas_usage_sources (project_id, plan_key, app_name, tool_name, feature_prefix, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, plan_key, app_name, tool_name) DO UPDATE SET
			feature_prefix=excluded.feature_prefix,
			metadata_json=excluded.metadata_json,
			updated_at=CURRENT_TIMESTAMP`,
		pid, planKey, app, tool, strArg(args, "feature_prefix"), jsonOrEmpty(args["metadata"], "{}"))
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
	body := map[string]any{"email": email, "name": strArg(args, "customer_name"), "auth_user_id": int64Arg(args, "auth_user_id")}
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
