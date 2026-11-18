package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws/wsutil"
)

func standbyTestCtx(t *testing.T, webrtc bool) *App {
	t.Helper()
	t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
	config := map[string]string{}
	if webrtc {
		config["softphone_webrtc_enabled"] = "true"
		config["softphone_webrtc_handover"] = "true"
	}
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&answerPlatform{}), tk.WithConfig(config))
	t.Cleanup(func() { globalCtx = previous })
	return &App{installID: 42}
}

type standbyGrant struct {
	StandbyID   string `json:"standby_id"`
	CallID      string `json:"call_id"`
	MediaURL    string `json:"media_url"`
	ExpiresInMS int64  `json:"expires_in_ms"`
}

func (g standbyGrant) token() string { return g.MediaURL[strings.LastIndex(g.MediaURL, "/")+1:] }

func decodeStandby(t *testing.T, w *httptest.ResponseRecorder) standbyGrant {
	t.Helper()
	var grant standbyGrant
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &grant) != nil || grant.StandbyID == "" || grant.MediaURL == "" {
		t.Fatalf("standby = %d %s", w.Code, w.Body)
	}
	return grant
}

// operatorStandby asks for a standby as the trusted operator panel.
func operatorStandby(t *testing.T, app *App, callID string) standbyGrant {
	t.Helper()
	w := httptest.NewRecorder()
	app.softphoneStandbyRequest(w, httptest.NewRequest(http.MethodPost, "/softphone/standby/"+callID, nil), "project-a", callID)
	return decodeStandby(t, w)
}

func operatorAnswer(t *testing.T, app *App, callID string, body string) softphoneSession {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/softphone/answer/"+callID, strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), answerRequestArrivalKey{}, time.Now()))
	w := httptest.NewRecorder()
	app.softphoneAnswer(w, request, "project-a", callID)
	var session softphoneSession
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	return session
}

func standbyByID(app *App, id string) *softphoneStandby {
	app.softphoneStandbys.mu.Lock()
	defer app.softphoneStandbys.mu.Unlock()
	return app.softphoneStandbys.byID[id]
}

func waitStandbyState(t *testing.T, s *softphoneStandby, state int32) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for s.state.Load() != state {
		if time.Now().After(deadline) {
			t.Fatalf("standby state %d, want %d", s.state.Load(), state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func insertPendingTelnyxCall(t *testing.T, app *App, id string) callRow {
	t.Helper()
	row := callRow{
		ID: id, ThreadID: "pending-" + id, Direction: "inbound", CarrierSlug: "telnyx",
		CarrierConnectionID: 10, CarrierSID: "telnyx-" + id, CallbackSecret: "callback-secret",
		ToNumber: "+33123456789", FromNumber: "+33612345678", Status: "pending",
		PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
		PeerKind: peerKindHuman, AudioBridgeURL: "pending",
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	return row
}

// A standby is issued only for a call this browser is being offered, only
// with WebRTC, and ends no later than the offer.
func TestStandbyIssuedOnlyForOfferedCalls(t *testing.T) {
	app := standbyTestCtx(t, true)
	phoneTestPolicy(t, app)
	row := phoneTestRingOfferCall(t, app, "standby-offered")
	alice, eve := phoneTestIdentity("alice"), phoneTestIdentity("eve")
	grant := decodeStandby(t, phoneTestRequest(app, &alice, "POST", "/softphone/standby/"+row.ID, map[string]any{"destination_id": "sales"}))
	if grant.CallID != row.ID || grant.ExpiresInMS <= 0 || grant.ExpiresInMS > 20_000 {
		t.Fatalf("grant %+v", grant)
	}
	if !strings.HasPrefix(grant.MediaURL, "/api/apps/telephony/_install/42/softphone/media/"+row.ID+"/") {
		t.Fatalf("media url %s", grant.MediaURL)
	}
	// Eve's destination is queued, not offered.
	if w := phoneTestRequest(app, &eve, "POST", "/softphone/standby/"+row.ID, map[string]any{}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not_offered") {
		t.Fatalf("eve standby = %d %s", w.Code, w.Body)
	}
	answered := phoneTestCall(t, app, "standby-answered", "in-progress")
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/standby/"+answered.ID, map[string]any{}); w.Code != http.StatusConflict {
		t.Fatalf("answered call standby = %d %s", w.Code, w.Body)
	}
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/standby/missing", map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown call standby = %d %s", w.Code, w.Body)
	}
}

func TestStandbyRequiresWebRTC(t *testing.T) {
	app := standbyTestCtx(t, false)
	row := insertPendingTelnyxCall(t, app, "standby-no-webrtc")
	w := httptest.NewRecorder()
	app.softphoneStandbyRequest(w, httptest.NewRequest(http.MethodPost, "/softphone/standby/"+row.ID, nil), "project-a", row.ID)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "webrtc_unavailable") {
		t.Fatalf("standby without WebRTC = %d %s", w.Code, w.Body)
	}
}

// An install can turn ring-time connections off.
func TestStandbyCanBeTurnedOff(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&answerPlatform{}), tk.WithConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_standby": "false"}))
	t.Cleanup(func() { globalCtx = previous })
	app := &App{installID: 42}
	row := insertPendingTelnyxCall(t, app, "standby-off")
	w := httptest.NewRecorder()
	app.softphoneStandbyRequest(w, httptest.NewRequest(http.MethodPost, "/softphone/standby/"+row.ID, nil), "project-a", row.ID)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "standby_disabled") {
		t.Fatalf("standby turned off = %d %s", w.Code, w.Body)
	}
}

// The answer promotes the browser's own parked standby with the claim's media
// grant; someone else's answer ends it.
func TestStandbyPromotedByItsOwnAnswerOnly(t *testing.T) {
	app := standbyTestCtx(t, true)
	phoneTestPolicy(t, app)
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	park := func(callID string, who phoneIdentity) (standbyGrant, *softphoneStandby) {
		grant := decodeStandby(t, phoneTestRequest(app, &who, "POST", "/softphone/standby/"+callID, map[string]any{"destination_id": "sales"}))
		s := app.softphoneStandbys.connect(callID, grant.token())
		if s == nil || !s.state.CompareAndSwap(standbyConnecting, standbyParked) {
			t.Fatal("standby did not connect")
		}
		return grant, s
	}

	own := phoneTestRingOfferCall(t, app, "standby-own")
	grant, s := park(own.ID, alice)
	w := phoneTestRequest(app, &alice, "POST", "/softphone/answer/"+own.ID, map[string]any{"destination_id": "sales", "standby_id": grant.StandbyID})
	var session softphoneSession
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Standby != "promoted" {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	select {
	case token := <-s.promoted:
		if token != session.SessionToken || s.mediaToken() != token {
			t.Fatal("standby promoted with another grant")
		}
	default:
		t.Fatal("standby not promoted")
	}
	if standbyByID(app, grant.StandbyID) != nil {
		t.Fatal("promoted standby still registered")
	}

	other := phoneTestRingOfferCall(t, app, "standby-other")
	grant, s = park(other.ID, alice)
	w = phoneTestRequest(app, &bob, "POST", "/softphone/answer/"+other.ID, map[string]any{"destination_id": "sales", "standby_id": grant.StandbyID})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Standby != "unavailable" {
		t.Fatalf("bob's answer = %d %s", w.Code, w.Body)
	}
	select {
	case <-s.ended:
	default:
		t.Fatal("alice's standby kept after bob claimed the call")
	}
}

func TestStandbyRegistryBounds(t *testing.T) {
	var r softphoneStandbyRegistry
	issue := func(id, call, principal string) *softphoneStandby {
		s := &softphoneStandby{id: id, callID: call, projectID: "p", principal: principal, expires: time.Now().Add(time.Minute)}
		if err := r.issue(s); err != nil {
			t.Fatalf("issue %s: %v", id, err)
		}
		return s
	}
	// A new standby for the same call replaces the browser's previous one.
	first := issue("a1", "call-1", "alice")
	issue("a2", "call-1", "alice")
	if first.state.Load() != standbyEnded {
		t.Fatal("replaced standby still waiting")
	}
	// Beyond the per-principal bound the oldest standby gives way.
	oldest := issue("a3", "call-2", "alice")
	oldest.expires = time.Now().Add(time.Second)
	issue("a4", "call-3", "alice")
	issue("a5", "call-4", "alice")
	issue("a6", "call-5", "alice")
	if oldest.state.Load() != standbyEnded {
		t.Fatal("oldest standby kept beyond the per-principal bound")
	}
	// A standby being claimed is never ended to make room.
	claiming := standbyByIDIn(&r, "a2")
	claiming.state.Store(standbyClaiming)
	if r.endIfWaiting(claiming) || claiming.state.Load() != standbyClaiming {
		t.Fatal("claiming standby ended")
	}
	r.endCall("call-1", nil)
	if claiming.state.Load() != standbyClaiming {
		t.Fatal("claiming standby ended by endCall")
	}
	r.release(claiming)
	if claiming.state.Load() != standbyParked || !r.endIfWaiting(claiming) {
		t.Fatal("released standby could not end")
	}
	select {
	case <-claiming.ended:
	default:
		t.Fatal("ended standby did not signal")
	}
	// Trusted operators share no identity: two operators' standbys for one call
	// do not replace each other.
	operatorA, operatorB := issue("o1", "call-ops", ""), issue("o2", "call-ops", "")
	if operatorA.state.Load() == standbyEnded || operatorB.state.Load() == standbyEnded {
		t.Fatal("an operator's standby replaced another operator's")
	}
	// Per call.
	for i := 0; i < standbyPerCall; i++ {
		issue("c"+string(rune('a'+i)), "busy", "user-"+string(rune('a'+i)))
	}
	if err := r.issue(&softphoneStandby{id: "overflow", callID: "busy", projectID: "p", principal: "late", expires: time.Now().Add(time.Minute)}); err != errStandbyLimit {
		t.Fatalf("per-call bound: %v", err)
	}
	// An expired standby is dropped at the next issue.
	expired := issue("x1", "call-x", "zed")
	expired.expires = time.Now().Add(-time.Second)
	issue("x2", "call-y", "yan")
	if standbyByIDIn(&r, "x1") != nil {
		t.Fatal("expired standby kept")
	}
}

func standbyByIDIn(r *softphoneStandbyRegistry, id string) *softphoneStandby {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// A standby token works once, and only for WebRTC.
func TestStandbyTokenIsSingleUseWebRTCOnly(t *testing.T) {
	app := standbyTestCtx(t, true)
	row := insertPendingTelnyxCall(t, app, "standby-websocket")
	grant := operatorStandby(t, app, row.ID)
	server := softphoneTestServer(t, app)
	url := server.URL + "/softphone/media/" + row.ID + "/" + grant.token()
	for _, attempt := range []string{"", "?transport=webrtc"} {
		response, err := http.Get(url + attempt)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatalf("standby media%s = %d", attempt, response.StatusCode)
		}
	}
	if standbyByID(app, grant.StandbyID) != nil {
		t.Fatal("rejected standby kept")
	}
}

// The full ring-time path: WebRTC connects while the call rings, the answer
// promotes it, and the carrier is answered only once the browser says its
// microphone is attached.
func TestStandbyRealConnectionAnswersAfterArmed(t *testing.T) {
	app := standbyTestCtx(t, true)
	row := insertPendingTelnyxCall(t, app, "standby-rtc")
	grant := operatorStandby(t, app, row.ID)
	server := softphoneTestServer(t, app)
	browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/"+grant.token()+"?transport=webrtc", func() {})
	s := standbyByID(app, grant.StandbyID)
	if s == nil {
		t.Fatal("standby not registered")
	}
	waitStandbyState(t, s, standbyParked)
	if current, _ := app.db().findCall(row.ID); current.Status != "pending" {
		t.Fatalf("parked standby changed the call: %s", current.Status)
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
	session := operatorAnswer(t, app, row.ID, `{"standby_id":"`+grant.StandbyID+`"}`)
	if session.Standby != "promoted" {
		t.Fatalf("session %+v", session)
	}
	// Nothing is answered until the browser's microphone is attached.
	_ = browser.socket.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	for {
		data, _, err := wsutil.ReadServerData(browser.socket)
		if err != nil {
			break
		}
		if strings.Contains(string(data), `"type":"ready"`) {
			t.Fatal("ready before the browser armed")
		}
	}
	if current, _ := app.db().findCall(row.ID); current.Status != "answering" {
		t.Fatalf("carrier answered before the browser armed: %s", current.Status)
	}
	if err := wsutil.WriteClientText(browser.socket, []byte(`{"type":"standby.armed"}`)); err != nil {
		t.Fatal(err)
	}
	readSoftphoneEventWithin(t, browser.socket, "ready", 8*time.Second)
	var line map[string]any
	select {
	case line = <-lines:
	case <-time.After(2 * time.Second):
		t.Fatal("no timing line")
	}
	if line["standby"] != true || line["transport"] != "webrtc" || line["browser_attach_ms"] != int64(0) {
		t.Fatalf("timing line %v", line)
	}
	previous := int64(0)
	for _, key := range []string{"claim_ms", "session_returned_ms", "rtc_ready_ms", "carrier_answer_ms", "media_start_ms", "total_ms"} {
		value, ok := line[key].(int64)
		if !ok || value < previous {
			t.Fatalf("%s = %v after %d: %v", key, line[key], previous, line)
		}
		previous = value
	}
}

// A browser that never attaches its microphone gives the call back.
func TestStandbyUnarmedGivesCallBack(t *testing.T) {
	previous := standbyArmTimeout
	standbyArmTimeout = 300 * time.Millisecond
	t.Cleanup(func() { standbyArmTimeout = previous })
	app := standbyTestCtx(t, true)
	row := insertPendingTelnyxCall(t, app, "standby-unarmed")
	grant := operatorStandby(t, app, row.ID)
	server := softphoneTestServer(t, app)
	browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/"+grant.token()+"?transport=webrtc", func() {})
	waitStandbyState(t, standbyByID(app, grant.StandbyID), standbyParked)
	if session := operatorAnswer(t, app, row.ID, `{"standby_id":"`+grant.StandbyID+`"}`); session.Standby != "promoted" {
		t.Fatalf("session %+v", session)
	}
	_ = browser.socket.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := wsutil.ReadServerData(browser.socket); err != nil {
			break
		}
	}
	current, _ := app.db().findCall(row.ID)
	if current.Status != "pending" || current.PeerToken != "" {
		t.Fatalf("unarmed claim kept: %s token=%q", current.Status, current.PeerToken)
	}
}

// When another browser answers, a parked standby closes.
func TestStandbyClosesWhenCallClaimedElsewhere(t *testing.T) {
	app := standbyTestCtx(t, true)
	row := insertPendingTelnyxCall(t, app, "standby-elsewhere")
	grant := operatorStandby(t, app, row.ID)
	server := softphoneTestServer(t, app)
	browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/"+grant.token()+"?transport=webrtc", func() {})
	s := standbyByID(app, grant.StandbyID)
	waitStandbyState(t, s, standbyParked)
	if session := operatorAnswer(t, app, row.ID, `{}`); session.Standby != "" {
		t.Fatalf("session %+v", session)
	}
	_ = browser.socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		data, _, err := wsutil.ReadServerData(browser.socket)
		if err != nil {
			break
		}
		if strings.Contains(string(data), `"type":"ready"`) {
			t.Fatal("standby attached to a call claimed elsewhere")
		}
	}
	if s.state.Load() != standbyEnded {
		t.Fatalf("standby state %d", s.state.Load())
	}
}

// If the ring-time connection is lost right after the claim, the claim stays
// with the browser, which connects again with its grant.
func TestStandbyLostAfterClaimLeavesClaimForFreshConnection(t *testing.T) {
	app := standbyTestCtx(t, true)
	row := insertPendingTelnyxCall(t, app, "standby-lost")
	grant := operatorStandby(t, app, row.ID)
	server := softphoneTestServer(t, app)
	browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/"+grant.token()+"?transport=webrtc", func() {})
	waitStandbyState(t, standbyByID(app, grant.StandbyID), standbyParked)
	session := operatorAnswer(t, app, row.ID, `{"standby_id":"`+grant.StandbyID+`"}`)
	if session.Standby != "promoted" {
		t.Fatalf("session %+v", session)
	}
	browser.socket.Close()
	browser.pc.Close()
	time.Sleep(200 * time.Millisecond)
	if current, _ := app.db().findCall(row.ID); current.Status != "answering" || current.PeerToken != session.SessionToken {
		t.Fatalf("claim not kept: %s", current.Status)
	}
	fresh := dialWS(t, server.URL+"/softphone/media/"+row.ID+"/"+session.SessionToken)
	readSoftphoneEventWithin(t, fresh, "ready", 8*time.Second)
}

// A browser that leaves while its answer is being claimed, when that claim
// then fails, does not leave its standby behind.
func TestStandbyLeftDuringFailedClaimEnds(t *testing.T) {
	app := standbyTestCtx(t, true)
	row := insertPendingTelnyxCall(t, app, "standby-left")
	grant := operatorStandby(t, app, row.ID)
	server := softphoneTestServer(t, app)
	browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/"+grant.token()+"?transport=webrtc", func() {})
	s := standbyByID(app, grant.StandbyID)
	waitStandbyState(t, s, standbyParked)
	if app.softphoneStandbys.reserve(row.ID, grant.StandbyID, "") != s {
		t.Fatal("reserve failed")
	}
	browser.socket.Close()
	browser.pc.Close()
	time.Sleep(300 * time.Millisecond)
	if s.state.Load() != standbyClaiming {
		t.Fatalf("claiming standby state %d", s.state.Load())
	}
	app.softphoneStandbys.release(s)
	waitStandbyState(t, s, standbyEnded)
}
