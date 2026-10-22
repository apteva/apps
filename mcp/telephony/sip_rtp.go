package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gotranspile/g722"
	"github.com/pion/rtp"
)

const (
	sipRTPPacketSamples = 160
	sipRTPPacketTime    = 20 * time.Millisecond
	sipRTPPrimeSamples  = 480
	sipRTPQueuePackets  = 100
	sipRTPInactivity    = 30 * time.Second
)

var sipRTPPorts = struct {
	sync.Mutex
	next map[string]int
}{next: make(map[string]int)}

type sipRTPMedia struct {
	rtcp                 sipRTCPState
	rtcpConn             *net.UDPConn
	readAt               time.Time // single receive owner only
	readCompletedAt      time.Time // socket dequeue, distinct from kernel arrival
	socketOOB            [128]byte
	early                *sipEarlyReceiver
	observations         sipRTPObservations
	diagnostics          atomic.Pointer[sipLiveDiagnostics]
	jitterTelemetry      atomic.Pointer[rtpJitterBuffer]
	coreForwardTelemetry atomic.Pointer[websocketWriterPump]
	remoteMode           atomic.Int32
	pacer                atomic.Pointer[sipRTPPacer]
	recorder             atomic.Pointer[localCallRecorder]
	hold                 atomic.Pointer[localSIPHold]
	liveTimeline         atomic.Pointer[liveAudioTimeline]
	conn                 *net.UDPConn
	offer                sipMediaOffer
	security             *sipMediaSecurity
	remote               *net.UDPAddr
	localPort            int
	closeOnce            sync.Once
	codecOnce            sync.Once
	g722Encoder          *g722.Encoder
	g722Decoder          *g722.Decoder
	g722SentDecoder      *g722.Decoder
	lastPacket           atomic.Int64
}

func openSIPRTPMedia(cfg sipGatewayConfig, offer sipMediaOffer) (*sipRTPMedia, error) {
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		return nil, err
	}
	remote := net.UDPAddrFromAddrPort(netip.AddrPortFrom(offer.RemoteAddress, uint16(offer.RemotePort)))
	sipRTPPorts.Lock()
	defer sipRTPPorts.Unlock()
	pool := fmt.Sprintf("%s:%d-%d", cfg.RTPBindIP, cfg.RTPPortMin, cfg.RTPPortMax)
	start := sipRTPPorts.next[pool]
	if start < cfg.RTPPortMin || start > cfg.RTPPortMax {
		start = cfg.RTPPortMin
	}
	count := (cfg.RTPPortMax - cfg.RTPPortMin + 1) / 2
	// Internal local harnesses can request ephemeral sockets. Runtime validation
	// requires an explicit nonzero range; both paths still reserve an even pair.
	ephemeral := cfg.RTPPortMin == 0 && cfg.RTPPortMax == 0
	if ephemeral {
		count = 128
	}
	for offset := 0; offset < count; offset++ {
		port := 0
		if !ephemeral {
			port = cfg.RTPPortMin + ((start-cfg.RTPPortMin)/2+offset)%count*2
		}
		local := net.UDPAddrFromAddrPort(netip.AddrPortFrom(cfg.RTPBindIP, uint16(port)))
		conn, err := net.ListenUDP("udp4", local)
		if err != nil {
			continue
		}
		if ephemeral {
			port = conn.LocalAddr().(*net.UDPAddr).Port
			if port%2 != 0 || port == 65535 {
				conn.Close()
				continue
			}
		}
		rtcpLocal := net.UDPAddrFromAddrPort(netip.AddrPortFrom(cfg.RTPBindIP, uint16(port+1)))
		rtcpConn, err := net.ListenUDP("udp4", rtcpLocal)
		if err != nil {
			_ = conn.Close()
			continue
		}
		media := &sipRTPMedia{
			rtcpConn: rtcpConn,
			conn:     conn, offer: offer, security: security, remote: remote, localPort: port,
		}
		if !ephemeral {
			sipRTPPorts.next[pool] = cfg.RTPPortMin + ((port-cfg.RTPPortMin)/2+1)%count*2
		}

		media.observations.kernelDropsAvailable.Store(enableSIPSocketDropCounter(conn))
		media.observations.kernelTimestampAvailable.Store(enableSIPSocketTimestamps(conn))
		media.lastPacket.Store(time.Now().UnixNano())
		return media, nil
	}
	return nil, errors.New("no direct SIP RTP port is available in the configured range")
}

func (m *sipRTPMedia) Close() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		if m.rtcpConn != nil {
			_ = m.rtcpConn.Close()
		}
		if m.conn != nil {
			_ = m.conn.Close()
		}
	})
}

func (m *sipRTPMedia) decodePacket(raw []byte) (*rtp.Packet, error) {
	return m.decodePacketAt(raw, time.Now())
}
func (m *sipRTPMedia) decodePacketAt(raw []byte, at time.Time) (*rtp.Packet, error) {
	if m.security != nil {
		decrypted, err := m.security.RemoteContext.DecryptRTP(nil, raw, nil)
		if err != nil {
			m.observedDecodeError(err, true)
			return nil, err
		}
		raw = decrypted
	}
	var packet rtp.Packet
	if err := packet.Unmarshal(raw); err != nil {
		m.observedDecodeError(err, false)
		return nil, err
	}
	if packet.PayloadType != m.offer.PayloadType && (m.offer.DTMFPayloadType <= 0 || int(packet.PayloadType) != m.offer.DTMFPayloadType) {
		m.observations.decodeErrors.Add(1)
		return nil, fmt.Errorf("unexpected RTP payload type %d", packet.PayloadType)
	}
	if len(packet.Payload) == 0 || len(packet.Payload) > 2048 {
		m.observations.decodeErrors.Add(1)
		return nil, errors.New("invalid RTP payload size")
	}
	m.startRTCP()
	m.observations.received(&packet, len(raw), at, packet.PayloadType == m.offer.PayloadType)
	if !m.readCompletedAt.IsZero() {
		m.observations.receiveEvidence.observe(packet.SequenceNumber, packet.Timestamp, packet.SSRC, at, m.readCompletedAt, time.Now())
	}
	return &packet, nil
}

func (m *sipRTPMedia) writePacket(packet *rtp.Packet) (result error) {
	m.startRTCP()
	startedAt := time.Now()
	defer func() {
		atomicMax(&m.observations.maxSend, int64(time.Since(startedAt)))
		if result != nil {
			if !errors.Is(result, net.ErrClosed) && (m.diagnostics.Load() == nil || !m.diagnostics.Load().ended.Load()) {
				m.observations.sendErrors.Add(1)
				m.observations.lastError.Store(time.Now().UnixNano())
			}
			if d := m.diagnostics.Load(); d != nil {
				d.event("transport_write_error", "carrier", transportEvidenceError(result), 0)
			}
		}
	}()
	raw, err := packet.Marshal()
	if err != nil {
		return err
	}
	if m.security != nil {
		raw, err = m.security.LocalContext.EncryptRTP(nil, raw, nil)
		if err != nil {
			return err
		}
	}
	if err := m.conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	started := time.Now()
	_, err = m.conn.WriteToUDP(raw, m.remote)
	if err == nil {
		m.observations.sentPacket(len(raw), time.Now())
		m.rtcp.localSSRC.Store(packet.SSRC)
		m.rtcp.lastRTP.Store(packet.Timestamp)
		m.rtcp.lastSentNS.Store(time.Now().UnixNano())
		m.rtcp.payloadOctets.Add(uint32(len(packet.Payload)))
	}
	if timeline := m.liveTimeline.Load(); timeline != nil {
		stage := "carrier_send"
		if err != nil {
			stage = "carrier_send_error"
		}
		timeline.observe(stage, len(raw), started, "", "")
	}
	return err
}

type sipPlaybackState struct {
	pending atomic.Int64
}

func (s *sipPlaybackState) hasPending() bool {
	return s.pending.Load() > 0
}

type sipRTPOutboundPacket struct {
	enqueuedAt time.Time // Original microphone source time; never refreshed on handoff.
	queuedAt   time.Time // Local SIP queue entry, independent of source age.
	payload    []byte
	pcm        []int16
	itemID     string
	audioEndMS int
}

type sipRTPPacer struct {
	dtmfCh         chan string
	dtmf           sipDTMFState
	observeSent    func([]byte)
	diagnostics    livePacerStats
	ctx            context.Context
	media          *sipRTPMedia
	playback       *sipPlaybackState
	onProgress     func(twilioPlaybackProgress) error
	queue          chan sipRTPOutboundPacket
	clearCh        chan chan int
	errCh          chan error
	done           chan struct{}
	progress       chan struct{}
	sequence       uint16
	timestamp      uint32
	ssrc           uint32
	dropStale      bool
	trimToPackets  int
	droppedPackets atomic.Int64
	needsCrossfade atomic.Bool
	lastSentPCM    []int16
	dropMu         sync.Mutex
	drops          []audioDropEvent
}

func newSIPRTPPacer(
	ctx context.Context,
	media *sipRTPMedia,
	playback *sipPlaybackState,
	onProgress func(twilioPlaybackProgress) error,
) *sipRTPPacer {
	return newSIPRTPPacerWithPolicy(ctx, media, playback, bufferedCarrierPacerPolicy(), onProgress)
}

func newSIPRTPPacerWithPolicy(
	ctx context.Context,
	media *sipRTPMedia,
	playback *sipPlaybackState,
	policy carrierPacerPolicy,
	onProgress func(twilioPlaybackProgress) error,
	observers ...func([]byte),
) *sipRTPPacer {
	queuePackets := sipRTPQueuePackets
	if policy.dropStale {
		queuePackets = max(1, policy.maxQueueMS/int(sipRTPPacketTime/time.Millisecond))
	}
	var seed [10]byte
	if _, err := rand.Read(seed[:]); err != nil {
		panic("SIP RTP random source unavailable: " + err.Error())
	}
	pacer := &sipRTPPacer{
		ctx: ctx, media: media, playback: playback, onProgress: onProgress, dtmfCh: make(chan string, 4),
		queue:   make(chan sipRTPOutboundPacket, queuePackets),
		clearCh: make(chan chan int), errCh: make(chan error, 1), done: make(chan struct{}), progress: make(chan struct{}, 1),
		sequence: binary.BigEndian.Uint16(seed[:2]), timestamp: binary.BigEndian.Uint32(seed[2:6]),
		ssrc:          binary.BigEndian.Uint32(seed[6:]),
		dropStale:     policy.dropStale,
		trimToPackets: max(1, policy.trimToMS/int(sipRTPPacketTime/time.Millisecond)),
	}
	if len(observers) > 0 {
		pacer.observeSent = observers[0]
	}
	go pacer.run()
	return pacer
}

func (p *sipRTPPacer) run() {
	defer close(p.done)
	clockStart := time.Now()
	baseTimestamp := p.timestamp
	var lastSent time.Time
	var lastFrame time.Duration
	ticker := time.NewTicker(sipRTPPacketTime)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case digits := <-p.dtmfCh:
			if len(p.dtmf.digits)+len(digits) <= 128 {
				p.dtmf.digits += digits
			}
		case reply := <-p.clearCh:
			cleared := p.drain()
			reply <- cleared * int(sipRTPPacketTime/time.Millisecond)
		case <-ticker.C:
			now := time.Now()
			frame := now.Sub(clockStart) / sipRTPPacketTime
			if !lastSent.IsZero() && frame <= lastFrame {
				continue
			}
			if sent, err := p.dtmfTick(now, baseTimestamp+uint32(frame)*sipRTPPacketSamples); sent {
				if err != nil {
					select {
					case p.errCh <- err:
					default:
					}
					return
				}
				lastSent = now
				lastFrame = frame
				continue
			}
			if packet, ok := p.dequeueLivePacket(now); ok {
				failed := func() bool {
					defer p.playback.pending.Add(-1)
					if mode := p.media.remoteMode.Load(); mode == 1 || mode == 3 {
						return false
					}
					if len(packet.pcm) > 0 {
						if p.needsCrossfade.Swap(false) && len(p.lastSentPCM) > 0 {
							applyPCMOverlapCrossfade(p.lastSentPCM, packet.pcm, p.media.sampleRate()/200)
						}
						packet.payload = p.media.encodeAudio(packet.pcm)
						p.lastSentPCM = append(p.lastSentPCM[:0], packet.pcm...)
					} else if p.media.offer.Codec != "G722" {
						pcm := decodeSIPG711(packet.payload, p.media.offer.Codec)
						if p.needsCrossfade.Swap(false) && len(p.lastSentPCM) > 0 {
							applyPCMOverlapCrossfade(p.lastSentPCM, pcm, twilioMediaSampleRate/200)
							packet.payload = encodeSIPG711(pcm, p.media.offer.Codec)
						}
						p.lastSentPCM = pcm
					}
					p.timestamp = baseTimestamp + uint32(frame)*sipRTPPacketSamples
					wire := &rtp.Packet{Header: rtp.Header{
						Version: 2, PayloadType: p.media.offer.PayloadType, Marker: lastSent.IsZero() || now.Sub(lastSent) > 30*time.Millisecond,
						SequenceNumber: p.sequence, Timestamp: p.timestamp, SSRC: p.ssrc,
					}, Payload: packet.payload}
					p.sequence++
					lastSent = now
					lastFrame = frame
					writeStarted := time.Now()
					if err := p.media.writePacket(wire); err != nil {
						select {
						case p.errCh <- err:
						default:
						}
						return true
					}
					if p.observeSent != nil {
						p.observeSent(packet.payload)
					}
					p.diagnostics.sent(20, writeStarted)
					if p.onProgress != nil && packet.itemID != "" && packet.audioEndMS > 0 {
						if err := p.onProgress(twilioPlaybackProgress{
							ItemID: packet.itemID, AudioEndMS: packet.audioEndMS,
						}); err != nil {
							select {
							case p.errCh <- err:
							default:
							}
							return true
						}
					}
					return false
				}()
				if failed {
					return
				}
			} else {
				if hold := p.media.hold.Load(); hold != nil && p.media.remoteMode.Load() != 1 && p.media.remoteMode.Load() != 3 {
					pcm := hold.frame(p.media.sampleRate() / 50)
					wire := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: p.media.offer.PayloadType, SequenceNumber: p.sequence, Timestamp: baseTimestamp + uint32(frame)*sipRTPPacketSamples, SSRC: p.ssrc}, Payload: p.media.encodeAudio(pcm)}
					p.sequence++
					lastSent = now
					lastFrame = frame
					writeStarted := time.Now()
					if err := p.media.writePacket(wire); err != nil {
						select {
						case p.errCh <- err:
						default:
						}
						return
					}
					if p.observeSent != nil {
						p.observeSent(wire.Payload)
					}
					p.diagnostics.sent(20, writeStarted)
				}
			}
		}
		select {
		case p.progress <- struct{}{}:
		default:
		}
	}
}

// Expired frames do not consume media time. Drain only the bounded queue
// present at this tick, then send at most one usable frame. Consuming one stale
// frame per tick would preserve a backlog forever while new audio arrives.
func (p *sipRTPPacer) dequeueLivePacket(now time.Time) (sipRTPOutboundPacket, bool) {
	for remaining := len(p.queue); remaining > 0; remaining-- {
		select {
		case packet := <-p.queue:
			if hold := p.media.hold.Load(); hold != nil {
				packet.pcm = hold.frame(p.media.sampleRate() / 50)
				packet.payload = nil
				packet.itemID = ""
				packet.enqueuedAt = now
			}
			sourceAge, residence := time.Duration(0), time.Duration(0)
			if !packet.enqueuedAt.IsZero() {
				sourceAge = max(0, now.Sub(packet.enqueuedAt))
			}
			if !packet.queuedAt.IsZero() {
				residence = max(0, now.Sub(packet.queuedAt))
			}
			p.diagnostics.observeAge(sourceAge, residence)
			if !p.dropStale || sourceAge < liveAudioMaxAge {
				return packet, true
			}
			p.playback.pending.Add(-1)
			p.droppedPackets.Add(1)
			p.diagnostics.dropped("live_audio_age_limit", 20)
			p.needsCrossfade.Store(true)
			p.dropMu.Lock()
			p.drops = append(p.drops, audioDropEvent{
				Timestamp: now.UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier",
				Reason: "live_audio_age_limit", DurationMS: 20,
				FrameAgeMS: float64(sourceAge) / float64(time.Millisecond), QueueResidenceMS: float64(residence) / float64(time.Millisecond),
			})
			if len(p.drops) > 100 {
				p.drops = p.drops[len(p.drops)-100:]
			}
			p.dropMu.Unlock()
		default:
			return sipRTPOutboundPacket{}, false
		}
	}
	return sipRTPOutboundPacket{}, false
}

func (p *sipRTPPacer) drain() int {
	count := 0
	for {
		select {
		case <-p.queue:
			p.playback.pending.Add(-1)
			count++
		default:
			return count
		}
	}
}

func (p *sipRTPPacer) enqueue(packets []sipRTPOutboundPacket) (int, error) {
	before := len(p.queue) + len(packets)
	droppedBefore := p.droppedPackets.Load()
	if p.dropStale && len(packets) > cap(p.queue) {
		p.droppedPackets.Add(int64(len(packets) - cap(p.queue)))
		packets = packets[len(packets)-cap(p.queue):]
	}
	if p.dropStale && len(packets) > cap(p.queue)-len(p.queue) {
		targetPackets := min(p.trimToPackets, cap(p.queue)-len(packets))
	trimQueue:
		for len(p.queue) > targetPackets {
			select {
			case <-p.queue:
				p.playback.pending.Add(-1)
				p.droppedPackets.Add(1)
			default:
				break trimQueue
			}
		}
	}
	if dropped := p.droppedPackets.Load() - droppedBefore; dropped > 0 {
		p.diagnostics.dropped("stale_live_audio", int(dropped)*20)
		p.needsCrossfade.Store(true)
		p.dropMu.Lock()
		p.drops = append(p.drops, audioDropEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier",
			Reason: "stale_live_audio", DurationMS: int(dropped) * int(sipRTPPacketTime/time.Millisecond),
			QueueBeforeMS: before * int(sipRTPPacketTime/time.Millisecond), QueueAfterMS: len(p.queue) * int(sipRTPPacketTime/time.Millisecond),
		})
		if len(p.drops) > 100 {
			p.drops = p.drops[len(p.drops)-100:]
		}
		p.dropMu.Unlock()
	}
	if len(packets) > cap(p.queue)-len(p.queue) {
		return len(p.queue) * int(sipRTPPacketTime/time.Millisecond), errors.New("direct SIP playback queue overflow")
	}
	for _, packet := range packets {
		packet.queuedAt = time.Now()
		if packet.enqueuedAt.IsZero() {
			packet.enqueuedAt = time.Now()
		}
		// Count before publishing so a concurrently scheduled sender cannot
		// consume a frame before it is counted. Keep it pending through writing.
		p.playback.pending.Add(1)
		select {
		case p.queue <- packet:
		default:
			p.playback.pending.Add(-1)
			return len(p.queue) * int(sipRTPPacketTime/time.Millisecond), errors.New("direct SIP playback queue changed concurrently")
		}
	}
	p.diagnostics.queued(len(p.queue) * 20)
	return len(p.queue) * int(sipRTPPacketTime/time.Millisecond), nil
}

func (p *sipRTPPacer) dropEvents() []audioDropEvent {
	p.dropMu.Lock()
	defer p.dropMu.Unlock()
	return append([]audioDropEvent(nil), p.drops...)
}

func (p *sipRTPPacer) droppedMS() int {
	return int(p.droppedPackets.Load()) * int(sipRTPPacketTime/time.Millisecond)
}

func (p *sipRTPPacer) clear(ctx context.Context) (int, error) {
	reply := make(chan int, 1)
	select {
	case p.clearCh <- reply:
	case <-p.done:
		return 0, net.ErrClosed
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case value := <-reply:
		return value, nil
	case <-p.done:
		return 0, net.ErrClosed
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (p *sipRTPPacer) Err() <-chan error { return p.errCh }

type sipBridgeSession struct {
	playbackPolicy sipPlaybackPolicy
	callID         string
	ctx            context.Context
	media          *sipRTPMedia
	finish         func(string, error)
	hangup         func() error
	takePrepared   func() net.Conn
}

func (a *App) bridgeDirectSIPMedia(session *sipSession) {
	session.answerMu.Lock()
	media := session.media
	session.answerMu.Unlock()
	policy := sipPlaybackPolicy{}
	session.local.mu.Lock()
	if session.local.config.RTPPlayback != nil {
		policy = *session.local.config.RTPPlayback
	}
	session.local.mu.Unlock()
	a.bridgeSIPMedia(sipBridgeSession{
		playbackPolicy: policy,
		takePrepared:   session.takePreparedBridge,
		callID:         session.call.ID, ctx: session.ctx, media: media,
		finish: session.finish, hangup: session.hangup,
	})
}

func (a *App) bridgeSIPMedia(session sipBridgeSession) {
	row, err := a.db().findCall(session.callID)
	if err != nil {
		session.finish("local_error", err)
		return
	}
	media := session.media
	if row == nil || media == nil {
		return
	}
	claimed, err := a.db().claimMedia(row.ID)
	if err != nil || !claimed {
		session.finish("local_error", fmt.Errorf("claim direct SIP media: %w", err))
		return
	}
	defer a.db().releaseMedia(row.ID)
	var core net.Conn
	if session.takePrepared != nil {
		core = session.takePrepared()
	}
	if core == nil {
		core, err = a.dialSIPBridge(session.ctx, row)
		if err != nil {
			_ = a.db().updateMediaStatusWithLeg(row.ID, "error", err.Error(), 1011, "audio bridge rejected", string(mediaCloseLegCore))
			session.finish("core", err)
			return
		}
	}
	coreWriter := newWebSocketWriterPump(core, ws.StateClientSide)
	media.coreForwardTelemetry.Store(coreWriter)
	defer media.coreForwardTelemetry.CompareAndSwap(coreWriter, nil)
	coreCloser := newGracefulWebSocket(core, coreWriter)
	defer coreCloser.Close(ws.StatusNormalClosure, "direct SIP media ended")

	_ = a.db().updateMediaStatus(row.ID, "connected", "", 0, "")
	_ = a.db().clearStateExpiry(row.ID)
	ctx, cancel := context.WithCancel(session.ctx)
	defer cancel()
	closeState := &websocketCloseState{}
	tap := a.listeners.openBridge(row.ID)
	defer a.listeners.closeBridge(row.ID, tap)
	audioFrontend := newCarrierAudioFrontend(media.sampleRate())
	var humanHub *softphoneHub
	if row.PeerKind == peerKindHuman {
		humanHub = a.softphones.hubFor(row.ID)
		humanHub.sipMedia.Store(media)
		defer humanHub.sipMedia.CompareAndSwap(media, nil)
		media.liveTimeline.Store(&humanHub.timeline)
		humanHub.setCarrierForward(coreWriter)
		defer humanHub.finishCarrierForward(coreWriter)
	}
	inputResampler := newPCMResampler(media.sampleRate(), 24000)
	outputResampler := newPCMResampler(24000, media.sampleRate())
	playback := &sipPlaybackState{}
	pacerPolicy := bufferedCarrierPacerPolicy()
	pacerMode := "buffered"
	if row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		pacerPolicy = liveHumanCarrierPacerPolicy()
		pacerMode = "live_human"
	}
	recorder, recordErr := a.newLocalRecording(row, false)
	if recordErr != nil {
		globalCtx.Logger().Warn("local SIP recording unavailable", "call", row.ID, "err", recordErr)
	}
	media.recorder.Store(recorder)
	defer func() { media.recorder.CompareAndSwap(recorder, nil); recorder.Close() }()
	recordOutput := newPCMResampler(media.sampleRate(), 24000)
	policyGeneration := newSecret()
	pacer := newSIPRTPPacerWithPolicy(ctx, media, playback, pacerPolicy, func(progress twilioPlaybackProgress) error {
		control, _ := json.Marshal(realtimeBridgeControl{
			Type: "playback.progress", ItemID: progress.ItemID, AudioEndMS: progress.AudioEndMS,
		})
		return coreWriter.Write(ws.OpText, control)
	}, func(payload []byte) {
		var sentPCM []int16
		listeners := tap.hasListeners()
		if recorder != nil || listeners || media.offer.Codec == "G722" {
			sentPCM = recordOutput.Process(media.decodeSentAudio(payload))
			recorder.Record(1, sentPCM)
		}
		a.observeAIOutput(row, time.Now().UTC(), policyGeneration)
		if listeners {
			tap.publish(1, pcm16ToBytes(sentPCM))
		}
	})
	media.pacer.Store(pacer)
	defer media.pacer.CompareAndSwap(pacer, nil)
	a.bindAIPlayback(row, policyGeneration, playback.hasPending)
	if humanHub != nil {
		humanHub.setPacerStats(&pacer.diagnostics)
	}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		media.Close()
		coreCloser.Close(ws.StatusGoingAway, "SIP media drained")
		workers.Wait()
		<-pacer.done
	}()
	jitter := newRTPJitterBufferWithPolicy(session.playbackPolicy)
	media.jitterTelemetry.Store(jitter)
	framer := newRTPAudioFramer(jitter, media.offer)
	maxQueuedMS := 0
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-ctx.Done()
		coreCloser.Close(ws.StatusGoingAway, "direct SIP media stopped")
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		select {
		case err := <-pacer.Err():
			closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "write direct SIP RTP: "+err.Error())
			cancel()
		case <-ctx.Done():
		}
	}()

	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		media.finishEarlyReceiver()
		buffer := make([]byte, 4096)
		for {
			_ = media.conn.SetReadDeadline(time.Now().Add(time.Second))
			n, source, err := media.readPacket(buffer)
			media.observedReadError(err)
			if err != nil {
				if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
					return
				}
				if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					if mode := media.remoteMode.Load(); mode == 2 || mode == 3 {
						continue
					}
					if time.Since(time.Unix(0, media.lastPacket.Load())) > sipRTPInactivity {
						if d := media.diagnostics.Load(); d != nil {
							d.event("sip_media_timeout", "carrier", "direct SIP RTP timed out", 0)
						}
						closeState.SetLeg(mediaCloseLegCarrier, ws.StatusGoingAway, "direct SIP RTP timed out")
						return
					}
					continue
				}
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "read direct SIP RTP")
				return
			}
			if source.IP.String() != media.remote.IP.String() || source.Port != media.remote.Port {
				media.observations.rejectedSource.Add(1)
				continue
			}
			packet, err := media.decodePacketAt(buffer[:n], media.readAt)
			if err != nil {
				continue
			}
			media.lastPacket.Store(time.Now().UnixNano())
			if humanHub != nil {
				humanHub.timeline.observe("carrier_media_read", n, time.Time{}, fmt.Sprint(packet.Timestamp), fmt.Sprint(packet.SequenceNumber))
			}
			if int(packet.PayloadType) == media.offer.DTMFPayloadType {
				if !jitter.sourceKnown.Load() || jitter.sourceSSRC.Load() == packet.SSRC {
					jitter.control(packet.SequenceNumber)
				}
				a.observeAICaller(row, true, time.Now().UTC(), policyGeneration)
				continue
			}
			if mode := media.remoteMode.Load(); mode == 2 || mode == 3 {
				continue
			}
			jitter.pushSourceRTPAt(packet.SSRC, packet.SequenceNumber, packet.Payload, packet.Timestamp, media.readAt, time.Now(), time.Now)
		}
	}()

	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		ticker := time.NewTicker(sipRTPPacketTime)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				mode := media.remoteMode.Load()
				jitter.setPaused(mode == 2 || mode == 3, now)
				pcm, ready := framer.popPCM(media)
				if !ready {
					continue
				}
				processed := processCarrierInput(row, audioFrontend, pcm)
				a.observeAICaller(row, processed.SpeechActive, time.Now().UTC(), policyGeneration)
				localSpeechStarted := processed.SpeechStarted && playback.hasPending()
				if localSpeechStarted {
					audioFrontend.markLocalSignal()
				}
				pcm24 := inputResampler.Process(processed.PCM)
				if len(pcm24) == 0 {
					continue
				}
				if humanHub != nil {
					humanHub.timeline.observe("carrier_decoded", len(pcm24)*2, time.Time{}, "", "")
				}
				recorder.Record(0, pcm24)
				tap.publishPCM(0, pcm24)
				coreWriter.QueueAudio(pcm16ToBytes(pcm24))
				if localSpeechStarted {
					control, _ := json.Marshal(realtimeBridgeControl{Type: "input.speech_started"})
					if err := coreWriter.Write(ws.OpText, control); err != nil {
						closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write speech start to realtime bridge")
						return
					}
					logLocalBargeIn(globalCtx.Logger(), "sip_direct", row.ID, processed)
				}
			}
		}
	}()

	defer func() {
		leg, code, reason := closeState.Cause()
		a.finishMediaBridge(row.ID, leg, code, reason)
		droppedStaleMS := pacer.droppedMS()
		logAudioFrontendDiagnostics(globalCtx.Logger(), audioFrontend, row, "sip_direct", carrierCodecPCMU8, maxQueuedMS, droppedStaleMS)
		jitter.mu.Lock()
		lost, inboundDroppedMS, maxAge, jitterMS := int(media.observations.loss.Load()), (jitter.droppedSamples+jitter.lateDroppedSamples)/8, jitter.maxAgeMS, jitter.jitterMS
		jitter.mu.Unlock()
		var preAnswerDroppedMS int64
		if hub := a.softphones.lookup(row.ID); hub != nil {
			preAnswerDroppedMS = hub.preAnswerDroppedMS()
		}
		_ = a.db().updateCarrierAudioDiagnostics(row.ID, carrierAudioDiagnostics{
			OperatorInterrupts: audioFrontend.snapshot().OperatorInterrupts, LocalInterrupts: audioFrontend.snapshot().LocalInterrupts, ProviderCoreInterrupts: audioFrontend.snapshot().ProviderCoreInterrupts,
			InputAudio: audioFrontend.transportSnapshot(),
			Provider:   "sip_direct", Codec: media.offer.Codec, SampleRate: media.sampleRate(),
			SendAheadMS: pacerPolicy.bufferMS, PacerMode: pacerMode, MaxQueuedMS: maxQueuedMS, DroppedStaleMS: droppedStaleMS,
			PreAnswerMicrophoneDroppedMS: preAnswerDroppedMS,
			DropEvents:                   pacer.dropEvents(),
			SequenceGaps:                 lost, InboundDroppedMS: inboundDroppedMS + int((coreWriter.audioSnapshot().OverflowBytes+coreWriter.audioSnapshot().StaleBytes)*1000/48000), InboundMaxQueueAgeMS: maxAge, InboundJitterMS: jitterMS,
		})
		if session.ctx.Err() == nil {
			_ = session.hangup()
		}
		session.finish(string(leg), errors.New(reason))
	}()

	var nextFrame realtimeBridgeControl
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		data, op, err := readWebSocketData(core, ws.StateClientSide, coreWriter)
		if err != nil {
			code, reason := websocketCloseDetails(err)
			closeState.SetLeg(mediaCloseLegCore, code, reason)
			return
		}
		if op == ws.OpText {
			control, ok := parseRealtimeBridgeControl(data)
			if !ok {
				continue
			}
			switch control.Type {
			case "audio.frame":
				nextFrame = control
			case "interrupt":
				a.observeAICaller(row, true, time.Now().UTC(), policyGeneration)
				nextFrame = realtimeBridgeControl{}
				audioFrontend.markInterrupt(control.Source)
				if _, err := pacer.clear(ctx); err != nil {
					closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "clear direct SIP playback")
					return
				}
			}
			continue
		}
		if op != ws.OpBinary || len(data) == 0 {
			continue
		}
		if media.hold.Load() != nil {
			continue
		}
		data, sourceAt := carrierMicrophoneSource(row, data)
		pcm8 := outputResampler.Process(bytesToPCM16(data))
		if len(pcm8) == 0 {
			continue
		}
		chunks := sipPCMFrames(pcm8, media.sampleRate(), nextFrame)
		packets := make([]sipRTPOutboundPacket, 0, len(chunks))
		for i, chunk := range chunks {
			if !sourceAt.IsZero() && i > 0 {
				sourceAt = sourceAt.Add(20 * time.Millisecond)
			}
			pcm := chunk.PCM
			if len(pcm) < media.sampleRate()/50 {
				padded := make([]int16, media.sampleRate()/50)
				copy(padded, pcm)
				pcm = padded
			}
			packets = append(packets, sipRTPOutboundPacket{
				enqueuedAt: sourceAt, pcm: pcm,
				itemID: nextFrame.ItemID, audioEndMS: chunk.AudioEndMS,
			})
		}
		nextFrame = realtimeBridgeControl{}
		queued, err := pacer.enqueue(packets)
		if err != nil {
			control, _ := json.Marshal(realtimeBridgeControl{Type: "playback.overflow"})
			_ = coreWriter.Write(ws.OpText, control)
			continue
		}
		if queued > maxQueuedMS {
			maxQueuedMS = queued
		}
	}
}

func sipEncodedSilence(codec string, samples int) []byte {
	value := byte(0xff)
	if codec == "PCMA" {
		value = 0xd5
	}
	out := make([]byte, samples)
	for i := range out {
		out[i] = value
	}
	return out
}

func decodeSIPG711(payload []byte, codec string) []int16 {
	if codec == "PCMA" {
		return alawToPCM16(payload)
	}
	return ulawToPCM16(payload)
}

func encodeSIPG711(pcm []int16, codec string) []byte {
	if codec == "PCMA" {
		return pcm16ToAlaw(pcm)
	}
	return pcm16ToUlaw(pcm)
}

func alawToPCM16(encoded []byte) []int16 {
	out := make([]int16, len(encoded))
	for i, value := range encoded {
		value ^= 0x55
		sample := int16(value&0x0f)<<4 + 8
		segment := (value & 0x70) >> 4
		if segment >= 1 {
			sample += 0x100
		}
		if segment > 1 {
			sample <<= segment - 1
		}
		if value&0x80 == 0 {
			sample = -sample
		}
		out[i] = sample
	}
	return out
}

func pcm16ToAlaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, sample := range pcm {
		sign := byte(0x80)
		value := int(sample)
		if value < 0 {
			sign = 0
			value = -value - 1
		}
		if value > 32767 {
			value = 32767
		}
		var encoded byte
		if value < 256 {
			encoded = byte(value >> 4)
		} else {
			segment := 1
			for threshold := 512; segment < 7 && value >= threshold; threshold <<= 1 {
				segment++
			}
			encoded = byte(segment<<4) | byte((value>>(segment+3))&0x0f)
		}
		out[i] = (encoded | sign) ^ 0x55
	}
	return out
}

func (m *sipRTPMedia) sampleRate() int {
	if m.offer.Codec == "G722" {
		return 16000
	}
	return 8000
}
func (m *sipRTPMedia) initCodec() {
	m.codecOnce.Do(func() {
		if m.offer.Codec == "G722" {
			m.g722Encoder = g722.NewEncoder(g722.Rate64000, 0)
			m.g722Decoder = g722.NewDecoder(g722.Rate64000, 0)
			m.g722SentDecoder = g722.NewDecoder(g722.Rate64000, 0)
		}
	})
}
func (m *sipRTPMedia) encodeAudio(pcm []int16) []byte {
	m.initCodec()
	if m.offer.Codec != "G722" {
		return encodeSIPG711(pcm, m.offer.Codec)
	}
	out := make([]byte, (len(pcm)+1)/2)
	n := m.g722Encoder.Encode(out, pcm)
	return out[:n]
}
func (m *sipRTPMedia) decodeAudio(p []byte) []int16 {
	m.initCodec()
	if m.offer.Codec != "G722" {
		return decodeSIPG711(p, m.offer.Codec)
	}
	out := make([]int16, len(p)*2)
	n := m.g722Decoder.Decode(out, p)
	return out[:n]
}
func (m *sipRTPMedia) decodeSentAudio(p []byte) []int16 {
	m.initCodec()
	if m.offer.Codec != "G722" {
		return decodeSIPG711(p, m.offer.Codec)
	}
	out := make([]int16, len(p)*2)
	n := m.g722SentDecoder.Decode(out, p)
	return out[:n]
}
func sipPCMFrames(pcm []int16, rate int, control realtimeBridgeControl) []twilioAudioPacket {
	frames := []twilioAudioPacket{}
	samples := rate / 50
	for offset := 0; offset < len(pcm); offset += samples {
		end := min(len(pcm), offset+samples)
		frame := make([]int16, samples)
		copy(frame, pcm[offset:end])
		frames = append(frames, twilioAudioPacket{PCM: frame, AudioEndMS: control.AudioEndMS - (len(pcm)-end)*1000/rate})
	}
	return frames
}
