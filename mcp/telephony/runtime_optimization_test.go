package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestResamplerBufferReuseExactSamplesAndOwnership(t *testing.T) {
	for _, rates := range [][2]int{{8000, 24000}, {24000, 48000}, {24000, 8000}, {8000, 48000}} {
		t.Run(fmt.Sprint(rates), func(t *testing.T) {
			a, b := newPCMResampler(rates[0], rates[1]), newPCMResampler(rates[0], rates[1])
			var dst []int16
			input := sinePCM(rates[0], 731, rates[0]/3)
			for start := 0; start < len(input); start += 137 {
				in := input[start:min(start+137, len(input))]
				want := a.Process(in)
				dst = b.ProcessInto(in, dst)
				if !reflect.DeepEqual(want, dst) {
					t.Fatal("sample change", start)
				}
				for i := range dst {
					dst[i] = 1234
				}
			}
			frame := sinePCM(rates[0], 441, rates[0]/50)
			dst = b.ProcessInto(frame, dst)
			if allocs := testing.AllocsPerRun(100, func() { dst = b.ProcessInto(frame, dst) }); allocs != 0 {
				t.Fatalf("steady conversion allocates: %v", allocs)
			}
		})
	}
}
func TestSharedCallReadAndNotificationExpiry(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "share")
	r := callReadTestRequest(t, a, "alice")
	list, _, e := a.phoneCallRead(r, "project-a", 100, false)
	if e != nil {
		t.Fatal(e)
	}
	hint, _, e := a.phoneCallRead(r, "project-a", 100, true)
	if e != nil || list.permissions != hint.permissions || len(a.callReads.entries) != 1 {
		t.Fatal("list/SSE rebuilt", e)
	}
	digest, e := a.callNotificationSnapshot(r, "project-a")
	if e != nil || digest != list.fingerprint {
		t.Fatal("fingerprint changed", e)
	}
	callReadExec(t, a, `UPDATE call_offers SET expires_at=? WHERE call_id='share'`, ringTime(time.Now().Add(-time.Second)))
	next, e := a.callNotificationSnapshot(r, "project-a")
	if e != nil || next == digest {
		t.Fatal("offer expiry hidden", e)
	}
}
func TestNoOpUpdatesPreserveReadAndPolicyEpochs(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "same")
	before := callReadVersion(t, a, "project-a")
	var policyBefore, policyAfter int
	if e := a.db().db.QueryRow(`SELECT revision FROM telephony_policy_versions WHERE project_id='project-a'`).Scan(&policyBefore); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`UPDATE calls SET status=status,media_status=media_status WHERE id='same'`, `UPDATE calls SET voice=voice,directive=directive WHERE id='same'`, `UPDATE call_offers SET status=status,expires_at=expires_at WHERE call_id='same'`, `UPDATE routing_destinations SET config_json=config_json WHERE id='sales'`, `UPDATE telephony_access_policies SET policy_json=policy_json,revision=revision WHERE project_id='project-a'`} {
		callReadExec(t, a, q)
	}
	if callReadVersion(t, a, "project-a") != before {
		t.Fatal("no-op invalidated")
	}
	if e := a.db().db.QueryRow(`SELECT revision FROM telephony_policy_versions WHERE project_id='project-a'`).Scan(&policyAfter); e != nil || policyAfter != policyBefore {
		t.Fatal(e, "policy invalidated")
	}
	callReadExec(t, a, `UPDATE calls SET status='completed' WHERE id='same'`)
	if callReadVersion(t, a, "project-a") <= before {
		t.Fatal("real change ignored")
	}
}
func TestIncrementalCarrierHistoryScopesRetentionAndLateEvents(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := phoneTestCall(t, a, "incremental", "answered")
	insert := func(id, at string) {
		t.Helper()
		v := carrierTelemetryRecord{ID: id, ProjectID: row.ProjectID, CallID: row.ID, At: at, Kind: "event"}
		raw, _ := json.Marshal(v)
		_, e := ctx.AppDB().Exec(`INSERT INTO telephony_carrier_transport_history VALUES(?,?,?,?,?,?,?)`, id, row.ProjectID, row.ID, "event", at, ringTime(time.Now().Add(time.Hour)), string(raw))
		if e != nil {
			t.Fatal(e)
		}
	}
	insert("one", ringTime(time.Now()))
	p, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, "")
	if e != nil || len(p.Records) != 1 || p.Reset {
		t.Fatal(p, e)
	}
	empty, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, p.Cursor)
	if e != nil || len(empty.Records) != 0 {
		t.Fatal(empty, e)
	}
	insert("late", ringTime(time.Now().Add(-time.Minute)))
	next, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, p.Cursor)
	if e != nil || len(next.Records) != 1 || next.Records[0].ID != "late" {
		t.Fatal("late occurrence skipped", next, e)
	}
	if _, e = a.db().carrierHistoryPage(t.Context(), "other", row.ID, p.Cursor); e != errHistoryCursor {
		t.Fatal("cross-project cursor", e)
	}
	if _, e = a.db().carrierHistoryPage(t.Context(), row.ProjectID, "other", p.Cursor); e != errHistoryCursor {
		t.Fatal("cross-call cursor", e)
	}
	callReadExec(t, a, `DELETE FROM telephony_carrier_transport_history WHERE id='one'`)
	trimmed, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, p.Cursor)
	if e != nil || !trimmed.Reset {
		t.Fatal("retention gap hidden", trimmed, e)
	}
	for i := 0; i < 260; i++ {
		insert(fmt.Sprintf("batch-%d", i), ringTime(time.Now()))
	}
	page, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, next.Cursor)
	if e != nil || len(page.Records) != 256 || !page.More {
		t.Fatal("unbounded page", page.More, len(page.Records), e)
	}
	rest, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, page.Cursor)
	if e != nil || len(rest.Records) != 4 || rest.More {
		t.Fatal("pagination skipped", rest, e)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = a.db().carrierHistoryPage(canceled, row.ProjectID, row.ID, ""); e == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestLightweightAudioDashboardAndExtrasReuse(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	phoneTestCall(t, a, "dashboard-light", "answered")
	if e := a.db().refreshAudioDashboard(t.Context()); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	f, e := parseAudioDashboardFilter(httptest.NewRequest("GET", "/audio-health?include_totals=false&include_facets=false", nil), now)
	if e != nil {
		t.Fatal(e)
	}
	got, e := a.db().audioDashboard(t.Context(), ctx.CurrentProject(), f, now, &a.dashboardExtras)
	if e != nil || len(got["totals"].(map[string]int64)) != 0 || len(got["facets"].(map[string]any)) != 0 || len(a.dashboardExtras.entries) != 0 {
		t.Fatal("computed omitted aggregates", e)
	}
	f.OmitTotals = false
	f.OmitFacets = false
	if _, e = a.db().audioDashboard(t.Context(), ctx.CurrentProject(), f, now, &a.dashboardExtras); e != nil {
		t.Fatal(e)
	}
	if len(a.dashboardExtras.entries) != 2 {
		t.Fatal("not shared", len(a.dashboardExtras.entries))
	}
	f.Limit = 1
	if _, e = a.db().audioDashboard(t.Context(), ctx.CurrentProject(), f, now, &a.dashboardExtras); e != nil || len(a.dashboardExtras.entries) != 2 {
		t.Fatal("per-page totals rebuilt", e)
	}
	callReadExec(t, a, `UPDATE calls SET status='completed' WHERE id='dashboard-light'`)
	if _, e = a.db().audioDashboard(t.Context(), ctx.CurrentProject(), f, now, &a.dashboardExtras); e != nil || len(a.dashboardExtras.entries) != 4 {
		t.Fatal("stale aggregates", e)
	}
}
func TestIdleWorkerGateRechecksNewWorkAndErrors(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	runs := 0
	run := func(context.Context, *sdk.AppCtx) error { runs++; return nil }
	for i := 0; i < 20; i++ {
		if e := a.runIdleGated("ai-handoffs", run, t.Context(), ctx); e != nil {
			t.Fatal(e)
		}
	}
	if runs != 1 {
		t.Fatal("empty worker rebuilt", runs)
	}
	phoneTestCall(t, a, "worker-wake", "answered")
	if e := a.runIdleGated("ai-handoffs", run, t.Context(), ctx); e != nil || runs != 2 {
		t.Fatal("durable work change not detected", e, runs)
	}
	a.idleWorkers.mu.Lock()
	for k, v := range a.idleWorkers.entries {
		v.until = time.Now().Add(-time.Second)
		a.idleWorkers.entries[k] = v
	}
	a.idleWorkers.mu.Unlock()
	if e := a.runIdleGated("ai-handoffs", run, t.Context(), ctx); e != nil || runs != 3 {
		t.Fatal("fallback check skipped", e, runs)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if e := a.runIdleGated("ai-handoffs", run, canceled, ctx); e == nil {
		t.Fatal("canceled lookup ignored")
	}
}

func TestHintUpgradeRetainsPermissionScopeWithoutRebuilding(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "upgrade")
	r := callReadTestRequest(t, a, "alice")
	hint, _, e := a.phoneCallRead(r, "project-a", 100, true)
	if e != nil || !hint.hint {
		t.Fatal(e)
	}
	list, _, e := a.phoneCallRead(r, "project-a", 100, false)
	if e != nil || list.hint || list.permissions != hint.permissions || len(list.rows) != 1 || list.rows[0].ID != "upgrade" || list.fingerprint != hint.fingerprint {
		t.Fatal("upgrade lost read scope", list, e)
	}
	denied := *phoneUserFrom(r)
	denied.Destinations = map[string]bool{}
	hidden, _, e := a.phoneCallRead(withPhonePrincipal(r, &denied), "project-a", 100, false)
	if e != nil || len(hidden.rows) != 0 {
		t.Fatal("upgrade crossed grants", hidden, e)
	}
}
func TestAudioHistoryModesPreserveOperatorAuthorization(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestCall(t, a, "mode", "answered")
	for _, mode := range []string{"none", "incremental"} {
		w := phoneTestRequest(a, nil, "GET", "/audio-health?call_id=mode&history="+mode, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		if _, ok := got["browser"]; !ok {
			t.Fatal("live counters missing")
		}
		if mode == "none" {
			if _, ok := got["carrier_transport_history"]; ok {
				t.Fatal("unrequested history loaded")
			}
		}
		id := phoneTestIdentity("alice")
		denied := phoneTestRequest(a, &id, "GET", "/audio-health?call_id=mode&history="+mode, nil)
		if denied.Code != 403 {
			t.Fatal("delegated access crossed operator boundary", denied.Code)
		}
	}
	if w := phoneTestRequest(a, nil, "GET", "/audio-health?call_id=mode&history=incremental&history_cursor=invalid", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := phoneTestRequest(a, nil, "GET", "/audio-health?include_facets=nonsense", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestNotificationFingerprintTracksControlGrantChanges(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "scope-change")
	r := callReadTestRequest(t, a, "alice")
	before, e := a.callNotificationSnapshot(r, "project-a")
	if e != nil {
		t.Fatal(e)
	}
	changed := *phoneUserFrom(r)
	changed.CoachScope = !changed.CoachScope
	after, e := a.callNotificationSnapshot(withPhonePrincipal(r, &changed), "project-a")
	if e != nil || before == after {
		t.Fatal("control grant change hidden by equal rows", e)
	}
}
