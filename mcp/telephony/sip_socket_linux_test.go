//go:build linux

package main

import (
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSIPLinuxKernelTimestampSeparatesUnreadSocketWait(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	m := &sipRTPMedia{conn: conn}
	if !enableSIPSocketTimestamps(conn) {
		t.Fatal("SO_TIMESTAMPNS unavailable on Linux")
	}
	m.observations.kernelTimestampAvailable.Store(true)
	// Linux enables network timestamping through a deferred global static key.
	// Let that setup complete before sending the measured queued datagram; the
	// initial packet can otherwise receive a lazy timestamp at dequeue.
	time.Sleep(100 * time.Millisecond)
	sender, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	before := time.Now()
	if _, err := sender.Write([]byte("audio")); err != nil {
		t.Fatal(err)
	}
	// Intentional unread residence, not an assumed network delay.
	time.Sleep(60 * time.Millisecond)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 32)
	n, _, err := m.readPacket(buf)
	if err != nil || string(buf[:n]) != "audio" {
		t.Fatal("payload changed", err)
	}
	if m.readAt.Before(before) || m.readCompletedAt.Sub(m.readAt) < 40*time.Millisecond || m.observations.kernelTimestampPackets.Load() != 1 {
		t.Fatal("kernel timestamp or socket residence missing", m.readAt, m.readCompletedAt, m.observations.snapshot(time.Now()))
	}
	s := m.observations.snapshot(time.Now())
	if !s.KernelTimestampAvailable || s.MaxSocketWaitMS < 40 || s.KernelTimestampInvalid != 0 {
		t.Fatal("live diagnostics lost timing", s)
	}
	// Disabling timestamp support falls back to ordinary read time safely.
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var optionErr error
	if err = raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 0) }); err != nil || optionErr != nil {
		t.Fatal(err, optionErr)
	}
	if _, err := sender.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	n, _, err = m.readPacket(buf)
	if err != nil || string(buf[:n]) != "next" || m.readAt != m.readCompletedAt || m.observations.kernelTimestampPackets.Load() != 1 {
		t.Fatal("second read failed", err)
	}
}

func TestSIPLinuxStaleSocketBacklogCannotBecomeFreshAtRead(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	m := &sipRTPMedia{conn: conn}
	if !enableSIPSocketTimestamps(conn) {
		t.Fatal("SO_TIMESTAMPNS unavailable")
	}
	// Complete the kernel's deferred timestamp activation before this isolated
	// test sends the deliberately stale datagram. No production sleep is added.
	time.Sleep(100 * time.Millisecond)
	sender, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	sender.Write(make([]byte, 160))
	time.Sleep(250 * time.Millisecond)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	n, _, err := m.readPacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	b := newRTPJitterBuffer()
	b.pushSourceRTPAt(1, 1, buf[:n], 160, m.readAt, m.readCompletedAt)
	if b.lateDroppedSamples != 160 || len(b.packets) != 0 || b.events[0].Reason != "ingress_processing_age" || b.events[0].LocalQueueAgeMS < 200 {
		t.Fatal("stale datagram was relabeled fresh", b.events[0])
	}
}
