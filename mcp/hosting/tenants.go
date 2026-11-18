package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Step plans per backend ──────────────────────────────────────────────

// Erasing a Mac also removes Hosting's SSH drop-in and reset helpers, so a
// cleaned machine gets both back before it is checked and sold again.
var afterResetSteps = []string{"verify_clean", "prepare_ssh", "install_reset_scripts", "check_clean"}

func accessGrantSteps(t *Tenant) []string {
	if len(t.AccessKeys) == 0 {
		return nil
	}
	return []string{"grant", "verify_access"}
}

func workloadSteps(product *Product, step string) []string {
	if product != nil && product.runsContainer() {
		return []string{step}
	}
	return nil
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// eraseRefusedReason says why a pool machine must not be erased
// automatically. Erasing a reverse-tunnel Mac also removes the tunnel and
// Instances' key, and nothing restores them yet, so the machine would never
// come back online.
func eraseRefusedReason(m *Machine) string {
	if m != nil && m.ConnectMode == "reverse_tunnel" {
		return "this Mac reaches the server through a reverse tunnel; erasing it removes the tunnel and Instances' access, and no recovery restores them yet"
	}
	return ""
}

func planSteps(kind string, profile *Profile, product *Product, pool *Pool, m *Machine, t *Tenant) ([]string, error) {
	onDemand := profile.Backend == BackendMachineOnDemand
	macPool := pool != nil && pool.Platform == "macos"
	canErase := macPool && eraseRefusedReason(m) == ""
	switch kind {
	case OpProvision:
		if onDemand {
			return join([]string{"create_instance", "wait_ready", "prepare_ssh", "preflight"}, accessGrantSteps(t), workloadSteps(product, "run_workload")), nil
		}
		return join([]string{"prepare_ssh", "preflight", "check_os"}, accessGrantSteps(t), workloadSteps(product, "run_workload")), nil
	case OpSuspend:
		return join(workloadSteps(product, "stop_workload"), []string{"revoke", "verify_revoked"}), nil
	case OpResume:
		return join([]string{"preflight"}, accessGrantSteps(t), workloadSteps(product, "start_workload")), nil
	case OpAccessKeys:
		if len(t.AccessKeys) == 0 {
			return []string{"revoke", "verify_revoked"}, nil
		}
		return []string{"grant", "verify_access"}, nil
	case OpRestart:
		if len(t.AccessKeys) == 0 {
			return []string{"reboot"}, nil
		}
		return []string{"reboot", "wait_access"}, nil
	case OpRelease:
		if onDemand {
			return []string{"revoke_quiet", "destroy_instance"}, nil
		}
		base := join([]string{"revoke", "verify_revoked"}, workloadSteps(product, "destroy_workload"))
		if canErase {
			return join(base, []string{"wipe"}, afterResetSteps), nil
		}
		// No automatic reset (Linux pool, tunnel Mac): access is removed and
		// the machine is quarantined for a manual clean.
		return base, nil
	case OpReinstall:
		if !macPool {
			return nil, errors.New("reinstall is only available for macOS pool machines")
		}
		if reason := eraseRefusedReason(m); reason != "" {
			return nil, errors.New("reinstall refused: " + reason)
		}
		return join([]string{"revoke", "verify_revoked"}, workloadSteps(product, "destroy_workload"), []string{"reinstall"}, afterResetSteps,
			[]string{"preflight", "check_os"}, accessGrantSteps(t), workloadSteps(product, "run_workload")), nil
	}
	return nil, fmt.Errorf("unknown operation %s", kind)
}

// ─── Create ──────────────────────────────────────────────────────────────

type createRequest struct {
	ExternalRef   string
	ProfileKey    string
	OwnerEmail    string
	ReservationID string
	SSHKeys       []string
	Slug          string
	RuntimeConfig map[string]any
	Metadata      map[string]any
	AptevaVersion string
	DeleteVolumes bool
}

func parseCreateRequest(args map[string]any) (*createRequest, error) {
	req := &createRequest{
		ExternalRef:   strings.TrimSpace(strArg(args, "external_ref")),
		ProfileKey:    firstNonEmpty(strArg(args, "profile"), strArg(args, "profile_key")),
		OwnerEmail:    strings.ToLower(strings.TrimSpace(strArg(args, "owner_email"))),
		ReservationID: strArg(args, "reservation_id"),
		Slug:          strArg(args, "slug"),
		RuntimeConfig: mapArg(args, "runtime_config"),
		Metadata:      mapArg(args, "metadata"),
		AptevaVersion: strArg(args, "apteva_version"),
	}
	if req.ExternalRef == "" {
		return nil, errors.New("external_ref required (the caller's id for this tenant, e.g. the SaaS account id)")
	}
	if req.OwnerEmail == "" || !strings.Contains(req.OwnerEmail, "@") {
		return nil, errors.New("valid owner_email required")
	}
	keys, err := parseSSHKeys(args["ssh_keys"])
	if err != nil {
		return nil, err
	}
	req.SSHKeys = keys
	return req, nil
}

// parseSSHKeys accepts an array of public keys or one newline-separated string.
func parseSSHKeys(v any) ([]string, error) {
	var raw []string
	switch x := v.(type) {
	case nil:
	case string:
		raw = strings.Split(x, "\n")
	default:
		raw = stringSliceFromAny(v)
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range raw {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		fields := strings.Fields(k)
		if len(fields) < 2 || !(strings.HasPrefix(fields[0], "ssh-") || strings.HasPrefix(fields[0], "ecdsa-") || strings.HasPrefix(fields[0], "sk-")) {
			return nil, fmt.Errorf("not an SSH public key: %.40q", k)
		}
		if strings.ContainsAny(k, "\r\x00") {
			return nil, errors.New("SSH key contains control characters")
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) > 20 {
		return nil, errors.New("at most 20 SSH keys")
	}
	return out, nil
}

func (a *App) createTenant(ctx *sdk.AppCtx, args map[string]any) (*Tenant, error) {
	req, err := parseCreateRequest(args)
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if existing, err := dbTenantByRef(db, req.ExternalRef); err != nil {
		return nil, err
	} else if existing != nil {
		if req.ProfileKey != "" && existing.ProfileKey != req.ProfileKey {
			return nil, fmt.Errorf("external_ref %q already has tenant %s on profile %q; changing profile is a release followed by a new create", req.ExternalRef, existing.ID, existing.ProfileKey)
		}
		switch existing.Status {
		case TenantSuspended, TenantSuspending:
			// account_active fires again when a past-due account pays: bring
			// the tenant back instead of reporting the suspended one as done.
			t, _, err := a.tenantLifecycle(ctx, OpResume, map[string]any{"tenant_id": existing.ID, "actor": firstNonEmpty(actor(args), "create")})
			return t, err
		case TenantReleasing:
			return nil, fmt.Errorf("tenant %s for external_ref %q is being released; create again once it is released", existing.ID, req.ExternalRef)
		}
		return existing, nil // repeat call: same tenant, nothing new started
	}
	profile, err := requireProfile(db, req.ProfileKey)
	if err != nil {
		return nil, err
	}
	if profile.Status != "active" {
		return nil, fmt.Errorf("profile %q is archived", profile.Key)
	}
	product, err := dbProductGet(db, profile.ProductKey)
	if err != nil {
		return nil, err
	}
	if product == nil {
		return nil, fmt.Errorf("product %q not found", profile.ProductKey)
	}
	slug := req.Slug
	if slug == "" {
		slug = "t-" + strings.TrimPrefix(newID("x"), "x_")
	}
	if slug, err = normalizeSlug(slug); err != nil {
		return nil, err
	}
	t := &Tenant{
		ID:            newID("htn"),
		ExternalRef:   req.ExternalRef,
		ProfileKey:    profile.Key,
		Backend:       profile.Backend,
		Slug:          slug,
		OwnerEmail:    req.OwnerEmail,
		AccessKeys:    req.SSHKeys,
		AptevaVersion: req.AptevaVersion,
		Status:        TenantProvisioning,
	}
	meta := mergeMaps(req.Metadata, map[string]any{"product_key": product.Key, "runtime_kind": product.RuntimeKind})
	if len(req.RuntimeConfig) > 0 {
		meta["runtime_config"] = req.RuntimeConfig
	}
	if product.runsContainer() {
		if t.Image, err = resolveImage(configString(ctx, "default_image", "ghcr.io/apteva/apteva:latest"), product, profile, req.RuntimeConfig); err != nil {
			return nil, err
		}
	}
	switch profile.Backend {
	case BackendContainer:
		if !product.runsContainer() {
			return nil, fmt.Errorf("product %q needs a machine backend", product.Key)
		}
		t.Metadata = jsonRaw(meta)
		return a.createContainerTenant(ctx, t, profile, product, req.RuntimeConfig)
	case BackendMachinePool, BackendMachineOnDemand:
		t.SSHUser = customerUser(t.ID)
		t.Metadata = jsonRaw(meta)
		return a.createMachineTenant(ctx, t, profile, product, req.ReservationID)
	default:
		return nil, fmt.Errorf("unknown backend %q", profile.Backend)
	}
}

func (a *App) createContainerTenant(ctx *sdk.AppCtx, t *Tenant, profile *Profile, product *Product, runtimeConfig map[string]any) (*Tenant, error) {
	db := ctx.AppDB()
	t.DefaultHostname = t.Slug + "." + strings.Trim(strings.ToLower(configString(ctx, "default_base_domain", "hosted.apteva.local")), ".")
	if err := dbTenantInsert(db, t); err != nil {
		return nil, uniqueErr(err, t)
	}
	_ = recordEvent(db, t.ID, "provisioning.started", "hosting", map[string]any{"profile": profile.Key})
	fail := func(err error, kind string) (*Tenant, error) {
		_ = dbTenantSetStatus(db, t.ID, TenantFailed, err.Error())
		_ = recordEvent(db, t.ID, kind, "hosting", map[string]any{"error": err.Error()})
		return nil, err
	}
	var out workloadResp
	if err := ctx.PlatformAPI().CallAppResult("containers", "containers_run", containerRunInput(t, profile, product, runtimeConfig, 0), &out); err != nil {
		return fail(err, "provisioning.failed")
	}
	if out.Workload.ID == "" {
		return fail(errors.New("containers_run returned empty workload id"), "provisioning.failed")
	}
	if _, err := db.Exec(`UPDATE hosting_tenants SET workload_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, out.Workload.ID, t.ID); err != nil {
		return nil, err
	}
	target := workloadTarget(&out.Workload)
	if target == "" {
		return fail(errors.New("containers_run returned no reachable workload URL"), "provisioning.failed")
	}
	route, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{Hostname: t.DefaultHostname, Target: target, TLSMode: "auto"})
	if err != nil {
		return fail(err, "ingress.failed")
	}
	if _, err := db.Exec(`UPDATE hosting_tenants SET status='active', last_health_status=?, last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		firstNonEmpty(out.Workload.HealthStatus, out.Workload.Status, "unknown"), t.ID); err != nil {
		return nil, err
	}
	_ = recordEvent(db, t.ID, "ingress.active", "platform", map[string]any{"hostname": route.Hostname, "target": route.Target})
	_ = recordEvent(db, t.ID, "provisioning.active", "hosting", map[string]any{"workload_id": out.Workload.ID})
	ctx.Emit("hosting.tenant.active", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "hostname": t.DefaultHostname})
	return dbTenantGet(db, t.ID)
}

func uniqueErr(err error, t *Tenant) error {
	if strings.Contains(err.Error(), "UNIQUE") {
		if strings.Contains(err.Error(), "slug") || strings.Contains(err.Error(), "default_hostname") {
			return fmt.Errorf("slug %q is already in use", t.Slug)
		}
		return fmt.Errorf("external_ref %q already has a live tenant", t.ExternalRef)
	}
	return err
}

func (a *App) createMachineTenant(ctx *sdk.AppCtx, t *Tenant, profile *Profile, product *Product, reservationID string) (*Tenant, error) {
	db := ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var machine *Machine
	var pool *Pool
	if profile.Backend == BackendMachinePool {
		if pool, err = requirePool(tx, profile.PoolKey); err != nil {
			return nil, err
		}
		t.Metadata = jsonRaw(mergeMaps(mapFromAny(t.Metadata), map[string]any{"platform": pool.Platform}))
		if machine, err = claimMachineTx(tx, profile, t, reservationID); err != nil {
			return nil, err
		}
		if machine == nil {
			t.Status = TenantWaitingForMachine
		} else {
			t.MachineID = machine.ID
		}
	}
	if err := dbTenantInsert(tx, t); err != nil {
		return nil, uniqueErr(err, t)
	}
	if machine != nil {
		// Hand-over happens in the same transaction as the tenant insert, so a
		// machine can never be given out twice.
		res, err := tx.Exec(`UPDATE hosting_machines SET state='preparing', state_reason='', tenant_id=?, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND tenant_id IS NULL AND state IN ('available','reserved')`, t.ID, machine.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, errors.New("machine was taken by another tenant; retry")
		}
	}
	var op *Operation
	if t.Status == TenantProvisioning {
		steps, err := planSteps(OpProvision, profile, product, pool, machine, t)
		if err != nil {
			return nil, err
		}
		if op, err = enqueueOperation(tx, OpProvision, t.ID, t.MachineID, 0, steps, "hosting"); err != nil {
			return nil, err
		}
	}
	_ = recordEvent(tx, t.ID, "tenant.created", "hosting", map[string]any{"profile": profile.Key, "machine_id": t.MachineID, "status": t.Status})
	if err := tx.Commit(); err != nil {
		return nil, uniqueErr(err, t)
	}
	if op != nil {
		kickOperation(a, ctx, op.ID)
	}
	return dbTenantGet(db, t.ID)
}

// claimMachineTx picks the machine for a new pool tenant: the caller's held
// reservation (given, or found by profile + external_ref) when that machine
// can still be sold, otherwise any machine for sale. nil means none is free
// and the tenant waits.
func claimMachineTx(tx *sql.Tx, profile *Profile, t *Tenant, reservationID string) (*Machine, error) {
	var r *Reservation
	if reservationID != "" {
		var err error
		if r, err = dbReservationGet(tx, reservationID); err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fmt.Errorf("reservation %s not found", reservationID)
		}
		if r.ProfileKey != profile.Key || r.ExternalRef != t.ExternalRef {
			return nil, fmt.Errorf("reservation %s belongs to another profile or external_ref", reservationID)
		}
	} else if t.ExternalRef != "" {
		found, err := scanReservation(tx.QueryRow(reservationSelect+` WHERE profile_key=? AND external_ref=? AND status='held'`, profile.Key, t.ExternalRef))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		r = found
	}
	if r != nil && r.Status == "held" {
		var ok int
		err := tx.QueryRow(`SELECT 1 FROM hosting_machines m WHERE m.id=? AND m.state='reserved' AND m.online=1 AND m.validated=1 AND m.tenant_id IS NULL
			AND m.public_host != '' AND m.endpoint_issue = ''
			AND EXISTS (SELECT 1 FROM hosting_pools p WHERE p.key=m.pool_key AND p.execution_enabled=1)`, r.MachineID).Scan(&ok)
		if err == nil {
			t.ReservationID = r.ID
			return dbMachineGet(tx, r.MachineID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// The held machine went offline, lost validation or its pool stopped
		// execution: drop the hold and fall back to another machine.
		if _, err := tx.Exec(`UPDATE hosting_reservations SET status='released', updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='held'`, r.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE hosting_machines SET state='available', state_reason='', updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='reserved' AND tenant_id IS NULL`, r.MachineID); err != nil {
			return nil, err
		}
	}
	// Expired, released or unusable hold: any machine for sale.
	var id int64
	err := tx.QueryRow(`SELECT m.id FROM hosting_machines m WHERE m.pool_key=? AND `+forSaleCondition+`
		AND NOT EXISTS (SELECT 1 FROM hosting_reservations r WHERE r.machine_id=m.id AND r.status='held')
		ORDER BY m.id LIMIT 1`, profile.PoolKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbMachineGet(tx, id)
}

// assignWaitingTenants hands freed machines to paid tenants that are waiting,
// oldest first.
func (a *App) assignWaitingTenants(ctx *sdk.AppCtx) {
	db := ctx.AppDB()
	rows, err := db.Query(`SELECT id FROM hosting_tenants WHERE status='waiting_for_machine' ORDER BY created_at, rowid LIMIT 50`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := a.assignWaitingTenant(ctx, id); err != nil {
			ctx.Logger().Info("hosting waiting tenant not assigned", "tenant_id", id, "reason", err)
		}
	}
}

func (a *App) assignWaitingTenant(ctx *sdk.AppCtx, tenantID string) (bool, error) {
	db := ctx.AppDB()
	if t, err := dbTenantGet(db, tenantID); err != nil || t == nil || t.Status != TenantWaitingForMachine {
		return false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	t, err := dbTenantGet(tx, tenantID)
	if err != nil || t == nil || t.Status != TenantWaitingForMachine {
		return false, err
	}
	profile, err := requireProfile(tx, t.ProfileKey)
	if err != nil {
		return false, err
	}
	pool, err := requirePool(tx, profile.PoolKey)
	if err != nil {
		return false, err
	}
	m, err := claimMachineTx(tx, profile, t, "")
	if err != nil || m == nil {
		return false, err
	}
	res, err := tx.Exec(`UPDATE hosting_machines SET state='preparing', state_reason='', tenant_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND tenant_id IS NULL AND state IN ('available','reserved')`, t.ID, m.ID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE hosting_tenants SET machine_id=?, reservation_id=NULLIF(?, ''), status='provisioning', updated_at=CURRENT_TIMESTAMP WHERE id=?`, m.ID, t.ReservationID, t.ID); err != nil {
		return false, err
	}
	t.MachineID = m.ID
	product, err := productForTx(tx, profile.ProductKey)
	if err != nil {
		return false, err
	}
	steps, err := planSteps(OpProvision, profile, product, pool, m, t)
	if err != nil {
		return false, err
	}
	op, err := enqueueOperation(tx, OpProvision, t.ID, m.ID, 0, steps, "worker")
	if err != nil {
		return false, err
	}
	_ = recordEvent(tx, t.ID, "machine.assigned", "worker", map[string]any{"machine_id": m.ID})
	if err := tx.Commit(); err != nil {
		return false, err
	}
	kickOperation(a, ctx, op.ID)
	return true, nil
}

// ─── Lifecycle ───────────────────────────────────────────────────────────

type lifecycleSpec struct {
	kind      string
	transient string   // tenant status while the run is in flight
	settled   string   // status after which the call is a no-op
	allowed   []string // statuses the call may start from
}

var lifecycleSpecs = map[string]lifecycleSpec{
	OpSuspend: {kind: OpSuspend, transient: TenantSuspending, settled: TenantSuspended, allowed: []string{TenantActive, TenantResuming, TenantProvisioning}},
	OpResume:  {kind: OpResume, transient: TenantResuming, settled: TenantActive, allowed: []string{TenantSuspended, TenantSuspending}},
}

// tenantLifecycle starts suspend or resume. Repeating a call that already
// happened (or is happening) returns the tenant without a new run.
func (a *App) tenantLifecycle(ctx *sdk.AppCtx, kind string, args map[string]any) (*Tenant, *Operation, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, nil, err
	}
	spec := lifecycleSpecs[kind]
	if t.Status == spec.settled || t.Status == spec.transient {
		return t, nil, nil
	}
	switch t.Status {
	case TenantReleasing, TenantReleased:
		return nil, nil, fmt.Errorf("tenant %s is %s", t.ID, t.Status)
	case TenantWaitingForMachine:
		if kind == OpSuspend {
			// Nothing runs yet: keep the place in line but stop assignment.
			err := dbTenantSetStatus(db, t.ID, TenantSuspended, "")
			_ = dbTenantMergeMetadata(db, t.ID, map[string]any{"suspended_while_waiting": true})
			t, _ = dbTenantGet(db, t.ID)
			return t, nil, err
		}
	}
	if kind == OpResume && boolFromAny(mapFromAny(t.Metadata)["suspended_while_waiting"], false) && t.MachineID == 0 {
		err := dbTenantSetStatus(db, t.ID, TenantWaitingForMachine, "")
		_ = dbTenantMergeMetadata(db, t.ID, map[string]any{"suspended_while_waiting": false})
		a.assignWaitingTenants(ctx)
		t, _ = dbTenantGet(db, t.ID)
		return t, nil, err
	}
	if !containsStep(spec.allowed, t.Status) {
		return nil, nil, fmt.Errorf("cannot %s tenant %s while it is %s", kind, t.ID, t.Status)
	}
	if t.Backend == BackendContainer {
		return a.containerLifecycle(ctx, t, kind)
	}
	op, err := a.enqueueTenantOp(ctx, t, kind, spec.transient, actor(args))
	if err != nil {
		return nil, nil, err
	}
	t, err = dbTenantGet(db, t.ID)
	return t, op, err
}

func (a *App) enqueueTenantOp(ctx *sdk.AppCtx, t *Tenant, kind, transient, actorName string) (*Operation, error) {
	db := ctx.AppDB()
	profile, err := requireProfile(db, t.ProfileKey)
	if err != nil {
		return nil, err
	}
	product, err := dbProductGet(db, profile.ProductKey)
	if err != nil {
		return nil, err
	}
	var pool *Pool
	var m *Machine
	if t.MachineID > 0 {
		if m, err = requireMachine(db, t.MachineID); err != nil {
			return nil, err
		}
		if pool, err = requirePool(db, m.PoolKey); err != nil {
			return nil, err
		}
	}
	steps, err := planSteps(kind, profile, product, pool, m, t)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	op, err := enqueueOperation(tx, kind, t.ID, t.MachineID, t.InstanceID, steps, firstNonEmpty(actorName, "tool"))
	if err != nil {
		return nil, err
	}
	if transient != "" {
		if err := dbTenantSetStatus(tx, t.ID, transient, ""); err != nil {
			return nil, err
		}
	}
	_ = recordEvent(tx, t.ID, kind+".requested", firstNonEmpty(actorName, "tool"), map[string]any{"operation_id": op.ID})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	kickOperation(a, ctx, op.ID)
	return op, nil
}

func (a *App) containerLifecycle(ctx *sdk.AppCtx, t *Tenant, kind string) (*Tenant, *Operation, error) {
	db := ctx.AppDB()
	tool, status := "containers_stop", TenantSuspended
	if kind == OpResume {
		tool, status = "containers_start", TenantActive
	}
	if t.WorkloadID != "" {
		var out workloadResp
		if err := ctx.PlatformAPI().CallAppResult("containers", tool, map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
			return nil, nil, err
		}
	}
	if err := dbTenantSetStatus(db, t.ID, status, ""); err != nil {
		return nil, nil, err
	}
	_ = recordEvent(db, t.ID, status, "tool", nil)
	ctx.Emit("hosting.tenant."+status, map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef})
	t, err := dbTenantGet(db, t.ID)
	return t, nil, err
}

// releaseTenant ends a tenant now. When to end it is the caller's decision.
func (a *App) releaseTenant(ctx *sdk.AppCtx, args map[string]any) (*Tenant, *Operation, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		if ref := strArg(args, "external_ref"); ref != "" && strArg(args, "tenant_id") == "" && strings.Contains(err.Error(), "no live tenant") {
			// Already released (or never created): repeating a release is a no-op.
			_ = releaseHeldReservations(db, ref)
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if t.Status == TenantReleasing || t.Status == TenantReleased {
		return t, nil, nil
	}
	if err := confirmTenant(t, args); err != nil {
		return nil, nil, err
	}
	if t.ReservationID != "" {
		_, _ = db.Exec(`UPDATE hosting_reservations SET status='released', updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='held'`, t.ReservationID)
	}
	if t.Backend == BackendContainer {
		if t.DefaultHostname != "" {
			_ = ctx.PlatformAPI().UnexposeIngress(t.DefaultHostname)
		}
		if t.WorkloadID != "" {
			var out map[string]any
			if err := ctx.PlatformAPI().CallAppResult("containers", "containers_destroy", map[string]any{"workload_id": t.WorkloadID, "delete_volumes": boolArg(args, "delete_volumes")}, &out); err != nil {
				return nil, nil, err
			}
		}
		if err := dbTenantSetStatus(db, t.ID, TenantReleased, ""); err != nil {
			return nil, nil, err
		}
		// The hostname is free for reuse once the tenant is gone.
		_, _ = db.Exec(`UPDATE hosting_tenants SET default_hostname=NULL WHERE id=?`, t.ID)
		_ = recordEvent(db, t.ID, "released", actor(args), nil)
		ctx.Emit("hosting.tenant.released", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef})
		t, err = dbTenantGet(db, t.ID)
		return t, nil, err
	}
	if t.MachineID == 0 && t.InstanceID == 0 && (t.Status == TenantWaitingForMachine || t.Status == TenantSuspended || t.Status == TenantFailed) {
		// Never got hardware: nothing to clean.
		if err := dbTenantSetStatus(db, t.ID, TenantReleased, ""); err != nil {
			return nil, nil, err
		}
		_ = recordEvent(db, t.ID, "released", actor(args), map[string]any{"without_machine": true})
		ctx.Emit("hosting.tenant.released", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef})
		t, err = dbTenantGet(db, t.ID)
		return t, nil, err
	}
	op, err := a.enqueueTenantOp(ctx, t, OpRelease, TenantReleasing, actor(args))
	if err != nil {
		return nil, nil, err
	}
	t, err = dbTenantGet(db, t.ID)
	return t, op, err
}

// confirmTenant guards tools that destroy customer data: the caller must pass
// confirm equal to the tenant id (SaaS keeps it from tenant creation).
func confirmTenant(t *Tenant, args map[string]any) error {
	if got := strings.TrimSpace(strArg(args, "confirm")); got != t.ID {
		return fmt.Errorf("this destroys the tenant's data; confirm must be the tenant id %s", t.ID)
	}
	return nil
}

func releaseHeldReservations(db querier, ref string) error {
	_, err := db.Exec(`UPDATE hosting_reservations SET status='released', updated_at=CURRENT_TIMESTAMP WHERE external_ref=? AND status='held'`, ref)
	return err
}

// machineTenantOp runs restart, reinstall or an access-key change on a
// machine tenant.
func (a *App) machineTenantOp(ctx *sdk.AppCtx, kind string, args map[string]any) (*Tenant, *Operation, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, nil, err
	}
	if t.Backend == BackendContainer {
		if kind == OpRestart {
			var out workloadResp
			if err := ctx.PlatformAPI().CallAppResult("containers", "containers_restart", map[string]any{"workload_id": t.WorkloadID}, &out); err != nil {
				return nil, nil, err
			}
			_ = recordEvent(db, t.ID, "restarted", actor(args), nil)
			t, err = dbTenantGet(db, t.ID)
			return t, nil, err
		}
		return nil, nil, fmt.Errorf("%s is only available for machine tenants", kind)
	}
	if kind == OpReinstall {
		if err := confirmTenant(t, args); err != nil {
			return nil, nil, err
		}
	}
	switch t.Status {
	case TenantActive:
	case TenantSuspended:
		if kind != OpAccessKeys {
			return nil, nil, fmt.Errorf("tenant %s is suspended", t.ID)
		}
	default:
		if kind != OpAccessKeys {
			return nil, nil, fmt.Errorf("cannot %s tenant %s while it is %s", kind, t.ID, t.Status)
		}
	}
	transient := ""
	if kind == OpReinstall {
		transient = TenantProvisioning
	}
	if kind == OpAccessKeys {
		keys, err := parseSSHKeys(args["ssh_keys"])
		if err != nil {
			return nil, nil, err
		}
		if _, err := db.Exec(`UPDATE hosting_tenants SET access_keys_json=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, jsonOrEmpty(keys, "[]"), t.ID); err != nil {
			return nil, nil, err
		}
		t.AccessKeys = keys
		// Keys on a tenant that is not running are applied on resume/provision.
		if t.Status != TenantActive {
			t, err = dbTenantGet(db, t.ID)
			return t, nil, err
		}
	}
	op, err := a.enqueueTenantOp(ctx, t, kind, transient, actor(args))
	if err != nil {
		return nil, nil, err
	}
	t, err = dbTenantGet(db, t.ID)
	return t, op, err
}

// ─── Reservations and availability ───────────────────────────────────────

func reservationTTL(ctx *sdk.AppCtx) time.Duration {
	mins := 30
	if v := configString(ctx, "reservation_ttl_minutes", ""); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n <= 24*60 {
			mins = n
		}
	}
	return time.Duration(mins) * time.Minute
}

func (a *App) createReservation(ctx *sdk.AppCtx, args map[string]any) (*Reservation, error) {
	db := ctx.AppDB()
	ref := strings.TrimSpace(strArg(args, "external_ref"))
	if ref == "" {
		return nil, errors.New("external_ref required")
	}
	profile, err := requireProfile(db, firstNonEmpty(strArg(args, "profile"), strArg(args, "profile_key")))
	if err != nil {
		return nil, err
	}
	if profile.Backend != BackendMachinePool {
		return nil, errors.New("reservations are only needed for machine_pool profiles")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if r, err := scanReservation(tx.QueryRow(reservationSelect+` WHERE profile_key=? AND external_ref=? AND status='held'`, profile.Key, ref)); err == nil {
		return r, nil // repeat call: same hold
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var machineID int64
	err = tx.QueryRow(`SELECT m.id FROM hosting_machines m WHERE m.pool_key=? AND `+forSaleCondition+` ORDER BY m.id LIMIT 1`, profile.PoolKey).Scan(&machineID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("no machine available for this profile")
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE hosting_machines SET state='reserved', state_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='available' AND tenant_id IS NULL`, "held for "+ref, machineID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("machine was taken; retry")
	}
	r := &Reservation{ID: newID("hrs"), ProfileKey: profile.Key, MachineID: machineID, ExternalRef: ref, Status: "held", ExpiresAt: sqlTime(now().Add(reservationTTL(ctx)))}
	if _, err := tx.Exec(`INSERT INTO hosting_reservations (id, profile_key, machine_id, external_ref, status, expires_at) VALUES (?, ?, ?, ?, 'held', ?)`,
		r.ID, r.ProfileKey, r.MachineID, r.ExternalRef, r.ExpiresAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbReservationGet(db, r.ID)
}

func (a *App) releaseReservation(ctx *sdk.AppCtx, id string, status string) (*Reservation, error) {
	db := ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := dbReservationGet(tx, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("reservation %s not found", id)
	}
	if r.Status != "held" {
		return r, nil
	}
	if _, err := tx.Exec(`UPDATE hosting_reservations SET status=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='held'`, status, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE hosting_machines SET state='available', state_reason='', updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='reserved' AND tenant_id IS NULL`, r.MachineID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbReservationGet(db, id)
}

func (a *App) expireReservations(ctx *sdk.AppCtx) {
	rows, err := ctx.AppDB().Query(`SELECT id FROM hosting_reservations WHERE status='held' AND expires_at<=?`, sqlTime(now()))
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_, _ = a.releaseReservation(ctx, id, "expired")
	}
}

type Availability struct {
	Profile   string `json:"profile"`
	Backend   string `json:"backend"`
	ForSale   bool   `json:"for_sale"`
	Available *int   `json:"available,omitempty"`
	Waiting   int    `json:"waiting,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func availability(db *sql.DB, profile *Profile) (*Availability, error) {
	out := &Availability{Profile: profile.Key, Backend: profile.Backend}
	if profile.Status != "active" {
		out.Reason = "profile is archived"
		return out, nil
	}
	switch profile.Backend {
	case BackendContainer:
		out.ForSale = true
	case BackendMachineOnDemand:
		out.ForSale = true
		out.Reason = "created on demand; provider stock is checked at creation"
	case BackendMachinePool:
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM hosting_machines m WHERE m.pool_key=? AND `+forSaleCondition, profile.PoolKey).Scan(&n); err != nil {
			return nil, err
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM hosting_tenants WHERE profile_key=? AND status='waiting_for_machine'`, profile.Key).Scan(&out.Waiting); err != nil {
			return nil, err
		}
		free := n - out.Waiting
		if free < 0 {
			free = 0
		}
		out.Available = &free
		out.ForSale = free > 0
		if !out.ForSale {
			out.Reason = "no machine available"
		}
	}
	return out, nil
}

// resolveTenant settles a tenant left behind by a failure: its machine is
// quarantined, or its last run failed, so it sits in suspending, resuming,
// releasing or failed. The operator picks the outcome after checking the
// machine:
//
//	resume       restore access and run the tenant again
//	release      remove access, then reset the machine as a normal release
//	force_clean  erase the macOS machine without contacting the customer
//	             account first (when revoke itself keeps failing)
func (a *App) resolveStuckTenant(ctx *sdk.AppCtx, args map[string]any) (*Tenant, *Operation, error) {
	db := ctx.AppDB()
	t, err := dbTenantGet(db, strArg(args, "tenant_id"))
	if err != nil {
		return nil, nil, err
	}
	if t == nil {
		return nil, nil, fmt.Errorf("tenant %s not found", strArg(args, "tenant_id"))
	}
	if !boolArg(args, "reviewed") {
		return nil, nil, errors.New("resolve needs reviewed=true: check the machine and the run log first")
	}
	if err := confirmTenant(t, args); err != nil {
		return nil, nil, err
	}
	if t.Backend == BackendContainer {
		return nil, nil, errors.New("container tenants have no runs to resolve; use suspend, resume or release")
	}
	if t.Status == TenantReleased {
		return nil, nil, fmt.Errorf("tenant %s is already released", t.ID)
	}
	var m *Machine
	if t.MachineID > 0 {
		if m, err = requireMachine(db, t.MachineID); err != nil {
			return nil, nil, err
		}
	}
	stuck := t.Status == TenantFailed || t.Status == TenantSuspending || t.Status == TenantResuming || t.Status == TenantReleasing ||
		(m != nil && m.State == MachineQuarantined)
	if !stuck {
		return nil, nil, fmt.Errorf("tenant %s is %s and its machine is not quarantined; nothing to resolve", t.ID, t.Status)
	}
	if op, _ := activeOperationFor(db, t.ID, 0); op != nil {
		return nil, nil, fmt.Errorf("run %s is still %s for this tenant; quarantine the machine to stop it first", op.ID, op.Status)
	}
	action := strArg(args, "action")
	var kind, transient string
	var steps []string
	switch action {
	case "resume":
		kind, transient = OpResume, TenantResuming
	case "release":
		kind, transient = OpRelease, TenantReleasing
	case "force_clean":
		if m == nil {
			return nil, nil, errors.New("force_clean needs a pool machine")
		}
		pool, err := requirePool(db, m.PoolKey)
		if err != nil {
			return nil, nil, err
		}
		if pool.Platform != "macos" {
			return nil, nil, errors.New("force_clean is only available for macOS pool machines")
		}
		if !m.Validated {
			return nil, nil, fmt.Errorf("machine %d is not validated; erase is refused", m.ID)
		}
		if reason := eraseRefusedReason(m); reason != "" {
			return nil, nil, errors.New("force_clean refused: " + reason + "; use release, then clean the Mac by hand")
		}
		kind, transient = OpRelease, TenantReleasing
		steps = join([]string{"wipe"}, afterResetSteps)
	default:
		return nil, nil, errors.New("action must be resume, release or force_clean")
	}
	who := firstNonEmpty(actor(args), "operator")
	if m != nil {
		// Lift the quarantine so the run may touch the machine again.
		if err := dbMachineSetState(db, m.ID, MachineAssigned, "resolved by "+who+": "+action); err != nil {
			return nil, nil, err
		}
	}
	_ = recordEvent(db, t.ID, "resolved", who, map[string]any{"action": action, "previous_status": t.Status})
	if steps == nil {
		op, err := a.enqueueTenantOp(ctx, t, kind, transient, who)
		if err != nil {
			return nil, nil, err
		}
		t, err = dbTenantGet(db, t.ID)
		return t, op, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	op, err := enqueueOperation(tx, kind, t.ID, t.MachineID, t.InstanceID, steps, who)
	if err != nil {
		return nil, nil, err
	}
	if err := dbTenantSetStatus(tx, t.ID, transient, ""); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	kickOperation(a, ctx, op.ID)
	t, err = dbTenantGet(db, t.ID)
	return t, op, err
}
