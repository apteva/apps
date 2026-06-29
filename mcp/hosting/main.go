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
	if err := seedPlans(ctx.AppDB(), configString(ctx, "default_image", "apteva:latest")); err != nil {
		return err
	}
	ctx.Logger().Info("hosting mounted", "version", "1.0.1", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "health-poll",
		Schedule: "@every 60s",
		Run: func(context.Context, *sdk.AppCtx) error {
			return nil
		},
	}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/plans", Handler: a.handlePlans},
		{Pattern: "/customers", Handler: a.handleCustomers},
		{Pattern: "/tenants", Handler: a.handleTenants},
		{Pattern: "/tenants/", Handler: a.handleTenantItem},
		{Pattern: "/usage", Handler: a.handleUsage},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "hosting_plan_list", Description: "List hosting plans.", InputSchema: schemaObject(nil, nil), Handler: a.toolPlanList},
		{Name: "hosting_customer_create", Description: "Find or create a hosting customer by email.", InputSchema: schemaObject(map[string]any{"email": strSchema(), "name": strSchema(), "billing_customer_id": intSchema(), "metadata": objSchema()}, []string{"email"}), Handler: a.toolCustomerCreate},
		{Name: "hosting_tenant_create", Description: "Provision a hosted Apteva tenant container and default hostname.", InputSchema: schemaObject(map[string]any{
			"customer_id": intSchema(), "customer_email": strSchema(), "customer_name": strSchema(),
			"owner_email": strSchema(), "slug": strSchema(), "plan_key": strSchema(),
			"subscription_id": intSchema(), "apteva_version": strSchema(), "image": strSchema(), "metadata": objSchema(),
		}, []string{"owner_email", "slug"}), Handler: a.toolTenantCreate},
		{Name: "hosting_tenant_get", Description: "Fetch one hosted tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantGet},
		{Name: "hosting_tenant_list", Description: "List hosted tenants.", InputSchema: schemaObject(map[string]any{"customer_id": intSchema(), "status": strSchema(), "plan_key": strSchema()}, nil), Handler: a.toolTenantList},
		{Name: "hosting_tenant_suspend", Description: "Stop and suspend a tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantSuspend},
		{Name: "hosting_tenant_resume", Description: "Start a suspended/stopped tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantResume},
		{Name: "hosting_tenant_restart", Description: "Restart a hosted tenant.", InputSchema: tenantIDSchema(), Handler: a.toolTenantRestart},
		{Name: "hosting_tenant_delete", Description: "Destroy a hosted tenant.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "delete_volumes": boolSchema()}, []string{"tenant_id"}), Handler: a.toolTenantDelete},
		{Name: "hosting_tenant_logs", Description: "Tail hosted tenant logs.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "tail": intSchema()}, []string{"tenant_id"}), Handler: a.toolTenantLogs},
		{Name: "hosting_tenant_health", Description: "Probe hosted tenant health.", InputSchema: tenantIDSchema(), Handler: a.toolTenantHealth},
		{Name: "hosting_usage_get", Description: "Return usage totals.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "customer_id": intSchema(), "feature_key": strSchema()}, nil), Handler: a.toolUsageGet},
		{Name: "hosting_subscription_sync", Description: "Apply a subscription status to a tenant.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "subscription_status": strSchema(), "actor": strSchema()}, []string{"tenant_id", "subscription_status"}), Handler: a.toolSubscriptionSync},
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

type Plan struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	BillingMode string          `json:"billing_mode"`
	PriceCents  *int64          `json:"price_cents,omitempty"`
	Interval    string          `json:"interval,omitempty"`
	Image       string          `json:"image"`
	CPU         float64         `json:"cpu"`
	MemoryMB    int64           `json:"memory_mb"`
	StorageMB   int64           `json:"storage_mb"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	Limits      []PlanLimit     `json:"limits,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
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

func (a *App) toolTenantCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := a.provisionTenant(ctx, args)
	if err != nil {
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
		return map[string]any{"tenant": t}, nil
	case "paused", "cancelled", "ended":
		return a.toolTenantSuspend(ctx, args)
	default:
		return nil, fmt.Errorf("unsupported subscription_status %q", status)
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
	image := firstNonEmpty(strArg(args, "image"), plan.Image, configString(ctx, "default_image", "apteva:latest"), "apteva:latest")
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
		Metadata:        jsonRaw(args["metadata"]),
	}
	if tenant.OwnerEmail == "" {
		return nil, errors.New("owner_email required")
	}
	if err := dbTenantInsert(ctx.AppDB(), tenant); err != nil {
		return nil, err
	}
	_ = recordEvent(ctx.AppDB(), tenant.ID, "provisioning.started", "tool", map[string]any{"plan_key": plan.Key})

	workload, err := createContainer(ctx, tenant, plan)
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
	return dbTenantGet(ctx.AppDB(), tenant.ID)
}

func createContainer(ctx *sdk.AppCtx, tenant *Tenant, plan *Plan) (*containerWorkload, error) {
	env := map[string]any{
		"PORT":               "5280",
		"APTEVA_BIND":        "0.0.0.0",
		"DB_PATH":            "/data/apteva.db",
		"DATA_DIR":           "/data",
		"APTEVA_APPS_CACHE":  "/data/apps",
		"CORE_CMD":           "/usr/local/bin/apteva-core",
		"APTEVA_HOSTED":      "1",
		"APTEVA_HOSTING_ID":  tenant.ID,
		"APTEVA_OWNER_EMAIL": tenant.OwnerEmail,
	}
	if tenant.AptevaVersion != "" {
		env["APTEVA_VERSION"] = tenant.AptevaVersion
	}
	input := map[string]any{
		"name":           "hosting-" + tenant.Slug,
		"blueprint_slug": "apteva",
		"image":          tenant.Image,
		"env":            env,
		"resources": map[string]any{
			"cpu":       plan.CPU,
			"memory_mb": plan.MemoryMB,
		},
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

func seedPlans(db *sql.DB, defaultImage string) error {
	plans := []Plan{
		{Key: "free", Name: "Free", BillingMode: "free", Image: defaultImage, CPU: 0.5, MemoryMB: 512, StorageMB: 512},
		{Key: "starter", Name: "Starter", BillingMode: "paid", Image: defaultImage, CPU: 1, MemoryMB: 1024, StorageMB: 10240},
		{Key: "pro", Name: "Pro", BillingMode: "paid", Image: defaultImage, CPU: 2, MemoryMB: 2048, StorageMB: 51200},
	}
	limits := map[string]map[string]int64{
		"free":    {"hosting.tenants": 1, "hosting.custom_domains": 0, "containers.storage_mb": 512},
		"starter": {"hosting.tenants": 1, "hosting.custom_domains": 1, "containers.storage_mb": 10240},
		"pro":     {"hosting.tenants": 5, "hosting.custom_domains": 10, "containers.storage_mb": 51200},
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
	body := map[string]any{"email": email, "name": strArg(args, "customer_name")}
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

func dbPlanList(db *sql.DB) ([]*Plan, error) {
	rows, err := db.Query(`SELECT key, name, billing_mode, price_cents, COALESCE(interval,''), image, cpu, memory_mb, storage_mb, metadata_json, created_at, updated_at FROM hosting_plans ORDER BY key`)
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
		limits, err := dbPlanLimits(db, p.Key)
		if err != nil {
			return nil, err
		}
		p.Limits = limits
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbPlanGet(db *sql.DB, key string) (*Plan, error) {
	p, err := scanPlan(db.QueryRow(`SELECT key, name, billing_mode, price_cents, COALESCE(interval,''), image, cpu, memory_mb, storage_mb, metadata_json, created_at, updated_at FROM hosting_plans WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Limits, err = dbPlanLimits(db, key)
	return p, err
}

type rowScanner interface{ Scan(...any) error }

func scanPlan(row rowScanner) (*Plan, error) {
	var p Plan
	var price sql.NullInt64
	var meta string
	if err := row.Scan(&p.Key, &p.Name, &p.BillingMode, &price, &p.Interval, &p.Image, &p.CPU, &p.MemoryMB, &p.StorageMB, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if price.Valid {
		p.PriceCents = &price.Int64
	}
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
	_, err := db.Exec(`INSERT INTO hosting_usage_events (tenant_id, customer_id, feature_key, quantity, idempotency_key, metadata_json) VALUES (?, ?, ?, ?, ?, ?)`,
		nullStr(tenantID), customerID, feature, qty, nullStr(idem), jsonOrEmpty(meta, "{}"))
	if err != nil && strings.Contains(err.Error(), "ux_hosting_usage_idempotency") {
		return nil
	}
	return err
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

func strSchema() map[string]any  { return map[string]any{"type": "string"} }
func intSchema() map[string]any  { return map[string]any{"type": "integer"} }
func boolSchema() map[string]any { return map[string]any{"type": "boolean"} }
func objSchema() map[string]any  { return map[string]any{"type": "object"} }

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
