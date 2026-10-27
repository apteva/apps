package main

import (
	"time"

	"github.com/pion/rtp"
)

type rtcTimingEvent struct {
	ConnectionID string    `json:"connection_id"`
	PathRevision uint64    `json:"path_revision"`
	QueuePackets int       `json:"queue_packets"`
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"`
	Reason       string    `json:"reason,omitempty"`
	SSRC         uint32    `json:"ssrc"`
	Sequence     uint16    `json:"sequence"`
	Timestamp    uint32    `json:"rtp_timestamp"`
	LastPlayed   uint32    `json:"last_played_timestamp"`
	OffsetMS     float64   `json:"playout_offset_ms"`
	TargetMS     float64   `json:"target_ms"`
	Epoch        uint64    `json:"epoch"`
}
type rtcTimingSnapshot struct {
	Rejections         map[string]uint64 `json:"rejections,omitempty"`
	Resynchronizations uint64            `json:"resynchronizations"`
	DecodedRecoveries  uint64            `json:"decoded_recoveries"`
	BridgeHandoffs     uint64            `json:"bridge_handoffs"`
	TargetMS           float64           `json:"target_ms"`
	Events             []rtcTimingEvent  `json:"events,omitempty"`
}

func (s *rtcMediaStats) timing(e rtcTimingEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ConnectionID = s.rtp.connectionID
	e.PathRevision = s.rtp.pathRevision.Load()
	t := &s.value.Timing
	if e.TargetMS > 0 {
		t.TargetMS = e.TargetMS
	}
	switch e.Kind {
	case "rejected":
		if t.Rejections == nil {
			t.Rejections = map[string]uint64{}
		}
		t.Rejections[e.Reason]++
	case "resynchronized":
		t.Resynchronizations++
	case "decoded":
		t.DecodedRecoveries++
	case "bridge_handoff":
		t.BridgeHandoffs++
	}
	t.Events = append(t.Events, e)
	if len(t.Events) > 32 {
		copy(t.Events, t.Events[len(t.Events)-32:])
		t.Events = t.Events[:32]
	}
}
func (j *rtcJitter) timing(kind, reason string, p *rtp.Packet, now, due time.Time) {
	if j.onTiming != nil {
		j.onTiming(rtcTimingEvent{QueuePackets: len(j.packets), At: now.UTC(), Kind: kind, Reason: reason, SSRC: p.SSRC, Sequence: p.SequenceNumber, Timestamp: p.Timestamp, LastPlayed: j.last, OffsetMS: float64(due.Sub(now)) / float64(time.Millisecond), TargetMS: float64(j.reserve) / float64(time.Millisecond), Epoch: j.epoch})
	}
}
func (j *rtcJitter) reject(reason string, p *rtp.Packet, now, due time.Time) bool {
	j.lastReject = reason
	j.timing("rejected", reason, p, now, due)
	return false
}

type rtcTimingCandidate struct {
	first, last          time.Time
	seq                  uint16
	ts                   uint32
	count                int
	minOffset, maxOffset time.Duration
}

// Only sustained, paced, forward-sequence input can prove a replacement clock.
// Replays, duplicates, batch catch-up, malformed Opus and isolated reorder cannot.
func (j *rtcJitter) considerRecovery(p *rtp.Packet, now, due time.Time, forward bool) bool {
	c := &j.candidate
	if !forward {
		return false
	}
	if !j.lastRecovery.IsZero() && now.Sub(j.lastRecovery) < 2*time.Second {
		*c = rtcTimingCandidate{}
		return false
	}
	if j.validate == nil || !j.validate(p.Payload) {
		*c = rtcTimingCandidate{}
		return false
	}
	offset := now.Sub(due)
	gap := now.Sub(c.last)
	step := time.Duration(int32(p.Timestamp-c.ts)) * time.Second / 48000
	continuous := c.count > 0 && uint16(p.SequenceNumber-c.seq) == 1 && gap >= 5*time.Millisecond && gap <= 120*time.Millisecond && step >= 2500*time.Microsecond && step <= 120*time.Millisecond
	if !continuous || max(c.maxOffset, offset)-min(c.minOffset, offset) > 40*time.Millisecond {
		*c = rtcTimingCandidate{first: now, count: 1, minOffset: offset, maxOffset: offset}
	} else {
		c.count++
		c.minOffset = min(c.minOffset, offset)
		c.maxOffset = max(c.maxOffset, offset)
	}
	c.last = now
	c.seq = p.SequenceNumber
	c.ts = p.Timestamp
	if c.count < 6 || now.Sub(c.first) < 100*time.Millisecond {
		return false
	}
	for _, q := range j.packets {
		j.timing("rejected", "epoch_replaced", q.packet, now, q.due)
		if j.discard != nil {
			j.discard(q.packet)
		}
	}
	j.packets = nil
	j.baseTS = p.Timestamp
	j.base = now.Add(60 * time.Millisecond)
	j.played = false
	j.lastRecovery = now
	j.epoch++
	j.candidate = rtcTimingCandidate{}
	j.distribution = audioDelayDistribution{}
	j.reserve = 60 * time.Millisecond
	j.timing("resynchronized", "persistent_timing_rejection", p, now, due)
	return true
}
