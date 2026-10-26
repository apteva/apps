package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func browserTelemetryTestBody(call, offer, session, stage string) map[string]any {
	return map[string]any{"browser_tab_id": "tab-" + session, "browser_session_id": session, "events": []any{map[string]any{"call_id": call, "offer_id": offer, "event_id": "event-" + stage, "stage": stage, "occurred_at": ringTime(time.Now())}}}
}
func TestBrowserTelemetryReceiptDoesNotProveDisplayOrRingtone(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "delivery-receipt")
	for _, session := range []string{"browser-one", "browser-two"} {
		w := phoneTestRequest(a, &alice, "POST", "/softphone/offer/ack/"+row.ID, map[string]any{"offer_id": "sales-" + row.ID, "browser_tab_id": "tab-" + session, "browser_session_id": session})
		if w.Code != 200 {
			t.Fatalf("ack %d %s", w.Code, w.Body)
		}
	}
	result, err := a.browserOfferTelemetry("project-a", row.ID, alice.key())
	if err != nil {
		t.Fatal(err)
	}
	events := result["events"].([]map[string]any)
	if len(events) != 2 {
		t.Fatalf("missing tab receipts %+v", events)
	}
	for _, e := range events {
		if e["stage"] != "browser_acknowledged" {
			t.Fatal("ACK fabricated UI/ringtone evidence")
		}
	}
	if result["physical_audibility"] != "unknown" {
		t.Fatal("audibility claimed")
	}
}
func TestBrowserTelemetryIdempotentLateEvidenceAndFinishedReads(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "delivery-late")
	body := browserTelemetryTestBody(row.ID, "sales-"+row.ID, "one", "ringtone_started")
	for i := 0; i < 2; i++ {
		w := phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", body)
		if w.Code != 200 {
			t.Fatalf("write %d %s", w.Code, w.Body)
		}
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET status='completed' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().db.Exec(`UPDATE call_offers SET status='expired' WHERE call_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	w := phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", browserTelemetryTestBody(row.ID, "sales-"+row.ID, "one", "ui_hidden"))
	if w.Code != 200 {
		t.Fatalf("late observation %d %s", w.Code, w.Body)
	}
	w = phoneTestRequest(a, &alice, "GET", "/softphone/offer-telemetry/"+row.ID, nil)
	if w.Code != 200 {
		t.Fatalf("finished diagnostics %d %s", w.Code, w.Body)
	}
	var r struct {
		Events []struct {
			Stage string `json:"stage"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 2 {
		t.Fatalf("duplicate event retained %+v", r)
	}
	stored, _ := a.db().findCall(row.ID)
	if stored.Status != "completed" {
		t.Fatal("telemetry changed call lifecycle")
	}
}
func TestBrowserTelemetryPermissionsAndProjectIsolation(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice, bob, eve := phoneTestIdentity("alice"), phoneTestIdentity("bob"), phoneTestIdentity("eve")
	row := phoneTestRingOfferCall(t, a, "delivery-isolation")
	for _, identity := range []phoneIdentity{alice, bob} {
		w := phoneTestRequest(a, &identity, "POST", "/softphone/telemetry", browserTelemetryTestBody(row.ID, "sales-"+row.ID, "same-browser-id", "ui_displayed"))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	r, err := a.browserOfferTelemetry("project-a", row.ID, alice.key())
	if err != nil {
		t.Fatal(err)
	}
	if len(r["events"].([]map[string]any)) != 1 {
		t.Fatal("another adviser's browser events leaked")
	}
	w := phoneTestRequest(a, &eve, "POST", "/softphone/telemetry", browserTelemetryTestBody(row.ID, "sales-"+row.ID, "eve", "ui_displayed"))
	if w.Code != 404 {
		t.Fatalf("unoffered write %d %s", w.Code, w.Body)
	}
	if _, err = a.db().db.Exec(`UPDATE calls SET project_id='other' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", browserTelemetryTestBody(row.ID, "sales-"+row.ID, "alice", "ui_displayed"))
	if w.Code != 404 {
		t.Fatalf("cross-project write %d %s", w.Code, w.Body)
	}
}
func TestBrowserTelemetryWatchHealthAndRedactedLabels(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "delivery-health")
	body := browserTelemetryTestBody(row.ID, "sales-"+row.ID, "browser-one", "ui_displayed")
	body["watch"] = map[string]any{"browser_tab_id": "tab-browser-one", "browser_session_id": "browser-one", "watch_id": "watch-one", "state": "reconnecting", "connections": 2, "disconnects": 1, "reconnects": 1, "last_heartbeat_at": ringTime(time.Now()), "last_successful_call_list_at": ringTime(time.Now()), "last_failure": map[string]any{"category": "http", "status": 503, "code": "runtime_not_admitting", "requestId": "tel-server-123"}}
	w := phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", body)
	if w.Code != 200 {
		t.Fatalf("snapshot %d %s", w.Code, w.Body)
	}
	r, err := a.browserOfferTelemetry(row.ProjectID, row.ID, alice.key())
	if err != nil {
		t.Fatal(err)
	}
	if len(r["watch_health"].([]map[string]any)) != 1 {
		t.Fatal("watch state missing")
	}
	body["extra_secret"] = "MUST_NOT_STORE"
	w = phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", body)
	if w.Code != 400 {
		t.Fatal("arbitrary metadata accepted")
	}
}
func TestBrowserTelemetryValidationAndDatabaseErrorsAreDistinct(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "delivery-errors")
	body := browserTelemetryTestBody(row.ID, "sales-"+row.ID, "one", "carrier_answered")
	w := phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", body)
	if w.Code != 400 {
		t.Fatalf("client forged server stage %d %s", w.Code, w.Body)
	}
	if _, err := a.db().db.Exec(`DROP TABLE telephony_offer_browser_events`); err != nil {
		t.Fatal(err)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/telemetry", browserTelemetryTestBody(row.ID, "sales-"+row.ID, "one", "ui_displayed"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "telemetry_unavailable") {
		t.Fatalf("DB failure misclassified %d %s", w.Code, w.Body)
	}
}
func TestDeliveryRequestCorrelationIncludesRejectedAdmission(t *testing.T) {
	a := autoAnswerTestSetup(t)
	a.runtime = runtimeTestActive()
	a.runtime.state = "standby"
	a.runtime.initialized = false
	r := httptest.NewRequest("POST", "/softphone/place", strings.NewReader(`{}`))
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	r.Header.Set("X-Request-ID", "tel-client-123")
	w := httptest.NewRecorder()
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == "/softphone/" {
			route.Handler(w, r)
		}
	}
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("X-Request-ID") != "tel-client-123" {
		t.Fatalf("lost correlation %d %s %s", w.Code, w.Header(), w.Body)
	}
}
