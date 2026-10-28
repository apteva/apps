package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

type reviewRTCTick func(<-chan rtcPCMFrame, *rtcPCMFramer, time.Time, time.Time, *rtcMediaStats) (rtcPCMFrame, bool)

// Frozen tick selection from telephony/v0.12.61 nextRTCPlaybackFrame: no
// backlog trimming and a partial flush 20 ms after the last chunk.
func review61RTCPlaybackTick(frames <-chan rtcPCMFrame, pending *rtcPCMFramer, lastChunkAt, now time.Time, stats *rtcMediaStats) (rtcPCMFrame, bool) {
	frame, open := nextRTCFrame(frames, now, stats)
	if !open || lastChunkAt.IsZero() || now.Sub(lastChunkAt) < 20*time.Millisecond {
		return frame, open
	}
	if dropped := pending.discardExpired(now); dropped > 0 {
		stats.drop("webrtc_playback_partial_age", "carrier_to_operator", 0, dropped*1000/24000)
	}
	if frame.pcm != nil {
		return frame, true
	}
	if tail, padding := pending.flushPartial(now); tail.pcm != nil {
		stats.mu.Lock()
		stats.value.PartialFlushedFrames++
		stats.value.PartialPaddedMS += float64(padding) / 24
		stats.mu.Unlock()
		frame = tail
	}
	return frame, true
}

type reviewStallResult struct {
	DropMS     map[string]int64
	DepthAfter [2]int           // queued frames 800 ms after each stall
	Recovered  [2]time.Duration // stall end until queue <= target (-1: never)
	MeanQueue  float64          // mean queued frames after a tick, last second
	FinalDepth int              // after the last tick
	MaxJump    int
	Played     int
}

// Real push, queueRTCFrame and tick selection. Chunks arrive every 20 ms and
// ticks run 10 ms later. A sender-loop stall suspends both, then delivers the
// held chunks at once (the intake channel is unbuffered). Stalls: 60 ms at
// 1.0 s and 80 ms at 2.0 s.
func replayRTCStalls(tick reviewRTCTick, gaps bool) reviewStallResult {
	start := time.Unix(1700000000, 0)
	stalls := [][2]int{{1000, 1060}, {2000, 2080}}
	sample := 0
	source := func() []int16 {
		pcm := make([]int16, 480)
		for i := range pcm {
			// 440 Hz voice; optionally with a 200 ms pause every 600 ms.
			if !gaps || (sample/24)%600 < 400 {
				pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(sample)/24000))
			}
			sample++
		}
		return pcm
	}
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	done := make(chan struct{})
	var pending rtcPCMFramer
	var last time.Time
	var held [][]int16
	var out []int16
	r := reviewStallResult{DropMS: map[string]int64{}, Recovered: [2]time.Duration{-1, -1}}
	collect := func() {
		stats.mu.Lock()
		for _, e := range stats.value.Events {
			r.DropMS[e.Reason] += int64(e.DurationMS)
		}
		stats.value.Events = nil
		stats.mu.Unlock()
	}
	deliver := func(pcm []int16, now time.Time) {
		pending.discardExpired(now)
		last = now
		pending.push(pcm16ToBytes(pcm), now.Add(liveAudioMaxAge), func(f rtcPCMFrame) bool { return queueRTCFrame(frames, f, done, stats) })
		collect()
	}
	queued, ticks := 0, 0
	for ms := 0; ms <= 3200; ms += 10 {
		now := start.Add(time.Duration(ms) * time.Millisecond)
		inStall := false
		for _, s := range stalls {
			inStall = inStall || ms >= s[0] && ms < s[1]
		}
		if inStall {
			if ms%20 == 0 {
				held = append(held, source())
			}
			continue
		}
		for _, pcm := range held {
			deliver(pcm, now)
		}
		held = held[:0]
		if ms%20 == 0 {
			deliver(source(), now)
			continue
		}
		frame, _ := tick(frames, &pending, last, now, stats)
		collect()
		if frame.pcm != nil {
			out = append(out, frame.pcm...)
			r.Played++
		}
		for k, s := range stalls {
			since := time.Duration(ms-s[1]) * time.Millisecond
			if since >= 0 && r.Recovered[k] < 0 && len(frames) <= rtcPlaybackTargetFrames && (k == 1 || ms < stalls[1][0]) {
				r.Recovered[k] = since
			}
			if since >= 800*time.Millisecond && since < 820*time.Millisecond {
				r.DepthAfter[k] = len(frames)
			}
		}
		if ms > 2200 {
			queued += len(frames)
			ticks++
		}
		r.FinalDepth = len(frames)
	}
	r.MeanQueue = float64(queued) / float64(ticks)
	for i := 1; i < len(out); i++ {
		r.MaxJump = max(r.MaxJump, int(math.Abs(float64(out[i])-float64(out[i-1]))))
	}
	return r
}

func TestReviewFRTCBacklogReturnsToTargetAfterStalls(t *testing.T) {
	for _, gaps := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuous_speech", true: "speech_with_pauses"}[gaps], func(t *testing.T) {
			old := replayRTCStalls(review61RTCPlaybackTick, gaps)
			fixed := replayRTCStalls(nextRTCPlaybackFrame, gaps)
			t.Logf("v0.12.61: %+v", old)
			t.Logf("fixed:    %+v", fixed)
			if old.DepthAfter[0] <= rtcPlaybackTargetFrames || old.DropMS["webrtc_playback_overflow"] == 0 {
				t.Fatal("did not reproduce the persistent backlog", old)
			}
			if fixed.DropMS["webrtc_playback_overflow"] != 0 || fixed.FinalDepth > rtcPlaybackTargetFrames {
				t.Fatal("backlog persisted or overflowed", fixed)
			}
			for k, d := range fixed.Recovered {
				if d < 0 || d > 800*time.Millisecond || fixed.DepthAfter[k] > rtcPlaybackTargetFrames {
					t.Fatal("backlog not trimmed within a bounded time", k, fixed)
				}
			}
			// The 440 Hz source changes by at most ~920 per sample; a trim must not
			// add a step discontinuity (a click).
			if fixed.MaxJump > 1500 {
				t.Fatal("trim introduced a discontinuity", fixed.MaxJump)
			}
			if gaps && fixed.DropMS["webrtc_playback_backlog_speech"] >= fixed.DropMS["webrtc_playback_backlog_silence"] {
				t.Fatal("speech trimmed in preference to available silence", fixed)
			}
		})
	}
}

// Steady arrival with a small jitter never accumulates a backlog, so the
// trimmer must not remove audio from an ordinary call.
func TestReviewFRTCBacklogTrimIdleOnSteadyCall(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		if r := replayTailJitter(t, true, seed); r.drops != 0 || !r.orderPreserved {
			t.Fatal("steady call trimmed or reordered", r)
		}
	}
}

// A continuation arriving a few ms late is not an utterance tail: the partial
// waits to join it instead of being padded with a ramp to silence mid-word.
func TestReviewFRTCPartialWaitsForSlightlyLateChunk(t *testing.T) {
	for _, tick := range []struct {
		name string
		fn   reviewRTCTick
	}{{"v0.12.61", review61RTCPlaybackTick}, {"fixed", nextRTCPlaybackFrame}} {
		t.Run(tick.name, func(t *testing.T) {
			now := time.Unix(1700000000, 0)
			pcm := make([]int16, 1200)
			for i := range pcm {
				pcm[i] = int16(i + 1)
			}
			var f rtcPCMFramer
			stats := &rtcMediaStats{}
			frames := make(chan rtcPCMFrame, 6)
			done := make(chan struct{})
			emit := func(v rtcPCMFrame) bool { return queueRTCFrame(frames, v, done, stats) }
			ms := func(n int) time.Time { return now.Add(time.Duration(n) * time.Millisecond) }
			f.push(pcm16ToBytes(pcm[:720]), ms(250), emit) // 30 ms chunk: one frame + 10 ms partial
			first, _ := tick.fn(frames, &f, now, ms(10), stats)
			late, _ := tick.fn(frames, &f, now, ms(40), stats) // successor due at 30 ms, arrives at 42 ms
			f.push(pcm16ToBytes(pcm[720:]), ms(292), emit)
			second, _ := tick.fn(frames, &f, ms(42), ms(60), stats)
			if !slices.Equal(first.pcm, pcm[:480]) {
				t.Fatal("first frame changed")
			}
			flushed := stats.snapshot().PartialFlushedFrames
			t.Logf("late-tick frame=%v flushed=%d padded_ms=%.1f", late.pcm != nil, flushed, stats.snapshot().PartialPaddedMS)
			if tick.name == "v0.12.61" {
				if flushed != 1 {
					t.Fatal("did not reproduce the mid-word padding")
				}
				return
			}
			if late.pcm != nil || flushed != 0 || !slices.Equal(second.pcm, pcm[480:960]) {
				t.Fatal("mid-word partial padded or continuation misaligned", flushed)
			}
		})
	}
}

func TestReviewFRTCDropsReportListenerDirection(t *testing.T) {
	for _, direction := range []string{"caller_to_listener", "adviser_to_listener"} {
		now := time.Unix(1700000000, 0)
		stats := &rtcMediaStats{direction: direction}
		frames := make(chan rtcPCMFrame, 6)
		done := make(chan struct{})
		for range 7 {
			queueRTCFrame(frames, rtcPCMFrame{pcm: make([]int16, 480), expires: now}, done, stats)
		}
		nextRTCFrame(frames, now.Add(time.Millisecond), stats)
		events := stats.snapshot().Events
		reasons := map[string]bool{}
		for _, e := range events {
			reasons[e.Reason] = true
			if e.Direction != direction {
				t.Fatal("drop reported against the wrong media direction", e)
			}
		}
		if !reasons["webrtc_playback_overflow"] || !reasons["webrtc_playback_queue_age"] {
			t.Fatal("expected overflow and queue-age drops", events)
		}
	}
	if (&rtcMediaStats{}).mediaDirection() != "carrier_to_operator" {
		t.Fatal("operator default changed")
	}
}

type reviewCountingEncoder struct {
	calls  int
	during func()
	inputs [][]float32
}

func (e *reviewCountingEncoder) EncodeFloat32(pcm []float32, out []byte) (int, error) {
	e.calls++
	e.inputs = append(e.inputs, append([]float32(nil), pcm...))
	if e.during != nil {
		e.during()
	}
	out[0] = byte(e.calls)
	return 1, nil
}
func (e *reviewCountingEncoder) configure(int, int) error { return nil }
func (e *reviewCountingEncoder) capability() string       { return "test" }
func (e *reviewCountingEncoder) Close()                   {}

// A coaching stop during encoding must not encode the same 20 ms a second
// time: the encoder state has already advanced past the mixed frame.
func TestReviewFRTCWhisperStopDuringEncodeEncodesOnce(t *testing.T) {
	main := make([]float32, 480)
	overlay := slices.Repeat([]int16{8000}, 480)
	mixed := make([]float32, 480)
	out := make([]byte, 1275)
	valid := true
	voice := &rtcWhisperFrame{pcm: make([]int16, 160), at: time.Now(), valid: func() bool { return valid }}
	encoder := &reviewCountingEncoder{during: func() { valid = false }}
	n, wasMixed, err := encodeRTCSlot(encoder, main, voice, overlay, mixed, out)
	if err != nil || n != 1 || encoder.calls != 1 || !wasMixed || out[0] != 1 {
		t.Fatal("slot re-encoded or encoded packet discarded", encoder.calls, wasMixed)
	}
	// Already stopped before encoding: the overlay is never encoded.
	encoder = &reviewCountingEncoder{}
	if _, wasMixed, _ = encodeRTCSlot(encoder, main, voice, overlay, mixed, out); wasMixed || encoder.calls != 1 || encoder.inputs[0][0] != 0 {
		t.Fatal("revoked overlay encoded", encoder.calls)
	}
	valid = true
	voice.at = time.Now().Add(-coachAudioMaxAge - time.Millisecond)
	if _, wasMixed, _ = encodeRTCSlot(encoder, main, voice, overlay, mixed, out); wasMixed {
		t.Fatal("stale overlay encoded")
	}
}

func TestReviewFRTCWriteErrorsAreLossUntilPersistent(t *testing.T) {
	var f rtcWriteFailures
	transient := errors.New("srtp: write buffer full")
	for range 3 {
		for k := 1; k < rtcMaxConsecutiveWriteErrors; k++ {
			if f.fatal(transient) {
				t.Fatal("transient write error ended the call", k)
			}
		}
		f.fatal(nil)
	}
	for k := 1; k <= rtcMaxConsecutiveWriteErrors; k++ {
		if fatal := f.fatal(transient); fatal != (k == rtcMaxConsecutiveWriteErrors) {
			t.Fatal("persistent failure bound", k, fatal)
		}
	}
	for _, err := range []error{net.ErrClosed, fmt.Errorf("write: %w", io.ErrClosedPipe), webrtc.ErrConnectionClosed} {
		var g rtcWriteFailures
		if !g.fatal(err) {
			t.Fatal("closed transport not fatal", err)
		}
	}
}

// PeerConnection.Close runs after coachMu is released, so the signaling
// reader, bridge reader and expiry ticker are not blocked behind teardown.
func TestReviewFListenerCoachCloseOutsideLock(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	coachDone := make(chan struct{})
	slot := &listenerCoachSlot{pc: pc, done: coachDone, generation: 3, closePeer: func(got *webrtc.PeerConnection) {
		if got != pc {
			t.Error("wrong peer closed")
		}
		close(entered)
		<-release
	}}
	finished := make(chan struct{})
	go func() { slot.closeIf(func(_ *webrtc.PeerConnection, g uint32) bool { return g == 3 }); close(finished) }()
	<-entered
	if !slot.mu.TryLock() {
		close(release)
		t.Fatal("coach lock held during PeerConnection.Close")
	}
	if slot.pc != nil || slot.done != nil {
		t.Fatal("peer not detached before close")
	}
	slot.mu.Unlock()
	select {
	case <-coachDone:
	default:
		t.Fatal("capture readers not signaled")
	}
	close(release)
	<-finished
}
