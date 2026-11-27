package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// App-managed pools: machines another app drives. The first (and only)
// managing app is Mobile: a pool machine is a phone (device_ref = Mobile's
// device id), the lock on it is an exclusive Mobile session that Hosting
// opens and keeps renewing, and the reset is Mobile's baseline, clean and
// verify_clean. Hosting uses Mobile's tools as they are and never changes
// Mobile.

const managedByMobile = "mobile"

// deviceSessionTTL is the Mobile session length Hosting asks for; the
// minute sweep renews it, so a Hosting outage under an hour keeps the lock.
const deviceSessionTTL = 3600

// cleanBatch is the most apps Mobile's clean accepts in clear_data.
const cleanBatch = 50

// ─── Mobile calls ────────────────────────────────────────────────────────

type mobileDeviceInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Driver     string `json:"driver"`
	OS         string `json:"os"`
	State      string `json:"state"`
	LastSeenAt string `json:"last_seen_at"`
	Health     any    `json:"health,omitempty"`
}

// online: Mobile lists a phone as ready, configured, or busy (in a session).
func (d *mobileDeviceInfo) online() bool {
	return d != nil && (d.State == "ready" || d.State == "configured" || d.State == "busy")
}

type mobileSessionInfo struct {
	ID        string `json:"id"`
	DeviceID  string `json:"device_id"`
	Status    string `json:"status"`
	CallerRef string `json:"caller_ref"`
	EndReason string `json:"end_reason"`
	EndedAt   *int64 `json:"ended_at"`
}

// live: the session still holds the phone (a paused one resumes when the
// phone reconnects, until Mobile's offline grace ends it).
func (s *mobileSessionInfo) live() bool {
	if s == nil || s.EndedAt != nil {
		return false
	}
	return s.Status == "opening" || s.Status == "active" || s.Status == "paused"
}

func mobileCall(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
	if ctx.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	return ctx.PlatformAPI().CallAppResult(managedByMobile, tool, withProject(projectID, args), out)
}

func mobileDevices(ctx *sdk.AppCtx, projectID string) (map[string]*mobileDeviceInfo, error) {
	var out struct {
		Devices []mobileDeviceInfo `json:"devices"`
	}
	if err := mobileCall(ctx, projectID, "mobile_devices", map[string]any{}, &out); err != nil {
		return nil, err
	}
	byID := map[string]*mobileDeviceInfo{}
	for i := range out.Devices {
		byID[out.Devices[i].ID] = &out.Devices[i]
	}
	return byID, nil
}

func mobileSession(ctx *sdk.AppCtx, projectID string, args map[string]any) (*mobileSessionInfo, error) {
	var out struct {
		Session mobileSessionInfo `json:"session"`
	}
	if err := mobileCall(ctx, projectID, "mobile_session", args, &out); err != nil {
		return nil, err
	}
	return &out.Session, nil
}

// deviceBusy: Mobile refused because another session holds the phone.
func deviceBusy(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "exclusive session") || strings.Contains(s, "busy")
}

// sessionGone: Mobile no longer knows the session or it already ended.
func sessionGone(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "session_inactive") || strings.Contains(s, "not active") ||
		strings.Contains(s, "session is ") || strings.Contains(s, "no such session")
}

func setDeviceSession(db querier, machineID int64, sessionID, owner string) error {
	_, err := db.Exec(`UPDATE hosting_machines SET device_session=?, device_session_owner=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, sessionID, owner, machineID)
	return err
}

// openDeviceSession opens an exclusive session on the machine's phone. The
// caller stores it; a session it cannot store must be closed again.
func openDeviceSession(ctx *sdk.AppCtx, projectID string, m *Machine, callerRef string) (*mobileSessionInfo, error) {
	s, err := mobileSession(ctx, projectID, map[string]any{"action": "open", "device_id": m.DeviceRef, "ttl_seconds": deviceSessionTTL, "caller_ref": callerRef})
	if err != nil {
		return nil, err
	}
	if s.ID == "" {
		return nil, errors.New("mobile returned no session id")
	}
	return s, nil
}

// endDeviceSession stops a session; an already ended one is fine.
func endDeviceSession(ctx *sdk.AppCtx, projectID, sessionID, action, reason string) error {
	args := map[string]any{"action": action, "session_id": sessionID}
	if action == "stop" {
		args["reason"], args["stopped_by"] = reason, "hosting"
	}
	var out map[string]any
	if err := mobileCall(ctx, projectID, "mobile_session", args, &out); err != nil && !sessionGone(err) {
		return err
	}
	return nil
}

// ─── Plans ───────────────────────────────────────────────────────────────

// deviceResetSteps return a phone to its baseline and prove it.
var deviceResetSteps = []string{"device_stop", "device_check", "device_open", "device_clean", "device_verify_clean", "device_close"}

func deviceInspectSteps(captureBaseline bool) []string {
	baseline := "device_baseline"
	if captureBaseline {
		baseline = "device_baseline_capture"
	}
	return []string{"device_check", "device_stop", "device_open", baseline, "device_verify_clean", "device_close"}
}

func planDeviceSteps(kind string) ([]string, error) {
	switch kind {
	case OpProvision, OpResume:
		// The session Hosting opens is the tenant's: it stays open.
		return []string{"device_check", "device_open"}, nil
	case OpSuspend:
		return []string{"device_stop"}, nil
	case OpRelease:
		return deviceResetSteps, nil
	case OpRestart, OpReinstall, OpAccessKeys:
		return nil, fmt.Errorf("%s is not available for phones (app-managed pool); use hosting_tenant_swap to move the tenant to another phone", strings.ReplaceAll(kind, "_", " "))
	}
	return nil, fmt.Errorf("unknown operation %s", kind)
}

// ─── Steps ───────────────────────────────────────────────────────────────

func (a *App) runDeviceStep(ctx *sdk.AppCtx, env *stepEnv, step string) error {
	m := env.machine
	if !m.managed() {
		return fmt.Errorf("%s needs a phone from an app-managed pool", step)
	}
	pid := env.projectID
	switch step {
	case "device_check":
		devices, err := mobileDevices(ctx, pid)
		if err != nil {
			return pending("Mobile cannot be reached: " + err.Error())
		}
		d := devices[m.DeviceRef]
		if d == nil {
			return fmt.Errorf("Mobile does not list device %s", m.DeviceRef)
		}
		if d.Platform != "android" || d.Driver != "adb" {
			return fmt.Errorf("device %s is %s/%s; Hosting rents Android phones on the ADB driver only", m.DeviceRef, d.Platform, d.Driver)
		}
		if !d.online() {
			return pending("phone is " + firstNonEmpty(d.State, "offline"))
		}
		return nil
	case "device_open":
		// Provision and resume open the tenant's session; every other run
		// opens one of its own that is never shown to a tenant.
		ref, owner := "hosting:"+env.op.Kind+":"+env.op.ID, ""
		if env.tenant != nil && (env.op.Kind == OpProvision || env.op.Kind == OpResume) {
			ref, owner = "hosting-tenant:"+env.tenant.ID, env.tenant.ID
		}
		if m.DeviceSession != "" {
			s, err := mobileSession(ctx, pid, map[string]any{"action": "get", "session_id": m.DeviceSession})
			if err != nil && !sessionGone(err) {
				return pending("Mobile cannot be reached: " + err.Error())
			}
			if err == nil && s.live() {
				if m.DeviceSessionOwner == owner && s.CallerRef == ref {
					return nil // this run opened it before a restart
				}
				// Someone else's session (a tenant's, or an earlier run's):
				// end it, never reuse it.
				if err := endDeviceSession(ctx, pid, m.DeviceSession, "stop", "hosting "+env.op.Kind); err != nil {
					return pending("Mobile cannot be reached: " + err.Error())
				}
			}
			_ = setDeviceSession(ctx.AppDB(), m.ID, "", "")
		}
		s, err := openDeviceSession(ctx, pid, m, ref)
		if err != nil {
			if deviceBusy(err) {
				return pending("another session holds the phone: " + err.Error())
			}
			return err
		}
		if err := setDeviceSession(ctx.AppDB(), m.ID, s.ID, owner); err != nil {
			_ = endDeviceSession(ctx, pid, s.ID, "close", "")
			return err
		}
		return nil
	case "device_stop", "device_close":
		if m.DeviceSession == "" {
			return nil
		}
		action := map[string]string{"device_stop": "stop", "device_close": "close"}[step]
		if err := endDeviceSession(ctx, pid, m.DeviceSession, action, "hosting "+env.op.Kind); err != nil {
			return pending("Mobile cannot be reached: " + err.Error())
		}
		_, err := ctx.AppDB().Exec(`UPDATE hosting_machines SET device_session='', device_session_owner='', updated_at=CURRENT_TIMESTAMP WHERE id=? AND device_session=?`, m.ID, m.DeviceSession)
		return err
	case "device_baseline", "device_baseline_capture":
		if step == "device_baseline" {
			var got map[string]any
			err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_get", "session_id": m.DeviceSession}, &got)
			if err == nil && len(mapFromAny(got["baseline"])) > 0 {
				return nil
			}
			if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no baseline") && !strings.Contains(err.Error(), "not_found") {
				return err
			}
		}
		var out map[string]any
		return mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_capture", "session_id": m.DeviceSession}, &out)
	case "device_clean":
		var got struct {
			Baseline struct {
				Apps []string `json:"apps"`
			} `json:"baseline"`
		}
		if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_get", "session_id": m.DeviceSession}, &got); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		// The renter may have signed in to apps that were on the phone
		// before: clear the data of every third-party baseline app. Mobile
		// takes at most 50 per call, and verify_clean cannot see app data,
		// so every app must be confirmed cleared here.
		apps := got.Baseline.Apps
		for start := 0; start == 0 || start < len(apps); start += cleanBatch {
			batch := apps[start:min(start+cleanBatch, len(apps))]
			var report struct {
				DataCleared []string `json:"data_cleared"`
			}
			if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "clean", "session_id": m.DeviceSession, "confirm": true, "clear_data": batch}, &report); err != nil {
				return err
			}
			cleared := map[string]bool{}
			for _, a := range report.DataCleared {
				cleared[a] = true
			}
			var missed []string
			for _, a := range batch {
				if !cleared[a] {
					missed = append(missed, a)
				}
			}
			if len(missed) > 0 {
				return fmt.Errorf("could not clear the data of %d baseline app(s): %s", len(missed), tail(strings.Join(missed, ", "), 300))
			}
			if len(apps) == 0 {
				break
			}
		}
		return nil
	case "device_verify_clean":
		var out struct {
			Clean       bool            `json:"clean"`
			Differences json.RawMessage `json:"differences"`
		}
		if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "verify_clean", "session_id": m.DeviceSession}, &out); err != nil {
			return err
		}
		if !out.Clean {
			return fmt.Errorf("phone differs from its baseline: %s", tail(string(out.Differences), 400))
		}
		return nil
	}
	return fmt.Errorf("unknown step %q", step)
}

// ─── Minute sweep: health, session hold, degraded ────────────────────────

func degradedAfter(ctx *sdk.AppCtx) time.Duration {
	mins := 5
	if v := configString(ctx, "degraded_after_minutes", ""); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 1 && n <= 24*60 {
			mins = n
		}
	}
	return time.Duration(mins) * time.Minute
}

func managedMachines(ctx *sdk.AppCtx) ([]*Machine, error) {
	rows, err := ctx.AppDB().Query(machineSelect + ` WHERE device_ref != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// refreshDevices records whether each phone is online, keeps the session of
// every rented phone open, and marks a tenant degraded while its phone is
// offline longer than degraded_after_minutes or held by someone else.
func (a *App) refreshDevices(ctx *sdk.AppCtx) {
	machines, err := managedMachines(ctx)
	if err != nil || len(machines) == 0 {
		return
	}
	db := ctx.AppDB()
	pid := ctx.CurrentProject()
	devices, err := mobileDevices(ctx, pid)
	if err != nil {
		// Off sale without an alert per phone: the phones may be fine,
		// Hosting just cannot ask.
		quiet := 0
		for _, m := range machines {
			if m.Online {
				_, _ = db.Exec(`UPDATE hosting_machines SET online=0, updated_at=CURRENT_TIMESTAMP WHERE id=?`, m.ID)
				a.setQuietOffline(m.ID, true)
				quiet++
			}
		}
		ctx.Logger().Warn("hosting cannot reach Mobile; phones marked offline without alerts", "phones", quiet, "err", err)
		return
	}
	for _, m := range machines {
		d := devices[m.DeviceRef]
		online := d.online()
		seen := m.LastSeenAt
		if online {
			seen = sqlTime(now())
		} else if d != nil {
			if t, ok := parseSQLTime(d.LastSeenAt); ok {
				seen = sqlTime(t)
			}
		}
		if online != m.Online || seen != m.LastSeenAt {
			_, _ = db.Exec(`UPDATE hosting_machines SET online=?, last_seen_at=NULLIF(?, ''), updated_at=CASE WHEN online!=? THEN CURRENT_TIMESTAMP ELSE updated_at END WHERE id=?`,
				boolToInt(online), seen, boolToInt(online), m.ID)
		}
		wasQuiet := a.setQuietOffline(m.ID, false)
		if online != m.Online && !(online && wasQuiet) {
			detail := "not listed by Mobile"
			if d != nil {
				detail = d.State
			}
			emitMachineOnline(ctx, m, online, seen, detail)
		}
		m.Online, m.LastSeenAt = online, seen
		if m.TenantID != "" {
			a.holdRentedDevice(ctx, m)
		}
	}
}

// noRunOn is true while no run is queued or in flight for the tenant or
// the machine. The sweep writes only under it, in the same statement, so a
// release (or any run) that starts during its slow Mobile calls wins.
const noRunOn = `NOT EXISTS (SELECT 1 FROM hosting_operations o WHERE o.status IN ('queued','running','waiting') AND (o.tenant_id=? OR o.machine_id=?))`

// holdRentedDevice keeps the tenant's session on its phone and moves the
// tenant between active and degraded.
func (a *App) holdRentedDevice(ctx *sdk.AppCtx, seen *Machine) {
	db := ctx.AppDB()
	m, err := dbMachineGet(db, seen.ID)
	if err != nil || m == nil || m.TenantID == "" {
		return
	}
	t, err := dbTenantGet(db, m.TenantID)
	if err != nil || t == nil || t.MachineID != m.ID || (t.Status != TenantActive && t.Status != TenantDegraded) {
		return
	}
	if op, _ := activeOperationFor(db, t.ID, m.ID); op != nil {
		return // a run owns the phone right now
	}
	problem, urgent := "", false
	if m.Online {
		problem, urgent = a.keepDeviceSession(ctx, m, t)
	} else {
		problem = "phone offline"
		if since, ok := parseSQLTime(m.LastSeenAt); ok && now().Sub(since) < degradedAfter(ctx) {
			return // a short drop: Mobile pauses the session and resumes it
		}
	}
	setStatus := func(to, reason string) bool {
		res, err := db.Exec(`UPDATE hosting_tenants SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND machine_id=? AND status=? AND `+noRunOn, to, reason, t.ID, m.ID, t.Status, t.ID, m.ID)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n == 1
	}
	switch {
	case problem == "" && t.Status == TenantDegraded:
		if !setStatus(TenantActive, "") {
			return
		}
		_ = recordEvent(db, t.ID, "tenant.recovered", "worker", map[string]any{"machine_id": m.ID, "device_ref": m.DeviceRef})
		ctx.Emit("hosting.tenant.recovered", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "machine_id": m.ID,
			"managed_by": m.ManagedBy, "device_ref": m.DeviceRef})
	case problem != "" && t.Status == TenantActive && (urgent || !m.Online):
		if !setStatus(TenantDegraded, problem) {
			return
		}
		_ = recordEvent(db, t.ID, "tenant.degraded", "worker", map[string]any{"machine_id": m.ID, "device_ref": m.DeviceRef, "reason": problem})
		ctx.Logger().Info("hosting tenant degraded", "tenant_id", t.ID, "machine_id", m.ID, "reason", problem)
		ctx.Emit("hosting.tenant.degraded", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "machine_id": m.ID,
			"managed_by": m.ManagedBy, "device_ref": m.DeviceRef, "reason": problem, "last_seen_at": m.LastSeenAt})
	}
}

// keepDeviceSession renews the tenant's session, or opens a new one when it
// ended (the phone was away past Mobile's offline grace, or it expired). A
// new session id reaches the customer through hosting_tenant_get and
// hosting.tenant.access_updated. urgent: someone else holds the phone.
func (a *App) keepDeviceSession(ctx *sdk.AppCtx, m *Machine, t *Tenant) (problem string, urgent bool) {
	db := ctx.AppDB()
	pid := ctx.CurrentProject()
	old := m.DeviceSession
	if old != "" {
		s, err := mobileSession(ctx, pid, map[string]any{"action": "get", "session_id": old})
		switch {
		case err != nil && !sessionGone(err):
			return "", false // cannot tell; try again next minute
		case err == nil && s.live() && m.DeviceSessionOwner != t.ID:
			// A session Hosting left from a run: never renew or show it. It
			// expires within the hour and the tenant gets its own then.
			return "", false
		case err == nil && s.live():
			if s.Status == "paused" {
				return "phone offline (session paused)", false
			}
			if _, err := mobileSession(ctx, pid, map[string]any{"action": "renew", "session_id": old, "ttl_seconds": deviceSessionTTL}); err != nil {
				ctx.Logger().Info("hosting device session renew failed", "machine_id", m.ID, "err", err)
			}
			return "", false
		}
	}
	s, err := openDeviceSession(ctx, pid, m, "hosting-tenant:"+t.ID)
	if err != nil {
		if deviceBusy(err) {
			return "another session holds the phone", true
		}
		return "cannot open a session on the phone: " + err.Error(), false
	}
	// Stored only if the rental is still live, the machine still has the
	// session we saw, and no run started meanwhile; otherwise give it back.
	res, err := db.Exec(`UPDATE hosting_machines SET device_session=?, device_session_owner=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND tenant_id=? AND device_session=?
		AND EXISTS (SELECT 1 FROM hosting_tenants t WHERE t.id=? AND t.machine_id=? AND t.status IN ('active','degraded'))
		AND `+noRunOn, s.ID, t.ID, m.ID, t.ID, old, t.ID, m.ID, t.ID, m.ID)
	if n := rowsAffected(res, err); n != 1 {
		_ = endDeviceSession(ctx, pid, s.ID, "close", "")
		return "", false
	}
	_ = recordEvent(db, t.ID, "access.session_changed", "worker", map[string]any{"machine_id": m.ID, "reason": "session_reopened"})
	ctx.Emit("hosting.tenant.access_updated", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "machine_id": m.ID,
		"managed_by": m.ManagedBy, "device_ref": m.DeviceRef, "reason": "session_reopened"})
	return "", false
}

func rowsAffected(res sql.Result, err error) int64 {
	if err != nil || res == nil {
		return -1
	}
	n, _ := res.RowsAffected()
	return n
}

// ─── Swap ────────────────────────────────────────────────────────────────

// swapTenantDevice moves a phone tenant to a free phone in the same pool.
// The old phone is quarantined for an operator (clean it, then inspect).
func (a *App) swapTenantDevice(ctx *sdk.AppCtx, args map[string]any) (*Tenant, *Operation, error) {
	db := ctx.AppDB()
	t, err := resolveTenant(db, args)
	if err != nil {
		return nil, nil, err
	}
	if err := confirmTenant(t, args); err != nil {
		return nil, nil, err
	}
	if t.MachineID == 0 {
		return nil, nil, fmt.Errorf("tenant %s has no machine", t.ID)
	}
	old, err := requireMachine(db, t.MachineID)
	if err != nil {
		return nil, nil, err
	}
	if !old.managed() {
		return nil, nil, errors.New("swap is only available for phones (app-managed pools)")
	}
	if t.Status != TenantActive && t.Status != TenantDegraded {
		return nil, nil, fmt.Errorf("cannot swap tenant %s while it is %s", t.ID, t.Status)
	}
	if op, _ := activeOperationFor(db, t.ID, old.ID); op != nil {
		return nil, nil, fmt.Errorf("run %s is still %s for this tenant", op.ID, op.Status)
	}
	reason := firstNonEmpty(strArg(args, "reason"), "swapped")
	who := firstNonEmpty(actor(args), "operator")
	profile, err := requireProfile(db, t.ProfileKey)
	if err != nil {
		return nil, nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var newID int64
	err = tx.QueryRow(`SELECT m.id FROM hosting_machines m WHERE m.pool_key=? AND m.id!=? AND `+forSaleCondition+`
		AND NOT EXISTS (SELECT 1 FROM hosting_reservations r WHERE r.machine_id=m.id AND r.status='held')
		ORDER BY m.id LIMIT 1`, old.PoolKey, old.ID).Scan(&newID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil, fmt.Errorf("no free phone in pool %s to swap to", old.PoolKey)
		}
		return nil, nil, err
	}
	// Claim the tenant first, in the transaction, under the same conditions
	// checked above: a release or another run that started meanwhile wins.
	res, err := tx.Exec(`UPDATE hosting_tenants SET machine_id=?, status='provisioning', last_error='', updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND machine_id=? AND status IN ('active','degraded') AND `+noRunOn, newID, t.ID, old.ID, t.ID, old.ID)
	if n := rowsAffected(res, err); n != 1 {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("tenant %s changed while swapping (a release or another run started); check it and try again", t.ID)
	}
	msg := fmt.Sprintf("swapped away from tenant %s: %s", t.ID, reason)
	if err := fenceMachineOperations(tx, old.ID, "stopped: "+msg); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE hosting_machines SET tenant_id=NULL, state='quarantined', state_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=?`, msg, old.ID, t.ID); err != nil {
		return nil, nil, err
	}
	res, err = tx.Exec(`UPDATE hosting_machines SET state='preparing', state_reason='', tenant_id=?, device_session='', device_session_owner='', updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND tenant_id IS NULL AND state='available'`, t.ID, newID)
	if n := rowsAffected(res, err); n != 1 {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("the free phone was taken by another tenant; retry")
	}
	steps, err := planDeviceSteps(OpProvision)
	if err != nil {
		return nil, nil, err
	}
	op, err := enqueueOperation(tx, OpProvision, t.ID, newID, 0, steps, who)
	if err != nil {
		return nil, nil, err
	}
	// The old phone keeps the customer's session until it is stopped: a run
	// does it, retried while Mobile cannot be reached, and reports a failure.
	stopOp, err := enqueueOperation(tx, OpDeviceStop, "", old.ID, 0, []string{"device_stop"}, who)
	if err != nil {
		return nil, nil, err
	}
	_ = recordEvent(tx, t.ID, "tenant.swapped", who, map[string]any{"from_machine_id": old.ID, "to_machine_id": newID, "reason": reason, "profile": profile.Key})
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	kickOperation(a, ctx, stopOp.ID)
	newM, _ := dbMachineGet(db, newID)
	ctx.Emit("hosting.tenant.swapped", map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "from_machine_id": old.ID, "to_machine_id": newID,
		"from_device_ref": old.DeviceRef, "device_ref": newM.DeviceRef, "managed_by": old.ManagedBy, "reason": reason, "operation_id": op.ID})
	emitMachineQuarantined(ctx, old, msg)
	kickOperation(a, ctx, op.ID)
	t, err = dbTenantGet(db, t.ID)
	return t, op, err
}

func emitMachineQuarantined(ctx *sdk.AppCtx, m *Machine, reason string) {
	ctx.Emit("hosting.machine.quarantined", map[string]any{"machine_id": m.ID, "pool_key": m.PoolKey, "instance_id": m.InstanceID,
		"managed_by": m.ManagedBy, "device_ref": m.DeviceRef, "reason": reason})
}

// ─── Tools ───────────────────────────────────────────────────────────────

// addManagedDevice adds a phone that Mobile lists to an app-managed pool.
// Like any machine it starts quarantined; inspect records its baseline.
func (a *App) addManagedDevice(ctx *sdk.AppCtx, pool *Pool, args map[string]any) (any, error) {
	db := ctx.AppDB()
	ref := strings.TrimSpace(strArg(args, "device_ref"))
	if ref == "" {
		return nil, fmt.Errorf("device_ref required: the device id from mobile_devices (pool %s is managed by %s)", pool.Key, pool.ManagedBy)
	}
	if int64Arg(args, "instance_id") != 0 {
		return nil, errors.New("a phone has no Instances host; pass device_ref only")
	}
	devices, err := mobileDevices(ctx, ctx.CurrentProject())
	if err != nil {
		return nil, fmt.Errorf("mobile: %w", err)
	}
	d := devices[ref]
	if d == nil {
		return nil, fmt.Errorf("Mobile does not list device %q; check mobile_devices", ref)
	}
	if d.Platform != "android" || d.Driver != "adb" {
		return nil, fmt.Errorf("device %s is %s/%s; Hosting rents Android phones on the ADB driver only (clean and verify_clean need ADB)", ref, d.Platform, d.Driver)
	}
	online := d.online()
	res, err := db.Exec(`INSERT INTO hosting_machines (pool_key, device_ref, name, state, state_reason, online, last_seen_at, endpoint_source, serial, expected_os)
		VALUES (?, ?, ?, 'quarantined', 'added; run inspect', ?, CASE WHEN ? THEN CURRENT_TIMESTAMP END, ?, ?, ?)`,
		pool.Key, ref, firstNonEmpty(strArg(args, "name"), d.Name, ref), boolToInt(online), online, pool.ManagedBy,
		strArg(args, "serial"), strArg(args, "expected_os"))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("device %s is already a machine in a pool", ref)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	m, err := dbMachineGet(db, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"machine": m, "device": d}, nil
}

func (a *App) deviceHealth(ctx *sdk.AppCtx, t *Tenant, m *Machine) (any, error) {
	db := ctx.AppDB()
	devices, err := mobileDevices(ctx, ctx.CurrentProject())
	health := "unknown"
	var d *mobileDeviceInfo
	switch {
	case err != nil:
		health = "error"
	default:
		d = devices[m.DeviceRef]
		switch {
		case d == nil:
			health = "missing"
		case d.online():
			health = "ok"
		default:
			health = firstNonEmpty(d.State, "offline")
		}
	}
	_, _ = db.Exec(`UPDATE hosting_tenants SET last_health_status=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, health, t.ID)
	t, _ = dbTenantGet(db, t.ID)
	out := map[string]any{"tenant": t, "device": d}
	if err != nil {
		out["error"] = err.Error()
	}
	return out, nil
}

func (a *App) toolTenantSwap(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, op, err := a.swapTenantDevice(ctx, args)
	if err != nil {
		return nil, err
	}
	return tenantResult(ctx.AppDB(), t, op), nil
}
