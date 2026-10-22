package main

import (
	"bytes"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSIPRTCPReportsAndIntervalLoss(t *testing.T) {
	m := &sipRTPMedia{}
	at := time.Unix(100, 0)
	for i, seq := range []uint16{1, 2, 4, 5} {
		m.observations.received(&rtp.Packet{Header: rtp.Header{SSRC: 123, SequenceNumber: seq, Timestamp: uint32(seq) * 160}}, 172, at.Add(time.Duration(i)*20*time.Millisecond), true)
	}
	m.rtcp.localSSRC.Store(456)
	raw, err := m.rtcpReport(456, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	packets, err := rtcp.Unmarshal(raw)
	if err != nil || len(packets) != 2 {
		t.Fatal("compound report missing", err)
	}
	rr, ok := packets[0].(*rtcp.ReceiverReport)
	if !ok || len(rr.Reports) != 1 || rr.Reports[0].TotalLost != 1 || rr.Reports[0].FractionLost != 51 || rr.Reports[0].LastSequenceNumber != 5 {
		t.Fatal("incorrect RTCP reception statistics", rr)
	}
	m.observations.received(&rtp.Packet{Header: rtp.Header{SSRC: 123, SequenceNumber: 6, Timestamp: 960}}, 172, at.Add(1100*time.Millisecond), true)
	raw, _ = m.rtcpReport(456, at.Add(2*time.Second))
	packets, _ = rtcp.Unmarshal(raw)
	if packets[0].(*rtcp.ReceiverReport).Reports[0].FractionLost != 0 {
		t.Fatal("cumulative loss reused as new interval loss")
	}
	peer := &rtcp.SenderReport{SSRC: 123, NTPTime: sipNTP(at), RTPTime: 160, Reports: []rtcp.ReceptionReport{{SSRC: 456, FractionLost: 32, Jitter: 80, LastSenderReport: uint32(sipNTP(at.Add(900*time.Millisecond)) >> 16), Delay: uint32(10 * 65536 / 1000)}}}
	raw, _ = peer.Marshal()
	m.consumeRTCP(raw, nil, at.Add(time.Second))
	snapshot := m.rtcp.snapshot()
	if snapshot.ReportsReceived != 1 || snapshot.PeerLostFraction != .125 || snapshot.PeerJitterMS != 10 || snapshot.RTTMS < 89 || snapshot.RTTMS > 91 {
		t.Fatal("peer statistics not retained", snapshot)
	}
	m.observations.sent.Store(3)
	m.rtcp.lastRTP.Store(800)
	m.rtcp.lastSentNS.Store(at.UnixNano())
	m.rtcp.payloadOctets.Store(480)
	raw, _ = m.rtcpReport(456, at.Add(time.Second))
	packets, _ = rtcp.Unmarshal(raw)
	sr, ok := packets[0].(*rtcp.SenderReport)
	if !ok || sr.RTPTime != 8800 || sr.PacketCount != 3 || sr.OctetCount != 480 || sr.SSRC != 456 {
		t.Fatal("incorrect sender report", sr)
	}
	if bytes.Contains(raw, []byte("token")) {
		t.Fatal("credential-bearing report")
	}
}

func TestSIPRTCPSecureReplayForgeryAndMalformed(t *testing.T) {
	offer, key := sipAuditSecureOffer(t)
	security, err := newSIPMediaSecurityWithLocalKey(offer, key)
	if err != nil {
		t.Fatal(err)
	}
	peerOffer := offer
	peerOffer.RemoteKey = key
	peer, err := newSIPMediaSecurityWithLocalKey(peerOffer, offer.RemoteKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := (&rtcp.SenderReport{SSRC: 123, NTPTime: sipNTP(time.Now())}).Marshal()
	encrypted, err := peer.LocalRTCP.EncryptRTCP(nil, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &sipRTPMedia{}
	m.consumeRTCP(encrypted, security, time.Now())
	m.consumeRTCP(encrypted, security, time.Now())
	forged := append([]byte(nil), encrypted...)
	forged[len(forged)-1] ^= 1
	m.consumeRTCP(forged, security, time.Now())
	m.consumeRTCP(make([]byte, 8), nil, time.Now())
	s := m.rtcp.snapshot()
	if s.ReportsReceived != 1 || s.SecurityErrors != 2 || s.Invalid != 1 || m.observations.decodeErrors.Load() != 0 {
		t.Fatal("SRTCP integrity/replay failure affected RTP", s)
	}
}

func TestSIPRTCPUDPSourceValidationAndCleanup(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	cfg := directSIPTestConfig()
	cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
	offer := sipMediaOffer{RemoteAddress: netip.MustParseAddr("127.0.0.1"), RemotePort: peer.LocalAddr().(*net.UDPAddr).Port, RemoteRTCPPort: peer.LocalAddr().(*net.UDPAddr).Port, Codec: "PCMU", PacketSamples: 160}
	m, err := openSIPRTPMedia(cfg, offer)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.startRTCP()
	raw, _ := (&rtcp.SenderReport{SSRC: 123, NTPTime: sipNTP(time.Now())}).Marshal()
	if _, err = peer.WriteToUDP(raw, m.rtcpConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	wrong, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer wrong.Close()
	_, _ = wrong.WriteToUDP(raw, m.rtcpConn.LocalAddr().(*net.UDPAddr))
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) && (m.rtcp.received.Load() == 0 || m.rtcp.rejected.Load() == 0) {
		time.Sleep(time.Millisecond)
	}
	if m.rtcp.received.Load() != 1 || m.rtcp.rejected.Load() != 1 {
		t.Fatal("RTCP endpoint checks failed", m.rtcp.snapshot())
	}
	m.Close()
	select {
	case <-m.rtcp.done:
	case <-time.After(time.Second):
		t.Fatal("RTCP worker leaked on close")
	}
	if m.observations.readErrors.Load() != 0 || m.observations.sendErrors.Load() != 0 {
		t.Fatal("RTCP cleanup classified as live RTP failure")
	}
}

func TestSIPRTCPNegotiation(t *testing.T) {
	cfg := directSIPTestConfig()
	cfg.SRTPMode = sipSRTPDisabled
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	offer := sipMediaOffer{Codec: "PCMU", PayloadType: 0, Direction: "sendrecv", DTMFPayloadType: 101}
	body, err := buildSIPMediaAnswer(cfg, offer, 26000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "a=rtcp:26001 IN IP4 ") {
		t.Fatal("RTCP port not advertised")
	}
	parsed, err := parseSIPMediaOffer(body, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RemoteRTCPPort != 26001 {
		t.Fatal("RTCP peer port lost", parsed.RemoteRTCPPort)
	}
	if _, err = parseSIPMediaOffer(append(body, []byte("a=rtcp-mux-only\r\n")...), cfg); err == nil {
		t.Fatal("unsupported mux-only was accepted")
	}
	bad := bytes.Replace(body, []byte("a=rtcp:26001"), []byte("a=rtcp:99999"), 1)
	if _, err = parseSIPMediaOffer(bad, cfg); err == nil {
		t.Fatal("invalid RTCP port accepted")
	}
}

func TestSIPRTCPAdjacentPortFailureReleasesRTP(t *testing.T) {
	var rtpSocket, rtcpSocket *net.UDPConn
	for i := 0; i < 100; i++ {
		var err error
		rtpSocket, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		port := rtpSocket.LocalAddr().(*net.UDPAddr).Port
		if port%2 != 0 || port == 65535 {
			rtpSocket.Close()
			continue
		}
		rtcpSocket, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + 1})
		if err == nil {
			break
		}
		rtpSocket.Close()
	}
	if rtcpSocket == nil {
		t.Fatal("could not reserve local port pair")
	}
	defer rtcpSocket.Close()
	port := rtpSocket.LocalAddr().(*net.UDPAddr).Port
	rtpSocket.Close()
	cfg := directSIPTestConfig()
	cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPPortMin = port
	cfg.RTPPortMax = port + 1
	offer := sipMediaOffer{RemoteAddress: netip.MustParseAddr("127.0.0.1"), RemotePort: 25000, Codec: "PCMU", PacketSamples: 160}
	if media, err := openSIPRTPMedia(cfg, offer); err == nil {
		media.Close()
		t.Fatal("allocated an incomplete RTP/RTCP pair")
	}
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal("RTP leaked after adjacent port failed", err)
	}
	probe.Close()
}

func BenchmarkSIPRTCPReport(b *testing.B) {
	m := &sipRTPMedia{}
	now := time.Unix(100, 0)
	m.observations.received(&rtp.Packet{Header: rtp.Header{SSRC: 123, SequenceNumber: 1, Timestamp: 160}}, 172, now, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.rtcpReport(456, now); err != nil {
			b.Fatal(err)
		}
	}
}
