package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func incidentDisconnect(call, adviser string, at time.Time, audioAge, signalingAge float64, reason, intent string) audioNetworkEvent {
	ago := func(seconds float64) string {
		return at.Add(-time.Duration(seconds * float64(time.Second))).Format(time.RFC3339Nano)
	}
	return audioNetworkEvent{Event: "softphone.browser.disconnected", CallID: call, AdviserIdentity: phoneTestIdentity(adviser), OccurredAt: at.Format(time.RFC3339Nano), Reason: reason, ShutdownIntent: intent,
		Disconnect: &audioDisconnectInfo{LastAudioReadAt: ago(audioAge), LastReadAt: ago(signalingAge), ErrorClass: "eof", ICEState: "connected"}}
}
func incidentEvent(call, event string, at time.Time) audioNetworkEvent {
	return audioNetworkEvent{Event: event, CallID: call, OccurredAt: at.Format(time.RFC3339Nano)}
}

// The shape of 2026-11-04: per-connection faults, then a mix of recovery times.
func TestIncidentReportReadsTheNovemberFourthPattern(t *testing.T) {
	at := time.Date(2026, 11, 4, 15, 2, 40, 0, time.UTC)
	from, to := at.Add(-time.Minute), at.Add(time.Minute)
	recovery := &audioRecoveryRecord{RecoveryID: "recovery-1", Outcome: "connected", AttemptsTotal: 2}
	withRecovery := incidentEvent("bruno", "softphone.browser.recovery", at.Add(20*time.Second))
	withRecovery.Recovery = recovery
	events := []audioNetworkEvent{
		incidentEvent("bruno", "softphone.browser.connected", at.Add(-10*time.Minute)), // Before the fault.
		incidentDisconnect("bruno", "bruno", at, 0.02, 18.66, "transport_read_error", ""),
		incidentEvent("bruno", "softphone.browser.setup_failed", at.Add(15*time.Second)),
		incidentEvent("bruno", "softphone.browser.connected", at.Add(19*time.Second)),
		withRecovery,
		incidentDisconnect("sara", "sara", at.Add(2*time.Second), 9.95, 0.1, "peer_close", "audio_error"),
		incidentEvent("sara", "softphone.browser.connected", at.Add(32*time.Second)),
		incidentDisconnect("tabara", "tabara", at.Add(3*time.Second), 11.41, 17.41, "transport_read_error", ""),
		incidentEvent("tabara", "softphone.browser.connected", at.Add(35*time.Second)),
		incidentDisconnect("loane", "loane", at.Add(4*time.Second), 14.77, 18.19, "transport_read_error", ""),
		// Not faults.
		incidentDisconnect("hungup", "x", at, 0, 0, "peer_close", "user_stop"),
		incidentDisconnect("replaced", "x", at, 0, 0, "media_token_replaced", ""),
		incidentDisconnect("earlier", "x", at.Add(-5*time.Minute), 20, 20, "transport_read_error", ""),
	}
	calls := buildAudioIncidentCalls(events, from, to)
	if len(calls) != 4 {
		t.Fatalf("%d calls: %+v", len(calls), calls)
	}
	byCall := map[string]audioIncidentCall{}
	for _, c := range calls {
		byCall[c.CallID] = c
	}
	bruno := byCall["bruno"]
	if bruno.FirstFailed != "signaling" || bruno.Adviser != "bruno" || *bruno.ReconnectMS != 19000 || bruno.FailedAttempts != 1 || bruno.Recovery != recovery || *bruno.SignalingAgeMS != 18660 {
		t.Fatalf("%+v", bruno)
	}
	if byCall["sara"].FirstFailed != "audio" || byCall["tabara"].FirstFailed != "both" || byCall["loane"].FirstFailed != "both" {
		t.Fatalf("%+v", byCall)
	}
	if byCall["loane"].ReconnectedAt != "" || byCall["loane"].ReconnectMS != nil {
		t.Fatalf("%+v", byCall["loane"])
	}
	summary := summarizeAudioIncident(calls, 1)
	if summary.AffectedCalls != 4 || summary.Reconnected != 3 || summary.NotReconnected != 1 || summary.HealthyCalls != 1 || summary.FirstFailed["both"] != 2 || summary.MedianMS != 30000 || summary.LongestMS != 32000 {
		t.Fatalf("%+v", summary)
	}
	if calls[0].CallID != "bruno" || summary.FirstAt != calls[0].DisconnectedAt {
		t.Fatalf("not ordered: %+v", calls)
	}
	text := readAudioIncident(summary, []audioProbeRound{{Results: []audioProbeResult{{OK: true}, {OK: false}}}})
	for _, want := range []string{"4 of 5", "3 reconnected, 1 did not", "signaling only on 1", "audio only on 1", "both signaling and audio on 2", "1 of 2 failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("reading %q lacks %q", text, want)
		}
	}
	if got := readAudioIncident(summarizeAudioIncident(nil, 3), nil); !strings.HasPrefix(got, "No browser audio") {
		t.Fatal(got)
	}
}

func TestIncidentWindowIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 4, 16, 0, 0, 0, time.UTC)
	from, to, err := parseAudioIncidentWindow("", "", now)
	if err != nil || !to.Equal(now) || to.Sub(from) != audioIncidentDefaultWindow {
		t.Fatal(from, to, err)
	}
	for _, c := range [][2]string{{"not a time", ""}, {"", "not a time"}, {"2026-11-04T15:00:00Z", "2026-11-04T14:00:00Z"}, {"2026-11-04T01:00:00Z", "2026-11-04T15:00:00Z"}} {
		if _, _, err := parseAudioIncidentWindow(c[0], c[1], now); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
}

func TestIncidentRouteIsOperatorOnlyAndReadsStoredEvents(t *testing.T) {
	ctx := softphoneTestCtx(t)
	_ = ctx
	app, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	network := audioNetworkEvent{ID: "browser-1:disconnected", Event: "softphone.browser.disconnected", CallID: row.ID, ProjectID: row.ProjectID, AdviserIdentity: alice, Reason: "transport_read_error", OccurredAt: at.Format(time.RFC3339Nano), ExpiresAt: at.Add(time.Hour).Format(time.RFC3339Nano),
		Disconnect: &audioDisconnectInfo{LastAudioReadAt: at.Format(time.RFC3339Nano), LastReadAt: at.Add(-18 * time.Second).Format(time.RFC3339Nano)}}
	app.collectBrowserNetwork(network)
	if _, err = app.audioNetworks.flush(t.Context(), app.db(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = app.db().saveAudioProbeRound(row.ProjectID, audioProbeRound{At: at.Format(time.RFC3339Nano), Calls: 3, Results: []audioProbeResult{{Target: "sip.telnyx.com:5060", OK: false, MS: 2000, Error: "timeout"}}}, at); err != nil {
		t.Fatal(err)
	}
	if err = app.db().saveAudioDashboardAlert(audioAlert{ProjectID: row.ProjectID, Stage: "browser", State: "degraded", CallCount: 3, At: at.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if w := phoneTestRequest(app, &alice, "GET", "/audio-health?view=incident", nil); w.Code != 403 {
		t.Fatalf("an adviser read project-wide incidents: %d", w.Code)
	}
	w := phoneTestRequest(app, nil, "GET", "/audio-health?view=incident&project_id=project-a", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var report audioIncidentReport
	if err = json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Calls) != 1 || report.Calls[0].FirstFailed != "signaling" || report.Calls[0].Adviser != "alice" {
		t.Fatalf("%+v", report)
	}
	if len(report.Probes) != 1 || report.Probes[0].Results[0].Error != "timeout" {
		t.Fatalf("%+v", report.Probes)
	}
	if bad := phoneTestRequest(app, nil, "GET", "/audio-health?view=incident&project_id=project-a&from=nope", nil); bad.Code != 400 {
		t.Fatalf("%d", bad.Code)
	}
	result, err := app.toolAudioIncident(t.Context(), ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(audioIncidentReport); len(got.Calls) != 1 {
		t.Fatalf("tool: %+v", got)
	}
	if _, err = app.toolAudioIncident(t.Context(), ctx, map[string]any{"from": "nope"}); err == nil {
		t.Fatal("tool accepted a bad time")
	}
	// A call answered in the window with no fault is listed as healthy.
	healthy := phoneTestCall(t, app, "call-healthy", "answered")
	if _, err = app.db().db.Exec(`UPDATE calls SET peer_kind=?,placed_at=?,answered_at=? WHERE id=?`, peerKindHuman, at.Add(-time.Minute).Format(time.RFC3339), at.Add(-30*time.Second).Format(time.RFC3339Nano), healthy.ID); err != nil {
		t.Fatal(err)
	}
	got, count, err := app.db().audioIncidentHealthyCalls(t.Context(), row.ProjectID, at.Add(-time.Minute), at.Add(time.Minute), map[string]bool{row.ID: true})
	if err != nil || count != 1 || got[0].CallID != healthy.ID {
		t.Fatalf("%+v %d %v", got, count, err)
	}
}

func TestNetworkChecksStartOnlyOnABurstAndNotAgainSoon(t *testing.T) {
	var m audioProbeMonitor
	at := time.Date(2026, 11, 4, 15, 2, 40, 0, time.UTC)
	if m.observe("p", "a", at) != 0 || m.observe("p", "a", at.Add(time.Second)) != 0 || m.observe("p", "b", at.Add(2*time.Second)) != 0 {
		t.Fatal("started below three calls")
	}
	if got := m.observe("p", "c", at.Add(3*time.Second)); got != 3 {
		t.Fatalf("burst not seen: %d", got)
	}
	if m.observe("p", "d", at.Add(4*time.Second)) != 0 {
		t.Fatal("ignored the cooldown")
	}
	if m.observe("other", "a", at) != 0 || m.observe("other", "b", at) != 0 || m.observe("other", "c", at) != 3 {
		t.Fatal("projects share a burst")
	}
	// Spread out over more than the window, they are not a burst.
	var slow audioProbeMonitor
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		if slow.observe("p", id, at.Add(time.Duration(i)*16*time.Second)) != 0 {
			t.Fatal("isolated drops counted as a burst")
		}
	}
	later := at.Add(audioProbeCooldown + time.Minute)
	if m.observe("p", "x", later) != 0 || m.observe("p", "y", later) != 0 || m.observe("p", "z", later) != 3 {
		t.Fatal("second burst after the cooldown not seen")
	}
}

func TestOnlyFaultsFeedTheNetworkChecks(t *testing.T) {
	var fired atomic.Int32
	app := &App{}
	for _, call := range []string{"a", "b", "c", "d"} {
		e := audioNetworkEvent{Event: "softphone.browser.disconnected", Reason: "peer_close", ShutdownIntent: "user_stop", CallID: call, ProjectID: "p"}
		app.collectBrowserNetwork(e)
		if audioDisconnectUnplanned(e) {
			fired.Add(1)
		}
	}
	if fired.Load() != 0 || len(app.audioProbes.recent) != 0 {
		t.Fatal("a normal hangup counted as a fault")
	}
}

func TestNetworkCheckTargetsAndResults(t *testing.T) {
	got := audioProbeTargets("", "https://agents.example.com/base")
	if strings.Join(got, ",") != "agents.example.com:443,sip.telnyx.com:5060,1.1.1.1:443" {
		t.Fatal(got)
	}
	if got = audioProbeTargets("", "http://127.0.0.1:5280"); got[0] != "127.0.0.1:5280" {
		t.Fatal(got)
	}
	if got = audioProbeTargets("off", "https://x"); got != nil {
		t.Fatal(got)
	}
	if got = audioProbeTargets("a.example:1, not-valid ,b.example:2,c:3,d:4,e:5,f:6", ""); strings.Join(got, ",") != "a.example:1,b.example:2,c:3,d:4,e:5" {
		t.Fatal(got)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := listener.Addr().String()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := closed.Addr().String()
	closed.Close()
	defer listener.Close()
	results := runAudioProbes(context.Background(), []string{open, refused}, time.Second)
	if !results[0].OK || results[0].Error != "" || results[1].OK || results[1].Error != "refused" {
		t.Fatalf("%+v", results)
	}
}

func TestLocalProxyAddressIsOffUntilConfiguredAndLoopbackOnly(t *testing.T) {
	resolve := func(remote, setting string) audioNetworkAddress {
		t.Helper()
		r := httptest.NewRequest("GET", "http://local/softphone/media/call/x", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", "198.51.100.7")
		address, err := resolveAudioNetworkAddress(r, setting)
		if err != nil {
			t.Fatal(err)
		}
		return address
	}
	if got := resolve("127.0.0.1:9000", ""); got.Client.String() != "127.0.0.1" || got.Source != "socket_peer" {
		t.Fatalf("on by default: %+v", got)
	}
	if got := resolve("127.0.0.1:9000", "127.0.0.1/32"); got.Client.String() != "198.51.100.7" || got.SocketPeer.String() != "127.0.0.1" || got.Source != "local_proxy" {
		t.Fatalf("%+v", got)
	}
	if got := resolve("192.0.2.4:9000", "127.0.0.1/32"); got.Client.String() != "192.0.2.4" || got.Source != "socket_peer" {
		t.Fatalf("remote client wrote its own address: %+v", got)
	}
	// A remote range in the setting is ignored, even with a loopback one beside it.
	for _, setting := range []string{"192.0.2.0/24", "0.0.0.0/0", "::/0", "127.0.0.1/1", "nonsense"} {
		if got := resolve("192.0.2.4:9000", setting); got.Source != "socket_peer" {
			t.Fatalf("%q: %+v", setting, got)
		}
		if got := audioLocalProxyPrefixes(setting); got != "" {
			t.Fatalf("%q allowed %q", setting, got)
		}
	}
	if got := audioLocalProxyPrefixes("127.0.0.1/32, 192.0.2.0/24, ::1/128"); got != "127.0.0.1/32,::1/128" {
		t.Fatal(got)
	}
	// A signed assertion that fails verification still blocks the header.
	r := httptest.NewRequest("GET", "http://local/softphone/media/call/x", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	r.Header.Set("X-Apteva-Client-IP-Signature", "00")
	r.Header.Set("X-Apteva-Client-IP-Metadata", "00")
	if got, _ := resolveAudioNetworkAddress(r, "127.0.0.1/32"); got.Source == "local_proxy" {
		t.Fatalf("a rejected assertion was overridden: %+v", got)
	}
}
