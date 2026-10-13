//go:build linux

package main

import (
	"encoding/binary"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

func enableSIPSocketDropCounter(conn *net.UDPConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var optionErr error
	err = raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1) })
	return err == nil && optionErr == nil
}
func enableSIPSocketTimestamps(conn *net.UDPConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var optionErr error
	err = raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1) })
	return err == nil && optionErr == nil
}
func (m *sipRTPMedia) readSocketPacket(buffer []byte) (int, *net.UDPAddr, error) {
	n, oobn, flags, addr, err := m.conn.ReadMsgUDP(buffer, m.socketOOB[:])
	m.readCompletedAt = time.Now()
	m.readAt = m.readCompletedAt
	if flags&unix.MSG_CTRUNC != 0 {
		m.observations.kernelTimestampInvalid.Add(1)
	} else {
		// Parse one message at a time: no ancillary-message slice allocation.
		for data := m.socketOOB[:oobn]; len(data) >= unix.CmsgLen(0); {
			header, payload, rest, e := unix.ParseOneSocketControlMessage(data)
			if e != nil {
				m.observations.kernelTimestampInvalid.Add(1)
				break
			}
			data = rest
			if header.Level != unix.SOL_SOCKET {
				continue
			}
			switch header.Type {
			case unix.SO_RXQ_OVFL:
				if len(payload) >= 4 {
					m.observations.kernelDrops.Store(int64(binary.NativeEndian.Uint32(payload)))
				}
			case unix.SCM_TIMESTAMPNS:
				// Supported Linux amd64/arm64 both use a 64-bit timespec.
				if len(payload) < 16 {
					m.observations.kernelTimestampInvalid.Add(1)
					continue
				}
				at, valid := sipKernelArrival(int64(binary.NativeEndian.Uint64(payload)), int64(binary.NativeEndian.Uint64(payload[8:])), m.readCompletedAt)
				if !valid {
					m.observations.kernelTimestampInvalid.Add(1)
					continue
				}
				m.readAt = at
				m.observations.kernelTimestampPackets.Add(1)
				wait := m.readCompletedAt.Sub(at).Nanoseconds()
				m.observations.lastSocketWait.Store(wait)
				atomicMax(&m.observations.maxSocketWait, wait)
			}
		}
	}
	return n, addr, err
}
