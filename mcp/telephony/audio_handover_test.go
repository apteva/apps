package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func handoverTestCtx(t *testing.T, on bool) *App {
	t.Helper()
	config := map[string]string{"softphone_webrtc_enabled": "true"}
	if on {
		config["softphone_webrtc_handover"] = "true"
	}
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithConfig(config))
	t.Cleanup(func() { globalCtx = previous })
	return &App{installID: 42}
}

func handoverTestLimits(t *testing.T, sample, hold, quiet time.Duration) {
	t.Helper()
	a, b, c := rtcHandoverSample, rtcHandoverHoldLimit, rtcHandoverQuiet
	rtcHandoverSample, rtcHandoverHoldLimit, rtcHandoverQuiet = sample, hold, quiet
	t.Cleanup(func() { rtcHandoverSample, rtcHandoverHoldLimit, rtcHandoverQuiet = a, b, c })
}

type handoverFixture struct {
	app     *App
	row     callRow
	server  *httptest.Server
	first   *softphoneSession
	browser *rtcTestBrowser
	writer  *websocketWriterPump
}

func newHandoverFixture(t *testing.T, on bool) *handoverFixture {
	t.Helper()
	app := handoverTestCtx(t, on)
	row := insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	t.Cleanup(server.Close)
	dialWS(t, server.URL+"/peer/"+row.ID+"/cb-secret")
	first, e := app.issuePhoneSession(&row, nil)
	if e != nil {
		t.Fatal(e)
	}
	browser := rtcTestConnect(t, handoverMediaURL(server, row.ID, first.SessionToken))
	return &handoverFixture{app: app, row: row, server: server, first: first, browser: browser, writer: app.softphones.lookup(row.ID).browserWriter()}
}

func handoverMediaURL(server *httptest.Server, id, token string) string {
	return server.URL + "/softphone/media/" + id + "/" + token + "?transport=webrtc"
}

// Keeps microphone RTP arriving at the server, as a healthy browser would.
func keepMicFlowing(t *testing.T, b *rtcTestBrowser) (stop func()) {
	t.Helper()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				_ = b.mic.WriteSample(media.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond})
			}
		}
	}()
	var once atomic.Bool
	stop = func() {
		if once.CompareAndSwap(false, true) {
			close(done)
			<-finished
		}
	}
	t.Cleanup(stop)
	return stop
}

func waitForDetach(t *testing.T, app *App, id string, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if h := app.softphones.lookup(id); h == nil || h.browserWriter() == nil {
			return time.Since(start)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection was still attached after %v", within)
	return 0
}

func stillAttached(t *testing.T, app *App, id string, writer *websocketWriterPump, during time.Duration) {
	t.Helper()
	for end := time.Now().Add(during); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if h := app.softphones.lookup(id); h == nil || h.browserWriter() != writer {
			t.Fatal("connection was torn down while its audio was healthy")
		}
	}
}

func handoverEvents(app *App, name string) []audioNetworkEvent {
	app.audioNetworks.mu.Lock()
	defer app.audioNetworks.mu.Unlock()
	var out []audioNetworkEvent
	for _, e := range app.audioNetworks.pending {
		if e.Event == name {
			out = append(out, e)
		}
	}
	return out
}

func issueCandidate(t *testing.T, f *handoverFixture, expected string) *softphoneSession {
	t.Helper()
	s, e := f.app.issuePendingPhoneSession(context.Background(), &f.row, nil, audioSessionCorrelation{}, &phoneAttachmentGuard{ID: newAudioSessionID(), Expected: expected, Handover: true})
	if e != nil {
		t.Fatal(e)
	}
	if !s.HandoverPending {
		t.Fatal("candidate must say it is pending")
	}
	return s
}

// Regression 1: signaling drops while RTP is healthy. The audio survives, and a
// replacement takes over without a gap in authorization.
func TestHandoverKeepsAudioThroughASignalingDropAndReplaces(t *testing.T) {
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	if e := f.browser.socket.Close(); e != nil {
		t.Fatal(e)
	}
	stillAttached(t, f.app, f.row.ID, f.writer, 1500*time.Millisecond)
	if n := len(handoverEvents(f.app, "softphone.browser.signaling_lost")); n != 1 {
		t.Fatal("the signaling fault must be recorded while audio flows", n)
	}
	candidate := issueCandidate(t, f, f.first.SessionGeneration)
	// The current session is untouched until the candidate has connected.
	if reason := f.app.phoneMediaDenialReason(&f.row, f.first.SessionToken); reason != "" {
		t.Fatal("issuing a candidate must not disturb the live session", reason)
	}
	if reason := f.app.phoneMediaDenialReason(&f.row, candidate.SessionToken); reason != "media_token_replaced" {
		t.Fatal("a candidate grant must be useless outside the media attach", reason)
	}
	next := rtcTestConnect(t, handoverMediaURL(f.server, f.row.ID, candidate.SessionToken))
	stopNext := keepMicFlowing(t, next)
	defer stopNext()
	if h := f.app.softphones.lookup(f.row.ID); h == nil || h.browserWriter() == f.writer || h.browserWriter() == nil {
		t.Fatal("the candidate must replace the old connection")
	}
	if reason := f.app.phoneMediaDenialReason(&f.row, candidate.SessionToken); reason != "" {
		t.Fatal("the committed candidate must be the active session", reason)
	}
	if reason := f.app.phoneMediaDenialReason(&f.row, f.first.SessionToken); reason != "media_token_replaced" {
		t.Fatal("the old session must end at commit", reason)
	}
	if n := len(handoverEvents(f.app, "softphone.media.handover_committed")); n != 1 {
		t.Fatal("the commit must be recorded", n)
	}
	var raw string
	if e := f.app.db().db.QueryRow(`SELECT diagnostic_json FROM telephony_media_sessions WHERE call_id=?`, f.row.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var state audioSessionCorrelation
	if e := json.Unmarshal([]byte(raw), &state); e != nil || state.SessionID != candidate.SessionGeneration || state.PreviousSessionID != f.first.SessionGeneration {
		t.Fatal("generation history was lost at commit", raw, e)
	}
	if !secureEqual(phoneHash(candidate.SessionToken), mustPendingCleared(t, f)) {
		t.Fatal("the active grant must be the candidate's")
	}
	// Read back from the store as the incident report does, not from memory. The
	// candidate's connected event follows its ready message, so it can arrive later.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := f.app.audioNetworks.flush(t.Context(), f.app.db(), time.Now()); e != nil {
			t.Fatal(e)
		}
		now := time.Now()
		report, e := f.app.audioIncident(context.Background(), f.row.ProjectID, now.Add(-time.Minute), now.Add(time.Minute), now)
		if e != nil {
			t.Fatal(e)
		}
		if len(report.Calls) == 1 && report.Calls[0].FirstFailed == "signaling" && report.Calls[0].ReconnectMS != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a call that handed over must appear in the incident report: %+v", report.Calls)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustPendingCleared(t *testing.T, f *handoverFixture) string {
	t.Helper()
	var active, pending string
	var expires int64
	if e := f.app.db().db.QueryRow(`SELECT token_hash,pending_token_hash,pending_expires_at FROM telephony_media_sessions WHERE call_id=?`, f.row.ID).Scan(&active, &pending, &expires); e != nil {
		t.Fatal(e)
	}
	if pending != "" || expires != 0 {
		t.Fatal("the candidate grant must not outlive its commit")
	}
	return active
}

func TestHandoverOffLeavesTodaysBehaviourUnchanged(t *testing.T) {
	f := newHandoverFixture(t, false)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	f.browser.socket.Close()
	if took := waitForDetach(t, f.app, f.row.ID, 2*time.Second); took > 1500*time.Millisecond {
		t.Fatal("a signaling drop must still end the connection promptly", took)
	}
	if n := len(handoverEvents(f.app, "softphone.browser.signaling_lost")); n != 0 {
		t.Fatal("nothing was held, so nothing should say so", n)
	}
	w := phoneTestRequest(f.app, nil, "POST", "/softphone/attach/"+f.row.ID, map[string]any{"media_diagnostics": map[string]any{}, "attachment_guard": map[string]any{"id": newAudioSessionID(), "expected_generation": f.first.SessionGeneration, "handover": true}})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "handover_unavailable") {
		t.Fatal("a server with handover off must refuse a candidate", w.Code, w.Body.String())
	}
}

func TestHandoverAttachOverHTTPIssuesACandidateOnly(t *testing.T) {
	f := newHandoverFixture(t, true)
	w := phoneTestRequest(f.app, nil, "POST", "/softphone/attach/"+f.row.ID, map[string]any{"attachment_guard": map[string]any{"id": newAudioSessionID(), "expected_generation": f.first.SessionGeneration, "handover": true}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"handover_pending":true`) {
		t.Fatal("attach should issue a candidate", w.Code, w.Body.String())
	}
	if reason := f.app.phoneMediaDenialReason(&f.row, f.first.SessionToken); reason != "" {
		t.Fatal("attach must leave the live session valid", reason)
	}
	stale := phoneTestRequest(f.app, nil, "POST", "/softphone/attach/"+f.row.ID, map[string]any{"attachment_guard": map[string]any{"id": newAudioSessionID(), "expected_generation": newAudioSessionID(), "handover": true}})
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), "media_session_changed") || !strings.Contains(stale.Body.String(), f.first.SessionGeneration) {
		t.Fatal("a stale generation must be refused and name the current one", stale.Code, stale.Body.String())
	}
}

// Regression 2: both fail. Nothing is held, because there is nothing to save.
func TestHandoverDoesNotHoldWhenAudioHasAlsoStopped(t *testing.T) {
	f := newHandoverFixture(t, true)
	f.browser.socket.Close()
	if took := waitForDetach(t, f.app, f.row.ID, 3*time.Second); took > 2*time.Second {
		t.Fatal("no audio means no hold", took)
	}
	if n := len(handoverEvents(f.app, "softphone.browser.signaling_lost")); n != 0 {
		t.Fatal("audio was not flowing, so nothing was held", n)
	}
}

// Audio stops partway through a hold, so the hold ends.
func TestHandoverHoldEndsWhenAudioStops(t *testing.T) {
	handoverTestLimits(t, 200*time.Millisecond, time.Minute, 300*time.Millisecond)
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	f.browser.socket.Close()
	stillAttached(t, f.app, f.row.ID, f.writer, 600*time.Millisecond)
	stop()
	waitForDetach(t, f.app, f.row.ID, 2*time.Second)
}

// Regression 3: a replacement is delayed or fails. The old connection keeps the
// audio until the limit, and no further.
func TestHandoverHoldHasAHardLimit(t *testing.T) {
	handoverTestLimits(t, 100*time.Millisecond, 800*time.Millisecond, time.Minute)
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	f.browser.socket.Close()
	stillAttached(t, f.app, f.row.ID, f.writer, 500*time.Millisecond)
	waitForDetach(t, f.app, f.row.ID, 2*time.Second)
}

// A close frame is the browser choosing to leave, however healthy the audio.
func TestHandoverDoesNotHoldACleanClose(t *testing.T) {
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	if e := wsutil.WriteClientMessage(f.browser.socket, 0x8, []byte{0x03, 0xe8}); e != nil {
		t.Fatal(e)
	}
	if took := waitForDetach(t, f.app, f.row.ID, 3*time.Second); took > 2*time.Second {
		t.Fatal("a chosen close must not be held", took)
	}
	if n := len(handoverEvents(f.app, "softphone.browser.signaling_lost")); n != 0 {
		t.Fatal("a chosen close is not a signaling fault", n)
	}
}

// A managed runtime revokes a browser by closing its socket from this side. That
// is no network fault, and audio has to stop at once.
func TestHandoverDoesNotHoldWhenThisServerClosedTheSocket(t *testing.T) {
	app := handoverTestCtx(t, true)
	row := insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	t.Cleanup(server.Close)
	dialWS(t, server.URL+"/peer/"+row.ID+"/cb-secret")
	first, e := app.issuePhoneSession(&row, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx, revoke := context.WithCancel(context.Background())
	var live sync.WaitGroup
	guarded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		live.Add(1)
		defer live.Done()
		app.handleSoftphoneMedia(runtimeSocketResponse{ResponseWriter: w, ctx: ctx}, r)
	}))
	t.Cleanup(func() { revoke(); guarded.Close(); live.Wait() })
	browser := rtcTestConnect(t, handoverMediaURL(guarded, row.ID, first.SessionToken))
	stop := keepMicFlowing(t, browser)
	defer stop()
	revoke()
	if took := waitForDetach(t, app, row.ID, 3*time.Second); took > 2*time.Second {
		t.Fatal("a revoked browser must not be held", took)
	}
	if n := len(handoverEvents(app, "softphone.browser.signaling_lost")); n != 0 {
		t.Fatal("closing a socket here is not a signaling fault", n)
	}
}

// Regression 4: duplicate recoveries. Only the newest candidate can win, and an
// ordinary attach fences them all.
func TestHandoverCandidatesAreFenced(t *testing.T) {
	f := newHandoverFixture(t, true)
	older := issueCandidate(t, f, f.first.SessionGeneration)
	newer := issueCandidate(t, f, f.first.SessionGeneration)
	reject := func(name string, token, query string, want string) {
		t.Helper()
		response, e := http.Get(f.server.URL + "/softphone/media/" + f.row.ID + "/" + token + query)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		if response.StatusCode != http.StatusForbidden || body["code"] != want {
			t.Fatalf("%s: got %d %v, want 403 %s", name, response.StatusCode, body, want)
		}
	}
	reject("superseded candidate", older.SessionToken, "?transport=webrtc", "media_token_replaced")
	reject("candidate over the WebSocket transport", newer.SessionToken, "", "media_token_replaced")
	// A renewal cannot make a candidate active or extend anything.
	w := phoneTestRequest(f.app, nil, "POST", "/softphone/renew/"+f.row.ID, map[string]any{"session_token": newer.SessionToken})
	if w.Code != http.StatusForbidden {
		t.Fatal("a candidate must not renew", w.Code)
	}
	// Issue an ordinary session, as a takeover or a recovery would.
	current, e := f.app.issuePhoneSession(&f.row, nil)
	if e != nil {
		t.Fatal(e)
	}
	reject("candidate after an ordinary attach", newer.SessionToken, "?transport=webrtc", "media_token_replaced")
	if committed, e := f.app.commitPendingPhoneSession(&f.row, newer.SessionToken); e != nil || committed {
		t.Fatal("a fenced candidate must not commit", committed, e)
	}
	if reason := f.app.phoneMediaDenialReason(&f.row, current.SessionToken); reason != "" {
		t.Fatal("the fence must leave the newer session valid", reason)
	}
	// A candidate cannot start from a session that is no longer current.
	if _, e := f.app.issuePendingPhoneSession(context.Background(), &f.row, nil, audioSessionCorrelation{}, &phoneAttachmentGuard{ID: newAudioSessionID(), Expected: f.first.SessionGeneration, Handover: true}); e != errPhoneSessionChanged {
		t.Fatal("stale expected generation", e)
	}
}

// Regression 5: revocation, takeover, lease expiry and fencing all still apply.
func TestHandoverCandidateFencedWhileConnectingLeavesTheOldConnectionAlone(t *testing.T) {
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	candidate := issueCandidate(t, f, f.first.SessionGeneration)
	rtcTestConnect(t, handoverMediaURL(f.server, f.row.ID, candidate.SessionToken), func() {
		// The candidate is mid-setup when its grant disappears.
		if _, e := f.app.db().db.Exec(`UPDATE telephony_media_sessions SET pending_token_hash='',pending_expires_at=0 WHERE call_id=?`, f.row.ID); e != nil {
			t.Fatal(e)
		}
	})
	deadline := time.Now().Add(8 * time.Second)
	for len(handoverEvents(f.app, "softphone.media.handover_failed")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the failed commit was not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reason := f.app.phoneMediaDenialReason(&f.row, f.first.SessionToken); reason != "" {
		t.Fatal("the old session must stay valid", reason)
	}
	stillAttached(t, f.app, f.row.ID, f.writer, 300*time.Millisecond)
}

func TestHandoverHoldDoesNotOutliveTheLease(t *testing.T) {
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	f.browser.socket.Close()
	stillAttached(t, f.app, f.row.ID, f.writer, 800*time.Millisecond)
	if _, e := f.app.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=1 WHERE call_id=?`, f.row.ID); e != nil {
		t.Fatal(e)
	}
	if took := waitForDetach(t, f.app, f.row.ID, 4*time.Second); took > 3*time.Second {
		t.Fatal("the media watchdog must end a hold when the lease ends", took)
	}
}

func TestHandoverHoldEndsAtTakeover(t *testing.T) {
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	f.browser.socket.Close()
	stillAttached(t, f.app, f.row.ID, f.writer, 800*time.Millisecond)
	if _, e := f.app.issuePhoneSession(&f.row, nil); e != nil {
		t.Fatal(e)
	}
	if took := waitForDetach(t, f.app, f.row.ID, 4*time.Second); took > 3*time.Second {
		t.Fatal("a takeover must end a hold", took)
	}
}

func TestHandoverHoldEndsWhenTheCallEnds(t *testing.T) {
	f := newHandoverFixture(t, true)
	stop := keepMicFlowing(t, f.browser)
	defer stop()
	f.browser.socket.Close()
	stillAttached(t, f.app, f.row.ID, f.writer, 800*time.Millisecond)
	if _, e := f.app.db().db.Exec(`DELETE FROM telephony_media_sessions WHERE call_id=?`, f.row.ID); e != nil {
		t.Fatal(e)
	}
	if took := waitForDetach(t, f.app, f.row.ID, 4*time.Second); took > 3*time.Second {
		t.Fatal("ending the grant must end a hold", took)
	}
}

func TestHandoverCandidateIsDeniedForARevokedUser(t *testing.T) {
	app := handoverTestCtx(t, true)
	policy := phoneTestPolicy(t, app)
	row := insertSoftphoneCall(t, app, "in-progress")
	alice := phoneTestIdentity("alice")
	principal, e := app.phonePrincipal("project-a", alice)
	if e != nil {
		t.Fatal(e)
	}
	if e := app.setPhoneOwner(&row, principal, "sales"); e != nil {
		t.Fatal(e)
	}
	first, e := app.issuePhoneSession(&row, principal)
	if e != nil {
		t.Fatal(e)
	}
	// Another adviser cannot be issued a candidate for this session.
	bob := phoneTestIdentity("bob")
	if other, e := app.phonePrincipal("project-a", bob); e == nil {
		if _, e := app.issuePendingPhoneSession(context.Background(), &row, other, audioSessionCorrelation{}, &phoneAttachmentGuard{ID: newAudioSessionID(), Expected: first.SessionGeneration, Handover: true}); e == nil {
			t.Fatal("a candidate grant crossed advisers")
		}
	}
	candidate, e := app.issuePendingPhoneSession(context.Background(), &row, principal, audioSessionCorrelation{}, &phoneAttachmentGuard{ID: newAudioSessionID(), Expected: first.SessionGeneration, Handover: true})
	if e != nil {
		t.Fatal(e)
	}
	for i := range policy.Users {
		if policy.Users[i].Identity == alice {
			policy.Users[i].Enabled = false
		}
	}
	if w := phoneTestRequest(app, nil, "PUT", "/access/policy", policy); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	reason, _, _, _, pending := app.phoneMediaPendingCheck(&row, candidate.SessionToken)
	if !pending || reason == "" {
		t.Fatal("a revoked user's candidate must be denied", reason, pending)
	}
	if _, _, e := app.commitHandover(&row, candidate.SessionToken, audioNetworkEvent{CallID: row.ID, ProjectID: row.ProjectID, Retention: time.Hour}); e == nil {
		t.Fatal("a revoked user's candidate must not commit")
	}
}

func TestHandoverExpiredCandidateIsRefused(t *testing.T) {
	f := newHandoverFixture(t, true)
	candidate := issueCandidate(t, f, f.first.SessionGeneration)
	if _, e := f.app.db().db.Exec(`UPDATE telephony_media_sessions SET pending_expires_at=1 WHERE call_id=?`, f.row.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, _, _, pending := f.app.phoneMediaPendingCheck(&f.row, candidate.SessionToken); pending {
		t.Fatal("an expired candidate is not a candidate")
	}
	if committed, e := f.app.commitPendingPhoneSession(&f.row, candidate.SessionToken); e != nil || committed {
		t.Fatal("an expired candidate must not commit", committed, e)
	}
}

// Regression 7: a slow credential service or platform call on the candidate. The
// old connection's audio and the call's claim lock are not waiting for it.
func TestHandoverSlowCandidateSetupBlocksNothing(t *testing.T) {
	app, _, platform := turnTestFixture(t)
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform), tk.WithConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_handover": "true"}))
	t.Cleanup(func() { globalCtx = previous })
	row := insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	defer server.Close()
	dialWS(t, server.URL+"/peer/"+row.ID+"/cb-secret")
	first, e := app.issuePhoneSession(&row, nil)
	if e != nil {
		t.Fatal(e)
	}
	saveTURNTestPolicy(t, app, globalCtx, nil, phoneTURNPolicy{Source: "app", Transport: "udp", ICEPolicy: "all"})
	browser := rtcTestConnect(t, handoverMediaURL(server, row.ID, first.SessionToken))
	stop := keepMicFlowing(t, browser)
	defer stop()
	writer := app.softphones.lookup(row.ID).browserWriter()
	f := &handoverFixture{app: app, row: row}
	candidate := issueCandidate(t, f, first.SessionGeneration)
	// The candidate's credential request never returns while this test runs.
	platform.wait = true
	socket := dialWS(t, handoverMediaURL(server, row.ID, candidate.SessionToken))
	defer socket.Close()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	unlock := app.softphones.lockClaim(row.ID)
	unlock()
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Fatal("a slow candidate must not hold the call's claim lock", took)
	}
	if reason := app.phoneMediaDenialReason(&row, first.SessionToken); reason != "" {
		t.Fatal("the old session must stay valid", reason)
	}
	stillAttached(t, app, row.ID, writer, 300*time.Millisecond)
}

func TestHoldForHandoverReturnsAtOnceWithoutAConnectedPath(t *testing.T) {
	pc, e := webrtc.NewPeerConnection(webrtc.Configuration{})
	if e != nil {
		t.Fatal(e)
	}
	defer pc.Close()
	called := false
	start := time.Now()
	holdRTCForHandover(pc, &rtcMediaStats{}, make(chan struct{}), func() { called = true })
	if called || time.Since(start) > 100*time.Millisecond {
		t.Fatal("no connected path means nothing to hold")
	}
}

func TestDiscardedWriterNeverTouchesItsSocket(t *testing.T) {
	writer := newWebSocketWriterPump(nil, 0)
	defer writer.Stop()
	writer.discard.Store(true)
	if e := writer.Write(1, []byte("x")); e != nil {
		t.Fatal(e)
	}
	if !writer.queueControl([]byte("x")) {
		t.Fatal("a discarded control frame still succeeds")
	}
}

func TestIncidentCountsAHeldSignalingFault(t *testing.T) {
	at := time.Date(2026, 11, 4, 15, 2, 0, 0, time.UTC)
	calls := buildAudioIncidentCalls([]audioNetworkEvent{
		{CallID: "call-1", Event: "softphone.browser.signaling_lost", OccurredAt: at.Format(time.RFC3339Nano), Disconnect: &audioDisconnectInfo{LastAudioReadAt: at.Add(-10 * time.Millisecond).Format(time.RFC3339Nano), LastReadAt: at.Add(-time.Second).Format(time.RFC3339Nano)}},
		{CallID: "call-1", Event: "softphone.browser.connected", OccurredAt: at.Add(1500 * time.Millisecond).Format(time.RFC3339Nano)},
		{CallID: "call-1", Event: "softphone.browser.disconnected", Reason: "session_replaced", OccurredAt: at.Add(1600 * time.Millisecond).Format(time.RFC3339Nano)},
	}, at.Add(-time.Minute), at.Add(time.Minute))
	if len(calls) != 1 || calls[0].FirstFailed != "signaling" || calls[0].ReconnectMS == nil || *calls[0].ReconnectMS != 1500 || calls[0].Disconnects != 1 {
		t.Fatalf("%+v", calls)
	}
}
