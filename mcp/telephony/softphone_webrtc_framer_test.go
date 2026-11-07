package main

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

// Use the production PCM framer and queue selector with an explicit clock.
// No scheduler/network timing is needed to reproduce the consumed deadline.
func TestRTCFramePartialStartupDoesNotRetainConsumedDeadline(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, part := range []string{"queued_frame", "retained_partial"} {
		t.Run(part, func(t *testing.T) {
			var framer rtcPCMFramer
			var last rtcPCMFrame
			emit := func(frame rtcPCMFrame) bool { last = frame; return true }
			framer.push(pcm16ToBytes(make([]int16, 432)), start.Add(250*time.Millisecond), emit) // 18 ms startup
			for n := 1; n <= 12; n++ {
				now := start.Add(time.Duration(n) * 20 * time.Millisecond)
				if dropped := framer.discardExpired(now); dropped != 0 {
					t.Fatal("fresh partial expired during steady arrival", dropped)
				}
				framer.push(pcm16ToBytes(make([]int16, 480)), now.Add(250*time.Millisecond), emit)
			}
			now := start.Add(260 * time.Millisecond)
			if part == "retained_partial" {
				if dropped := framer.discardExpired(now); dropped != 0 {
					t.Fatalf("fresh 240 ms partial inherited consumed 0 ms deadline: dropped %d samples (%d ms)", dropped, dropped*1000/24000)
				}
				return
			}
			queue := make(chan rtcPCMFrame, 6)
			queue <- last // At 240 ms, oldest samples are from the 220 ms chunk.
			stats := &rtcMediaStats{}
			frame, open := nextRTCFrame(queue, now, stats)
			if !open || len(frame.pcm) != 480 || stats.snapshot().OutboundDroppedMS != 0 {
				t.Fatalf("fresh frame discarded at 260 ms: inherited deadline=%v; expected=%v; drops=%+v", last.expires.Sub(start), 470*time.Millisecond, stats.snapshot())
			}
			if !frame.expires.Equal(start.Add(470 * time.Millisecond)) {
				t.Fatal("wrong oldest retained deadline", frame.expires.Sub(start))
			}
		})
	}
}

func TestRTCFrameConstituentDeadlinesStillExpireOldAudio(t *testing.T) {
	for _, oldFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "old_first", false: "old_last"}[oldFirst], func(t *testing.T) {
			start := time.Unix(1000, 0)
			old, fresh := start.Add(10*time.Millisecond), start.Add(300*time.Millisecond)
			first, second := fresh, old
			if oldFirst {
				first, second = old, fresh
			}
			var framer rtcPCMFramer
			frames := make(chan rtcPCMFrame, 6)
			stats := &rtcMediaStats{}
			emit := func(frame rtcPCMFrame) bool { frames <- frame; return true }
			framer.push(pcm16ToBytes(make([]int16, 240)), first, emit)
			framer.push(pcm16ToBytes(make([]int16, 240)), second, emit)
			framer.push(pcm16ToBytes([]int16{123}), fresh, emit)
			framer.push(pcm16ToBytes(make([]int16, 479)), fresh, emit)
			frame, open := nextRTCFrame(frames, start.Add(20*time.Millisecond), stats)
			if !open || len(frame.pcm) != 480 || frame.pcm[0] != 123 || !frame.expires.Equal(fresh) {
				t.Fatal("genuine old constituent was replayed or poisoned a later fresh frame", frame)
			}
			v := stats.snapshot()
			if v.OutboundDroppedMS != 20 || len(v.Events) != 1 || v.Events[0].Reason != "webrtc_playback_queue_age" {
				t.Fatal("stale frame protection or loss accounting changed", v)
			}
		})
	}
}

func TestRTCFramePartialExpiryPreservesFreshSegments(t *testing.T) {
	for _, oldFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "expired_prefix", false: "expired_suffix"}[oldFirst], func(t *testing.T) {
			start := time.Unix(1000, 0)
			var framer rtcPCMFramer
			var output rtcPCMFrame
			emit := func(frame rtcPCMFrame) bool { output = frame; return true }
			old := pcm16ToBytes(slices.Repeat([]int16{11}, 240))
			fresh := pcm16ToBytes(slices.Repeat([]int16{22}, 120))
			if oldFirst {
				framer.push(old, start.Add(10*time.Millisecond), emit)
				framer.push(fresh, start.Add(100*time.Millisecond), emit)
			} else {
				framer.push(fresh, start.Add(100*time.Millisecond), emit)
				framer.push(old, start.Add(10*time.Millisecond), emit)
			}
			if dropped := framer.discardExpired(start.Add(20 * time.Millisecond)); dropped != 240 {
				t.Fatal("partial expiry did not count only old samples", dropped)
			}
			if len(framer.pending) != 120 || !slices.Equal(framer.pending, bytesToPCM16(fresh)) {
				t.Fatal("fresh partial corrupted", framer.pending)
			}
			framer.push(pcm16ToBytes(slices.Repeat([]int16{33}, 360)), start.Add(200*time.Millisecond), emit)
			want := append(bytesToPCM16(fresh), slices.Repeat([]int16{33}, 360)...)
			if !slices.Equal(output.pcm, want) || !output.expires.Equal(start.Add(100*time.Millisecond)) {
				t.Fatal("retained segment deadline or sample order changed", output.expires.Sub(start))
			}
			if len(framer.pending) != 0 || len(framer.segments) != 0 {
				t.Fatal("consumed segments retained")
			}
		})
	}
}

func TestRTCFrameSchedulingPausePreservesFreshQueuedAudio(t *testing.T) {
	start := time.Unix(1000, 0)
	var framer rtcPCMFramer
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	done := make(chan struct{})
	emit := func(frame rtcPCMFrame) bool { return queueRTCFrame(frames, frame, done, stats) }
	framer.push(pcm16ToBytes(make([]int16, 432)), start.Add(250*time.Millisecond), emit)
	for n := 1; n <= 6; n++ {
		framer.push(pcm16ToBytes(slices.Repeat([]int16{int16(n)}, 480)), start.Add(time.Duration(250+20*n)*time.Millisecond), emit)
	}
	// Playback resumes at 320 ms. Four expired frames are discarded; frames
	// containing newer samples remain independently eligible.
	frame, open := nextRTCFrame(frames, start.Add(320*time.Millisecond), stats)
	if !open || len(frame.pcm) != 480 || frame.pcm[0] != 4 || frame.pcm[479] != 5 || !frame.expires.Equal(start.Add(330*time.Millisecond)) {
		t.Fatal("pause replayed stale speech or lost fresh speech", frame.expires.Sub(start))
	}
	if v := stats.snapshot(); v.OutboundDroppedMS != 80 || len(v.Events) != 4 {
		t.Fatal("pause loss accounting", v)
	}
	if len(frames) != 1 {
		t.Fatal("fresh queued tail was discarded", len(frames))
	}
	if dropped := framer.discardExpired(start.Add(320 * time.Millisecond)); dropped != 0 {
		t.Fatal("fresh unframed tail inherited expired deadline", dropped)
	}
}

func TestRTCFrameBurstKeepsQueueAndPartialBounded(t *testing.T) {
	start := time.Unix(1000, 0)
	var framer rtcPCMFramer
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	done := make(chan struct{})
	emit := func(frame rtcPCMFrame) bool { return queueRTCFrame(frames, frame, done, stats) }
	pcm := make([]int16, 24000)
	for k := range pcm {
		pcm[k] = int16(k)
	}
	framer.push(pcm16ToBytes(pcm), start.Add(liveAudioMaxAge), emit)
	if len(frames) != 6 || cap(framer.pending) != 480 || len(framer.pending) != 0 || len(framer.segments) != 0 {
		t.Fatal("burst exceeded frame/partial bounds", len(frames), cap(framer.pending))
	}
	if v := stats.snapshot(); v.MaxQueueMS != 120 || v.OutboundDroppedMS != 880 || len(v.Events) != 32 {
		t.Fatal("burst bounds or discarded duration changed", v)
	}
	for i := 44; i < 50; i++ {
		frame, open := nextRTCFrame(frames, start, stats)
		if !open || !slices.Equal(frame.pcm, pcm[i*480:(i+1)*480]) {
			t.Fatal("burst did not retain newest six frames", i)
		}
	}
}

func TestRTCFrameSampleContinuityAcrossChunkBoundaries(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, chunk := range []int{1, 7, 24, 48, 239, 432, 479, 480, 481, 960, 24000} {
		t.Run(strconv.Itoa(chunk)+"_samples", func(t *testing.T) {
			pcm := make([]int16, 24000+432)
			for k := range pcm {
				pcm[k] = int16(k * 37)
			}
			var framer rtcPCMFramer
			var output []int16
			emit := func(frame rtcPCMFrame) bool {
				if len(frame.pcm) != 480 {
					t.Fatal("wrong Opus frame size")
				}
				output = append(output, frame.pcm...)
				return true
			}
			for k := 0; k < len(pcm); k += chunk {
				framer.push(pcm16ToBytes(pcm[k:min(k+chunk, len(pcm))]), start.Add(time.Second), emit)
				if len(framer.pending) >= 480 || len(framer.segments) > len(framer.pending) {
					t.Fatal("unbounded retained segments")
				}
			}
			output = append(output, framer.pending...)
			if !slices.Equal(output, pcm) {
				t.Fatal("framing changed PCM values or sample order")
			}
			if dropped := framer.discardExpired(start.Add(time.Second)); dropped != 432 {
				t.Fatal("expiry boundary changed", dropped)
			}
		})
	}
}

func TestRTCFrameDeadlineMetadataRemainsBounded(t *testing.T) {
	start := time.Unix(1000, 0)
	var framer rtcPCMFramer
	var output rtcPCMFrame
	emit := func(frame rtcPCMFrame) bool { output = frame; return true }
	for n := 1; n <= 479; n++ {
		framer.push(pcm16ToBytes([]int16{int16(n)}), start.Add(time.Duration(n)*time.Millisecond), emit)
	}
	if len(framer.pending) != 479 || len(framer.segments) != 479 {
		t.Fatal("tiny chunks must remain sample-bounded", len(framer.pending), len(framer.segments))
	}
	if dropped := framer.discardExpired(start.Add(240 * time.Millisecond)); dropped != 240 {
		t.Fatal("only expired individual segments should be removed", dropped)
	}
	framer.push(pcm16ToBytes(make([]int16, 241)), start.Add(time.Second), emit)
	if len(output.pcm) != 480 || output.pcm[0] != 241 || !output.expires.Equal(start.Add(241*time.Millisecond)) {
		t.Fatal("deadline metadata lost during compaction", output.expires.Sub(start))
	}
	if len(framer.pending) != 0 || len(framer.segments) != 0 {
		t.Fatal("consumed deadline metadata retained")
	}
}
