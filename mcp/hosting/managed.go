package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
		// The session Hosting opens is the tenant's: it stays open. The
		// handover proves the renter can use the phone (no PIN in the way).
		return []string{"device_check", "device_open", "device_handover"}, nil
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
		captured := step == "device_baseline_capture"
		if !captured {
			var got map[string]any
			err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_get", "session_id": m.DeviceSession}, &got)
			if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no baseline") && !strings.Contains(err.Error(), "not_found") {
				return err
			}
			if err != nil || len(mapFromAny(got["baseline"])) == 0 {
				captured = true
			}
		}
		if captured {
			var out map[string]any
			if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_capture", "session_id": m.DeviceSession}, &out); err != nil {
				return err
			}
		}
		var got struct {
			Baseline phoneBaseline `json:"baseline"`
		}
		if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_get", "session_id": m.DeviceSession}, &got); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		return got.Baseline.saleProblem(captured)
	case "device_handover":
		return a.deviceHandover(ctx, env)
	case "device_clean":
		var got struct {
			Baseline phoneBaseline `json:"baseline"`
		}
		if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "baseline_get", "session_id": m.DeviceSession}, &got); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		systemApps, err := poolSystemApps(ctx.AppDB(), m.PoolKey, env.profile)
		if err != nil {
			return err
		}
		// The renter may have signed in to apps that were on the phone
		// before: clear the data of every third-party baseline app, and of
		// the system apps the pool's profiles list. Mobile takes at most 50
		// per call, and verify_clean cannot see app data, so every app must
		// be confirmed cleared here.
		apps := unionStrings(got.Baseline.Apps, systemApps)
		for start := 0; start == 0 || start < len(apps); start += cleanBatch {
			batch := apps[start:min(start+cleanBatch, len(apps))]
			var report struct {
				Clean       *bool           `json:"clean"`
				NotUndone   json.RawMessage `json:"not_undone"`
				VerifyError string          `json:"verify_error"`
				DataCleared []string        `json:"data_cleared"`
			}
			if err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "clean", "session_id": m.DeviceSession, "confirm": true, "clear_data": batch}, &report); err != nil {
				if strings.Contains(err.Error(), "clean_system_apps") || strings.Contains(err.Error(), "invalid_argument") {
					return fmt.Errorf("Mobile refused clear_data (every system app in a profile's clear_system_apps must also be in Mobile's clean_system_apps setting): %w", err)
				}
				return err
			}
			// Mobile lists what it could not put back (a new PIN, a Bluetooth
			// pairing): never sell the phone on with those.
			if notUndone := strings.TrimSpace(string(report.NotUndone)); (report.Clean != nil && !*report.Clean) || (notUndone != "" && notUndone != "null" && notUndone != "[]") || report.VerifyError != "" {
				return fmt.Errorf("Mobile could not undo everything on the phone: not_undone=%s verify_error=%q", tail(firstNonEmpty(notUndone, "[]"), 400), report.VerifyError)
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

// ─── Baseline and handover checks ────────────────────────────────────────

// minBaselineCoverage is the baseline coverage Mobile 0.12.0 records (Wi-Fi,
// accessibility, admins, users, lock, ...). An older baseline cannot prove
// a phone is back to the state it was sold in.
const minBaselineCoverage = 2

type phoneBaseline struct {
	Apps     []string `json:"apps"`
	Coverage int      `json:"coverage"`
	Lock     string   `json:"lock"`
	Users    int      `json:"users"`
}

// saleProblem is why inspect must not put the phone on sale, or nil.
func (b phoneBaseline) saleProblem(captured bool) error {
	switch {
	case b.Coverage < minBaselineCoverage && captured:
		return fmt.Errorf("Mobile recorded a coverage %d baseline; Hosting needs coverage %d (Mobile 0.12.0 or later)", b.Coverage, minBaselineCoverage)
	case b.Coverage < minBaselineCoverage:
		return fmt.Errorf("the phone's baseline is from an older Mobile (coverage %d, need %d); run inspect with capture_baseline=true", b.Coverage, minBaselineCoverage)
	case b.Lock == "secure":
		return errors.New("the phone has a PIN, pattern or password (baseline lock: secure); remove it, then run inspect with capture_baseline=true")
	case b.Users > 1:
		return fmt.Errorf("the phone has %d users; remove the extra users, then run inspect with capture_baseline=true", b.Users)
	}
	return nil
}

// handoverError: the phone cannot be handed to the renter (a PIN, pattern or
// password, or a lock state Mobile cannot read). failOperation moves the
// tenant to another free phone when the pool has one.
type handoverError struct{ reason, detail string }

func (e *handoverError) Error() string { return e.reason + ": " + e.detail }

// deviceHandover wakes the phone in the tenant's session and refuses it
// while a lock screen with a secret stands in the way.
func (a *App) deviceHandover(ctx *sdk.AppCtx, env *stepEnv) error {
	if !env.profile.phone().requireUnlocked() {
		return nil
	}
	m, pid := env.machine, env.projectID
	if m.DeviceSession == "" {
		// A retried handover: the failed one ended the renter's session.
		if err := a.runDeviceStep(ctx, env, "device_open"); err != nil {
			return err
		}
		fresh, err := dbMachineGet(ctx.AppDB(), m.ID)
		if err != nil || fresh == nil {
			return fmt.Errorf("reload machine %d: %v", m.ID, err)
		}
		m = fresh
	}
	var out struct {
		OK       bool   `json:"ok"`
		ScreenOn bool   `json:"screen_on"`
		Keyguard string `json:"keyguard"`
		Locked   *bool  `json:"locked"`
	}
	var fail *handoverError
	err := mobileCall(ctx, pid, "mobile_device", map[string]any{"action": "wake", "session_id": m.DeviceSession}, &out)
	if err != nil {
		s := strings.ToLower(err.Error())
		switch {
		case strings.Contains(s, "device_locked") || strings.Contains(s, "locked with a pin") || strings.Contains(s, "lock screen did not go away"):
			fail = &handoverError{"phone_locked", err.Error()}
		case strings.Contains(s, "unsupported") || strings.Contains(s, "could not tell whether"):
			fail = &handoverError{"lock_unknown", err.Error()}
		default:
			return pending("Mobile could not wake the phone: " + err.Error())
		}
	} else if !out.ScreenOn || out.Locked == nil || *out.Locked {
		fail = &handoverError{"phone_locked", fmt.Sprintf("after wake: screen_on=%t keyguard=%q", out.ScreenOn, out.Keyguard)}
	}
	if fail == nil {
		return nil
	}
	// The renter never gets a locked phone: end the session handed to them.
	if m.DeviceSession != "" {
		if err := endDeviceSession(ctx, pid, m.DeviceSession, "stop", "hosting handover failed"); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE hosting_machines SET device_session='', device_session_owner='', updated_at=CURRENT_TIMESTAMP WHERE id=? AND device_session=?`, m.ID, m.DeviceSession)
		}
	}
	return fail
}

// handoverFailed runs after failOperation for a failed handover: it moves the
// tenant to another free phone in the pool, or leaves the run failed for an
// operator (a resume goes back to suspended: the renter has no access).
func (a *App) handoverFailed(ctx *sdk.AppCtx, op *Operation, he *handoverError) {
	db := ctx.AppDB()
	t, _ := dbTenantGet(db, op.TenantID)
	old, _ := dbMachineGet(db, op.MachineID)
	if t == nil || old == nil || t.MachineID != old.ID {
		return
	}
	payload := map[string]any{"tenant_id": t.ID, "external_ref": t.ExternalRef, "machine_id": old.ID, "managed_by": old.ManagedBy,
		"device_ref": old.DeviceRef, "reason": he.reason, "detail": he.detail, "operation_id": op.ID, "kind": op.Kind}
	msg := "handover failed for tenant " + t.ID + ": " + he.Error()
	newOp, stopOp, newID, err := a.relocateTenant(ctx, t, old, op.Kind, []string{TenantFailed, TenantProvisioning, TenantResuming}, msg, "hosting",
		"tenant.handover_failed", map[string]any{"reason": he.reason, "detail": he.detail, "from_machine_id": old.ID})
	if err == nil {
		newM, _ := dbMachineGet(db, newID)
		payload["moved"], payload["to_machine_id"], payload["new_operation_id"] = true, newID, newOp.ID
		if newM != nil {
			payload["to_device_ref"] = newM.DeviceRef
		}
		ctx.Logger().Info("hosting handover failed; tenant moved to another phone", "tenant_id", t.ID, "from", old.ID, "to", newID, "reason", he.reason)
		ctx.Emit("hosting.tenant.handover_failed", payload)
		kickOperation(a, ctx, stopOp.ID)
		kickOperation(a, ctx, newOp.ID)
		return
	}
	payload["moved"], payload["not_moved_reason"] = false, err.Error()
	if op.Kind == OpResume {
		_, _ = db.Exec(`UPDATE hosting_tenants SET status=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND machine_id=? AND status=?`, TenantSuspended, t.ID, old.ID, TenantResuming)
	}
	_ = recordEvent(db, t.ID, "tenant.handover_failed", "hosting", map[string]any{"reason": he.reason, "detail": he.detail, "machine_id": old.ID, "not_moved": err.Error()})
	ctx.Logger().Warn("hosting handover failed; no other phone", "tenant_id", t.ID, "machine_id", old.ID, "reason", he.reason, "err", err)
	ctx.Emit("hosting.tenant.handover_failed", payload)
}

// poolSystemApps are the system apps a clean on this pool clears: every
// active profile's clear_system_apps, plus the tenant's own profile.
func poolSystemApps(db *sql.DB, poolKey string, profile *Profile) ([]string, error) {
	rows, err := db.Query(`SELECT settings_json FROM hosting_profiles WHERE pool_key=? AND status='active' ORDER BY key`, poolKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = unionStrings(out, (&Profile{Settings: json.RawMessage(raw)}).phone().ClearSystemApps)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if profile != nil && profile.PoolKey == poolKey {
		out = unionStrings(out, profile.phone().ClearSystemApps)
	}
	return out, nil
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

var androidPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// validatePhoneSettings checks the phone settings of a profile; pool is nil
// for a backend without a pool. Phone settings only fit phone pools.
func validatePhoneSettings(p *Profile, pool *Pool) error {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(p.Settings, &raw)
	has := false
	for _, k := range []string{"require_unlocked_handover", "wipe_mode", "clear_system_apps"} {
		if _, ok := raw[k]; ok {
			has = true
		}
	}
	if !has {
		return nil
	}
	if pool == nil || pool.ManagedBy != managedByMobile {
		return errors.New("require_unlocked_handover, wipe_mode and clear_system_apps are settings for phone pools (managed_by mobile)")
	}
	var s PhoneSettings
	if err := json.Unmarshal(p.Settings, &s); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	switch s.WipeMode {
	case "", "deep_clean":
	case "ephemeral_user":
		return errors.New("wipe_mode ephemeral_user is not available: Mobile has no throwaway-user sessions; use deep_clean")
	default:
		return fmt.Errorf("wipe_mode must be deep_clean, not %q", s.WipeMode)
	}
	if len(s.ClearSystemApps) > 200 {
		return errors.New("clear_system_apps takes at most 200 apps")
	}
	for _, app := range s.ClearSystemApps {
		if !androidPackage.MatchString(app) {
			return fmt.Errorf("clear_system_apps: %q is not an Android package name", app)
		}
	}
	return nil
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
	msg := fmt.Sprintf("swapped away from tenant %s: %s", t.ID, reason)
	op, stopOp, newID, err := a.relocateTenant(ctx, t, old, OpProvision, []string{TenantActive, TenantDegraded}, msg, who,
		"tenant.swapped", map[string]any{"from_machine_id": old.ID, "reason": reason, "profile": profile.Key})
	if err != nil {
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

// relocateTenant moves a phone tenant off old onto a free phone in the same
// pool and queues kind there (provision or resume, both open a fresh
// session). The old phone is quarantined with msg and an OpDeviceStop run
// ends the session it still has. The tenant must still be on old in one of
// statuses with no run queued or in flight: a release or another run that
// started meanwhile wins. The caller kicks both runs.
func (a *App) relocateTenant(ctx *sdk.AppCtx, t *Tenant, old *Machine, kind string, statuses []string, msg, who, eventKind string, eventData map[string]any) (op, stopOp *Operation, newID int64, err error) {
	transient := map[string]string{OpProvision: TenantProvisioning, OpResume: TenantResuming}[kind]
	if transient == "" {
		return nil, nil, 0, fmt.Errorf("cannot move a tenant with a %s run", kind)
	}
	db := ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT m.id FROM hosting_machines m WHERE m.pool_key=? AND m.id!=? AND `+forSaleCondition+`
		AND NOT EXISTS (SELECT 1 FROM hosting_reservations r WHERE r.machine_id=m.id AND r.status='held')
		ORDER BY m.id LIMIT 1`, old.PoolKey, old.ID).Scan(&newID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, 0, fmt.Errorf("no free phone in pool %s to move to", old.PoolKey)
		}
		return nil, nil, 0, err
	}
	args := []any{newID, transient, t.ID, old.ID}
	for _, st := range statuses {
		args = append(args, st)
	}
	args = append(args, t.ID, old.ID)
	res, err := tx.Exec(`UPDATE hosting_tenants SET machine_id=?, status=?, last_error='', updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND machine_id=? AND status IN (?`+strings.Repeat(",?", len(statuses)-1)+`) AND `+noRunOn, args...)
	if n := rowsAffected(res, err); n != 1 {
		if err != nil {
			return nil, nil, 0, err
		}
		return nil, nil, 0, fmt.Errorf("tenant %s changed while moving it (a release or another run started); check it and try again", t.ID)
	}
	if err := fenceMachineOperations(tx, old.ID, "stopped: "+msg); err != nil {
		return nil, nil, 0, err
	}
	if _, err := tx.Exec(`UPDATE hosting_machines SET tenant_id=NULL, state='quarantined', state_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=?`, msg, old.ID, t.ID); err != nil {
		return nil, nil, 0, err
	}
	res, err = tx.Exec(`UPDATE hosting_machines SET state=?, state_reason='', tenant_id=?, device_session='', device_session_owner='', updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND tenant_id IS NULL AND state='available'`, MachinePreparing, t.ID, newID)
	if n := rowsAffected(res, err); n != 1 {
		if err != nil {
			return nil, nil, 0, err
		}
		return nil, nil, 0, errors.New("the free phone was taken by another tenant; retry")
	}
	steps, err := planDeviceSteps(kind)
	if err != nil {
		return nil, nil, 0, err
	}
	if op, err = enqueueOperation(tx, kind, t.ID, newID, 0, steps, who); err != nil {
		return nil, nil, 0, err
	}
	// The old phone keeps the customer's session until it is stopped: a run
	// does it, retried while Mobile cannot be reached, and reports a failure.
	if stopOp, err = enqueueOperation(tx, OpDeviceStop, "", old.ID, 0, []string{"device_stop"}, who); err != nil {
		return nil, nil, 0, err
	}
	data := map[string]any{"to_machine_id": newID}
	for k, v := range eventData {
		data[k] = v
	}
	_ = recordEvent(tx, t.ID, eventKind, who, data)
	if err := tx.Commit(); err != nil {
		return nil, nil, 0, err
	}
	return op, stopOp, newID, nil
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
