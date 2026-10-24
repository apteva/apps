package main

// Twilio Media Streams bridge.
//
// Twilio opens a WebSocket to /media/twilio/{call_id} once the call
// is connected. The protocol is JSON text frames with one of:
//
//   {"event":"connected","protocol":"Call","version":"1.0.0"}
//   {"event":"start","start":{...},"streamSid":"MZ..."}
//   {"event":"media","media":{"track":"inbound","chunk":"...",
//                             "timestamp":"...","payload":"<base64 μ-law>"},
//                    "streamSid":"..."}
//   {"event":"stop","stop":{"accountSid":"...","callSid":"..."},
//                   "streamSid":"..."}
//
// Audio frames are base64-encoded μ-law at 8 kHz, ~20ms (160 bytes
// raw μ-law = 320 PCM16 samples at 16-bit → 640 bytes PCM16 @ 8 kHz).
//
// This bridge:
//   1. Reads the "start" frame to learn streamSid + callSid.
//   2. Looks up the call's audio_bridge_url from the app DB.
//   3. Opens a binary WebSocket to core's audio bridge.
//   4. Two goroutines:
//        Twilio → core: base64 decode → μ-law → PCM16@8kHz → resample
//                       to PCM16@24kHz → ws.WriteBinary.
//        core → Twilio: ws.ReadBinary PCM16@24kHz → resample to 8kHz →
//                       PCM16 → μ-law → base64 encode → JSON wrap →
//                       ws.WriteText.
//   5. Either side closing tears down both — the realtime thread on
//      core's side will be killed by the status callback handler when
//      Twilio reports the call ended.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gobwas/ws"
)

// twilioFrame is the wire envelope for media frames coming from
// Twilio. Only the fields we actually act on are typed; the rest
// (event metadata, sequence numbers) is ignored.
type twilioFrame struct {
	Event          string `json:"event"`
	StreamSID      string `json:"streamSid,omitempty"`
	SequenceNumber string `json:"sequenceNumber,omitempty"`
	Start          *struct {
		CallSID     string `json:"callSid"`
		MediaFormat struct {
			Encoding   string `json:"encoding"`
			SampleRate int    `json:"sampleRate"`
			Channels   int    `json:"channels"`
		} `json:"mediaFormat"`
	} `json:"start,omitempty"`
	Media *struct {
		Payload   string `json:"payload"`
		Timestamp string `json:"timestamp,omitempty"`
		Chunk     string `json:"chunk,omitempty"`
		Track     string `json:"track,omitempty"`
	} `json:"media,omitempty"`
	DTMF *struct {
		Digit string `json:"digit"`
	} `json:"dtmf,omitempty"`
	Mark *struct {
		Name string `json:"name"`
	} `json:"mark,omitempty"`
}

// twilioOutbound is the shape we send back to Twilio for outbound
// audio. The streamSid must match what Twilio sent in the "start"
// frame.
type twilioOutbound struct {
	Event     string `json:"event"`
	StreamSID string `json:"streamSid"`
	Media     struct {
		Payload string `json:"payload"`
	} `json:"media"`
}

type twilioPlaybackProgress struct {
	Silent     bool
	ItemID     string
	AudioEndMS int
	Generation int64
}

const twilioMediaPacketSamples = 160 // 20ms at Twilio's 8kHz media rate.

type twilioAudioPacket struct {
	Encoded    []byte
	SourceAt   time.Time
	PCM        []int16
	AudioEndMS int
}

func twilioAudioPackets(pcm8 []int16, pcm24Samples int, frame realtimeBridgeControl) []twilioAudioPacket {
	if len(pcm8) == 0 {
		return nil
	}
	packetCount := (len(pcm8) + twilioMediaPacketSamples - 1) / twilioMediaPacketSamples
	packets := make([]twilioAudioPacket, 0, packetCount)
	durationMS := 0
	startMS := frame.AudioEndMS
	if frame.AudioEndMS > 0 && pcm24Samples > 0 {
		durationMS = (pcm24Samples*1000 + 12000) / 24000
		startMS = max(0, frame.AudioEndMS-durationMS)
	}
	for start := 0; start < len(pcm8); start += twilioMediaPacketSamples {
		end := min(len(pcm8), start+twilioMediaPacketSamples)
		packetEndMS := 0
		if frame.AudioEndMS > 0 {
			packetEndMS = startMS
			if durationMS > 0 {
				packetEndMS += (end*durationMS + len(pcm8)/2) / len(pcm8)
			}
			if end == len(pcm8) || packetEndMS > frame.AudioEndMS {
				packetEndMS = frame.AudioEndMS
			}
		}
		packets = append(packets, twilioAudioPacket{PCM: pcm8[start:end], AudioEndMS: packetEndMS})
	}
	return packets
}

type twilioPlaybackTracker struct {
	mu            sync.Mutex
	next          uint64
	pending       map[string]twilioPlaybackProgress
	pendingSpeech int
	evicted       uint64
}

func newTwilioPlaybackTracker() *twilioPlaybackTracker {
	return &twilioPlaybackTracker{pending: make(map[string]twilioPlaybackProgress)}
}

func (t *twilioPlaybackTracker) add(itemID string, audioEndMS int) string {
	return t.addGeneration(itemID, 0, audioEndMS)
}
func (t *twilioPlaybackTracker) addGeneration(itemID string, generation int64, audioEndMS int) string {
	return t.addPacket(itemID, generation, audioEndMS, false)
}

func (t *twilioPlaybackTracker) addPacket(itemID string, generation int64, audioEndMS int, silent bool) string {
	if itemID == "" || audioEndMS <= 0 {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) >= 1024 {
		for name, old := range t.pending {
			if old.ItemID == itemID && old.Generation == generation {
				delete(t.pending, name)
				if !old.Silent {
					t.pendingSpeech--
				}
				t.evicted++
			}
		}
		if len(t.pending) >= 1024 {
			return ""
		}
	}
	t.next++
	name := "apt-" + strconv.FormatUint(t.next, 10)
	t.pending[name] = twilioPlaybackProgress{Generation: generation, ItemID: itemID, AudioEndMS: audioEndMS, Silent: silent}
	if !silent {
		t.pendingSpeech++
	}
	return name
}

func (t *twilioPlaybackTracker) acknowledge(name string) (twilioPlaybackProgress, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	progress, ok := t.pending[name]
	delete(t.pending, name)
	if ok && !progress.Silent {
		t.pendingSpeech--
	}
	return progress, ok
}

func (t *twilioPlaybackTracker) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.pending)
	t.pendingSpeech = 0
}

func (t *twilioPlaybackTracker) hasPendingSpeech() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingSpeech > 0
}

func (t *twilioPlaybackTracker) hasPending() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending) > 0
}

func realtimeInterruptControl(data []byte) bool {
	control, ok := parseRealtimeBridgeControl(data)
	return ok && control.Type == "interrupt"
}

type realtimeBridgeControl struct {
	Generation    int64  `json:"session_generation,omitempty"`
	Silence       bool   `json:"silence,omitempty"`
	RequestedAtMS int64  `json:"requested_at_ms,omitempty"`
	Type          string `json:"type"`
	ResponseID    string `json:"response_id,omitempty"`
	ItemID        string `json:"item_id,omitempty"`
	AudioEndMS    int    `json:"audio_end_ms,omitempty"`
	Source        string `json:"source,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func parseRealtimeBridgeControl(data []byte) (realtimeBridgeControl, bool) {
	var control realtimeBridgeControl
	err := json.Unmarshal(data, &control)
	return control, err == nil && control.Type != ""
}

func (a *App) handleTwilioMediaStream(w http.ResponseWriter, r *http.Request) {
	callID := callIDFromMediaPath(r.URL.Path, "/media/twilio/")
	if callID == "" {
		a.rejectTwilioHandshake(w, callID, "missing_call_id", "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		a.rejectTwilioHandshake(w, callID, "unknown_call", "unknown call_id", http.StatusNotFound)
		return
	}
	if err := a.authorizeCallRequest(r, row); err != nil {
		a.rejectTwilioHandshake(w, callID, "invalid_callback_auth", "forbidden", http.StatusForbidden)
		return
	}
	proxyID := authenticatedCarrierHeaders(w, r, row)
	if row.CarrierSlug != "twilio" || row.AudioBridgeURL == "" || row.AudioBridgeURL == "pending" || isTerminalStatus(row.Status) {
		a.recordCarrierHandshakeRejection(row, proxyID, http.StatusGone, "bridge_unavailable")
		a.rejectTwilioHandshake(w, callID, "bridge_unavailable_or_call_terminal", "no audio bridge for this call", http.StatusGone)
		return
	}
	bridge, err := a.claimCarrierBridge(row, r.Context(), proxyID)
	claimed := bridge != nil
	if err != nil && !errors.Is(err, errCarrierBridgeBusy) {
		a.recordCarrierHandshakeRejection(row, proxyID, 500, "claim_database_error")
		a.rejectTwilioHandshake(w, callID, "claim_database_error", "claim media", http.StatusInternalServerError)
		return
	}
	if !claimed {
		a.recordCarrierHandshakeRejection(row, proxyID, http.StatusConflict, "duplicate_connection")
		a.rejectTwilioHandshake(w, callID, "socket_claim_unavailable", "media bridge already active", http.StatusConflict)
		return
	}
	defer bridge.finish()
	bridgeURL, err := a.mediaBridgeURL(row)
	if err != nil {
		bridge.fail(carrierBridgeEvidence{Kind: "bridge_setup_error", Leg: string(mediaCloseLegLocalError), Detail: transportEvidenceError(err)})
		a.rejectTwilioHandshake(w, callID, "audio_bridge_unavailable", "audio bridge unavailable", http.StatusBadGateway)
		return
	}

	tw, twReadConn, err := upgradeBuffered(w, r)
	if err != nil {
		bridge.fail(carrierBridgeEvidence{Kind: "bridge_setup_error", Leg: string(mediaCloseLegCarrier), Detail: transportEvidenceError(err)})
		globalCtx.Logger().Warn("twilio ws upgrade", "err", err, "call", callID)
		return
	}
	if !bridge.bind(tw) {
		return
	}
	closeState := bridge.closeState
	twWriter := newWebSocketWriterPump(tw, ws.StateServerSide)
	bridge.watch(twWriter)
	twCloser := newGracefulWebSocket(tw, twWriter)
	defer func() {
		code, reason := closeState.Details()
		twCloser.Close(code, reason)
	}()

	// Open core's audio bridge WS using the URL stamped on the call row.
	coreURL, err := url.Parse(bridgeURL)
	if err != nil {
		closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "invalid realtime bridge URL")
		bridge.fail(carrierBridgeEvidence{Kind: "bridge_setup_error", Leg: string(mediaCloseLegLocalError), Detail: transportEvidenceError(err)})
		globalCtx.Logger().Warn("parse audio bridge url", "err", err, "url", redactURL(row.AudioBridgeURL))
		return
	}
	dialer, response := a.agentBridgeDialer(row)
	core, buffered, _, err := dialer.Dial(bridge.ctx, coreURL.String())
	if err != nil {
		closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "realtime bridge rejected")
		bridge.fail(carrierBridgeEvidence{Kind: "bridge_setup_error", Leg: string(mediaCloseLegCore), Detail: transportEvidenceError(err)})
		globalCtx.Logger().Warn("dial core audio bridge", "err", err, "url", redactURL(coreURL.String()))
		return
	}
	if buffered != nil {
		core = hijackedConn{Conn: core, reader: buffered}
	}
	if !bridge.bind(core) {
		return
	}
	agentSession, err := a.newAgentAudioSession(row, response)
	if err != nil {
		_ = core.Close()
		return
	}
	core = a.resilientAgentBridge(bridge.ctx, row, core, response, agentSession)
	if _, ok := core.(*agentCoreLink); ok {
		bridge.coreManaged.Store(true)
	}
	if !bridge.bind(core) {
		return
	}
	coreWriter := newWebSocketWriterPump(core, ws.StateClientSide)
	agentSession.configureWriter(coreWriter)
	coreWriter.audioRate.Store(int64(agentSession.byteRate()))
	agentSession.configureWriter(twWriter)
	bridge.trackWriter(mediaCloseLegCore, coreWriter)
	coreCloser := newGracefulWebSocket(core, coreWriter)
	defer func() {
		code, reason := closeState.Details()
		coreCloser.Close(code, reason)
	}()

	globalCtx.Logger().Info("bridge up", "call", callID, "thread", row.ThreadID)

	ctx, cancel := bridge.ctx, bridge.cancel
	defer cancel()
	go func() {
		select {
		case <-r.Context().Done():
			closeState.SetLeg(mediaCloseLegRequest, ws.StatusGoingAway, "media request context canceled")
			bridge.cancelWithCause("request_context_canceled", mediaCloseLegRequest, 1001, "media request context canceled", "")
		case <-ctx.Done():
		}
	}()
	go func() {
		<-ctx.Done()
		code, reason := closeState.Details()
		twCloser.Close(code, reason)
		coreCloser.Close(code, reason)
	}()

	// streamSid is learned from Twilio's "start" frame; until we have
	// it we can't send outbound media frames (Twilio rejects them).
	// Guarded by a buffered channel so the core→twilio goroutine can
	// wait for it without racing.
	streamSidCh := make(chan string, 1)
	inputResampler := newPCMResampler(8000, 24000)
	agentInput := newAgentMediaConverter(agentSession, 8000, true)
	agentOutput := newAgentMediaConverter(agentSession, 8000, false)
	playback := newTwilioPlaybackTracker()
	tap := a.listeners.openBridge(callID)
	defer a.listeners.closeBridge(callID, tap)
	audioFrontend := newCarrierAudioFrontend(8000)
	defer a.trackAgentAudio(row.ID, agentSession)()
	agentSession.configureFrontend(audioFrontend)
	inputSequences := &audioSequenceTracker{}
	var humanHub *softphoneHub
	if row.PeerKind == peerKindHuman {
		humanHub = a.softphones.hubFor(callID)
		humanHub.setCarrierForward(coreWriter)
		humanHub.reception.begin(true)
		defer humanHub.finishCarrierForward(coreWriter)
	}
	defer func() {
		select {
		case <-streamSidCh:
		default:
			close(streamSidCh)
		}
	}()

	// Twilio → core (μ-law → PCM16@24kHz).
	go func() {
		defer cancel()
		for {
			data, op, err := readWebSocketData(twReadConn, ws.StateServerSide, twWriter, bridge.received)
			if err != nil {
				bridge.socketError(mediaCloseLegCarrier, "read", err)
				code, reason := websocketCloseDetails(err)
				closeState.SetLeg(mediaCloseLegCarrier, code, reason)
				return
			}
			if ctx.Err() != nil {
				return
			}
			if len(data) > maxCarrierFrameBytes {
				bridge.recordLocalCancellation("protocol_rejection", mediaCloseLegCarrier, 1009, "media frame too large", "")
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusMessageTooBig, "Twilio media frame too large")
				return
			}
			if op != ws.OpText || len(data) == 0 {
				continue
			}
			readAt, receiptClock := time.Now(), mediaClockMS()
			if humanHub != nil {
				humanHub.timeline.observe("carrier_socket_read", len(data), time.Time{}, "", "", bridge.generation)
			}
			var f twilioFrame
			if err := json.Unmarshal(data, &f); err != nil {
				bridge.arrival.decodeErrors.Add(1)
				continue
			}
			if humanHub != nil {
				humanHub.timeline.observe("carrier_json_decoded", len(data), readAt, "", "", bridge.generation)
			}

			if sequence, err := strconv.ParseUint(f.SequenceNumber, 10, 64); err == nil {
				inputSequences.observe(sequence, "carrier_to_operator")
			}
			switch f.Event {
			case "start":
				if f.Start == nil || f.Start.CallSID == "" {
					closeState.SetLeg(mediaCloseLegCarrier, ws.StatusProtocolError, "Twilio start frame missing call SID")
					return
				}
				if row.CarrierSID != "" && row.CarrierSID != f.Start.CallSID {
					bridge.recordLocalCancellation("protocol_rejection", mediaCloseLegCarrier, 1008, "call ID mismatch", "")
					closeState.SetLeg(mediaCloseLegCarrier, ws.StatusPolicyViolation, "Twilio call SID mismatch")
					return
				}
				if row.CarrierSID == "" {
					_ = a.db().updateCarrierIdentity(callID, f.Start.CallSID, row.CarrierRequestID)
				}
				if f.StreamSID != "" {
					bridge.setStream(f.StreamSID)
					select {
					case streamSidCh <- f.StreamSID:
					default:
					}
				}
				format := f.Start.MediaFormat
				if (format.Encoding != "" || format.SampleRate != 0 || format.Channels != 0) &&
					(format.Encoding != "audio/x-mulaw" || format.SampleRate != twilioMediaSampleRate || format.Channels != 1) {
					bridge.recordLocalCancellation("format_rejection", mediaCloseLegCarrier, 1003, err.Error(), "")
					closeState.SetLeg(mediaCloseLegCarrier, ws.StatusUnsupportedData, "Twilio negotiated unsupported media format")
					return
				}
			case "media":
				if f.Media != nil && f.Media.Track != "" && f.Media.Track != "inbound" {
					continue
				}
				decodeStarted := time.Now()
				if humanHub != nil {
					humanHub.timeline.observe("carrier_media_read", len(data), time.Time{}, "", f.SequenceNumber, bridge.generation)
				}
				if f.Media == nil || f.Media.Payload == "" {
					continue
				}
				mu, err := base64.StdEncoding.DecodeString(f.Media.Payload)
				if err != nil {
					continue
				}
				if len(mu) == 0 {
					continue
				}
				bridge.media()
				src := carrierSource{}
				mapped, receipt := 0.0, receiptClock
				if humanHub != nil {
					src = sourceMedia(f.StreamSID, f.Media.Timestamp, f.Media.Chunk)
					var drop bool
					mapped, drop = humanHub.reception.observe(src, float64(len(mu))*1000/8000, receipt)
					if drop {
						inputResampler = newPCMResampler(8000, 24000)
						continue
					}
				}
				processed := processCarrierInput(row, audioFrontend, ulawToPCM16(mu))
				observedAt := time.Now()
				agentSession.observeCaller(processed.SpeechActive, observedAt)
				a.observeAICaller(row, processed.SpeechActive, observedAt.UTC(), bridge.generation)
				localSpeechStarted := processed.SpeechStarted && playback.hasPendingSpeech()
				if localSpeechStarted {
					audioFrontend.markLocalSignal()
				}
				pcm24 := inputResampler.Process(processed.PCM)
				if len(pcm24) == 0 {
					continue
				}
				tap.publishPCM(0, pcm24)
				if humanHub != nil {
					humanHub.timeline.observe("carrier_decoded", len(pcm24)*2, decodeStarted, f.Media.Timestamp, f.Media.Chunk, bridge.generation)
					coreWriter.queueAudio(encodeSourceAudio(pcm16ToBytes(pcm24), src, mapped, receipt, humanHub.reception.snapshot(receipt, false).Epoch), sourceAudioHeaderBytes)
				} else {
					err = coreWriter.Write(ws.OpBinary, agentInput.inputWithPCM24(processed.PCM, pcm24, mu, "g711_ulaw"))
				}
				if err == nil && localSpeechStarted {
					control, _ := json.Marshal(realtimeBridgeControl{Type: "input.speech_started"})
					err = coreWriter.Write(ws.OpText, control)
				}
				if err != nil {
					bridge.socketError(mediaCloseLegCore, "write", err)
					closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write caller audio to realtime bridge")
					return
				}
				if localSpeechStarted {
					logLocalBargeIn(globalCtx.Logger(), "twilio", callID, processed)
				}
			case "dtmf":
				if f.DTMF != nil && validDTMFDigits(f.DTMF.Digit) {
					a.observeAICaller(row, true, time.Now().UTC(), bridge.generation)
				}
			case "mark":
				if f.Mark == nil {
					continue
				}
				progress, ok := playback.acknowledge(f.Mark.Name)
				if !ok {
					continue
				}
				err := agentSession.progress(coreWriter, progress, "carrier_mark")
				if err != nil {
					bridge.socketError(mediaCloseLegCore, "write", err)
					closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write playback progress to realtime bridge")
					return
				}
			case "stop":
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusNormalClosure, "Twilio call media ended normally")
				return
			}
		}
	}()

	// core → Twilio (PCM16@24kHz → μ-law). Block on streamSid first.
	var streamSID string
	select {
	case streamSID = <-streamSidCh:
	case <-ctx.Done():
		return
	}

	maxQueuedMS := 0
	pacerPolicy := agentSession.policy()
	pacerPolicy.onSent = func(silent bool) {
		if !silent {
			a.observeAIOutput(row, time.Now().UTC(), bridge.generation)
		}
	}
	pacerMode := "buffered"
	if row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		pacerPolicy = liveHumanCarrierPacerPolicy()
		pacerMode = "live_human"
	}
	droppedStaleMS := 0
	observeSent := tap.jsonOutputObserver(carrierCodecPCMU8)
	pacer := newTwilioAudioPacerWithPolicy(ctx, streamSID, playback, pacerPolicy, func(payload []byte) error {
		started := time.Now()
		err := twWriter.write(ws.OpText, payload, carrierMediaWriteTimeout(row))
		if err == nil {
			observeSent(payload)
		}
		if humanHub != nil {
			stage := "carrier_send"
			if err != nil {
				stage = "carrier_send_error"
			}
			humanHub.timeline.observe(stage, len(payload), started, "", "", bridge.generation)
		}
		return err
	}, func(err error) {
		globalCtx.Logger().Warn("twilio media writer failed", "call", callID, "err", err)
		bridge.socketError(mediaCloseLegCarrier, "write", err)
		closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "Twilio media writer failed")
		cancel()
	})
	a.bindAIPlayback(row, bridge.generation, func() bool { return pacer.pendingSpeechSamples.Load() > 0 || playback.hasPendingSpeech() })
	if humanHub != nil {
		humanHub.setPacerStats(&pacer.diagnostics)
	}
	defer func() {
		logAudioFrontendDiagnostics(globalCtx.Logger(), audioFrontend, row, "twilio", carrierCodecPCMU8, maxQueuedMS, droppedStaleMS)
		var preAnswerDroppedMS int64
		sequenceGaps, dropEvents := inputSequences.snapshot()
		carrierGaps := sequenceGaps
		captureGapCount := 0
		dropEvents = append(dropEvents, pacer.dropEvents()...)
		if hub := a.softphones.lookup(callID); hub != nil {
			preAnswerDroppedMS = hub.preAnswerDroppedMS()
			captureGaps, captureDrops := hub.captureDiagnostics()
			sequenceGaps += captureGaps
			captureGapCount = captureGaps
			dropEvents = append(dropEvents, captureDrops...)
		}
		_ = a.db().updateCarrierAudioDiagnostics(callID, carrierAudioDiagnostics{
			AgentAudio: agentSession.snapshot(), OperatorInterrupts: audioFrontend.snapshot().OperatorInterrupts, LocalInterrupts: audioFrontend.snapshot().LocalInterrupts, ProviderCoreInterrupts: audioFrontend.snapshot().ProviderCoreInterrupts,
			InputAudio: audioFrontend.transportSnapshot(),
			Provider:   "twilio", Codec: carrierCodecPCMU8, SampleRate: twilioMediaSampleRate,
			SendAheadMS: pacerPolicy.bufferMS, PacerMode: pacerMode, MaxQueuedMS: maxQueuedMS, DroppedStaleMS: max(droppedStaleMS, int(pacer.diagnostics.snapshot().DroppedMS)),
			PreAnswerMicrophoneDroppedMS: preAnswerDroppedMS,
			CarrierSequenceGaps:          carrierGaps, CaptureSequenceGaps: captureGapCount,
			SequenceGaps: sequenceGaps, DropEvents: dropEvents,
		}, bridge.generation)
	}()

	if err := agentSession.ready(coreWriter, "carrier_mark"); err != nil {
		return
	}
	var nextFrame realtimeBridgeControl
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		data, op, err := readWebSocketData(core, ws.StateClientSide, coreWriter, bridge.receivedFor(mediaCloseLegCore))
		if err != nil {
			if a.handleAgentCompletionClose(row, err) {
				closeState.SetLeg(mediaCloseLegCore, ws.StatusNormalClosure, "agent conversation completed")
				return
			}
			bridge.socketError(mediaCloseLegCore, "read", err)
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
				a.observeAICaller(row, true, time.Now().UTC(), bridge.generation)
				if matchesAgentPlayback(nextFrame.ItemID, nextFrame.Generation, control) {
					nextFrame = realtimeBridgeControl{}
				}
				interruptSource := audioFrontend.markInterrupt(control.Source)
				agentSession.cancel(control.ItemID, control.Generation)
				receipt, err := pacer.cancelPlayback(ctx, control)
				clearedMS := receipt.LocalMS
				if err == nil {
					err = agentSession.cleared(coreWriter, control, receipt)
				}
				if err != nil {
					closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "clear Twilio playback")
					return
				}
				globalCtx.Logger().Info("twilio playback cleared", "call", callID, "source", interruptSource, "queued_ms", clearedMS)
			}
			continue
		}
		if op != ws.OpBinary || len(data) == 0 {
			continue
		}
		if !agentSession.accept(nextFrame) {
			nextFrame = realtimeBridgeControl{}
			continue
		}
		data, sourceAt := carrierMicrophoneSource(row, data)
		pcm8 := agentOutput.output(data)
		if len(pcm8) == 0 {
			continue
		}
		packets := twilioAudioPackets(pcm8, agentSession.pcm24Samples(len(data)), nextFrame)
		if agentSession.mediaFormat() == "g711_ulaw" {
			for i := range packets {
				start := i * twilioMediaPacketSamples
				packets[i].Encoded = append([]byte(nil), data[start:min(len(data), start+twilioMediaPacketSamples)]...)
			}
		}
		for i := range packets {
			if !sourceAt.IsZero() {
				packets[i].SourceAt = sourceAt.Add(time.Duration(i) * 20 * time.Millisecond)
			}
		}
		frame := nextFrame
		nextFrame = realtimeBridgeControl{}
		var queuedMS, droppedMS int
		err = agentSession.withPlaybackPressure(ctx, frame, func() error {
			var e error
			queuedMS, droppedMS, e = pacer.enqueueWithDiagnostics(ctx, packets, frame)
			return e
		})
		if errors.Is(err, errTwilioPacerOverflow) {
			globalCtx.Logger().Warn("twilio playback queue overflow", "call", callID, "item", frame.ItemID, "max_queued_ms", maxQueuedMS)
			err = agentSession.overflow(coreWriter, frame.ItemID, frame.Generation, "carrier_queue_overflow")
			if err != nil {
				bridge.socketError(mediaCloseLegCore, "write", err)
				closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "report playback overflow to realtime bridge")
				return
			}
			continue
		}
		if err != nil {
			closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "queue Twilio playback")
			return
		}
		if queuedMS > maxQueuedMS {
			maxQueuedMS = queuedMS
		}
		if droppedMS > droppedStaleMS {
			droppedStaleMS = droppedMS
		}
	}
}

// ─── codec: μ-law ↔ PCM16 ──────────────────────────────────────────

// ulawToPCM16 expands G.711 μ-law to linear PCM16. One-to-one sample
// mapping; output length = input length.
func ulawToPCM16(mu []byte) []int16 {
	out := make([]int16, len(mu))
	for i, b := range mu {
		out[i] = ulawDecodeOne(b)
	}
	return out
}

func pcm16ToUlaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		out[i] = ulawEncodeOne(s)
	}
	return out
}

// ulawDecodeOne expands a single μ-law byte to PCM16. Standard ITU
// G.711 μ-law decoding table inlined.
func ulawDecodeOne(b byte) int16 {
	b = ^b
	sign := int16(b & 0x80)
	exponent := int16((b >> 4) & 0x07)
	mantissa := int16(b & 0x0F)
	sample := ((mantissa << 3) + 0x84) << exponent
	sample -= 0x84
	if sign != 0 {
		sample = -sample
	}
	return sample
}

func ulawEncodeOne(pcm int16) byte {
	const bias = 0x84
	const clip = 32635
	sign := byte(0x00)
	if pcm < 0 {
		pcm = -pcm
		sign = 0x80
	}
	if pcm > clip {
		pcm = clip
	}
	pcm += bias
	exponent := byte(7)
	for mask := int16(0x4000); pcm&mask == 0 && exponent > 0; mask >>= 1 {
		exponent--
	}
	mantissa := byte((pcm >> (exponent + 3)) & 0x0F)
	return ^(sign | (exponent << 4) | mantissa)
}

// ─── resampling: 8 kHz ↔ 24 kHz ────────────────────────────────────
//
// The streaming paths retain a pcmResampler between frames. These
// helpers remain for codec tests and one-shot conversions.

func upsample8to24(pcm8 []int16) []int16 {
	return resamplePCM(pcm8, 8000, 24000)
}

func downsample24to8(pcm24 []int16) []int16 {
	return resamplePCM(pcm24, 24000, 8000)
}

// ─── PCM16 ↔ byte slice (little-endian) ────────────────────────────

func pcm16ToBytes(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}

func bytesToPCM16(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

// Log only stable reason codes and call IDs; media URLs contain callback secrets.
func (a *App) rejectTwilioHandshake(w http.ResponseWriter, callID, reason, message string, status int) {
	if globalCtx != nil {
		globalCtx.Logger().Warn("twilio websocket handshake rejected", "call", callID, "reason", reason, "http_status", status)
	}
	http.Error(w, message, status)
}

func (t *twilioPlaybackTracker) affectedPlaybacks(item string, generation int64) (bool, []agentCollateralPlayback) {
	t.mu.Lock()
	defer t.mu.Unlock()
	target := false
	others := map[agentPlaybackKey]bool{}
	for _, p := range t.pending {
		if item == "" || p.ItemID == item && (generation == 0 || p.Generation == generation) {
			target = true
		} else {
			others[agentPlaybackKey{p.Generation, p.ItemID}] = true
		}
	}
	out := []agentCollateralPlayback{}
	for key := range others {
		out = append(out, agentCollateralPlayback{ItemID: key.Item, Generation: key.Generation})
	}
	return target, out
}
