//go:build linux

package main

import (
	"encoding/binary"
	"net"

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
func (m *sipRTPMedia) readSocketPacket(buffer []byte) (int, *net.UDPAddr, error) {
	n, oobn, _, addr, err := m.conn.ReadMsgUDP(buffer, m.socketOOB[:])
	if oobn > 0 {
		messages, e := unix.ParseSocketControlMessage(m.socketOOB[:oobn])
		if e == nil {
			for _, msg := range messages {
				if msg.Header.Level == unix.SOL_SOCKET && msg.Header.Type == unix.SO_RXQ_OVFL && len(msg.Data) >= 4 {
					m.observations.kernelDrops.Store(int64(binary.NativeEndian.Uint32(msg.Data)))
				}
			}
		}
	}
	return n, addr, err
}
