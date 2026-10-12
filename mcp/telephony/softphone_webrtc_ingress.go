package main

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
)

type rtcIngressEvent struct {
	At                time.Time `json:"at"`
	ConnectionID      string    `json:"connection_id"`
	PathRevision      uint64    `json:"path_revision"`
	SSRC              uint32    `json:"ssrc"`
	Sequence          uint16    `json:"sequence"`
	Timestamp         uint32    `json:"rtp_timestamp"`
	PreviousSequence  uint16    `json:"previous_sequence"`
	PreviousTimestamp uint32    `json:"previous_rtp_timestamp"`
	Kind              string    `json:"kind"`
	ArrivalGapMS      float64   `json:"arrival_gap_ms"`
	QueueWaitMS       float64   `json:"queue_wait_ms"`
	QueuePackets      int       `json:"queue_packets"`
	ConcealedMS       int       `json:"concealed_ms"`
}
type rtcIngressSnapshot struct {
	FirstPacketAt string `json:"first_packet_at,omitempty"`
	Packets       uint64 `json:"packets"`
	Padding       uint64 `json:"padding_packets"`
	Skipped       uint64 `json:"observations_skipped"`
	// Pion owns the ICE sockets; absence of kernel counters is explicit.
	SocketDropsAvailable bool              `json:"socket_drops_available"`
	MaxArrivalGapMS      float64           `json:"max_arrival_gap_ms"`
	MaxQueueWaitMS       float64           `json:"max_queue_wait_ms"`
	Events               []rtcIngressEvent `json:"events,omitempty"`
}
type rtcIngressTelemetry struct {
	firstPacket               atomic.Int64
	packets, padding, skipped atomic.Uint64
	maxGap, maxWait           atomic.Int64
	mu                        sync.Mutex
	previous                  rtcIngressEvent
	events                    [64]rtcIngressEvent
	count                     uint64
}

func (d *rtcIngressTelemetry) observe(p *rtp.Packet, at, processed time.Time, queue int, connection string, path uint64, kind string, concealed int) {
	if kind == "received" {
		d.firstPacket.CompareAndSwap(0, at.UnixNano())
		d.packets.Add(1)
	}
	if kind == "padding" {
		d.padding.Add(1)
	}
	wait := max(int64(0), processed.Sub(at).Nanoseconds())
	if kind == "processed" && wait <= int64(20*time.Millisecond) {
		return
	}
	atomicMax(&d.maxWait, wait)
	if !d.mu.TryLock() {
		d.skipped.Add(1)
		return
	}
	defer d.mu.Unlock()
	prev := d.previous
	e := rtcIngressEvent{At: at.UTC(), ConnectionID: connection, PathRevision: path, SSRC: p.SSRC, Sequence: p.SequenceNumber, Timestamp: p.Timestamp, Kind: kind, QueueWaitMS: float64(wait) / 1e6, QueuePackets: queue, ConcealedMS: concealed}
	if prev.SSRC == p.SSRC && !prev.At.IsZero() {
		e.PreviousSequence = prev.Sequence
		e.PreviousTimestamp = prev.Timestamp
		e.ArrivalGapMS = max(0, float64(at.Sub(prev.At))/1e6)
		if kind == "received" || kind == "padding" {
			atomicMax(&d.maxGap, at.Sub(prev.At).Nanoseconds())
		}
	}
	if kind == "received" || kind == "padding" {
		d.previous = e
		// Sequence gaps are transport observations. DTX/padding never imply speech loss.
		if prev.SSRC != p.SSRC || prev.At.IsZero() {
			e.Kind = "stream_started"
		} else if uint16(p.SequenceNumber-prev.Sequence) != 1 {
			e.Kind = "sequence_discontinuity"
		} else if e.ArrivalGapMS > 60 {
			e.Kind = "arrival_gap"
		} else if e.QueueWaitMS > 20 {
			e.Kind = "processing_delay"
		} else {
			return
		}
	}
	d.events[d.count%uint64(len(d.events))] = e
	d.count++
}
func (d *rtcIngressTelemetry) snapshot() rtcIngressSnapshot {
	s := rtcIngressSnapshot{Packets: d.packets.Load(), Padding: d.padding.Load(), Skipped: d.skipped.Load(), MaxArrivalGapMS: float64(d.maxGap.Load()) / 1e6, MaxQueueWaitMS: float64(d.maxWait.Load()) / 1e6}
	if n := d.firstPacket.Load(); n > 0 {
		s.FirstPacketAt = ringTime(time.Unix(0, n))
	}
	if d.mu.TryLock() {
		s.Events = chronologicalRing(d.events[:], d.count)
		d.mu.Unlock()
	} else {
		s.Skipped++
	}
	return s
}
func mergeRTCIngress(a, b rtcIngressSnapshot) rtcIngressSnapshot {
	if a.FirstPacketAt == "" {
		a.FirstPacketAt = b.FirstPacketAt
	}
	a.Packets += b.Packets
	a.Padding += b.Padding
	a.Skipped += b.Skipped
	a.MaxArrivalGapMS = max(a.MaxArrivalGapMS, b.MaxArrivalGapMS)
	a.MaxQueueWaitMS = max(a.MaxQueueWaitMS, b.MaxQueueWaitMS)
	a.Events = append(a.Events, b.Events...)
	if len(a.Events) > 64 {
		a.Events = a.Events[len(a.Events)-64:]
	}
	return a
}
