package main

import (
	"sync"
	"time"
)

type sipReceiveEvidence struct {
	Arrival   time.Time `json:"kernel_arrival_at"`
	Read      time.Time `json:"socket_read_at"`
	Handler   time.Time `json:"handler_at"`
	Sequence  uint16    `json:"sequence"`
	Timestamp uint32    `json:"rtp_timestamp"`
	SSRC      uint32    `json:"ssrc"`
	SocketMS  float64   `json:"socket_residence_ms"`
	HandlerMS float64   `json:"handler_delay_ms"`
}
type sipReceiveWindow struct {
	mu          sync.Mutex
	last        time.Time
	items       [32]sipReceiveEvidence
	head, count int
}

// Sample once per second, or at most four times per second during incidents.
// This is evidence only: contention skips it, never waits or changes admission.
func (w *sipReceiveWindow) observe(seq uint16, ts, ssrc uint32, at, read, handler time.Time) {
	if at.IsZero() || read.Before(at) || handler.Before(read) || !w.mu.TryLock() {
		return
	}
	defer w.mu.Unlock()
	interval := time.Second
	if handler.Sub(at) > 40*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	if !w.last.IsZero() && handler.Sub(w.last) < interval {
		return
	}
	w.items[w.head] = sipReceiveEvidence{at.UTC(), read.UTC(), handler.UTC(), seq, ts, ssrc, float64(read.Sub(at)) / 1e6, float64(handler.Sub(read)) / 1e6}
	w.head = (w.head + 1) % len(w.items)
	w.count = min(w.count+1, len(w.items))
	w.last = handler
}
func (w *sipReceiveWindow) snapshot() []sipReceiveEvidence {
	if !w.mu.TryLock() {
		return nil
	}
	items, head, count := w.items, w.head, w.count
	w.mu.Unlock()
	if count == 0 {
		return nil
	}
	out := make([]sipReceiveEvidence, count)
	for i := range out {
		out[i] = items[(head-count+i+len(items))%len(items)]
	}
	return out
}
