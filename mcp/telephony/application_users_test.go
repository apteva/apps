package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func phoneTestIdentity(id string) phoneIdentity {
	return phoneIdentity{"auth", "11", "user", id, "org-1"}
}
func phoneTestRequest(app *App, identity *phoneIdentity, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	if identity != nil {
		r.Header.Set("X-Apteva-Issuer-App", identity.IssuerApp)
		r.Header.Set("X-Apteva-Issuer-Install-ID", identity.IssuerInstallID)
		r.Header.Set("X-Apteva-Subject-Type", identity.SubjectType)
		r.Header.Set("X-Apteva-Subject-ID", identity.SubjectID)
		r.Header.Set("X-Apteva-Organization-ID", identity.OrganizationID)
		r.Header.Set("X-Apteva-Scopes", `[{"type":"app_user","app":"telephony","actions":["call.read","call.dial","call.answer","call.attach","call.hangup","call.takeover"]}]`)
	}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func phoneTestPolicy(t *testing.T, app *App) phonePolicy {
	t.Helper()
	for _, id := range []string{"sales", "support"} {
		if _, e := app.saveRoutingDestination("project-a", id, id, "browser", map[string]any{}, true); e != nil {
			t.Fatal(e)
		}
	}
	p := phonePolicy{Users: []phoneUser{
		{Identity: phoneTestIdentity("alice"), Enabled: true, Groups: []string{"sales-team"}, phoneGrant: phoneGrant{Role: "user"}},
		{Identity: phoneTestIdentity("bob"), Enabled: true, Groups: []string{"sales-team"}, phoneGrant: phoneGrant{Role: "user"}},
		{Identity: phoneTestIdentity("eve"), Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"support"}}},
		{Identity: phoneTestIdentity("boss"), Enabled: true, phoneGrant: phoneGrant{Role: "supervisor", Destinations: []string{"sales"}, OutboundNumbers: []string{"+13502231050"}}},
	}, Groups: []phoneGroup{{ID: "sales-team", phoneGrant: phoneGrant{Role: "user", Destinations: []string{"sales"}, OutboundNumbers: []string{"+13502231050"}}}}}
	w := phoneTestRequest(app, nil, "PUT", "/access/policy", p)
	if w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	return p
}
func phoneTestCall(t *testing.T, app *App, id, status string) callRow {
	t.Helper()
	row := callRow{ID: id, ThreadID: "test-" + id, Direction: "inbound", CarrierSlug: "twilio", Status: status, ProjectID: "project-a", PeerKind: peerKindHuman, PeerToken: "peer-" + id, CallbackSecret: "cb-" + id, ToNumber: "+13502231050", FromNumber: "+13334445555", RoutingDestinationID: "sales", PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if e := app.db().insertCall(row); e != nil {
		t.Fatal(e)
	}
	return row
}
func TestApplicationUserPolicyIsolation(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	p := phoneTestPolicy(t, app)
	alice := phoneTestIdentity("alice")
	phoneTestCall(t, app, "sales-call", "pending")
	w := phoneTestRequest(app, &alice, "GET", "/calls", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "sales-call") {
		t.Fatalf("authorized incoming missing: %d %s", w.Code, w.Body)
	}
	eve := phoneTestIdentity("eve")
	w = phoneTestRequest(app, &eve, "GET", "/calls", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "sales-call") {
		t.Fatalf("other destination leaked: %d %s", w.Code, w.Body)
	}
	for _, change := range []func(*phoneIdentity){func(p *phoneIdentity) { p.IssuerInstallID = "12" }, func(p *phoneIdentity) { p.OrganizationID = "org-2" }, func(p *phoneIdentity) { p.IssuerApp = "other" }, func(p *phoneIdentity) { p.SubjectID = "" }} {
		identity := alice
		change(&identity)
		if w := phoneTestRequest(app, &identity, "GET", "/calls", nil); w.Code < 400 {
			t.Fatalf("identity accepted: %+v", identity)
		}
	}
	for _, path := range []string{"/calls?project_id=other", "/recordings/x", "/routing/destinations", "/numbers/connected", "/access/policy"} {
		if w := phoneTestRequest(app, &alice, "GET", path, nil); w.Code < 400 {
			t.Fatalf("unauthorized path %s: %d", path, w.Code)
		}
	}
	if w := phoneTestRequest(app, &alice, "PUT", "/access/policy", p); w.Code != 403 {
		t.Fatal("user modified grants")
	}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", phonePolicy{}); w.Code != 409 {
		t.Fatalf("stale policy overwrote access: %d", w.Code)
	}
	for i := range p.Users {
		if p.Users[i].Identity == alice {
			p.Users[i].Enabled = false
		}
	}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", p); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := phoneTestRequest(app, &alice, "GET", "/calls", nil); w.Code != 403 {
		t.Fatal("revoked user accepted")
	}
}
func TestApplicationUserAnswerOwnershipAndTakeover(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "sales-call", "pending")
	alice, bob, eve, boss := phoneTestIdentity("alice"), phoneTestIdentity("bob"), phoneTestIdentity("eve"), phoneTestIdentity("boss")
	if w := phoneTestRequest(app, &eve, "POST", "/softphone/answer/"+row.ID, map[string]any{}); w.Code != 404 {
		t.Fatalf("unauthorized offer answer: %d", w.Code)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, p := range []phoneIdentity{alice, bob} {
		wg.Add(1)
		go func(p phoneIdentity) {
			defer wg.Done()
			codes <- phoneTestRequest(app, &p, "POST", "/softphone/answer/"+row.ID, map[string]any{}).Code
		}(p)
	}
	wg.Wait()
	close(codes)
	wins := 0
	for c := range codes {
		if c == 200 {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent winners: %d", wins)
	}
	owner, _, _ := app.phoneOwner(row.ID)
	winner, loser := alice, bob
	if owner == bob.key() {
		winner, loser = bob, alice
	}
	for _, path := range []string{"/softphone/attach/", "/softphone/takeover/", "/softphone/answer/"} {
		w := phoneTestRequest(app, &loser, "POST", path+row.ID, map[string]any{"rejoin": true})
		if w.Code < 400 {
			t.Fatalf("unauthorized takeover via %s", path)
		}
	}
	if w := phoneTestRequest(app, &loser, "POST", "/calls/"+row.ID+"/hangup", nil); w.Code != 404 {
		t.Fatalf("cross-user hangup: %d", w.Code)
	}
	if w := phoneTestRequest(app, &loser, "GET", "/calls?call_id="+row.ID, nil); w.Code != 404 {
		t.Fatalf("claimed call leaked: %d %s", w.Code, w.Body)
	}
	w := phoneTestRequest(app, &winner, "POST", "/softphone/attach/"+row.ID, nil)
	if w.Code != 200 {
		t.Fatalf("own attach: %d %s", w.Code, w.Body)
	}
	var session softphoneSession
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	fresh, _ := app.db().findCall(row.ID)
	if !app.validPhoneMedia(fresh, session.SessionToken) {
		t.Fatal("own media rejected")
	}
	if app.validPhoneMedia(fresh, fresh.PeerToken) {
		t.Fatal("legacy media bypassed owner")
	}
	w = phoneTestRequest(app, &boss, "POST", "/softphone/takeover/"+row.ID, nil)
	if w.Code != 200 {
		t.Fatalf("supervisor takeover: %d %s", w.Code, w.Body)
	}
	if app.validPhoneMedia(fresh, session.SessionToken) {
		t.Fatal("old media survived takeover")
	}
	if w := phoneTestRequest(app, &winner, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": session.SessionToken}); w.Code < 400 {
		t.Fatal("old user renewed after takeover")
	}
}
func TestApplicationUserBackendAssignmentAndLease(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	policy := phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "backend-call", "in-progress")
	alice := phoneTestIdentity("alice")
	w := phoneTestRequest(app, nil, "POST", "/access/assign/"+row.ID, alice)
	if w.Code != 200 {
		t.Fatalf("assign: %d %s", w.Code, w.Body)
	}
	w = phoneTestRequest(app, &alice, "POST", "/softphone/attach/"+row.ID, nil)
	var session softphoneSession
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatalf("attach: %d %s", w.Code, w.Body)
	}
	if session.CallID != row.ID || session.LeaseSeconds != 60 {
		t.Fatal("wrong backend call session")
	}
	w = phoneTestRequest(app, &alice, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": session.SessionToken})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/place", map[string]any{"to": "+12025550100", "from": "+12025550199", "idempotency_key": "test"}); w.Code != 403 {
		t.Fatal("unpermitted outbound number accepted")
	}
	server := softphoneTestServer(t, app)
	browser := dialWS(t, server.URL+strings.Replace(session.MediaURL, "/api/apps/telephony/_install/42", "", 1))
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	for i := range policy.Users {
		if policy.Users[i].Identity == alice {
			policy.Users[i].Enabled = false
		}
	}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if app.validPhoneMedia(&row, session.SessionToken) {
		t.Fatal("revocation retained media grant")
	}
	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, _, err := wsutil.ReadServerData(browser)
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("revoked socket remained open")
			}
			break
		}
	}
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": session.SessionToken}); w.Code != 403 {
		t.Fatal("revoked user renewed lease")
	}
	_, _, _, err := ws.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+strings.Replace(session.MediaURL, "/api/apps/telephony/_install/42", "", 1))
	if err == nil {
		t.Fatal("revoked media reconnected")
	}
}
func TestApplicationUserMCPAndIncompleteScopesDenied(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	for _, tool := range app.MCPTools() {
		if tool.HandlerCtx != nil {
			_, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{SubjectType: "user", SubjectID: "alice"}), globalCtx, map[string]any{})
			if err == nil || !strings.Contains(err.Error(), "application users") {
				t.Fatalf("MCP bypass: %s", tool.Name)
			}
		}
	}
	identity := phoneTestIdentity("alice")
	r := httptest.NewRequest("GET", "/calls", nil)
	r.Header.Set("X-Apteva-Issuer-App", identity.IssuerApp)
	r.Header.Set("X-Apteva-Issuer-Install-ID", identity.IssuerInstallID)
	r.Header.Set("X-Apteva-Subject-Type", "user")
	r.Header.Set("X-Apteva-Subject-ID", identity.SubjectID)
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	r.Header.Set("X-Apteva-Organization-ID", identity.OrganizationID)
	for _, scopes := range []string{"", `[{"type":"app_user","app":"telephony","actions":["*"]}]`, `[{"type":"app_user","app":"other","actions":["call.read"]}]`} {
		r.Header.Set("X-Apteva-Scopes", scopes)
		w := httptest.NewRecorder()
		app.applicationUserHTTP(app.handleListCalls)(w, r)
		if w.Code != 403 {
			t.Fatal("missing exact scope accepted")
		}
	}
}
func TestApplicationUserVisibilityBeforeLimit(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	phoneTestCall(t, app, "visible", "pending")
	for i := 0; i < 105; i++ {
		row := phoneTestCall(t, app, fmt.Sprintf("hidden-%03d", i), "pending")
		if _, e := app.db().db.Exec(`UPDATE calls SET routing_destination_id='support',placed_at='2099-01-01T00:00:00Z' WHERE id=?`, row.ID); e != nil {
			t.Fatal(e)
		}
	}
	alice := phoneTestIdentity("alice")
	w := phoneTestRequest(app, &alice, "GET", "/calls", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "visible") || strings.Contains(w.Body.String(), "hidden-") {
		t.Fatalf("visibility pagination: %d %s", w.Code, w.Body)
	}
}

func TestApplicationUserDialIdempotencyIsPrincipalScoped(t *testing.T) {
	platform := &answerPlatform{bindings: map[string]any{"carrier": int64(9)}, credentials: &sdk.ConnectionCredentials{Slug: "twilio", Fields: map[string]string{"auth_token": "test-auth-token", "phone_number": "+13502231050"}}, integrationResponse: map[string]json.RawMessage{"make_call": json.RawMessage(`{"sid":"CA-outbound"}`)}}
	app, _ := withTelephonyTestContext(t, platform)
	phoneTestPolicy(t, app)
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	dial := func(p phoneIdentity) softphoneSession {
		w := phoneTestRequest(app, &p, "POST", "/softphone/place", map[string]any{"to": "+14155550100", "from": "+13502231050", "idempotency_key": "same-client-key"})
		var session softphoneSession
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
			t.Fatalf("dial: %d %s", w.Code, w.Body)
		}
		return session
	}
	first := dial(alice)
	retry := dial(alice)
	other := dial(bob)
	if first.CallID != retry.CallID || first.CallID == other.CallID {
		t.Fatal("idempotency crossed principals")
	}
	owner, _, err := app.phoneOwner(first.CallID)
	if err != nil || owner != alice.key() {
		t.Fatal("outbound owner missing")
	}
	row, _ := app.db().findCall(first.CallID)
	if app.validPhoneMedia(row, first.SessionToken) {
		t.Fatal("reissued token retained stale media")
	}
	if !app.validPhoneMedia(row, retry.SessionToken) {
		t.Fatal("current media denied")
	}
	if _, err = app.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=0 WHERE call_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if app.validPhoneMedia(row, retry.SessionToken) {
		t.Fatal("expired media accepted")
	}
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": retry.SessionToken}); w.Code != 403 {
		t.Fatal("expired lease resurrected")
	}
}

func TestApplicationUserIVRSelectionRestrictsRinging(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "ivr-call", "pending")
	if _, err := app.db().db.Exec(`UPDATE calls SET routing_flow_version_id='published',routing_destination_id='' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	alice := phoneTestIdentity("alice")
	eve := phoneTestIdentity("eve")
	if w := phoneTestRequest(app, &alice, "GET", "/calls", nil); strings.Contains(w.Body.String(), row.ID) {
		t.Fatal("IVR rang before selection")
	}
	result := simulateRoutingDefinition(routingDefinition{Entry: "menu", Nodes: []routingNode{
		{ID: "menu", Type: "dtmf_menu", Branches: map[string]string{"1": "sales", "default": "support"}},
		{ID: "sales", Type: "destination", Config: map[string]any{"destination_id": "sales"}},
		{ID: "support", Type: "destination", Config: map[string]any{"destination_id": "support"}},
	}}, routingSimulationContext{Digits: map[string]string{"menu": "1"}})
	if !result.Valid {
		t.Fatal(result)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET routing_destination_id=? WHERE id=?`, result.DestinationID, row.ID); err != nil {
		t.Fatal(err)
	}
	if w := phoneTestRequest(app, &alice, "GET", "/calls", nil); !strings.Contains(w.Body.String(), row.ID) {
		t.Fatal("assigned IVR destination did not ring")
	}
	if w := phoneTestRequest(app, &eve, "GET", "/calls", nil); strings.Contains(w.Body.String(), row.ID) {
		t.Fatal("IVR selection leaked to other group")
	}
}
