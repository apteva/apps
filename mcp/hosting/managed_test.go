package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Fake Mobile (as of Mobile 0.10.0, unchanged by Hosting) ─────────────

type fakeSession struct {
	ID, Device, Status, CallerRef, EndReason string
	Ended                                    bool
}

type fakeMobile struct {
	devices   map[string]*mobileDeviceInfo
	sessions  map[string]*fakeSession
	order     []string
	next      int
	baselines map[string][]string // device → third-party apps at capture
	installed map[string][]string // device → apps added since
	stuck     map[string]bool     // clean leaves the phone dirty
	cleans    []map[string]any
	down      bool
}

func newFakeMobile(ids ...string) *fakeMobile {
	f := &fakeMobile{devices: map[string]*mobileDeviceInfo{}, sessions: map[string]*fakeSession{}, baselines: map[string][]string{},
		installed: map[string][]string{}, stuck: map[string]bool{}}
	for _, id := range ids {
		f.devices[id] = &mobileDeviceInfo{ID: id, Name: "Pixel " + id, Platform: "android", Driver: "adb", State: "ready"}
	}
	return f
}

func (f *fakeMobile) liveOn(device string) *fakeSession {
	for _, id := range f.order {
		if s := f.sessions[id]; s.Device == device && !s.Ended {
			return s
		}
	}
	return nil
}

func (f *fakeMobile) open(device, ref string) (*fakeSession, error) {
	d := f.devices[device]
	if d == nil || !d.online() {
		return nil, errors.New("device_unavailable: no matching device; inspect mobile_devices for setup diagnostics")
	}
	if f.liveOn(device) != nil {
		return nil, errors.New("device_unavailable: device already has an exclusive session")
	}
	f.next++
	s := &fakeSession{ID: fmt.Sprintf("ses_%d", f.next), Device: device, Status: "active", CallerRef: ref}
	f.sessions[s.ID] = s
	f.order = append(f.order, s.ID)
	return s, nil
}

func (f *fakeMobile) end(id, status string) {
	if s := f.sessions[id]; s != nil && !s.Ended {
		s.Ended, s.Status, s.EndReason = true, status, status
	}
}

func (f *fakeMobile) setOnline(device string, online bool) {
	d := f.devices[device]
	d.State = map[bool]string{true: "ready", false: "offline"}[online]
	if s := f.liveOn(device); s != nil {
		s.Status = map[bool]string{true: "active", false: "paused"}[online]
	}
}

func (f *fakeMobile) view(s *fakeSession) map[string]any {
	v := map[string]any{"id": s.ID, "device_id": s.Device, "status": s.Status, "caller_ref": s.CallerRef, "mode": "screen"}
	if s.Ended {
		v["ended_at"] = 1
		v["end_reason"] = s.EndReason
	}
	return v
}

func (f *fakeMobile) handle(tool string, in map[string]any) (any, error) {
	if f.down {
		return nil, errors.New("dial tcp 127.0.0.1:42000: connection refused")
	}
	switch tool {
	case "mobile_devices":
		var list []mobileDeviceInfo
		for _, d := range f.devices {
			list = append(list, *d)
		}
		return map[string]any{"devices": list}, nil
	case "mobile_session":
		sid := strFromAny(in["session_id"])
		s := f.sessions[sid]
		switch strFromAny(in["action"]) {
		case "open":
			ns, err := f.open(strFromAny(in["device_id"]), strFromAny(in["caller_ref"]))
			if err != nil {
				return nil, err
			}
			return map[string]any{"session": f.view(ns)}, nil
		case "get":
			if s == nil {
				return nil, errors.New("not_found: session not found")
			}
			return map[string]any{"session": f.view(s)}, nil
		case "renew":
			if s == nil || s.Ended {
				return nil, errors.New("session_inactive: session is closed")
			}
			if s.Status == "paused" {
				return nil, errors.New("device_unavailable: the phone is offline; the session is paused until it reconnects")
			}
			return map[string]any{"session": f.view(s)}, nil
		case "close", "stop":
			if s == nil {
				return nil, errors.New("not_found: session not found")
			}
			f.end(sid, map[string]string{"close": "closed", "stop": "stopped"}[strFromAny(in["action"])])
			if in["action"] == "stop" {
				return map[string]any{"stopped": []string{sid}, "count": 1}, nil
			}
			return map[string]any{"session": f.view(s)}, nil
		}
	case "mobile_device":
		s := f.sessions[strFromAny(in["session_id"])]
		if s == nil || s.Ended || s.Status != "active" {
			return nil, errors.New("session_inactive: session is not active")
		}
		dev := s.Device
		switch strFromAny(in["action"]) {
		case "baseline_capture":
			f.baselines[dev] = []string{"com.whatsapp", "org.mozilla.firefox"}
			f.installed[dev] = nil
			return map[string]any{"baseline": map[string]any{"apps": f.baselines[dev]}}, nil
		case "baseline_get":
			b, ok := f.baselines[dev]
			if !ok {
				return nil, errors.New("not_found: no baseline for this phone; run baseline_capture first")
			}
			return map[string]any{"baseline": map[string]any{"apps": b}}, nil
		case "clean":
			if in["confirm"] != true {
				return nil, errors.New("invalid_argument: pass confirm=true")
			}
			f.cleans = append(f.cleans, in)
			if !f.stuck[dev] {
				f.installed[dev] = nil
			}
			return map[string]any{"uninstalled": []string{}}, nil
		case "verify_clean":
			var diff []map[string]any
			for _, a := range f.installed[dev] {
				diff = append(diff, map[string]any{"kind": "app_added", "value": a})
			}
			return map[string]any{"clean": len(diff) == 0, "differences": diff}, nil
		}
	}
	return nil, fmt.Errorf("unexpected mobile call %s %v", tool, in)
}

// ─── Helpers ─────────────────────────────────────────────────────────────

// setupPhonePool makes a validated, inspected phone pool "phones" with a
// profile "phone" and returns the machine ids.
func setupPhonePool(t *testing.T, a *App, ctx *sdk.AppCtx, stub *platformStub, devices ...string) []int64 {
	t.Helper()
	if stub.mobile == nil {
		stub.mobile = newFakeMobile(devices...)
	}
	call(t, a.toolPoolUpsert, ctx, map[string]any{"key": "phones", "name": "Phones", "managed_by": "mobile"})
	call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "phones", "enabled": true})
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "phone", "backend": "machine_pool", "product_key": "ssh-access", "pool_key": "phones"})
	var ids []int64
	for _, d := range devices {
		id := int64Arg(mapFromAny(call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "phones", "device_ref": d})["machine"]), "id")
		call(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": id, "validated": true, "confirm": fmt.Sprint(id)})
		call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": id})
		if m := machine(t, ctx.AppDB(), id); m.State != MachineAvailable || m.DeviceSession != "" {
			t.Fatalf("phone %d after inspect = %s (%s), session %q", id, m.State, m.StateReason, m.DeviceSession)
		}
		ids = append(ids, id)
	}
	return ids
}

func lastMobile(stub *platformStub, action string) map[string]any {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for i := len(stub.calls) - 1; i >= 0; i-- {
		if c := stub.calls[i]; c.App == "mobile" && strFromAny(c.Input["action"]) == action {
			return c.Input
		}
	}
	return nil
}

// ─── Tests ───────────────────────────────────────────────────────────────

func TestPhonePoolEndToEnd(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1", "android:p2")
	fm := stub.mobile
	if len(fm.baselines["android:p1"]) == 0 || fm.liveOn("android:p1") != nil {
		t.Fatalf("inspect must record a baseline and close its session: %v", fm.sessions)
	}
	ev := recordEvents(ctx)

	// Rent: Hosting holds an exclusive session for the customer.
	out := call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-p", "profile": "phone", "owner_email": "p@example.com", "ssh_keys": []any{testKey}})
	id := strArg(mapFromAny(out["tenant"]), "id")
	tn := tenant(t, db, id)
	if tn.Status != TenantActive || tn.MachineID != ids[0] || tn.Device == nil || tn.Device.SessionID == "" || tn.Access != nil || tn.SSHUser != "" {
		t.Fatalf("rented phone tenant = %+v device %+v", tn, tn.Device)
	}
	sess := tn.Device.SessionID
	if s := fm.sessions[sess]; s == nil || s.CallerRef != "hosting-tenant:"+id || s.Ended {
		t.Fatalf("session = %+v", s)
	}
	if got := ev.take("hosting.tenant.active"); len(got) != 1 || got[0]["device_ref"] != "android:p1" || got[0]["managed_by"] != "mobile" {
		t.Fatalf("active events = %v", got)
	}
	if _, err := fm.open("android:p1", "someone-else"); err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("another caller opened the rented phone: %v", err)
	}
	if m := machine(t, db, ids[0]); m.State != MachineAssigned || m.DeviceSession != sess {
		t.Fatalf("machine = %s session %q", m.State, m.DeviceSession)
	}

	// Each minute the session is renewed.
	_ = a.tick(ctx)
	if r := lastMobile(stub, "renew"); r == nil || r["session_id"] != sess || r["ttl_seconds"] != float64(deviceSessionTTL) && r["ttl_seconds"] != deviceSessionTTL {
		t.Fatalf("renew = %v", r)
	}

	// Short drop: offline alert, tenant still active. Long drop: degraded.
	fm.setOnline("android:p1", false)
	_ = a.tick(ctx)
	if got := ev.take("hosting.machine.offline"); len(got) != 1 {
		t.Fatalf("offline events = %v", got)
	}
	if tenant(t, db, id).Status != TenantActive {
		t.Fatal("a short drop must not degrade the tenant")
	}
	advanceClock(t, 6*time.Minute)
	_ = a.tick(ctx)
	if tn := tenant(t, db, id); tn.Status != TenantDegraded || tn.Device.SessionID != sess {
		t.Fatalf("after 6 min offline: %s session %q", tn.Status, tn.Device.SessionID)
	}
	if got := ev.take("hosting.tenant.degraded"); len(got) != 1 || got[0]["reason"] != "phone offline" {
		t.Fatalf("degraded events = %v", got)
	}
	_ = a.tick(ctx)
	if got := ev.take("hosting.tenant.degraded"); len(got) != 0 {
		t.Fatalf("degraded must fire once: %v", got)
	}
	// Suspend works from degraded? Not here: it comes back first.
	fm.setOnline("android:p1", true)
	_ = a.tick(ctx)
	if tn := tenant(t, db, id); tn.Status != TenantActive || tn.Device.SessionID != sess {
		t.Fatalf("back online: %s session %q", tn.Status, tn.Device.SessionID)
	}
	if got := ev.take("hosting.tenant.recovered"); len(got) != 1 {
		t.Fatalf("recovered events = %v", got)
	}

	// Mobile ended the session (device_lost): Hosting opens a new one.
	fm.end(sess, "device_lost")
	_ = a.tick(ctx)
	tn = tenant(t, db, id)
	if tn.Status != TenantActive || tn.Device.SessionID == "" || tn.Device.SessionID == sess {
		t.Fatalf("after device_lost: %s session %q", tn.Status, tn.Device.SessionID)
	}
	if got := ev.take("hosting.tenant.access_updated"); len(got) != 1 || got[0]["reason"] != "session_reopened" {
		t.Fatalf("access_updated events = %v", got)
	}
	sess = tn.Device.SessionID

	// Someone else took the phone while the session was gone: degraded at once.
	fm.end(sess, "expired")
	foreign, err := fm.open("android:p1", "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	_ = a.tick(ctx)
	if tn := tenant(t, db, id); tn.Status != TenantDegraded || !strings.Contains(tn.LastError, "another session") {
		t.Fatalf("phone taken: %s (%s)", tn.Status, tn.LastError)
	}

	// Swap to the other phone; the old one is quarantined.
	callErr(t, a.toolTenantSwap, ctx, map[string]any{"tenant_id": id, "confirm": "nope"}, "confirm must be the tenant id")
	call(t, a.toolTenantSwap, ctx, map[string]any{"tenant_id": id, "confirm": id, "reason": "phone taken"})
	tn = tenant(t, db, id)
	if tn.Status != TenantActive || tn.MachineID != ids[1] || tn.Device.DeviceRef != "android:p2" || tn.Device.SessionID == "" {
		t.Fatalf("after swap: %+v device %+v", tn, tn.Device)
	}
	if m := machine(t, db, ids[0]); m.State != MachineQuarantined || m.TenantID != "" {
		t.Fatalf("old phone = %s tenant %q", m.State, m.TenantID)
	}
	if got := ev.take("hosting.tenant.swapped"); len(got) != 1 || got[0]["from_device_ref"] != "android:p1" || got[0]["device_ref"] != "android:p2" {
		t.Fatalf("swapped events = %v", got)
	}
	if got := ev.take("hosting.machine.quarantined"); len(got) != 1 || got[0]["machine_id"] != float64(ids[0]) {
		t.Fatalf("quarantined events = %v", got)
	}

	// Release: the renter installed an app; it is cleaned and verified.
	fm.installed["android:p2"] = []string{"com.example.renter"}
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	if tn := tenant(t, db, id); tn.Status != TenantReleased || tn.Device == nil || tn.Device.SessionID != "" {
		t.Fatalf("released tenant = %s device %+v", tn.Status, tn.Device)
	}
	if m := machine(t, db, ids[1]); m.State != MachineAvailable || m.TenantID != "" || m.DeviceSession != "" {
		t.Fatalf("released phone = %s (%s) session %q", m.State, m.StateReason, m.DeviceSession)
	}
	if len(fm.cleans) != 1 || fmt.Sprint(fm.cleans[0]["clear_data"]) != "[com.whatsapp org.mozilla.firefox]" {
		t.Fatalf("clean calls = %v", fm.cleans)
	}
	if fm.liveOn("android:p2") != nil {
		t.Fatal("release must close Hosting's session")
	}

	// The old phone comes back after the other user leaves and a clean.
	fm.end(foreign.ID, "closed")
	call(t, a.toolMachineClean, ctx, map[string]any{"machine_id": ids[0], "confirm": fmt.Sprint(ids[0])})
	if m := machine(t, db, ids[0]); m.State != MachineAvailable {
		t.Fatalf("cleaned phone = %s (%s)", m.State, m.StateReason)
	}
	av := mapFromAny(call(t, a.toolAvailability, ctx, map[string]any{"profile": "phone"})["availability"])
	if av["machines"] != float64(2) {
		t.Fatalf("availability = %v", av)
	}
}

func TestPhoneReleaseLeftDirtyIsQuarantined(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-d", "profile": "phone", "owner_email": "d@example.com"})["tenant"]), "id")
	ev := recordEvents(ctx)
	fm.installed["android:p1"] = []string{"com.example.stubborn"}
	fm.stuck["android:p1"] = true
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	m := machine(t, db, ids[0])
	if m.State != MachineQuarantined || !strings.Contains(m.StateReason, "differs from its baseline") || !strings.Contains(m.StateReason, "com.example.stubborn") {
		t.Fatalf("dirty phone = %s (%s)", m.State, m.StateReason)
	}
	if got := ev.take("hosting.machine.quarantined"); len(got) != 1 {
		t.Fatalf("quarantined events = %v", got)
	}
	if got := ev.take("hosting.operation.failed"); len(got) != 1 || got[0]["step"] != "device_verify_clean" {
		t.Fatalf("failed events = %v", got)
	}
	if av := mapFromAny(call(t, a.toolAvailability, ctx, map[string]any{"profile": "phone"})["availability"]); av["machines"] != float64(0) {
		t.Fatalf("a dirty phone must not be sold: %v", av)
	}
}

func TestPhoneSuspendResumeAndWaitingLine(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	first := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-1", "profile": "phone", "owner_email": "1@example.com"})["tenant"]), "id")
	second := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-2", "profile": "phone", "owner_email": "2@example.com"})["tenant"]), "id")
	if tenant(t, db, second).Status != TenantWaitingForMachine {
		t.Fatal("second renter must wait for the only phone")
	}
	sess := tenant(t, db, first).Device.SessionID
	call(t, a.toolTenantSuspend, ctx, map[string]any{"tenant_id": first})
	if tn := tenant(t, db, first); tn.Status != TenantSuspended || tn.Device.SessionID != "" || !fm.sessions[sess].Ended || fm.sessions[sess].Status != "stopped" {
		t.Fatalf("suspended: %s session %q mobile %+v", tn.Status, tn.Device.SessionID, fm.sessions[sess])
	}
	_ = a.tick(ctx)
	if fm.liveOn("android:p1") != nil {
		t.Fatal("the sweep must not hold a suspended tenant's phone")
	}
	call(t, a.toolTenantResume, ctx, map[string]any{"tenant_id": first})
	if tn := tenant(t, db, first); tn.Status != TenantActive || tn.Device.SessionID == "" || tn.Device.SessionID == sess {
		t.Fatalf("resumed: %s session %q", tn.Status, tn.Device.SessionID)
	}
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": first, "confirm": first})
	if tn := tenant(t, db, second); tn.Status != TenantActive || tn.MachineID != ids[0] {
		t.Fatalf("waiting renter after release: %s machine %d", tn.Status, tn.MachineID)
	}
}

func TestPhoneBusyAtProvisionWaits(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	other, _ := fm.open("android:p1", "tester")
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-b", "profile": "phone", "owner_email": "b@example.com"})["tenant"]), "id")
	if tn := tenant(t, db, id); tn.Status != TenantProvisioning {
		t.Fatalf("busy phone: %s", tn.Status)
	}
	fm.end(other.ID, "closed")
	advanceClock(t, time.Minute)
	_ = a.tick(ctx)
	if tn := tenant(t, db, id); tn.Status != TenantActive {
		t.Fatalf("after the other session closed: %s (%s)", tn.Status, tn.LastError)
	}
}

func TestPhonePoolRefusals(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	callErr(t, a.toolPoolUpsert, ctx, map[string]any{"key": "droids", "platform": "android"}, "managed_by=mobile")
	callErr(t, a.toolPoolUpsert, ctx, map[string]any{"key": "droids", "managed_by": "mobile", "platform": "macos"}, "platform android")
	callErr(t, a.toolPoolUpsert, ctx, map[string]any{"key": "droids", "managed_by": "robots"}, "managed_by must be")
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	stub.mobile.devices["ios:x"] = &mobileDeviceInfo{ID: "ios:x", Platform: "ios", Driver: "xctest", State: "ready"}
	callErr(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "phones", "device_ref": "android:nope"}, "does not list device")
	callErr(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "phones", "device_ref": "ios:x"}, "ADB driver only")
	callErr(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "phones", "device_ref": "android:p1"}, "already a machine")
	callErr(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "phones"}, "device_ref required")
	callErr(t, a.toolPoolUpsert, ctx, map[string]any{"key": "phones", "platform": "linux"}, "cannot change")
	callErr(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": ids[0], "public_host": "x.example.com"}, "no SSH endpoint")

	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-r", "profile": "phone", "owner_email": "r@example.com"})["tenant"]), "id")
	callErr(t, a.toolTenantRestart, ctx, map[string]any{"tenant_id": id}, "not available for phones")
	callErr(t, a.toolTenantReinstall, ctx, map[string]any{"tenant_id": id, "confirm": id}, "not available for phones")
	callErr(t, a.toolTenantAccessKeysSet, ctx, map[string]any{"tenant_id": id, "ssh_keys": []any{testKey}}, "not available for phones")
	callErr(t, a.toolTenantSwap, ctx, map[string]any{"tenant_id": id, "confirm": id}, "no free phone")

	// Mobile unreachable: phones go off sale without alerts.
	ev := recordEvents(ctx)
	stub.mobile.down = true
	_ = a.tick(ctx)
	if m := machine(t, db, ids[0]); m.Online {
		t.Fatal("phone must be marked offline when Mobile cannot be reached")
	}
	if got := ev.take("hosting.machine.offline"); len(got) != 0 {
		t.Fatalf("no alert when Mobile is down: %v", got)
	}
	stub.mobile.down = false
	_ = a.tick(ctx)
	if got := ev.take("hosting.machine.online"); len(got) != 0 {
		t.Fatalf("no alert when Mobile is back: %v", got)
	}
	if !machine(t, db, ids[0]).Online {
		t.Fatal("phone back online")
	}
}

func TestPhoneHealthAndTenantView(t *testing.T) {
	stub := newStub()
	a, ctx, _ := newTestCtx(t, stub)
	setupPhonePool(t, a, ctx, stub, "android:p1")
	id := strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": "acct-h", "profile": "phone", "owner_email": "h@example.com"})["tenant"]), "id")
	out := call(t, a.toolTenantHealth, ctx, map[string]any{"tenant_id": id})
	if mapFromAny(out["tenant"])["last_health_status"] != "ok" || mapFromAny(out["device"])["id"] != "android:p1" {
		t.Fatalf("health = %v", out)
	}
	got := mapFromAny(mapFromAny(call(t, a.toolTenantGet, ctx, map[string]any{"tenant_id": id})["tenant"])["device"])
	if got["managed_by"] != "mobile" || got["device_ref"] != "android:p1" || got["session_id"] == "" || got["online"] != true {
		t.Fatalf("tenant device = %v", got)
	}
}

func TestMigration009KeepsMachines(t *testing.T) {
	db := openTestDB(t)
	applyMigrations(t, db, "008_alerts.sql")
	for _, q := range []string{
		`INSERT INTO hosting_pools (key, name, platform, execution_enabled) VALUES ('macs', 'Macs', 'macos', 1)`,
		`INSERT INTO hosting_machines (pool_key, instance_id, name, state, online, validated, public_host, connect_mode) VALUES ('macs', 7, 'm7', 'assigned', 1, 1, 'mac.example.com', 'direct')`,
		`INSERT INTO hosting_products (key, name, runtime_kind) VALUES ('ssh-access', 'SSH', 'ssh_access')`,
		`INSERT INTO hosting_profiles (key, name, backend, product_key, pool_key) VALUES ('mac', 'Mac', 'machine_pool', 'ssh-access', 'macs')`,
		`INSERT INTO hosting_tenants (id, external_ref, profile_key, backend, machine_id, slug, owner_email, status) VALUES ('htn_1', 'a', 'mac', 'machine_pool', 1, 's1', 'a@example.com', 'active')`,
		`UPDATE hosting_machines SET tenant_id='htn_1'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw, err := os.ReadFile("migrations/009_app_managed.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatalf("apply 009: %v", err)
	}
	m, err := dbMachineGet(db, 1)
	if err != nil || m == nil || m.InstanceID != 7 || m.State != MachineAssigned || m.PublicHost != "mac.example.com" || m.TenantID != "htn_1" || m.ConnectMode != "direct" || m.managed() {
		t.Fatalf("machine after 009 = %+v, %v", m, err)
	}
	p, err := dbPoolGet(db, "macs")
	if err != nil || p == nil || p.ManagedBy != "" || !p.ExecutionEnabled {
		t.Fatalf("pool after 009 = %+v, %v", p, err)
	}
	if tn, _ := dbTenantGet(db, "htn_1"); tn == nil || tn.MachineID != 1 || tn.Device != nil {
		t.Fatalf("tenant after 009 = %+v", tn)
	}
	var fkErrors int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&fkErrors); err != nil || fkErrors != 0 {
		t.Fatalf("foreign key check: %d %v", fkErrors, err)
	}
	if _, err := db.Exec(`INSERT INTO hosting_machines (pool_key, name) VALUES ('macs', 'nothing')`); err == nil {
		t.Fatal("a machine needs an instance or a device")
	}
}
