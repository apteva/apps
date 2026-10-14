package main

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

type rtcSocketPacket struct {
	ssrc         uint32
	sequence     uint16
	at, dequeued time.Time
	valid        bool
}

// A PeerConnection owns this bounded collector. ICE/STUN/DTLS and TURN-wrapped
// packets are not reported as RTP. Unknown paths remain explicitly unavailable.
type rtcSocketTelemetry struct {
	mu                               sync.Mutex
	packets                          [128]rtcSocketPacket
	available, dropsAvailable        atomic.Bool
	drops, skipped, timestampPackets atomic.Uint64
	maxSocketWait, maxPionWait       atomic.Int64
}

func (d *rtcSocketTelemetry) record(data []byte, at, dequeued time.Time) {
	if len(data) < 12 || data[0]>>6 != 2 || data[1] >= 192 && data[1] <= 223 {
		return
	}
	ssrc, sequence := binary.BigEndian.Uint32(data[8:12]), binary.BigEndian.Uint16(data[2:4])
	if !d.mu.TryLock() {
		d.skipped.Add(1)
		return
	}
	d.packets[(uint32(sequence)^ssrc)%uint32(len(d.packets))] = rtcSocketPacket{ssrc, sequence, at, dequeued, true}
	d.mu.Unlock()

}
func (d *rtcSocketTelemetry) take(ssrc uint32, sequence uint16, now time.Time) (time.Time, bool) {
	p, ok := d.takePacket(ssrc, sequence, now)
	return p.at, ok
}
func (d *rtcSocketTelemetry) takePacket(ssrc uint32, sequence uint16, now time.Time) (rtcSocketPacket, bool) {
	if !d.mu.TryLock() {
		d.skipped.Add(1)
		return rtcSocketPacket{}, false
	}
	defer d.mu.Unlock()
	p := &d.packets[(uint32(sequence)^ssrc)%uint32(len(d.packets))]
	if !p.valid || p.ssrc != ssrc || p.sequence != sequence || now.Sub(p.dequeued) > time.Second {
		return rtcSocketPacket{}, false
	}
	p.valid = false
	// Only matched, decrypted RTP accepted by Pion contributes media counters.
	if !p.at.Equal(p.dequeued) {
		d.timestampPackets.Add(1)
		atomicMax(&d.maxSocketWait, max(0, p.dequeued.Sub(p.at).Nanoseconds()))
	}
	atomicMax(&d.maxPionWait, max(0, now.Sub(p.dequeued).Nanoseconds()))
	return *p, true
}

type rtcObservedNet struct {
	transport.Net
	telemetry *rtcSocketTelemetry
}

func newRTCObservedNet(d *rtcSocketTelemetry) (*rtcObservedNet, error) {
	base, err := stdnet.NewNet()
	return &rtcObservedNet{base, d}, err
}
func (n *rtcObservedNet) wrap(c transport.UDPConn, err error) (transport.UDPConn, error) {
	if err != nil {
		return c, err
	}
	native, ok := c.(*net.UDPConn)
	if !ok {
		return c, nil
	}
	if enableSIPSocketTimestamps(native) {
		n.telemetry.available.Store(true)
	}
	if enableSIPSocketDropCounter(native) {
		n.telemetry.dropsAvailable.Store(true)
	}
	return &rtcObservedUDP{UDPConn: c, native: native, telemetry: n.telemetry}, nil
}
func (n *rtcObservedNet) ListenUDP(network string, addr *net.UDPAddr) (transport.UDPConn, error) {
	return n.wrap(n.Net.ListenUDP(network, addr))
}
func (n *rtcObservedNet) DialUDP(network string, local, remote *net.UDPAddr) (transport.UDPConn, error) {
	return n.wrap(n.Net.DialUDP(network, local, remote))
}

type rtcObservedUDP struct {
	transport.UDPConn
	native    *net.UDPConn
	telemetry *rtcSocketTelemetry
	lastDrops atomic.Uint32
}

func (c *rtcObservedUDP) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, e := c.ReadFromUDP(b)
	return n, a, e
}
func (c *rtcObservedUDP) Read(b []byte) (int, error) { n, _, e := c.ReadFromUDP(b); return n, e }
func (c *rtcObservedUDP) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	n, a, at, dequeued, e := c.readObserved(b)
	if e == nil {
		c.telemetry.record(b[:n], at, dequeued)
	}
	return n, a, e
}

// Keep the transport's explicit ancillary API intact for callers supplying OOB.
func (c *rtcObservedUDP) ReadMsgUDP(b, oob []byte) (int, int, int, *net.UDPAddr, error) {
	n, on, flags, a, e := c.native.ReadMsgUDP(b, oob)
	now := time.Now()
	at := c.observedArrival(oob[:on], flags, now)
	if e == nil {
		c.telemetry.record(b[:n], at, now)
	}
	return n, on, flags, a, e
}
