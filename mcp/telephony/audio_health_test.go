package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestAudioPeerHashTrustAndScope(t *testing.T) {
	a, b := audioPeerHasher{}, audioPeerHasher{}
	request := httptest.NewRequest("GET", "http://local", nil)
	request.RemoteAddr = "192.0.2.4:53210"
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	direct, e, source := a.hash(request, "")
	if source != "socket_peer" || len(direct) != 32 || strings.Contains(direct, "192.0.2") {
		t.Fatal(direct, e, source)
	}
	request.RemoteAddr = "192.0.2.4:53211"
	same, e2, _ := a.hash(request, "")
	if same != direct || e2 != e {
		t.Fatal("port change split the same peer")
	}
	other, e3, _ := b.hash(request, "")
	if other == direct || e3 == e {
		t.Fatal("hash must be process scoped")
	}
	forwarded, _, source := a.hash(request, "192.0.2.0/24")
	if forwarded == direct || source != "trusted_forwarded_peer" {
		t.Fatal("trusted forwarding not resolved")
	}
	request.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.7")
	value, _, _ := a.hash(request, "192.0.2.0/24")
	if value != forwarded {
		t.Fatal("accepted spoofed prefix past untrusted hop")
	}
	request.Header.Set("X-Forwarded-For", "invalid, 198.51.100.7")
	value, _, source = a.hash(request, "192.0.2.0/24")
	if value != direct || source != "socket_peer" {
		t.Fatal("malformed chain did not fall back")
	}
}
func TestAudioHealthUsesRecentObservationsAndRecovers(t *testing.T) {
	var tracker audioCallTelemetry
	now := time.Unix(1000, 0)
	sample := func(stage string, n float64, active, bad bool, at time.Time) []audioHealthEvent {
		return tracker.observeHealth(at, []audioHealthObservation{{stage, "over_budget", bad, active, n}})
	}
	// Lifetime counters are a baseline, not evidence of a fresh incident.
	if len(sample("telephony_to_browser", 500, true, false, now)) != 0 {
		t.Fatal("historical counters triggered incident")
	}
	changes := sample("telephony_to_browser", 520, true, false, now.Add(time.Second))
	if len(changes) != 1 || changes[0].Reason != "audio_degraded" {
		t.Fatal(changes)
	}
	_, health := tracker.snapshots()
	if health.State != "audio_degraded" {
		t.Fatal(health)
	}
	if len(sample("telephony_to_browser", 520, true, false, now.Add(9*time.Second))) != 0 {
		t.Fatal("premature recovery")
	}
	changes = sample("telephony_to_browser", 520, true, false, now.Add(11*time.Second))
	if len(changes) != 1 || changes[0].State != "healthy" {
		t.Fatal(changes)
	}
	// Mute/hold/suspension baselines counters without creating incidents.
	sample("browser_to_telephony", 0, false, false, now)
	sample("browser_to_telephony", 300, false, false, now.Add(time.Second))
	if len(sample("browser_to_telephony", 300, true, false, now.Add(2*time.Second))) != 1 {
		t.Fatal("expected activation only")
	}
	_, health = tracker.snapshots()
	if health.Stages["browser_to_telephony"].State != "healthy" {
		t.Fatal(health)
	}
	sample("carrier_to_telephony", 0, true, true, now)
	_, health = tracker.snapshots()
	if health.Stages["carrier_to_telephony"].State != "audio_degraded" {
		t.Fatal("current stall ignored")
	}
	for i := 0; i < 200; i++ {
		sample("carrier_to_telephony", float64(i), true, i%2 == 0, now.Add(time.Duration(i)*20*time.Second))
	}
	_, health = tracker.snapshots()
	if len(health.Events) > 64 {
		t.Fatal("unbounded events")
	}
}
func TestAudioCorrelationDistinctCallsScopesRecoveryCooldown(t *testing.T) {
	var c audioAlertCorrelator
	now := time.Unix(1000, 0)
	for i := 0; i < 100; i++ {
		if c.observe("p", "telnyx", "carrier_to_telephony", "one", true, now, 3, time.Minute) != nil {
			t.Fatal("one call became a multi-call alert")
		}
	}
	if c.observe("other", "telnyx", "carrier_to_telephony", "two", true, now, 3, time.Minute) != nil {
		t.Fatal("project leak")
	}
	c.observe("p", "twilio", "carrier_to_telephony", "two", true, now, 3, time.Minute)
	c.observe("p", "telnyx", "browser_to_telephony", "two", true, now, 3, time.Minute)
	c.observe("p", "telnyx", "carrier_to_telephony", "two", true, now, 3, time.Minute)
	alert := c.observe("p", "telnyx", "carrier_to_telephony", "three", true, now, 3, time.Minute)
	if alert == nil || alert.CallCount != 3 || alert.State != "audio_degraded" {
		t.Fatal(alert)
	}
	if c.observe("p", "telnyx", "carrier_to_telephony", "four", true, now, 3, time.Minute) != nil {
		t.Fatal("duplicate alert")
	}
	recovered := c.expire(now.Add(30 * time.Second))
	if len(recovered) != 1 || recovered[0].State != "recovered" {
		t.Fatal(recovered)
	}
	for _, id := range []string{"a", "b", "c"} {
		if c.observe("p", "telnyx", "carrier_to_telephony", id, true, now.Add(31*time.Second), 3, time.Minute) != nil {
			t.Fatal("cooldown bypassed")
		}
	}
	c.observe("p", "telnyx", "carrier_to_telephony", "a", true, now.Add(61*time.Second), 3, time.Minute)
	c.observe("p", "telnyx", "carrier_to_telephony", "b", true, now.Add(61*time.Second), 3, time.Minute)
	if c.observe("p", "telnyx", "carrier_to_telephony", "c", true, now.Add(61*time.Second), 3, time.Minute) == nil {
		t.Fatal("next episode suppressed")
	}
	if c.observe("p", "telnyx", "carrier_to_telephony", "c", true, now, 0, time.Minute) != nil {
		t.Fatal("disabled alerts")
	}
}
func TestAudioSocketHistoryAndBrowserTotalsSurviveRecovery(t *testing.T) {
	var t1 audioCallTelemetry
	t1.restore("")
	first, second := &websocketWriterPump{}, &websocketWriterPump{}
	t1.opened(first, "hash", "epoch", "socket_peer")
	browser := browserAudioDiagnostics{ClientEpoch: "worker-a", Timing: &browserAudioTiming{}}
	browser.Timing.Transport.ReconnectAttempts = 2
	browser.Timing.Transport.ReconnectSuccesses = 1
	t1.observeBrowser(browser)
	t1.observeBrowser(browser)
	t1.closed(first, "", wsutil.ClosedError{Code: ws.StatusGoingAway, Reason: "client closed"})
	t1.closed(first, "", nil) // Duplicate cleanup cannot double-count.
	t1.opened(second, "hash", "epoch", "socket_peer")
	t1.observeBrowser(browser)
	socket, health := t1.snapshots()
	if socket.Connections != 2 || socket.Reconnects != 1 || socket.Disconnects != 1 || socket.BrowserTotals["reconnect_attempts"] != 2 || socket.Events[1].Code != 1001 {
		t.Fatal(socket)
	}
	raw, _ := json.Marshal(browserAudioDiagnostics{ClientEpoch: browser.ClientEpoch, Timing: browser.Timing, Server: &serverAudioDiagnostics{Socket: socket, Health: health}})
	var t2 audioCallTelemetry
	t2.restore(string(raw))
	t2.opened(&websocketWriterPump{}, "newhash", "newepoch", "socket_peer")
	t2.observeBrowser(browser)
	socket, _ = t2.snapshots()
	if socket.Connections != 3 || socket.Reconnects != 2 || socket.BrowserTotals["reconnect_attempts"] != 2 {
		t.Fatal("restored counters double counted", socket)
	}
	browser.ClientEpoch = "worker-b"
	browser.Timing.Transport.ReconnectAttempts = 1
	t2.observeBrowser(browser)
	socket, _ = t2.snapshots()
	if socket.BrowserTotals["reconnect_attempts"] != 3 {
		t.Fatal("new runtime not accumulated", socket)
	}
	// Snapshots are detached from live history.
	socket.Events[0].Reason = "mutated"
	s, _ := t2.snapshots()
	if s.Events[0].Reason == "mutated" {
		t.Fatal("snapshot aliases live history")
	}
}

func TestBrowserReattachKeepsCarrierLegAndPersistsConnectionHistory(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	session, err := app.issuePhoneSession(&row, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/"+row.ID+"/"+row.CallbackSecret)
	defer peer.Close()
	browser := dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	hub := app.softphones.hubFor(row.ID)
	carrier := hub.peerWriter()
	if carrier == nil {
		t.Fatal("carrier not attached")
	}
	_ = browser.Close()
	deadline := time.Now().Add(3 * time.Second)
	for hub.browserWriter() != nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.browserWriter() != nil || hub.peerWriter() != carrier {
		t.Fatal("browser drop disturbed carrier")
	}
	current, err := app.db().findCall(row.ID)
	if err != nil || isTerminalStatus(current.Status) || current.MediaStatus != "connected" {
		t.Fatal(current, err)
	}
	reply := phoneTestRequest(app, nil, "POST", "/softphone/attach/"+row.ID, nil)
	if reply.Code != http.StatusOK {
		t.Fatalf("fresh attach %d %s", reply.Code, reply.Body)
	}
	var fresh softphoneSession
	if json.Unmarshal(reply.Body.Bytes(), &fresh) != nil || fresh.SessionToken == session.SessionToken {
		t.Fatal("fresh authorization missing")
	}
	recovered := dialWS(t, server.URL+strings.TrimPrefix(fresh.MediaURL, "/api/apps/telephony/_install/42"))
	defer recovered.Close()
	readSoftphoneEventWithin(t, recovered, "ready", 3*time.Second)
	pcm := bytes.Repeat([]byte{0x34, 0x12}, 480)
	if err := wsutil.WriteClientBinary(peer, pcm); err != nil {
		t.Fatal(err)
	}
	_ = recovered.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		data, op, err := wsutil.ReadServerData(recovered)
		if err != nil {
			t.Fatal(err)
		}
		if op == ws.OpBinary {
			if !bytes.Equal(data, pcm) {
				t.Fatal("audio changed after reattach")
			}
			break
		}
	}
	if hub.peerWriter() != carrier {
		t.Fatal("carrier socket was replaced")
	}
	// Degradation changes only diagnostics, never call status or carrier ownership.
	now := time.Now()
	hub.telemetry.observeHealth(now, []audioHealthObservation{{"telephony_to_browser", "test_latency", true, true, 0}})
	if err := app.db().updateServerAudioDiagnostics(row.ID, hub.serverAudioSnapshot()); err != nil {
		t.Fatal(err)
	}
	current, err = app.db().findCall(row.ID)
	if err != nil || isTerminalStatus(current.Status) || current.MediaStatus != "connected" {
		t.Fatal("diagnostics changed lifecycle", current, err)
	}
	var diag browserAudioDiagnostics
	if json.Unmarshal([]byte(current.BrowserAudioDiagnostics), &diag) != nil || diag.Server == nil {
		t.Fatal("diagnostics not retained")
	}
	if diag.Server.Socket.Reconnects != 1 || diag.Server.Socket.Disconnects != 1 || diag.Server.Health.Reason != "audio_degraded" {
		t.Fatal(diag.Server)
	}
}

func TestAudioGapCountersExcludeHoldAndNoncontinuousCarrierSilence(t *testing.T) {
	var reception carrierReception
	reception.begin(true)
	reception.observe(carrierSource{}, 20, 1000)
	reception.observe(carrierSource{}, 20, 1500)
	if reception.snapshot(1500, true).GapsOverBudget != 1 {
		t.Fatal("500ms interruption not counted")
	}
	reception.pause(true)
	reception.observe(carrierSource{}, 20, 3000)
	if reception.snapshot(3000, true).GapsOverBudget != 1 {
		t.Fatal("hold counted as failure")
	}
	reception.pause(false)
	reception.begin(false)
	reception.observe(carrierSource{}, 20, 4000)
	reception.observe(carrierSource{}, 20, 10000)
	if reception.snapshot(10000, true).GapsOverBudget != 1 {
		t.Fatal("omitted silence counted as failure")
	}
}

func TestAudioDiagnosticsWriteFailureCannotBlockMicrophoneFrames(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	session, err := app.issuePhoneSession(&row, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/"+row.ID+"/"+row.CallbackSecret)
	defer peer.Close()
	browser := dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	if _, err = app.db().db.Exec(`ALTER TABLE calls RENAME TO diagnostics_unavailable_calls`); err != nil {
		t.Fatal(err)
	}
	defer app.db().db.Exec(`ALTER TABLE diagnostics_unavailable_calls RENAME TO calls`)
	if err = wsutil.WriteClientText(browser, []byte(`{"type":"diagnostics","diagnostics":{"rtt_ms":42,"connection_state":"connected","audio_context_state":"running"}}`)); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual failing persistence tick, not just an in-memory helper.
	time.Sleep(1200 * time.Millisecond)
	pcm := bytes.Repeat([]byte{0x28, 0x08}, 480)
	if err = wsutil.WriteClientBinary(browser, pcm); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	for {
		data, op, err := wsutil.ReadServerData(peer)
		if err != nil {
			t.Fatal("diagnostics outage blocked media", err)
		}
		if op == ws.OpBinary {
			if !bytes.Equal(data, pcm) {
				t.Fatal("microphone PCM changed")
			}
			break
		}
	}
	if !app.softphones.hubFor(row.ID).telemetry.pending() {
		t.Fatal("failed diagnostics write not queued for retry")
	}
}
