package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestNotificationOutboxRetriesSameIdentityWithoutBlockingAudio(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, app, "notifications", "telnyx")
	app.db().db.Exec(`UPDATE call_events SET published_at='existing'`)
	var fail atomic.Bool
	fail.Store(true)
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ids = append(ids, body["event_id"].(string))
		if fail.Load() {
			http.Error(w, "temporary", 503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_APP_TOKEN", "local-test")
	app.observeCallNotification(row.ProjectID, row.ID, "telephony.audio.degraded", map[string]any{"reason": "test"})
	if err := app.flushCallNotifications(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.publishLifecycleEvents(ctx, row.ID); err == nil {
		t.Fatal("503 acknowledged")
	}
	var pending int
	app.db().db.QueryRow(`SELECT count(*) FROM call_events WHERE published_at='' AND call_id=?`, row.ID).Scan(&pending)
	if pending != 1 {
		t.Fatal(pending)
	}
	fail.Store(false)
	if err := app.publishLifecycleEvents(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("non-idempotent retry %v", ids)
	}
	app.notifications.mu.Lock()
	done := make(chan struct{})
	go func() {
		app.observeCallNotification(row.ProjectID, row.ID, "telephony.audio.degraded", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("audio observer waited for outbox")
	}
	app.notifications.mu.Unlock()
	if app.notifications.dropped.Load() != 1 {
		t.Fatal("collection loss hidden")
	}
	// Persistence errors retain the bounded pending memory for the next tick.
	app.observeCallNotification(row.ProjectID, row.ID, "recording.ready", map[string]any{"id": "recording"})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := app.flushCallNotifications(canceled); err == nil {
		t.Fatal("canceled database write succeeded")
	}
	if len(app.notifications.pending) != 1 {
		t.Fatal("failed persistence lost notification")
	}
	if err := app.flushCallNotifications(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestRTCIngressEvidenceIsBoundedAndDoesNotInventSpeechLoss(t *testing.T) {
	var d rtcIngressTelemetry
	at := time.Now()
	packet := func(seq uint16, ts uint32) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: seq, Timestamp: ts}}
	}
	d.observe(packet(1, 960), at, at, 0, "browser-1", 1, "received", 0)
	// DTX/mute advances the timestamp with consecutive packet numbers.
	d.observe(packet(2, 480000), at.Add(time.Second), at.Add(time.Second), 0, "browser-1", 1, "received", 0)
	// Padding is transport traffic, never concealment.
	d.observe(packet(3, 480000), at.Add(time.Second), at.Add(time.Second), 0, "browser-1", 1, "padding", 0)
	d.observe(packet(5, 481920), at.Add(1040*time.Millisecond), at.Add(1040*time.Millisecond), 0, "browser-1", 1, "received", 0)
	s := d.snapshot()
	if s.Packets != 3 || s.Padding != 1 {
		t.Fatal(s)
	}
	for _, e := range s.Events {
		if e.ConcealedMS != 0 {
			t.Fatal("invented speech loss", e)
		}
	}
	d.observe(packet(5, 481920), at, at.Add(30*time.Millisecond), 4, "browser-1", 1, "processed", 0)
	d.observe(packet(5, 481920), at, at, 2, "browser-1", 1, "concealment", 20)
	for i := 0; i < 1000; i++ {
		d.observe(packet(uint16(i), uint32(i*960)), at, at, 1, "browser-1", 1, "concealment", 20)
	}
	if s = d.snapshot(); len(s.Events) != 64 || s.MaxQueueWaitMS != 30 || s.SocketDropsAvailable {
		t.Fatal(s)
	}
	d.mu.Lock()
	done := make(chan struct{})
	go func() { d.observe(packet(6, 482880), at, at, 0, "browser-1", 1, "received", 0); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RTP observation blocked")
	}
	d.mu.Unlock()
	if d.snapshot().Skipped != 1 {
		t.Fatal("contention not exposed")
	}
}
func TestSIPBYEFailureRemainsVisibleAfterLocalCancellation(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, app, "sip-bye-failure", "telnyx")
	d := app.newSIPDiagnostics(row, "dialog")
	media := &sipRTPMedia{}
	d.attach(media, "dialog")
	d.end("local", nil)
	media.observedReadError(net.ErrClosed)
	if d.snapshot(time.Now()).RTP.ReadErrors != 0 {
		t.Fatal("cleanup counted as live error")
	}
	d.event("sip_bye_requested", "local", "call_ended", 0)
	d.event("sip_bye_result", "local", "certificate verification failed", 0)
	v := d.snapshot(time.Now())
	if v.Signal.BYEFailures != 1 || v.Signal.BYEStatus != "unconfirmed" {
		t.Fatal(v.Signal)
	}
	summary := audioDashboardSummary{Metrics: map[string]float64{}}
	addCarrierAudioSummary(&summary, &carrierTelemetryRecord{SIP: &v, At: ringTime(time.Now())})
	if !containsString(summary.Issues, "carrier_error") {
		t.Fatal("BYE failure hidden as cleanup")
	}
	d.event("sip_bye_result", "local", "", 200)
	if d.snapshot(time.Now()).Signal.BYEStatus != "confirmed" {
		t.Fatal("confirmed hangup lost")
	}
}
func TestProxyCallIdentityRequiresValidMediaSession(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "proxy-correlate", "in-progress")
	for _, tc := range []struct{ token, transport, want string }{{"invalid", "invalid", ""}, {row.PeerToken, "invalid", row.ID}} {
		r := httptest.NewRequest("GET", "http://local/softphone/media/"+row.ID+"/"+tc.token+"?transport="+tc.transport, nil)
		r.Header.Set("X-Apteva-Call-ID", "forged")
		r.Header.Set(proxyConnectionHeader, "proxy-123")
		w := httptest.NewRecorder()
		app.handleSoftphoneMedia(w, r)
		if got := w.Header().Get("X-Apteva-Call-ID"); got != tc.want {
			t.Fatalf("status %d correlated %q want %q", w.Code, got, tc.want)
		}
	}
}

func TestProxyIdentityRetainedWithoutTokenOrBrowserOverride(t *testing.T) {
	r := httptest.NewRequest("GET", "http://local/softphone/media/call/SECRET", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set(proxyConnectionHeader, "proxy-123")
	address, _ := resolveAudioNetworkAddress(r)
	row := &callRow{ID: "authenticated-call", ProjectID: "project-a", ThreadID: "thread-safe"}
	v := newAudioNetworkContextWithAddress(row, phoneTestIdentity("alice"), address, nil)
	raw, _ := json.Marshal(v)
	if v.ProxyConnectionID != "proxy-123" || strings.Contains(string(raw), "SECRET") {
		t.Fatal("unsafe or missing correlation")
	}
	r.Header.Set(proxyConnectionHeader, "wss://private/SECRET")
	address, _ = resolveAudioNetworkAddress(r)
	if address.ProxyConnectionID != "" {
		t.Fatal("unbounded proxy metadata accepted")
	}
	a := &App{installID: 42}
	d := a.correlatedBridgeDialer(row)
	if err := d.OnHeader([]byte(proxyConnectionHeader), []byte("upstream-123")); err != nil {
		t.Fatal(err)
	}
	v2 := <-a.carrierTelemetry.queue
	if v2.CallID != row.ID || v2.ProxyConnectionID != "upstream-123" {
		t.Fatal("AI proxy identity not linked to stored call")
	}
}

func BenchmarkRTCIngressEvidenceHealthy(b *testing.B) {
	var d rtcIngressTelemetry
	p := &rtp.Packet{Header: rtp.Header{SSRC: 42}}
	start := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.SequenceNumber = uint16(i)
		p.Timestamp = uint32(i * 960)
		now := start.Add(time.Duration(i) * 20 * time.Millisecond)
		d.observe(p, now, now, 0, "benchmark", 1, "received", 0)
	}
}
