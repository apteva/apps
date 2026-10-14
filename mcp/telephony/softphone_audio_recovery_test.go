package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestRTCTailFlushPreservesSamplesAndDeadline(t *testing.T) {
	now := time.Now()
	for _, n := range []int{1, 240, 432, 479} {
		var f rtcPCMFramer
		pcm := make([]int16, n)
		for i := range pcm {
			pcm[i] = int16(123 + i)
		}
		expires := now.Add(250 * time.Millisecond)
		f.push(pcm16ToBytes(pcm), expires, func(rtcPCMFrame) bool { t.Fatal("partial emitted early"); return false })
		frame, padding := f.flushPartial(now.Add(20 * time.Millisecond))
		if len(frame.pcm) != 480 || padding != 480-n || !frame.expires.Equal(expires) || !bytes.Equal(pcm16ToBytes(frame.pcm[:n]), pcm16ToBytes(pcm)) {
			t.Fatal("tail changed", n, frame, padding)
		}
		if frame.pcm[479] != 0 || len(f.pending) != 0 {
			t.Fatal("tail not completed")
		}
		if repeat, _ := f.flushPartial(now); repeat.pcm != nil {
			t.Fatal("tail repeated")
		}
	}
	var expired rtcPCMFramer
	expired.push([]byte{1, 0}, now, func(rtcPCMFrame) bool { return true })
	if f, _ := expired.flushPartial(now.Add(time.Millisecond)); f.pcm != nil {
		t.Fatal("expired tail resurrected")
	}
}
func TestRTCReleaseBurstPacedAndSourceAgePreserved(t *testing.T) {
	now := time.Now()
	stats := &rtcMediaStats{}
	var q rtcAudioRelease
	// 140ms accumulation is recoverable inside the original 250ms age budget.
	for i := 0; i < 7; i++ {
		q.push([]int16{int16(i)}, now.Add(time.Duration(i)*20*time.Millisecond), stats, "operator_to_carrier")
	}
	for i := 0; i < 7; i++ {
		at := now.Add(140*time.Millisecond + time.Duration(i)*20*time.Millisecond)
		f, ok := q.pop(at, stats, "operator_to_carrier")
		if !ok || f.pcm[0] != int16(i) || !f.source.Equal(now.Add(time.Duration(i)*20*time.Millisecond)) {
			t.Fatal("fresh speech lost/reordered", i, f)
		}
		if _, ok = q.pop(at, stats, "operator_to_carrier"); ok {
			t.Fatal("burst escaped pacing")
		}
	}
	// A genuinely old packet is dropped, but the fresh packet behind it survives.
	q = rtcAudioRelease{}
	q.push(make([]int16, 480), now.Add(-time.Second), stats, "operator_to_carrier")
	q.push(make([]int16, 480), now, stats, "operator_to_carrier")
	f, ok := q.pop(now, stats, "operator_to_carrier")
	if !ok || !f.source.Equal(now) || stats.snapshot().IngressDiscardedMS != 20 {
		t.Fatal("source age not enforced", stats.snapshot())
	}
	for i := 0; i < 100; i++ {
		q.push(make([]int16, 480), now, stats, "operator_to_carrier")
	}
	if q.count != 16 {
		t.Fatal("unbounded capture reserve", q.count)
	}
}
func TestMicrophoneSourceSurvivesStartupAndPacketization(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		t.Run(direction, func(t *testing.T) {
			source := time.Now().Add(-20 * time.Millisecond)
			pcm := bytes.Repeat([]byte{123, 0}, 480)
			b := &websocketWriterPump{}
			h := &softphoneHub{browser: b, status: "answered", direction: direction}
			h.forwardMicrophoneAt(b, pcm, source, true)
			a, z := net.Pipe()
			defer a.Close()
			defer z.Close()
			writer := newWebSocketWriterPump(a, ws.StateServerSide)
			defer writer.Stop()
			h.setPeer(writer)
			z.SetReadDeadline(time.Now().Add(time.Second))
			data, _, err := wsutil.ReadServerData(z)
			if err != nil {
				t.Fatal(err)
			}
			for _, carrier := range []string{"telnyx", "twilio", "didww"} {
				got, at := carrierMicrophoneSource(&callRow{PeerKind: peerKindHuman, CarrierSlug: carrier}, data)
				if !bytes.Equal(got, pcm) || !at.Equal(source) {
					t.Fatal("handoff reset age/corrupted samples", carrier, at)
				}
			}
			ai, at := carrierMicrophoneSource(&callRow{PeerKind: peerKindRealtime}, data)
			if !bytes.Equal(ai, data) || !at.IsZero() {
				t.Fatal("AI binary protocol changed")
			}
			p := newCarrierAudioPacketizer(8000)
			if got := p.addAt(make([]int16, 144), 432, realtimeBridgeControl{}, source); len(got) != 0 {
				t.Fatal("partial emitted")
			}
			packets := p.addAt(make([]int16, 160), 480, realtimeBridgeControl{}, source.Add(20*time.Millisecond))
			if len(packets) != 1 || !packets[0].EnqueuedAt.Equal(source) {
				t.Fatal("oldest constituent age lost")
			}
			packets = p.addAt(make([]int16, 160), 480, realtimeBridgeControl{}, source.Add(40*time.Millisecond))
			if len(packets) != 1 || !packets[0].EnqueuedAt.Equal(source.Add(22*time.Millisecond)) {
				t.Fatal("consumed segment age retained", packets)
			}
		})
	}
}
func TestRTCSocketEvidenceBoundedAndSessionScoped(t *testing.T) {
	var d, other rtcSocketTelemetry
	data := make([]byte, 12)
	data[0] = 0x80
	data[1] = 111
	binary.BigEndian.PutUint32(data[8:], 123)
	now := time.Now()
	d.record(data, now.Add(-70*time.Millisecond), now.Add(-20*time.Millisecond))
	if _, ok := other.take(123, 0, now); ok {
		t.Fatal("cross-session evidence")
	}
	at, ok := d.take(123, 0, now)
	if !ok || !at.Equal(now.Add(-70*time.Millisecond)) || d.maxSocketWait.Load() != int64(50*time.Millisecond) || d.maxPionWait.Load() != int64(20*time.Millisecond) {
		t.Fatal("kernel/pion delay not separated")
	}
	if _, ok = d.take(123, 0, now); ok {
		t.Fatal("reused observation")
	}
	d.mu.Lock()
	d.record(data, now, now)
	d.mu.Unlock()
	if d.skipped.Load() != 1 {
		t.Fatal("collector blocked audio")
	}
	for i := 0; i < 10000; i++ {
		binary.BigEndian.PutUint16(data[2:], uint16(i))
		d.record(data, now, now)
	}
	if len(d.packets) != 128 {
		t.Fatal("unbounded history")
	}
	data[1] = 200
	d.record(data, now, now)
	if _, ok = d.take(123, 0, now); ok {
		t.Fatal("RTCP mistaken for RTP")
	}
}
func BenchmarkRTCSocketEvidence(b *testing.B) {
	var d rtcSocketTelemetry
	data := make([]byte, 12)
	data[0] = 0x80
	data[1] = 111
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		binary.BigEndian.PutUint16(data[2:], uint16(i))
		d.record(data, now, now)
		d.take(0, uint16(i), now)
	}
}

func TestRTCObservedUDPTransportUnchanged(t *testing.T) {
	d := &rtcSocketTelemetry{}
	network, err := newRTCObservedNet(d)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := network.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sender, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	packet := make([]byte, 32)
	packet[0] = 0x80
	packet[1] = 111
	binary.BigEndian.PutUint32(packet[8:], 123)
	sender.Write(packet)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil || !bytes.Equal(buf[:n], packet) || from.Port != sender.LocalAddr().(*net.UDPAddr).Port {
		t.Fatal("UDP wrapper changed transport", err)
	}
	if _, ok := d.take(123, 0, time.Now()); !ok {
		t.Fatal("UDP observation missing")
	}
}

func TestRTCTailReachesBrowserWithoutNextCarrierChunk(t *testing.T) {
	rtcTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	browser := rtcTestConnect(t, server.URL+"/softphone/media/call-soft-1/peer-secret?transport=webrtc")
	pcm := make([]int16, 432)
	for i := range pcm {
		pcm[i] = int16(7000 * math.Sin(float64(i)*.08))
	}
	if err := wsutil.WriteClientBinary(peer, pcm16ToBytes(pcm)); err != nil {
		t.Fatal(err)
	}
	decoder, err := newRTCOpusDecoderWithFEC(true)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	out := make([]int16, 2880)
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case payload := <-browser.caller:
			n, e := decoder.decode(payload, out)
			if e != nil {
				t.Fatal(e)
			}
			for _, v := range out[:n] {
				if v > 1000 || v < -1000 {
					stats := app.softphones.lookup("call-soft-1").browserWriter().conn.(*rtcHubConn).stats.snapshot()
					if stats.PartialFlushedFrames != 1 || stats.PartialPaddedMS != 2 || stats.OutboundDroppedMS != 0 {
						t.Fatal("tail accounting", stats)
					}
					return
				}
			}
		case <-deadline:
			t.Fatal("18ms speech tail waited for another carrier chunk or expired")
		}
	}
}

func BenchmarkRTCCaptureRelease(b *testing.B) {
	var q rtcAudioRelease
	stats := &rtcMediaStats{}
	pcm := make([]int16, 480)
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		at := now.Add(time.Duration(i) * 20 * time.Millisecond)
		q.push(pcm, at, stats, "operator_to_carrier")
		q.pop(at.Add(60*time.Millisecond), stats, "operator_to_carrier")
	}
}

func TestRTCCaptureCadenceDoesNotAccumulateTimerLateness(t *testing.T) {
	stats := &rtcMediaStats{}
	var q rtcAudioRelease
	start := time.Now()
	pcm := make([]int16, 480)
	for i := 0; i < 30000; i++ {
		source := start.Add(time.Duration(i) * 20 * time.Millisecond)
		q.push(pcm, source, stats, "operator_to_carrier")
		// A timer may wake 1ms late every time. This must not compound into
		// seconds of source age over a ten-minute healthy call.
		now := source.Add(60 * time.Millisecond)
		if q.next.After(now) {
			now = q.next
		}
		now = now.Add(time.Millisecond)
		if _, ok := q.pop(now, stats, "operator_to_carrier"); !ok {
			t.Fatal("healthy frame not released", i)
		}
		if now.Sub(source) > 63*time.Millisecond {
			t.Fatal("timer lateness accumulated", i, now.Sub(source))
		}
	}
	if stats.snapshot().IngressDiscardedMS != 0 {
		t.Fatal("healthy audio discarded")
	}
}
