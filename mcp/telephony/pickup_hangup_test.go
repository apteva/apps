package main

import (
	"encoding/json"
	"testing"
	"time"
)

func pickupClaim(t *testing.T, app *App, identity *phoneIdentity, callID string, body map[string]any) softphoneSession {
	t.Helper()
	w := phoneTestRequest(app, identity, "POST", "/softphone/answer/"+callID, body)
	if w.Code != 200 {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
	var session softphoneSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.SessionToken == "" {
		t.Fatalf("claim session: %s %v", w.Body, err)
	}
	return session
}

func hangupResponse(t *testing.T, app *App, identity *phoneIdentity, callID string, body any) map[string]any {
	t.Helper()
	w := phoneTestRequest(app, identity, "POST", "/calls/"+callID+"/hangup", body)
	if w.Code != 200 {
		t.Fatalf("hangup: %d %s", w.Code, w.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func callStatus(t *testing.T, app *App, id string) *callRow {
	t.Helper()
	row, err := app.db().findCall(id)
	if err != nil || row == nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return row
}

func TestHangupDuringPickupEndsOnlyForClaimingSession(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "pickup-call", "pending")
	alice, boss := phoneTestIdentity("alice"), phoneTestIdentity("boss")
	session := pickupClaim(t, app, &alice, row.ID, map[string]any{})

	// Another tab or device of the same adviser, a stale token, a supervisor:
	// none of them may end the call while the claiming browser connects.
	for _, attempt := range []struct {
		identity *phoneIdentity
		body     any
		reason   string
	}{
		{&alice, nil, "no_session_token"},
		{&alice, map[string]any{}, "no_session_token"},
		{&alice, map[string]any{"session_token": "not-the-claim"}, "answering_elsewhere"},
		{&boss, map[string]any{}, "no_session_token"},
	} {
		out := hangupResponse(t, app, attempt.identity, row.ID, attempt.body)
		if out["ignored"] != true || out["reason"] != attempt.reason {
			t.Fatalf("hangup %+v ended or misreported: %+v", attempt, out)
		}
		if got := callStatus(t, app, row.ID); got.Status != "answering" {
			t.Fatalf("call ended by a client that did not claim it: %s", got.Status)
		}
	}
	var audited int
	if err := app.db().db.QueryRow(`SELECT COUNT(*) FROM telephony_hangup_audit WHERE call_id=? AND outcome IN ('no_session_token','answering_elsewhere')`, row.ID).Scan(&audited); err != nil || audited != 4 {
		t.Fatalf("ignored hang-ups audited=%d err=%v", audited, err)
	}

	// The claiming session can still reject the call it is picking up.
	out := hangupResponse(t, app, &alice, row.ID, map[string]any{"session_token": session.SessionToken})
	if out["ignored"] == true {
		t.Fatalf("claiming session refused: %+v", out)
	}
	ended := callStatus(t, app, row.ID)
	if ended.Status != "canceled" || ended.TerminationInitiator != terminationByUser || ended.TerminationCause != "rejected" {
		t.Fatalf("claiming reject: %+v", ended)
	}
	events, err := app.db().listLifecycleEvents(row.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		termination, _ := event.Payload["termination"].(map[string]any)
		if event.Payload["status"] == "canceled" && termination != nil {
			found = termination["path"] == "hangup" && termination["session_generation"] == session.SessionGeneration && session.SessionGeneration != ""
		}
	}
	if !found {
		t.Fatalf("termination event does not name the ending request and session: %+v", events)
	}
}

func TestPendingHangupDeclinesOnlyTheUsersOffer(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	alice := phoneTestIdentity("alice")

	row := phoneTestRingOfferCall(t, app, "ring-call")
	out := hangupResponse(t, app, &alice, row.ID, nil)
	if out["offer_declined"] != true {
		t.Fatalf("ringing hang-up did not decline: %+v", out)
	}
	if got := callStatus(t, app, row.ID); got.Status != "pending" {
		t.Fatalf("ringing hang-up ended the call: %s", got.Status)
	}
	var sales, support string
	_ = app.db().db.QueryRow(`SELECT status FROM call_offers WHERE id=?`, "sales-"+row.ID).Scan(&sales)
	_ = app.db().db.QueryRow(`SELECT status FROM call_offers WHERE id=?`, "support-"+row.ID).Scan(&support)
	if sales != "declined" || support != "offered" {
		t.Fatalf("ring group did not move on: sales=%s support=%s", sales, support)
	}

	// A direct call with no offer of the user's keeps ringing.
	direct := phoneTestCall(t, app, "direct-call", "pending")
	if out := hangupResponse(t, app, &alice, direct.ID, nil); out["ignored"] != true || out["reason"] != "no_active_offer" {
		t.Fatalf("direct ringing hang-up: %+v", out)
	}
	if got := callStatus(t, app, direct.ID); got.Status != "pending" {
		t.Fatalf("adviser ended a ringing call: %s", got.Status)
	}

	// Operators keep ending a ringing call outright.
	hangupResponse(t, app, nil, direct.ID, nil)
	if got := callStatus(t, app, direct.ID); got.Status != "canceled" || got.TerminationInitiator != terminationByOperator {
		t.Fatalf("operator reject: %+v", got)
	}
}

func TestUnattachedPickupReturnsToRouting(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	alice := phoneTestIdentity("alice")

	row := phoneTestCall(t, app, "lost-tab", "pending")
	before := time.Now()
	pickupClaim(t, app, &alice, row.ID, map[string]any{})
	claimed := callStatus(t, app, row.ID)
	expires, err := time.Parse(time.RFC3339Nano, claimed.StateExpiresAt)
	if err != nil || expires.Before(before.Add(manualAnswerAttachWindow-time.Second)) || expires.After(time.Now().Add(manualAnswerAttachWindow)) {
		t.Fatalf("claim attach deadline=%q err=%v", claimed.StateExpiresAt, err)
	}

	// A carrier answer still in flight is waited for, not undone.
	if _, err := app.db().db.Exec(`INSERT INTO call_carrier_commands(call_id,project_id,token,owner,operation,lease_until) VALUES(?,?,?,?,?,?)`, row.ID, row.ProjectID, "command", "test", "answer", ringTime(time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := app.expireCallAt(ctx, claimed, expires.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := callStatus(t, app, row.ID); got.Status != "answering" || got.StateExpiresAt == claimed.StateExpiresAt {
		t.Fatalf("in-flight answer: status=%s expiry=%s", got.Status, got.StateExpiresAt)
	}
	if _, err := app.db().db.Exec(`DELETE FROM call_carrier_commands WHERE call_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}

	// The browser never attached: the call goes back to routing.
	stuck := callStatus(t, app, row.ID)
	at, _ := time.Parse(time.RFC3339Nano, stuck.StateExpiresAt)
	if err := app.expireCallAt(ctx, stuck, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := callStatus(t, app, row.ID); got.Status != "pending" || got.PeerToken != "" {
		t.Fatalf("unattached claim: status=%s token=%q", got.Status, got.PeerToken)
	}

	// In a ring group the claimed adviser's offer fails and the next one rings.
	ring := phoneTestRingOfferCall(t, app, "lost-ring")
	pickupClaim(t, app, &alice, ring.ID, map[string]any{"destination_id": "sales"})
	claimedRing := callStatus(t, app, ring.ID)
	at, _ = time.Parse(time.RFC3339Nano, claimedRing.StateExpiresAt)
	if err := app.expireCallAt(ctx, claimedRing, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var sales, support string
	_ = app.db().db.QueryRow(`SELECT status FROM call_offers WHERE id=?`, "sales-"+ring.ID).Scan(&sales)
	_ = app.db().db.QueryRow(`SELECT status FROM call_offers WHERE id=?`, "support-"+ring.ID).Scan(&support)
	if got := callStatus(t, app, ring.ID); got.Status != "pending" || sales != "failed" || support != "offered" {
		t.Fatalf("ring group after a lost pick-up: status=%s sales=%s support=%s", got.Status, sales, support)
	}
}
