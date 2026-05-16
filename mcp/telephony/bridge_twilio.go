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
	"net/http"
	"net/url"
	"strings"

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

func (a *App) handleTwilioMediaStream(w http.ResponseWriter, r *http.Request) {
	callID := strings.TrimPrefix(r.URL.Path, "/media/twilio/")
	callID = strings.TrimSuffix(callID, "/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if row.AudioBridgeURL == "" {
		http.Error(w, "no audio bridge for this call", http.StatusGone)
		return
	}

	tw, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		globalCtx.Logger().Warn("twilio ws upgrade", "err", err, "call", callID)
		return
	}
	defer tw.Close()

	// Open core's audio bridge WS using the URL stamped on the call row.
	coreURL, err := url.Parse(row.AudioBridgeURL)
	if err != nil {
		globalCtx.Logger().Warn("parse audio bridge url", "err", err, "url", row.AudioBridgeURL)
		return
	}
	dialer := ws.Dialer{}
	core, _, _, err := dialer.Dial(r.Context(), coreURL.String())
	if err != nil {
		globalCtx.Logger().Warn("dial core audio bridge", "err", err, "url", coreURL.String())
		return
	}
	defer core.Close()

	globalCtx.Logger().Info("bridge up", "call", callID, "thread", row.ThreadID)
	_ = a.db().updateStatus(callID, "in-progress", "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// streamSid is learned from Twilio's "start" frame; until we have
	// it we can't send outbound media frames (Twilio rejects them).
	// Guarded by a buffered channel so the core→twilio goroutine can
	// wait for it without racing.
	streamSidCh := make(chan string, 1)
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
			data, op, err := wsutil.ReadServerData(tw)
			if err != nil {
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
				pcm24 := upsample8to24(pcm8)
				if err := wsutil.WriteClientBinary(core, pcm16ToBytes(pcm24)); err != nil {
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
		if op != ws.OpBinary || len(data) == 0 {
			continue
		}
		pcm24 := bytesToPCM16(data)
		pcm8 := downsample24to8(pcm24)
		mu := pcm16ToUlaw(pcm8)

		out := twilioOutbound{Event: "media", StreamSID: streamSID}
		out.Media.Payload = base64.StdEncoding.EncodeToString(mu)
		buf, _ := json.Marshal(out)
		if err := wsutil.WriteServerText(tw, buf); err != nil {
			return
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
// Crude nearest-neighbour resampling. Good enough for v1 — voice
// quality is bounded by μ-law on the Twilio side anyway, and OpenAI's
// realtime model is tolerant of mild distortion. A future revision
// should use a proper polyphase filter (linear interpolation at
// minimum) for better intelligibility; the codec layer is isolated
// enough that the swap is a one-file change.

func upsample8to24(pcm8 []int16) []int16 {
	out := make([]int16, len(pcm8)*3)
	for i, s := range pcm8 {
		out[i*3] = s
		out[i*3+1] = s
		out[i*3+2] = s
	}
	return out
}

func downsample24to8(pcm24 []int16) []int16 {
	out := make([]int16, (len(pcm24)+2)/3)
	for i := range out {
		out[i] = pcm24[i*3]
	}
	return out
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
