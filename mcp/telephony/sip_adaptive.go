package main

import (
	"encoding/binary"
	"time"
)

func playbackPolicyName(p *sipPlaybackPolicy) string {
	if p != nil && p.resolved().Receiver == "reference" {
		return "automatic_rolling_playout_v1"
	}
	return "local_residence_playout_v2"
}

// The browser leg can retain the original local receipt age across the
// internal WebSocket hop. Raw RTP timing remains separate diagnostic evidence.
func queueSIPHumanAudio(writer *websocketWriterPump, pcm []int16, arrival time.Time, generation uint32) {
	receipt := mediaClockMS()
	mapped := receipt - max(0, float64(time.Since(arrival))/float64(time.Millisecond))
	frame := make([]byte, sourceAudioHeaderBytes+2*len(pcm))
	writeSourceAudioHeader(frame, carrierSource{Stream: "sip_local_residence"}, mapped, receipt, generation)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(frame[sourceAudioHeaderBytes+2*i:], uint16(v))
	}
	writer.queueAudioAt(frame, sourceAudioHeaderBytes, arrival)
}

type sipDecodeSkip struct {
	ts      uint32
	payload []byte
}

func (b *rtpJitterBuffer) rememberSkippedDecode(ts uint32, p []byte) {
	bytes := len(p)
	for i := 0; i < b.decodeSkipCount; i++ {
		bytes += len(b.decodeSkips[i].payload)
	}
	b.decodeSkipPending.Store(true)
	if b.decodeSkipCount < len(b.decodeSkips) && bytes <= 640 {
		b.decodeSkips[b.decodeSkipCount] = sipDecodeSkip{ts, p}
		b.decodeSkipCount++
	} else {
		if !b.decodeSkipUnknown {
			b.decodeUnknownFrom = ts
		}
		b.decodeUnknownThrough = ts
		b.decodeSkipUnknown = true
	}
}

// G.722 is stateful. When a bounded overflow discards encoded packets, consume
// at most four of them on the decoder owner without playing their audio. Never
// decode on socket ingress or retain an unbounded catch-up queue.
func (b *rtpJitterBuffer) warmDecoder(m *sipRTPMedia) {
	if !b.decodeSkipPending.Load() {
		return
	}
	b.mu.Lock()
	// Ingress may already have discarded packets AFTER the frame being decoded.
	// Leave those pending; decoding them now would reverse ADPCM state order.
	ts := b.lastPoppedTimestamp
	var skips [4]sipDecodeSkip
	count, remaining := 0, 0
	for i := 0; i < b.decodeSkipCount; i++ {
		q := b.decodeSkips[i]
		if int32(ts-q.ts) > 0 {
			skips[count] = q
			count++
		} else {
			b.decodeSkips[remaining] = q
			remaining++
		}
	}
	for i := remaining; i < len(b.decodeSkips); i++ {
		b.decodeSkips[i] = sipDecodeSkip{}
	}
	b.decodeSkipCount = remaining
	unknown := b.decodeSkipUnknown && int32(ts-b.decodeUnknownFrom) > 0
	if unknown && int32(ts-b.decodeUnknownThrough) > 0 {
		b.decodeSkipUnknown = false
	}
	b.decodeSkipPending.Store(remaining > 0 || b.decodeSkipUnknown)
	if unknown {
		b.decodeResets++
	} else {
		b.decodeWarmups += count
	}
	b.mu.Unlock()
	if unknown {
		m.resetReceiveDecoder()
		return
	}
	for i := 0; i < count; i++ {
		m.decodeAudio(skips[i].payload)
	}
}

// RTP is an unsynchronized sampling clock. Keep it as evidence and a bounded
// relative-lateness guard, never as the physical age of newly received audio.
// Existing queued packets always retain their actual arrival time.
func (b *rtpJitterBuffer) packetAge(now, arrival time.Time, ts uint32) time.Duration {
	if b.policy.Receiver == "adaptive" {
		return max(0, now.Sub(arrival))
	}
	return max(now.Sub(arrival), now.Sub(b.playoutTime(ts)))
}
