package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// A manual pick-up used to queue behind the app-wide auto-answer lock, so
// every pick-up in the install waited for the previous one.
func TestManualPickupDoesNotWaitForAutoAnswerLock(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	alice := phoneTestIdentity("alice")
	manual := phoneTestCall(t, a, "manual-while-locked", "pending")
	automatic := phoneTestCall(t, a, "automatic-while-locked", "pending")
	a.autoAnswerMu.Lock()
	locked := true
	defer func() {
		if locked {
			a.autoAnswerMu.Unlock()
		}
	}()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+manual.ID, map[string]any{})
	}()
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Fatalf("manual pick-up = %d %s", w.Code, w.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual pick-up waited for the auto-answer lock")
	}
	// An automatic pick-up still takes the lock around its policy check.
	go func() {
		done <- phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+automatic.ID, map[string]any{"destination_id": "sales", "auto_answer_session": autoAnswerTestSession})
	}()
	select {
	case <-done:
		t.Fatal("automatic pick-up skipped the auto-answer lock")
	case <-time.After(200 * time.Millisecond):
	}
	a.autoAnswerMu.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("automatic pick-up did not finish after the lock was released")
	}
}

// The claim, ring offers, attachment, owner and media grant are written in one
// transaction and all visible together.
func TestPickupClaimWritesEverythingTogether(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	row := phoneTestRingOfferCall(t, a, "one-transaction")
	alice := phoneTestIdentity("alice")
	w := phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales"})
	if w.Code != 200 {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	var session softphoneSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.SessionToken == "" || session.MediaURL == "" {
		t.Fatalf("session %s %v", w.Body, err)
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != "answering" || current.PeerToken == "" || current.PeerToken == row.PeerToken || current.ThreadID != "human-"+row.ID || !strings.Contains(current.AudioBridgeURL, "/peer/"+row.ID+"/") {
		t.Fatalf("claimed call %+v", current)
	}
	var owner, offerStatus string
	if err := a.db().db.QueryRow(`SELECT principal FROM telephony_call_owners WHERE call_id=?`, row.ID).Scan(&owner); err != nil || owner != alice.key() {
		t.Fatalf("owner %q %v", owner, err)
	}
	if err := a.db().db.QueryRow(`SELECT status FROM call_offers WHERE call_id=? AND destination_id='sales'`, row.ID).Scan(&offerStatus); err != nil || offerStatus != "claimed" {
		t.Fatalf("offer %q %v", offerStatus, err)
	}
	if reason, _, _ := a.phoneMediaCheckDetails(current, session.SessionToken); reason != "" {
		t.Fatalf("issued media grant rejected: %s", reason)
	}
	// A second pick-up of the same call is refused.
	if again := phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales"}); again.Code != http.StatusConflict {
		t.Fatalf("second pick-up = %d %s", again.Code, again.Body)
	}
}

// One "softphone answer timing" line per answered pick-up, on the server's
// clock, from the answer request until the browser's audio is ready.
func TestPickupTimingLine(t *testing.T) {
	platform := &answerPlatform{}
	app, _ := withTelephonyTestContext(t, platform)
	row := callRow{
		ID: "timed-pickup", ThreadID: "pending-timed-pickup", Direction: "inbound", CarrierSlug: "telnyx",
		CarrierConnectionID: 10, CarrierSID: "telnyx-timed", CallbackSecret: "callback-secret",
		ToNumber: "+33123456789", FromNumber: "+33612345678", Status: "pending",
		PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
		PeerKind: peerKindHuman, AudioBridgeURL: "pending",
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	lines := make(chan map[string]any, 2)
	pickupTimingHook = func(fields []any) {
		line := map[string]any{}
		for i := 0; i+1 < len(fields); i += 2 {
			line[fields[i].(string)] = fields[i+1]
		}
		lines <- line
	}
	t.Cleanup(func() { pickupTimingHook = nil })
	request := httptest.NewRequest(http.MethodPost, "/softphone/answer/"+row.ID, nil)
	request = request.WithContext(context.WithValue(request.Context(), answerRequestArrivalKey{}, time.Now()))
	recorder := httptest.NewRecorder()
	app.softphoneAnswer(recorder, request, "project-a", row.ID)
	var session softphoneSession
	if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &session) != nil {
		t.Fatalf("answer = %d %s", recorder.Code, recorder.Body)
	}
	server := softphoneTestServer(t, app)
	browser := dialWS(t, server.URL+"/softphone/media/"+row.ID+"/"+session.SessionToken)
	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, _, err := wsutil.ReadServerData(browser)
	if err != nil || !strings.Contains(string(data), `"type":"ready"`) {
		t.Fatalf("browser event %s %v", data, err)
	}
	var line map[string]any
	select {
	case line = <-lines:
	case <-time.After(2 * time.Second):
		t.Fatal("no timing line")
	}
	order := []string{"claim_ms", "session_returned_ms", "browser_attach_ms", "rtc_ready_ms", "carrier_answer_ms", "media_start_ms", "total_ms"}
	previous := int64(0)
	for _, key := range order {
		value, ok := line[key].(int64)
		if !ok || value < previous {
			t.Fatalf("%s = %v after %d: %v", key, line[key], previous, line)
		}
		previous = value
	}
	if line["call"] != row.ID || line["transport"] != "websocket" || line["standby"] != false {
		t.Fatalf("timing line %v", line)
	}
	// A reconnect to the same call does not log another pick-up.
	if _, ok := app.pickupTimings.Load(row.ID); ok {
		t.Fatal("timing entry kept after the pick-up was logged")
	}
	_ = ws.OpText
}

// An outbound direct SIP call older than retention can be deleted: its SIP
// details used to block the delete and roll back the whole batch.
func TestHousekeepingDeletesOutboundSIPCalls(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	now := time.Now().UTC()
	seedEfficiencyArchive(t, ctx.AppDB(), 3, now.Add(-40*24*time.Hour))
	db := ctx.AppDB()
	if _, err := db.Exec(`DELETE FROM recordings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO outbound_sip_calls(call_id,resource_id,host,codecs_json) VALUES('efficiency-2','resource','sip.example.test','[]')`); err != nil {
		t.Fatal(err)
	}
	if err := a.runHousekeepingAt(context.Background(), ctx, now); err != nil {
		t.Fatal(err)
	}
	var calls, details int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM calls),(SELECT COUNT(*) FROM outbound_sip_calls)`).Scan(&calls, &details); err != nil || calls != 0 || details != 0 {
		t.Fatalf("calls=%d outbound details=%d err=%v", calls, details, err)
	}
}

// One call that cannot be deleted is deferred; the rest of the batch is still
// deleted and the job does not fail.
func TestHousekeepingDefersCallItCannotDelete(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	now := time.Now().UTC()
	seedEfficiencyArchive(t, ctx.AppDB(), 3, now.Add(-40*24*time.Hour))
	db := ctx.AppDB()
	if _, err := db.Exec(`DELETE FROM recordings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE test_call_blocker(call_id TEXT REFERENCES calls(id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO test_call_blocker VALUES('efficiency-1')`); err != nil {
		t.Fatal(err)
	}
	if err := a.runHousekeepingAt(context.Background(), ctx, now); err != nil {
		t.Fatalf("one blocked call failed the job: %v", err)
	}
	var remaining []string
	rows, err := db.Query(`SELECT id FROM calls`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		remaining = append(remaining, id)
	}
	rows.Close()
	if len(remaining) != 1 || remaining[0] != "efficiency-1" {
		t.Fatalf("remaining calls %v", remaining)
	}
	var next int64
	if err := db.QueryRow(`SELECT housekeeping_after FROM calls WHERE id='efficiency-1'`).Scan(&next); err != nil || next <= now.Unix() {
		t.Fatalf("blocked call not deferred: %d %v", next, err)
	}
}
