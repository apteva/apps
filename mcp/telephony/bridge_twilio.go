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
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/gobwas/ws"
)

// twilioFrame is the wire envelope for media frames coming from
// Twilio. Only the fields we actually act on are typed; the rest
// (event metadata, sequence numbers) is ignored.
type twilioFrame struct {
	Event     string `json:"event"`
	StreamSID string `json:"streamSid,omitempty"`
	Start     *struct {
		CallSID string `json:"callSid"`
	} `json:"start,omitempty"`
	Media *struct {
		Payload string `json:"payload"`
	} `json:"media,omitempty"`
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
	ItemID     string
	AudioEndMS int
}

const twilioMediaPacketSamples = 160 // 20ms at Twilio's 8kHz media rate.

type twilioAudioPacket struct {
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
	mu      sync.Mutex
	next    uint64
	pending map[string]twilioPlaybackProgress
}

func newTwilioPlaybackTracker() *twilioPlaybackTracker {
	return &twilioPlaybackTracker{pending: make(map[string]twilioPlaybackProgress)}
}

func (t *twilioPlaybackTracker) add(itemID string, audioEndMS int) string {
	if itemID == "" || audioEndMS <= 0 {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	name := "apt-" + strconv.FormatUint(t.next, 10)
	t.pending[name] = twilioPlaybackProgress{ItemID: itemID, AudioEndMS: audioEndMS}
	return name
}

func (t *twilioPlaybackTracker) acknowledge(name string) (twilioPlaybackProgress, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	progress, ok := t.pending[name]
	delete(t.pending, name)
	return progress, ok
}

func (t *twilioPlaybackTracker) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.pending)
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
	Type       string `json:"type"`
	ResponseID string `json:"response_id,omitempty"`
	ItemID     string `json:"item_id,omitempty"`
	AudioEndMS int    `json:"audio_end_ms,omitempty"`
	Source     string `json:"source,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func parseRealtimeBridgeControl(data []byte) (realtimeBridgeControl, bool) {
	var control realtimeBridgeControl
	err := json.Unmarshal(data, &control)
	return control, err == nil && control.Type != ""
}

func (a *App) handleTwilioMediaStream(w http.ResponseWriter, r *http.Request) {
	callID := callIDFromMediaPath(r.URL.Path, "/media/twilio/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if err := a.authorizeCallRequest(r, row); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if row.CarrierSlug != "twilio" || row.AudioBridgeURL == "" || row.AudioBridgeURL == "pending" || isTerminalStatus(row.Status) {
		http.Error(w, "no audio bridge for this call", http.StatusGone)
		return
	}
	claimed, err := a.db().claimMedia(callID)
	if err != nil {
		http.Error(w, "claim media", http.StatusInternalServerError)
		return
	}
	if !claimed {
		http.Error(w, "media bridge already active", http.StatusConflict)
		return
	}
	defer a.db().releaseMedia(callID)
	bridgeURL, err := a.mediaBridgeURL(row)
	if err != nil {
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "audio bridge unavailable", string(mediaCloseLegLocalError))
		http.Error(w, "audio bridge unavailable", http.StatusBadGateway)
		return
	}

	tw, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "Twilio websocket upgrade failed", string(mediaCloseLegCarrier))
		globalCtx.Logger().Warn("twilio ws upgrade", "err", err, "call", callID)
		return
	}
	closeState := &websocketCloseState{}
	twWriter := newWebSocketWriterPump(tw, ws.StateServerSide)
	twCloser := newGracefulWebSocket(tw, twWriter)
	defer func() {
		code, reason := closeState.Details()
		twCloser.Close(code, reason)
	}()

	// Open core's audio bridge WS using the URL stamped on the call row.
	coreURL, err := url.Parse(bridgeURL)
	if err != nil {
		closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "invalid realtime bridge URL")
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "invalid realtime bridge URL", string(mediaCloseLegLocalError))
		globalCtx.Logger().Warn("parse audio bridge url", "err", err, "url", redactURL(row.AudioBridgeURL))
		return
	}
	dialer := ws.Dialer{}
	core, _, _, err := dialer.Dial(r.Context(), coreURL.String())
	if err != nil {
		closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "realtime bridge rejected")
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "core audio bridge rejected", string(mediaCloseLegCore))
		globalCtx.Logger().Warn("dial core audio bridge", "err", err, "url", redactURL(coreURL.String()))
		return
	}
	coreWriter := newWebSocketWriterPump(core, ws.StateClientSide)
	coreCloser := newGracefulWebSocket(core, coreWriter)
	defer func() {
		code, reason := closeState.Details()
		coreCloser.Close(code, reason)
	}()

	globalCtx.Logger().Info("bridge up", "call", callID, "thread", row.ThreadID)
	_ = a.db().updateMediaStatus(callID, "connected", "", 0, "")
	_ = a.db().clearStateExpiry(callID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() {
		leg, code, reason := closeState.Cause()
		a.finishMediaBridge(callID, leg, code, reason)
	}()
	go func() {
		select {
		case <-r.Context().Done():
			closeState.SetLeg(mediaCloseLegRequest, ws.StatusGoingAway, "media request context canceled")
			cancel()
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
	outputResampler := newPCMResampler(24000, 8000)
	playback := newTwilioPlaybackTracker()
	audioFrontend := newCarrierAudioFrontend(8000)
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
			data, op, err := readWebSocketData(tw, ws.StateServerSide, twWriter)
			if err != nil {
				code, reason := websocketCloseDetails(err)
				closeState.SetLeg(mediaCloseLegCarrier, code, reason)
				return
			}
			if len(data) > maxCarrierFrameBytes {
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusMessageTooBig, "Twilio media frame too large")
				return
			}
			if op != ws.OpText || len(data) == 0 {
				continue
			}
			var f twilioFrame
			if err := json.Unmarshal(data, &f); err != nil {
				continue
			}
			switch f.Event {
			case "start":
				if f.Start == nil || f.Start.CallSID == "" {
					closeState.SetLeg(mediaCloseLegCarrier, ws.StatusProtocolError, "Twilio start frame missing call SID")
					return
				}
				if row.CarrierSID != "" && row.CarrierSID != f.Start.CallSID {
					closeState.SetLeg(mediaCloseLegCarrier, ws.StatusPolicyViolation, "Twilio call SID mismatch")
					return
				}
				if row.CarrierSID == "" {
					_ = a.db().updateCarrierIdentity(callID, f.Start.CallSID, row.CarrierRequestID)
				}
				if f.StreamSID != "" {
					select {
					case streamSidCh <- f.StreamSID:
					default:
					}
				}
			case "media":
				if f.Media == nil || f.Media.Payload == "" {
					continue
				}
				mu, err := base64.StdEncoding.DecodeString(f.Media.Payload)
				if err != nil {
					continue
				}
				processed := audioFrontend.process(ulawToPCM16(mu))
				localSpeechStarted := processed.SpeechStarted && playback.hasPending()
				if localSpeechStarted {
					audioFrontend.markLocalSignal()
				}
				pcm24 := inputResampler.Process(processed.PCM)
				if len(pcm24) == 0 {
					continue
				}
				err = coreWriter.Write(ws.OpBinary, pcm16ToBytes(pcm24))
				if err == nil && localSpeechStarted {
					control, _ := json.Marshal(realtimeBridgeControl{Type: "input.speech_started"})
					err = coreWriter.Write(ws.OpText, control)
				}
				if err != nil {
					closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write caller audio to realtime bridge")
					return
				}
				if localSpeechStarted {
					logLocalBargeIn(globalCtx.Logger(), "twilio", callID, processed)
				}
			case "mark":
				if f.Mark == nil {
					continue
				}
				progress, ok := playback.acknowledge(f.Mark.Name)
				if !ok {
					continue
				}
				control, _ := json.Marshal(realtimeBridgeControl{
					Type: "playback.progress", ItemID: progress.ItemID, AudioEndMS: progress.AudioEndMS,
				})
				err := coreWriter.Write(ws.OpText, control)
				if err != nil {
					closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write playback progress to realtime bridge")
					return
				}
			case "stop":
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusNormalClosure, "Twilio media stream stopped")
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
	pacer := newTwilioAudioPacer(ctx, streamSID, playback, func(payload []byte) error {
		return twWriter.Write(ws.OpText, payload)
	}, func(err error) {
		globalCtx.Logger().Warn("twilio media writer failed", "call", callID, "err", err)
		closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "Twilio media writer failed")
		cancel()
	})
	defer func() {
		logAudioFrontendDiagnostics(globalCtx.Logger(), audioFrontend, row, "twilio", carrierCodecPCMU8, maxQueuedMS)
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
				interruptSource := audioFrontend.markInterrupt(control.Source)
				clearedMS, err := pacer.clear(ctx)
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
		pcm24 := bytesToPCM16(data)
		pcm8 := outputResampler.Process(pcm24)
		if len(pcm8) == 0 {
			continue
		}
		packets := twilioAudioPackets(pcm8, len(pcm24), nextFrame)
		frame := nextFrame
		nextFrame = realtimeBridgeControl{}
		queuedMS, err := pacer.enqueue(ctx, packets, frame)
		if errors.Is(err, errTwilioPacerOverflow) {
			globalCtx.Logger().Warn("twilio playback queue overflow", "call", callID, "item", frame.ItemID, "max_queued_ms", maxQueuedMS)
			control, _ := json.Marshal(realtimeBridgeControl{Type: "playback.overflow", ItemID: frame.ItemID})
			err = coreWriter.Write(ws.OpText, control)
			if err != nil {
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
