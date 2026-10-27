package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const autoAnswerTestSession = "automatic-browser-session-alice"

func autoAnswerTestSetup(t *testing.T) *App {
	t.Helper()
	softphoneTestCtx(t)
	a := &App{}
	phoneTestPolicy(t, a)
	return a
}
func autoAnswerTestPolicy(t *testing.T, a *App, body any) {
	t.Helper()
	w := phoneTestRequest(a, nil, "POST", "/softphone/auto-answer-policy", body)
	if w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
}
func autoAnswerTestAvailable(t *testing.T, a *App, id *phoneIdentity, session string) {
	t.Helper()
	w := phoneTestRequest(a, id, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": session, "available": true})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"designated":true`) {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
}
func autoAnswerTestOffer(a *App, id *phoneIdentity, session, call string) *httptest.ResponseRecorder {
	return phoneTestRequest(a, id, "POST", "/softphone/auto-answer-offer", map[string]any{"session_id": session, "call_id": call, "destination_id": "sales"})
}
func TestAutoAnswerDefaultsAndPolicyInheritance(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "auto-policy")
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	w := autoAnswerTestOffer(a, &alice, autoAnswerTestSession, row.ID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"eligible":false`) {
		t.Fatalf("default must stay manual: %d %s", w.Code, w.Body)
	}
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic", "delay_ms": 0}})
	e, err := a.effectiveAutoAnswer(row.ProjectID, alice.key(), "", "sales")
	if err != nil || e.Mode != "automatic" || e.DelayMS != 0 {
		t.Fatalf("immediate default: %+v %v", e, err)
	}
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "adviser", "identity": alice, "policy": map[string]any{"mode": "automatic", "delay_ms": 1500}})
	e, _ = a.effectiveAutoAnswer(row.ProjectID, alice.key(), "", "sales")
	if e.DelayMS != 1500 || e.Source != "adviser" {
		t.Fatalf("adviser precedence: %+v", e)
	}
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "destination", "scope_id": "sales", "policy": map[string]any{"mode": "manual", "allow_adviser_override": false}})
	e, _ = a.effectiveAutoAnswer(row.ProjectID, alice.key(), "", "sales")
	if e.Mode != "manual" || e.AllowAdviserOverride {
		t.Fatalf("administrator restriction ignored %+v", e)
	}
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "manual", "allow_adviser_override": false}})
	w = phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-policy", map[string]any{"policy": map[string]any{"mode": "automatic"}})
	if w.Code != 403 {
		t.Fatalf("self override allowed: %d %s", w.Code, w.Body)
	}
}
func TestAutoAnswerPermissionsIdentityAndProjectIsolation(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	eve := phoneTestIdentity("eve")
	row := phoneTestRingOfferCall(t, a, "auto-isolation")
	for _, body := range []any{map[string]any{"scope": "project"}, map[string]any{"identity": phoneTestIdentity("bob")}, map[string]any{"scope": "route", "scope_id": "x"}} {
		w := phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-policy", body)
		if w.Code != 403 {
			t.Fatalf("self scope escalation: %d %s", w.Code, w.Body)
		}
	}
	autoAnswerTestAvailable(t, a, &eve, autoAnswerTestSession)
	w := autoAnswerTestOffer(a, &eve, autoAnswerTestSession, row.ID)
	if w.Code == 200 {
		t.Fatalf("unoffered destination allowed %s", w.Body)
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET project_id='other' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	w = autoAnswerTestOffer(a, &alice, autoAnswerTestSession, row.ID)
	if w.Code != 404 {
		t.Fatalf("cross-project offer: %d", w.Code)
	}
}
func TestAutoAnswerSingleBrowserExpiryAndExplicitTakeover(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	body := map[string]any{"session_id": "second-browser-session-alice", "available": true}
	w := phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"designated":false`) {
		t.Fatalf("browser stole designation %d %s", w.Code, w.Body)
	}
	body["takeover"] = true
	w = phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", body)
	if !strings.Contains(w.Body.String(), `"designated":true`) {
		t.Fatal(w.Body.String())
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": autoAnswerTestSession, "available": true})
	if !strings.Contains(w.Body.String(), `"designated":false`) {
		t.Fatal("old browser regained ownership")
	}
	_, err := a.db().db.Exec(`UPDATE telephony_auto_answer_sessions SET expires_at=0`)
	if err != nil {
		t.Fatal(err)
	}
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
}
func TestAutoAnswerConcurrentOffersAndAdvisers(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	one := phoneTestRingOfferCall(t, a, "auto-one")
	two := phoneTestRingOfferCall(t, a, "auto-two")
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic"}})
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	autoAnswerTestAvailable(t, a, &bob, "automatic-browser-session-bob")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, id := range []string{one.ID, two.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			codes <- autoAnswerTestOffer(a, &alice, autoAnswerTestSession, id).Code
		}(id)
	}
	wg.Wait()
	close(codes)
	success := 0
	for c := range codes {
		if c == 200 {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("adviser acquired %d simultaneous offers", success)
	}
	var reserved string
	if err := a.db().db.QueryRow(`SELECT call_id FROM telephony_auto_answer_reservations WHERE principal=?`, alice.key()).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	w := autoAnswerTestOffer(a, &bob, "automatic-browser-session-bob", reserved)
	if w.Code != 409 {
		t.Fatalf("two advisers reserved same call: %d %s", w.Code, w.Body)
	}
}
func TestAutoAnswerPlacementRechecksOfferPolicyAndSession(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "auto-answer")
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic"}})
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	w := autoAnswerTestOffer(a, &alice, autoAnswerTestSession, row.ID)
	if w.Code != 200 {
		t.Fatalf("reserve %d %s", w.Code, w.Body)
	}
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "manual"}})
	w = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales", "auto_answer_session": autoAnswerTestSession})
	if w.Code != 409 {
		t.Fatalf("changed policy ignored %d %s", w.Code, w.Body)
	}
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic"}})
	_, err := a.db().db.Exec(`UPDATE telephony_auto_answer_sessions SET expires_at=0`)
	if err != nil {
		t.Fatal(err)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales", "auto_answer_session": autoAnswerTestSession})
	if w.Code != 409 {
		t.Fatalf("expired session ignored %d %s", w.Code, w.Body)
	}
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	w = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales", "auto_answer_session": autoAnswerTestSession})
	if w.Code != 200 {
		t.Fatalf("normal human placement failed %d %s", w.Code, w.Body)
	}
	var session softphoneSession
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.CallID != row.ID {
		t.Fatalf("invalid media grant %+v %v", session, err)
	}
}
func TestAutoAnswerWithdrawnAndTerminalOffersNeverAnswer(t *testing.T) {
	for _, change := range []string{"expire", "terminal", "revoke"} {
		t.Run(change, func(t *testing.T) {
			a := autoAnswerTestSetup(t)
			alice := phoneTestIdentity("alice")
			row := phoneTestRingOfferCall(t, a, "auto-"+change)
			autoAnswerTestPolicy(t, a, map[string]any{"scope": "project", "policy": map[string]any{"mode": "automatic"}})
			autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
			w := autoAnswerTestOffer(a, &alice, autoAnswerTestSession, row.ID)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			switch change {
			case "expire":
				a.db().db.Exec(`UPDATE call_offers SET expires_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID)
			case "terminal":
				a.db().db.Exec(`UPDATE calls SET status='completed' WHERE id=?`, row.ID)
			case "revoke":
				a.db().db.Exec(`DELETE FROM telephony_access_policies WHERE project_id=?`, row.ProjectID)
			}
			w = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales", "auto_answer_session": autoAnswerTestSession})
			if w.Code == http.StatusOK {
				t.Fatalf("%s still answered", change)
			}
		})
	}
}
func TestAutoAnswerBusyOwnershipAndIndependentIdentityTuples(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	other := alice
	other.IssuerInstallID = "22"
	other.OrganizationID = "org-2"
	p, _ := a.phonePolicy("project-a")
	p.Users = append(p.Users, phoneUser{Identity: other, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"sales"}}})
	w := phoneTestRequest(a, nil, "PUT", "/access/policy", p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	autoAnswerTestPolicy(t, a, map[string]any{"identity": alice, "policy": map[string]any{"mode": "automatic"}})
	left, _ := a.effectiveAutoAnswer("project-a", alice.key(), "", "")
	right, _ := a.effectiveAutoAnswer("project-a", other.key(), "", "")
	isolated, _ := a.effectiveAutoAnswer("other-project", alice.key(), "", "")
	if left.Mode != "automatic" || right.Mode != "manual" || isolated.Mode != "manual" {
		t.Fatalf("identity/project policies mixed: %+v %+v %+v", left, right, isolated)
	}
	active := phoneTestCall(t, a, "occupied", "answered")
	principal, _ := a.phonePrincipal("project-a", alice)
	if err := a.setPhoneOwner(&active, principal, "sales"); err != nil {
		t.Fatal(err)
	}
	row := phoneTestRingOfferCall(t, a, "new-offer")
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	w = autoAnswerTestOffer(a, &alice, autoAnswerTestSession, row.ID)
	if w.Code != 409 {
		t.Fatalf("occupied adviser allowed: %d %s", w.Code, w.Body)
	}
}
func TestAutoAnswerValidationAndStorageFailures(t *testing.T) {
	a := autoAnswerTestSetup(t)
	for _, policy := range []any{map[string]any{"mode": "automatic", "delay_ms": -1}, map[string]any{"mode": "automatic", "delay_ms": 10001}, map[string]any{"mode": "unknown"}} {
		w := phoneTestRequest(a, nil, "POST", "/softphone/auto-answer-policy", map[string]any{"scope": "project", "policy": policy})
		if w.Code != 400 {
			t.Fatalf("bad policy accepted %d %s", w.Code, w.Body)
		}
	}
	if _, err := a.db().db.Exec(`DROP TABLE telephony_auto_answer_policies`); err != nil {
		t.Fatal(err)
	}
	w := phoneTestRequest(a, nil, "POST", "/softphone/auto-answer-policy", map[string]any{})
	if w.Code != 503 {
		t.Fatalf("policy storage failure hidden %d %s", w.Code, w.Body)
	}
}

func TestAutoAnswerWarningPolicyAndLegacyClientGate(t *testing.T) {
	a := autoAnswerTestSetup(t)
	alice := phoneTestIdentity("alice")
	row := phoneTestRingOfferCall(t, a, "warning-gate")
	autoAnswerTestPolicy(t, a, map[string]any{"scope": "adviser", "identity": alice, "policy": map[string]any{"mode": "automatic", "warning_tone": map[string]any{"enabled": true}}})
	e, err := a.effectiveAutoAnswer("project-a", alice.key(), "", "sales")
	if err != nil || e.WarningTone == nil || !e.WarningTone.Enabled || e.WarningTone.WindowMS != 1000 {
		t.Fatalf("effective: %+v %v", e, err)
	}
	autoAnswerTestAvailable(t, a, &alice, autoAnswerTestSession)
	offer := map[string]any{"session_id": autoAnswerTestSession, "call_id": row.ID, "destination_id": "sales"}
	w := phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-offer", offer)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "auto_answer_warning_unsupported") {
		t.Fatalf("old client permitted: %d %s", w.Code, w.Body)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-session", map[string]any{"session_id": autoAnswerTestSession, "available": true, "warning_tone_supported": true})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/auto-answer-offer", offer)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "offer_ttl_ms") {
		t.Fatalf("reservation: %d %s", w.Code, w.Body)
	}
	for _, completed := range []bool{false, true} {
		request := httptest.NewRequest("POST", "/", nil)
		pp, principalErr := a.phonePrincipal("project-a", alice)
		if principalErr != nil {
			t.Fatal(principalErr)
		}
		request = request.WithContext(context.WithValue(request.Context(), phonePrincipalKey{}, pp))
		if err = a.validateAutoAnswer(request, &row, autoAnswerTestSession, "sales", completed); (err == nil) != completed {
			t.Fatalf("completion %v: %v", completed, err)
		}
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales", "auto_answer_session": autoAnswerTestSession})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "auto_answer_warning_required") {
		t.Fatalf("unconfirmed warning bypassed HTTP gate: %d %s", w.Code, w.Body)
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{"destination_id": "sales"})
	if w.Code != 200 {
		t.Fatalf("manual answer blocked by warning policy: %d %s", w.Code, w.Body)
	}
	// A separate adviser retains the manual default, with no warning policy.
	other, _ := a.effectiveAutoAnswer("project-a", phoneTestIdentity("bob").key(), "", "sales")
	if other.Mode != "manual" || other.WarningTone != nil {
		t.Fatal("policy leaked")
	}
}
func TestAutoAnswerWarningBounds(t *testing.T) {
	for _, ms := range []int{-1, 1, 749, 3001} {
		p := autoAnswerPolicy{Mode: "automatic", WarningTone: &autoAnswerWarningPolicy{Enabled: true, WindowMS: ms}}
		if p.validate() == nil {
			t.Fatalf("accepted %d", ms)
		}
	}
	for _, ms := range []int{0, 750, 1000, 3000} {
		p := autoAnswerPolicy{Mode: "automatic", WarningTone: &autoAnswerWarningPolicy{Enabled: true, WindowMS: ms}}
		if err := p.validate(); err != nil {
			t.Fatal(ms, err)
		}
	}
}
