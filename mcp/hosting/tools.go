package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Tenants ─────────────────────────────────────────────────────────────

func tenantResult(db *sql.DB, t *Tenant, op *Operation) map[string]any {
	out := map[string]any{"tenant": t}
	if op != nil {
		out["operation"] = op
	} else if t != nil {
		if active, _ := activeOperationFor(db, t.ID, 0); active != nil {
			out["operation"] = active
		}
	}
	return out
}

func (a *App) toolTenantCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := a.createTenant(ctx, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, nil), nil
}

func (a *App) toolTenantGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, err
	}
	t.revealSession()
	out := tenantResult(db, t, nil)
	if events, err := dbEventList(db, t.ID, 20); err == nil {
		out["events"] = events
	}
	return out, nil
}

func (a *App) toolTenantList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := dbTenantList(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tenants": out, "count": len(out)}, nil
}

func (a *App) toolTenantSuspend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.tenantLifecycle(ctx, OpSuspend, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}

func (a *App) toolTenantResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.tenantLifecycle(ctx, OpResume, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}

func (a *App) toolTenantRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.releaseTenant(ctx, args)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return map[string]any{"tenant": nil, "already_released": true}, nil
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}

func (a *App) toolTenantRestart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.machineTenantOp(ctx, OpRestart, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}

func (a *App) toolTenantReinstall(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.machineTenantOp(ctx, OpReinstall, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}

func (a *App) toolTenantAccessKeysSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, ok := args["ssh_keys"]; !ok {
		return nil, errors.New("ssh_keys required (use an empty list to remove access)")
	}
	t, op, err := a.machineTenantOp(ctx, OpAccessKeys, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}

func (a *App) toolTenantLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, err
	}
	if t.Backend == BackendContainer || t.WorkloadID != "" {
		var out map[string]any
		if t.WorkloadID != "" {
			if err := ctx.PlatformAPI().CallAppResult("containers", "containers_logs", map[string]any{"workload_id": t.WorkloadID, "tail": intArg(args, "tail", 200)}, &out); err != nil {
				return nil, err
			}
		}
		if t.Backend == BackendContainer {
			return out, nil
		}
		ops, _ := dbOperationList(db, map[string]any{"tenant_id": t.ID, "limit": 10})
		events, _ := dbEventList(db, t.ID, 50)
		return map[string]any{"container": out, "operations": ops, "events": events}, nil
	}
	ops, err := dbOperationList(db, map[string]any{"tenant_id": t.ID, "limit": 10})
	if err != nil {
		return nil, err
	}
	events, err := dbEventList(db, t.ID, 50)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operations": ops, "events": events}, nil
}

func (a *App) toolTenantHealth(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, err
	}
	if t.Backend == BackendContainer {
		var out workloadResp
		if err := ctx.PlatformAPI().CallAppResult("containers", "containers_health", map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
			_, _ = db.Exec(`UPDATE hosting_tenants SET last_health_status='error', last_error=? WHERE id=?`, err.Error(), t.ID)
			return nil, err
		}
		_, _ = db.Exec(`UPDATE hosting_tenants SET last_health_status=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, firstNonEmpty(out.Workload.HealthStatus, out.Workload.Status), t.ID)
		t, _ = dbTenantGet(db, t.ID)
		return map[string]any{"tenant": t, "workload": out.Workload}, nil
	}
	instanceID := t.InstanceID
	if t.MachineID > 0 {
		if m, err := dbMachineGet(db, t.MachineID); err == nil && m != nil {
			instanceID = m.InstanceID
			if m.managed() {
				return a.deviceHealth(ctx, t, m)
			}
		}
	}
	if instanceID == 0 {
		return map[string]any{"tenant": t, "instance": nil}, nil
	}
	inst, err := instanceGet(ctx, ctx.CurrentProject(), instanceID)
	health := "unknown"
	if err == nil {
		health = map[bool]string{true: "ok", false: firstNonEmpty(map[bool]string{true: "disconnected"}[inst.Status == "ready"], inst.Status)}[inst.online()]
	} else {
		health = "error"
	}
	_, _ = db.Exec(`UPDATE hosting_tenants SET last_health_status=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, health, t.ID)
	t, _ = dbTenantGet(db, t.ID)
	out := map[string]any{"tenant": t, "instance": inst}
	if err != nil {
		out["error"] = err.Error()
	}
	return out, nil
}

func (a *App) toolTenantUsage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "backend": t.Backend}
	if t.WorkloadID != "" {
		usage, err := callAppMap(ctx, "containers", "containers_usage_get", withProject(ctx.CurrentProject(), map[string]any{"workload_id": t.WorkloadID}))
		if err != nil {
			return nil, err
		}
		out["container"] = usage
	}
	instanceID := t.InstanceID
	if t.MachineID > 0 {
		if m, err := dbMachineGet(db, t.MachineID); err == nil && m != nil {
			instanceID = m.InstanceID
		}
	}
	if instanceID > 0 {
		metrics, err := callAppMap(ctx, "instances", "instance_metrics", withProject(ctx.CurrentProject(), map[string]any{"id": instanceID}))
		if err != nil {
			out["machine_error"] = err.Error()
		} else {
			out["machine"] = metrics
		}
	}
	return out, nil
}

// ─── Capacity ────────────────────────────────────────────────────────────

func (a *App) toolAvailability(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	p, err := requireProfile(db, strArg(args, "profile"))
	if err != nil {
		return nil, err
	}
	out, err := availability(db, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"availability": out}, nil
}

func (a *App) toolReservationCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.createReservation(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"reservation": r}, nil
}

func (a *App) toolReservationRelease(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	r, err := a.releaseReservation(ctx, strArg(args, "reservation_id"), "released")
	if err != nil {
		return nil, err
	}
	return map[string]any{"reservation": r}, nil
}

// ─── Profiles and products ───────────────────────────────────────────────

func (a *App) toolProductList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	products, err := dbProductList(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	return map[string]any{"products": products, "count": len(products)}, nil
}

func (a *App) toolProfileList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profiles, err := dbProfileList(ctx.AppDB(), boolArg(args, "include_archived"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"profiles": profiles, "count": len(profiles)}, nil
}

var keyRE = slugRE

func (a *App) toolProfileUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	key := strings.ToLower(strings.TrimSpace(strArg(args, "key")))
	if !keyRE.MatchString(key) {
		return nil, errors.New("key must be lowercase letters, numbers and dashes")
	}
	existing, err := dbProfileGet(db, key)
	if err != nil {
		return nil, err
	}
	p := &Profile{Key: key, Status: "active", CPU: 1, MemoryMB: 1024, StorageMB: 1024, Settings: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`)}
	if existing != nil {
		p = existing
	}
	p.Name = firstNonEmpty(strArg(args, "name"), p.Name, key)
	p.Backend = firstNonEmpty(strArg(args, "backend"), p.Backend)
	p.ProductKey = firstNonEmpty(strArg(args, "product_key"), p.ProductKey)
	if v, ok := args["pool_key"]; ok {
		p.PoolKey = strFromAny(v)
	}
	if v, ok := args["image"]; ok {
		p.Image = strFromAny(v)
	}
	if v, ok := args["cpu"]; ok {
		if f, err := strconv.ParseFloat(strFromAny(v), 64); err == nil && f > 0 {
			p.CPU = f
		}
	}
	if v := int64Arg(args, "memory_mb"); v > 0 {
		p.MemoryMB = v
	}
	if v := int64Arg(args, "storage_mb"); v > 0 {
		p.StorageMB = v
	}
	if _, ok := args["settings"]; ok {
		p.Settings = jsonRaw(mapArg(args, "settings"))
	}
	if _, ok := args["metadata"]; ok {
		p.Metadata = jsonRaw(mapArg(args, "metadata"))
	}
	if v := strArg(args, "status"); v != "" {
		p.Status = v
	}
	if v, ok := args["presale_limit"]; ok {
		// null removes the limit (unbounded waiting line, nothing sold ahead).
		if v == nil || strFromAny(v) == "" {
			p.PresaleLimit = nil
		} else {
			f, err := strconv.ParseFloat(strFromAny(v), 64)
			if err != nil || f < 0 || f != float64(int64(f)) {
				return nil, errors.New("presale_limit must be a whole number, 0 or more, or null to remove the limit")
			}
			n := int64(f)
			p.PresaleLimit = &n
		}
	}
	if v, ok := args["presale_ready_days"]; ok {
		// null removes the promise; waiting tenants keep the due_at they got.
		if v == nil || strFromAny(v) == "" {
			p.PresaleReadyDays = nil
		} else {
			f, err := strconv.ParseFloat(strFromAny(v), 64)
			if err != nil || f < 1 || f != float64(int64(f)) {
				return nil, errors.New("presale_ready_days must be a whole number of days, 1 or more, or null to remove it")
			}
			n := int64(f)
			p.PresaleReadyDays = &n
		}
	}
	if v, ok := args["presale_note"]; ok {
		p.PresaleNote = strings.TrimSpace(strFromAny(v))
	}
	if err := validateProfile(db, p); err != nil {
		return nil, err
	}
	if err := dbProfileUpsert(db, p); err != nil {
		return nil, err
	}
	p, err = dbProfileGet(db, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"profile": p}, nil
}

func validateProfile(db *sql.DB, p *Profile) error {
	if p.Status != "active" && p.Status != "archived" {
		return errors.New("status must be active or archived")
	}
	if (p.PresaleLimit != nil || p.PresaleReadyDays != nil) && p.Backend != BackendMachinePool {
		return errors.New("presale_limit and presale_ready_days are only for machine_pool profiles")
	}
	product, err := dbProductGet(db, p.ProductKey)
	if err != nil {
		return err
	}
	if product == nil {
		return fmt.Errorf("product %q not found", p.ProductKey)
	}
	switch p.Backend {
	case BackendContainer:
		if !product.runsContainer() {
			return fmt.Errorf("product %q needs a machine backend", product.Key)
		}
		p.PoolKey = ""
	case BackendMachinePool:
		if p.PoolKey == "" {
			return errors.New("machine_pool profiles need pool_key")
		}
		if _, err := requirePool(db, p.PoolKey); err != nil {
			return err
		}
	case BackendMachineOnDemand:
		s := p.onDemand()
		if s.Provider == "" || s.ServerType == "" {
			return errors.New("machine_on_demand profiles need settings.provider and settings.server_type")
		}
		if s.Platform != "" && s.Platform != "linux" && s.Platform != "macos" {
			return errors.New("settings.platform must be linux or macos")
		}
		p.PoolKey = ""
	default:
		return fmt.Errorf("backend must be container, machine_pool or machine_on_demand, not %q", p.Backend)
	}
	return nil
}

// ─── Pools ───────────────────────────────────────────────────────────────

type poolSummary struct {
	*Pool
	Counts map[string]int `json:"counts"`
}

func machineCounts(db *sql.DB, poolKey string) (map[string]int, error) {
	counts := map[string]int{"available": 0, "reserved": 0, "assigned": 0, "maintenance": 0, "quarantined": 0, "offline": 0, "for_sale": 0, "busy": 0}
	rows, err := db.Query(`SELECT state, online FROM hosting_machines WHERE pool_key=?`, poolKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var online int
		if err := rows.Scan(&state, &online); err != nil {
			return nil, err
		}
		if online == 0 {
			counts["offline"]++
		}
		switch state {
		case MachineAvailable, MachineReserved, MachineMaintenance, MachineQuarantined:
			counts[state]++
		case MachineAssigned, MachineSuspended:
			counts["assigned"]++
		default: // preparing, cleaning
			counts["busy"]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var forSale int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosting_machines m WHERE m.pool_key=? AND `+forSaleCondition, poolKey).Scan(&forSale); err != nil {
		return nil, err
	}
	counts["for_sale"] = forSale
	var waiting int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosting_tenants t JOIN hosting_profiles p ON p.key=t.profile_key WHERE p.pool_key=? AND t.status='waiting_for_machine'`, poolKey).Scan(&waiting); err != nil {
		return nil, err
	}
	counts["waiting_tenants"] = waiting
	return counts, nil
}

func (a *App) toolPoolList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	db := ctx.AppDB()
	pools, err := dbPoolList(db)
	if err != nil {
		return nil, err
	}
	out := make([]poolSummary, 0, len(pools))
	for _, p := range pools {
		counts, err := machineCounts(db, p.Key)
		if err != nil {
			return nil, err
		}
		out = append(out, poolSummary{Pool: p, Counts: counts})
	}
	return map[string]any{"pools": out, "count": len(out)}, nil
}

func (a *App) toolPoolUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	key := strings.ToLower(strings.TrimSpace(strArg(args, "key")))
	if !keyRE.MatchString(key) {
		return nil, errors.New("key must be lowercase letters, numbers and dashes")
	}
	platform := strArg(args, "platform")
	managedBy := strings.TrimSpace(strArg(args, "managed_by"))
	switch managedBy {
	case "":
		if platform != "macos" && platform != "linux" {
			return nil, errors.New("platform must be macos or linux (android pools need managed_by=mobile)")
		}
	case managedByMobile:
		if platform == "" {
			platform = "android"
		}
		if platform != "android" {
			return nil, errors.New("a pool managed by mobile has platform android")
		}
	default:
		return nil, errors.New("managed_by must be empty (Instances hosts) or mobile (phones)")
	}
	existing, err := dbPoolGet(db, key)
	if err != nil {
		return nil, err
	}
	if existing != nil && (existing.Platform != platform || existing.ManagedBy != managedBy) {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM hosting_machines WHERE pool_key=?`, key).Scan(&n)
		if n > 0 {
			return nil, errors.New("cannot change the platform or managed_by of a pool that has machines")
		}
	}
	meta := "{}"
	if _, ok := args["metadata"]; ok {
		meta = jsonOrEmpty(mapArg(args, "metadata"), "{}")
	} else if existing != nil {
		meta = stringOrJSON(existing.Metadata, "{}")
	}
	if _, err := db.Exec(`INSERT INTO hosting_pools (key, name, platform, managed_by, script_set, metadata_json) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET name=excluded.name, platform=excluded.platform, managed_by=excluded.managed_by, script_set=excluded.script_set, metadata_json=excluded.metadata_json, updated_at=CURRENT_TIMESTAMP`,
		key, firstNonEmpty(strArg(args, "name"), key), platform, managedBy, platform, meta); err != nil {
		return nil, err
	}
	p, err := dbPoolGet(db, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pool": p}, nil
}

func (a *App) toolPoolSetExecution(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	p, err := requirePool(db, strArg(args, "key"))
	if err != nil {
		return nil, err
	}
	enabled := boolArg(args, "enabled")
	if _, err := db.Exec(`UPDATE hosting_pools SET execution_enabled=?, updated_at=CURRENT_TIMESTAMP WHERE key=?`, boolToInt(enabled), p.Key); err != nil {
		return nil, err
	}
	ctx.Logger().Info("hosting pool execution changed", "pool", p.Key, "enabled", enabled, "actor", actor(args))
	if enabled {
		a.assignWaitingTenants(ctx)
	}
	p, err = dbPoolGet(db, p.Key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pool": p}, nil
}

// ─── Machines ────────────────────────────────────────────────────────────

func (a *App) toolMachineAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	pool, err := requirePool(db, strArg(args, "pool_key"))
	if err != nil {
		return nil, err
	}
	if pool.ManagedBy != "" {
		return a.addManagedDevice(ctx, pool, args)
	}
	if strArg(args, "device_ref") != "" {
		return nil, fmt.Errorf("pool %s holds Instances hosts; device_ref is for pools managed by mobile", pool.Key)
	}
	instanceID := int64Arg(args, "instance_id")
	if instanceID <= 0 {
		return nil, errors.New("instance_id required (the local host, id 0, cannot be rented)")
	}
	inst, err := instanceGet(ctx, ctx.CurrentProject(), instanceID)
	if err != nil {
		return nil, fmt.Errorf("instances: %w", err)
	}
	if inst.Platform != "" && inst.Platform != pool.Platform {
		return nil, fmt.Errorf("instance %d is %s but pool %s is %s", instanceID, inst.Platform, pool.Key, pool.Platform)
	}
	manualHost := strings.TrimSpace(strArg(args, "public_host"))
	if inst.ConnectMode == "reverse_tunnel" && boolArg(args, "enable_public_ssh") && (inst.PublicSSH == nil || !inst.PublicSSH.Enabled) {
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("instances", "instance_public_ssh_set", withProject(ctx.CurrentProject(), map[string]any{"id": instanceID, "enabled": true}), &out); err != nil {
			return nil, fmt.Errorf("instance_public_ssh_set: %w", err)
		}
		if inst, err = instanceGet(ctx, ctx.CurrentProject(), instanceID); err != nil {
			return nil, fmt.Errorf("instances: %w", err)
		}
	}
	if inst.ConnectMode == "reverse_tunnel" && inst.PublicSSH == nil && manualHost == "" {
		return nil, fmt.Errorf("instance %d reaches this server through a reverse tunnel, and this Instances version has no public SSH for customers; upgrade Instances to 0.19 or later", instanceID)
	}
	host, port, issue := customerEndpoint(inst)
	source := "instances"
	if manualHost != "" {
		host, port, issue, source = manualHost, intArg(args, "public_port", inst.port()), "", "manual"
	}
	res, err := db.Exec(`INSERT INTO hosting_machines (pool_key, instance_id, name, state, state_reason, online, last_seen_at, public_host, public_port,
		connect_mode, endpoint_source, endpoint_issue, serial, expected_os, management_user)
		VALUES (?, ?, ?, 'quarantined', 'added; run inspect', ?, CASE WHEN ? THEN CURRENT_TIMESTAMP END, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pool.Key, instanceID, firstNonEmpty(strArg(args, "name"), inst.Name, fmt.Sprintf("instance-%d", instanceID)),
		boolToInt(inst.online()), inst.online(), host, port, inst.ConnectMode, source, issue,
		strArg(args, "serial"), strArg(args, "expected_os"), strArg(args, "management_user"))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("instance %d is already a machine in a pool", instanceID)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	m, err := dbMachineGet(db, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"machine": m}, nil
}

type machineView struct {
	*Machine
	Operation *Operation `json:"operation,omitempty"`
	Tenant    *Tenant    `json:"tenant,omitempty"`
	ForSale   bool       `json:"for_sale"`
	// NotForSale says why a machine is not for sale, first reason only.
	NotForSale string `json:"not_for_sale,omitempty"`
}

func notForSaleReason(p *Pool, m *Machine) string {
	switch {
	case m.TenantID != "":
		return "assigned"
	case m.State != MachineAvailable:
		return m.State
	case !m.Online:
		if m.ConnectMode == "reverse_tunnel" {
			return "tunnel down"
		}
		return "offline"
	case !m.Validated:
		return "not validated"
	case p == nil || !p.ExecutionEnabled:
		return "pool execution off"
	case m.EndpointIssue != "":
		if strings.HasPrefix(m.EndpointIssue, "no public SSH") {
			return "no public SSH"
		}
		return m.EndpointIssue
	case m.PublicHost == "" && !m.managed():
		return "no customer endpoint"
	}
	return ""
}

func machineViews(db *sql.DB, machines []*Machine) ([]machineView, error) {
	pools := map[string]*Pool{}
	out := make([]machineView, 0, len(machines))
	for _, m := range machines {
		v := machineView{Machine: m}
		if op, err := activeOperationFor(db, "", m.ID); err == nil {
			v.Operation = op
		}
		if m.TenantID != "" {
			v.Tenant, _ = dbTenantGet(db, m.TenantID)
		}
		p, ok := pools[m.PoolKey]
		if !ok {
			p, _ = dbPoolGet(db, m.PoolKey)
			pools[m.PoolKey] = p
		}
		v.NotForSale = notForSaleReason(p, m)
		v.ForSale = v.NotForSale == ""
		out = append(out, v)
	}
	return out, nil
}

func (a *App) toolMachineList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	machines, err := dbMachineList(db, strArg(args, "pool_key"), strArg(args, "state"))
	if err != nil {
		return nil, err
	}
	views, err := machineViews(db, machines)
	if err != nil {
		return nil, err
	}
	return map[string]any{"machines": views, "count": len(views)}, nil
}

func (a *App) toolMachineGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	views, err := machineViews(db, []*Machine{m})
	if err != nil {
		return nil, err
	}
	ops, err := dbOperationList(db, map[string]any{"machine_id": m.ID, "limit": 20})
	if err != nil {
		return nil, err
	}
	return map[string]any{"machine": views[0], "operations": ops}, nil
}

func confirmMachine(args map[string]any, m *Machine) error {
	if strings.TrimSpace(strArg(args, "confirm")) != strconv.FormatInt(m.ID, 10) {
		return fmt.Errorf("confirm must be the machine id (%d)", m.ID)
	}
	return nil
}

func (a *App) toolMachineUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	sets := []string{}
	params := []any{}
	for _, f := range []string{"name", "serial", "management_user", "expected_os"} {
		if v, ok := args[f]; ok {
			sets = append(sets, f+"=?")
			params = append(params, strings.TrimSpace(strFromAny(v)))
		}
	}
	// An endpoint typed by the operator wins over Instances; an empty
	// public_host hands the endpoint back to Instances.
	resetEndpoint := false
	if _, ok := args["public_host"]; ok && m.managed() {
		return nil, errors.New("a phone has no SSH endpoint; customers reach it through Mobile")
	}
	if v, ok := args["public_host"]; ok {
		if host := strings.TrimSpace(strFromAny(v)); host != "" {
			sets = append(sets, "public_host=?", "endpoint_source='manual'", "endpoint_issue=''")
			params = append(params, host)
		} else {
			sets = append(sets, "endpoint_source='instances'")
			resetEndpoint = true
		}
	}
	if v := intArg(args, "public_port", 0); v > 0 && !resetEndpoint {
		sets = append(sets, "public_port=?", "endpoint_source='manual'")
		params = append(params, v)
	}
	if _, ok := args["validated"]; ok {
		if err := confirmMachine(args, m); err != nil {
			return nil, err
		}
		validated := boolArg(args, "validated")
		sets = append(sets, "validated=?")
		params = append(params, boolToInt(validated))
		if !validated && m.State == MachineAvailable {
			sets = append(sets, "state='quarantined'", "state_reason='validation removed'")
		}
		ctx.Logger().Info("hosting machine validation changed", "machine_id", m.ID, "validated", validated, "actor", actor(args))
	}
	if len(sets) == 0 {
		return map[string]any{"machine": m}, nil
	}
	params = append(params, m.ID)
	if _, err := db.Exec(`UPDATE hosting_machines SET `+strings.Join(sets, ", ")+`, updated_at=CURRENT_TIMESTAMP WHERE id=?`, params...); err != nil {
		return nil, err
	}
	if resetEndpoint {
		if m, err = dbMachineGet(db, m.ID); err == nil {
			a.refreshMachine(ctx, m)
		}
	}
	m, err = dbMachineGet(db, m.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"machine": m}, nil
}

func (a *App) machineOp(ctx *sdk.AppCtx, m *Machine, kind string, steps []string, actorName string) (*Operation, error) {
	if m.TenantID != "" {
		return nil, fmt.Errorf("machine %d has tenant %s; release it first", m.ID, m.TenantID)
	}
	if active, err := activeOperationFor(ctx.AppDB(), "", m.ID); err != nil {
		return nil, err
	} else if active != nil {
		if active.Kind == kind {
			return active, nil
		}
		return nil, fmt.Errorf("machine %d is busy with %s run %s", m.ID, active.Kind, active.ID)
	}
	op, err := enqueueOperation(ctx.AppDB(), kind, "", m.ID, m.InstanceID, steps, firstNonEmpty(actorName, "operator"))
	if err != nil {
		return nil, err
	}
	kickOperation(a, ctx, op.ID)
	return dbOperationGet(ctx.AppDB(), op.ID)
}

func (a *App) toolMachineInspect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	if m.State != MachineQuarantined && m.State != MachineMaintenance && m.State != MachineAvailable {
		return nil, fmt.Errorf("machine %d is %s; inspect runs on quarantined, maintenance or available machines", m.ID, m.State)
	}
	pool, err := requirePool(db, m.PoolKey)
	if err != nil {
		return nil, err
	}
	if m.EndpointIssue != "" && m.EndpointSource != "manual" {
		return nil, fmt.Errorf("machine %d cannot be sold: %s", m.ID, m.EndpointIssue)
	}
	steps := []string{"prepare_ssh", "preflight", "check_os", "check_clean"}
	if m.managed() {
		steps = deviceInspectSteps(boolArg(args, "capture_baseline"))
	} else if boolArg(args, "capture_baseline") {
		return nil, errors.New("capture_baseline is for phones (app-managed pools)")
	} else if pool.Platform == "macos" {
		steps = []string{"prepare_ssh", "preflight", "check_os", "install_reset_scripts", "check_clean"}
	}
	if m.State != MachineQuarantined {
		_ = dbMachineSetState(db, m.ID, MachineQuarantined, "inspecting")
	}
	op, err := a.machineOp(ctx, m, OpInspect, steps, actor(args))
	if err != nil {
		return nil, err
	}
	m, _ = dbMachineGet(db, m.ID)
	return map[string]any{"machine": m, "operation": op}, nil
}

func (a *App) toolMachineClean(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	if err := confirmMachine(args, m); err != nil {
		return nil, err
	}
	pool, err := requirePool(db, m.PoolKey)
	if err != nil {
		return nil, err
	}
	if m.managed() {
		if m.State == MachineReserved || m.State == MachinePreparing || m.State == MachineAssigned || m.State == MachineSuspended {
			return nil, fmt.Errorf("machine %d is %s", m.ID, m.State)
		}
		op, err := a.machineOp(ctx, m, OpClean, deviceResetSteps, actor(args))
		if err != nil {
			return nil, err
		}
		m, _ = dbMachineGet(db, m.ID)
		return map[string]any{"machine": m, "operation": op}, nil
	}
	if pool.Platform != "macos" {
		return nil, errors.New("automatic cleaning is only available for macOS pools; clean the machine by hand and run inspect")
	}
	if reason := eraseRefusedReason(m); reason != "" {
		return nil, errors.New("clean refused: " + reason + "; clean it by hand and run inspect")
	}
	if !m.Validated {
		return nil, errors.New("machine is not validated; erase is refused until its recovery has been tested")
	}
	if m.State == MachineReserved || m.State == MachinePreparing || m.State == MachineAssigned || m.State == MachineSuspended {
		return nil, fmt.Errorf("machine %d is %s", m.ID, m.State)
	}
	op, err := a.machineOp(ctx, m, OpClean, join([]string{"wipe"}, afterResetSteps), actor(args))
	if err != nil {
		return nil, err
	}
	m, _ = dbMachineGet(db, m.ID)
	return map[string]any{"machine": m, "operation": op}, nil
}

func (a *App) toolMachineSetMaintenance(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	if boolArg(args, "maintenance") {
		if m.State != MachineAvailable && m.State != MachineMaintenance {
			return nil, fmt.Errorf("machine %d is %s; only available machines go into maintenance", m.ID, m.State)
		}
		err = dbMachineSetState(db, m.ID, MachineMaintenance, firstNonEmpty(strArg(args, "reason"), "maintenance"))
	} else {
		if m.State != MachineMaintenance {
			return nil, fmt.Errorf("machine %d is %s, not in maintenance", m.ID, m.State)
		}
		err = dbMachineSetState(db, m.ID, MachineAvailable, "")
		if err == nil {
			a.assignWaitingTenants(ctx)
		}
	}
	if err != nil {
		return nil, err
	}
	m, err = dbMachineGet(db, m.ID)
	return map[string]any{"machine": m}, err
}

func (a *App) toolMachineQuarantine(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	reason := firstNonEmpty(strArg(args, "reason"), "quarantined by "+firstNonEmpty(actor(args), "operator"))
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := fenceMachineOperations(tx, m.ID, "stopped: "+reason); err != nil {
		return nil, err
	}
	if err := dbMachineSetState(tx, m.ID, MachineQuarantined, reason); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE hosting_reservations SET status='released', updated_at=CURRENT_TIMESTAMP WHERE machine_id=? AND status='held'`, m.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	m, err = dbMachineGet(db, m.ID)
	return map[string]any{"machine": m}, err
}

func (a *App) toolMachineRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	m, err := requireMachine(db, int64Arg(args, "machine_id"))
	if err != nil {
		return nil, err
	}
	if err := confirmMachine(args, m); err != nil {
		return nil, err
	}
	if m.TenantID != "" {
		return nil, fmt.Errorf("machine %d has tenant %s; release it first", m.ID, m.TenantID)
	}
	if active, err := activeOperationFor(db, "", m.ID); err != nil {
		return nil, err
	} else if active != nil {
		return nil, fmt.Errorf("machine %d is busy with %s run %s", m.ID, active.Kind, active.ID)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Past tenants and holds keep their history; only the link goes.
	if _, err := tx.Exec(`UPDATE hosting_tenants SET machine_id=NULL, metadata_json=json_set(metadata_json, '$.removed_machine_id', ?) WHERE machine_id=?`, m.ID, m.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM hosting_reservations WHERE machine_id=?`, m.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM hosting_machines WHERE id=?`, m.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Logger().Info("hosting machine removed", "machine_id", m.ID, "instance_id", m.InstanceID, "actor", actor(args))
	return map[string]any{"removed": true, "machine_id": m.ID}, nil
}

// refreshMachines records whether each pool machine's Instances host answers.
func (a *App) refreshMachines(ctx *sdk.AppCtx) {
	machines, err := dbMachineList(ctx.AppDB(), "", "")
	if err != nil {
		return
	}
	unreachable := 0
	var lastErr error
	for _, m := range machines {
		if m.managed() {
			continue // refreshDevices asks the managing app
		}
		if err := a.refreshMachine(ctx, m); err != nil {
			unreachable++
			lastErr = err
		}
	}
	if unreachable > 0 {
		// One warning instead of an offline alert per machine: the machines
		// may be fine, Hosting just cannot ask.
		ctx.Logger().Warn("hosting cannot reach Instances; machines marked offline without alerts", "machines", unreachable, "err", lastErr)
	}
}

func (a *App) setQuietOffline(id int64, quiet bool) (was bool) {
	a.quietMu.Lock()
	defer a.quietMu.Unlock()
	was = a.quietOffline[id]
	if quiet {
		if a.quietOffline == nil {
			a.quietOffline = map[int64]bool{}
		}
		a.quietOffline[id] = true
	} else {
		delete(a.quietOffline, id)
	}
	return was
}

// instanceMissing tells "Instances answered: no such instance" apart from
// "Instances could not be reached".
func instanceMissing(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

// refreshMachine copies Instances' view of one machine: whether it is
// connected, how it is reached, and (unless an operator set it by hand)
// where customers reach it. A changed endpoint shows up in the assigned
// tenant's SSH command at once, since that is built from the machine row.
//
// It returns an error only when Instances could not be reached: the machine
// is then taken off sale without an alert.
func (a *App) refreshMachine(ctx *sdk.AppCtx, m *Machine) error {
	db := ctx.AppDB()
	inst, err := instanceGet(ctx, ctx.CurrentProject(), m.InstanceID)
	if err != nil {
		missing := instanceMissing(err)
		if m.Online {
			_, _ = db.Exec(`UPDATE hosting_machines SET online=0, updated_at=CURRENT_TIMESTAMP WHERE id=?`, m.ID)
			if missing {
				emitMachineOnline(ctx, m, false, m.LastSeenAt, "instance not found in Instances")
			} else {
				a.setQuietOffline(m.ID, true)
			}
		}
		if missing {
			return nil
		}
		return err
	}
	online := inst.online()
	seen := m.LastSeenAt
	if inst.LastSeenAt != "" {
		seen = inst.LastSeenAt
	} else if online {
		seen = sqlTime(now())
	}
	host, port, issue := m.PublicHost, m.PublicPort, m.EndpointIssue
	if m.EndpointSource != "manual" {
		host, port, issue = customerEndpoint(inst)
	}
	if host != m.PublicHost || port != m.PublicPort {
		ctx.Logger().Info("hosting machine endpoint changed", "machine_id", m.ID, "from", fmt.Sprintf("%s:%d", m.PublicHost, m.PublicPort), "to", fmt.Sprintf("%s:%d", host, port), "issue", issue)
		if m.TenantID != "" {
			_ = recordEvent(db, m.TenantID, "access.endpoint_changed", "instances", map[string]any{"machine_id": m.ID, "host": host, "port": port, "issue": issue})
		}
	}
	res, _ := db.Exec(`UPDATE hosting_machines SET online=?, last_seen_at=NULLIF(?, ''), connect_mode=?, public_host=?, public_port=?, endpoint_issue=?,
		updated_at=CASE WHEN online!=? OR public_host!=? OR public_port!=? OR endpoint_issue!=? OR connect_mode!=? THEN CURRENT_TIMESTAMP ELSE updated_at END
		WHERE id=?`, boolToInt(online), seen, inst.ConnectMode, host, port, issue,
		boolToInt(online), host, port, issue, inst.ConnectMode, m.ID)
	wasQuiet := a.setQuietOffline(m.ID, false)
	switch {
	case res == nil || online == m.Online:
	case online && wasQuiet:
		// Back after Instances was unreachable: no offline alert went out.
	default:
		emitMachineOnline(ctx, m, online, seen, inst.Status)
	}
	return nil
}

// emitMachineOnline tells operators a machine went offline or came back.
// A machine being erased (state cleaning) is expected to drop off: its run
// has its own time limits and reports hosting.operation.failed instead.
func emitMachineOnline(ctx *sdk.AppCtx, m *Machine, online bool, lastSeen, detail string) {
	if m.State == MachineCleaning {
		return
	}
	event := "hosting.machine.offline"
	if online {
		event = "hosting.machine.online"
	}
	ctx.Logger().Info("hosting machine "+map[bool]string{true: "online", false: "offline"}[online], "machine_id", m.ID, "state", m.State)
	ctx.Emit(event, map[string]any{"machine_id": m.ID, "pool_key": m.PoolKey, "instance_id": m.InstanceID, "state": m.State,
		"tenant_id": m.TenantID, "last_seen_at": lastSeen, "detail": detail})
}

// ─── Runs ────────────────────────────────────────────────────────────────

func (a *App) toolOperationGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	op, err := dbOperationGet(ctx.AppDB(), strArg(args, "operation_id"))
	if err != nil {
		return nil, err
	}
	if op == nil {
		return nil, errors.New("operation not found")
	}
	return map[string]any{"operation": op}, nil
}

func (a *App) toolOperationList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ops, err := dbOperationList(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operations": ops, "count": len(ops)}, nil
}

func (a *App) toolOperationRetry(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	op, err := a.retryOperation(ctx, strArg(args, "operation_id"), boolArg(args, "reviewed"), firstNonEmpty(actor(args), "operator"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation": op}, nil
}

func (a *App) toolTenantUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db := ctx.AppDB()
	t, err := dbTenantGet(db, strArg(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("tenant %s not found", strArg(args, "tenant_id"))
	}
	if t.Status == TenantReleased {
		return nil, fmt.Errorf("tenant %s is released", t.ID)
	}
	changes := map[string]any{}
	if _, ok := args["external_ref"]; ok {
		ref := strings.TrimSpace(strArg(args, "external_ref"))
		if ref != t.ExternalRef {
			if t.ExternalRef != "" {
				if err := confirmTenant(t, args); err != nil {
					return nil, fmt.Errorf("tenant already has external_ref %q; %w", t.ExternalRef, err)
				}
			}
			if _, err := db.Exec(`UPDATE hosting_tenants SET external_ref=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, ref, t.ID); err != nil {
				if strings.Contains(err.Error(), "UNIQUE") {
					return nil, fmt.Errorf("external_ref %q already has a live tenant", ref)
				}
				return nil, err
			}
			changes["external_ref"] = map[string]any{"from": t.ExternalRef, "to": ref}
		}
	}
	if email := strings.ToLower(strings.TrimSpace(strArg(args, "owner_email"))); email != "" && email != t.OwnerEmail {
		if !strings.Contains(email, "@") {
			return nil, errors.New("valid owner_email required")
		}
		if _, err := db.Exec(`UPDATE hosting_tenants SET owner_email=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, email, t.ID); err != nil {
			return nil, err
		}
		changes["owner_email"] = email
	}
	if meta := mapArg(args, "metadata"); len(meta) > 0 {
		if err := dbTenantMergeMetadata(db, t.ID, meta); err != nil {
			return nil, err
		}
		changes["metadata"] = true
	}
	if len(changes) > 0 {
		_ = recordEvent(db, t.ID, "tenant.updated", firstNonEmpty(actor(args), "tool"), changes)
	}
	t, err = dbTenantGet(db, t.ID)
	return map[string]any{"tenant": t, "changed": len(changes) > 0}, err
}

func (a *App) toolTenantResolve(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.resolveStuckTenant(ctx, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}
