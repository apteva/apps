package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
)

func TestTransportHistoryAllowlistBoundsAndUnknownNativeFields(t *testing.T) {
	now := time.Now()
	sample := browserTransportSample{ID: "s1", Timestamp: now.Format(time.RFC3339Nano), Transport: "webrtc", Reason: "periodic", ConnectionID: "forged", ReceivingConnectionID: "forged", CallID: "forged", WindowMS: math.Inf(1), Metrics: map[string]float64{"remote_receiver_packetsLost": 7, "pair_availableOutgoingBitrate": 128000, "queue_ms": math.NaN(), "rtt_ms": -5, "private": 7}, States: map[string]string{"protocol": "udp", "local_candidate": "relay", "remote_candidate": "host", "codec": "audio/opus", "address": "SECRET", "ice": "invalid"}}
	out := normalizeTransportSamples([]browserTransportSample{sample, sample, sample, sample, sample}, now)
	if len(out) != 4 || out[0].ConnectionID != "" || out[0].ReceivingConnectionID != "" || out[0].CallID != "" || out[0].WindowMS != 0 || out[0].Metrics["remote_receiver_packetsLost"] != 7 {
		t.Fatal(out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "forged") {
		t.Fatal(string(raw))
	}
	if _, ok := out[0].Metrics["receiver_jitter_buffer_interval_ms"]; ok {
		t.Fatal("unsupported native measurement invented")
	}
	for _, change := range []func(*browserTransportSample){func(s *browserTransportSample) { s.Timestamp = "invalid" }, func(s *browserTransportSample) { s.Timestamp = now.Add(2 * time.Minute).Format(time.RFC3339Nano) }, func(s *browserTransportSample) { s.ID = "https://secret" }, func(s *browserTransportSample) { s.Transport = "bad" }, func(s *browserTransportSample) { s.Reason = "secret" }} {
		bad := sample
		change(&bad)
		if len(normalizeTransportSamples([]browserTransportSample{bad}, now)) != 0 {
			t.Fatal(bad)
		}
	}
}
func TestTransportHistoryActiveWriterAttributionAndCompactSnapshot(t *testing.T) {
	var tracker audioCallTelemetry
	first, second := &websocketWriterPump{}, &websocketWriterPump{}
	id1 := tracker.openedWithNetwork(first, "hash", "epoch", "socket_peer", audioNetworkEvent{Retention: time.Hour}, nil)
	base := time.Now().Add(-50 * time.Millisecond)
	tracker.socket.Events[0].At = base.Format(time.RFC3339Nano)
	id2 := tracker.openedWithNetwork(second, "hash", "epoch", "socket_peer", audioNetworkEvent{Retention: time.Hour}, nil)
	tracker.socket.Events[1].At = base.Add(20 * time.Millisecond).Format(time.RFC3339Nano)
	sample := browserTransportSample{ID: "s1", Timestamp: base.Add(10 * time.Millisecond).Format(time.RFC3339Nano), Transport: "websocket", Reason: "periodic"}
	report := normalizeBrowserAudioDiagnostics(browserAudioDiagnostics{ClientEpoch: "epoch", TransportSamples: []browserTransportSample{sample}})
	if old := tracker.observeBrowserConnection(first, report); len(old) != 0 {
		t.Fatal("stale writer accepted")
	}
	samples := tracker.observeBrowserConnection(second, report)
	if len(samples) != 1 || samples[0].ConnectionID != id1 || samples[0].ReceivingConnectionID != id2 || samples[0].ID == "s1" || samples[0].ExpiresAt == "" || len(tracker.browser.TransportSamples) != 0 {
		t.Fatal(samples, tracker.browser)
	}
	if again := tracker.observeBrowserConnection(second, report); again[0].ID != samples[0].ID {
		t.Fatal("retry not idempotent")
	}
}
func TestTransportHistoryPersistenceFailureRetentionAccessAndIndexedQuery(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	before, _ := app.db().findCall(row.ID)
	now := time.Now()
	sample := browserTransportSample{ID: "id-0", Timestamp: audioNetworkTimestamp(now), Transport: "webrtc", Reason: "periodic", Metrics: map[string]float64{"remote_receiver_packetsLost": 7}, ExpiresAt: audioNetworkTimestamp(now.Add(time.Hour))}
	app.audioTransports.enqueue([]browserTransportSample{sample, sample}, &row)
	_, err := app.db().db.Exec(`CREATE TRIGGER fail_transport BEFORE INSERT ON telephony_browser_transport_samples BEGIN SELECT RAISE(ABORT,'test'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.audioTransports.flush(context.Background(), app.db(), now); err == nil || len(app.audioTransports.pending) != 2 {
		t.Fatal(err)
	}
	app.db().db.Exec(`DROP TRIGGER fail_transport`)
	if err = app.audioTransports.flush(context.Background(), app.db(), now); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 150; i++ {
		s := sample
		s.ID = fmt.Sprint("id-", i)
		s.Timestamp = audioNetworkTimestamp(now.Add(time.Duration(i) * time.Millisecond))
		app.audioTransports.enqueue([]browserTransportSample{s}, &row)
	}
	for i := 0; i < 50; i++ {
		s := sample
		s.ID = fmt.Sprint("incident-", i)
		s.Reason = "incident"
		app.audioTransports.enqueue([]browserTransportSample{s}, &row)
	}
	for len(app.audioTransports.pending) > 0 {
		if err = app.audioTransports.flush(context.Background(), app.db(), now); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := app.db().browserTransportSamples(context.Background(), row.ProjectID, row.ID, now)
	if err != nil || len(samples) != 128 {
		t.Fatal(len(samples), err)
	}
	counts := map[string]int{}
	for _, s := range samples {
		counts[s.Reason]++
	}
	if counts["periodic"] != 96 || counts["incident"] != 32 {
		t.Fatal(counts)
	}
	other, _ := app.db().browserTransportSamples(context.Background(), "other", row.ID, now)
	if len(other) != 0 {
		t.Fatal("cross-project history")
	}
	expired, _ := app.db().browserTransportSamples(context.Background(), row.ProjectID, row.ID, now.Add(2*time.Hour))
	if len(expired) != 0 {
		t.Fatal("expired visible")
	}
	after, _ := app.db().findCall(row.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("collection modified call")
	}
	identity := phoneTestIdentity("alice")
	denied := phoneTestRequest(app, &identity, "GET", "/audio-health?call_id="+row.ID, nil)
	if denied.Code != 403 {
		t.Fatal(denied.Code)
	}
	response := phoneTestRequest(app, nil, "GET", "/audio-health?call_id="+row.ID, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "transport_samples") {
		t.Fatal(response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body["browser"]), "transport_samples") {
		t.Fatal("history in call summary")
	}
	plans, err := app.db().db.Query(`EXPLAIN QUERY PLAN SELECT sample_json FROM telephony_browser_transport_samples WHERE project_id=? AND call_id=? AND expires_at>? ORDER BY occurred_at DESC,id DESC LIMIT 128`, row.ProjectID, row.ID, audioNetworkTimestamp(now))
	if err != nil {
		t.Fatal(err)
	}
	defer plans.Close()
	plan := ""
	for plans.Next() {
		var id, parent, unused int
		var detail string
		plans.Scan(&id, &parent, &unused, &detail)
		plan += detail
	}
	if !strings.Contains(plan, "idx_browser_transport_call") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatal(plan)
	}
}
func TestTransportHistoryCollectorNeverWaitsOrChangesDuplexAudio(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	app.audioTransports.mu.Lock()
	// A busy collector is intentionally held across both directions of media.
	func() {
		defer app.audioTransports.mu.Unlock()
		diagnostics, _ := json.Marshal(map[string]any{"type": "diagnostics", "diagnostics": browserAudioDiagnostics{TransportSamples: []browserTransportSample{{ID: "s1", Timestamp: time.Now().Format(time.RFC3339Nano), Transport: "websocket", Reason: "periodic"}}}})
		if err := wsutil.WriteClientText(browser, diagnostics); err != nil {
			t.Fatal(err)
		}
		pcm := pcm16ToBytes([]int16{100, -200, 300, -400})
		if err := wsutil.WriteClientBinary(browser, pcm); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, peer, 3*time.Second); string(got) != string(pcm) {
			t.Fatal("microphone changed")
		}
		if err := wsutil.WriteClientBinary(peer, pcm); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, browser, 3*time.Second); string(got) != string(pcm) {
			t.Fatal("playback changed")
		}
	}()
	if app.audioTransports.dropped.Load() != 1 {
		t.Fatal("collector did not reject without waiting")
	}
	for i := 0; i < 1025; i++ {
		app.audioTransports.enqueue([]browserTransportSample{{ID: fmt.Sprint(i)}}, &row)
	}
	if len(app.audioTransports.pending) != 1024 || app.audioTransports.dropped.Load() != 2 {
		t.Fatal("unbounded collector")
	}
}

func TestTransportHistoryPacedPartsMergeIdempotentlyAndReportIncompleteSamples(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	now := time.Now()
	base := browserTransportSample{ID: "s1", Timestamp: now.Format(time.RFC3339Nano), Transport: "webrtc", Reason: "periodic", PartCount: 2, Metrics: map[string]float64{"remote_receiver_packetsLost": 7}, States: map[string]string{"protocol": "udp"}}
	parts := normalizeTransportSamples([]browserTransportSample{base}, now)
	parts[0].ExpiresAt = audioNetworkTimestamp(now.Add(time.Hour))
	app.audioTransports.enqueue(parts, &row)
	if err := app.audioTransports.flush(context.Background(), app.db(), now); err != nil {
		t.Fatal(err)
	}
	first, err := app.db().browserTransportSamples(context.Background(), row.ProjectID, row.ID, now)
	if err != nil || len(first) != 1 || first[0].Complete {
		t.Fatal(first, err)
	}
	next := base
	next.PartIndex = 1
	next.Metrics = map[string]float64{"pair_availableOutgoingBitrate": 128000}
	next.States = map[string]string{}
	parts = normalizeTransportSamples([]browserTransportSample{next, next}, now)
	for i := range parts {
		parts[i].ExpiresAt = audioNetworkTimestamp(now.Add(time.Hour))
	}
	app.audioTransports.enqueue(parts, &row)
	if err = app.audioTransports.flush(context.Background(), app.db(), now); err != nil {
		t.Fatal(err)
	}
	complete, err := app.db().browserTransportSamples(context.Background(), row.ProjectID, row.ID, now)
	if err != nil || len(complete) != 1 || !complete[0].Complete || len(complete[0].Parts) != 2 || complete[0].Metrics["remote_receiver_packetsLost"] != 7 || complete[0].Metrics["pair_availableOutgoingBitrate"] != 128000 || complete[0].States["protocol"] != "udp" {
		t.Fatal(complete, err)
	}
	// A periodic observation reused as pre-incident context must move into
	// the incident retention budget, together with its public reason.
	next.Reason = "incident_context"
	parts = normalizeTransportSamples([]browserTransportSample{next}, now)
	parts[0].ExpiresAt = audioNetworkTimestamp(now.Add(time.Hour))
	app.audioTransports.enqueue(parts, &row)
	if err = app.audioTransports.flush(context.Background(), app.db(), now); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err = app.db().db.QueryRow(`SELECT kind FROM telephony_browser_transport_samples WHERE id=?`, base.ID).Scan(&kind); err != nil || kind != "incident" {
		t.Fatal(kind, err)
	}
	var telemetry audioCallTelemetry
	w := &websocketWriterPump{}
	telemetry.openedWithNetwork(w, "hash", "epoch", "socket_peer", audioNetworkEvent{}, nil)
	telemetry.observeBrowserConnection(w, browserAudioDiagnostics{ClientEpoch: "epoch", ConnectionState: "connected", PlaybackDroppedMS: 20})
	telemetry.attributeTransportSamples(w, "epoch", normalizeTransportSamples([]browserTransportSample{base}, now))
	if telemetry.browser.ConnectionState != "connected" || telemetry.browser.PlaybackDroppedMS != 20 {
		t.Fatal("history piece replaced the media summary")
	}
}
