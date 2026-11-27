// Hosting app: tenants on containers and machines.
//
// Hosting gives each customer a container or a machine and runs its
// lifecycle: set up, grant access, suspend, resume, clean and release. It
// sells nothing and watches no payments. Callers (SaaS plan actions, a
// storefront, an operator) decide when things happen; Hosting makes every
// call safe to repeat and safe to run against real hardware.
package main

import (
	"context"
	"errors"
	"os"
	"sync"

	_ "embed"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

const appVersion = "2.4.1"

type App struct {
	// quietOffline holds machines marked offline because Instances could not
	// be reached (not because it reported them down): no alert went out, so
	// none goes out when they come back either.
	quietMu      sync.Mutex
	quietOffline map[int64]bool
}

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
	image := configString(ctx, "default_image", "ghcr.io/apteva/apteva:latest")
	if err := seedProducts(ctx.AppDB(), image); err != nil {
		return err
	}
	if err := seedProfiles(ctx.AppDB(), image); err != nil {
		return err
	}
	ctx.Logger().Info("hosting mounted", "version", appVersion, "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route           { return a.routes() }
func main()                                      { sdk.Run(&App{}) }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "hosting-runner",
		Schedule: "@every 1m",
		Run: func(_ context.Context, ctx *sdk.AppCtx) error {
			if ctx == nil {
				ctx = globalCtx
			}
			return a.tick(ctx)
		},
	}}
}

// tick is the 60-second sweep: expire holds, refresh machine status, hand
// freed machines to waiting tenants, and resume due or interrupted runs.
func (a *App) tick(ctx *sdk.AppCtx) error {
	if ctx == nil {
		return nil
	}
	a.expireReservations(ctx)
	a.refreshMachines(ctx)
	a.assignWaitingTenants(ctx)
	a.alertStuckTenants(ctx)
	ids, err := dueOperationIDs(ctx.AppDB(), 25)
	if err != nil {
		return err
	}
	for _, id := range ids {
		kickOperation(a, ctx, id)
	}
	return nil
}

func (a *App) MCPTools() []sdk.Tool {
	tenantRef := map[string]any{"tenant_id": strSchema(), "external_ref": strSchema(), "actor": strSchema()}
	withRef := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range tenantRef {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	machineID := map[string]any{"machine_id": intSchema(), "actor": strSchema()}
	return []sdk.Tool{
		// Tenants
		{Name: "hosting_tenant_create", Description: "Create a tenant on a profile. Keyed by external_ref (the caller's id, e.g. a SaaS account id): repeating the call returns the same tenant. Container profiles are ready on return; machine profiles return status provisioning (or waiting_for_machine when no machine is free) and finish in the background. Customers waiting are served oldest first. With presale_limit set, waiting needs the caller's presale hold or room under the limit, else the call fails. Args: external_ref, profile, owner_email, reservation_id?, ssh_keys?, slug?, runtime_config?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"external_ref": strSchema(), "profile": strSchema(), "owner_email": strSchema(), "reservation_id": strSchema(),
				"ssh_keys": map[string]any{"type": "array", "items": strSchema()}, "slug": strSchema(),
				"runtime_config": objSchema(), "metadata": objSchema(), "apteva_version": strSchema(),
			}, []string{"external_ref", "profile", "owner_email"}), Handler: a.toolTenantCreate},
		{Name: "hosting_tenant_get", Description: "Fetch one tenant by tenant_id or external_ref, with its SSH access details, current run and recent events.", InputSchema: schemaObject(tenantRef, nil), Handler: a.toolTenantGet},
		{Name: "hosting_tenant_update", Description: "Update a tenant's external_ref, owner_email or metadata (merged). Use it to link a tenant migrated from 1.x to the caller's account id. Changing an external_ref that is already set needs confirm equal to the tenant id.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "external_ref": strSchema(), "owner_email": strSchema(), "metadata": objSchema(), "confirm": strSchema(), "actor": strSchema()}, []string{"tenant_id"}), Handler: a.toolTenantUpdate},
		{Name: "hosting_tenant_resolve", Description: "Settle a machine tenant left by a failure (quarantined machine, or stuck suspending/resuming/releasing/failed). action: resume, release, or force_clean (erase a validated macOS machine without revoking first). Needs reviewed=true and confirm equal to the tenant id.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "action": strSchema(), "reviewed": boolSchema(), "confirm": strSchema(), "actor": strSchema()}, []string{"tenant_id", "action", "reviewed", "confirm"}), Handler: a.toolTenantResolve},
		{Name: "hosting_tenant_list", Description: "List tenants. Filters: status, profile_key, backend, owner_email, external_ref, include_released.", InputSchema: schemaObject(map[string]any{"status": strSchema(), "profile_key": strSchema(), "backend": strSchema(), "owner_email": strSchema(), "external_ref": strSchema(), "include_released": boolSchema(), "limit": intSchema()}, nil), Handler: a.toolTenantList},
		{Name: "hosting_tenant_suspend", Description: "Suspend a tenant: stop its container, or remove SSH access to its machine. Never wipes. Repeating it is a no-op.", InputSchema: schemaObject(tenantRef, nil), Handler: a.toolTenantSuspend},
		{Name: "hosting_tenant_resume", Description: "Resume a suspended tenant: start its container, or restore SSH access. Repeating it is a no-op.", InputSchema: schemaObject(tenantRef, nil), Handler: a.toolTenantResume},
		{Name: "hosting_tenant_release", Description: "End a tenant now. Container: destroy it and its hostname. Pool machine: remove access, wipe, verify clean, back on sale. On-demand machine: destroy the instance (waits for a provider minimum allocation such as Scaleway Macs' 24 hours). Needs confirm equal to the tenant id. Repeating it is a no-op.", InputSchema: schemaObject(withRef(map[string]any{"delete_volumes": boolSchema(), "confirm": strSchema(), "actor": strSchema()}), []string{"confirm"}), Handler: a.toolTenantRelease},
		{Name: "hosting_tenant_restart", Description: "Restart a tenant's container or reboot its machine.", InputSchema: schemaObject(tenantRef, nil), Handler: a.toolTenantRestart},
		{Name: "hosting_tenant_reinstall", Description: "Erase and reinstall a macOS pool machine for the same tenant, then restore access. Destroys the customer's data. Needs confirm equal to the tenant id.", InputSchema: schemaObject(withRef(map[string]any{"confirm": strSchema(), "actor": strSchema()}), []string{"confirm"}), Handler: a.toolTenantReinstall},
		{Name: "hosting_tenant_access_keys_set", Description: "Replace a machine tenant's SSH public keys. An empty list removes access.", InputSchema: schemaObject(withRef(map[string]any{"ssh_keys": map[string]any{"type": "array", "items": strSchema()}}), []string{"ssh_keys"}), Handler: a.toolTenantAccessKeysSet},
		{Name: "hosting_tenant_logs", Description: "Container logs, or a machine tenant's run log and events.", InputSchema: schemaObject(withRef(map[string]any{"tail": intSchema()}), nil), Handler: a.toolTenantLogs},
		{Name: "hosting_tenant_health", Description: "Container health, or a machine's Instances status.", InputSchema: schemaObject(tenantRef, nil), Handler: a.toolTenantHealth},
		{Name: "hosting_tenant_usage", Description: "Runtime usage for a tenant: container usage gauges, or machine metrics from Instances. Suitable as a SaaS usage source.", InputSchema: schemaObject(tenantRef, nil), Handler: a.toolTenantUsage},

		// Capacity
		{Name: "hosting_availability", Description: "Whether a profile can take a new tenant now. For pool profiles: available = machines (free now, not promised to customers already waiting) + presale (orders still taken ahead of hardware under presale_limit), with presale_note.", InputSchema: schemaObject(map[string]any{"profile": strSchema()}, []string{"profile"}), Handler: a.toolAvailability},
		{Name: "hosting_reservation_create", Description: "Hold a pool machine during checkout. Holds a free machine when one is not already promised to a waiting customer; otherwise a presale hold (presale: true, no machine) while the profile's presale_limit has room. Repeating the call for the same profile and external_ref returns the same hold. Holds expire after reservation_ttl_minutes.", InputSchema: schemaObject(map[string]any{"profile": strSchema(), "external_ref": strSchema()}, []string{"profile", "external_ref"}), Handler: a.toolReservationCreate},
		{Name: "hosting_reservation_release", Description: "Release a held pool machine early.", InputSchema: schemaObject(map[string]any{"reservation_id": strSchema()}, []string{"reservation_id"}), Handler: a.toolReservationRelease},

		// Profiles and products
		{Name: "hosting_product_list", Description: "List what can run on a tenant: Apteva, Docker images, WordPress, dedicated machines.", InputSchema: schemaObject(nil, nil), Handler: a.toolProductList},
		{Name: "hosting_profile_list", Description: "List runtime profiles (where a tenant runs and what runs on it).", InputSchema: schemaObject(map[string]any{"include_archived": boolSchema()}, nil), Handler: a.toolProfileList},
		{Name: "hosting_profile_upsert", Description: "Create or update a runtime profile. backend: container, machine_pool (needs pool_key) or machine_on_demand (needs settings.provider and settings.server_type; optional region, image, platform, setup, provider_connection_id). presale_limit (machine_pool only): how many orders to take ahead of hardware; 0 sells free machines only, null removes the limit (unbounded waiting line). presale_note: free text shown with availability, e.g. ready within 10 days. presale_ready_days: the promise in days; a tenant that has to wait gets due_at = order time + this, and hosting.tenant.stuck fires when it passes.",
			InputSchema: schemaObject(map[string]any{
				"key": strSchema(), "name": strSchema(), "backend": strSchema(), "product_key": strSchema(), "pool_key": strSchema(), "image": strSchema(),
				"cpu": map[string]any{"type": "number"}, "memory_mb": intSchema(), "storage_mb": intSchema(), "settings": objSchema(), "status": strSchema(), "metadata": objSchema(),
				"presale_limit": intSchema(), "presale_note": strSchema(), "presale_ready_days": intSchema(),
			}, []string{"key", "backend", "product_key"}), Handler: a.toolProfileUpsert},

		// Pools
		{Name: "hosting_pool_list", Description: "List machine pools with machine counts per state.", InputSchema: schemaObject(nil, nil), Handler: a.toolPoolList},
		{Name: "hosting_pool_upsert", Description: "Create or update a machine pool. platform: macos or linux. New pools start with execution switched off.", InputSchema: schemaObject(map[string]any{"key": strSchema(), "name": strSchema(), "platform": strSchema(), "metadata": objSchema()}, []string{"key", "platform"}), Handler: a.toolPoolUpsert},
		{Name: "hosting_pool_set_execution", Description: "Switch command execution on or off for a pool. While off, no command runs on its machines and nothing from it is for sale.", InputSchema: schemaObject(map[string]any{"key": strSchema(), "enabled": boolSchema(), "actor": strSchema()}, []string{"key", "enabled"}), Handler: a.toolPoolSetExecution},

		// Machines
		{Name: "hosting_machine_add", Description: "Add an Instances host to a pool. It starts quarantined; run inspect, then mark it validated once its reset has been tested. The customer endpoint comes from Instances (a reverse-tunnel machine uses its public SSH port; enable_public_ssh=true turns that on) unless public_host is given.", InputSchema: schemaObject(map[string]any{"pool_key": strSchema(), "instance_id": intSchema(), "name": strSchema(), "serial": strSchema(), "management_user": strSchema(), "expected_os": strSchema(), "public_host": strSchema(), "public_port": intSchema(), "enable_public_ssh": boolSchema()}, []string{"pool_key", "instance_id"}), Handler: a.toolMachineAdd},
		{Name: "hosting_machine_list", Description: "List pool machines with state, online status, tenant and current run.", InputSchema: schemaObject(map[string]any{"pool_key": strSchema(), "state": strSchema()}, nil), Handler: a.toolMachineList},
		{Name: "hosting_machine_get", Description: "Fetch one pool machine with its recent runs.", InputSchema: schemaObject(machineID, []string{"machine_id"}), Handler: a.toolMachineGet},
		{Name: "hosting_machine_update", Description: "Update a machine's profile: serial, management_user, expected_os, public_host, public_port, name. A public_host set here wins over Instances; an empty public_host hands the endpoint back to Instances. Setting validated needs confirm equal to the machine id.", InputSchema: schemaObject(map[string]any{"machine_id": intSchema(), "name": strSchema(), "serial": strSchema(), "management_user": strSchema(), "expected_os": strSchema(), "public_host": strSchema(), "public_port": intSchema(), "validated": boolSchema(), "confirm": strSchema(), "actor": strSchema()}, []string{"machine_id"}), Handler: a.toolMachineUpdate},
		{Name: "hosting_machine_inspect", Description: "Check a machine without a tenant: install Hosting's SSH profile and reset helpers, run preflight, and confirm no customer account survives. A validated machine that passes goes back on sale.", InputSchema: schemaObject(machineID, []string{"machine_id"}), Handler: a.toolMachineInspect},
		{Name: "hosting_machine_clean", Description: "Erase a macOS machine without a tenant, verify it is clean and put it back on sale. Needs confirm equal to the machine id.", InputSchema: schemaObject(map[string]any{"machine_id": intSchema(), "confirm": strSchema(), "actor": strSchema()}, []string{"machine_id", "confirm"}), Handler: a.toolMachineClean},
		{Name: "hosting_machine_set_maintenance", Description: "Take an available machine off sale (maintenance=true) or put it back (false) without re-inspecting.", InputSchema: schemaObject(map[string]any{"machine_id": intSchema(), "maintenance": boolSchema(), "reason": strSchema(), "actor": strSchema()}, []string{"machine_id", "maintenance"}), Handler: a.toolMachineSetMaintenance},
		{Name: "hosting_machine_quarantine", Description: "Stop assigning a machine and stop every unfinished run on it.", InputSchema: schemaObject(map[string]any{"machine_id": intSchema(), "reason": strSchema(), "actor": strSchema()}, []string{"machine_id"}), Handler: a.toolMachineQuarantine},
		{Name: "hosting_machine_remove", Description: "Remove a machine without a tenant from its pool. The Instances host is not touched. Needs confirm equal to the machine id.", InputSchema: schemaObject(map[string]any{"machine_id": intSchema(), "confirm": strSchema(), "actor": strSchema()}, []string{"machine_id", "confirm"}), Handler: a.toolMachineRemove},

		// Runs
		{Name: "hosting_operation_get", Description: "Fetch one run with its steps and log.", InputSchema: schemaObject(map[string]any{"operation_id": strSchema()}, []string{"operation_id"}), Handler: a.toolOperationGet},
		{Name: "hosting_operation_list", Description: "List runs. Filters: tenant_id, machine_id, status.", InputSchema: schemaObject(map[string]any{"tenant_id": strSchema(), "machine_id": intSchema(), "status": strSchema(), "limit": intSchema()}, nil), Handler: a.toolOperationList},
		{Name: "hosting_operation_retry", Description: "Retry a failed run after reviewing the machine and the log. Needs reviewed=true.", InputSchema: schemaObject(map[string]any{"operation_id": strSchema(), "reviewed": boolSchema(), "actor": strSchema()}, []string{"operation_id", "reviewed"}), Handler: a.toolOperationRetry},
	}
}
