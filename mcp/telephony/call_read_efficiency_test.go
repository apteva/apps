package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func callReadTestRequest(t *testing.T, a *App, user string) *http.Request {
	t.Helper()
	p, e := a.phonePrincipal("project-a", phoneTestIdentity(user))
	if e != nil {
		t.Fatal(e)
	}
	return withPhonePrincipal(httptest.NewRequest("GET", "/calls", nil), p)
}
func callReadExec(t *testing.T, a *App, query string, args ...any) {
	t.Helper()
	if _, e := a.db().db.Exec(query, args...); e != nil {
		t.Fatal(e)
	}
}
func callReadVersion(t *testing.T, a *App, project string) int64 {
	t.Helper()
	var v int64
	if e := a.db().db.QueryRow(`SELECT COALESCE((SELECT revision FROM telephony_read_versions WHERE project_id=?),0)`, project).Scan(&v); e != nil {
		t.Fatal(e)
	}
	return v
}
func TestCallReadProjectionAndPermissionParity(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "offered")
	phoneTestCall(t, a, "own", "completed")
	phoneTestCall(t, a, "supervised", "completed")
	phoneTestCall(t, a, "hidden", "pending")
	callReadExec(t, a, `UPDATE calls SET routing_destination_id='support' WHERE id='hidden'`)
	callReadExec(t, a, `INSERT INTO telephony_call_owners VALUES('own','project-a',?,'sales')`, phoneTestIdentity("alice").key())
	callReadExec(t, a, `UPDATE calls SET browser_audio_diagnostics=?,carrier_audio_diagnostics=?,carrier_signaling_json=?,callback_secret='private',peer_token='private',carrier_sid='sid-'||id,recording_mode='always',recording_control_state='pause_requested',recording_requested_at=?,hold_state='held',duration_started_at='2026-01-01T00:00:00Z',ended_at=CASE WHEN status='completed' THEN '2026-01-01T01:00:00Z' ELSE ended_at END`, strings.Repeat("x", 64000), strings.Repeat("x", 64000), strings.Repeat("x", 64000), time.Now().UTC().Format(time.RFC3339Nano))
	callReadExec(t, a, `INSERT INTO call_control_settings(project_id,hold_music_url,updated_at) VALUES('project-a','https://example.test/music','now')`)
	for _, user := range []string{"alice", "eve", "boss"} {
		t.Run(user, func(t *testing.T) {
			req := callReadTestRequest(t, a, user)
			full, e := a.db().listWhere(`project_id=? ORDER BY placed_at DESC,id DESC`, "project-a")
			if e != nil {
				t.Fatal(e)
			}
			if e = a.db().attachRingOffers("project-a", full); e != nil {
				t.Fatal(e)
			}
			if e = a.db().attachRecordingSummaries("project-a", full); e != nil {
				t.Fatal(e)
			}
			full = a.filterPhoneCalls(req, full)
			model, checked, e := a.phoneCallRead(req, "project-a", 100, false)
			if e != nil {
				t.Fatal(e)
			}
			old := a.callsPanelForRequest(req, full, false)
			new := a.callsPanelForRequest(checked, model.rows, false)
			oldJSON, _ := json.Marshal(old)
			newJSON, _ := json.Marshal(new)
			if string(oldJSON) != string(newJSON) {
				t.Fatalf("response changed:\nold=%s\nnew=%s", oldJSON, newJSON)
			}
			for _, r := range model.rows {
				if r.BrowserAudioDiagnostics != "" || r.CarrierAudioDiagnostics != "" || r.CarrierSignalingJSON != "" || r.PeerToken != "" || r.CallbackSecret != "" {
					t.Fatal("list loaded private/large payloads")
				}
			}
			if phoneUserFrom(req).readPermissions != nil {
				t.Fatal("mutated authentication principal")
			}
		})
	}
}
func TestCallReadCacheInvalidatesAndAnswerStaysAuthoritative(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "offered")
	req := callReadTestRequest(t, a, "alice")
	first, _, e := a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || len(first.rows) != 1 {
		t.Fatalf("initial %v %v", first, e)
	}
	repeat, _, e := a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || first.permissions != repeat.permissions {
		t.Fatal("identical scope not reused")
	}
	callReadExec(t, a, `UPDATE call_offers SET destination_id='support' WHERE id='sales-offered'`)
	next, _, e := a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || len(next.rows) != 0 {
		t.Fatalf("stale offer visible: %v %v", next.rows, e)
	}
	row, e := a.db().findCall("offered")
	if e != nil {
		t.Fatal(e)
	}
	if a.phoneOfferDestination(phoneUserFrom(req), row, "") != "" {
		t.Fatal("Answer used list metadata")
	}
	boss, _, e := a.phoneCallRead(callReadTestRequest(t, a, "boss"), "project-a", 100, false)
	if e != nil || len(boss.rows) != 1 {
		t.Fatal("supervisor lost visibility")
	}
	callReadExec(t, a, `UPDATE calls SET status='completed' WHERE id='offered'`)
	settled, _, e := a.phoneCallRead(callReadTestRequest(t, a, "boss"), "project-a", 100, false)
	if e != nil || settled.rows[0].Status != "completed" {
		t.Fatal("terminal state cached")
	}
}
func TestCallReadOfferExpiryAndRecordingClockBoundary(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "expiry")
	expiry := time.Now().Add(100 * time.Millisecond)
	callReadExec(t, a, `UPDATE call_offers SET expires_at=? WHERE id='sales-expiry'`, ringTime(expiry))
	req := callReadTestRequest(t, a, "alice")
	m, _, e := a.phoneCallRead(req, "project-a", 100, true)
	if e != nil || len(m.rows) != 1 {
		t.Fatal(e)
	}
	if m.until.After(expiry) {
		t.Fatal("cache outlives offer")
	}
	time.Sleep(time.Until(expiry) + 5*time.Millisecond)
	m, _, e = a.phoneCallRead(req, "project-a", 100, true)
	if e != nil || len(m.rows) != 0 {
		t.Fatal("expired offer reused")
	}
	phoneTestCall(t, a, "recording-clock", "pending")
	boundary := time.Now().Add(100 * time.Millisecond)
	callReadExec(t, a, `UPDATE calls SET recording_mode='always',recording_control_state='pause_requested',recording_requested_at=? WHERE id='recording-clock'`, boundary.Add(-30*time.Second).Format(time.RFC3339Nano))
	before, e := a.callNotificationSnapshot(req, "project-a")
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(time.Until(boundary) + 5*time.Millisecond)
	after, e := a.callNotificationSnapshot(req, "project-a")
	if e != nil || before == after {
		t.Fatal("recording timeout hidden by cache")
	}
}
func TestCallReadCacheGrantIsolationAndRevocation(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	policy := phoneTestPolicy(t, a)
	phoneTestCall(t, a, "direct", "pending")
	alice := callReadTestRequest(t, a, "alice")
	m, _, e := a.phoneCallRead(alice, "project-a", 100, false)
	if e != nil || len(m.rows) != 1 {
		t.Fatal(e)
	}
	narrowed := *phoneUserFrom(alice)
	narrowed.Destinations = map[string]bool{}
	m, _, e = a.phoneCallRead(withPhonePrincipal(alice, &narrowed), "project-a", 100, false)
	if e != nil || len(m.rows) != 0 {
		t.Fatal("grant scopes shared cached calls")
	}
	for i := range policy.Users {
		if policy.Users[i].Identity.SubjectID == "alice" {
			policy.Users[i].Enabled = false
		}
	}
	raw, _ := json.Marshal(policy)
	callReadExec(t, a, `UPDATE telephony_access_policies SET policy_json=? WHERE project_id='project-a'`, string(raw))
	id := phoneTestIdentity("alice")
	w := phoneTestRequest(a, &id, "GET", "/calls", nil)
	if w.Code != 403 {
		t.Fatalf("revoked user cached: %d", w.Code)
	}
}
func TestCallReadEpochExcludesMediaDiagnosticsAndTracksDependencies(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestRingOfferCall(t, a, "epoch")
	v := callReadVersion(t, a, "project-a")
	callReadExec(t, a, `UPDATE calls SET browser_audio_diagnostics='{}',carrier_audio_diagnostics='{}',updated_at='now' WHERE id='epoch'`)
	if callReadVersion(t, a, "project-a") != v {
		t.Fatal("diagnostic hot path invalidated read cache")
	}
	for _, q := range []string{`UPDATE calls SET media_status='connected' WHERE id='epoch'`, `UPDATE call_ring_runs SET status='exhausted' WHERE call_id='epoch'`, `UPDATE routing_destinations SET config_json='{}' WHERE id='sales'`, `INSERT INTO telephony_call_owners VALUES('epoch','project-a','owner','sales')`, `UPDATE telephony_call_owners SET destination_id='support' WHERE call_id='epoch'`, `DELETE FROM telephony_call_owners WHERE call_id='epoch'`} {
		callReadExec(t, a, q)
		next := callReadVersion(t, a, "project-a")
		if next <= v {
			t.Fatalf("missing invalidation: %s", q)
		}
		v = next
	}
	before := callReadVersion(t, a, "other")
	callReadExec(t, a, `UPDATE calls SET project_id='other' WHERE id='epoch'`)
	if callReadVersion(t, a, "project-a") <= v || callReadVersion(t, a, "other") <= before {
		t.Fatal("project move did not invalidate both scopes")
	}
}
func TestCallReadConcurrentRequestsAndBoundedCache(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestCall(t, a, "concurrent", "pending")
	req := callReadTestRequest(t, a, "alice")
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, _, e := a.phoneCallRead(req, "project-a", 100, false)
			if e != nil || len(m.rows) != 1 {
				t.Errorf("concurrent read %v %v", m, e)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < callReadCacheLimit+10; i++ {
		copy := *phoneUserFrom(req)
		copy.Identity.SubjectID = fmt.Sprint(i)
		_, _, e := a.phoneCallRead(withPhonePrincipal(req, &copy), "project-a", 100, false)
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(a.callReads.entries) > callReadCacheLimit {
		t.Fatal("unbounded cache")
	}
}
func TestScopedRecordingSummaryAndCoveringIndex(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	for i, state := range []string{"failed", "pending", "importing", "provider_only", "stored"} {
		callReadExec(t, a, `INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at) VALUES(?,'one','project-a','twilio',1,?,?,'now')`, fmt.Sprint(i), fmt.Sprint(i), state)
	}
	callReadExec(t, a, `INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at,deleted_at) VALUES('deleted','one','project-a','twilio',1,'deleted','stored','now','now'),('foreign','one','other','twilio',1,'foreign','stored','now','')`)
	calls := []callRow{{ID: "one"}, {ID: "two"}}
	if e := a.db().attachRecordingSummaries("project-a", calls); e != nil {
		t.Fatal(e)
	}
	if calls[0].RecordingCount != 5 || calls[0].RecordingStatus != "stored" || calls[1].RecordingCount != 0 {
		t.Fatal(calls)
	}
	if e := a.db().attachRecordingSummaries("project-a", nil); e != nil {
		t.Fatal(e)
	}
	rows, e := a.db().db.Query(`EXPLAIN QUERY PLAN SELECT call_id,COUNT(*),MAX(storage_status) FROM recordings WHERE project_id=? AND deleted_at='' AND call_id IN (?,?) GROUP BY call_id`, "project-a", "one", "two")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var x, y, z int
		var detail string
		if e = rows.Scan(&x, &y, &z, &detail); e != nil {
			t.Fatal(e)
		}
		plan += detail
	}
	if !strings.Contains(plan, "COVERING INDEX idx_recordings_call_summary (project_id=? AND call_id=?)") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatal(plan)
	}
}
func TestCapacityCleanupParityAndOccupiedSlotPlan(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	now := time.Now()
	for _, status := range []string{"completed", "failed", "busy", "no-answer", "canceled", "pending", "answered", "in-progress"} {
		phoneTestCall(t, a, status, status)
		callReadExec(t, a, `INSERT INTO phone_capacity(call_id,project_id,principal,destination_id,expires_at) VALUES(?,'project-a','user','sales',?)`, status, ringTime(now.Add(-time.Second)))
	}
	tx, e := a.db().db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = cleanupCapacityTx(tx, now); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	rows, e := a.db().db.Query(`SELECT call_id FROM phone_capacity ORDER BY call_id`)
	if e != nil {
		t.Fatal(e)
	}
	var kept []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		kept = append(kept, id)
	}
	rows.Close()
	if !reflect.DeepEqual(kept, []string{"answered", "in-progress"}) {
		t.Fatal(kept)
	}
	rows, e = a.db().db.Query(`EXPLAIN QUERY PLAN `+capacityCleanupSQL, ringTime(now), ringTime(now))
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var x, y, z int
		var detail string
		rows.Scan(&x, &y, &z, &detail)
		plan += detail
	}
	if !strings.Contains(plan, "SCAN phone_capacity") || !strings.Contains(plan, "SEARCH c USING INDEX sqlite_autoindex_calls_1 (id=?)") || strings.Contains(plan, "idx_calls_status") {
		t.Fatal(plan)
	}
}

func TestCallReadFiltersBeforeLimitAndBoundsRetainedMetadata(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	config, _ := json.Marshal(map[string]any{"capacity": destinationCapacity{Identity: phoneTestIdentity("bob"), Limit: 1}})
	callReadExec(t, a, `UPDATE routing_destinations SET config_json=? WHERE id='sales'`, string(config))
	for i := 0; i < 205; i++ {
		id := fmt.Sprintf("ineligible-%03d", i)
		phoneTestCall(t, a, id, "pending")
		callReadExec(t, a, `UPDATE calls SET placed_at='2099-01-01T00:00:00Z' WHERE id=?`, id)
	}
	phoneTestCall(t, a, "older-owned", "completed")
	callReadExec(t, a, `INSERT INTO telephony_call_owners VALUES('older-owned','project-a',?,'sales')`, phoneTestIdentity("alice").key())
	m, _, e := a.phoneCallRead(callReadTestRequest(t, a, "alice"), "project-a", 1, false)
	if e != nil || len(m.rows) != 1 || m.rows[0].ID != "older-owned" {
		t.Fatalf("visibility must precede limit: %v %v", m.rows, e)
	}
	if len(m.permissions.covered) != 1 || len(m.permissions.owners) != 1 || len(m.permissions.offers) != 1 {
		t.Fatal("cache retained ineligible history metadata")
	}
}

func TestCallReadIdentityConfigurationAndRecordingInvalidation(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	phoneTestCall(t, a, "direct", "pending")
	req := callReadTestRequest(t, a, "alice")
	before, _, e := a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || len(before.rows) != 1 {
		t.Fatal(e)
	}
	callReadExec(t, a, `INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at) VALUES('r','direct','project-a','twilio',1,'r','pending','now')`)
	after, _, e := a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || after.rows[0].RecordingCount != 1 || after.rows[0].RecordingStatus != "pending" {
		t.Fatal("recording insert cached", e)
	}
	callReadExec(t, a, `UPDATE recordings SET storage_status='stored' WHERE id='r'`)
	after, _, e = a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || after.rows[0].RecordingStatus != "stored" {
		t.Fatal("recording status cached", e)
	}
	callReadExec(t, a, `UPDATE recordings SET deleted_at='now' WHERE id='r'`)
	after, _, e = a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || after.rows[0].RecordingCount != 0 {
		t.Fatal("deleted recording cached", e)
	}
	config, _ := json.Marshal(map[string]any{"capacity": destinationCapacity{Identity: phoneTestIdentity("bob"), Limit: 1}})
	callReadExec(t, a, `UPDATE routing_destinations SET config_json=? WHERE id='sales'`, string(config))
	after, _, e = a.phoneCallRead(req, "project-a", 100, false)
	if e != nil || len(after.rows) != 0 {
		t.Fatal("destination identity cached", e)
	}
}
