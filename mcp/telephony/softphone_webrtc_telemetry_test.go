package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestRTPSendTelemetryBoundedAndNonblocking(t *testing.T) {
	d := &rtcSendTelemetry{connectionID: "browser-1", ssrc: 42}
	d.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			err := writeObservedRTP(func(p *rtp.Packet) error {
				if p.SequenceNumber != uint16(i) {
					panic("modified packet")
				}
				return nil
			}, &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i * 960)}, Payload: []byte{1}}, d)
			if err != nil {
				panic(err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		d.mu.Unlock()
		t.Fatal("audio waited for diagnostics lock")
	}
	snapshots := make(chan rtcSendSnapshot, 1)
	go func() { snapshots <- d.snapshot() }()
	select {
	case s := <-snapshots:
		if !s.SamplesUnavailable || s.Attempts != 1000 {
			d.mu.Unlock()
			t.Fatal("busy snapshot did not retain counters")
		}
	case <-time.After(time.Second):
		d.mu.Unlock()
		t.Fatal("snapshot waited for packet collector")
	}
	d.mu.Unlock()
	s := d.snapshot()
	if s.Attempts != 1000 || s.ObservationsSkipped != 1000 || len(s.Recent) != 0 {
		t.Fatalf("skipped observations not accounted: %+v", s)
	}
	for i := 0; i < 1000; i++ {
		d.observe(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i * 960)}}, time.Now(), 11*time.Millisecond, 1, nil)
	}
	s = d.snapshot()
	if len(s.Recent) != 64 || len(s.Incidents) != 16 || s.Recent[0].Sequence != 936 || s.Recent[63].Sequence != 999 || s.Errors != 0 || s.MaxWriteUS != 11000 {
		t.Fatalf("unbounded or incorrect samples: %+v", s)
	}
	s.Recent[0].Sequence = 0
	if d.snapshot().Recent[0].Sequence != 936 {
		t.Fatal("snapshot aliases telemetry ring")
	}
}
func TestRTPSendTelemetryErrorsPacketIdentityAndMerge(t *testing.T) {
	d := &rtcSendTelemetry{connectionID: "browser-2", ssrc: 4242}
	d.pathRevision.Store(3)
	p := &rtp.Packet{Header: rtp.Header{SequenceNumber: 65535, Timestamp: 0xfffffc00}, Payload: []byte{1, 2, 3}}
	err := writeObservedRTP(func(got *rtp.Packet) error {
		if got != p {
			t.Fatal("packet replaced")
		}
		return errors.New("secret token must not be copied")
	}, p, d)
	if err == nil {
		t.Fatal("send error swallowed")
	}
	s := d.snapshot()
	e := s.Incidents[0]
	if s.Attempts != 1 || s.Errors != 1 || e.ConnectionID != "browser-2" || e.SSRC != 4242 || e.PathRevision != 3 || e.Sequence != 65535 || e.RTPTimestamp != p.Timestamp || e.At.IsZero() || e.Outcome != "send_error" || e.PayloadBytes != 3 {
		t.Fatalf("packet evidence missing: %+v", s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("raw error copied")
	}
	d.observe(p, time.Now(), time.Millisecond, 3, net.ErrClosed)
	if d.snapshot().Recent[1].Outcome != "connection_closed" {
		t.Fatal("close error not classified")
	}
	d.observe(p, time.Now(), time.Millisecond, 3, context.DeadlineExceeded)
	if d.snapshot().Recent[2].Outcome != "send_timeout" {
		t.Fatal("timeout not classified")
	}
	merged := mergeRTCSends(s, s)
	merged.Recent[0].SSRC = 0
	if s.Recent[0].SSRC != 4242 {
		t.Fatal("reconnect summary aliases retained packet evidence")
	}
	if merged.Attempts != 2 || merged.Errors != 2 || len(merged.Recent) != 2 {
		t.Fatal("reconnect evidence lost")
	}
}
func TestRTCSelectedMediaEndpointPrivateAndScoped(t *testing.T) {
	for _, ip := range []string{"203.0.113.19", "2001:db8::19"} {
		t.Run(ip, func(t *testing.T) {
			d := &rtcSendTelemetry{connectionID: "browser-path"}
			var c audioNetworkCollector
			base := audioNetworkEvent{CallID: "call", ProjectID: "project-a", AdviserIdentity: phoneTestIdentity("alice"), ClientIP: "192.0.2.10", AddressSource: "trusted_proxy", Retention: time.Hour}
			pair := &webrtc.ICECandidatePair{Local: &webrtc.ICECandidate{Address: "127.0.0.1", Port: 19000, Protocol: webrtc.ICEProtocolUDP, Typ: webrtc.ICECandidateTypeHost}, Remote: &webrtc.ICECandidate{Address: ip, Port: 20000, Typ: webrtc.ICECandidateTypeRelay}}
			for i := 0; i < 12; i++ {
				d.selectedPath(pair, base, ip, c.enqueue)
			}
			s := d.snapshot()
			if len(s.Paths) != 8 || s.Paths[0].Revision != 5 || s.Paths[7].Revision != 12 {
				t.Fatal("path history not bounded")
			}
			raw, _ := json.Marshal(s)
			if strings.Contains(string(raw), ip) {
				t.Fatal("raw IP leaked into ordinary diagnostics")
			}
			e := c.pending[0]
			if e.ProjectID != base.ProjectID || e.AdviserIdentity != base.AdviserIdentity || e.ConnectionID != d.connectionID || e.ClientIP != base.ClientIP || e.MediaPath.RemoteIP != ip || e.MediaPath.RemotePort != 20000 || e.MediaPath.RemoteCandidate != "relay" || e.MediaPath.AddressSource != "ice_selected_remote" || e.MediaPath.RemoteClassification != "known_vpn_exit" {
				t.Fatalf("wrong selected media path: %+v", e)
			}
			d.selectedPath(nil, base, ip, c.enqueue)
			if d.pathRevision.Load() != 12 {
				t.Fatal("fabricated selected path")
			}
		})
	}
	softphoneTestCtx(t)
	a := &App{installID: 42}
	row := phoneTestCall(t, a, "rtc-private-network", "in-progress")
	var c audioNetworkCollector
	d := &rtcSendTelemetry{connectionID: "rtc-private"}
	base := newAudioNetworkContext(&row, phoneTestIdentity("alice"), httptest.NewRequest("GET", "http://local/", nil), nil)
	d.selectedPath(&webrtc.ICECandidatePair{Local: &webrtc.ICECandidate{Protocol: webrtc.ICEProtocolUDP, Typ: webrtc.ICECandidateTypeHost}, Remote: &webrtc.ICECandidate{Address: "203.0.113.19", Port: 20000, Typ: webrtc.ICECandidateTypeSrflx}}, base, "", c.enqueue)
	if _, err := c.flush(context.Background(), a.db(), time.Now()); err != nil {
		t.Fatal(err)
	}
	events, err := a.db().browserNetworkEvents(context.Background(), row.ProjectID, row.ID, time.Now())
	if err != nil || len(events) != 1 || events[0].MediaPath.RemoteIP != "203.0.113.19" {
		t.Fatal("media endpoint not persisted", err)
	}
	if other, _ := a.db().browserNetworkEvents(context.Background(), "other", row.ID, time.Now()); len(other) != 0 {
		t.Fatal("cross-project endpoint")
	}
	identity := phoneTestIdentity("alice")
	if response := phoneTestRequest(a, &identity, "GET", "/audio-health?call_id="+row.ID, nil); response.Code != 403 {
		t.Fatal("endpoint exposed to delegated user")
	}
}

// Exercises the actual optional backbone: signaling, ICE, DTLS, SRTP, Opus and
// Telephony's paced sender. A busy telemetry snapshot cannot stop RTP delivery.
func TestRTCBackbonePacketEvidenceMatchesWireWithBusyTelemetry(t *testing.T) {
	ready := make(chan *rtcHubConn, 1)
	setupErr := make(chan error, 1)
	paths := make(chan audioNetworkEvent, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, read, err := upgradeBuffered(w, r)
		if err != nil {
			setupErr <- err
			return
		}
		hub, stop, err := connectSoftphoneRTC(c, read, softphoneRTCConfig{Enabled: true, Bitrate: 32000, NetworkContext: audioNetworkEvent{ConnectionID: "rtc-real", CallID: "call", ProjectID: "p", Retention: time.Hour}, CollectNetwork: func(e audioNetworkEvent) {
			select {
			case paths <- e:
			default:
			}
		}})
		if err != nil {
			setupErr <- err
			return
		}
		defer stop()
		rtc := hub.(*rtcHubConn)
		ready <- rtc
		// Keep the media handler alive until the test closes its signaling socket.
		var b [1]byte
		_, _ = rtc.Read(b[:])
	}))
	defer server.Close()
	api, err := softphoneRTCAPI(softphoneRTCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	packets := make(chan *rtp.Packet, 128)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				p, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				select {
				case packets <- p:
				default:
				}
			}
		}()
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("gather timeout")
	}
	signal := dialWS(t, server.URL)
	defer signal.Close()
	data, _ := json.Marshal(map[string]string{"type": "webrtc.offer", "sdp": pc.LocalDescription().SDP})
	if err = wsutil.WriteClientText(signal, data); err != nil {
		t.Fatal(err)
	}
	_ = signal.SetReadDeadline(time.Now().Add(5 * time.Second))
	var answer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	for {
		data, _, err = wsutil.ReadServerData(signal)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &answer); err != nil {
			t.Fatal(err)
		}
		if answer.Type == "webrtc.answer" {
			break
		}
		if answer.Type != "webrtc.config" {
			t.Fatal("unexpected signaling event", answer.Type)
		}
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Fatal(err)
	}
	var rtc *rtcHubConn
	select {
	case rtc = <-ready:
	case err = <-setupErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("backbone setup timeout")
	}
	defer rtc.Close()
	var received *rtp.Packet
	select {
	case received = <-packets:
	case <-time.After(3 * time.Second):
		t.Fatal("no SRTP packet")
	}
	time.Sleep(10 * time.Millisecond) // Receiver delivery may precede completion accounting.
	s := rtc.stats.snapshot().RTP
	found := false
	for _, e := range s.Recent {
		if e.Sequence == received.SequenceNumber && e.RTPTimestamp == received.Timestamp && e.SSRC == received.SSRC && e.ConnectionID == "rtc-real" && e.Outcome == "sent" {
			found = true
		}
	}
	if !found {
		t.Fatal("retained send evidence does not match wire", s)
	}
	select {
	case e := <-paths:
		if net.ParseIP(e.MediaPath.RemoteIP) == nil || e.MediaPath.RemotePort == 0 || e.ConnectionID != "rtc-real" {
			t.Fatal("missing real selected ICE endpoint", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no selected path event")
	}
	rtc.stats.rtp.mu.Lock()
	before := rtc.stats.rtp.skipped.Load()
	// Ignore packets already queued before holding the telemetry lock.
	for len(packets) > 0 {
		<-packets
	}
	for i := 0; i < 3; i++ {
		select {
		case <-packets:
		case <-time.After(time.Second):
			rtc.stats.rtp.mu.Unlock()
			t.Fatal("busy telemetry stopped media")
		}
	}
	rtc.stats.rtp.mu.Unlock()
	if rtc.stats.rtp.skipped.Load() <= before {
		t.Fatal("contended observations not skipped")
	}
}

func TestRTPTelemetryConcurrentSnapshots(t *testing.T) {
	var d rtcSendTelemetry
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5000; j++ {
				d.observe(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(j)}}, time.Now(), time.Microsecond, 0, nil)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		_ = d.snapshot()
	}
	wg.Wait()
	if d.snapshot().Attempts != 20000 {
		t.Fatal("cumulative counters lost")
	}
}

func BenchmarkRTPSendTelemetry(b *testing.B) {
	p := &rtp.Packet{Header: rtp.Header{SequenceNumber: 1, Timestamp: 960}, Payload: make([]byte, 80)}
	write := func(*rtp.Packet) error { return nil }
	b.Run("baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = write(p)
		}
	})
	b.Run("observed", func(b *testing.B) {
		var d rtcSendTelemetry
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = writeObservedRTP(write, p, &d)
		}
	})
	b.Run("busy_snapshot", func(b *testing.B) {
		var d rtcSendTelemetry
		d.mu.Lock()
		defer d.mu.Unlock()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = writeObservedRTP(write, p, &d)
		}
	})
}

func TestRTPLossDeltaWirePersistence(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	row := phoneTestCall(t, a, "rtc-loss-delta", "in-progress")
	before, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	metrics := map[string]float64{"receiver_ssrc": 4242, "receiver_loss_delta": 3, "receiver_loss_window_ms": 1000, "remote_receiver_ssrc": 1234, "remote_receiver_loss_delta": 2, "remote_receiver_loss_window_ms": 2000}
	samples := normalizeTransportSamples([]browserTransportSample{{ID: "sample-1", Timestamp: now.Format(time.RFC3339Nano), Transport: "webrtc", Reason: "incident", Metrics: metrics}}, now)
	if len(samples) != 1 || len(samples[0].Metrics) != 6 {
		t.Fatal("loss timing filtered out", samples)
	}
	b := browserAudioDiagnostics{DropEvents: []audioDropEvent{{Timestamp: now.Format(time.RFC3339Nano), Direction: "carrier_to_operator", Reason: "webrtc_packet_loss", PacketCount: 3, SSRC: 4242, WindowMS: 1000}}}
	if err := a.db().updateBrowserAudioDiagnostics(row.ID, b); err != nil {
		t.Fatal(err)
	}
	saved, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var decoded browserAudioDiagnostics
	if err = json.Unmarshal([]byte(saved.BrowserAudioDiagnostics), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.DropEvents) != 1 || decoded.DropEvents[0].PacketCount != 3 || decoded.DropEvents[0].SSRC != 4242 || decoded.DropEvents[0].WindowMS != 1000 {
		t.Fatal("loss evidence lost", decoded.DropEvents)
	}
	if saved.Status != before.Status || saved.MediaStatus != before.MediaStatus {
		t.Fatal("diagnostics modified call state")
	}
}

func BenchmarkRTPTelemetrySnapshotJSON(b *testing.B) {
	var d rtcSendTelemetry
	for i := 0; i < 100; i++ {
		d.observe(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i)}}, time.Now(), 11*time.Millisecond, 1, nil)
	}
	sample, _ := json.Marshal(d.snapshot())
	b.ReportMetric(float64(len(sample)), "json_bytes")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sample, _ = json.Marshal(d.snapshot())
	}
	_ = sample
}
