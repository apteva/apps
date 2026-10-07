package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestBrowserNetworkTrustedAddressesAndClassification(t *testing.T) {
	row := &callRow{ID: "call", ProjectID: "project"}
	identity := phoneTestIdentity("alice")
	for _, tc := range []struct{ remote, forwarded, proxies, exits, ip, classification, source string }{
		{"198.51.100.7:1234", "203.0.113.8", "", "198.51.100.7", "198.51.100.7", "known_vpn_exit", "socket_peer"},
		{"192.0.2.4:1234", "203.0.113.8,198.51.100.7", "192.0.2.0/24", "198.51.100.7", "198.51.100.7", "known_vpn_exit", "trusted_forwarded_peer"},
		{"192.0.2.4:1234", "invalid,198.51.100.7", "192.0.2.0/24", "198.51.100.7", "192.0.2.4", "unknown", "socket_peer"},
		{"[2001:db8:2::7]:1234", "", "", "2001:db8:2::/64", "2001:db8:2::7", "known_vpn_exit", "socket_peer"},
		{"[::ffff:198.51.100.7]:1234", "", "", "198.51.100.7", "198.51.100.7", "known_vpn_exit", "socket_peer"},
		{"198.51.100.8:1234", "", "", "invalid,0.0.0.0/0", "198.51.100.8", "unknown", "socket_peer"},
		{"invalid", "", "", "198.51.100.7", "", "unknown", "unavailable"},
	} {
		t.Run(tc.remote+tc.forwarded, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://local/softphone/media/call/DO_NOT_STORE", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			r.Header.Set("Authorization", "Bearer DO_NOT_STORE")
			e := newAudioNetworkContext(row, identity, r, map[string]string{"audio_telemetry_trusted_proxy_cidrs": tc.proxies, "audio_telemetry_known_vpn_exits": tc.exits})
			if e.ClientIP != tc.ip || e.Classification != tc.classification || e.AddressSource != tc.source || e.AdviserIdentity != identity || e.Retention != 7*24*time.Hour {
				t.Fatalf("%+v", e)
			}
			raw, _ := json.Marshal(e)
			if strings.Contains(string(raw), "DO_NOT_STORE") {
				t.Fatal("copied secrets")
			}
		})
	}
	if audioNetworkClassification(netip.Addr{}, "198.51.100.7") != "unknown" {
		t.Fatal("invalid IP classified")
	}
}

func TestBrowserNetworkCollectionIsBoundedAndNeverWaits(t *testing.T) {
	var c audioNetworkCollector
	c.mu.Lock()
	done := make(chan struct{})
	go func() { c.enqueue(audioNetworkEvent{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("collector waited for a lock")
	}
	c.mu.Unlock()
	for i := 0; i < audioNetworkQueueLimit+1; i++ {
		c.enqueue(audioNetworkEvent{})
	}
	if len(c.pending) != audioNetworkQueueLimit || c.dropped.Load() != 2 {
		t.Fatal(len(c.pending), c.dropped.Load())
	}
}

func TestBrowserNetworkFrozenIdentityAndConnectionSamples(t *testing.T) {
	var tracker audioCallTelemetry
	var collected []audioNetworkEvent
	collect := func(e audioNetworkEvent) { collected = append(collected, e) }
	first, second := &websocketWriterPump{}, &websocketWriterPump{}
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	metadata := audioNetworkEvent{CallID: "call", ProjectID: "project", AdviserIdentity: alice, ClientIP: "198.51.100.7", Classification: "known_vpn_exit", Retention: 24 * time.Hour}
	id1 := tracker.openedWithNetwork(first, "hash", "epoch", "socket_peer", metadata, collect)
	oldSample := time.Now().UTC().Format(time.RFC3339Nano)
	metadata.AdviserIdentity = bob
	metadata.ClientIP = "203.0.113.8"
	metadata.Classification = "unknown"
	id2 := tracker.openedWithNetwork(second, "hash2", "epoch", "socket_peer", metadata, collect)
	tracker.closed(first, "session_replaced", nil)
	tracker.closed(first, "handler_closed", nil)
	if id1 == id2 || len(collected) != 3 || collected[2].AdviserIdentity != alice || collected[2].ClientIP != "198.51.100.7" || collected[2].Action != "replaced" || collected[1].AdviserIdentity != bob {
		t.Fatalf("%+v", collected)
	}
	report := browserAudioDiagnostics{ConnectionID: "forged", ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano), DropEvents: []audioDropEvent{
		{Timestamp: oldSample, ConnectionID: "forged"},
		{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), ConnectionID: "forged"},
		{Timestamp: "invalid", ConnectionID: "forged"},
	}, SessionEvents: []mediaSessionEvent{{Timestamp: oldSample, ConnectionID: "forged"}}}
	tracker.observeBrowserConnection(second, report)
	if tracker.browser.ConnectionID != id2 || tracker.browser.DropEvents[0].ConnectionID != id1 || tracker.browser.DropEvents[1].ConnectionID != id2 || tracker.browser.DropEvents[2].ConnectionID != "" || tracker.browser.SessionEvents[0].ConnectionID != id1 {
		t.Fatalf("%+v", tracker.browser)
	}
	tracker.observeBrowserConnection(first, browserAudioDiagnostics{PlaybackDroppedMS: 999})
	if tracker.browser.PlaybackDroppedMS == 999 {
		t.Fatal("stale socket diagnostics accepted")
	}
	tracker.closed(second, "transport_read_error", wsutil.ClosedError{Code: ws.StatusGoingAway, Reason: "https://secret/token"})
	socket, _ := tracker.snapshots()
	raw, _ := json.Marshal(socket)
	if strings.Contains(string(raw), "198.51.100.7") || strings.Contains(string(raw), "secret/token") || socket.ConnectionID != "" || socket.Events[3].Code != 1001 {
		t.Fatal(string(raw))
	}
}

func TestBrowserNetworkServerGapsPreserveConnectionBoundaries(t *testing.T) {
	var timeline liveAudioTimeline
	timeline.observe("microphone_server_receipt", 960, time.Time{}, "", "1", "first")
	timeline.mu.Lock()
	s := timeline.stages["microphone_server_receipt"]
	s.LastClockMS -= 500
	timeline.stages["microphone_server_receipt"] = s
	timeline.mu.Unlock()
	timeline.observe("microphone_server_receipt", 960, time.Time{}, "", "2", "second")
	_, stages := timeline.snapshot()
	gaps := stages["microphone_server_receipt"].GapEvents
	if len(gaps) != 1 || gaps[0].ConnectionID != "second" || gaps[0].PreviousConnectionID != "first" {
		t.Fatal(gaps)
	}
	var hub softphoneHub
	hub.observeCaptureFrame(10, "first")
	hub.observeCaptureFrame(12, "second")
	n, drops := hub.captureDiagnostics()
	if n != 1 || len(drops) != 1 || drops[0].ConnectionID != "second" || drops[0].DurationMS != 20 {
		t.Fatal(n, drops)
	}
}

func TestBrowserNetworkPersistenceRetryExpiryAndOperatorOnly(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	before, _ := app.db().findCall(row.ID)
	now := time.Now().UTC()
	makeEvent := func(id string, expiry time.Time) audioNetworkEvent {
		return audioNetworkEvent{ID: id, CallID: row.ID, ProjectID: row.ProjectID, ConnectionID: "browser", OccurredAt: now.Format(time.RFC3339Nano), ExpiresAt: expiry.Format(time.RFC3339Nano), ClientIP: "198.51.100.7", AdviserIdentity: phoneTestIdentity("alice")}
	}
	live := makeEvent("live", now.Add(time.Hour))
	app.audioNetworks.enqueue(live)
	app.audioNetworks.enqueue(live)
	app.audioNetworks.enqueue(makeEvent("expired", now.Add(-time.Hour)))
	// Force a monitoring write failure without touching calls or media tables.
	if _, e := app.db().db.Exec(`CREATE TRIGGER fail_browser_network BEFORE INSERT ON telephony_browser_network_events BEGIN SELECT RAISE(ABORT,'test'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := app.audioNetworks.flush(context.Background(), app.db(), now); e == nil {
		t.Fatal("expected collection failure")
	}
	if len(app.audioNetworks.pending) != 3 {
		t.Fatal("failed batch lost")
	}
	if _, e := app.db().db.Exec(`DROP TRIGGER fail_browser_network`); e != nil {
		t.Fatal(e)
	}
	changed, e := app.audioNetworks.flush(context.Background(), app.db(), now)
	if e != nil || len(changed[row.ProjectID]) != 2 || len(app.audioNetworks.pending) != 0 {
		t.Fatal(changed, e)
	}
	events, e := app.db().browserNetworkEvents(context.Background(), row.ProjectID, row.ID, now)
	if e != nil || len(events) != 1 {
		t.Fatal(events, e)
	}
	if other, _ := app.db().browserNetworkEvents(context.Background(), "other", row.ID, now); len(other) != 0 {
		t.Fatal("cross-project records")
	}
	after, _ := app.db().findCall(row.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("network collection modified call")
	}
	w := phoneTestRequest(app, nil, "GET", "/audio-health?call_id="+row.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "198.51.100.7") {
		t.Fatal(w.Code, w.Body.String())
	}
	identity := phoneTestIdentity("alice")
	w = phoneTestRequest(app, &identity, "GET", "/audio-health?call_id="+row.ID, nil)
	if w.Code != 403 || strings.Contains(w.Body.String(), "198.51.100.7") {
		t.Fatal("network details exposed to delegated user")
	}
	public, _ := json.Marshal(callsPublic([]callRow{*after}))
	if strings.Contains(string(public), "198.51.100.7") {
		t.Fatal("raw address in call response")
	}
	// Reads hide expiry immediately, before the next cleanup batch.
	events, e = app.db().browserNetworkEvents(context.Background(), row.ProjectID, row.ID, now.Add(2*time.Hour))
	if e != nil || len(events) != 0 {
		t.Fatal(events, e)
	}
}

func TestBrowserNetworkFlushIsBounded(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	now := time.Now().UTC()
	for i := 0; i < 125; i++ {
		app.audioNetworks.enqueue(audioNetworkEvent{ID: fmt.Sprint(i), CallID: row.ID, ProjectID: row.ProjectID, OccurredAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)})
	}
	changed, err := app.audioNetworks.flush(context.Background(), app.db(), now)
	if err != nil || len(changed[row.ProjectID]) != 100 || len(app.audioNetworks.pending) != 25 {
		t.Fatal(changed, err)
	}
	if _, err = app.audioNetworks.flush(context.Background(), app.db(), now); err != nil {
		t.Fatal(err)
	}
	var n int
	app.db().db.QueryRow(`SELECT count(*) FROM telephony_browser_network_events`).Scan(&n)
	if n != 125 {
		t.Fatal(n)
	}
}

func TestBrowserNetworkConcurrentFlushAccountsForEveryCollectedEvent(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	now := time.Now().UTC()
	var wg sync.WaitGroup
	done := make(chan struct{})
	fail := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				if _, err := app.audioNetworks.flush(context.Background(), app.db(), now); err != nil {
					fail <- err
					return
				}
			}
		}
	}()
	for i := 0; i < 500; i++ {
		app.audioNetworks.enqueue(audioNetworkEvent{ID: fmt.Sprint(i), CallID: row.ID, ProjectID: row.ProjectID, OccurredAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)})
	}
	close(done)
	wg.Wait()
	select {
	case err := <-fail:
		t.Fatal(err)
	default:
	}
	for len(app.audioNetworks.pending) > 0 {
		if _, err := app.audioNetworks.flush(context.Background(), app.db(), now); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	app.db().db.QueryRow(`SELECT count(*) FROM telephony_browser_network_events`).Scan(&n)
	if uint64(n)+app.audioNetworks.dropped.Load() != 500 {
		t.Fatal("unaccounted collection loss", n, app.audioNetworks.dropped.Load())
	}
}

func TestBrowserNetworkRejectsDoNotCreateAttachmentEvents(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	for _, token := range []string{"invalid", row.PeerToken} {
		r := httptest.NewRequest("GET", "/softphone/media/"+row.ID+"/"+token, nil)
		w := httptest.NewRecorder()
		app.handleSoftphoneMedia(w, r)
		if len(app.audioNetworks.pending) != 0 {
			t.Fatal("failed validation or upgrade recorded an attachment")
		}
	}
}

func TestBrowserNetworkFailedMonitoringKeepsBothMediaDirectionsAndReconnect(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "network-media", "in-progress")
	alice := phoneTestIdentity("alice")
	if err := app.setPhoneOwner(&row, &phonePrincipal{Identity: alice, Project: row.ProjectID, Destinations: map[string]bool{"sales": true}}, "sales"); err != nil {
		t.Fatal(err)
	}
	session, err := app.issuePhoneSession(&row, &phonePrincipal{Identity: alice})
	if err != nil {
		t.Fatal(err)
	}
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/"+row.ID+"/"+row.CallbackSecret)
	defer peer.Close()
	browser := dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42"))
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	hub := app.softphones.hubFor(row.ID)
	carrier := hub.peerWriter()
	socket, _ := hub.telemetry.snapshots()
	id1 := socket.ConnectionID
	if _, err = app.db().db.Exec(`DROP TABLE telephony_browser_network_events`); err != nil {
		t.Fatal(err)
	}
	if _, err = app.audioNetworks.flush(context.Background(), app.db(), time.Now()); err == nil {
		t.Fatal("missing monitoring table succeeded")
	}
	pcm := bytes.Repeat([]byte{0x34, 0x12}, 480)
	if err = wsutil.WriteClientBinary(browser, pcm); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, peer, 3*time.Second); !bytes.Equal(got, pcm) {
		t.Fatal("microphone media changed")
	}
	if err = wsutil.WriteClientBinary(peer, pcm); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, browser, 3*time.Second); !bytes.Equal(got, pcm) {
		t.Fatal("caller media changed")
	}
	fresh, err := app.issuePhoneSession(&row, &phonePrincipal{Identity: alice})
	if err != nil {
		t.Fatal(err)
	}
	second := dialWS(t, server.URL+strings.TrimPrefix(fresh.MediaURL, "/api/apps/telephony/_install/42"))
	defer second.Close()
	readSoftphoneEventWithin(t, second, "ready", 3*time.Second)
	socket, _ = hub.telemetry.snapshots()
	if socket.ConnectionID == id1 || socket.Events[len(socket.Events)-1].Reason != "session_replaced" || hub.peerWriter() != carrier {
		t.Fatal("monitoring affected replacement or carrier")
	}
	if err = wsutil.WriteClientBinary(second, pcm); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, peer, 3*time.Second); !bytes.Equal(got, pcm) {
		t.Fatal("reconnected microphone changed")
	}
	current, err := app.db().findCall(row.ID)
	if err != nil || isTerminalStatus(current.Status) {
		t.Fatal("monitoring failure ended call")
	}
	app.audioNetworks.mu.Lock()
	defer app.audioNetworks.mu.Unlock()
	for _, e := range app.audioNetworks.pending {
		if e.AdviserIdentity != alice || e.IdentitySource != "validated_media_session" || e.ClientIP != "127.0.0.1" {
			t.Fatalf("incorrect browser attribution: %+v", e)
		}
	}
}
