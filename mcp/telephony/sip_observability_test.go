package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

func TestSIPTelemetrySequenceWrapReorderAndDTX(t *testing.T) {
	var o sipRTPObservations
	now := time.Now()
	for i, seq := range []uint16{65534, 0, 65535, 0, 1} {
		o.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: uint32(160 * i), SSRC: 1}}, 172, now.Add(time.Duration(i)*20*time.Millisecond), true)
	}
	s := o.snapshot(now.Add(time.Second))
	if s.Packets != 5 || s.SequenceGaps != 1 || s.SequenceLoss != 0 || s.Duplicates != 1 || s.OutOfOrder != 1 {
		t.Fatal(s)
	}
	var dtx sipRTPObservations
	dtx.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1, Timestamp: 160, SSRC: 2}}, 172, now, true)
	dtx.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2, Timestamp: 80160, SSRC: 2}}, 172, now.Add(10*time.Second), true)
	s = dtx.snapshot(now.Add(10 * time.Second))
	if s.MaxGapMS != 10000 || s.MaxDeliveryExcessMS != 0 || s.JitterMS != 0 {
		t.Fatal("ordinary DTX treated as delayed delivery", s)
	}
}
func TestSIPTelemetryDoesNotWaitForSamplerOrDatabase(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "sip-no-wait", "telnyx")
	d := a.newSIPDiagnostics(row, "dialog")
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU"}}
	d.attach(m, "dialog")
	m.observations.timingMu.Lock()
	j := newRTPJitterBuffer()
	m.jitterTelemetry.Store(j)
	j.mu.Lock()
	pacer := &sipRTPPacer{}
	m.pacer.Store(pacer)
	pacer.diagnostics.mu.Lock()
	writer := &websocketWriterPump{}
	m.coreForwardTelemetry.Store(writer)
	writer.audioMu.Lock()
	a.carrierTelemetry.flushMu.Lock()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			m.observations.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), SSRC: 1}}, 172, time.Now(), true)
		}
		_ = d.sample(time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RTP telemetry waited for reporting lock")
	}
	a.carrierTelemetry.flushMu.Unlock()
	writer.audioMu.Unlock()
	pacer.diagnostics.mu.Unlock()
	j.mu.Unlock()
	m.observations.timingMu.Unlock()
	if m.observations.packets.Load() != 10000 || m.observations.skipped.Load() != 10000 {
		t.Fatal("lost packet totals or confidence")
	}
	d.end("local", nil)
}
func TestSIPTelemetrySecurityAndExpectedCleanup(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "sip-security", "telnyx")
	offer, key := sipAuditSecureOffer(t)
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		t.Fatal(err)
	}
	d := a.newSIPDiagnostics(row, "secure-dialog")
	m := &sipRTPMedia{offer: offer, security: security}
	d.attach(m, "secure-dialog")
	sender, err := srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		t.Fatal(err)
	}
	packet := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, PayloadType: 0, Timestamp: 160, SSRC: 10}, Payload: bytes.Repeat([]byte{0xff}, 160)}
	raw, _ := packet.Marshal()
	encrypted, err := sender.EncryptRTP(nil, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.decodePacket(encrypted); err != nil {
		t.Fatal(err)
	}
	if _, err = m.decodePacket(encrypted); err == nil {
		t.Fatal("accepted replay")
	}
	packet.SequenceNumber = 2
	raw, _ = packet.Marshal()
	encrypted, _ = sender.EncryptRTP(nil, raw, nil)
	encrypted[len(encrypted)-1] ^= 1
	if _, err = m.decodePacket(encrypted); err == nil {
		t.Fatal("accepted forged authentication tag")
	}
	s := d.snapshot(time.Now())
	if s.RTP.Packets != 1 || s.RTP.ReplayErrors != 1 || s.RTP.AuthErrors != 1 || s.State != "audio_degraded" {
		t.Fatal(s)
	}
	m.remoteMode.Store(2)
	s = d.snapshot(time.Now().Add(time.Minute))
	if s.State != "inactive" || s.ReceiveExpectation != "intentional_hold" {
		t.Fatal(s)
	}
	d.end("carrier", nil)
	prior := m.observations.readErrors.Load()
	m.observedReadError(net.ErrClosed)
	if m.observations.readErrors.Load() != prior {
		t.Fatal("cleanup counted as live carrier failure")
	}
	flushCarrierTest(t, a)
	history, latest, err := a.db().carrierTransportEvidence(t.Context(), row.ProjectID, row.ID)
	if err != nil || latest == nil || latest.SIP.State != "ended" {
		t.Fatal(latest, err)
	}
	found := false
	for _, r := range history {
		if r.Event != nil && r.Event.Kind == "transport_read_error" {
			found = r.Event.AfterLocalCancel
		}
	}
	if !found {
		t.Fatal("missing after-shutdown socket evidence")
	}
}
func TestSIPTelemetryLiveBeforeBrowserAndDialogEvents(t *testing.T) {
	f := newSIPDialogFixture(t, false, time.Second)
	_ = f.answer(t)
	deadline := time.Now().Add(time.Second)
	for {
		s := f.session.diagnostics.snapshot(time.Now())
		if s.Signal.ACKAt != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ACK telemetry missing")
		}
		time.Sleep(time.Millisecond)
	}
	d := f.session.diagnostics
	// No browser attaches; carrier/local IVR still produces live diagnostics.
	f.session.answerMu.Lock()
	m := f.session.media
	f.session.answerMu.Unlock()
	raw, _ := (&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 160, PayloadType: 0, SSRC: 3}, Payload: bytes.Repeat([]byte{0xff}, 160)}).Marshal()
	if _, err := m.decodePacket(raw); err != nil {
		t.Fatal(err)
	}
	f.send(t, "UPDATE", 2, "", "Session-Expires: 90;refresher=uac\r\n")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	d.event("sip_refresh_result", "carrier", "Request Pending", 491)
	f.app.carrierTelemetry.sample(time.Now())
	flushCarrierTest(t, f.app)
	v := dashboardTestResult(t, f.app, "?call_id="+f.call.ID)
	live := v["carrier_live"].(map[string]any)["sip"].(map[string]any)
	signal := live["signaling"].(map[string]any)
	if signal["invite_at"] == "" || signal["answer_at"] == "" || signal["ack_at"] == "" || signal["refresh_successes"] != float64(1) || signal["refresh_failures"] != float64(0) {
		t.Fatal(signal)
	}
	if live["rtp"].(map[string]any)["packets_received"] != float64(1) {
		t.Fatal(live)
	}
	if err := f.app.db().refreshAudioDashboard(t.Context()); err != nil {
		t.Fatal(err)
	}
	var summary string
	if err := f.app.db().db.QueryRow(`SELECT summary_json FROM telephony_audio_reports WHERE call_id=?`, f.call.ID).Scan(&summary); err != nil {
		t.Fatal("IVR not visible without browser", err)
	}
	var parsed audioDashboardSummary
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.CarrierTransport != "sip" || parsed.Boundaries["browser"].State != "unobserved" {
		t.Fatal(parsed)
	}
	f.send(t, "BYE", 3, "", "Reason: Q.850;cause=16;text=private-secret\r\n")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	f.waitEnded(t)
	s := d.snapshot(time.Now())
	if s.Signal.BYEInitiator != "carrier" || s.Signal.EndReason != "Q.850;cause=16" || s.State != "ended" {
		t.Fatal(s)
	}
}
func TestSIPTelemetryConcurrentSamplingAndMedia(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "sip-concurrent", "telnyx")
	d := a.newSIPDiagnostics(row, "concurrent-dialog")
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU"}}
	d.attach(m, "concurrent-dialog")
	j := newRTPJitterBuffer()
	m.jitterTelemetry.Store(j)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			m.observations.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 3}}, 172, time.Now(), true)
			j.push(uint16(i), make([]byte, 160))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			j.pop()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			a.carrierTelemetry.sample(time.Now())
			d.event("sip_refresh_attempt", "local", "INVITE", 0)
		}
	}()
	wg.Wait()
	d.end("local", errors.New("application_shutdown"))
	flushCarrierTest(t, a)
	if m.observations.packets.Load() != 1000 {
		t.Fatal("sampling lost packet totals")
	}
}
func TestSIPTelemetrySeparatesCarrierBrowserInternal(t *testing.T) {
	s := audioDashboardSummary{Metrics: map[string]float64{}, Health: audioHealthSnapshot{State: "healthy"}, Boundaries: map[string]audioHealthStage{"browser": {State: "healthy"}}}
	addCarrierAudioSummary(&s, &carrierTelemetryRecord{At: ringTime(time.Now()), SIP: &sipLiveSnapshot{State: "audio_degraded", RTP: sipRTPSnapshot{AuthErrors: 1}, Jitter: sipJitterSnapshot{DroppedMS: 20}}})
	if s.Boundaries["browser"].State != "healthy" || s.Boundaries["carrier"].State != "audio_degraded" || !containsString(s.Issues, "carrier_error") || containsString(s.Issues, "browser_error") {
		t.Fatal(s)
	}
}

func TestSIPTelemetryDelayedDeliveryDistinctFromSilence(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "sip-delayed-arrival", "telnyx")
	d := a.newSIPDiagnostics(row, "delayed-dialog")
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU"}}
	d.attach(m, "delayed-dialog")
	now := time.Now()
	m.observations.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1, Timestamp: 160, SSRC: 2}}, 172, now, true)
	// A ten-second DTX interval has the corresponding RTP clock advance.
	m.observations.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2, Timestamp: 80160, SSRC: 2}}, 172, now.Add(10*time.Second), true)
	if d.snapshot(now.Add(10*time.Second)).State != "healthy" {
		t.Fatal("DTX alarm")
	}
	// A ten-second delivery pause with only 20ms of source clock advance is distinct.
	m.observations.received(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3, Timestamp: 80320, SSRC: 2}}, 172, now.Add(20*time.Second), true)
	sample := d.sample(now.Add(20 * time.Second))
	if sample.Kind != "incident" || sample.SIP.State != "audio_degraded" || sample.SIP.RTP.MaxDeliveryExcessMS != 9980 {
		t.Fatal(sample)
	}
	if d.snapshot(now.Add(26*time.Second)).State != "healthy" {
		t.Fatal("historical delay labelled live indefinitely")
	}
	d.end("local", nil)
}

func TestSIPTelemetrySkippedObservationIsNotPacketLoss(t *testing.T) {
	var o sipRTPObservations
	now := time.Now()
	packet := func(sequence uint16) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{SequenceNumber: sequence, Timestamp: uint32(sequence) * 160, SSRC: 1}}
	}
	o.received(packet(1), 172, now, true)
	o.timingMu.Lock()
	for i := uint16(2); i <= 10; i++ {
		o.received(packet(i), 172, now.Add(time.Duration(i)*20*time.Millisecond), true)
	}
	o.timingMu.Unlock()
	o.received(packet(11), 172, now.Add(220*time.Millisecond), true)
	s := o.snapshot(now.Add(time.Second))
	if s.Packets != 11 || s.Skipped != 9 || s.SequenceLoss != 0 || s.SequenceGaps != 0 {
		t.Fatal("reporting contention fabricated carrier loss", s)
	}
}

func TestCarrierTelemetryFreshSampleDoesNotRefreshBrowserEvidence(t *testing.T) {
	b := browserAudioDiagnostics{ReceivedAt: ringTime(time.Now().Add(-time.Hour)), ConnectionState: "connected", AudioContextState: "running"}
	s := audioDashboardSummary{Metrics: map[string]float64{}, Health: audioHealthSnapshot{State: "audio_degraded"}, Boundaries: audioBoundarySummary(b)}
	addCarrierAudioSummary(&s, &carrierTelemetryRecord{At: ringTime(time.Now()), SIP: &sipLiveSnapshot{State: "healthy"}})
	if s.Boundaries["browser"].State != "stale" || s.Boundaries["carrier"].State != "healthy" || s.Health.State == "audio_degraded" {
		t.Fatal("carrier sample laundered old browser evidence", s)
	}
}
