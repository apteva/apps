package main

import (
	"encoding/binary"
	"time"
)

// The carrier supplies arbitrary PCM chunk boundaries; Opus needs 20 ms frames.
type rtcPCMFramer struct {
	pending  []int16
	segments []rtcPCMSegment
	// Samples in the latest source chunk: its successor is expected one chunk
	// duration after arrival.
	lastChunk int
	// Consecutive playback ticks with the queue above its latency target.
	backlogTicks int
}

const (
	// Frames still queued after this tick's frame which are not excess latency.
	// ±1 ms arrival jitter alone holds two for runs of up to ~100 ms.
	rtcPlaybackTargetFrames = 1
	// A backlog persisting this long is trimmed at a silent frame (200 ms) or,
	// failing that, by crossfading speech (400 ms, then at most every 100 ms).
	rtcBacklogSilenceTicks = 10
	rtcBacklogSpeechTicks  = 20
	rtcBacklogSpeechEvery  = 5
	rtcSilencePeak         = 64 // about -54 dBFS
	rtcTrimCrossfade       = 120
)

type rtcPCMSegment struct {
	samples int
	expires time.Time
}

// Called only by the paced owner after a full packet interval without new PCM.
// Complete a live utterance tail before its deadline. The missing suffix is a
// short ramp to silence; no received sample is changed or given a new lifetime.
func (f *rtcPCMFramer) flushPartial(now time.Time) (rtcPCMFrame, int) {
	if len(f.pending) == 0 || len(f.segments) == 0 {
		return rtcPCMFrame{}, 0
	}
	deadline := f.segments[0].expires
	for _, s := range f.segments[1:] {
		if s.expires.Before(deadline) {
			deadline = s.expires
		}
	}
	if !deadline.After(now) {
		return rtcPCMFrame{}, 0
	}
	pcm := make([]int16, 480)
	n := copy(pcm, f.pending)
	padding := len(pcm) - n
	last := int32(pcm[n-1])
	for i := n; i < len(pcm); i++ {
		pcm[i] = int16(last * int32(len(pcm)-1-i) / int32(padding))
	}
	f.pending, f.segments = f.pending[:0], f.segments[:0]
	return rtcPCMFrame{pcm: pcm, expires: deadline}, padding
}

func (f *rtcPCMFramer) discardExpired(now time.Time) int {
	dropped, read, kept := 0, 0, 0
	segments := f.segments[:0]
	for _, segment := range f.segments {
		end := read + segment.samples
		if segment.expires.After(now) {
			copy(f.pending[kept:], f.pending[read:end])
			kept += segment.samples
			segments = append(segments, segment)
		} else {
			dropped += segment.samples
		}
		read = end
	}
	f.pending = f.pending[:kept]
	f.segments = segments
	return dropped
}

func (f *rtcPCMFramer) push(data []byte, expires time.Time, emit func(rtcPCMFrame) bool) bool {
	if f.pending == nil {
		f.pending = make([]int16, 0, 480)
	}
	f.lastChunk = len(data) / 2
	for len(data) >= 2 {
		take := min(480-len(f.pending), len(data)/2)
		for k := 0; k < take*2; k += 2 {
			f.pending = append(f.pending, int16(binary.LittleEndian.Uint16(data[k:])))
		}
		data = data[take*2:]
		if n := len(f.segments); n > 0 && f.segments[n-1].expires.Equal(expires) {
			f.segments[n-1].samples += take
		} else {
			f.segments = append(f.segments, rtcPCMSegment{samples: take, expires: expires})
		}
		if len(f.pending) == 480 {
			// A frame may contain several source chunks with different remaining
			// lifetimes. Only deadlines of samples in THIS frame contribute.
			deadline := f.segments[0].expires
			for _, segment := range f.segments[1:] {
				if segment.expires.Before(deadline) {
					deadline = segment.expires
				}
			}
			frame := rtcPCMFrame{pcm: append([]int16(nil), f.pending...), expires: deadline}
			f.pending = f.pending[:0]
			f.segments = f.segments[:0]
			if !emit(frame) {
				return false
			}
		}
	}
	return true
}

// One producer, paced consumer. Keep the existing six-frame/120 ms bound.
func queueRTCFrame(frames chan rtcPCMFrame, frame rtcPCMFrame, done <-chan struct{}, stats *rtcMediaStats) bool {
	select {
	case frames <- frame:
	case <-done:
		return false
	default:
		select {
		case <-frames:
			stats.drop("webrtc_playback_overflow", stats.mediaDirection(), 0, 20)
		default:
		}
		select {
		case frames <- frame:
		default:
		}
	}
	stats.mu.Lock()
	stats.value.MaxQueueMS = max(stats.value.MaxQueueMS, len(frames)*20)
	stats.mu.Unlock()
	return true
}

// A complete frame already owns this tick. An idle chunk arrival does not prove
// the remaining samples are an utterance tail while full frames remain queued.
// Preserve the partial to join the next chunk; never enqueue padded extra audio.
func nextRTCPlaybackFrame(frames <-chan rtcPCMFrame, pending *rtcPCMFramer, lastChunkAt, now time.Time, stats *rtcMediaStats) (rtcPCMFrame, bool) {
	frame, open := nextRTCFrame(frames, now, stats)
	if !open {
		return frame, open
	}
	frame = pending.trimBacklog(frames, frame, now, stats)
	if lastChunkAt.IsZero() || now.Sub(lastChunkAt) < 20*time.Millisecond {
		return frame, true
	}
	// Age protection applies even when this tick already has a complete frame.
	if dropped := pending.discardExpired(now); dropped > 0 {
		stats.drop("webrtc_playback_partial_age", stats.mediaDirection(), 0, dropped*1000/24000)
	}
	if frame.pcm != nil {
		return frame, true
	}
	// A slightly late continuation is not an utterance tail. Like the SIP pacer,
	// wait one packet interval past the chunk expected after the last one.
	expected := max(20*time.Millisecond, time.Duration(pending.lastChunk)*time.Second/24000)
	if now.Sub(lastChunkAt) < expected+20*time.Millisecond {
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

// A stall delivers its backlog at once and the paced consumer cannot catch up,
// so without trimming the added latency lasts for the rest of the call. Once a
// backlog persists, drop silent frames first; continuous speech is shortened
// by one frame with a short crossfade into its successor, never with a jump.
func (f *rtcPCMFramer) trimBacklog(frames <-chan rtcPCMFrame, frame rtcPCMFrame, now time.Time, stats *rtcMediaStats) rtcPCMFrame {
	if frame.pcm == nil || len(frames) <= rtcPlaybackTargetFrames {
		f.backlogTicks = 0
		return frame
	}
	f.backlogTicks++
	silent := rtcPCMSilent(frame.pcm)
	if f.backlogTicks < rtcBacklogSilenceTicks || !silent && f.backlogTicks < rtcBacklogSpeechTicks {
		return frame
	}
	next, _ := nextRTCFrame(frames, now, stats)
	if next.pcm == nil {
		return frame
	}
	if silent {
		stats.drop("webrtc_playback_backlog_silence", stats.mediaDirection(), 0, len(frame.pcm)*1000/24000)
		return next
	}
	stats.drop("webrtc_playback_backlog_speech", stats.mediaDirection(), 0, len(frame.pcm)*1000/24000)
	f.backlogTicks = rtcBacklogSpeechTicks - rtcBacklogSpeechEvery
	return rtcPCMFrame{pcm: rtcCrossfadeFrames(frame.pcm, next.pcm), expires: frame.expires}
}

func rtcPCMSilent(pcm []int16) bool {
	for _, v := range pcm {
		if v > rtcSilencePeak || v < -rtcSilencePeak {
			return false
		}
	}
	return true
}

// Starts as the dropped frame, so it continues the previous output, and ends
// as its successor, so the following frame continues it.
func rtcCrossfadeFrames(dropped, next []int16) []int16 {
	out := append([]int16(nil), next...)
	n := min(rtcTrimCrossfade, len(dropped), len(next))
	for i := 0; i < n; i++ {
		t := float64(i+1) / float64(n+1)
		out[i] = int16(float64(dropped[i])*(1-t) + float64(next[i])*t)
	}
	return out
}
