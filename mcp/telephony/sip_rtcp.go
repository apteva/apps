package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"github.com/pion/rtcp"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// RTP+1 is negotiated explicitly. RTP capture/playout never waits for RTCP,
// logging, a database or a provider. One low-rate worker owns each RTCP socket
// and SRTCP contexts. Hand-built media without an RTCP socket remains supported.
type sipRTCPState struct {
	started                                                       atomic.Bool
	reportStream                                                  uint32
	expectedPrior, lostPrior                                      int64
	once                                                          sync.Once
	done                                                          chan struct{}
	received, sent, invalid, rejected, securityErrors, sendErrors atomic.Int64
	senderNTP                                                     atomic.Uint64
	senderReceivedNS                                              atomic.Int64
	localSSRC                                                     atomic.Uint32
	lastRTP                                                       atomic.Uint32
	lastSentNS                                                    atomic.Int64
	payloadOctets                                                 atomic.Uint32
	peerLost                                                      atomic.Uint32
	peerJitter                                                    atomic.Uint32
	rttNS                                                         atomic.Int64
}
type sipRTCPSnapshot struct {
	Mode               string  `json:"mode"`
	ReportsReceived    int64   `json:"reports_received"`
	ReportsSent        int64   `json:"reports_sent"`
	Invalid            int64   `json:"invalid_reports"`
	RejectedSource     int64   `json:"rejected_source_reports"`
	SecurityErrors     int64   `json:"security_errors"`
	SendErrors         int64   `json:"send_errors"`
	PeerLostFraction   float64 `json:"peer_lost_fraction"`
	PeerJitterMS       float64 `json:"peer_jitter_ms"`
	RTTMS              float64 `json:"rtt_ms"`
	LastSenderReportAt string  `json:"last_sender_report_at,omitempty"`
}

func (s *sipRTCPState) snapshot() sipRTCPSnapshot {
	mode := "not_started"
	if s.started.Load() || s.sent.Load() > 0 || s.received.Load() > 0 {
		mode = "rtp_plus_one"
	}
	v := sipRTCPSnapshot{Mode: mode, ReportsReceived: s.received.Load(), ReportsSent: s.sent.Load(), Invalid: s.invalid.Load(), RejectedSource: s.rejected.Load(), SecurityErrors: s.securityErrors.Load(), SendErrors: s.sendErrors.Load(), PeerLostFraction: float64(s.peerLost.Load()) / 256, PeerJitterMS: float64(s.peerJitter.Load()) / 8, RTTMS: float64(s.rttNS.Load()) / 1e6}
	if n := s.senderReceivedNS.Load(); n > 0 {
		v.LastSenderReportAt = ringTime(time.Unix(0, n))
	}
	return v
}
func sipNTP(now time.Time) uint64 {
	seconds := uint64(now.Unix() + 2208988800)
	return seconds<<32 | uint64(now.Nanosecond())*(1<<32)/1e9
}
func sipRTCPPeer(o sipMediaOffer) *net.UDPAddr {
	ip := o.RemoteRTCPAddress
	if !ip.IsValid() {
		ip = o.RemoteAddress
	}
	port := o.RemoteRTCPPort
	if port == 0 {
		port = o.RemotePort + 1
	}
	if !ip.IsValid() || port < 1 || port > 65535 {
		return nil
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
}
func (m *sipRTPMedia) startRTCP() {
	if m.rtcpConn == nil {
		return
	}
	m.rtcp.once.Do(func() {
		// Final SDP and security are immutable by the time the real RTP reader owns
		// media. Provisional outbound packets are drained without starting RTCP.
		peer := sipRTCPPeer(m.offer)
		security := m.security
		m.rtcp.done = make(chan struct{})
		go func() {
			defer close(m.rtcp.done)
			if peer == nil {
				return
			}
			if security != nil && (security.LocalRTCP == nil || security.RemoteRTCP == nil) {
				m.rtcp.securityErrors.Add(1)
				return
			}
			m.rtcp.started.Store(true)
			var seed [4]byte
			if _, err := rand.Read(seed[:]); err != nil {
				m.rtcp.invalid.Add(1)
				return
			}
			reporter := binary.BigEndian.Uint32(seed[:])
			if reporter == 0 {
				reporter = 1
			}
			buffer := make([]byte, 4096)
			// Spread initial reports across calls; subsequent intervals are five seconds.
			next := time.Now().Add(5*time.Second + time.Duration(reporter%1000)*time.Millisecond)
			for {
				_ = m.rtcpConn.SetReadDeadline(next)
				n, source, err := m.rtcpConn.ReadFromUDP(buffer)
				if err != nil {
					if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
						return
					}
				} else if source.IP.Equal(peer.IP) && source.Port == peer.Port {
					m.consumeRTCP(buffer[:n], security, time.Now())
				} else {
					m.rtcp.rejected.Add(1)
				}
				if !time.Now().Before(next) {
					raw, e := m.rtcpReport(reporter, time.Now())
					if e == nil && security != nil {
						raw, e = security.LocalRTCP.EncryptRTCP(nil, raw, nil)
					}
					if e == nil {
						_ = m.rtcpConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
						_, e = m.rtcpConn.WriteToUDP(raw, peer)
					}
					if e != nil {
						m.rtcp.sendErrors.Add(1)
					} else {
						m.rtcp.sent.Add(1)
					}
					next = time.Now().Add(5 * time.Second)
				}
			}
		}()
	})
}
func (m *sipRTPMedia) consumeRTCP(raw []byte, security *sipMediaSecurity, now time.Time) {
	if len(raw) < 8 || len(raw) > 4096 {
		m.rtcp.invalid.Add(1)
		return
	}
	if security != nil {
		if security.RemoteRTCP == nil {
			m.rtcp.securityErrors.Add(1)
			return
		}
		var err error
		raw, err = security.RemoteRTCP.DecryptRTCP(nil, raw, nil)
		if err != nil {
			m.rtcp.securityErrors.Add(1)
			return
		}
	}
	packets, err := rtcp.Unmarshal(raw)
	if err != nil {
		m.rtcp.invalid.Add(1)
		return
	}
	for _, packet := range packets {
		var reports []rtcp.ReceptionReport
		switch p := packet.(type) {
		case *rtcp.SenderReport:
			// Reports from the negotiated endpoint are diagnostic observations. They
			// never replace the receiver's clock or claim synchronized one-way delay.
			m.rtcp.senderNTP.Store(p.NTPTime)
			m.rtcp.senderReceivedNS.Store(now.UnixNano())
			reports = p.Reports
		case *rtcp.ReceiverReport:
			reports = p.Reports
		}
		for _, report := range reports {
			if report.SSRC != m.rtcp.localSSRC.Load() || report.SSRC == 0 {
				continue
			}
			m.rtcp.peerLost.Store(uint32(report.FractionLost))
			m.rtcp.peerJitter.Store(report.Jitter)
			if report.LastSenderReport != 0 {
				delay := uint32(sipNTP(now)>>16) - report.LastSenderReport - report.Delay
				if delay < 60*65536 {
					m.rtcp.rttNS.Store(int64(delay) * int64(time.Second) / 65536)
				}
			}
		}
	}
	m.rtcp.received.Add(1)
}
func (m *sipRTPMedia) rtcpReport(reporter uint32, now time.Time) ([]byte, error) {
	if local := m.rtcp.localSSRC.Load(); local != 0 {
		reporter = local
	}
	var blocks []rtcp.ReceptionReport
	o := &m.observations
	// Diagnostics must not contend with packet observation; omit a reception
	// block this interval if the media owner is updating it.
	if o.timingMu.TryLock() {
		if o.have {
			lostCount := max(int64(0), o.loss.Load()-o.streamLossBase)
			expected := o.highest - o.first + 1
			if m.rtcp.reportStream != o.stream || expected < m.rtcp.expectedPrior {
				m.rtcp.expectedPrior = 0
				m.rtcp.lostPrior = 0
				m.rtcp.reportStream = o.stream
			}
			deltaExpected, deltaLost := expected-m.rtcp.expectedPrior, lostCount-m.rtcp.lostPrior
			fraction := uint8(0)
			if deltaExpected > 0 && deltaLost > 0 {
				fraction = uint8(min(int64(255), deltaLost*256/deltaExpected))
			}
			m.rtcp.expectedPrior, m.rtcp.lostPrior = expected, lostCount
			lost := uint32(min(int64(0x7fffff), lostCount))
			block := rtcp.ReceptionReport{SSRC: o.stream, FractionLost: fraction, TotalLost: lost, LastSequenceNumber: uint32(o.highest), Jitter: uint32(max(0, o.jitterNS.Load()) * 8000 / int64(time.Second))}
			if n := m.rtcp.senderReceivedNS.Load(); n > 0 {
				block.LastSenderReport = uint32(m.rtcp.senderNTP.Load() >> 16)
				block.Delay = uint32(max(int64(0), now.UnixNano()-n) * 65536 / int64(time.Second))
			}
			blocks = append(blocks, block)
		}
		o.timingMu.Unlock()
	}
	var report rtcp.Packet = &rtcp.ReceiverReport{SSRC: reporter, Reports: blocks}
	if sent := m.observations.sent.Load(); sent > 0 {
		timestamp := m.rtcp.lastRTP.Load()
		if at := m.rtcp.lastSentNS.Load(); at > 0 {
			timestamp += uint32(max(int64(0), now.UnixNano()-at) * 8000 / int64(time.Second))
		}
		report = &rtcp.SenderReport{SSRC: reporter, NTPTime: sipNTP(now), RTPTime: timestamp, PacketCount: uint32(sent), OctetCount: m.rtcp.payloadOctets.Load(), Reports: blocks}
	}
	description := &rtcp.SourceDescription{Chunks: []rtcp.SourceDescriptionChunk{{Source: reporter, Items: []rtcp.SourceDescriptionItem{{Type: rtcp.SDESCNAME, Text: fmt.Sprintf("telephony-%08x", reporter)}}}}}
	return rtcp.Marshal([]rtcp.Packet{report, description})
}
