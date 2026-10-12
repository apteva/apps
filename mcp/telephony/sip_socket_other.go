//go:build !linux

package main

import "net"

func enableSIPSocketDropCounter(_ *net.UDPConn) bool { return false }
func (m *sipRTPMedia) readSocketPacket(buffer []byte) (int, *net.UDPAddr, error) {
	return m.conn.ReadFromUDP(buffer)
}
