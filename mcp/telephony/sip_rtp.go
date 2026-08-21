package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/pion/rtp"
)

const (
	sipRTPPacketSamples = 160
	sipRTPPacketTime    = 20 * time.Millisecond
	sipRTPPrimeSamples  = 480
	sipRTPQueuePackets  = 100
	sipRTPInactivity    = 30 * time.Second
)

type sipRTPMedia struct {
	conn       *net.UDPConn
	offer      sipMediaOffer
	security   *sipMediaSecurity
	remote     *net.UDPAddr
	localPort  int
	closeOnce  sync.Once
	lastPacket atomic.Int64
}

func openSIPRTPMedia(cfg sipGatewayConfig, offer sipMediaOffer) (*sipRTPMedia, error) {
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		return nil, err
	}
	remote := net.UDPAddrFromAddrPort(netip.AddrPortFrom(offer.RemoteAddress, uint16(offer.RemotePort)))
	for port := cfg.RTPPortMin; port <= cfg.RTPPortMax; port += 2 {
		local := net.UDPAddrFromAddrPort(netip.AddrPortFrom(cfg.RTPBindIP, uint16(port)))
		conn, err := net.ListenUDP("udp4", local)
		if err != nil {
			continue
		}
		media := &sipRTPMedia{
			conn: conn, offer: offer, security: security, remote: remote, localPort: port,
		}
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
		if m.conn != nil {
			_ = m.conn.Close()
		}
	})
}

func (m *sipRTPMedia) decodePacket(raw []byte) (*rtp.Packet, error) {
	if m.security != nil {
		decrypted, err := m.security.RemoteContext.DecryptRTP(nil, raw, nil)
		if err != nil {
			return nil, err
		}
		raw = decrypted
	}
	var packet rtp.Packet
	if err := packet.Unmarshal(raw); err != nil {
		return nil, err
	}
	if packet.PayloadType != m.offer.PayloadType {
		return nil, fmt.Errorf("unexpected RTP payload type %d", packet.PayloadType)
	}
	if len(packet.Payload) == 0 || len(packet.Payload) > 2048 {
		return nil, errors.New("invalid RTP payload size")
	}
	return &packet, nil
}

func (m *sipRTPMedia) writePacket(packet *rtp.Packet) error {
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
	_, err = m.conn.WriteToUDP(raw, m.remote)
	return err
}

type rtpJitterBuffer struct {
	mu       sync.Mutex
	packets  map[uint16][]byte
	expected uint16
	started  bool
	primed   bool
	buffered int
}

func newRTPJitterBuffer() *rtpJitterBuffer {
	return &rtpJitterBuffer{packets: make(map[uint16][]byte)}
}

func (b *rtpJitterBuffer) push(sequence uint16, payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		b.started = true
		b.expected = sequence
	}
	if b.primed && rtpSequenceBefore(sequence, b.expected) {
		return
	}
	if !b.primed && rtpSequenceBefore(sequence, b.expected) {
		b.expected = sequence
	}
	if _, exists := b.packets[sequence]; exists {
		return
	}
	if len(b.packets) >= 32 {
		return
	}
	b.packets[sequence] = append([]byte(nil), payload...)
	b.buffered += len(payload)
	if b.buffered >= sipRTPPrimeSamples {
		b.primed = true
	}
}

func rtpSequenceBefore(left, right uint16) bool {
	return int16(left-right) < 0
}

func (b *rtpJitterBuffer) pop() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.primed {
		return nil, false
	}
	payload, ok := b.packets[b.expected]
	delete(b.packets, b.expected)
	b.buffered -= len(payload)
	b.expected++
	if ok {
		return payload, true
	}
	return nil, true
}

type rtpAudioFramer struct {
	jitter         *rtpJitterBuffer
	codec          string
	missingSamples int
	pending        []byte
}

func newRTPAudioFramer(jitter *rtpJitterBuffer, offer sipMediaOffer) *rtpAudioFramer {
	missingSamples := offer.PacketSamples
	if missingSamples <= 0 {
		missingSamples = sipRTPPacketSamples
	}
	return &rtpAudioFramer{jitter: jitter, codec: offer.Codec, missingSamples: missingSamples}
}

func (f *rtpAudioFramer) pop() ([]byte, bool) {
	for len(f.pending) < sipRTPPacketSamples {
		payload, ready := f.jitter.pop()
		if !ready {
			return nil, false
		}
		if payload == nil {
			payload = sipEncodedSilence(f.codec, f.missingSamples)
		}
		f.pending = append(f.pending, payload...)
	}
	out := append([]byte(nil), f.pending[:sipRTPPacketSamples]...)
	f.pending = f.pending[sipRTPPacketSamples:]
	return out, true
}

type sipPlaybackState struct {
	pending atomic.Int64
}

func (s *sipPlaybackState) hasPending() bool {
	return s.pending.Load() > 0
}

type sipRTPOutboundPacket struct {
	payload    []byte
	itemID     string
	audioEndMS int
}

type sipRTPPacer struct {
	ctx            context.Context
	media          *sipRTPMedia
	playback       *sipPlaybackState
	onProgress     func(twilioPlaybackProgress) error
	queue          chan sipRTPOutboundPacket
	clearCh        chan chan int
	errCh          chan error
	done           chan struct{}
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
) *sipRTPPacer {
	queuePackets := sipRTPQueuePackets
	if policy.dropStale {
		queuePackets = max(1, policy.maxQueueMS/int(sipRTPPacketTime/time.Millisecond))
	}
	pacer := &sipRTPPacer{
		ctx: ctx, media: media, playback: playback, onProgress: onProgress,
		queue:   make(chan sipRTPOutboundPacket, queuePackets),
		clearCh: make(chan chan int), errCh: make(chan error, 1), done: make(chan struct{}),
		sequence: uint16(time.Now().UnixNano()), timestamp: uint32(time.Now().UnixNano()),
		ssrc:          uint32(time.Now().UnixNano() >> 16),
		dropStale:     policy.dropStale,
		trimToPackets: max(1, policy.trimToMS/int(sipRTPPacketTime/time.Millisecond)),
	}
	go pacer.run()
	return pacer
}

func (p *sipRTPPacer) run() {
	defer close(p.done)
	ticker := time.NewTicker(sipRTPPacketTime)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case reply := <-p.clearCh:
			cleared := p.drain()
			reply <- cleared * int(sipRTPPacketTime/time.Millisecond)
		case <-ticker.C:
			select {
			case packet := <-p.queue:
				p.playback.pending.Add(-1)
				if p.needsCrossfade.Swap(false) && len(p.lastSentPCM) > 0 {
					pcm := decodeSIPG711(packet.payload, p.media.offer.Codec)
					applyPCMOverlapCrossfade(p.lastSentPCM, pcm, twilioMediaSampleRate/200)
					packet.payload = encodeSIPG711(pcm, p.media.offer.Codec)
				}
				p.lastSentPCM = decodeSIPG711(packet.payload, p.media.offer.Codec)
				wire := &rtp.Packet{Header: rtp.Header{
					Version: 2, PayloadType: p.media.offer.PayloadType,
					SequenceNumber: p.sequence, Timestamp: p.timestamp, SSRC: p.ssrc,
				}, Payload: packet.payload}
				p.sequence++
				p.timestamp += sipRTPPacketSamples
				if err := p.media.writePacket(wire); err != nil {
					select {
					case p.errCh <- err:
					default:
					}
					return
				}
				if p.onProgress != nil && packet.itemID != "" && packet.audioEndMS > 0 {
					if err := p.onProgress(twilioPlaybackProgress{
						ItemID: packet.itemID, AudioEndMS: packet.audioEndMS,
					}); err != nil {
						select {
						case p.errCh <- err:
						default:
						}
						return
					}
				}
			default:
			}
		}
	}
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
		select {
		case p.queue <- packet:
			p.playback.pending.Add(1)
		default:
			return len(p.queue) * int(sipRTPPacketTime/time.Millisecond), errors.New("direct SIP playback queue changed concurrently")
		}
	}
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

func (a *App) bridgeDirectSIPMedia(session *sipSession) {
	row := session.call
	session.answerMu.Lock()
	media := session.media
	session.answerMu.Unlock()
	if row == nil || media == nil {
		return
	}
	claimed, err := a.db().claimMedia(row.ID)
	if err != nil || !claimed {
		session.finish("local_error", fmt.Errorf("claim direct SIP media: %w", err))
		return
	}
	defer a.db().releaseMedia(row.ID)
	bridgeURL, err := a.mediaBridgeURL(row)
	if err != nil {
		_ = a.db().updateMediaStatusWithLeg(row.ID, "error", err.Error(), 1011, "audio bridge unavailable", string(mediaCloseLegLocalError))
		session.finish("local_error", err)
		return
	}
	coreURL, err := url.Parse(bridgeURL)
	if err != nil {
		session.finish("local_error", err)
		return
	}
	core, _, _, err := (ws.Dialer{}).Dial(session.ctx, coreURL.String())
	if err != nil {
		_ = a.db().updateMediaStatusWithLeg(row.ID, "error", err.Error(), 1011, "core audio bridge rejected", string(mediaCloseLegCore))
		session.finish("core", err)
		return
	}
	coreWriter := newWebSocketWriterPump(core, ws.StateClientSide)
	coreCloser := newGracefulWebSocket(core, coreWriter)
	defer coreCloser.Close(ws.StatusNormalClosure, "direct SIP media ended")

	_ = a.db().updateMediaStatus(row.ID, "connected", "", 0, "")
	_ = a.db().clearStateExpiry(row.ID)
	ctx, cancel := context.WithCancel(session.ctx)
	defer cancel()
	closeState := &websocketCloseState{}
	audioFrontend := newCarrierAudioFrontend(8000)
	inputResampler := newPCMResampler(8000, 24000)
	outputResampler := newPCMResampler(24000, 8000)
	playback := &sipPlaybackState{}
	pacerPolicy := bufferedCarrierPacerPolicy()
	pacerMode := "buffered"
	if row.PeerKind == peerKindHuman {
		pacerPolicy = liveHumanCarrierPacerPolicy()
		pacerMode = "live_human"
	}
	pacer := newSIPRTPPacerWithPolicy(ctx, media, playback, pacerPolicy, func(progress twilioPlaybackProgress) error {
		control, _ := json.Marshal(realtimeBridgeControl{
			Type: "playback.progress", ItemID: progress.ItemID, AudioEndMS: progress.AudioEndMS,
		})
		return coreWriter.Write(ws.OpText, control)
	})
	jitter := newRTPJitterBuffer()
	framer := newRTPAudioFramer(jitter, media.offer)
	maxQueuedMS := 0
	go func() {
		<-ctx.Done()
		coreCloser.Close(ws.StatusGoingAway, "direct SIP media stopped")
	}()
	go func() {
		select {
		case err := <-pacer.Err():
			closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "write direct SIP RTP: "+err.Error())
			cancel()
		case <-ctx.Done():
		}
	}()

	go func() {
		defer cancel()
		buffer := make([]byte, 4096)
		for {
			_ = media.conn.SetReadDeadline(time.Now().Add(time.Second))
			n, source, err := media.conn.ReadFromUDP(buffer)
			if err != nil {
				if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
					return
				}
				if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					if time.Since(time.Unix(0, media.lastPacket.Load())) > sipRTPInactivity {
						closeState.SetLeg(mediaCloseLegCarrier, ws.StatusGoingAway, "direct SIP RTP timed out")
						return
					}
					continue
				}
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "read direct SIP RTP")
				return
			}
			if source.IP.String() != media.remote.IP.String() || source.Port != media.remote.Port {
				continue
			}
			packet, err := media.decodePacket(buffer[:n])
			if err != nil {
				continue
			}
			media.lastPacket.Store(time.Now().UnixNano())
			jitter.push(packet.SequenceNumber, packet.Payload)
		}
	}()

	go func() {
		defer cancel()
		ticker := time.NewTicker(sipRTPPacketTime)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				payload, ready := framer.pop()
				if !ready {
					continue
				}
				pcm := decodeSIPG711(payload, media.offer.Codec)
				processed := processCarrierInput(row, audioFrontend, pcm)
				localSpeechStarted := processed.SpeechStarted && playback.hasPending()
				if localSpeechStarted {
					audioFrontend.markLocalSignal()
				}
				pcm24 := inputResampler.Process(processed.PCM)
				if len(pcm24) == 0 {
					continue
				}
				if err := coreWriter.Write(ws.OpBinary, pcm16ToBytes(pcm24)); err != nil {
					closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write caller audio to realtime bridge")
					return
				}
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
		var preAnswerDroppedMS int64
		if hub := a.softphones.lookup(row.ID); hub != nil {
			preAnswerDroppedMS = hub.preAnswerDroppedMS()
		}
		_ = a.db().updateCarrierAudioDiagnostics(row.ID, carrierAudioDiagnostics{
			Provider: "sip_direct", Codec: carrierCodecPCMU8, SampleRate: 8000,
			PacerMode: pacerMode, MaxQueuedMS: maxQueuedMS, DroppedStaleMS: droppedStaleMS,
			PreAnswerMicrophoneDroppedMS: preAnswerDroppedMS,
			DropEvents:                   pacer.dropEvents(),
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
		pcm8 := outputResampler.Process(bytesToPCM16(data))
		if len(pcm8) == 0 {
			continue
		}
		chunks := twilioAudioPackets(pcm8, len(data)/2, nextFrame)
		packets := make([]sipRTPOutboundPacket, 0, len(chunks))
		for _, chunk := range chunks {
			pcm := chunk.PCM
			if len(pcm) < sipRTPPacketSamples {
				padded := make([]int16, sipRTPPacketSamples)
				copy(padded, pcm)
				pcm = padded
			}
			packets = append(packets, sipRTPOutboundPacket{
				payload: encodeSIPG711(pcm, media.offer.Codec),
				itemID:  nextFrame.ItemID, audioEndMS: chunk.AudioEndMS,
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
