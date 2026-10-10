package main

import (
	"encoding/binary"
	"time"
)

// The carrier supplies arbitrary PCM chunk boundaries; Opus needs 20 ms frames.
type rtcPCMFramer struct {
	pending  []int16
	segments []rtcPCMSegment
}

type rtcPCMSegment struct {
	samples int
	expires time.Time
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
			stats.drop("webrtc_playback_overflow", "carrier_to_operator", 0, 20)
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
