package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func flushCarrierTest(t *testing.T, a *App) {
	t.Helper()
	for i := 0; i < 30; i++ {
		if _, err := a.carrierTelemetry.flush(t.Context(), a.db(), time.Now()); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			} // bounded background retry
			t.Fatal(err)
		}
		if len(a.carrierTelemetry.queue) == 0 && len(a.carrierTelemetry.pending) == 0 {
			return
		}
	}
	t.Fatal("collector failed to drain")
}
func TestCarrierEvidenceCancellationAndCloseBoundaries(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	for _, tc := range []struct {
		name  string
		err   error
		class string
	}{
		{"eof", io.EOF, "eof"}, {"reset", syscall.ECONNRESET, "connection_reset"}, {"timeout", context.DeadlineExceeded, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := recoveryCall(t, a, "boundary-"+tc.name, "telnyx")
			b, err := a.claimCarrierBridge(row, t.Context(), "proxy-123")
			if err != nil {
				t.Fatal(err)
			}
			b.received(ws.OpClose, ws.NewCloseFrameBody(ws.StatusGoingAway, "carrier ending"))
			b.socketError(mediaCloseLegCarrier, "read", tc.err)
			if b.ctx.Err() == nil {
				t.Fatal("failure did not cancel transport")
			}
			first := b.first.ID
			b.socketError(mediaCloseLegCarrier, "write", net.ErrClosed)
			b.socketError(mediaCloseLegCarrier, "read", wsutil.ClosedError{Code: 1006})
			if b.first.ID != first {
				t.Fatal("cleanup overwrote first boundary failure")
			}
			flushCarrierTest(t, a)
			events, _, err := a.db().carrierTransportEvidence(t.Context(), row.ProjectID, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			var peer, cancel, late bool
			for _, r := range events {
				if r.ProxyConnectionID != "proxy-123" || r.CallID != row.ID || r.InstallID != a.installID {
					t.Fatal("lost correlation", r)
				}
				e := r.Event
				if e == nil {
					continue
				}
				if e.PeerCloseReceived {
					peer = true
					if e.ActualCloseCode != 1001 {
						t.Fatal(e)
					}
				}
				if e.ActualCloseCode == 1006 {
					t.Fatal("synthetic code presented as received frame")
				}
				if e.Kind == "local_close_requested" {
					cancel = true
					if e.AfterLocalCancel || e.Trigger != "carrier_read_error" {
						t.Fatal(e)
					}
				}
				if e.Kind == "transport_read_error" && !e.AfterLocalCancel && e.ErrorClass != tc.class {
					t.Fatal("wrong error class", e)
				}
				if e.Kind == "transport_write_error" && e.AfterLocalCancel {
					late = true
				}
			}
			if !peer || !cancel || !late {
				t.Fatal("missing boundary evidence", peer, cancel, late)
			}
		})
	}
}
func TestCarrierEvidenceGenerationGapAndLatestOwnership(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	row := recoveryCall(t, a, "gap-generations", "telnyx")
	old, err := a.claimCarrierBridge(row, t.Context(), "old-proxy")
	if err != nil {
		t.Fatal(err)
	}
	old.media()
	old.arrival.last.Store(time.Now().Add(-4320 * time.Millisecond).UnixNano())
	old.fail(carrierBridgeEvidence{Kind: "provider_error", ProviderEvent: "streaming.failed", EventID: "event-1", SignatureValidation: "verified"})
	old.mu.Lock()
	old.attempts = 2
	episode := old.episodeID
	old.mu.Unlock()
	fresh, err := a.claimCarrierBridge(row, t.Context(), "new-proxy")
	if err != nil {
		t.Fatal(err)
	}
	fresh.setStream("replacement")
	fresh.media()
	s := fresh.sample(time.Now())
	if s.Carrier.MaxGapMS < 4320 || s.RecoveryEpisodeID != episode {
		t.Fatal(s)
	}
	a.carrierTelemetry.enqueue(s)
	oldSample := old.sample(time.Now().Add(time.Second))
	a.carrierTelemetry.enqueue(oldSample)
	flushCarrierTest(t, a)
	_, latest, err := a.db().carrierTransportEvidence(t.Context(), row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.BridgeGeneration != fresh.generation || latest.ProxyConnectionID != "new-proxy" {
		t.Fatal("old cleanup replaced live observation", latest)
	}
	episodes := carrierRecoveryEpisodes([]map[string]any{{"recovery_episode_id": episode, "recovery_attempts": 2}, {"recovery_episode_id": episode, "recovery_attempts": 2}})
	if len(episodes) != 1 || episodes[0]["attempts"] != 2 {
		t.Fatal(episodes)
	}
	fresh.fail(carrierBridgeEvidence{Kind: "provider_error", EventID: "event-2"})
	if fresh.episodeID == episode {
		t.Fatal("new failure reused completed episode")
	}
	if _, l, e := a.db().carrierTransportEvidence(t.Context(), "other-project", row.ID); e != nil || l != nil {
		t.Fatal("cross-project leak", l, e)
	}
}
func TestCarrierEvidenceHandshakeCorrelationAndUpgrade(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	row := recoveryCall(t, a, "duplicate-proxy", "telnyx")
	_, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Exact production handler rejects a valid duplicate before a generation exists.
	r := httptest.NewRequest("GET", "/media/telnyx/"+row.ID+"/"+row.CallbackSecret, nil)
	r.Header.Set(proxyConnectionHeader, "proxy-duplicate")
	w := httptest.NewRecorder()
	a.handleTelnyxMediaStream(w, r)
	if w.Code != 409 || w.Header().Get("X-Apteva-Call-ID") != row.ID {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	flushCarrierTest(t, a)
	events, _, err := a.db().carrierTransportEvidence(t.Context(), row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, v := range events {
		if v.Event != nil && v.Event.Kind == "handshake_rejected" {
			found = true
			if v.ProxyConnectionID != "proxy-duplicate" || v.Event.HTTPStatus != 409 {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal(events)
	}
	bad := httptest.NewRecorder()
	a.handleTelnyxMediaStream(bad, httptest.NewRequest("GET", "/media/telnyx/"+row.ID+"/wrong", nil))
	if bad.Header().Get("X-Apteva-Call-ID") != "" {
		t.Fatal("unauthenticated call identity leaked")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticatedCarrierHeaders(w, r, row)
		c, _, err := upgradeBuffered(w, r)
		if err == nil {
			c.Close()
		}
	}))
	defer server.Close()
	dialer := ws.Dialer{OnHeader: func(k, v []byte) error {
		if strings.EqualFold(string(k), "X-Apteva-Call-ID") && string(v) == row.ID {
			found = true
		}
		return nil
	}}
	found = false
	conn, _, _, err := dialer.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !found {
		t.Fatal("101 omitted authenticated call header")
	}
}
func TestCarrierEvidenceCollectorBoundedAndObservational(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "collector-limit", "telnyx")
	before, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.carrierTelemetry.flushMu.Lock()
	done := make(chan struct{})
	go func() {
		for i := 0; i < carrierEvidenceLimit*2; i++ {
			a.carrierTelemetry.enqueue(carrierTelemetryRecord{ProjectID: row.ProjectID, CallID: row.ID, Kind: "event"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("audio-side collection blocked on database worker")
	}
	a.carrierTelemetry.flushMu.Unlock()
	if len(a.carrierTelemetry.queue) != carrierEvidenceLimit || a.carrierTelemetry.dropped.Load() != carrierEvidenceLimit {
		t.Fatal("unbounded collection")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = a.carrierTelemetry.flush(canceled, a.db(), time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	flushCarrierTest(t, a)
	after, _ := a.db().findCall(row.ID)
	if before.Status != after.Status || before.MediaStatus != after.MediaStatus {
		t.Fatal("telemetry mutated audio lifecycle")
	}
	var n int
	a.db().db.QueryRow(`SELECT count(*) FROM telephony_carrier_transport_history WHERE call_id=?`, row.ID).Scan(&n)
	if n != 128 {
		t.Fatal("history retention not bounded", n)
	}
}
func TestCarrierEvidenceSanitizesCredentialsAndSparseSamples(t *testing.T) {
	row := testCall("redaction", "in-progress")
	row.PeerToken = "private-peer"
	e := normalizeCarrierEvidence(&row, carrierBridgeEvidence{Detail: "failed wss://host/private?token=abc private-peer", LocalCloseReason: "https://user:password@host/?key=secret"})
	raw, _ := json.Marshal(e)
	for _, secret := range []string{"abc", "private-peer", "password", "key=secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal(string(raw))
		}
	}
	base := time.Unix(100, 0)
	for i := 0; i < 10; i++ {
		r := carrierTelemetryRecord{Kind: "sample", At: ringTime(base.Add(time.Duration(i) * time.Second)), SIP: &sipLiveSnapshot{State: "healthy"}}
		if retainCarrierSample(r) != (i%5 == 0) {
			t.Fatal(i)
		}
		r.Kind = "incident"
		if !retainCarrierSample(r) {
			t.Fatal("lost incident")
		}
	}
}

type carrierMetadataPlatform struct{ *answerPlatform }

func (p *carrierMetadataPlatform) ExecuteIntegrationToolContext(context.Context, int64, string, map[string]any) (*sdk.ExecuteResult, error) {
	return &sdk.ExecuteResult{Status: 422, Success: false, Data: json.RawMessage(`{"errors":[{"code":"connection_failed"}]}`), Headers: map[string]string{"Request-Id": "request-123", "Authorization": "sensitive-header", "Location": "https://private?token=secret"}}, nil
}
func TestCarrierEvidenceCommandSafeResultMetadata(t *testing.T) {
	p := &carrierMetadataPlatform{&answerPlatform{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	evidence := &carrierCommandObservation{}
	request := context.WithValue(t.Context(), carrierCommandObservationKey{}, evidence)
	_, err := executeCarrierTool(ctx, 10, "start_streaming", map[string]any{}, request)
	if err == nil || evidence.Status != 422 || evidence.RequestID != "request-123" {
		t.Fatal(evidence, err)
	}
}

func TestCarrierEvidenceGracefulShutdownRetainsTrigger(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "shutdown-evidence", "telnyx")
	_, err := a.claimCarrierBridge(row, t.Context(), "shutdown-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.OnUnmount(ctx); err != nil {
		t.Fatal(err)
	}
	history, _, err := a.db().carrierTransportEvidence(t.Context(), row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range history {
		if r.Event != nil && r.Event.Kind == "local_close_requested" && r.Event.Trigger == "application_shutdown" {
			found = true
		}
	}
	if !found {
		t.Fatal("graceful shutdown lost queued evidence", history)
	}
	a.carrierTelemetry.flushMu.Lock()
	started := time.Now()
	err = a.flushCarrierTelemetryOnShutdown()
	a.carrierTelemetry.flushMu.Unlock()
	if !errors.Is(err, errCarrierTelemetryBusy) || time.Since(started) > 100*time.Millisecond {
		t.Fatal("shutdown blocked on collector", err)
	}
}

func TestCarrierEvidenceTimedOutBatchRetainsAndRetries(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "batch-retry", "telnyx")
	for i := 0; i < 20; i++ {
		a.carrierTelemetry.enqueue(carrierTelemetryRecord{ProjectID: row.ProjectID, CallID: row.ID, Kind: "event", At: ringTime(time.Now())})
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := a.carrierTelemetry.flush(ctx, a.db(), time.Now()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(a.carrierTelemetry.pending) != 20 || a.carrierTelemetry.nextBatch != 10 {
		t.Fatalf("lost batch or no backoff: pending=%d next=%d", len(a.carrierTelemetry.pending), a.carrierTelemetry.nextBatch)
	}
	if _, err := a.carrierTelemetry.flush(t.Context(), a.db(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(a.carrierTelemetry.pending) != 10 || a.carrierTelemetry.nextBatch != 18 {
		t.Fatal("retry did not commit the reduced batch")
	}
	flushCarrierTest(t, a)
	var count int
	if err := a.db().db.QueryRow(`SELECT count(*) FROM telephony_carrier_transport_history WHERE call_id=?`, row.ID).Scan(&count); err != nil || count != 20 {
		t.Fatalf("lost/duplicated evidence: %d %v", count, err)
	}
}
