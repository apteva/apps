package main

import (
	"fmt"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func phoneProfile(t *testing.T, a *App, ctx *sdk.AppCtx, settings map[string]any) {
	t.Helper()
	call(t, a.toolProfileUpsert, ctx, map[string]any{"key": "phone", "backend": "machine_pool", "product_key": "ssh-access", "pool_key": "phones", "settings": settings})
}

func createPhoneTenant(t *testing.T, a *App, ctx *sdk.AppCtx, ref string) string {
	t.Helper()
	return strArg(mapFromAny(call(t, a.toolTenantCreate, ctx, map[string]any{"external_ref": ref, "profile": "phone", "owner_email": ref + "@example.com"})["tenant"]), "id")
}

func TestHandoverLockedPhoneMovesTenantToFreePhone(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1", "android:p2")
	fm := stub.mobile
	fm.lock["android:p1"] = "secure" // someone set a PIN after inspect
	ev := recordEvents(ctx)

	id := createPhoneTenant(t, a, ctx, "acct-h")
	tn := rented(t, db, id)
	if tn.Status != TenantActive || tn.MachineID != ids[1] || tn.Device == nil || tn.Device.SessionID == "" {
		t.Fatalf("tenant after a locked first phone = %s machine %d (%s)", tn.Status, tn.MachineID, tn.LastError)
	}
	if s := fm.sessions[tn.Device.SessionID]; s == nil || s.Device != "android:p2" || s.Ended {
		t.Fatalf("tenant session = %+v", s)
	}
	if fm.liveOn("android:p1") != nil {
		t.Fatal("the locked phone must keep no session")
	}
	m := machine(t, db, ids[0])
	if m.State != MachineQuarantined || m.TenantID != "" || !strings.Contains(m.StateReason, "phone_locked") {
		t.Fatalf("locked phone = %s tenant %q (%s)", m.State, m.TenantID, m.StateReason)
	}
	got := ev.take("hosting.tenant.handover_failed")
	if len(got) != 1 || got[0]["reason"] != "phone_locked" || got[0]["moved"] != true || got[0]["to_machine_id"] != float64(ids[1]) && got[0]["to_machine_id"] != ids[1] {
		t.Fatalf("handover_failed events = %v", got)
	}
	if got := ev.take("hosting.tenant.active"); len(got) != 1 || got[0]["device_ref"] != "android:p2" {
		t.Fatalf("active events = %v", got)
	}
	if len(ev.take("hosting.machine.quarantined")) == 0 {
		t.Fatal("the locked phone must raise a quarantine alert")
	}
}

func TestHandoverLockedNoSparePhoneFailsThenRetries(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	fm.lock["android:p1"] = "secure"
	ev := recordEvents(ctx)

	id := createPhoneTenant(t, a, ctx, "acct-n")
	tn := rented(t, db, id)
	if tn.Status != TenantFailed || !strings.Contains(tn.LastError, "phone_locked") || tn.Device == nil || tn.Device.SessionID != "" {
		t.Fatalf("tenant = %s (%s) device %+v", tn.Status, tn.LastError, tn.Device)
	}
	if fm.liveOn("android:p1") != nil {
		t.Fatal("a locked phone must not keep the renter's session")
	}
	got := ev.take("hosting.tenant.handover_failed")
	if len(got) != 1 || got[0]["moved"] != false || !strings.Contains(strFromAny(got[0]["not_moved_reason"]), "no free phone") {
		t.Fatalf("handover_failed events = %v", got)
	}
	failed := ev.take("hosting.operation.failed")
	if len(failed) != 1 || failed[0]["step"] != "device_handover" {
		t.Fatalf("failed events = %v", failed)
	}

	// The operator removes the PIN and retries the run.
	fm.lock["android:p1"] = "swipe"
	call(t, a.toolOperationRetry, ctx, map[string]any{"operation_id": failed[0]["operation_id"], "reviewed": true})
	if tn := rented(t, db, id); tn.Status != TenantActive || tn.MachineID != ids[0] || tn.Device.SessionID == "" {
		t.Fatalf("after retry: %s (%s)", tn.Status, tn.LastError)
	}
}

func TestHandoverLockedResumeGoesBackToSuspended(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	id := createPhoneTenant(t, a, ctx, "acct-r")
	call(t, a.toolTenantSuspend, ctx, map[string]any{"tenant_id": id})
	fm.lock["android:p1"] = "secure"
	call(t, a.toolTenantResume, ctx, map[string]any{"tenant_id": id})
	tn := rented(t, db, id)
	if tn.Status != TenantSuspended || tn.MachineID != ids[0] || !strings.Contains(tn.LastError, "phone_locked") || tn.Device.SessionID != "" {
		t.Fatalf("resume on a locked phone = %s machine %d (%s) session %q", tn.Status, tn.MachineID, tn.LastError, tn.Device.SessionID)
	}
	if fm.liveOn("android:p1") != nil {
		t.Fatal("a locked phone must not keep the renter's session")
	}
}

func TestHandoverUnknownLockAndOptOut(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	fm.lock["android:p1"] = "unknown"
	id := createPhoneTenant(t, a, ctx, "acct-u")
	if tn := rented(t, db, id); tn.Status != TenantFailed || !strings.Contains(tn.LastError, "lock_unknown") {
		t.Fatalf("unknown lock = %s (%s)", tn.Status, tn.LastError)
	}

	// require_unlocked_handover=false: hand the phone over without waking it.
	stub2 := newStub()
	a2, ctx2, db2 := newTestCtx(t, stub2)
	setupPhonePool(t, a2, ctx2, stub2, "android:p1")
	phoneProfile(t, a2, ctx2, map[string]any{"require_unlocked_handover": false})
	stub2.mobile.lock["android:p1"] = "secure"
	id2 := createPhoneTenant(t, a2, ctx2, "acct-o")
	if tn := rented(t, db2, id2); tn.Status != TenantActive {
		t.Fatalf("opted-out handover = %s (%s)", tn.Status, tn.LastError)
	}
	if len(stub2.mobile.wakes) != 0 {
		t.Fatalf("opted-out handover must not wake the phone: %v", stub2.mobile.wakes)
	}
}

func TestInspectRefusesUnsellablePhones(t *testing.T) {
	cases := []struct {
		name string
		prep func(f *fakeMobile)
		want string
	}{
		{"secure lock", func(f *fakeMobile) { f.lock["android:p1"] = "secure" }, "PIN, pattern or password"},
		{"extra users", func(f *fakeMobile) { f.users["android:p1"] = 2 }, "2 users"},
		{"old mobile", func(f *fakeMobile) { f.coverage = 1 }, "coverage 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStub()
			stub.mobile = newFakeMobile("android:p1")
			c.prep(stub.mobile)
			a, ctx, db := newTestCtx(t, stub)
			call(t, a.toolPoolUpsert, ctx, map[string]any{"key": "phones", "name": "Phones", "managed_by": "mobile"})
			call(t, a.toolPoolSetExecution, ctx, map[string]any{"key": "phones", "enabled": true})
			id := int64Arg(mapFromAny(call(t, a.toolMachineAdd, ctx, map[string]any{"pool_key": "phones", "device_ref": "android:p1"})["machine"]), "id")
			call(t, a.toolMachineUpdate, ctx, map[string]any{"machine_id": id, "validated": true, "confirm": fmt.Sprint(id)})
			call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": id})
			if m := machine(t, db, id); m.State != MachineQuarantined || !strings.Contains(m.StateReason, c.want) {
				t.Fatalf("inspect = %s (%s), want refusal with %q", m.State, m.StateReason, c.want)
			}
		})
	}

	// A baseline Mobile recorded before 0.12.0 is not reused: inspect asks
	// for a fresh one, and capture_baseline=true passes.
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	stub.mobile.coverage = 1
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": ids[0]})
	m := machine(t, db, ids[0])
	if m.State != MachineQuarantined || !strings.Contains(m.StateReason, "capture_baseline=true") {
		t.Fatalf("old baseline = %s (%s)", m.State, m.StateReason)
	}
	stub.mobile.coverage = 2
	call(t, a.toolMachineInspect, ctx, map[string]any{"machine_id": ids[0], "capture_baseline": true})
	if m := machine(t, db, ids[0]); m.State != MachineAvailable {
		t.Fatalf("after capture = %s (%s)", m.State, m.StateReason)
	}
}

func TestReleaseClearsProfileSystemApps(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	phoneProfile(t, a, ctx, map[string]any{"clear_system_apps": []any{"com.android.chrome", "com.google.android.gm"}})
	fm.system["com.android.chrome"], fm.system["com.google.android.gm"] = true, true
	id := createPhoneTenant(t, a, ctx, "acct-s")
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	if m := machine(t, db, ids[0]); m.State != MachineAvailable {
		t.Fatalf("after release = %s (%s)", m.State, m.StateReason)
	}
	cleared := map[string]bool{}
	for _, c := range fm.cleans {
		list, _ := c["clear_data"].([]string)
		for _, app := range list {
			cleared[app] = true
		}
	}
	for _, app := range []string{"com.whatsapp", "org.mozilla.firefox", "com.android.chrome", "com.google.android.gm"} {
		if !cleared[app] {
			t.Fatalf("%s was not cleared: %v", app, cleared)
		}
	}

	// A system app Mobile's operator did not allow fails the clean: the
	// phone is quarantined, never sold with the data left in place.
	delete(fm.system, "com.google.android.gm")
	id = createPhoneTenant(t, a, ctx, "acct-s2")
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	if m := machine(t, db, ids[0]); m.State != MachineQuarantined || !strings.Contains(m.StateReason, "clean_system_apps") {
		t.Fatalf("disallowed system app = %s (%s)", m.State, m.StateReason)
	}
}

func TestReleaseQuarantinesWhatMobileCannotUndo(t *testing.T) {
	stub := newStub()
	a, ctx, db := newTestCtx(t, stub)
	ids := setupPhonePool(t, a, ctx, stub, "android:p1")
	fm := stub.mobile
	id := createPhoneTenant(t, a, ctx, "acct-b")
	fm.notUndone["android:p1"] = []string{"bluetooth_bonded: AA:BB:CC"}
	call(t, a.toolTenantRelease, ctx, map[string]any{"tenant_id": id, "confirm": id})
	if m := machine(t, db, ids[0]); m.State != MachineQuarantined || !strings.Contains(m.StateReason, "bluetooth_bonded") {
		t.Fatalf("not undone = %s (%s)", m.State, m.StateReason)
	}

	// A PIN the renter set is caught too.
	stub2 := newStub()
	a2, ctx2, db2 := newTestCtx(t, stub2)
	ids2 := setupPhonePool(t, a2, ctx2, stub2, "android:p1")
	id2 := createPhoneTenant(t, a2, ctx2, "acct-pin")
	stub2.mobile.lock["android:p1"] = "secure"
	call(t, a2.toolTenantRelease, ctx2, map[string]any{"tenant_id": id2, "confirm": id2})
	if m := machine(t, db2, ids2[0]); m.State != MachineQuarantined || !strings.Contains(m.StateReason, "lock_changed") {
		t.Fatalf("renter PIN = %s (%s)", m.State, m.StateReason)
	}
}

func TestPhoneProfileSettingsValidation(t *testing.T) {
	stub := newStub()
	a, ctx, _ := newTestCtx(t, stub)
	setupPhonePool(t, a, ctx, stub, "android:p1")
	base := map[string]any{"key": "phone", "backend": "machine_pool", "product_key": "ssh-access", "pool_key": "phones"}
	with := func(settings map[string]any) map[string]any {
		out := map[string]any{"settings": settings}
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	callErr(t, a.toolProfileUpsert, ctx, with(map[string]any{"wipe_mode": "ephemeral_user"}), "no throwaway-user sessions")
	callErr(t, a.toolProfileUpsert, ctx, with(map[string]any{"wipe_mode": "factory"}), "wipe_mode must be deep_clean")
	callErr(t, a.toolProfileUpsert, ctx, with(map[string]any{"clear_system_apps": []any{"not a package"}}), "not an Android package name")
	call(t, a.toolProfileUpsert, ctx, with(map[string]any{"wipe_mode": "deep_clean", "require_unlocked_handover": true, "clear_system_apps": []any{"com.android.chrome"}}))

	call(t, a.toolPoolUpsert, ctx, map[string]any{"key": "macs", "name": "Macs", "platform": "macos"})
	callErr(t, a.toolProfileUpsert, ctx, map[string]any{"key": "mac", "backend": "machine_pool", "product_key": "ssh-access", "pool_key": "macs",
		"settings": map[string]any{"wipe_mode": "deep_clean"}}, "settings for phone pools")
}
