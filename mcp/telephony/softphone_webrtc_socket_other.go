//go:build !linux

package main

import (
	"net"
	"time"
)

func (c *rtcObservedUDP) readObserved(b []byte) (int, *net.UDPAddr, time.Time, time.Time, error) {
	n, a, e := c.native.ReadFromUDP(b)
	now := time.Now()
	return n, a, now, now, e
}
func (c *rtcObservedUDP) observedArrival(_ []byte, _ int, now time.Time) time.Time { return now }
