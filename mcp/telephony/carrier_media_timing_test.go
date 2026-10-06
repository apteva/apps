package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestCarrierSourceTenSecondCatchupConservesSamples(t *testing.T) {
	var r carrierReception
	r.begin(true)
	for i := 0; i < 50; i++ {
		_, drop := r.observe(sourceMedia("stream", i*20, i+1), 20, float64(1000+i*20))
		if drop {
			t.Fatal("steady speech discarded")
		}
	}
	if !r.snapshot(5000, true).Stalled {
		t.Fatal("missing-media stall not detected")
	}
	kept := 0.0
	for i := 50; i < 550; i++ {
		_, drop := r.observe(sourceMedia("stream", i*20, i+1), 20, 12000)
		if !drop {
			kept += 20
		}
	}
	s := r.snapshot(12000, true)
	if s.StaleDroppedMS+kept != 10000 || s.StaleDroppedMS < 9600 || kept > 360 {
		t.Fatalf("catchup accounting: kept=%v snapshot=%+v", kept, s)
	}
	if s.Stalls != 1 || s.Recoveries != 1 || s.Stalled || s.MaxGapMS < 10000 || s.MaxBatchMS < 10000 {
		t.Fatalf("incorrect incident/recovery diagnostics: %+v", s)
	}
	if s.MaxExcessAgeMS < 10000 {
		t.Fatal("carrier source age lost")
	}
	if len(s.Events) < 2 {
		t.Fatal("incident correlation missing")
	}
}

func TestCarrierSourceMissingFramesSilenceHoldAndStreamEpoch(t *testing.T) {
	var r carrierReception
	r.begin(true)
	r.observe(sourceMedia("a", 0, 1), 20, 1000)
	// A source timestamp jump is missing/omitted media, NOT received PCM loss.
	_, drop := r.observe(sourceMedia("a", 10020, 502), 20, 11020)
	s := r.snapshot(11020, true)
	if drop || s.StaleDroppedMS != 0 || s.Stalls != 1 || s.Recoveries != 1 {
		t.Fatalf("missing-frame accounting: %+v", s)
	}
	for i := 1; i <= 200; i++ {
		r.observe(sourceMedia("a", 10020+i*20, 502+i), 20, float64(11020+i*20))
	}
	if r.snapshot(15020, true).Stalled {
		t.Fatal("continuous silent PCM must count as delivery")
	}
	r.pause(true)
	if r.snapshot(30000, true).Stalled {
		t.Fatal("hold falsely detected as failure")
	}
	r.pause(false)
	before := r.snapshot(30000, true)
	r.observe(sourceMedia("b", 0, 1), 20, 30020)
	after := r.snapshot(30020, true)
	if after.Epoch <= before.Epoch || after.StaleDroppedMS != 0 {
		t.Fatal("new stream inherited stale source timeline")
	}
	// A caller microphone has no relationship to carrierReception; repeated
	// operator input cannot reset its timer.
	if !r.snapshot(33000, true).Stalled {
		t.Fatal("caller-direction stall missing")
	}
	var dtx carrierReception
	dtx.begin(false)
	dtx.observe(carrierSource{}, 20, 1000)
	if dtx.snapshot(30000, true).Stalled {
		t.Fatal("unknown/discontinuous provider falsely diagnosed")
	}
}

func TestCarrierSourceDuplicateRestartAndInvalidTimestamp(t *testing.T) {
	var r carrierReception
	r.begin(true)
	r.observe(sourceMedia("s", 100, 10), 20, 1000)
	if _, drop := r.observe(sourceMedia("s", 100, 10), 20, 1010); !drop {
		t.Fatal("duplicate media played")
	}
	before := r.snapshot(1010, false)
	if _, drop := r.observe(sourceMedia("s", 0, 11), 20, 1020); drop {
		t.Fatal("timestamp reset did not rebase")
	}
	if r.snapshot(1020, false).Epoch <= before.Epoch {
		t.Fatal("reset epoch missing")
	}
	for _, v := range []any{nil, "", "NaN", "Inf", -1, "bad"} {
		s := sourceMedia("s", v, nil)
		if s.Timed {
			t.Fatalf("invalid timestamp accepted: %v", v)
		}
		if _, drop := r.observe(s, 20, 1040); drop {
			t.Fatal("unknown timestamp falsely classified stale")
		}
	}
}

func TestSourceMetadataBrowserNegotiationAndQueueTrim(t *testing.T) {
	pcm := bytes.Repeat([]byte{42, 0}, 24000/2)
	src := sourceMedia("s", 100, 77)
	frame := encodeSourceAudio(pcm, src, 1000, 1010, 3)
	p := idleAudioWriter()
	h := &softphoneHub{browser: p, framedBrowser: p, framedVersion: 3}
	h.toBrowser(2, frame)
	got := <-p.audio
	if got.pcmBytes != liveAudioMaxBytes || sourceAudioHeader(got.payload) != 64 {
		t.Fatal("PCM budget includes metadata")
	}
	trimMS := float64(len(pcm)-liveAudioMaxBytes) * 1000 / 48000
	if math.Float64frombits(binary.LittleEndian.Uint64(got.payload[48:])) != 1000+trimMS {
		t.Fatal("trim did not advance source timeline")
	}
	if binary.LittleEndian.Uint64(got.payload[40:]) != 77 || binary.LittleEndian.Uint32(got.payload[56:]) != 3 {
		t.Fatal("source identity lost")
	}
	for _, version := range []int{0, 2} {
		w := idleAudioWriter()
		h := &softphoneHub{browser: w, framedVersion: version}
		if version == 2 {
			h.framedBrowser = w
		}
		h.toBrowser(2, encodeSourceAudio(pcm[:960], src, 1000, 1010, 3))
		out := <-w.audio
		header := sourceAudioHeader(out.payload)
		if !bytes.Equal(out.payload[header:], pcm[:960]) {
			t.Fatalf("legacy v%d PCM changed", version)
		}
		if version == 2 && header != 32 || version == 0 && header != 0 {
			t.Fatal("legacy framing changed")
		}
	}
}

func TestCarrierDeliveryNoticesDoNotGateMicrophone(t *testing.T) {
	w := idleAudioWriter()
	h := &softphoneHub{browser: w, readyBrowser: w, carrierForward: idleAudioWriter(), status: "answered"}
	h.reception.begin(true)
	h.reception.observe(sourceMedia("s", 0, 1), 20, 1)
	h.reception.snapshot(3001, true)
	h.carrierDeliveryNotice()
	var notice map[string]any
	readNotice := func() []byte {
		select {
		case request := <-w.requests:
			return request.payload
		case <-time.After(time.Second):
			t.Fatal("delivery notice missing")
			return nil
		}
	}
	json.Unmarshal(readNotice(), &notice)
	if notice["state"] != "stalled" || !h.microphoneReady() {
		t.Fatal("stall affected healthy adviser direction")
	}
	h.carrierDeliveryNotice()
	if len(w.requests) != 0 {
		t.Fatal("duplicate interruption notice")
	}
	now := mediaClockMS()
	h.reception.observe(sourceMedia("s", max(0, now-1), 151), 20, now)
	h.carrierDeliveryNotice()
	json.Unmarshal(readNotice(), &notice)
	if notice["state"] != "flowing" {
		t.Fatal("no recovery notice")
	}
}

func TestCarrierTimingConcurrentCallIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for call := 0; call < 24; call++ {
		wg.Add(1)
		go func(call int) {
			defer wg.Done()
			var r carrierReception
			r.begin(true)
			for frame := 0; frame < 550; frame++ {
				now := float64(1000 + frame*20)
				if call%2 == 0 && frame >= 50 {
					now = 12000
				}
				r.observe(sourceMedia("stream", frame*20, frame+1), 20, now)
			}
			s := r.snapshot(12000, true)
			if call%2 == 0 {
				if s.StaleDroppedMS < 9600 || s.Recoveries != 1 {
					t.Errorf("call %d unprotected: %+v", call, s)
				}
			} else if s.StaleDroppedMS != 0 || s.Stalls != 0 {
				t.Errorf("call %d affected by other caller: %+v", call, s)
			}
		}(call)
	}
	wg.Wait()
}

func TestOperatorInterruptRemainsExplicit(t *testing.T) {
	f := newCarrierAudioFrontend(8000)
	f.markLocalSignal()
	if f.markInterrupt("operator") != "operator" {
		t.Fatal("operator misattributed to local/provider")
	}
	s := f.snapshot()
	if s.OperatorInterrupts != 1 || s.ProviderCoreInterrupts != 0 || s.LocalInterrupts != 0 {
		t.Fatal("operator diagnostic lost")
	}
}

func TestExpiredSourceFrameDoesNotCloseHealthyMedia(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p := newWebSocketWriterPump(a, ws.StateServerSide)
	defer p.Stop()
	pcm := bytes.Repeat([]byte{42, 0}, 480)
	p.queueAudio(encodeSourceAudio(pcm, sourceMedia("s", 0, 1), mediaClockMS()-1000, mediaClockMS(), 1), 64)
	p.queueAudio(encodeSourceAudio(pcm, sourceMedia("s", 1000, 2), mediaClockMS(), mediaClockMS(), 1), 64)
	b.SetReadDeadline(time.Now().Add(time.Second))
	f, err := ws.ReadFrame(b)
	if err != nil || !bytes.Equal(f.Payload[64:], pcm) {
		t.Fatalf("stale source closed or changed live media: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for p.audioSnapshot().SentBytes == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := p.audioSnapshot()
	if s.SourceStaleBytes != 960 || s.WriteErrors != 0 || s.SentBytes != 960 {
		t.Fatalf("source expiry was a transport failure: %+v", s)
	}
}

func TestWebSocketPongCannotBlockReceivingCallerAudio(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	writer := newWebSocketWriterPump(server, ws.StateServerSide)
	defer writer.Stop()
	received := make(chan error, 1)
	go func() {
		data, op, err := readWebSocketData(server, ws.StateServerSide, writer)
		if err == nil && (op != ws.OpBinary || !bytes.Equal(data, []byte{1, 2})) {
			err = fmt.Errorf("media after ping changed: %v", data)
		}
		received <- err
	}()
	if err := wsutil.WriteClientMessage(client, ws.OpPing, []byte("probe")); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() { written <- wsutil.WriteClientBinary(client, []byte{1, 2}) }()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("caller reader waited for pong socket write")
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := ws.ReadFrame(client)
	if err != nil || frame.Header.OpCode != ws.OpPong || string(frame.Payload) != "probe" {
		t.Fatalf("queued pong was not serialized correctly: %+v %v", frame, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}
