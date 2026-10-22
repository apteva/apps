package main

import (
	"sync/atomic"
	"time"
)

// Optional header-only capture. At most 64 enabled call traces process-wide,
// 1,000 fixed slots per call. No audio, network addresses, SDP or credentials.
var sipPacketTraceSlots atomic.Int64

type sipPacketTiming struct {
	Sequence           uint16  `json:"sequence"`
	Timestamp          uint32  `json:"rtp_timestamp"`
	ArrivalNS          int64   `json:"arrival_unix_ns"`
	SSRC               uint32  `json:"ssrc"`
	PayloadType        int     `json:"payload_type"`
	PlayedNS           int64   `json:"played_unix_ns,omitempty"`
	HandlerDelayMS     float64 `json:"handler_delay_ms"`
	RelativeDeliveryMS float64 `json:"relative_delivery_ms"`
	PayloadSamples     int     `json:"payload_samples_8khz"`
	QueueMS            float64 `json:"queue_ms"`
	Decision           string  `json:"decision"`
}
type sipPacketTrace struct {
	ssrc        uint32
	payloadType int
	slots       []sipPacketTiming
	head, size  int
	released    bool
}

func newSIPPacketTrace(window int) *sipPacketTrace {
	for {
		n := sipPacketTraceSlots.Load()
		if n >= 64 {
			return nil
		}
		if sipPacketTraceSlots.CompareAndSwap(n, n+1) {
			break
		}
	}
	return &sipPacketTrace{slots: make([]sipPacketTiming, min(1000, max(1, window/20)))}
}

// Caller holds jitter.mu. UTC formatting is deferred to the snapshot collector.
func (t *sipPacketTrace) insert(seq uint16, ts uint32, arrival, processed time.Time, samples, queue int, relative time.Duration) {
	t.slots[t.head] = sipPacketTiming{Sequence: seq, Timestamp: ts, ArrivalNS: arrival.UnixNano(), SSRC: t.ssrc, PayloadType: t.payloadType, HandlerDelayMS: max(0, float64(processed.Sub(arrival))/float64(time.Millisecond)), RelativeDeliveryMS: float64(relative) / float64(time.Millisecond), PayloadSamples: samples, QueueMS: float64(queue) / 8, Decision: "admitted"}
	t.head = (t.head + 1) % len(t.slots)
	t.size = min(t.size+1, len(t.slots))
}
func (t *sipPacketTrace) decision(seq uint16, ts uint32, reason string) {
	if t.size == 0 {
		return
	}
	for n := 0; n < min(t.size, 32); n++ {
		i := (t.head + len(t.slots) - 1 - n) % len(t.slots)
		if t.slots[i].Sequence == seq && t.slots[i].Timestamp == ts {
			t.slots[i].Decision = reason
			return
		}
	}
}
func (t *sipPacketTrace) render(ts uint32, now time.Time) {
	for n := 0; n < min(t.size, 32); n++ {
		i := (t.head + len(t.slots) - 1 - n) % len(t.slots)
		if t.slots[i].Timestamp == ts {
			t.slots[i].PlayedNS = now.UnixNano()
			return
		}
	}
}
func (b *rtpJitterBuffer) releaseTrace() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.trace != nil && !b.trace.released {
		b.trace.released = true
		sipPacketTraceSlots.Add(-1)
	}
}
func (t *sipPacketTrace) snapshot() []sipPacketTiming {
	out := make([]sipPacketTiming, t.size)
	return t.copyInto(out)
}
func (t *sipPacketTrace) copyInto(out []sipPacketTiming) []sipPacketTiming {
	out = out[:t.size]
	start := (t.head - t.size + len(t.slots)) % len(t.slots)
	n := copy(out, t.slots[start:])
	copy(out[n:], t.slots[:])
	return out
}
func copySIPPacketTiming(j *rtpJitterBuffer) []sipPacketTiming {
	if j == nil || j.trace == nil {
		return nil
	}
	// Allocate before the media lock: no GC assist inside the critical section.
	out := make([]sipPacketTiming, len(j.trace.slots))
	if !j.mu.TryLock() {
		return nil
	}
	out = j.trace.copyInto(out)
	j.mu.Unlock()
	return out
}

// Explicit operator retrieval; normal dashboard polling never copies the ring.
func (a *App) sipPacketTimingForCall(project, call string) []sipPacketTiming {
	a.carrierTelemetry.mu.Lock()
	var found *sipLiveDiagnostics
	for _, source := range a.carrierTelemetry.sources {
		if d, ok := source.(*sipLiveDiagnostics); ok && d.row.ProjectID == project && d.row.ID == call {
			found = d
			break
		}
	}
	a.carrierTelemetry.mu.Unlock()
	if found == nil {
		return nil
	}
	m := found.media.Load()
	if m == nil {
		return nil
	}
	j := m.jitterTelemetry.Load()
	return copySIPPacketTiming(j)
}
