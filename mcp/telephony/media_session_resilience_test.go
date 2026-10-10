package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMediaPolicyOutagePreservesOnlyVerifiedLiveLeases(t *testing.T) {
	app, row, tap, _ := listenerFixture(t)
	alice, boss := phoneTestIdentity("alice"), phoneTestIdentity("boss")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	operator, err := app.issuePhoneSession(&row, p)
	if err != nil {
		t.Fatal(err)
	}
	listener := listenerIssue(t, app, row.ID, &boss)
	server := softphoneTestServer(t, app)
	browser := dialWS(t, server.URL+strings.TrimPrefix(operator.MediaURL, "/api/apps/telephony/_install/42"))
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	ls := listenerServer(t, app)
	observer := listenerDial(t, ls, listener)
	defer observer.Close()
	_ = observer.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, _, readErr := wsutil.ReadServerData(observer)
	if readErr != nil || !strings.Contains(string(data), "listener.ready") {
		t.Fatalf("listener ready: %s %v", data, readErr)
	}
	if _, err = app.db().db.Exec(`ALTER TABLE telephony_access_policies RENAME TO unavailable_policy`); err != nil {
		t.Fatal(err)
	}
	defer app.db().db.Exec(`ALTER TABLE unavailable_policy RENAME TO telephony_access_policies`)
	for _, tc := range []struct {
		action string
		token  string
		who    *phoneIdentity
	}{
		{"renew", operator.SessionToken, &alice}, {"listen-renew", listener.SessionToken, &boss},
	} {
		w := phoneTestRequest(app, tc.who, "POST", "/softphone/"+tc.action+"/"+row.ID, map[string]any{"session_token": tc.token})
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("temporary policy lookup returned %d: %s", w.Code, w.Body)
		}
	}
	if reason := app.phoneMediaDenialReason(&row, operator.SessionToken); reason != "policy_lookup_failed" {
		t.Fatal(reason)
	}
	if reason, _ := app.listenerSessionCheck(&row, listener.SessionToken); reason != "policy_lookup_failed" {
		t.Fatal(reason)
	}
	time.Sleep(1200 * time.Millisecond)
	if h := app.softphones.lookup(row.ID); h == nil || h.browserWriter() == nil {
		t.Fatal("policy outage disconnected authorized adviser")
	}
	tap.mu.Lock()
	l := tap.listeners[phoneHash(listener.SessionToken)]
	tap.mu.Unlock()
	if l == nil {
		t.Fatal("policy outage disconnected authorized listener")
	}
	select {
	case <-l.done:
		t.Fatal("policy outage revoked listener")
	default:
	}
	// A new connection still fails closed while policy is unavailable.
	w := phoneTestRequest(app, nil, "GET", strings.TrimPrefix(operator.MediaURL, "/api/apps/telephony/_install/42"), nil)
	if w.Code != 503 {
		t.Fatalf("new media bypassed policy: %d", w.Code)
	}
	if _, err = app.db().db.Exec(`ALTER TABLE unavailable_policy RENAME TO telephony_access_policies`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		action string
		token  string
		who    *phoneIdentity
	}{
		{"renew", operator.SessionToken, &alice}, {"listen-renew", listener.SessionToken, &boss},
	} {
		w := phoneTestRequest(app, tc.who, "POST", "/softphone/"+tc.action+"/"+row.ID, map[string]any{"session_token": tc.token})
		if w.Code != 200 {
			t.Fatalf("recovered renewal %d %s", w.Code, w.Body)
		}
	}
	// Actual expiry stays distinct and must not be renewed.
	if _, err = app.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=0 WHERE call_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	w = phoneTestRequest(app, &alice, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": operator.SessionToken})
	var response map[string]any
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != 403 || response["code"] != "media_lease_expired" {
		t.Fatalf("expiry classification: %d %s", w.Code, w.Body)
	}
}

func TestRenewalSessionLookupFailureIsNotRevocation(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "lease-lookup", "in-progress")
	session, err := app.issuePhoneSession(&row, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.db().db.Exec(`ALTER TABLE telephony_media_sessions RENAME TO unavailable_sessions`); err != nil {
		t.Fatal(err)
	}
	defer app.db().db.Exec(`ALTER TABLE unavailable_sessions RENAME TO telephony_media_sessions`)
	w := phoneTestRequest(app, nil, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": session.SessionToken})
	if w.Code != 503 || !strings.Contains(w.Body.String(), "media_session_lookup_failed") {
		t.Fatalf("lookup failure classified as denial: %d %s", w.Code, w.Body)
	}
}

func TestMediaSessionDiagnosticsAreRetainedAndBounded(t *testing.T) {
	var d browserAudioDiagnostics
	if err := json.Unmarshal([]byte(`{"session_events":[{"timestamp":"2026-10-05T12:00:00Z","action":"renew","outcome":"retrying","status":503,"remaining_ms":39000},{"action":"websocket","outcome":"closed","code":"1006","detail":"network interrupted"}],"timing":{"transport":{"playback_ingress_ms":40,"playback_received_ms":20,"playback_transport_dropped_ms":20,"drop_totals_ms":{"capture_clock_unavailable":20}}}}`), &d); err != nil {
		t.Fatal(err)
	}
	d = normalizeBrowserAudioDiagnostics(d)
	if len(d.SessionEvents) != 2 || d.SessionEvents[0].Status != 503 || d.SessionEvents[1].Code != "1006" || d.Timing.Transport.PlaybackIngressMS != 40 || d.Timing.Transport.DropTotalsMS["capture_clock_unavailable"] != 20 {
		t.Fatalf("lost diagnostics: %+v", d)
	}
	for i := 0; i < 100; i++ {
		d.SessionEvents = append(d.SessionEvents, mediaSessionEvent{Detail: strings.Repeat("x", 1000), RemainingMS: 99999999})
	}
	d = normalizeBrowserAudioDiagnostics(d)
	if len(d.SessionEvents) != 50 || d.SessionEvents[0].Detail != "detail_redacted" || d.SessionEvents[0].RemainingMS != 3600000 {
		t.Fatal("unbounded diagnostics")
	}
}

func TestRejectedCaptureMeasuresAgeBeforeFiltering(t *testing.T) {
	h := &softphoneHub{}
	frame := func(sent float64, sequence uint32) []byte {
		data := make([]byte, 32+960)
		binary.LittleEndian.PutUint32(data, softphoneAudioFrameV2)
		binary.LittleEndian.PutUint32(data[4:], sequence)
		binary.LittleEndian.PutUint64(data[16:], math.Float64bits(sent))
		return data
	}
	if h.observeCaptureTiming(frame(10000, 1)) {
		t.Fatal("first valid clock frame dropped")
	}
	if !h.observeCaptureTiming(frame(9000, 2)) {
		t.Fatal("one-second-old capture accepted")
	}
	s := h.serverAudioSnapshot()
	if s.CaptureTransitExcessMS < 1000 || s.CaptureStaleBytes != 960 || len(s.CaptureDropEvents) != 1 {
		t.Fatalf("missing rejected frame diagnostics: %+v", s)
	}
	e := s.CaptureDropEvents[0]
	if e.Reason != "capture_transit_age" || e.DurationMS != 20 || e.Sequence != 2 || e.QueueBeforeMS < 1000 || e.Timestamp == "" {
		t.Fatalf("bad drop event: %+v", e)
	}
	h.mu.Lock()
	h.captureTransitSet = false
	h.mu.Unlock()
	if h.observeCaptureTiming(frame(9000, 3)) {
		t.Fatal("new socket inherited old transit baseline")
	}
}

func TestFreshMediaSessionWriteFailureIsTemporary(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "grant-write", "in-progress")
	if _, err := app.db().db.Exec(`CREATE TRIGGER fail_media_grant BEFORE INSERT ON telephony_media_sessions BEGIN SELECT RAISE(FAIL,'fixture write outage'); END;`); err != nil {
		t.Fatal(err)
	}
	w := phoneTestRequest(app, nil, "POST", "/softphone/attach/"+row.ID, nil)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "media_session_unavailable") {
		t.Fatalf("temporary write classified as revocation: %d %s", w.Code, w.Body)
	}
	if _, err := app.db().db.Exec(`DROP TRIGGER fail_media_grant`); err != nil {
		t.Fatal(err)
	}
	w = phoneTestRequest(app, nil, "POST", "/softphone/attach/"+row.ID, nil)
	if w.Code != 200 {
		t.Fatalf("grant did not recover: %d %s", w.Code, w.Body)
	}
}

func TestPolicyOutageCannotExtendPreviouslyVerifiedLease(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	session, err := app.issuePhoneSession(&row, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=? WHERE call_id=?`, time.Now().Unix()+3, row.ID); err != nil {
		t.Fatal(err)
	}
	server := softphoneTestServer(t, app)
	browser := dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", time.Second)
	if _, err = app.db().db.Exec(`ALTER TABLE telephony_access_policies RENAME TO unavailable_policy`); err != nil {
		t.Fatal(err)
	}
	defer app.db().db.Exec(`ALTER TABLE unavailable_policy RENAME TO telephony_access_policies`)
	_ = browser.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err = wsutil.ReadServerData(browser)
		if err != nil {
			if !strings.Contains(err.Error(), "media_lease_expired") {
				t.Fatalf("expected expiry close, got %v", err)
			}
			break
		}
	}
}

func TestCoachingPolicyLookupOutageStopsTalkWithoutRevokingListening(t *testing.T) {
	app, row, h, tap, _, _, _ := coachingFixture(t)
	session := issueCoach(t, app, row)
	server := listenerServer(t, app)
	c := listenerDial(t, server, session)
	defer c.Close()
	coachCommand(t, c, "coach.start", 1)
	coachEvent(t, c, "coach.started")
	if _, err := app.db().db.Exec(`ALTER TABLE telephony_access_policies RENAME TO unavailable_policy`); err != nil {
		t.Fatal(err)
	}
	defer app.db().db.Exec(`ALTER TABLE unavailable_policy RENAME TO telephony_access_policies`)
	coachCommand(t, c, "coach.keepalive", 1)
	stopped := coachEvent(t, c, "coach.stopped")
	if stopped["reason"] != "policy_unavailable" {
		t.Fatalf("uncertain permission was not explicit: %+v", stopped)
	}
	tap.mu.Lock()
	l := tap.listeners[phoneHash(session.SessionToken)]
	tap.mu.Unlock()
	if l == nil || l.coaching.talking.Load() {
		t.Fatal("talk retained during uncertainty or listening closed")
	}
	select {
	case <-l.done:
		t.Fatal("temporary lookup revoked listening")
	default:
	}
	if h.browserWriter() == nil {
		t.Fatal("lookup outage affected adviser audio")
	}
	if _, err := app.db().db.Exec(`ALTER TABLE unavailable_policy RENAME TO telephony_access_policies`); err != nil {
		t.Fatal(err)
	}
	coachCommand(t, c, "coach.start", 2)
	coachEvent(t, c, "coach.started")
}

func TestMediaOwnershipLookupFailureIsTemporary(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal("project-a", alice)
	if err != nil {
		t.Fatal(err)
	}
	row := phoneTestCall(t, app, "owner-lookup", "in-progress")
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	session, err := app.issuePhoneSession(&row, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.db().db.Exec(`ALTER TABLE telephony_call_owners RENAME TO unavailable_owners`); err != nil {
		t.Fatal(err)
	}
	defer app.db().db.Exec(`ALTER TABLE unavailable_owners RENAME TO telephony_call_owners`)
	w := phoneTestRequest(app, &alice, "POST", "/softphone/renew/"+row.ID, map[string]any{"session_token": session.SessionToken})
	if w.Code != 503 || !strings.Contains(w.Body.String(), "owner_lookup_failed") {
		t.Fatalf("owner lookup failure misclassified: %d %s", w.Code, w.Body)
	}
}

func TestEstablishedLeaseFailureCannotExtendExpiry(t *testing.T) {
	for _, reason := range []string{"media_session_lookup_failed", "policy_lookup_failed", "owner_lookup_failed"} {
		if got := establishedMediaLeaseReason(reason, 101, 100); got != reason {
			t.Fatalf("valid lease lost: %s", got)
		}
		if got := establishedMediaLeaseReason(reason, 100, 100); got != "media_lease_expired" {
			t.Fatalf("expired lease survived: %s", got)
		}
	}
	if got := establishedMediaLeaseReason("media_token_replaced", 101, 100); got != "media_token_replaced" {
		t.Fatal("revocation hidden")
	}
}
func TestLiveSessionLookupOutageRecordsDeferredRecoveryAndExpiry(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "expiry"}[expire], func(t *testing.T) {
			softphoneTestCtx(t)
			app := &App{installID: 42}
			phoneTestPolicy(t, app)
			row := phoneTestCall(t, app, "session-outage", "in-progress")
			session, err := app.issuePhoneSession(&row, nil)
			if err != nil {
				t.Fatal(err)
			}
			if expire {
				if _, err = app.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=? WHERE call_id=?`, time.Now().Unix()+3, row.ID); err != nil {
					t.Fatal(err)
				}
			}
			server := softphoneTestServer(t, app)
			browser := dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
			defer browser.Close()
			readSoftphoneEventWithin(t, browser, "ready", time.Second)
			if _, err = app.db().db.Exec(`ALTER TABLE telephony_media_sessions RENAME TO unavailable_sessions`); err != nil {
				t.Fatal(err)
			}
			restored := false
			defer func() {
				if !restored {
					app.db().db.Exec(`ALTER TABLE unavailable_sessions RENAME TO telephony_media_sessions`)
				}
			}()
			hub := app.softphones.lookup(row.ID)
			until := time.Now().Add(2 * time.Second)
			found := false
			for !found && time.Now().Before(until) {
				socket, _ := hub.telemetry.snapshots()
				for _, e := range socket.LeaseEvents {
					if e.Action == "lease_check_deferred" && e.Reason == "media_session_lookup_failed" && e.ConnectionID != "" && e.At != "" && e.RemainingLeaseMS > 0 {
						found = true
					}
				}
				if !found {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if !found || hub.browserWriter() == nil {
				t.Fatal("lookup outage lost live lease or evidence")
			}
			// Actual media continues during the lookup outage, on the existing socket.
			pcm := make([]byte, 960)
			for i := range pcm {
				pcm[i] = byte(i)
			}
			hub.toBrowser(ws.OpBinary, pcm)
			_ = browser.SetReadDeadline(time.Now().Add(time.Second))
			for {
				data, op, e := wsutil.ReadServerData(browser)
				if e != nil {
					t.Fatal(e)
				}
				if op == ws.OpBinary {
					if !bytes.Equal(data, pcm) {
						t.Fatal("audio changed during lookup outage")
					}
					break
				}
			}
			if expire {
				_ = browser.SetReadDeadline(time.Now().Add(4 * time.Second))
				for {
					_, _, e := wsutil.ReadServerData(browser)
					if e != nil {
						if !strings.Contains(e.Error(), "media_lease_expired") {
							t.Fatalf("wrong expiry: %v", e)
						}
						break
					}
				}
			} else {
				if _, err = app.db().db.Exec(`ALTER TABLE unavailable_sessions RENAME TO telephony_media_sessions`); err != nil {
					t.Fatal(err)
				}
				restored = true
				until = time.Now().Add(2 * time.Second)
				found = false
				for !found && time.Now().Before(until) {
					socket, _ := hub.telemetry.snapshots()
					for _, e := range socket.LeaseEvents {
						if e.Action == "lease_check_recovered" {
							found = true
						}
					}
					if !found {
						time.Sleep(10 * time.Millisecond)
					}
				}
				if !found || hub.browserWriter() == nil {
					t.Fatal("session did not recover")
				}
			}
			current, e := app.db().findCall(row.ID)
			if e != nil || current.Status != row.Status {
				t.Fatal("lease handling changed carrier call")
			}
		})
	}
}

func TestLeaseEvidencePreservesNetworkConnectionAttribution(t *testing.T) {
	var tracker audioCallTelemetry
	writer := idleAudioWriter()
	identity := phoneTestIdentity("alice")
	id := tracker.openedWithNetwork(writer, "hash", "epoch", "socket_peer", audioNetworkEvent{AdviserIdentity: identity, IdentitySource: "verified_session"}, nil)
	for i := 0; i < 80; i++ {
		tracker.leaseCheckEvent(writer, "lease_check_deferred", "policy_lookup_failed", time.Now().Unix()+60)
	}
	snapshot, _ := tracker.snapshots()
	if len(snapshot.Events) != 1 || len(snapshot.LeaseEvents) != 64 || snapshot.LeaseEvents[0].AdviserIdentity != identity {
		t.Fatal("lease evidence changed connection history or lost attribution")
	}
	tracker.mu.Lock()
	attributed := tracker.connectionAtLocked(time.Now().UTC().Format(time.RFC3339Nano))
	tracker.mu.Unlock()
	if attributed != id {
		t.Fatal("observational lease events ended the connection interval")
	}
	snapshot.LeaseEvents[0].Reason = "modified"
	fresh, _ := tracker.snapshots()
	if fresh.LeaseEvents[0].Reason != "policy_lookup_failed" {
		t.Fatal("lease snapshot aliases telemetry")
	}
}
