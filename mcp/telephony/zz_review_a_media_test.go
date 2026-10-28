package main

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// RTCP TotalLost is cumulative: observation-lock contention must never reset
// the per-stream loss base or first sequence, and skipped packets are not loss.
func TestZZReviewARTCPCumulativeLossNeverDecreasesOnContention(t *testing.T) {
	m := &sipRTPMedia{}
	m.rtcp.localSSRC.Store(456)
	at := time.Unix(100, 0)
	packet := func(seq uint16) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{SSRC: 123, SequenceNumber: seq, Timestamp: uint32(seq) * 160}}
	}
	report := func(now time.Time) rtcp.ReceptionReport {
		raw, err := m.rtcpReport(456, now)
		if err != nil {
			t.Fatal(err)
		}
		packets, _ := rtcp.Unmarshal(raw)
		return packets[0].(*rtcp.ReceiverReport).Reports[0]
	}
	for seq := uint16(1); seq <= 20; seq++ {
		if seq == 5 || seq == 6 {
			continue // two real losses
		}
		m.observations.received(packet(seq), 172, at.Add(time.Duration(seq)*20*time.Millisecond), true)
	}
	first := report(at.Add(time.Second))
	if first.TotalLost != 2 {
		t.Fatal("setup", first.TotalLost)
	}
	m.observations.timingMu.Lock()
	for seq := uint16(21); seq <= 25; seq++ {
		m.observations.received(packet(seq), 172, at.Add(time.Duration(seq)*20*time.Millisecond), true)
	}
	m.observations.timingMu.Unlock()
	for seq := uint16(26); seq <= 40; seq++ {
		m.observations.received(packet(seq), 172, at.Add(time.Duration(seq)*20*time.Millisecond), true)
	}
	second := report(at.Add(2 * time.Second))
	if second.TotalLost < first.TotalLost || second.TotalLost != 2 || second.LastSequenceNumber != 40 {
		t.Fatalf("cumulative RTCP loss went backwards or counted skipped packets: %d -> %d (last %d)", first.TotalLost, second.TotalLost, second.LastSequenceNumber)
	}
	if m.observations.first != 1 {
		t.Fatal("stream start reset by contention", m.observations.first)
	}
}

func TestZZReviewALocalRecordingOrderDedupAndLoss(t *testing.T) {
	type out struct {
		seq     int
		silence int
	}
	var got []out
	emit := func(p []byte, silence int) {
		if p == nil {
			got = append(got, out{-1, silence})
			return
		}
		got = append(got, out{int(p[0]), 0})
	}
	var o sipLocalRTPOrder
	push := func(seq uint16) {
		p := make([]byte, 160)
		p[0] = byte(seq)
		o.push(seq, uint32(seq)*160, p, emit)
	}
	// 1 2 4 3 3 (dup) 5 [6 lost] 7 8 9 10 11 2 (very late dup) 12
	for _, seq := range []uint16{1, 2, 4, 3, 3, 5, 7, 8, 9, 10, 11, 2, 12} {
		push(seq)
	}
	o.flush(emit)
	want := []out{{1, 0}, {2, 0}, {3, 0}, {4, 0}, {5, 0}, {-1, 160}, {7, 0}, {8, 0}, {9, 0}, {10, 0}, {11, 0}, {12, 0}}
	if len(got) != len(want) {
		t.Fatalf("timeline %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timeline %v, want %v", got, want)
		}
	}
	// A far-behind sender rebase starts a new run instead of being discarded.
	got = nil
	for _, seq := range []uint16{40000, 40001, 40002} {
		push(seq)
	}
	o.flush(emit)
	if len(got) != 3 {
		t.Fatal("rebased run lost", got)
	}
}
