package main

import (
	"bytes"
	sdk "github.com/apteva/app-sdk"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourcePolicySIPRTPAndRTCPPreservesPackets(t *testing.T) {
	for _, size := range []int{256 << 10, 512 << 10, 1 << 20} {
		t.Run(stringSize(size), func(t *testing.T) {
			cfg := directSIPTestConfig()
			cfg.RTPPortMin = 0
			cfg.RTPPortMax = 0
			cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
			applied := 0
			cfg.socketPolicy = func(c *net.UDPConn) sdk.SocketPolicyResult {
				applied++
				return sdk.ApplyUDPSocketPolicy(c, sdk.SocketPolicy{ReceiveBytes: size, SendBytes: 32 << 10})
			}
			for _, codec := range []string{"PCMA", "G722"} {
				m, e := openSIPRTPMedia(cfg, sipMediaOffer{RemoteAddress: cfg.RTPBindIP, RemotePort: 9000, Codec: codec, PacketSamples: 160})
				if e != nil {
					t.Fatal(e)
				}
				for _, socket := range []*net.UDPConn{m.conn, m.rtcpConn} {
					sender, e := net.DialUDP("udp4", nil, socket.LocalAddr().(*net.UDPAddr))
					if e != nil {
						t.Fatal(e)
					}
					for i := 0; i < 25; i++ {
						packet := bytes.Repeat([]byte{byte(i)}, 172)
						if _, e = sender.Write(packet); e != nil {
							t.Fatal(e)
						}
						_ = socket.SetReadDeadline(time.Now().Add(time.Second))
						got := make([]byte, 512)
						n, _, e := socket.ReadFromUDP(got)
						if e != nil || !bytes.Equal(got[:n], packet) {
							t.Fatal("packet changed", codec, i, e)
						}
					}
					sender.Close()
				}
				m.Close()
			}
			if applied != 4 {
				t.Fatalf("RTP/RTCP policy coverage %d", applied)
			}
		})
	}
}
func stringSize(size int) string {
	switch size {
	case 256 << 10:
		return "256KiB"
	case 512 << 10:
		return "512KiB"
	default:
		return "1MiB"
	}
}
func TestResourcePolicyRTCWithoutTelemetryAndConcurrentSockets(t *testing.T) {
	var applied atomic.Int64
	network, e := newRTCObservedNet(nil)
	if e != nil {
		t.Fatal(e)
	}
	network.socketPolicy = func(c *net.UDPConn) sdk.SocketPolicyResult {
		applied.Add(1)
		d := 46
		return sdk.ApplyUDPSocketPolicy(c, sdk.SocketPolicy{ReceiveBytes: 256 << 10, DSCP: &d})
	}
	// Listener/coaching has no ingress collector: tuning must still run.
	for i := 0; i < 10; i++ {
		c, e := network.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if e != nil {
			t.Fatal(e)
		}
		c.Close()
	}
	if applied.Load() != 10 {
		t.Fatal(applied.Load())
	}
}
func TestResourcePolicySocketFailureDoesNotCloseMedia(t *testing.T) {
	network, e := newRTCObservedNet(nil)
	if e != nil {
		t.Fatal(e)
	}
	network.socketPolicy = func(c *net.UDPConn) sdk.SocketPolicyResult {
		return sdk.ApplyUDPSocketPolicy(c, sdk.SocketPolicy{ReceiveBytes: 1 << 30})
	}
	c, e := network.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	peer, e := net.DialUDP("udp4", nil, c.LocalAddr().(*net.UDPAddr))
	if e != nil {
		t.Fatal(e)
	}
	defer peer.Close()
	peer.Write([]byte("media"))
	c.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 10)
	n, _, e := c.ReadFromUDP(b)
	if e != nil || string(b[:n]) != "media" {
		t.Fatal(n, e)
	}
}
