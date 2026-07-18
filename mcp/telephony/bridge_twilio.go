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
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
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
		http.Error(w, "audio bridge unavailable", http.StatusBadGateway)
		return
	}

	tw, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		globalCtx.Logger().Warn("twilio ws upgrade", "err", err, "call", callID)
		return
	}
	defer tw.Close()
	defer a.finishMediaBridge(callID, "media-disconnected")

	// Open core's audio bridge WS using the URL stamped on the call row.
	coreURL, err := url.Parse(bridgeURL)
	if err != nil {
		globalCtx.Logger().Warn("parse audio bridge url", "err", err, "url", redactURL(row.AudioBridgeURL))
		return
	}
	dialer := ws.Dialer{}
	core, _, _, err := dialer.Dial(r.Context(), coreURL.String())
	if err != nil {
		globalCtx.Logger().Warn("dial core audio bridge", "err", err, "url", redactURL(coreURL.String()))
		return
	}
	defer core.Close()

	globalCtx.Logger().Info("bridge up", "call", callID, "thread", row.ThreadID)
	_ = a.db().updateStatus(callID, "in-progress", "")
	_ = a.db().clearStateExpiry(callID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = tw.Close()
		_ = core.Close()
	}()

	// streamSid is learned from Twilio's "start" frame; until we have
	// it we can't send outbound media frames (Twilio rejects them).
	// Guarded by a buffered channel so the core→twilio goroutine can
	// wait for it without racing.
	streamSidCh := make(chan string, 1)
	inputResampler := newPCMResampler(8000, 24000)
	outputResampler := newPCMResampler(24000, 8000)
	playback := newTwilioPlaybackTracker()
	speechDetector := newPCMSpeechStartDetector()
	var coreWriteMu sync.Mutex
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
			data, op, err := wsutil.ReadClientData(tw)
			if err != nil {
				return
			}
			if len(data) > maxCarrierFrameBytes {
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
					return
				}
				if row.CarrierSID != "" && row.CarrierSID != f.Start.CallSID {
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
				pcm8 := ulawToPCM16(mu)
				speechStarted := speechDetector.observe(pcm8)
				localSpeechStarted := speechStarted && playback.hasPending()
				pcm24 := inputResampler.Process(pcm8)
				if len(pcm24) == 0 {
					continue
				}
				coreWriteMu.Lock()
				err = wsutil.WriteClientBinary(core, pcm16ToBytes(pcm24))
				if err == nil && localSpeechStarted {
					control, _ := json.Marshal(realtimeBridgeControl{Type: "input.speech_started"})
					err = wsutil.WriteClientText(core, control)
				}
				coreWriteMu.Unlock()
				if err != nil {
					return
				}
				if localSpeechStarted {
					globalCtx.Logger().Info("local barge-in detected", "provider", "twilio", "call", callID)
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
				coreWriteMu.Lock()
				err := wsutil.WriteClientText(core, control)
				coreWriteMu.Unlock()
				if err != nil {
					return
				}
			case "stop":
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
		if err := tw.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		return wsutil.WriteServerText(tw, payload)
	}, func(err error) {
		globalCtx.Logger().Warn("twilio media writer failed", "call", callID, "err", err)
		cancel()
	})
	defer func() {
		globalCtx.Logger().Info("twilio media bridge metrics", "call", callID, "max_queued_ms", maxQueuedMS)
	}()

	var nextFrame realtimeBridgeControl
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		data, op, err := wsutil.ReadServerData(core)
		if err != nil {
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
				clearedMS, err := pacer.clear(ctx)
				if err != nil {
					return
				}
				globalCtx.Logger().Info("twilio playback cleared", "call", callID, "queued_ms", clearedMS)
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
			coreWriteMu.Lock()
			err = wsutil.WriteClientText(core, control)
			coreWriteMu.Unlock()
			if err != nil {
				return
			}
			continue
		}
		if err != nil {
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
