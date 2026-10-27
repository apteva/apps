package main

// A receive-only listening PC and a fresh send-only coaching PC per push-to-talk
// share the authorized listener control socket. No carrier session is created.
import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/rtcp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

type listenerRTCConn struct {
	ice *rtcICESession
	net.Conn
	stats      [2]*rtcMediaStats
	coachStats *rtcMediaStats
}

// Negotiate only audio, with a bounded wait which is canceled by listener/session
// teardown. A coaching PC is never reused for a later generation.
func answerListenerRTC(pc *webrtc.PeerConnection, sdp string, fec bool, done <-chan struct{}) (string, error) {
	if len(sdp) > 60000 || strings.Contains(sdp, "m=video ") || strings.Contains(sdp, "m=application ") {
		return "", errors.New("invalid audio SDP offer")
	}
	var err error
	if !fec {
		sdp, err = disableRTCSDPFEC(sdp)
		if err != nil {
			return "", err
		}
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	timer := time.NewTimer(rtcSetupTimeout)
	defer timer.Stop()
	select {
	case <-gather:
		return pc.LocalDescription().SDP, nil
	case <-done:
		return "", errors.New("listener canceled")
	case <-timer.C:
		return "", errors.New("ICE gathering timed out")
	}
}

func connectListenerRTC(signal, signalRead net.Conn, c softphoneRTCConfig, l *callListener) (*listenerRTCConn, func(), error) {
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
	writer := newWebSocketWriterPump(signal, ws.StateServerSide)
	hub, bridge := net.Pipe()
	input := newWebSocketWriterPump(bridge, ws.StateClientSide)
	done := make(chan struct{})
	var once sync.Once
	var coachMu sync.Mutex
	var coachPC *webrtc.PeerConnection
	var coachDone chan struct{}
	var coachGeneration uint32
	closeCoachLocked := func() {
		if coachDone != nil {
			close(coachDone)
			coachDone = nil
		}
		if coachPC != nil {
			coachPC.Close()
			coachPC = nil
		}
	}
	stop := func() {
		once.Do(func() {
			close(done)
			signal.Close()
			hub.Close()
			bridge.Close()
			writer.Stop()
			input.Stop()
			pc.Close()
			coachMu.Lock()
			closeCoachLocked()
			coachMu.Unlock()
		})
	}
	result := &listenerRTCConn{Conn: hub, coachStats: &rtcMediaStats{direction: "coach_to_adviser"}}
	tracks := [2]*webrtc.TrackLocalStaticRTP{}
	sourceReaders := [2]net.Conn{}
	sourceWriters := [2]*websocketWriterPump{}
	cleanup := func() {
		stop()
		for d := range sourceReaders {
			if sourceReaders[d] != nil {
				sourceReaders[d].Close()
			}
			if sourceWriters[d] != nil {
				sourceWriters[d].conn.Close()
				sourceWriters[d].Stop()
			}
		}
	}
	failSetup := func(err error) (*listenerRTCConn, func(), error) { cleanup(); return nil, nil, err }
	setupDone := make(chan struct{})
	defer close(setupDone)
	go func() {
		select {
		case <-l.done:
			stop()
		case <-done:
		case <-setupDone:
		}
	}()
	for d, name := range []string{"caller", "adviser"} {
		track, e := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, name, "telephony-listen")
		if e != nil {
			return failSetup(e)
		}
		sender, e := pc.AddTrack(track)
		if e != nil {
			return failSetup(e)
		}
		tracks[d] = track
		result.stats[d] = &rtcMediaStats{direction: []string{"caller_to_listener", "adviser_to_listener"}[d]}
		result.stats[d].voice.policy.Store(c.NetworkPolicy)
		parameters := sender.GetParameters()
		if len(parameters.Encodings) > 0 {
			result.stats[d].rtp.ssrc = uint32(parameters.Encodings[0].SSRC)
		}
		go func() {
			for {
				packets, _, e := sender.ReadRTCP()
				if e != nil {
					return
				}
				for _, packet := range packets {
					if report, ok := packet.(*rtcp.ReceiverReport); ok {
						for _, r := range report.Reports {
							if r.SSRC == result.stats[d].rtp.ssrc {
								result.stats[d].voice.observe(r.LastSequenceNumber, r.FractionLost, time.Now())
							}
						}
					}
				}
			}
		}()
		read, write := net.Pipe()
		sourceReaders[d] = read
		sourceWriters[d] = newWebSocketWriterPump(write, ws.StateServerSide)
	}
	connected := make(chan struct{}, 1)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
		if state == webrtc.PeerConnectionStateFailed {
			go stop()
		}
	})
	// Passive listening negotiates two recvonly transceivers; no remote capture is
	// accepted on this PC, even if a malicious browser adds a microphone.
	pc.OnTrack(func(_ *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go stop() })
	setupContext, cancelSetup := context.WithTimeout(context.Background(), rtcSetupTimeout)
	defer cancelSetup()
	iceSession, err := newRTCICESession(setupContext, c, writer, done)
	result.ice = iceSession
	if err != nil {
		return failSetup(err)
	}
	if err = writer.Write(ws.OpText, iceSession.initial(true)); err != nil {
		return failSetup(err)
	}

	signal.SetReadDeadline(time.Now().Add(rtcSetupTimeout))
	data, op, err := readWebSocketData(signalRead, ws.StateServerSide, writer)
	if err != nil {
		return failSetup(err)
	}
	var offer struct {
		Type       string `json:"type"`
		SDP        string `json:"sdp"`
		ICEVersion int    `json:"ice_version"`
	}
	if op != ws.OpText || len(data) > 65536 || json.Unmarshal(data, &offer) != nil || offer.Type != "webrtc.offer" {
		return failSetup(errors.New("invalid listener offer"))
	}
	if c.TURN != nil && offer.ICEVersion != 1 {
		return failSetup(errors.New("managed TURN requires an updated listener"))
	}
	sdp, err := answerListenerRTC(pc, offer.SDP, c.fecEnabled(), done)
	if err != nil {
		return failSetup(err)
	}
	// Enforce exactly two receive-only browser media sections. Passive and coach
	// credentials cannot upload media or obtain an operator's RTP endpoint.
	if !listenerRTCSDPDirection(offer.SDP, "recvonly", 2) {
		return failSetup(errors.New("listener must be receive-only"))
	}
	answer, _ := json.Marshal(map[string]any{"type": "webrtc.answer", "sdp": sdp})
	if err = writer.Write(ws.OpText, answer); err != nil {
		return failSetup(err)
	}
	timer := time.NewTimer(rtcSetupTimeout)
	defer timer.Stop()
	select {
	case <-connected:
	case <-done:
		return failSetup(errors.New("listener closed"))
	case <-l.done:
		return failSetup(errors.New("listener canceled"))
	case <-timer.C:
		return failSetup(errors.New("listener WebRTC timed out"))
	}
	signal.SetReadDeadline(time.Time{})
	if c.TURN != nil {
		go iceSession.run(writer)
	}
	for d := range tracks {
		go sendRTCAudio(sourceReaders[d], sourceWriters[d], writer, tracks[d], c.Bitrate, result.stats[d], done, stop, c.fecEnabled(), listenerMaxAge, 200, nil, c.InputRate)
	}
	// Carry all existing controls through the original lease/PTT authority. Only
	// negotiated SDP is intercepted; coaching RTP is converted into generation-
	// tagged capture frames which pass the same authorization and expiry checks.
	go func() {
		defer stop()
		for {
			data, op, err := readWebSocketData(signalRead, ws.StateServerSide, writer)
			if err != nil {
				return
			}
			if op != ws.OpText || len(data) > 65536 {
				return
			}
			var event struct {
				Type       string `json:"type"`
				Generation uint32 `json:"generation"`
				SDP        string `json:"sdp"`
				Revision   uint64 `json:"revision"`
			}
			if json.Unmarshal(data, &event) != nil {
				return
			}
			if event.Type == "webrtc.ice_refresh" {
				iceSession.requestRefresh()
				continue
			}
			if event.Type == "webrtc.restart_offer" {
				if !listenerRTCSDPDirection(event.SDP, "recvonly", 2) {
					return
				}
				iceSession.restart(pc, event.SDP, event.Revision, writer, "webrtc.restart_answer", nil, func() bool {
					select {
					case <-l.done:
						return false
					default:
						return true
					}
				}, result.stats[:]...)
				continue
			}
			if event.Type == "coach.webrtc.restart_offer" {
				if !listenerRTCCoachValid(l, event.Generation) || !listenerRTCSDPDirection(event.SDP, "sendonly", 1) {
					continue
				}
				coachMu.Lock()
				capture := coachPC
				generation := coachGeneration
				coachMu.Unlock()
				if capture == nil || generation != event.Generation {
					continue
				}
				iceSession.restart(capture, event.SDP, event.Revision, writer, "coach.webrtc.restart_answer", &generation, func() bool { return listenerRTCCoachValid(l, generation) }, result.coachStats)
				continue
			}

			if event.Type == "coach.webrtc.offer" {
				if !l.coaching.grant.Enabled {
					return
				}
				if !l.coaching.talking.Load() || event.Generation != l.coaching.generation.Load() || time.Now().UnixMilli() >= l.coaching.deadline.Load() {
					continue
				}
				coachMu.Lock()
				if coachGeneration >= event.Generation {
					coachMu.Unlock()
					continue
				}
				closeCoachLocked()
				coachGeneration = event.Generation
				capture, e := api.NewPeerConnection(webrtc.Configuration{ICEServers: c.ICEServers})
				if e != nil {
					coachMu.Unlock()
					return
				}
				captureDone := make(chan struct{})
				coachPC = capture
				coachDone = captureDone
				coachMu.Unlock()
				generation := event.Generation
				var trackOnce sync.Once
				capture.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
					if !strings.EqualFold(remote.Codec().MimeType, webrtc.MimeTypeOpus) {
						go stop()
						return
					}
					trackOnce.Do(func() {
						go receiveRTCAudio(remote, result.coachStats, captureDone, func() { go stop() }, c.fecEnabled(), func(pcm []int16, source time.Time) {
							if !listenerRTCCoachValid(l, generation) || time.Since(source) > coachAudioMaxAge {
								return
							}
							// Split native Opus packets into the existing bounded AWC1 protocol.
							for len(pcm) > 0 {
								n := min(480, len(pcm))
								frame := make([]byte, 24+n*2)
								binary.LittleEndian.PutUint32(frame, coachCaptureMagic)
								binary.LittleEndian.PutUint32(frame[4:], generation)
								binary.LittleEndian.PutUint32(frame[8:], l.rtcCaptureSequence.Add(1))
								binary.LittleEndian.PutUint64(frame[16:], math.Float64bits(mediaClockMS()-float64(time.Since(source))/float64(time.Millisecond)))
								copy(frame[24:], pcm16ToBytes(pcm[:n]))
								input.QueueAudio(frame)
								pcm = pcm[n:]
							}
						}, c.ReceiveEstimator)
					})
				})
				// Negotiation is independent of the read loop, so stop/keepalive remains
				// responsive during ICE gathering and cannot keep a revoked mic alive.
				go func() {
					answer, e := answerListenerRTC(capture, event.SDP, c.fecEnabled(), captureDone)
					if e != nil || !listenerRTCCoachValid(l, generation) {
						coachMu.Lock()
						if coachPC == capture {
							closeCoachLocked()
						}
						coachMu.Unlock()
						return
					}
					if !listenerRTCSDPDirection(event.SDP, "sendonly", 1) {
						go stop()
						return
					}
					value, _ := json.Marshal(map[string]any{"type": "coach.webrtc.answer", "generation": generation, "sdp": answer})
					writer.queueControl(value)
				}()
				continue
			}
			if event.Type == "coach.stop" {
				coachMu.Lock()
				if event.Generation >= coachGeneration {
					closeCoachLocked()
				}
				coachMu.Unlock()
			}
			if !input.queueControl(data) {
				return
			}
		}
	}()
	go func() {
		defer cleanup()
		for {
			data, op, err := readWebSocketData(bridge, ws.StateClientSide, input)
			if err != nil {
				var closed wsutil.ClosedError
				if errors.As(err, &closed) {
					_ = writer.write(ws.OpClose, ws.NewCloseFrameBody(closed.Code, closed.Reason), 250*time.Millisecond)
				}
				return
			}
			if op == ws.OpText {
				var event struct {
					Type       string `json:"type"`
					Generation uint32 `json:"generation"`
				}
				_ = json.Unmarshal(data, &event)
				if event.Type == "coach.stopped" {
					coachMu.Lock()
					if event.Generation == 0 || event.Generation >= coachGeneration {
						closeCoachLocked()
					}
					coachMu.Unlock()
				}
				if err = writer.Write(op, data); err != nil {
					return
				}
				continue
			}
			if op != ws.OpBinary || len(data) < 26 || len(data) > 984 || len(data)%2 != 0 || binary.LittleEndian.Uint32(data) != listenerFrameMagic {
				return
			}
			d := binary.LittleEndian.Uint32(data[4:])
			if d > 1 {
				return
			}
			clock := float64(binary.LittleEndian.Uint64(data[16:]))
			age := mediaClockMS() - clock
			if age > 200 {
				l.stale[d].Add(1)
				continue
			}
			// Source timing survives the adapter so partial-frame expiration uses the
			// samples actually retained by the shared RTP framer.
			pcm := make([]byte, 64+len(data)-24)
			binary.LittleEndian.PutUint32(pcm, softphoneAudioFrameV3)
			binary.LittleEndian.PutUint64(pcm[48:], math.Float64bits(clock))
			copy(pcm[64:], data[24:])
			sourceWriters[d].queueAudio(pcm, 64)
		}
	}()
	// Close the mic PC promptly on expiry, target change or server-side stop, even
	// when the browser does not send its stop control.
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-l.done:
				coachMu.Lock()
				closeCoachLocked()
				coachMu.Unlock()
				return
			case <-ticker.C:
				coachMu.Lock()
				if coachPC != nil && !listenerRTCCoachValid(l, coachGeneration) {
					closeCoachLocked()
				}
				coachMu.Unlock()
			}
		}
	}()
	return result, cleanup, nil
}

func listenerRTCCoachValid(l *callListener, generation uint32) bool {
	select {
	case <-l.done:
		return false
	default:
	}
	return l.coaching.grant.Enabled && l.coaching.talking.Load() && l.coaching.generation.Load() == generation && time.Now().UnixMilli() < l.coaching.deadline.Load() && time.Now().Unix() < l.coaching.expires.Load()
}

func listenerRTCSDPDirection(text, direction string, count int) bool {
	var description sdp.SessionDescription
	if description.Unmarshal([]byte(text)) != nil || len(description.MediaDescriptions) != count {
		return false
	}
	for _, media := range description.MediaDescriptions {
		if media.MediaName.Media != "audio" || media.MediaName.Port.Value == 0 {
			return false
		}
		actual := "sendrecv"
		for _, attr := range media.Attributes {
			switch attr.Key {
			case "sendonly", "recvonly", "sendrecv", "inactive":
				actual = attr.Key
			}
		}
		if actual != direction {
			return false
		}
	}
	return true
}
