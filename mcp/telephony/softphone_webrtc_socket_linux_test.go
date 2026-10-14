//go:build linux

package main

import (
	"encoding/binary"
	"golang.org/x/sys/unix"
	"net"
	"testing"
	"time"
)

func TestRTCLinuxKernelArrivalAndFallback(t *testing.T) {
	d := &rtcSocketTelemetry{}
	network, err := newRTCObservedNet(d)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := network.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !d.available.Load() || !d.dropsAvailable.Load() {
		t.Fatal("Linux kernel options not active")
	}
	time.Sleep(100 * time.Millisecond) // kernel timestamp static-key activation, test only
	sender, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	packet := make([]byte, 32)
	packet[0] = 0x80
	packet[1] = 111
	binary.BigEndian.PutUint32(packet[8:], 123)
	before := time.Now()
	sender.Write(packet)
	time.Sleep(60 * time.Millisecond)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	_, _, err = conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	at, ok := d.take(123, 0, time.Now())
	if !ok || at.Before(before) || time.Since(at) < 40*time.Millisecond || d.maxSocketWait.Load() < int64(40*time.Millisecond) {
		t.Fatal("unread socket residence not measured", at, d.maxSocketWait.Load())
	}
	native := conn.(*rtcObservedUDP)
	raw, _ := native.native.SyscallConn()
	var optionErr error
	err = raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 0) })
	if err != nil || optionErr != nil {
		t.Fatal(err, optionErr)
	}
	count := d.timestampPackets.Load()
	binary.BigEndian.PutUint16(packet[2:], 1)
	sender.Write(packet)
	_, _, err = conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok = d.take(123, 1, time.Now()); !ok || count != d.timestampPackets.Load() {
		t.Fatal("missing ancillary data broke fallback")
	}
	now := time.Now()
	if got := native.observedArrival([]byte{1, 2, 3}, unix.MSG_CTRUNC, now); !got.Equal(now) {
		t.Fatal("truncated ancillary used as time")
	}
}
