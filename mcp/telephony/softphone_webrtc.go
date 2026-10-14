package main

// WebRTC is an optional browser boundary for the existing human media hub.
// The in-memory pipe speaks its existing framed PCM/control protocol. Carrier,
// ownership, hold, recording, listener and coaching code retain one authority.
import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const rtcSetupTimeout = 18 * time.Second

type softphoneRTCConfig struct {
	socketTelemetry  *rtcSocketTelemetry
	NetworkContext   audioNetworkEvent
	KnownVPNExits    string
	CollectNetwork   func(audioNetworkEvent)
	Enabled          bool
	ICEServers       []webrtc.ICEServer
	PublicIPs        []string
	PortMin, PortMax uint16
	Bitrate          int
	InputRate        int
	FEC              *bool // nil retains the existing enabled preference.
}

func (c softphoneRTCConfig) relayConfigured() bool {
	for _, server := range c.ICEServers {
		for _, endpoint := range server.URLs {
			if strings.HasPrefix(endpoint, "turn:") || strings.HasPrefix(endpoint, "turns:") {
				return true
			}
		}
	}
	return false
}

func (c softphoneRTCConfig) fecEnabled() bool { return c.FEC == nil || *c.FEC }

func parseSoftphoneRTCConfig(values map[string]string) (softphoneRTCConfig, error) {
	c := softphoneRTCConfig{Enabled: values["softphone_webrtc_enabled"] == "true", Bitrate: 32000, InputRate: 48000}
	if v := values["softphone_webrtc_encoder_input_rate"]; v != "" {
		rate, err := strconv.Atoi(v)
		if err != nil || (rate != 24000 && rate != 48000) {
			return c, errors.New("invalid WebRTC encoder input rate")
		}
		c.InputRate = rate
	}
	if !c.Enabled {
		return c, nil
	}
	if text := strings.TrimSpace(values["softphone_webrtc_ice_servers"]); text != "" {
		if len(text) > 8192 || json.Unmarshal([]byte(text), &c.ICEServers) != nil || len(c.ICEServers) > 8 {
			return c, errors.New("invalid ICE servers")
		}
		for _, server := range c.ICEServers {
			if len(server.URLs) == 0 || len(server.URLs) > 8 {
				return c, errors.New("invalid ICE server URLs")
			}
			for _, u := range server.URLs {
				if !strings.HasPrefix(u, "stun:") && !strings.HasPrefix(u, "turn:") && !strings.HasPrefix(u, "turns:") {
					return c, errors.New("unsupported ICE URL")
				}
			}
		}
	}
	for _, ip := range strings.Split(values["softphone_webrtc_public_ips"], ",") {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil || len(c.PublicIPs) >= 8 {
			return c, errors.New("invalid public ICE address")
		}
		c.PublicIPs = append(c.PublicIPs, ip)
	}
	if values["softphone_webrtc_udp_port_min"] != "" || values["softphone_webrtc_udp_port_max"] != "" {
		lo, e1 := strconv.Atoi(values["softphone_webrtc_udp_port_min"])
		hi, e2 := strconv.Atoi(values["softphone_webrtc_udp_port_max"])
		if e1 != nil || e2 != nil || lo < 1024 || hi < lo || hi > 65535 {
			return c, errors.New("invalid UDP port range")
		}
		c.PortMin = uint16(lo)
		c.PortMax = uint16(hi)
	}
	return c, nil
}

func softphoneRTCAPI(c softphoneRTCConfig, settings ...func(*webrtc.SettingEngine)) (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	fec := "1"
	if !c.fecEnabled() {
		fec = "0"
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=" + fec + ";stereo=0;sprop-stereo=0;maxaveragebitrate=32000"}, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, err
	}
	s := webrtc.SettingEngine{}
	if c.socketTelemetry != nil {
		network, err := newRTCObservedNet(c.socketTelemetry)
		if err != nil {
			return nil, err
		}
		s.SetNet(network)
	}
	s.SetIncludeLoopbackCandidate(true)
	s.SetICETimeouts(5*time.Second, 10*time.Second, 2*time.Second)
	if c.PortMin != 0 {
		if err := s.SetEphemeralUDPPortRange(c.PortMin, c.PortMax); err != nil {
			return nil, err
		}
	}
	if len(c.PublicIPs) > 0 {
		s.SetNAT1To1IPs(c.PublicIPs, webrtc.ICECandidateTypeHost)
	}
	for _, apply := range settings {
		apply(&s)
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i), webrtc.WithSettingEngine(s)), nil
}

type rtcHubConn struct {
	disconnect *rtcDisconnectTracker
	net.Conn
	stop    func()
	stats   *rtcMediaStats
	whisper func([]byte, func() bool) bool
}

func (c *rtcHubConn) Close() error { c.stop(); return nil }
func (c *rtcHubConn) queueWhisper(data []byte, valid func() bool) bool {
	return c.whisper(data, valid)
}

type rtcMediaSnapshot struct {
	Ingress                rtcIngressSnapshot `json:"rtp_ingress"`
	CoachingMixedMS        int64              `json:"coaching_mixed_ms,omitempty"`
	IngressConcealedMS     int64              `json:"ingress_concealed_ms"`
	Encoder                *rtcEncoderState   `json:"encoder,omitempty"`
	EncoderControlErrors   uint64             `json:"encoder_control_errors"`
	RTP                    rtcSendSnapshot    `json:"rtp_send"`
	IngressDiscardedMS     int64              `json:"ingress_discarded_ms"`
	CaptureMaxQueueMS      int64              `json:"capture_max_queue_ms"`
	IngressRejectedPackets int64              `json:"ingress_rejected_packets"`
	IngressQueueDrops      int64              `json:"ingress_queue_drops"`
	IngressPaddingPackets  int64              `json:"ingress_padding_packets"`
	DecodeErrors           int64              `json:"decode_errors"`
	OutboundDroppedMS      int64              `json:"outbound_dropped_ms"`
	PacingSkippedMS        int64              `json:"pacing_skipped_ms"`
	PartialFlushedFrames   uint64             `json:"partial_flushed_frames"`
	PartialPaddedMS        float64            `json:"partial_padded_ms"`
	MaxQueueMS             int                `json:"max_queue_ms"`
	Events                 []audioDropEvent   `json:"events,omitempty"`
}
type rtcMediaStats struct {
	captureMaxQueue atomic.Int64
	ingress         rtcIngressTelemetry
	direction       string
	voice           rtcVoiceControl
	rtp             rtcSendTelemetry
	mu              sync.Mutex
	value           rtcMediaSnapshot
}

func (s *rtcMediaStats) snapshot() rtcMediaSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.value
	v.CaptureMaxQueueMS = s.captureMaxQueue.Load()
	v.Encoder = s.voice.codec.Load()
	v.EncoderControlErrors = s.voice.configurationErrors.Load()
	v.RTP = s.rtp.snapshot()
	v.Ingress = s.ingress.snapshot()
	v.Events = append([]audioDropEvent(nil), v.Events...)
	return v
}
func (s *rtcMediaStats) drop(reason, direction string, sequence uint64, duration int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch reason {
	case "webrtc_capture_source_age", "webrtc_capture_release_overflow":
		s.value.IngressDiscardedMS += int64(duration)
	case "webrtc_rtp_rejected":
		s.value.IngressRejectedPackets++
	case "webrtc_ingress_overflow":
		s.value.IngressQueueDrops++
	case "webrtc_decode_error":
		s.value.DecodeErrors++
	case "webrtc_pacing_gap":
		s.value.PacingSkippedMS += int64(duration)
	default:
		s.value.OutboundDroppedMS += int64(duration)
	}
	s.value.Events = append(s.value.Events, audioDropEvent{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: direction, Reason: reason, Sequence: sequence, DurationMS: duration})
	if len(s.value.Events) > 32 {
		s.value.Events = s.value.Events[len(s.value.Events)-32:]
	}
}
func mergeRTCSnapshots(a, b rtcMediaSnapshot) rtcMediaSnapshot {
	a.CoachingMixedMS += b.CoachingMixedMS
	a.IngressConcealedMS += b.IngressConcealedMS
	if b.Encoder != nil {
		a.Encoder = b.Encoder
	}
	a.EncoderControlErrors += b.EncoderControlErrors
	a.RTP = mergeRTCSends(a.RTP, b.RTP)
	a.Ingress = mergeRTCIngress(a.Ingress, b.Ingress)
	a.IngressDiscardedMS += b.IngressDiscardedMS
	a.CaptureMaxQueueMS = max(a.CaptureMaxQueueMS, b.CaptureMaxQueueMS)
	a.IngressRejectedPackets += b.IngressRejectedPackets
	a.IngressQueueDrops += b.IngressQueueDrops
	a.IngressPaddingPackets += b.IngressPaddingPackets
	a.DecodeErrors += b.DecodeErrors
	a.OutboundDroppedMS += b.OutboundDroppedMS
	a.PacingSkippedMS += b.PacingSkippedMS
	a.PartialFlushedFrames += b.PartialFlushedFrames
	a.PartialPaddedMS += b.PartialPaddedMS
	a.MaxQueueMS = max(a.MaxQueueMS, b.MaxQueueMS)
	a.Events = append(a.Events, b.Events...)
	if len(a.Events) > 32 {
		a.Events = a.Events[len(a.Events)-32:]
	}
	return a
}

// A bounded RTP jitter timeline: reorder within the initial 60ms cushion,
// discard duplicates and packets >320ms behind their original timeline.
// RTP wraps are compared modulo 2^32. DTX gaps never become PCM sequence loss.
type rtcJitter struct {
	baseTS  uint32
	base    time.Time
	last    uint32
	played  bool
	packets []rtcPacket
	arrival time.Time
	discard func(*rtp.Packet)
}
type rtcPacket struct {
	packet *rtp.Packet
	due    time.Time
}

func (j *rtcJitter) push(p *rtp.Packet, now time.Time) bool {
	if len(p.Payload) == 0 || len(p.Payload) > 1275 {
		return false
	}
	if j.base.IsZero() {
		j.baseTS = p.Timestamp
		j.base = now.Add(60 * time.Millisecond)
	}
	delta := int32(p.Timestamp - j.baseTS)
	// A discontinuous source timestamp starts a fresh bounded epoch.
	if delta < -48000 || delta > 48000*3600 {
		j.baseTS = p.Timestamp
		j.base = now.Add(60 * time.Millisecond)
		j.played = false
		j.packets = nil
		delta = 0
	}
	due := j.base.Add(time.Duration(delta) * time.Second / 48000)
	// Follow oscillator drift slowly (at most 200ppm); a delayed batch cannot
	// move the timeline by seconds. Earlier delivery can reduce the cushion.
	desired := now.Add(60*time.Millisecond - time.Duration(delta)*time.Second/48000)
	if desired.Before(j.base) {
		j.base = desired
	} else if !j.arrival.IsZero() {
		j.base = j.base.Add(min(desired.Sub(j.base), max(0, now.Sub(j.arrival))/5000))
	}
	j.arrival = now
	due = j.base.Add(time.Duration(delta) * time.Second / 48000)
	if j.played && int32(p.Timestamp-j.last) <= 0 || now.Sub(due) > 320*time.Millisecond || due.Sub(now) > 320*time.Millisecond {
		return false
	}
	for _, queued := range j.packets {
		if queued.packet.Timestamp == p.Timestamp {
			return false
		}
	}
	if len(j.packets) >= 16 {
		return false
	}
	payload := append([]byte(nil), p.Payload...)
	cp := *p
	cp.Payload = payload
	j.packets = append(j.packets, rtcPacket{&cp, due})
	for n := len(j.packets) - 1; n > 0 && j.packets[n].due.Before(j.packets[n-1].due); n-- {
		j.packets[n], j.packets[n-1] = j.packets[n-1], j.packets[n]
	}
	return true
}
func (j *rtcJitter) pop(now time.Time) *rtp.Packet {
	for len(j.packets) > 0 && !j.packets[0].due.After(now) {
		q := j.packets[0]
		j.packets = j.packets[1:]
		if now.Sub(q.due) > 320*time.Millisecond || (j.played && int32(q.packet.Timestamp-j.last) <= 0) {
			if j.discard != nil {
				j.discard(q.packet)
			}
			continue
		}
		j.last = q.packet.Timestamp
		j.played = true
		return q.packet
	}
	return nil
}

// connectSoftphoneRTC owns the real signaling socket until Close. PCM never
// crosses that socket. Private coaching is mixed only at this browser boundary,
// after carrier/recording/listener taps, retaining the pinned-adviser validator.
func connectSoftphoneRTC(signal, signalRead net.Conn, c softphoneRTCConfig) (net.Conn, func(), error) {
	stats := &rtcMediaStats{}
	stats.ingress.socket = &rtcSocketTelemetry{}
	c.socketTelemetry = stats.ingress.socket
	api, err := softphoneRTCAPI(c)
	if err != nil {
		signal.Close()
		return nil, nil, err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: c.ICEServers})
	if err != nil {
		signal.Close()
		return nil, nil, err
	}
	signalWriter := newWebSocketWriterPump(signal, ws.StateServerSide)
	hub, bridge := net.Pipe()
	input := newWebSocketWriterPump(bridge, ws.StateClientSide)
	done := make(chan struct{})
	stats.rtp.connectionID = c.NetworkContext.ConnectionID
	disconnect := &rtcDisconnectTracker{writer: signalWriter, pc: pc}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			disconnect.capture(nil, "rtc_bridge_closed")
			close(done)
			signal.Close()
			hub.Close()
			bridge.Close()
			signalWriter.Stop()
			input.Stop()
			pc.Close()
		})
	}
	fail := func(err error) (net.Conn, func(), error) {
		disconnect.capture(err, "")
		if c.CollectNetwork != nil {
			event := c.NetworkContext
			event.ID = event.ConnectionID + ":setup_failed"
			event.Event = "softphone.browser.setup_failed"
			event.Action = "setup_failed"
			info := disconnect.snapshot(err)
			event.Disconnect = &info
			event.CloseCode = disconnect.closeCode()
			event.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
			event.ExpiresAt = time.Now().Add(event.Retention).UTC().Format(time.RFC3339Nano)
			c.CollectNetwork(event)
		}
		stop()
		return nil, nil, err
	}
	connected := make(chan struct{}, 1)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			if state == webrtc.PeerConnectionStateFailed {
				disconnect.capture(nil, "rtc_connection_failed")
			}
			go stop()
		}
	})
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "caller", "telephony")
	if err != nil {
		return fail(err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		return fail(err)
	}
	parameters := sender.GetParameters()
	if len(parameters.Encodings) > 0 {
		stats.rtp.ssrc = uint32(parameters.Encodings[0].SSRC)
	}
	sender.Transport().ICETransport().OnSelectedCandidatePairChange(func(pair *webrtc.ICECandidatePair) {
		select {
		case <-done:
			return
		default:
		}
		stats.rtp.selectedPath(pair, c.NetworkContext, c.KnownVPNExits, c.CollectNetwork)
	})
	go func() {
		for {
			packets, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, packet := range packets {
				if report, ok := packet.(*rtcp.ReceiverReport); ok {
					for _, r := range report.Reports {
						if r.SSRC == stats.rtp.ssrc {
							stats.voice.observe(r.LastSequenceNumber, r.FractionLost, time.Now())
						}
					}
				}
			}
		}
	}()
	var trackOnce sync.Once
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !strings.EqualFold(remote.Codec().MimeType, webrtc.MimeTypeOpus) {
			go stop()
			return
		}
		trackOnce.Do(func() { go receiveSoftphoneRTP(remote, input, stats, done, stop, c.fecEnabled()) })
	})
	config, _ := json.Marshal(map[string]any{"type": "webrtc.config", "ice_servers": c.ICEServers, "relay_configured": c.relayConfigured(), "bitrate": c.Bitrate, "fec_requested": c.fecEnabled()})
	if err := signalWriter.Write(ws.OpText, config); err != nil {
		return fail(err)
	}
	deadline := time.Now().Add(rtcSetupTimeout)
	signal.SetReadDeadline(deadline)
	data, op, err := readWebSocketData(signalRead, ws.StateServerSide, signalWriter)
	if err != nil {
		return fail(err)
	}
	var offer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if op != ws.OpText || len(data) > 65536 || json.Unmarshal(data, &offer) != nil || offer.Type != "webrtc.offer" || len(offer.SDP) > 60000 || strings.Contains(offer.SDP, "m=video ") || strings.Contains(offer.SDP, "m=application ") {
		return fail(errors.New("invalid audio SDP offer"))
	}
	if !c.fecEnabled() {
		offer.SDP, err = disableRTCSDPFEC(offer.SDP)
		if err != nil {
			return fail(errors.New("invalid audio SDP offer"))
		}
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		return fail(errors.New("invalid remote description"))
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(err)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-gather:
	case <-done:
		return fail(errors.New("WebRTC closed during setup"))
	case <-timer.C:
		return fail(errors.New("ICE gathering timed out"))
	}
	answerJSON, _ := json.Marshal(map[string]any{"type": "webrtc.answer", "sdp": pc.LocalDescription().SDP})
	if err := signalWriter.Write(ws.OpText, answerJSON); err != nil {
		return fail(err)
	}
	go func() {
		for {
			data, op, err := readWebSocketData(signalRead, ws.StateServerSide, signalWriter)
			if err != nil {
				disconnect.capture(err, "")
				stop()
				return
			}
			if op != ws.OpText || len(data) > 65536 {
				disconnect.capture(ws.ProtocolError("invalid signaling frame"), "")
				stop()
				return
			}
			if !input.queueControl(data) {
				stop()
				return
			}
		}
	}()
	select {
	case <-connected:
	case <-done:
		return fail(errors.New("WebRTC setup failed"))
	case <-timer.C:
		return fail(errors.New("WebRTC connection timed out"))
	}
	signal.SetReadDeadline(time.Time{})
	whispers := make(chan rtcWhisperFrame, 6)
	queueWhisper := func(data []byte, valid func() bool) bool {
		return queueRTCWhisper(whispers, data, valid, done)
	}
	go sendRTCAudio(bridge, input, signalWriter, track, c.Bitrate, stats, done, stop, c.fecEnabled(), liveAudioMaxAge, 320, whispers, c.InputRate)
	return &rtcHubConn{Conn: hub, stop: stop, stats: stats, whisper: queueWhisper, disconnect: disconnect}, stop, nil
}

func receiveSoftphoneRTP(remote *webrtc.TrackRemote, input *websocketWriterPump, stats *rtcMediaStats, done <-chan struct{}, stop func(), fec bool) {
	receiveRTCAudio(remote, stats, done, stop, fec, func(samples []int16, at time.Time) { queueMicrophoneSource(input, pcm16ToBytes(samples), at, true) })
}

// The source instant follows the RTP timeline, including the bounded jitter
// cushion. Consumers can enforce a shorter lease/age budget before forwarding.
func receiveRTCAudio(remote *webrtc.TrackRemote, stats *rtcMediaStats, done <-chan struct{}, stop func(), fec bool, forward func([]int16, time.Time)) {
	direction := stats.direction
	if direction == "" {
		direction = "operator_to_carrier"
	}
	decoder, err := newRTCOpusDecoderWithFEC(fec)
	if err != nil {
		stop()
		return
	}
	defer decoder.Close()
	type receivedPacket struct {
		packet *rtp.Packet
		at     time.Time
	}
	packets := make(chan receivedPacket, 16)
	go func() {
		defer close(packets)
		for {
			p, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			at := time.Now()
			processed := at
			if stats.ingress.socket != nil {
				if socket, ok := stats.ingress.socket.takePacket(p.SSRC, p.SequenceNumber, at); ok {
					// Kernel timing is evidence only. Collection availability or
					// contention must never change the media clock/admission.
					stats.ingress.observeSocket(p, socket, at, stats.rtp.connectionID, stats.rtp.pathRevision.Load())
				}
			}
			if ignoreRTCPadding(p, stats) {
				stats.ingress.observe(p, at, processed, len(packets), stats.rtp.connectionID, stats.rtp.pathRevision.Load(), "padding", 0)
				continue
			}
			stats.ingress.observe(p, at, processed, len(packets), stats.rtp.connectionID, stats.rtp.pathRevision.Load(), "received", 0)
			select {
			case packets <- receivedPacket{p, at}:
			case <-done:
				return
			default:
				stats.drop("webrtc_ingress_overflow", direction, uint64(p.SequenceNumber), 0)
			}
		}
	}()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var due <-chan time.Time
	j := rtcJitter{discard: func(p *rtp.Packet) {
		stats.drop("webrtc_rtp_rejected", direction, uint64(p.SequenceNumber), 0)
	}}
	pcm := make([]int16, 24000*120/1000)
	var timeline rtcDecodeTimeline
	var release rtcAudioRelease

	for {
		select {
		case <-done:
			return
		case received, ok := <-packets:
			if !ok {
				return
			}
			p := received.packet
			stats.ingress.observe(p, received.at, time.Now(), len(packets), stats.rtp.connectionID, stats.rtp.pathRevision.Load(), "processed", 0)
			if !j.push(p, received.at) {
				stats.drop("webrtc_rtp_rejected", direction, uint64(p.SequenceNumber), 0)
			}
		case <-due:
		}
		now := time.Now()
		for p := j.pop(now); p != nil; p = j.pop(now) {
			if timeline.ssrc != p.SSRC {
				timeline.valid = false
			}
			repaired := 0
			for gap := timeline.missing(p.SequenceNumber, p.Timestamp); gap > 0; gap-- {
				if n, err := decoder.recover(p.Payload, pcm[:480], fec && gap == 1); err == nil && n == 480 {
					release.push(pcm[:n], j.base.Add(time.Duration(int32(p.Timestamp-j.baseTS))*time.Second/48000-60*time.Millisecond-time.Duration(gap)*20*time.Millisecond), stats, direction)
					stats.mu.Lock()
					stats.value.IngressConcealedMS += 20
					repaired += 20
					stats.mu.Unlock()
				}
			}
			if repaired > 0 {
				stats.ingress.observe(p, now, now, len(packets), stats.rtp.connectionID, stats.rtp.pathRevision.Load(), "concealment", repaired)
			}
			n, err := decoder.decode(p.Payload, pcm)
			if err != nil {
				timeline.valid = false
				stats.drop("webrtc_decode_error", direction, uint64(p.SequenceNumber), 0)
				continue
			}
			timeline = rtcDecodeTimeline{ssrc: p.SSRC, valid: true, seq: p.SequenceNumber, ts: p.Timestamp, samples: n}
			release.push(pcm[:n], j.base.Add(time.Duration(int32(p.Timestamp-j.baseTS))*time.Second/48000-60*time.Millisecond), stats, direction)
		}
		if f, ok := release.pop(now, stats, direction); ok {
			forward(f.pcm, f.source)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		due = nil
		var wake time.Time
		if len(j.packets) > 0 {
			wake = j.packets[0].due
		}
		if release.count > 0 && (wake.IsZero() || release.next.Before(wake)) {
			wake = release.next
		}
		if !wake.IsZero() {
			timer.Reset(max(time.Until(wake), time.Microsecond))
			due = timer.C
		}
	}
}

// Browser bandwidth probes may carry RTP padding without an Opus payload.
// They are transport traffic, never missing speech or decoder failures.
func ignoreRTCPadding(p *rtp.Packet, stats *rtcMediaStats) bool {
	if !p.Padding || len(p.Payload) != 0 {
		return false
	}
	stats.mu.Lock()
	stats.value.IngressPaddingPackets++
	stats.mu.Unlock()
	return true
}

func sendSoftphoneRTP(bridge net.Conn, input, signal *websocketWriterPump, track *webrtc.TrackLocalStaticRTP, bitrate int, stats *rtcMediaStats, done <-chan struct{}, stop func(), fec bool) {
	sendRTCAudio(bridge, input, signal, track, bitrate, stats, done, stop, fec, liveAudioMaxAge, 320, nil)
}

func sendRTCAudio(bridge net.Conn, input, signal *websocketWriterPump, track *webrtc.TrackLocalStaticRTP, bitrate int, stats *rtcMediaStats, done <-chan struct{}, stop func(), fec bool, maxAge time.Duration, sourceMaxMS float64, whispers <-chan rtcWhisperFrame, inputRates ...int) {
	direction := stats.direction
	if direction == "" {
		direction = "carrier_to_operator"
	}
	requestedRate := 48000
	if len(inputRates) > 0 && inputRates[0] != 0 {
		requestedRate = inputRates[0]
	}
	encoder, err := newRTCOpusEncoderAtRate(bitrate, fec, requestedRate)
	if err != nil {
		stop()
		return
	}
	defer encoder.Close()
	policy := newRTCVoicePolicy(bitrate)
	stats.voice.codec.Store(rtcOpusEncoderState(encoder, fec, bitrate, 10, time.Now()))
	// Keep 20ms frame boundaries and a bounded 120ms outgoing queue. Encoding
	// and RTP writes are paced outside the hub's carrier receive loop.
	frames := make(chan rtcPCMFrame, 6)
	type pcmChunk struct {
		data    []byte
		expires time.Time
	}
	// No second PCM queue: the paced owner owns framing/partial deadlines.
	chunks := make(chan pcmChunk)
	go func() {
		defer close(chunks)
		for {
			data, op, err := readWebSocketData(bridge, ws.StateClientSide, input)
			if err != nil {
				var closed wsutil.ClosedError
				if errors.As(err, &closed) {
					_ = signal.write(ws.OpClose, ws.NewCloseFrameBody(closed.Code, closed.Reason), 250*time.Millisecond)
				}
				stop()
				return
			}
			if op == ws.OpText {
				if err := signal.Write(op, data); err != nil {
					stop()
					return
				}
				continue
			}
			if op != ws.OpBinary {
				continue
			}
			if len(data) >= 16 && binary.LittleEndian.Uint32(data) == coachPlaybackMagic {
				// Coaching bypasses this PCM pipe through rtcHubConn.queueWhisper,
				// retaining the hub's pinned-adviser validator at the final write.
				continue
			}
			header := sourceAudioHeader(data)
			now := time.Now()
			expires := now.Add(maxAge)
			age := 0.0
			if header == 64 && mathFloat64(data[48:56]) > 0 {
				age = mediaClockMS() - mathFloat64(data[48:56])
				if age >= 0 && !math.IsInf(age, 0) {
					expires = now.Add(min(maxAge, time.Duration((sourceMaxMS-age)*float64(time.Millisecond))))
				}
			}
			if age > sourceMaxMS {
				stats.drop("webrtc_playback_source_age", direction, 0, (len(data)-header)*1000/48000)
				continue
			}
			if header == 0 {
				header = 0
			} else if header != 64 {
				header = 32
			}
			if len(data) < header || (len(data)-header)%2 != 0 || len(data) > 24000*2 {
				continue
			}
			select {
			case chunks <- pcmChunk{data[header:], expires}:
			case <-done:
				return
			}
		}
	}()
	inputRate := rtcEncoderInputRate(encoder)
	frameSamples := inputRate / 50
	resampler := newPCMResampler(24000, inputRate)
	whisperResampler := newPCMResampler(8000, inputRate)
	converted := make([]int16, 0, frameSamples+2)
	overlay := make([]int16, 0, frameSamples+2)
	mixedSamples := make([]float32, frameSamples)
	silence := make([]int16, 480)
	var whisperEpoch uint32
	samples := make([]float32, 0, frameSamples*2)
	out := make([]byte, 1275)
	clock := rtcRTPClock{base: time.Now(), baseTS: rand.Uint32()}
	sequence := rtp.NewRandomSequencer()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var pending rtcPCMFramer
	var lastChunkAt time.Time
	for {
		select {
		case <-done:
			return
		case chunk, open := <-chunks:
			if !open {
				return
			}
			now := time.Now()
			if dropped := pending.discardExpired(now); dropped > 0 {
				stats.drop("webrtc_playback_partial_age", direction, 0, dropped*1000/24000)
			}
			lastChunkAt = now
			if !pending.push(chunk.data, chunk.expires, func(frame rtcPCMFrame) bool {
				return queueRTCFrame(frames, frame, done, stats)
			}) {
				return
			}
		case <-tick.C:
			now := time.Now()
			if rate, loss, changed := policy.next(now, stats.voice.feedback.Load()); changed {
				if err := encoder.configure(rate, loss); err != nil {
					stats.voice.configurationErrors.Add(1)
				} else {
					stats.voice.codec.Store(rtcOpusEncoderState(encoder, fec, rate, loss, now))
				}
			}
			if clock.sent && clock.slot(now) <= clock.lastSlot {
				continue
			}
			frame, open := nextRTCFrame(frames, now, stats)
			if !open {
				return
			}
			if !lastChunkAt.IsZero() && now.Sub(lastChunkAt) >= 20*time.Millisecond {
				if dropped := pending.discardExpired(now); dropped > 0 {
					stats.drop("webrtc_playback_partial_age", direction, 0, dropped*1000/24000)
				}
				if tail, padding := pending.flushPartial(now); tail.pcm != nil {
					stats.mu.Lock()
					stats.value.PartialFlushedFrames++
					stats.value.PartialPaddedMS += float64(padding) / 24
					stats.mu.Unlock()
					if frame.pcm == nil {
						frame = tail
					} else {
						queueRTCFrame(frames, tail, done, stats)
					}
				}
			}
			if frame.pcm == nil {
				frame = rtcPCMFrame{pcm: silence, expires: now.Add(maxAge)}
			}
			if inputRate == 24000 {
				converted = append(converted[:0], frame.pcm...)
			} else {
				converted = resampler.ProcessInto(frame.pcm, converted)
			}
			for _, v := range converted {
				samples = append(samples, float32(v)/32768)
			}
			if len(samples) < frameSamples {
				continue
			}
			voice := nextRTCWhisper(whispers, now)
			mixed := voice != nil
			encodeSamples := samples[:frameSamples]
			if voice != nil {
				if voice.epoch != whisperEpoch {
					whisperResampler = newPCMResampler(8000, inputRate)
					whisperEpoch = voice.epoch
				}
				overlay = whisperResampler.ProcessInto(voice.pcm, overlay)
				encodeSamples = mixRTCWhisperInto(encodeSamples, overlay, mixedSamples)
			}
			n, err := encoder.EncodeFloat32(encodeSamples, out)
			// A stop/replacement during encoding invalidates the private overlay.
			if voice != nil && (!voice.valid() || time.Since(voice.at) > coachAudioMaxAge) {
				mixed = false
				n, err = encoder.EncodeFloat32(samples[:frameSamples], out)
			}
			samples = samples[:copy(samples, samples[frameSamples:])]
			if err != nil {
				stop()
				return
			}
			now = time.Now()
			if !frame.expires.After(now) {
				stats.drop("webrtc_playback_encode_age", direction, 0, 20)
				continue
			}
			timestamp, skipped := clock.next(now)
			if skipped > 0 {
				stats.drop("webrtc_pacing_gap", direction, 0, skipped)
			}
			if err := writeObservedRTP(track.WriteRTP, &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence.NextSequenceNumber(), Timestamp: timestamp}, Payload: append([]byte(nil), out[:n]...)}, &stats.rtp); err != nil {
				stop()
				return
			}
			if mixed {
				stats.mu.Lock()
				stats.value.CoachingMixedMS += int64(len(voice.pcm) * 1000 / 8000)
				stats.mu.Unlock()
			}
		}
	}
}

type rtcPCMFrame struct {
	pcm     []int16
	expires time.Time
}

// A process scheduling pause must not turn a small queue into old speech.
func nextRTCFrame(frames <-chan rtcPCMFrame, now time.Time, stats *rtcMediaStats) (rtcPCMFrame, bool) {
	for {
		select {
		case frame, open := <-frames:
			if !open {
				return rtcPCMFrame{}, false
			}
			if !frame.expires.After(now) {
				stats.drop("webrtc_playback_queue_age", "carrier_to_operator", 0, len(frame.pcm)*1000/24000)
				continue
			}
			return frame, true
		default:
			return rtcPCMFrame{}, true
		}
	}
}

// RTP media time follows the monotonic live clock, even when the encoder or
// scheduler misses ticks. Sequence numbers advance only for packets sent;
// omitted source slots must not manufacture transport packet-loss counters.
type rtcRTPClock struct {
	base     time.Time
	baseTS   uint32
	lastSlot int64
	sent     bool
}

func (c *rtcRTPClock) slot(now time.Time) int64 {
	if c.base.IsZero() {
		return 0
	}
	return int64(now.Sub(c.base) / (20 * time.Millisecond))
}
func (c *rtcRTPClock) next(now time.Time) (uint32, int) {
	if c.base.IsZero() {
		c.base = now
	}
	slot := c.slot(now)
	skipped := 0
	if c.sent && slot > c.lastSlot+1 {
		skipped = int((slot - c.lastSlot - 1) * 20)
	}
	c.lastSlot = slot
	c.sent = true
	return c.baseTS + uint32(slot*960), skipped
}

func mathFloat64(b []byte) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(b)) }
