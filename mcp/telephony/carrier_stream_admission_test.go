package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestCarrierStreamDocumentedErrorsAndLegacyMedia(t *testing.T) {
	for _, code := range []int{100002, 100003, 100004, 100005} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"event": "error", "stream_id": "stream-1", "payload": map[string]any{"code": code, "title": "title", "detail": "detail"}})
			var f carrierMediaFrame
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatal(err)
			}
			e := carrierFrameError("telnyx", f)
			if e.ProviderErrorCode != code || e.ProviderErrorTitle != "title" || e.ProviderErrorDetail != "detail" || e.StreamID != "stream-1" || e.Trigger != "provider_stream_error" {
				t.Fatal(e)
			}
		})
	}
	var legacy carrierMediaFrame
	if err := json.Unmarshal([]byte(`{"event":"error","error":{"code":100003,"title":"legacy","detail":"detail"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if carrierFrameError("signalwire", legacy).ProviderErrorTitle != "legacy" {
		t.Fatal("legacy provider error lost")
	}
	var bandwidth carrierMediaFrame
	if err := json.Unmarshal([]byte(`{"eventType":"media","track":"inbound","payload":"AAAA"}`), &bandwidth); err != nil {
		t.Fatal(err)
	}
	var pcm string
	if err := json.Unmarshal(bandwidth.Payload, &pcm); err != nil || pcm != "AAAA" {
		t.Fatal("Bandwidth string payload changed", err)
	}
}

type carrierStreamFixture struct {
	app    *App
	row    *callRow
	server *httptest.Server
	old    *carrierBridgeLease
	first  net.Conn
}

func newCarrierStreamFixture(t *testing.T) *carrierStreamFixture {
	t.Helper()
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "stream-admission", "telnyx")
	var handlers sync.WaitGroup
	track := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { handlers.Add(1); defer handlers.Done(); h(w, r) }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/", track(a.handlePeerSocket))
	mux.HandleFunc("/softphone/media/", track(a.handleSoftphoneMedia))
	mux.HandleFunc("/media/telnyx/", track(a.handleTelnyxMediaStream))
	server := httptest.NewServer(mux)
	parsed, _ := url.Parse(server.URL)
	t.Setenv("APTEVA_APP_PORT", parsed.Port())
	t.Cleanup(func() {
		a.stopCarrierBridges()
		a.stopRoutingDispatcher()
		server.Close()
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("media handlers did not finish")
		}
	})
	f := &carrierStreamFixture{app: a, row: row, server: server}
	f.first = dialWS(t, f.path())
	t.Cleanup(func() { _ = f.first.Close() })
	writeRecoveryTelnyxFrame(t, f.first, row, "original")
	waitStream(t, f, "original", true)
	a.mediaBridges.mu.Lock()
	f.old = a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if !f.old.expectsContinuousMedia() {
		t.Fatal("fixture does not expect continuous media")
	}
	return f
}
func (f *carrierStreamFixture) path() string {
	return f.server.URL + "/media/telnyx/" + f.row.ID + "/" + f.row.CallbackSecret
}
func waitStream(t *testing.T, f *carrierStreamFixture, stream string, flow bool) *carrierBridgeLease {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.app.mediaBridges.mu.Lock()
		b := f.app.mediaBridges.current[f.row.ID]
		f.app.mediaBridges.mu.Unlock()
		if b != nil {
			b.mu.Lock()
			sid := b.stream
			b.mu.Unlock()
			if sid == stream && (!flow || b.flowing.Load()) {
				return b
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stream not ready", stream)
	return nil
}
func candidateStart(t *testing.T, c net.Conn, call, stream string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"event": "start", "stream_id": stream, "start": map[string]any{"call_control_id": call, "media_format": map[string]any{"encoding": "L16", "sample_rate": 16000, "channels": 1}}})
	if err := wsutil.WriteClientText(c, raw); err != nil {
		t.Fatal(err)
	}
}
func TestCarrierStreamStalledReplacementActualSockets(t *testing.T) {
	f := newCarrierStreamFixture(t)
	browser := dialWS(t, f.server.URL+"/softphone/media/"+f.row.ID+"/"+f.row.PeerToken)
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", time.Second)
	f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
	candidate := dialWS(t, f.path())
	defer candidate.Close()
	candidateStart(t, candidate, f.row.CarrierSID, "replacement")
	fresh := waitStream(t, f, "replacement", false)
	if fresh == f.old || f.old.ctx.Err() == nil || fresh.ctx.Err() != nil {
		t.Fatal("ownership not transferred")
	}
	f.old.finish()
	writeRecoveryTelnyxFrame(t, candidate, f.row, "replacement")
	fresh = waitStream(t, f, "replacement", true)
	readSoftphoneEventWithin(t, browser, "peer.connected", time.Second)
	writeRecoveryTelnyxFrame(t, candidate, f.row, "replacement")
	if len(readBinaryWithin(t, browser, time.Second)) < 800 {
		t.Fatal("caller audio not resumed")
	}
	if err := wsutil.WriteClientBinary(browser, pcm16ToBytes(sinePCM(24000, 700, 480*5))); err != nil {
		t.Fatal(err)
	}
	_ = candidate.SetReadDeadline(time.Now().Add(time.Second))
	for {
		data, op, err := wsutil.ReadServerData(candidate)
		if err != nil {
			t.Fatal("microphone not resumed", err)
		}
		if op == ws.OpText && strings.Contains(string(data), `"event":"media"`) {
			break
		}
	}
	row, _ := f.app.db().findCall(f.row.ID)
	if row.ThreadID != f.row.ThreadID || row.PeerToken != f.row.PeerToken || row.Status != f.row.Status || !bridgeClaimActive(t, f.app, row.ID) {
		t.Fatal("call changed", row)
	}
	if fresh.sample(time.Now()).Carrier.MaxGapMS < 4900 {
		t.Fatal("cross-generation gap lost")
	}
}
func TestCarrierStreamCandidateCannotStealOwner(t *testing.T) {
	for _, action := range []string{"wrong_identity", "old_resumed", "hangup", "same_stream", "media_before_start"} {
		t.Run(action, func(t *testing.T) {
			f := newCarrierStreamFixture(t)
			f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
			c := dialWS(t, f.path())
			defer c.Close()
			call, stream := f.row.CarrierSID, "candidate"
			switch action {
			case "wrong_identity":
				call = "foreign-call"
			case "old_resumed":
				f.old.arrival.last.Store(time.Now().UnixNano())
			case "hangup":
				if err := f.app.db().updateStatus(f.row.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
			case "same_stream":
				stream = "original"
			case "media_before_start":
				raw, _ := json.Marshal(map[string]any{"event": "media", "stream_id": stream, "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(pcm16ToBytes(sinePCM(16000, 440, 320)))}})
				if err := wsutil.WriteClientText(c, raw); err != nil {
					t.Fatal(err)
				}
			}
			candidateStart(t, c, call, stream)
			if action == "same_stream" || action == "media_before_start" {
				fresh := waitStream(t, f, stream, false)
				deadline := time.Now().Add(time.Second)
				for fresh == f.old && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					f.app.mediaBridges.mu.Lock()
					fresh = f.app.mediaBridges.current[f.row.ID]
					f.app.mediaBridges.mu.Unlock()
				}
				if fresh == f.old {
					t.Fatal("logical stream reconnect rejected")
				}
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			_, _, err := wsutil.ReadServerData(c)
			if err == nil {
				t.Fatal("invalid candidate not closed")
			}
			f.app.mediaBridges.mu.Lock()
			owner := f.app.mediaBridges.current[f.row.ID]
			f.app.mediaBridges.mu.Unlock()
			if owner != f.old || f.old.ctx.Err() != nil {
				t.Fatal("candidate damaged existing owner")
			}
		})
	}
}
func TestCarrierStreamHealthyDuplicateAndHoldRemainProtected(t *testing.T) {
	f := newCarrierStreamFixture(t)
	if _, err := f.app.claimCarrierBridge(f.row, t.Context()); !errors.Is(err, errCarrierBridgeBusy) {
		t.Fatal("healthy duplicate admitted", err)
	}
	f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
	reception := f.old.reception.Load()
	reception.pause(true)
	if f.old.replaceable(time.Now()) {
		t.Fatal("held call considered stalled")
	}
	sample := f.old.sample(time.Now()).Carrier
	if sample.State != "inactive" {
		t.Fatal("hold flagged as audio failure", sample)
	}
	reception.pause(false)
	sample = f.old.sample(time.Now()).Carrier
	if sample.State != "audio_degraded" || sample.SocketState != "connected" || sample.LastMediaAgeMS < 4900 {
		t.Fatal("stalled media reported healthy", sample)
	}
}
func TestCarrierStreamDocumentedErrorActivatesBoundedRecovery(t *testing.T) {
	f := newCarrierStreamFixture(t)
	raw := `{"event":"error","stream_id":"original","payload":{"code":100003,"title":"malformed_frame","detail":"bad frame wss://secret.example/token?secret=yes"}}`
	if err := wsutil.WriteClientText(f.first, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.old.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("documented error ignored")
	}
	f.old.mu.Lock()
	first := f.old.first
	f.old.mu.Unlock()
	if first == nil || first.ProviderErrorCode != 100003 || first.ProviderErrorTitle != "malformed_frame" || strings.Contains(first.ProviderErrorDetail, "secret.example") {
		t.Fatal("provider evidence missing/unsanitized", first)
	}
	if f.old.arrival.decodeErrors.Load() != 0 {
		t.Fatal("documented error counted as decode failure")
	}
}
func TestCarrierStreamDecodeEvidenceIsBounded(t *testing.T) {
	f := newCarrierStreamFixture(t)
	for i := 0; i < 100; i++ {
		f.old.recordDecodeError(errors.New("invalid frame wss://private/token"))
	}
	if f.old.arrival.decodeErrors.Load() != 100 {
		t.Fatal("decode counter lost")
	}
	count := 0
	for len(f.app.carrierTelemetry.queue) > 0 {
		r := <-f.app.carrierTelemetry.queue
		if r.Event != nil && r.Event.Kind == "frame_decode_error" {
			count++
			if strings.Contains(r.Event.Detail, "private") {
				t.Fatal("credential URL leaked")
			}
		}
	}
	if count != 1 {
		t.Fatal("diagnostics not rate bounded", count)
	}
}
func TestCarrierStreamReplacementBurstBoundedAndAccounted(t *testing.T) {
	var r carrierReception
	r.begin(true)
	writer := idleAudioWriter()
	pcm := pcm16ToBytes(sinePCM(24000, 440, 480))
	for i := 0; i < 50; i++ {
		src := sourceMedia("replacement", i*20, i+1)
		mapped, drop := r.observe(src, 20, 6000+float64(i))
		if drop {
			t.Fatal("uncertain first batch falsely called stale")
		}
		writer.queueAudio(encodeSourceAudio(pcm, src, mapped, 6000+float64(i), r.s.Epoch), sourceAudioHeaderBytes)
	}
	s := r.snapshot(6050, true)
	if s.SourceClockConfidence != "uncalibrated" || s.UncalibratedBursts != 1 || s.MaxBatchMS != 1000 {
		t.Fatal(s)
	}
	q := writer.audioSnapshot()
	if q.QueuedMS > 320 || q.EnqueuedBytes != int64(writer.audioBytes)+q.OverflowBytes {
		t.Fatal("burst queue unbounded or losses missing", q)
	}
	var last []byte
	for len(writer.audio) > 0 {
		last = (<-writer.audio).payload
	}
	if !bytes.Equal(last[sourceAudioHeaderBytes:], pcm) || binary.LittleEndian.Uint64(last[40:]) != 50 {
		t.Fatal("retained waveform changed")
	}
}

func TestCarrierStreamReplacementDatabaseFailureKeepsOriginal(t *testing.T) {
	f := newCarrierStreamFixture(t)
	f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
	_, err := f.app.db().db.Exec(`CREATE TRIGGER reject_test_replacement BEFORE UPDATE OF media_generation ON calls BEGIN SELECT RAISE(ABORT,'test claim failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	c := dialWS(t, f.path())
	defer c.Close()
	candidateStart(t, c, f.row.CarrierSID, "replacement")
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = wsutil.ReadServerData(c)
	if err == nil {
		t.Fatal("failed database claim admitted")
	}
	if f.old.ctx.Err() != nil || f.old.replaced.Load() {
		t.Fatal("claim failure canceled original")
	}
	f.app.mediaBridges.mu.Lock()
	owner := f.app.mediaBridges.current[f.row.ID]
	f.app.mediaBridges.mu.Unlock()
	if owner != f.old {
		t.Fatal("claim failure changed ownership")
	}
}
func TestCarrierStreamCandidateTimeoutIsBounded(t *testing.T) {
	f := newCarrierStreamFixture(t)
	f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
	started := time.Now()
	c := dialWS(t, f.path())
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(carrierReplacementStartTimeout + time.Second))
	_, _, err := wsutil.ReadServerData(c)
	if err == nil {
		t.Fatal("idle candidate remained open")
	}
	if time.Since(started) > carrierReplacementStartTimeout+time.Second || f.old.ctx.Err() != nil {
		t.Fatal("unbounded candidate or original was canceled")
	}
}
func TestCarrierStreamSnapshotUpdatesHealthWithoutBrowserReport(t *testing.T) {
	f := newCarrierStreamFixture(t)
	f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
	sample := f.old.sample(time.Now())
	summary := audioDashboardSummary{Metrics: map[string]float64{}}
	addCarrierAudioSummary(&summary, &sample)
	if summary.Health.State != "audio_degraded" || summary.Boundaries["carrier"].Reason != "reception_stalled" {
		t.Fatal(summary)
	}
	f.old.media()
	recovered := f.old.sample(time.Now())
	if recovered.Carrier.State != "healthy" {
		t.Fatal("recovery remains unhealthy", recovered.Carrier)
	}
}
func BenchmarkCarrierStreamTelnyxDecode(b *testing.B) {
	raw := []byte(`{"event":"media","sequence_number":"2","stream_id":"stream","media":{"payload":"AAAA","timestamp":"20","chunk":"1"}}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var f carrierMediaFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCarrierStreamAdmissionDoesNotWaitForReceptionReporting(t *testing.T) {
	f := newCarrierStreamFixture(t)
	f.old.arrival.last.Store(time.Now().Add(-5 * time.Second).UnixNano())
	r := f.old.reception.Load()
	r.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- f.old.replaceable(time.Now()) }()
	select {
	case allowed := <-done:
		r.mu.Unlock()
		if !allowed {
			t.Fatal("reporting falsely rejected a stalled replacement")
		}
	case <-time.After(time.Second):
		r.mu.Unlock()
		t.Fatal("reporting blocked admission")
	}
}
