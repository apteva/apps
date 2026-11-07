package main

import (
	"testing"
	"time"
)

func TestSIPKernelArrivalPreservesResidenceAndRejectsInvalidClock(t *testing.T) {
	read := time.Now()
	for _, age := range []time.Duration{0, 250 * time.Millisecond, 10 * time.Second} {
		stamp := read.Add(-age)
		at, ok := sipKernelArrival(stamp.Unix(), int64(stamp.Nanosecond()), read)
		if !ok || read.Sub(at) != age {
			t.Fatal("kernel residence was lost", age, at, ok)
		}
	}
	for _, stamp := range [][2]int64{{0, 0}, {read.Unix(), -1}, {read.Unix(), 1e9}, {read.Unix() + 1, int64(read.Nanosecond())}} {
		at, ok := sipKernelArrival(stamp[0], stamp[1], read)
		if ok || at != read {
			t.Fatal("invalid/future timestamp changed arrival", stamp, at, ok)
		}
	}
}

func TestSIPEarlyReserveRetainsArrivalTimestamp(t *testing.T) {
	m := &sipRTPMedia{early: &sipEarlyReceiver{pending: make(chan sipEarlyPacket, 1)}}
	at := time.Now().Add(-50 * time.Millisecond)
	m.early.pending <- sipEarlyPacket{data: []byte("audio"), at: at}
	buf := make([]byte, 16)
	n, _, err := m.readPacket(buf)
	if err != nil || string(buf[:n]) != "audio" || m.readAt != at || m.readCompletedAt.Before(at) {
		t.Fatal("handoff erased arrival timing", m.readAt, m.readCompletedAt, err)
	}
}
