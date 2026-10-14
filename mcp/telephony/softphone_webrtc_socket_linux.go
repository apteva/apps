//go:build linux

package main

import (
	"encoding/binary"
	"golang.org/x/sys/unix"
	"net"
	"time"
)

func (c *rtcObservedUDP) readObserved(b []byte) (int, *net.UDPAddr, time.Time, time.Time, error) {
	var oob [128]byte
	n, on, flags, addr, err := c.native.ReadMsgUDP(b, oob[:])
	now := time.Now()
	return n, addr, c.observedArrival(oob[:on], flags, now), now, err
}
func (c *rtcObservedUDP) observedArrival(oob []byte, flags int, now time.Time) time.Time {
	at := now
	if flags&unix.MSG_CTRUNC != 0 {
		return at
	}
	for len(oob) >= unix.CmsgLen(0) {
		h, p, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			break
		}
		oob = rest
		if h.Level != unix.SOL_SOCKET {
			continue
		}
		if h.Type == unix.SO_RXQ_OVFL && len(p) >= 4 {
			value := binary.NativeEndian.Uint32(p)
			old := c.lastDrops.Swap(value)
			c.telemetry.drops.Add(uint64(value - old))
		}
		if h.Type == unix.SCM_TIMESTAMPNS && len(p) >= 16 {
			if t, ok := sipKernelArrival(int64(binary.NativeEndian.Uint64(p)), int64(binary.NativeEndian.Uint64(p[8:])), now); ok {
				at = t
			}
		}
	}
	return at
}
