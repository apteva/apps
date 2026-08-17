package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gobwas/ws"
)

const (
	carrierCodecPCMU8  = "pcmu8"
	carrierCodecL16_16 = "l16_16"
	carrierCodecL16_24 = "l16_24"
)

type jsonMediaBridgeConfig struct {
	Provider         string
	PathPrefix       string
	InputCodec       string
	OutputCodec      string
	RequireStreamSID bool
	OutboundShape    string
	PlaybackMarks    bool
}

type carrierMediaFrame struct {
	Event          string `json:"event"`
	StreamSID      string `json:"streamSid,omitempty"`
	StreamID       string `json:"stream_id,omitempty"`
	SequenceNumber any    `json:"sequenceNumber,omitempty"`
	Start          *struct {
		CallSID       string `json:"callSid,omitempty"`
		CallID        string `json:"callId,omitempty"`
		CallUUID      string `json:"callUuid,omitempty"`
		CallControlID string `json:"call_control_id,omitempty"`
		StreamSID     string `json:"streamSid,omitempty"`
		StreamID      string `json:"streamId,omitempty"`
	} `json:"start,omitempty"`
	Media *struct {
		Payload string `json:"payload"`
	} `json:"media,omitempty"`
	Mark *struct {
		Name string `json:"name"`
	} `json:"mark,omitempty"`
}

func (a *App) handleSignalWireMediaStream(w http.ResponseWriter, r *http.Request) {
	a.handleJSONMediaStream(w, r, jsonMediaBridgeConfig{
		Provider:         "signalwire",
		PathPrefix:       "/media/signalwire/",
		InputCodec:       carrierCodecL16_24,
		OutputCodec:      carrierCodecL16_24,
		RequireStreamSID: true,
		OutboundShape:    "signalwire",
	})
}

func (a *App) handleTelnyxMediaStream(w http.ResponseWriter, r *http.Request) {
	a.handleJSONMediaStream(w, r, jsonMediaBridgeConfig{
		Provider: "telnyx", PathPrefix: "/media/telnyx/", InputCodec: carrierCodecPCMU8, OutputCodec: carrierCodecPCMU8,
		RequireStreamSID: true, OutboundShape: "telnyx", PlaybackMarks: true,
	})
}

func (a *App) handlePlivoMediaStream(w http.ResponseWriter, r *http.Request) {
	a.handleJSONMediaStream(w, r, jsonMediaBridgeConfig{
		Provider: "plivo", PathPrefix: "/media/plivo/", InputCodec: carrierCodecPCMU8, OutputCodec: carrierCodecPCMU8,
		RequireStreamSID: true, OutboundShape: "plivo",
	})
}

func (a *App) handleJSONMediaStream(w http.ResponseWriter, r *http.Request, cfg jsonMediaBridgeConfig) {
	callID := callIDFromMediaPath(r.URL.Path, cfg.PathPrefix)
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
	if row.CarrierSlug != cfg.Provider || row.AudioBridgeURL == "" || row.AudioBridgeURL == "pending" || isTerminalStatus(row.Status) {
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

	carrier, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, cfg.Provider+" websocket upgrade failed", string(mediaCloseLegCarrier))
		globalCtx.Logger().Warn("carrier ws upgrade", "provider", cfg.Provider, "err", err, "call", callID)
		return
	}
	closeState := &websocketCloseState{}
	carrierWriter := newWebSocketWriterPump(carrier, ws.StateServerSide)
	carrierCloser := newGracefulWebSocket(carrier, carrierWriter)
	defer func() {
		code, reason := closeState.Details()
		carrierCloser.Close(code, reason)
	}()

	coreURL, err := url.Parse(bridgeURL)
	if err != nil {
		closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "invalid realtime bridge URL")
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "invalid realtime bridge URL", string(mediaCloseLegLocalError))
		globalCtx.Logger().Warn("parse audio bridge url", "provider", cfg.Provider, "err", err, "url", redactURL(row.AudioBridgeURL))
		return
	}
	dialer := ws.Dialer{}
	core, _, _, err := dialer.Dial(r.Context(), coreURL.String())
	if err != nil {
		closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "realtime bridge rejected")
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "core audio bridge rejected", string(mediaCloseLegCore))
		globalCtx.Logger().Warn("dial core audio bridge", "provider", cfg.Provider, "err", err, "url", redactURL(coreURL.String()))
		return
	}
	coreWriter := newWebSocketWriterPump(core, ws.StateClientSide)
	coreCloser := newGracefulWebSocket(core, coreWriter)
	defer func() {
		code, reason := closeState.Details()
		coreCloser.Close(code, reason)
	}()

	globalCtx.Logger().Info("bridge up", "provider", cfg.Provider, "call", callID, "thread", row.ThreadID)
	if err := a.db().updateMediaStatus(callID, "connected", "", 0, ""); err != nil {
		globalCtx.Logger().Warn("mark media connected", "provider", cfg.Provider, "call", callID, "err", err)
	}
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
		carrierCloser.Close(code, reason)
		coreCloser.Close(code, reason)
	}()

	streamSidCh := make(chan string, 1)
	inputResampler := carrierInputResampler(cfg.InputCodec)
	outputResampler := carrierOutputResampler(cfg.OutputCodec)
	playback := newTwilioPlaybackTracker()
	audioFrontend := newCarrierAudioFrontend(carrierCodecSampleRate(cfg.InputCodec))

	go func() {
		defer cancel()
		for {
			data, op, err := readWebSocketData(carrier, ws.StateServerSide, carrierWriter)
			if err != nil {
				code, reason := websocketCloseDetails(err)
				closeState.SetLeg(mediaCloseLegCarrier, code, reason)
				return
			}
			if len(data) > maxCarrierFrameBytes {
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusMessageTooBig, cfg.Provider+" media frame too large")
				return
			}
			if op != ws.OpText || len(data) == 0 {
				continue
			}
			var f carrierMediaFrame
			if err := json.Unmarshal(data, &f); err != nil {
				continue
			}
			switch f.Event {
			case "start":
				if providerCallID := frameCallID(f); providerCallID != "" {
					if row.CarrierSID != "" && row.CarrierSID != providerCallID {
						closeState.SetLeg(mediaCloseLegCarrier, ws.StatusPolicyViolation, cfg.Provider+" call ID mismatch")
						return
					}
					if row.CarrierSID == "" {
						_ = a.db().updateCarrierIdentity(callID, providerCallID, row.CarrierRequestID)
					}
				}
				if sid := frameStreamID(f); sid != "" {
					select {
					case streamSidCh <- sid:
					default:
					}
				}
			case "media":
				if f.Media == nil || f.Media.Payload == "" {
					continue
				}
				pcm, err := decodeCarrierPCM(f.Media.Payload, cfg.InputCodec)
				if err != nil {
					continue
				}
				processed := audioFrontend.process(pcm)
				localSpeechStarted := processed.SpeechStarted && playback.hasPending()
				if localSpeechStarted {
					audioFrontend.markLocalSignal()
				}
				pcm24 := processed.PCM
				if inputResampler != nil {
					pcm24 = inputResampler.Process(processed.PCM)
				}
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
					logLocalBargeIn(globalCtx.Logger(), cfg.Provider, callID, processed)
				}
			case "mark":
				if f.Mark == nil {
					continue
				}
				progress, ok := playback.acknowledge(f.Mark.Name)
				if !ok {
					continue
				}
				control, _ := json.Marshal(realtimeBridgeControl{Type: "playback.progress", ItemID: progress.ItemID, AudioEndMS: progress.AudioEndMS})
				err := coreWriter.Write(ws.OpText, control)
				if err != nil {
					closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write playback progress to realtime bridge")
					return
				}
			case "stop":
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusNormalClosure, cfg.Provider+" media stream stopped")
				return
			}
		}
	}()

	streamSID := ""
	if cfg.RequireStreamSID {
		select {
		case streamSID = <-streamSidCh:
		case <-ctx.Done():
			return
		}
	}
	sampleRate := carrierCodecSampleRate(cfg.OutputCodec)
	packetizer := newCarrierAudioPacketizer(sampleRate)
	maxQueuedMS := 0
	pacer := newJSONCarrierAudioPacer(ctx, sampleRate, cfg.OutputCodec, cfg.OutboundShape, streamSID, cfg.PlaybackMarks,
		playback,
		func(payload []byte) error {
			return carrierWriter.Write(ws.OpText, payload)
		},
		func(progress twilioPlaybackProgress) error {
			control, _ := json.Marshal(realtimeBridgeControl{Type: "playback.progress", ItemID: progress.ItemID, AudioEndMS: progress.AudioEndMS})
			return coreWriter.Write(ws.OpText, control)
		},
		func(err error) {
			globalCtx.Logger().Warn("carrier media writer failed", "provider", cfg.Provider, "call", callID, "err", err)
			closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, cfg.Provider+" media writer failed")
			cancel()
		},
	)
	defer func() {
		logAudioFrontendDiagnostics(globalCtx.Logger(), audioFrontend, row, cfg.Provider, cfg.InputCodec, maxQueuedMS)
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
				packetizer.clear()
				clearedMS, err := pacer.clear(ctx)
				if err != nil {
					closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "clear carrier playback")
					return
				}
				globalCtx.Logger().Info("carrier playback cleared", "provider", cfg.Provider, "call", callID, "source", interruptSource, "queued_ms", clearedMS)
			}
			continue
		}
		if op != ws.OpBinary || len(data) == 0 {
			continue
		}
		pcm24 := bytesToPCM16(data)
		pcmCarrier := pcm24
		if outputResampler != nil {
			pcmCarrier = outputResampler.Process(pcm24)
		}
		if len(pcmCarrier) == 0 {
			continue
		}
		packets := packetizer.add(pcmCarrier, len(pcm24), nextFrame)
		frame := nextFrame
		nextFrame = realtimeBridgeControl{}
		queuedMS, err := pacer.enqueue(ctx, packets)
		if errors.Is(err, errCarrierPacerOverflow) {
			control, _ := json.Marshal(realtimeBridgeControl{Type: "playback.overflow", ItemID: frame.ItemID})
			err = coreWriter.Write(ws.OpText, control)
			if err != nil {
				closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "report playback overflow to realtime bridge")
				return
			}
			continue
		}
		if err != nil {
			closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "queue carrier playback")
			return
		}
		if queuedMS > maxQueuedMS {
			maxQueuedMS = queuedMS
		}
	}
}

func carrierCodecSampleRate(codec string) int {
	switch codec {
	case carrierCodecPCMU8:
		return 8000
	case carrierCodecL16_16:
		return 16000
	case carrierCodecL16_24:
		return 24000
	default:
		return 8000
	}
}

func (a *App) handleVonageMediaStream(w http.ResponseWriter, r *http.Request) {
	callID := callIDFromMediaPath(r.URL.Path, "/media/vonage/")
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
	if row.CarrierSlug != "vonage" || row.AudioBridgeURL == "" || row.AudioBridgeURL == "pending" || isTerminalStatus(row.Status) {
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

	vonage, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "Vonage websocket upgrade failed", string(mediaCloseLegCarrier))
		globalCtx.Logger().Warn("vonage ws upgrade", "err", err, "call", callID)
		return
	}
	closeState := &websocketCloseState{}
	vonageWriter := newWebSocketWriterPump(vonage, ws.StateServerSide)
	vonageCloser := newGracefulWebSocket(vonage, vonageWriter)
	defer func() {
		code, reason := closeState.Details()
		vonageCloser.Close(code, reason)
	}()

	coreURL, err := url.Parse(bridgeURL)
	if err != nil {
		closeState.SetLeg(mediaCloseLegLocalError, ws.StatusInternalServerError, "invalid realtime bridge URL")
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "invalid realtime bridge URL", string(mediaCloseLegLocalError))
		globalCtx.Logger().Warn("parse audio bridge url", "provider", "vonage", "err", err, "url", redactURL(row.AudioBridgeURL))
		return
	}
	dialer := ws.Dialer{}
	core, _, _, err := dialer.Dial(r.Context(), coreURL.String())
	if err != nil {
		closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "realtime bridge rejected")
		_ = a.db().updateMediaStatusWithLeg(callID, "error", err.Error(), 1011, "core audio bridge rejected", string(mediaCloseLegCore))
		globalCtx.Logger().Warn("dial core audio bridge", "provider", "vonage", "err", err, "url", redactURL(coreURL.String()))
		return
	}
	coreWriter := newWebSocketWriterPump(core, ws.StateClientSide)
	coreCloser := newGracefulWebSocket(core, coreWriter)
	defer func() {
		code, reason := closeState.Details()
		coreCloser.Close(code, reason)
	}()

	globalCtx.Logger().Info("bridge up", "provider", "vonage", "call", callID, "thread", row.ThreadID)
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
		vonageCloser.Close(code, reason)
		coreCloser.Close(code, reason)
	}()

	audioFrontend := newCarrierAudioFrontend(16000)
	audioFrontend.mode = localBargeInOff
	defer logAudioFrontendDiagnostics(globalCtx.Logger(), audioFrontend, row, "vonage", carrierCodecL16_16, 0)

	go func() {
		defer cancel()
		inputResampler := newPCMResampler(16000, 24000)
		for {
			data, op, err := readWebSocketData(vonage, ws.StateServerSide, vonageWriter)
			if err != nil {
				code, reason := websocketCloseDetails(err)
				closeState.SetLeg(mediaCloseLegCarrier, code, reason)
				return
			}
			if len(data) > maxCarrierFrameBytes {
				closeState.SetLeg(mediaCloseLegCarrier, ws.StatusMessageTooBig, "Vonage media frame too large")
				return
			}
			if op != ws.OpBinary || len(data) == 0 {
				continue
			}
			processed := audioFrontend.process(bytesToPCM16(data))
			pcm24 := inputResampler.Process(processed.PCM)
			if len(pcm24) == 0 {
				continue
			}
			err = coreWriter.Write(ws.OpBinary, pcm16ToBytes(pcm24))
			if err != nil {
				closeState.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "write caller audio to realtime bridge")
				return
			}
		}
	}()

	outputResampler := newPCMResampler(24000, 16000)
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
			if ok && control.Type == "interrupt" {
				audioFrontend.markInterrupt(control.Source)
			}
			continue
		}
		if op != ws.OpBinary || len(data) == 0 {
			continue
		}
		pcm16 := outputResampler.Process(bytesToPCM16(data))
		err = writeVonageFrames(vonageWriter, pcm16ToBytes(pcm16))
		if err != nil {
			closeState.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "write realtime audio to Vonage")
			return
		}
	}
}

func callIDFromMediaPath(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, prefix), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0]
}

func mediaTokenFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "media" {
		return ""
	}
	return parts[3]
}

func frameStreamID(f carrierMediaFrame) string {
	if f.StreamSID != "" {
		return f.StreamSID
	}
	if f.StreamID != "" {
		return f.StreamID
	}
	if f.Start != nil {
		if f.Start.StreamSID != "" {
			return f.Start.StreamSID
		}
		return f.Start.StreamID
	}
	return ""
}

func frameCallID(f carrierMediaFrame) string {
	if f.Start == nil {
		return ""
	}
	return firstNonEmpty(f.Start.CallSID, f.Start.CallID, f.Start.CallUUID, f.Start.CallControlID)
}

func carrierInputResampler(codec string) *pcmResampler {
	switch codec {
	case carrierCodecPCMU8:
		return newPCMResampler(8000, 24000)
	case carrierCodecL16_16:
		return newPCMResampler(16000, 24000)
	default:
		return nil
	}
}

func carrierOutputResampler(codec string) *pcmResampler {
	switch codec {
	case carrierCodecPCMU8:
		return newPCMResampler(24000, 8000)
	case carrierCodecL16_16:
		return newPCMResampler(24000, 16000)
	default:
		return nil
	}
}

func decodeCarrierPayload(payload, codec string, streaming ...*pcmResampler) ([]byte, error) {
	pcm, err := decodeCarrierPCM(payload, codec)
	if err != nil {
		return nil, err
	}
	switch codec {
	case carrierCodecPCMU8:
		if len(streaming) > 0 && streaming[0] != nil {
			return pcm16ToBytes(streaming[0].Process(pcm)), nil
		}
		return pcm16ToBytes(resamplePCM(pcm, 8000, 24000)), nil
	case carrierCodecL16_16:
		if len(streaming) > 0 && streaming[0] != nil {
			return pcm16ToBytes(streaming[0].Process(pcm)), nil
		}
		return pcm16ToBytes(resamplePCM(pcm, 16000, 24000)), nil
	case carrierCodecL16_24:
		return pcm16ToBytes(pcm), nil
	default:
		return pcm16ToBytes(pcm), nil
	}
}

func decodeCarrierPCM(payload, codec string) ([]int16, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	if codec == carrierCodecPCMU8 {
		return ulawToPCM16(raw), nil
	}
	return bytesToPCM16(raw), nil
}

func encodeCarrierPayload(pcm24Bytes []byte, codec string, streaming ...*pcmResampler) (string, error) {
	switch codec {
	case carrierCodecPCMU8:
		pcm24 := bytesToPCM16(pcm24Bytes)
		pcm8 := resamplePCM(pcm24, 24000, 8000)
		if len(streaming) > 0 && streaming[0] != nil {
			pcm8 = streaming[0].Process(pcm24)
		}
		raw := pcm16ToUlaw(pcm8)
		return base64.StdEncoding.EncodeToString(raw), nil
	case carrierCodecL16_16:
		pcm24 := bytesToPCM16(pcm24Bytes)
		pcm16 := resamplePCM(pcm24, 24000, 16000)
		if len(streaming) > 0 && streaming[0] != nil {
			pcm16 = streaming[0].Process(pcm24)
		}
		return base64.StdEncoding.EncodeToString(pcm16ToBytes(pcm16)), nil
	case carrierCodecL16_24:
		return base64.StdEncoding.EncodeToString(pcm24Bytes), nil
	default:
		return base64.StdEncoding.EncodeToString(pcm24Bytes), nil
	}
}

func buildCarrierOutbound(shape, streamSID, payload string) any {
	switch shape {
	case "plivo":
		return map[string]any{
			"event": "playAudio",
			"media": map[string]string{
				"contentType": "audio/x-mulaw",
				"sampleRate":  "8000",
				"payload":     payload,
			},
		}
	case "signalwire":
		return map[string]any{
			"event":     "media",
			"streamSid": streamSID,
			"media": map[string]string{
				"payload": payload,
			},
		}
	default:
		return map[string]any{
			"event": "media",
			"media": map[string]string{
				"payload": payload,
			},
		}
	}
}

func buildCarrierClear(shape, streamSID string) any {
	switch shape {
	case "signalwire":
		return map[string]string{"event": "clear", "streamSid": streamSID}
	case "plivo":
		return map[string]string{"event": "clearAudio"}
	case "telnyx":
		return map[string]string{"event": "clear"}
	default:
		return nil
	}
}

func (a *App) finishMediaBridge(callID string, leg mediaCloseLeg, code ws.StatusCode, reason string) {
	status := "disconnected"
	errMsg := ""
	if code == ws.StatusInternalServerError {
		status = "error"
		errMsg = reason
	}
	_ = a.db().updateMediaStatusWithLeg(callID, status, errMsg, int(code), reason, string(leg))
	if status == "error" {
		_ = a.db().setStateExpiry(callID, time.Now().UTC().Add(2*time.Minute))
	} else {
		_ = a.db().clearStateExpiry(callID)
	}
	globalCtx.Logger().Info("media bridge down", "call", callID, "leg", leg, "code", code, "reason", reason)
}

func upsample16to24(pcm16 []int16) []int16 {
	return resamplePCM(pcm16, 16000, 24000)
}

func downsample24to16(pcm24 []int16) []int16 {
	return resamplePCM(pcm24, 24000, 16000)
}

func writeVonageFrames(writer *websocketWriterPump, data []byte) error {
	const frameBytes = 640 // 20ms of PCM16 mono at 16kHz.
	for len(data) > 0 {
		n := frameBytes
		if len(data) < n {
			n = len(data)
		}
		if err := writer.Write(ws.OpBinary, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
