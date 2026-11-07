// Frozen 0.12.4 reference for deterministic replay and CPU comparison.
// Production never uses this implementation.
package main

import (
	"sync"
	"sync/atomic"
	"time"
)

type legacySIPJitterBuffer struct {
	nowTime              time.Time
	mu                   sync.Mutex
	packets              map[uint16][]byte
	expected             uint16
	started              bool
	primed               bool
	buffered             int
	missing              int
	primeTicks           int
	arrivals             map[uint16]time.Time
	lost                 int
	droppedSamples       int
	lateDroppedSamples   int
	missingFilledSamples atomic.Int64
	maxAgeMS             int
	jitterMS             float64
	lastArrival          time.Time
	lastTimestamp        uint32
}

func newLegacySIPJitterBuffer() *legacySIPJitterBuffer {
	return &legacySIPJitterBuffer{packets: make(map[uint16][]byte), arrivals: make(map[uint16]time.Time)}
}

func (b *legacySIPJitterBuffer) push(sequence uint16, payload []byte) {
	b.pushRTP(sequence, payload, 0, time.Now())
}

func (b *legacySIPJitterBuffer) pushRTP(sequence uint16, payload []byte, timestamp uint32, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		b.started = true
		b.expected = sequence
	}
	if b.primed && legacySIPSequenceBefore(sequence, b.expected) {
		if len(b.packets) != 0 || timestamp == 0 || int32(timestamp-b.lastTimestamp) <= 0 || now.Sub(b.lastArrival) < 60*time.Millisecond {
			b.lateDroppedSamples += len(payload)
			return
		}
		b.expected = sequence
		b.primed = false
		b.primeTicks = 0
		b.missing = 0
	}
	if !b.primed && legacySIPSequenceBefore(sequence, b.expected) {
		b.expected = sequence
	}
	if _, exists := b.packets[sequence]; exists {
		return
	}
	if !b.lastArrival.IsZero() && timestamp != 0 {
		variation := now.Sub(b.lastArrival).Seconds()*1000 - float64(int32(timestamp-b.lastTimestamp))/8
		if variation < 0 {
			variation = -variation
		}
		b.jitterMS += (variation - b.jitterMS) / 16
	}
	b.lastArrival, b.lastTimestamp = now, timestamp
	if len(payload) > 960 {
		b.droppedSamples += len(payload)
		return
	}
	// Keep recent speech, not an old backlog, under overload or a clock jump.
	for len(b.packets) > 0 && (b.buffered+len(payload) > 960 || len(b.packets) >= 12) {
		oldest := b.oldest()
		b.droppedSamples += len(b.packets[oldest])
		b.buffered -= len(b.packets[oldest])
		delete(b.packets, oldest)
		delete(b.arrivals, oldest)
		b.expected = oldest + 1
	}
	b.packets[sequence] = append([]byte(nil), payload...)
	b.arrivals[sequence] = now
	b.buffered += len(payload)
	if b.buffered >= sipRTPPrimeSamples {
		b.primed = true
	}
}

// Caller holds b.mu.
func (b *legacySIPJitterBuffer) oldest() uint16 {
	var oldest uint16
	first := true
	for seq := range b.packets {
		if first || legacySIPSequenceBefore(seq, oldest) {
			oldest = seq
			first = false
		}
	}
	return oldest
}
func legacySIPSequenceBefore(left, right uint16) bool {
	return int16(left-right) < 0
}

func (b *legacySIPJitterBuffer) pop() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.primed {
		if len(b.packets) == 0 {
			return nil, false
		}
		b.primeTicks++
		if b.primeTicks < 3 {
			return nil, false
		}
		b.primed = true
	}
	now := b.nowTime
	if now.IsZero() {
		now = time.Now()
	}
	for len(b.packets) > 0 {
		oldest := b.oldest()
		age := now.Sub(b.arrivals[oldest])
		if int(age/time.Millisecond) > b.maxAgeMS {
			b.maxAgeMS = int(age / time.Millisecond)
		}
		if age <= 120*time.Millisecond {
			break
		}
		b.droppedSamples += len(b.packets[oldest])
		b.buffered -= len(b.packets[oldest])
		delete(b.packets, oldest)
		delete(b.arrivals, oldest)
		b.expected = oldest + 1
	}
	payload, ok := b.packets[b.expected]
	delete(b.packets, b.expected)
	delete(b.arrivals, b.expected)
	b.buffered -= len(payload)
	b.expected++
	if ok {
		b.missing = 0
	} else {
		b.missing++
		b.lost++
	}
	// After sustained silence, re-prime from the next packet instead of
	// inventing sequence numbers indefinitely (RTP DTX advances timestamps only).
	if b.missing >= 10 && len(b.packets) == 0 {
		b.started = false
		b.primed = false
		b.missing = 0
		b.primeTicks = 0
	}
	if ok {
		return payload, true
	}
	return nil, true
}

type legacySIPAudioFramer struct {
	jitter         *legacySIPJitterBuffer
	codec          string
	missingSamples int
	pending        []byte
	pendingPCM     []int16
}

func newLegacySIPAudioFramer(jitter *legacySIPJitterBuffer, offer sipMediaOffer) *legacySIPAudioFramer {
	missingSamples := offer.PacketSamples
	if missingSamples <= 0 {
		missingSamples = sipRTPPacketSamples
	}
	return &legacySIPAudioFramer{jitter: jitter, codec: offer.Codec, missingSamples: missingSamples}
}

// Decode actual packets before framing. Missing G.722 packets become PCM
// silence, never G.711 bytes accidentally interpreted by a stateful decoder.
func (f *legacySIPAudioFramer) popPCM(media *sipRTPMedia) ([]int16, bool) {
	samples := media.sampleRate() / 50
	for len(f.pendingPCM) < samples {
		payload, ready := f.jitter.pop()
		if !ready {
			return nil, false
		}
		var pcm []int16
		if payload == nil {
			f.jitter.missingFilledSamples.Add(int64(f.missingSamples))
			pcm = make([]int16, f.missingSamples*media.sampleRate()/8000)
		} else {
			pcm = media.decodeAudio(payload)
		}
		f.pendingPCM = append(f.pendingPCM, pcm...)
	}
	out := append([]int16(nil), f.pendingPCM[:samples]...)
	f.pendingPCM = f.pendingPCM[samples:]
	return out, true
}

func (f *legacySIPAudioFramer) pop() ([]byte, bool) {
	for len(f.pending) < sipRTPPacketSamples {
		payload, ready := f.jitter.pop()
		if !ready {
			return nil, false
		}
		if payload == nil {
			f.jitter.missingFilledSamples.Add(int64(f.missingSamples))
			payload = sipEncodedSilence(f.codec, f.missingSamples)
		}
		f.pending = append(f.pending, payload...)
	}
	out := append([]byte(nil), f.pending[:sipRTPPacketSamples]...)
	f.pending = f.pending[sipRTPPacketSamples:]
	return out, true
}
