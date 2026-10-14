package main

import (
	"encoding/binary"
	"math"
	"time"
)

// Private overlay: this queue is owned only by the pinned browser's RTP encoder.
// It is never exposed to the carrier, recording tap or listener registry.
type rtcWhisperFrame struct {
	epoch uint32
	pcm   []int16 // 8kHz PCMU decoded without resampling on the main call path
	at    time.Time
	valid func() bool
}

func queueRTCWhisper(queue chan rtcWhisperFrame, data []byte, valid func() bool, done <-chan struct{}) bool {
	if len(data) <= 16 || len(data) > 176 || binary.LittleEndian.Uint32(data) != coachPlaybackMagic || valid == nil {
		return false
	}
	age := mediaClockMS() - mathFloat64(data[8:16])
	if math.IsNaN(age) || math.IsInf(age, 0) || age < 0 || age > float64(coachAudioMaxAge/time.Millisecond) {
		return false
	}
	frame := rtcWhisperFrame{epoch: binary.LittleEndian.Uint32(data[4:]), pcm: ulawToPCM16(data[16:]), at: time.Now().Add(-time.Duration(age * float64(time.Millisecond))), valid: valid}
	select {
	case <-done:
		return false
	default:
	}
	select {
	case queue <- frame:
		return true
	default:
	}
	select {
	case <-queue:
	default:
	}
	select {
	case queue <- frame:
		return true
	default:
		return false
	}
}

func nextRTCWhisper(queue <-chan rtcWhisperFrame, now time.Time) *rtcWhisperFrame {
	for {
		select {
		case frame, open := <-queue:
			if !open {
				return nil
			}
			if now.Sub(frame.at) <= coachAudioMaxAge && frame.valid() {
				return &frame
			}
		default:
			return nil
		}
	}
}
func mixRTCWhisper(main []float32, pcm8k []int16, resampler *pcmResampler) []float32 {
	// Resample in the encoder goroutine, never while holding a hub lock. A short
	// frame is padded rather than repeating private speech after its lease ends.
	overlay := resampler.Process(pcm8k)
	return mixRTCWhisperInto(main, overlay, make([]float32, len(main)))
}
func mixRTCWhisperInto(main []float32, overlay []int16, dst []float32) []float32 {
	out := dst[:len(main)]
	copy(out, main)
	for i := 0; i < len(out) && i < len(overlay); i++ {
		headroom := max(float32(0), 1-float32(math.Abs(float64(out[i]))))
		out[i] += max(-headroom, min(headroom, float32(overlay[i])/32768*.5))
	}
	return out
}
