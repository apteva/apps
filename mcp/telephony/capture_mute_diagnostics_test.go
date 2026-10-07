package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
)

func TestCaptureMuteSeparatesExactOmissionsFromRealLoss(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ranges    []captureMutedRange
		next      uint32
		wantGap   int
		wantMuted uint64
		wantFirst uint64
	}{
		{"unreported-loss-remains", nil, 25, 14, 0, 11},
		{"mute-only", []captureMutedRange{{11, 20}}, 21, 0, 10, 0},
		{"mute-then-real-loss", []captureMutedRange{{11, 20}}, 25, 4, 10, 21},
		{"real-loss-around-mute", []captureMutedRange{{12, 14}, {17, 19}}, 22, 5, 6, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &websocketWriterPump{}
			h := &softphoneHub{}
			h.setBrowser(w)
			h.observeCaptureFrame(10)
			for _, r := range tc.ranges {
				h.observeMutedCaptureRange(w, r.First, r.Last, uint64(r.Last-r.First)+1, "connection")
			}
			h.observeCaptureFrame(tc.next, "connection")
			n, events := h.captureDiagnostics()
			if n != tc.wantGap || h.captureMutedFrames != tc.wantMuted {
				t.Fatal(n, h.captureMutedFrames)
			}
			if n > 0 && (len(events) != 1 || events[0].DurationMS != n*20 || events[0].Sequence != tc.wantFirst) {
				t.Fatal(events)
			}
			if n == 0 && len(events) != 0 {
				t.Fatal("intentional mute reported as lost audio", events)
			}
		})
	}
}

func TestCaptureMuteMetadataIsBoundedIdempotentAndConnectionScoped(t *testing.T) {
	w, replacement := &websocketWriterPump{}, &websocketWriterPump{}
	h := &softphoneHub{}
	h.setBrowser(w)
	h.observeCaptureFrame(0)
	for i := uint32(1); i <= 1000; i += 10 {
		h.observeMutedCaptureRange(w, i, i+9, 10, "first")
		h.observeMutedCaptureRange(w, i, i+9, 10, "first")
	}
	if h.captureMutedFrames != 1000 || len(h.captureMutedRanges) != 1 || len(h.captureMutedEvents) != 32 {
		t.Fatal(h.captureMutedFrames, len(h.captureMutedRanges), len(h.captureMutedEvents))
	}
	h.observeCaptureFrame(1001)
	h.observeMutedCaptureRange(w, 1, 1000, 1000, "first") // Replay after consumption.
	for _, r := range []struct {
		first, last uint32
		frames      uint64
	}{{1002, 1001, 0}, {1002, 1010, 1}, {1002, 1001003, 1000002}} {
		h.observeMutedCaptureRange(w, r.first, r.last, r.frames, "first")
	}
	if n, _ := h.captureDiagnostics(); n != 0 || h.captureMutedFrames != 1000 {
		t.Fatal(n, h.captureMutedFrames)
	}
	h.setBrowser(replacement)
	h.observeCaptureFrame(0)
	h.observeMutedCaptureRange(w, 1, 10, 10, "stale")
	h.observeCaptureFrame(11)
	if n, _ := h.captureDiagnostics(); n != 10 {
		t.Fatal("stale socket hid new connection loss", n)
	}
	h.observeMutedCaptureRange(replacement, 12, 21, 10, "replacement")
	h.observeCaptureFrame(22)
	if n, _ := h.captureDiagnostics(); n != 10 || h.captureMutedFrames != 1010 {
		t.Fatal(n, h.captureMutedFrames)
	}
	s := h.serverAudioSnapshot()
	if s.CaptureMutedMS != 20200 || s.CaptureMutedEvents[len(s.CaptureMutedEvents)-1].ConnectionID != "replacement" {
		t.Fatal(s.CaptureMutedEvents)
	}
}

func TestCaptureMuteWireMetadataKeepsPCMAndCarrierIndependent(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	pcm := pcm16ToBytes([]int16{300, -300, 1800, -1800})
	send := func(sequence uint32) {
		frame := make([]byte, 16+len(pcm))
		binary.LittleEndian.PutUint32(frame, softphoneAudioFrameMagic)
		binary.LittleEndian.PutUint32(frame[4:], sequence)
		copy(frame[16:], pcm)
		if err := wsutil.WriteClientBinary(browser, frame); err != nil {
			t.Fatal(err)
		}
		if got := readBinaryWithin(t, peer, time.Second); !bytes.Equal(got, pcm) {
			t.Fatal("microphone PCM changed")
		}
	}
	send(10)
	for i := 0; i < 2; i++ {
		if err := wsutil.WriteClientText(browser, []byte(`{"type":"capture.omitted","reason":"muted","first_sequence":11,"last_sequence":110,"frames":100}`)); err != nil {
			t.Fatal(err)
		}
	}
	// Caller playback remains independent while microphone transmission stops.
	if err := wsutil.WriteClientBinary(peer, pcm); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, browser, time.Second); !bytes.Equal(got, pcm) {
		t.Fatal("mute diagnostics altered caller audio")
	}
	send(111)
	send(113) // One real unreported omission must still count.
	h := app.softphones.lookup("call-soft-1")
	n, _ := h.captureDiagnostics()
	s := h.serverAudioSnapshot()
	if n != 1 || s.CaptureMutedFrames != 100 || s.CaptureMutedMS != 2000 {
		t.Fatal(n, s.CaptureMutedFrames, s.CaptureMutedMS)
	}
	_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := wsutil.ReadServerData(peer); err == nil {
		t.Fatal("diagnostic metadata leaked to carrier")
	}
}

func TestCaptureMuteDashboardIsInformational(t *testing.T) {
	b := browserAudioDiagnostics{Timing: &browserAudioTiming{}, Server: &serverAudioDiagnostics{CaptureMutedFrames: 100, CaptureMutedMS: 2000}}
	b.Timing.Transport.CaptureMutedFrames = 100
	b.Timing.Transport.CaptureMutedMS = 2000
	row := testCall("muted-call", "in-progress")
	raw, _ := json.Marshal(b)
	row.BrowserAudioDiagnostics = string(raw)
	s := summarizeAudioDashboard(&row)
	if len(s.Issues) != 0 || s.Metrics["capture_muted_ms"] != 2000 {
		t.Fatal(s)
	}
}
