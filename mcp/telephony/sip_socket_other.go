//go:build !linux

package main

import (
	"net"
	"time"
)

func enableSIPSocketDropCounter(_ *net.UDPConn) bool { return false }
func enableSIPSocketTimestamps(_ *net.UDPConn) bool  { return false }
func (m *sipRTPMedia) readSocketPacket(buffer []byte) (int, *net.UDPAddr, error) {
	n, addr, err := m.conn.ReadFromUDP(buffer)
	m.readAt = time.Now()
	m.readCompletedAt = m.readAt
	return n, addr, err
}
