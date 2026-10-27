// Frozen 0.12.59 jitter controller (10e65c5b), used only for before/after replays.
package main

import (
	"github.com/pion/rtp"
	"time"
)

type v59RTCJitter struct {
	baseTS  uint32
	base    time.Time
	last    uint32
	played  bool
	packets []v59RTCPacket
	arrival time.Time
	discard func(*rtp.Packet)
}
type v59RTCPacket struct {
	packet *rtp.Packet
	due    time.Time
}

func (j *v59RTCJitter) push(p *rtp.Packet, now time.Time) bool {
	if len(p.Payload) == 0 || len(p.Payload) > 1275 {
		return false
	}
	if j.base.IsZero() {
		j.baseTS = p.Timestamp
		j.base = now.Add(60 * time.Millisecond)
	}
	delta := int32(p.Timestamp - j.baseTS)
	// A discontinuous source timestamp starts a fresh bounded epoch.
	if delta < -48000 || delta > 48000*3600 {
		j.baseTS = p.Timestamp
		j.base = now.Add(60 * time.Millisecond)
		j.played = false
		j.packets = nil
		delta = 0
	}
	due := j.base.Add(time.Duration(delta) * time.Second / 48000)
	// Follow oscillator drift slowly (at most 200ppm); a delayed batch cannot
	// move the timeline by seconds. Earlier delivery can reduce the cushion.
	desired := now.Add(60*time.Millisecond - time.Duration(delta)*time.Second/48000)
	if desired.Before(j.base) {
		j.base = desired
	} else if !j.arrival.IsZero() {
		j.base = j.base.Add(min(desired.Sub(j.base), max(0, now.Sub(j.arrival))/5000))
	}
	j.arrival = now
	due = j.base.Add(time.Duration(delta) * time.Second / 48000)
	if j.played && int32(p.Timestamp-j.last) <= 0 || now.Sub(due) > 320*time.Millisecond || due.Sub(now) > 320*time.Millisecond {
		return false
	}
	for _, queued := range j.packets {
		if queued.packet.Timestamp == p.Timestamp {
			return false
		}
	}
	if len(j.packets) >= 16 {
		return false
	}
	payload := append([]byte(nil), p.Payload...)
	cp := *p
	cp.Payload = payload
	j.packets = append(j.packets, v59RTCPacket{&cp, due})
	for n := len(j.packets) - 1; n > 0 && j.packets[n].due.Before(j.packets[n-1].due); n-- {
		j.packets[n], j.packets[n-1] = j.packets[n-1], j.packets[n]
	}
	return true
}
func (j *v59RTCJitter) pop(now time.Time) *rtp.Packet {
	for len(j.packets) > 0 && !j.packets[0].due.After(now) {
		q := j.packets[0]
		j.packets = j.packets[1:]
		if now.Sub(q.due) > 320*time.Millisecond || (j.played && int32(q.packet.Timestamp-j.last) <= 0) {
			if j.discard != nil {
				j.discard(q.packet)
			}
			continue
		}
		j.last = q.packet.Timestamp
		j.played = true
		return q.packet
	}
	return nil
}
