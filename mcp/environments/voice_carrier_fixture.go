package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

type carrierRouteResult struct {
	InboundURL string `json:"inbound_url"`
	Route      struct {
		ID          string `json:"id"`
		PhoneNumber string `json:"phone_number"`
	} `json:"route"`
}

type carrierActiveCalls struct {
	Calls []struct {
		CallID   string `json:"call_id"`
		ThreadID string `json:"thread_id"`
		Status   string `json:"status"`
	} `json:"calls"`
}

type carrierTwiML struct {
	Connect struct {
		Stream struct {
			URL            string `xml:"url,attr"`
			StatusCallback string `xml:"statusCallback,attr"`
		} `xml:"Stream"`
	} `xml:"Connect"`
}

type carrierMediaSocket struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *carrierMediaSocket) writeJSON(value any) error {
	raw, _ := json.Marshal(value)
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteMessage(websocket.TextMessage, raw)
}

func (s *service) runCarrierVoiceCall(ctx context.Context, run *Run, spec VoiceFixtureSpec) (call *VoiceCall, err error) {
	fixture, err := s.db.getProtocolFixture(run.ID, spec.ProtocolFixture)
	if err != nil || fixture == nil {
		if err == nil {
			err = errors.New("protocol fixture not found")
		}
		return nil, err
	}
	if fixture.Pack != protocolPackTelephonyCarrier || fixture.Protocol != protocolTwilio {
		return nil, fmt.Errorf("protocol fixture %q does not support carrier voice", fixture.ID)
	}
	call = &VoiceCall{
		ID: "voice_" + token(12), RunID: run.ID, Status: "running", Spec: spec,
		CallerThreadID: "caller-" + token(6), CallerAgentAlias: "voice-caller-" + token(5),
		StartedAt: time.Now().UTC(), Transcript: []VoiceTranscriptTurn{},
		Validity: VoiceCallValidity{Status: "pending"},
	}
	if err := s.db.saveVoiceCall(call); err != nil {
		return nil, err
	}
	s.ctx.Emit("environment.voice_call.started", voiceCallEvent(call))
	defer func() {
		finished := time.Now().UTC()
		call.FinishedAt = &finished
		call.Metrics.DurationMS = finished.Sub(call.StartedAt).Milliseconds()
		call.ProtocolEvents, _ = s.db.listProtocolEvents(run.ID, fixture.ID, call.ID)
		if err != nil {
			call.Status = "failed"
			call.Error = err.Error()
		} else if call.Validity.Status == "invalid" {
			call.Status = "invalid_simulation"
		} else if call.Status == "running" {
			call.Status = "completed"
		}
		_ = s.db.saveVoiceCall(call)
		topic := "environment.voice_call.completed"
		if call.Status == "failed" {
			topic = "environment.voice_call.failed"
		} else if call.Status == "invalid_simulation" {
			topic = "environment.voice_call.invalid"
		}
		s.ctx.Emit(topic, voiceCallEvent(call))
	}()

	callerDirective := voiceCallerDirective(spec)
	if _, err = s.runtime().SpawnRuntimeAgent(run.RuntimeID, sdk.RuntimeAgentSpawnRequest{
		Draft: &sdk.RuntimeAgentDraft{Name: firstNonEmpty(spec.CallerName, "Evaluation caller"), Directive: callerDirective, Mode: "autonomous"},
		Alias: call.CallerAgentAlias, StartPaused: true,
	}); err != nil {
		return call, fmt.Errorf("spawn caller agent: %w", err)
	}
	defer s.runtime().StopRuntimeAgent(run.RuntimeID, call.CallerAgentAlias)

	caller, err := s.runtime().SpawnRuntimeRealtimeThread(run.RuntimeID, call.CallerAgentAlias, sdk.RuntimeRealtimeSpawnRequest{
		ThreadID: call.CallerThreadID, Directive: callerDirective,
		Provider: firstNonEmpty(spec.CallerProvider, spec.Provider), Voice: spec.CallerVoice,
		Tools: []string{"done"}, Ephemeral: true, BridgeDisconnectTTLSeconds: 15,
	})
	if err != nil {
		return call, fmt.Errorf("spawn caller voice: %w", err)
	}
	defer s.runtime().StopRuntimeRealtimeThread(run.RuntimeID, call.CallerAgentAlias, call.CallerThreadID)
	if caller.AudioBridgeURL == "" {
		return call, errors.New("caller realtime thread returned no audio bridge")
	}

	var route carrierRouteResult
	endpoint, err := s.runtime().GetRuntimeAppEndpoint(run.RuntimeID, fixture.TargetApp)
	if err != nil {
		return call, fmt.Errorf("load runtime app endpoint: %w", err)
	}
	virtualNumber := carrierVirtualNumber(call.ID)
	routeInput := map[string]any{
		"phone_number": virtualNumber,
		"answer_mode":  "realtime_immediate", "directive": voiceTargetDirective(spec),
		"voice": spec.Voice, "greeting": firstNonEmpty(spec.Greeting, voiceOpeningCue()),
		"timeout_sec": spec.TimeoutSeconds, "recording_mode": "off",
	}
	if err = s.runtime().CallRuntimeAppAsAgentResult(run.RuntimeID, fixture.TargetApp, spec.TargetAgent, "telephony_routes_create", routeInput, &route); err != nil {
		return call, fmt.Errorf("create simulated inbound route: %w", err)
	}
	if route.InboundURL == "" {
		return call, errors.New("telephony returned no inbound URL")
	}
	s.addProtocolEvent(run.ID, fixture.ID, call.ID, "route.created", "fixture_to_app", map[string]any{"route_id": route.Route.ID, "phone_number": route.Route.PhoneNumber})

	callSID := "CA" + token(32)
	from := stringConfig(fixture.Config, "caller_number", "+15550100002")
	to := firstNonEmpty(route.Route.PhoneNumber, virtualNumber)
	inboundForm := url.Values{"CallSid": {callSID}, "From": {from}, "To": {to}, "CallStatus": {"ringing"}}
	inboundGatewayURL, err := carrierGatewayURL(route.InboundURL, endpoint)
	if err != nil {
		return call, err
	}
	twimlRaw, status, postErr := carrierPOST(ctx, inboundGatewayURL, route.InboundURL, inboundForm, stringConfig(fixture.Config, "auth_token", ""))
	s.addProtocolEvent(run.ID, fixture.ID, call.ID, "webhook.inbound", "fixture_to_app", map[string]any{"status": status, "call_sid": callSID, "from": from, "to": to})
	if postErr != nil {
		return call, fmt.Errorf("deliver inbound carrier webhook: %w", postErr)
	}
	if status < 200 || status >= 300 {
		return call, fmt.Errorf("inbound carrier webhook returned %d: %s", status, strings.TrimSpace(string(twimlRaw)))
	}
	var response carrierTwiML
	if err := xml.Unmarshal(twimlRaw, &response); err != nil {
		return call, fmt.Errorf("parse Telephony response: %w", err)
	}
	if response.Connect.Stream.URL == "" {
		return call, errors.New("Telephony did not answer with a media stream")
	}

	var active carrierActiveCalls
	if err := s.runtime().CallRuntimeAppAsAgentResult(run.RuntimeID, fixture.TargetApp, spec.TargetAgent, "telephony_active_calls", map[string]any{}, &active); err != nil {
		return call, fmt.Errorf("inspect inbound call: %w", err)
	}
	for _, item := range active.Calls {
		if item.ThreadID != "" {
			call.TargetThreadID = item.ThreadID
			break
		}
	}
	if call.TargetThreadID == "" {
		return call, errors.New("Telephony started no realtime thread for the inbound call")
	}

	callerConn, _, err := websocket.DefaultDialer.DialContext(ctx, caller.AudioBridgeURL, nil)
	if err != nil {
		return call, fmt.Errorf("connect caller audio: %w", err)
	}
	callerSocket := &voiceSocket{conn: callerConn}
	defer callerSocket.close()

	headers := http.Header{}
	headers.Set("X-Twilio-Signature", twilioFixtureSignature(response.Connect.Stream.URL, url.Values{}, stringConfig(fixture.Config, "auth_token", "")))
	mediaGatewayURL, err := carrierGatewayURL(response.Connect.Stream.URL, endpoint)
	if err != nil {
		return call, err
	}
	mediaConn, _, err := websocket.DefaultDialer.DialContext(ctx, mediaGatewayURL, headers)
	if err != nil {
		return call, fmt.Errorf("connect simulated carrier media: %w", err)
	}
	media := &carrierMediaSocket{conn: mediaConn}
	defer mediaConn.Close()
	streamSID := "MZ" + token(32)
	if err := media.writeJSON(map[string]any{"event": "connected", "protocol": "Call", "version": "1.0.0"}); err != nil {
		return call, err
	}
	if err := media.writeJSON(map[string]any{"event": "start", "streamSid": streamSID, "start": map[string]any{"callSid": callSID, "accountSid": stringConfig(fixture.Config, "account_sid", ""), "tracks": []string{"inbound"}, "mediaFormat": map[string]any{"encoding": "audio/x-mulaw", "sampleRate": 8000, "channels": 1}}}); err != nil {
		return call, err
	}
	s.addProtocolEvent(run.ID, fixture.ID, call.ID, "media.connected", "bidirectional", map[string]any{"stream_sid": streamSID, "codec": "PCMU", "sample_rate": 8000})
	if response.Connect.Stream.StatusCallback != "" {
		callbackGatewayURL, _ := carrierGatewayURL(response.Connect.Stream.StatusCallback, endpoint)
		_, _, _ = carrierPOST(ctx, callbackGatewayURL, response.Connect.Stream.StatusCallback, url.Values{"CallSid": {callSID}, "StreamSid": {streamSID}, "StreamEvent": {"stream-started"}}, stringConfig(fixture.Config, "auth_token", ""))
		s.addProtocolEvent(run.ID, fixture.ID, call.ID, "callback.stream_started", "fixture_to_app", map[string]any{"stream_sid": streamSID})
	}

	call.StartedAt = time.Now().UTC()
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var receptionistAudio, callerAudio cappedAudio
	bridgeErrors := make(chan error, 2)
	mediaActivity := newVoiceMediaActivity(call.StartedAt)
	go relayCallerToCarrier(bridgeCtx, callerSocket, media, streamSID, mediaActivity, &callerAudio, bridgeErrors)
	go relayCarrierToCaller(bridgeCtx, media, callerSocket, mediaActivity, &receptionistAudio, bridgeErrors)

	timeout := time.NewTimer(time.Duration(spec.TimeoutSeconds) * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	endedBy := ""
	lastTranscript := ""
	callerDone := false
	for endedBy == "" {
		select {
		case <-ctx.Done():
			return call, ctx.Err()
		case <-timeout.C:
			endedBy = "timeout"
		case bridgeErr := <-bridgeErrors:
			if bridgeErr == nil || websocket.IsCloseError(bridgeErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				endedBy = "caller_done"
			} else if s.waitForVoiceCallerCompletion(ctx, run.RuntimeID, call) {
				endedBy = "caller_done"
			} else {
				endedBy = "audio_disconnected"
			}
		case <-ticker.C:
			callerEvents, callerErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, call.CallerAgentAlias, call.StartedAt, 500)
			if callerErr == nil && voiceCallerCompleted(callerEvents, call.CallerThreadID) {
				callerDone = true
			}
			targetEvents, targetErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, spec.TargetAgent, call.StartedAt, 1000)
			if targetErr != nil {
				continue
			}
			transcript := voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt)
			raw, _ := json.Marshal(transcript)
			if len(transcript) > 0 && string(raw) != lastTranscript {
				lastTranscript = string(raw)
				call.Transcript = transcript
				call.TargetTelemetry = targetEvents
				_ = s.db.saveVoiceCall(call)
				s.ctx.Emit("environment.voice_call.progress", voiceCallEvent(call))
			}
			if callerErr == nil {
				if callerDone && voiceRelayDeliverySettled(mediaActivity, targetEvents, call.TargetThreadID, callerEvents, call.CallerThreadID) {
					endedBy = "caller_done"
				} else if voiceConversationIsIdle(time.Now(), mediaActivity, transcript, targetEvents, call.TargetThreadID, callerEvents, call.CallerThreadID) {
					endedBy = "conversation_idle"
				}
			}
		}
	}
	cancel()
	_ = media.writeJSON(map[string]any{"event": "stop", "streamSid": streamSID, "stop": map[string]any{"accountSid": stringConfig(fixture.Config, "account_sid", ""), "callSid": callSID}})
	_ = mediaConn.Close()
	callerSocket.close()

	if response.Connect.Stream.StatusCallback != "" {
		callbackGatewayURL, _ := carrierGatewayURL(response.Connect.Stream.StatusCallback, endpoint)
		_, _, _ = carrierPOST(context.Background(), callbackGatewayURL, response.Connect.Stream.StatusCallback, url.Values{"CallSid": {callSID}, "StreamSid": {streamSID}, "StreamEvent": {"stream-stopped"}}, stringConfig(fixture.Config, "auth_token", ""))
		s.addProtocolEvent(run.ID, fixture.ID, call.ID, "callback.stream_stopped", "fixture_to_app", map[string]any{"stream_sid": streamSID})
	}
	statusURL := carrierInboundStatusURL(route.InboundURL)
	statusGatewayURL, _ := carrierGatewayURL(statusURL, endpoint)
	_, _, _ = carrierPOST(context.Background(), statusGatewayURL, statusURL, url.Values{"CallSid": {callSID}, "From": {from}, "To": {to}, "CallStatus": {"completed"}}, stringConfig(fixture.Config, "auth_token", ""))
	s.addProtocolEvent(run.ID, fixture.ID, call.ID, "callback.call_completed", "fixture_to_app", map[string]any{"call_sid": callSID})

	targetEvents, targetErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, spec.TargetAgent, call.StartedAt, 1000)
	if targetErr != nil {
		return call, fmt.Errorf("read receptionist telemetry: %w", targetErr)
	}
	callerEvents, callerErr := s.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, call.CallerAgentAlias, call.StartedAt, 1000)
	if callerErr != nil {
		return call, fmt.Errorf("read caller telemetry: %w", callerErr)
	}
	call.TargetTelemetry = targetEvents
	call.CallerTelemetry = callerEvents
	call.Transcript = voiceTranscript(targetEvents, call.TargetThreadID, call.StartedAt)
	call.Execution, _ = s.runtime().WaitRuntimeAgent(run.RuntimeID, spec.TargetAgent, sdk.RuntimeAgentWaitRequest{
		ThreadID: call.TargetThreadID, TimeoutSeconds: 5, IdleSeconds: 1, PostToolIdleSeconds: 1, RequireActivity: true,
	})
	call.Metrics = voiceMetrics(targetEvents, callerEvents, call.TargetThreadID, call.CallerThreadID, call.StartedAt, receptionistAudio.bytes(), callerAudio.bytes(), endedBy)
	call.Validity = assessVoiceCall(call)
	if err := s.writeVoiceRecording(call.ID, "receptionist", receptionistAudio.bytes()); err != nil {
		return call, err
	}
	if err := s.writeVoiceRecording(call.ID, "caller", callerAudio.bytes()); err != nil {
		return call, err
	}
	call.TargetRecording = "receptionist"
	call.CallerRecording = "caller"
	return call, nil
}

func (s *service) addProtocolEvent(runID, fixtureID, callID, eventType, direction string, data map[string]any) {
	_ = s.db.addProtocolEvent(&ProtocolFixtureEvent{RunID: runID, FixtureID: fixtureID, CallID: callID, Type: eventType, Direction: direction, Data: data, CreatedAt: time.Now().UTC()})
}

func carrierPOST(ctx context.Context, endpoint, signedEndpoint string, form url.Values, authToken string) ([]byte, int, error) {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", twilioFixtureSignature(signedEndpoint, form, authToken))
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, err
}

func carrierGatewayURL(external string, endpoint *sdk.RuntimeAppEndpoint) (string, error) {
	if endpoint == nil || endpoint.PlatformURL == "" || endpoint.GatewayURL == "" {
		return "", errors.New("runtime app endpoint is incomplete")
	}
	externalURL, err := url.Parse(external)
	if err != nil {
		return "", fmt.Errorf("parse carrier URL: %w", err)
	}
	publicBase, err := url.Parse(endpoint.PlatformURL)
	if err != nil {
		return "", fmt.Errorf("parse runtime public URL: %w", err)
	}
	gatewayBase, err := url.Parse(endpoint.GatewayURL)
	if err != nil {
		return "", fmt.Errorf("parse runtime gateway URL: %w", err)
	}
	publicPath := strings.TrimRight(publicBase.Path, "/")
	if externalURL.Host != publicBase.Host || !strings.HasPrefix(externalURL.Path, publicPath+"/") {
		return "", fmt.Errorf("carrier URL escaped runtime origin")
	}
	gatewayBase.Path = strings.TrimRight(gatewayBase.Path, "/") + strings.TrimPrefix(externalURL.Path, publicPath)
	gatewayBase.RawQuery = externalURL.RawQuery
	if externalURL.Scheme == "wss" {
		gatewayBase.Scheme = "ws"
	}
	return gatewayBase.String(), nil
}

func twilioFixtureSignature(endpoint string, form url.Values, authToken string) string {
	keys := make([]string, 0, len(form))
	for key := range form {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var signed strings.Builder
	signed.WriteString(endpoint)
	for _, key := range keys {
		if values := form[key]; len(values) > 0 {
			signed.WriteString(key)
			signed.WriteString(values[0])
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	_, _ = mac.Write([]byte(signed.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func carrierInboundStatusURL(inbound string) string {
	parsed, err := url.Parse(inbound)
	if err != nil {
		return inbound
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/status"
	return parsed.String()
}

func carrierVirtualNumber(callID string) string {
	var digits strings.Builder
	for _, value := range callID {
		if value >= '0' && value <= '9' {
			digits.WriteRune(value)
		}
	}
	suffix := digits.String()
	if len(suffix) < 6 {
		for _, value := range []byte(callID) {
			suffix += fmt.Sprintf("%d", value%10)
			if len(suffix) >= 6 {
				break
			}
		}
	}
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return "+1555" + suffix
}

func relayCallerToCarrier(ctx context.Context, caller *voiceSocket, media *carrierMediaSocket, streamSID string, activity *voiceMediaActivity, capture *cappedAudio, result chan<- error) {
	resampler := newCarrierResampler(24000, 8000)
	for {
		messageType, data, err := caller.conn.ReadMessage()
		if err != nil {
			sendVoiceRelayResult(ctx, result, err)
			return
		}
		if messageType != websocket.BinaryMessage || len(data) == 0 {
			continue
		}
		capture.append(data)
		activity.update("caller", true, time.Now())
		pcm8 := resampler.Process(carrierBytesToPCM(data))
		for len(pcm8) > 0 {
			count := 160
			if len(pcm8) < count {
				count = len(pcm8)
			}
			payload := base64.StdEncoding.EncodeToString(carrierPCMToUlaw(pcm8[:count]))
			if err := media.writeJSON(map[string]any{"event": "media", "streamSid": streamSID, "media": map[string]string{"track": "inbound", "payload": payload}}); err != nil {
				sendVoiceRelayResult(ctx, result, err)
				return
			}
			pcm8 = pcm8[count:]
		}
	}
}

func relayCarrierToCaller(ctx context.Context, media *carrierMediaSocket, caller *voiceSocket, activity *voiceMediaActivity, capture *cappedAudio, result chan<- error) {
	resampler := newCarrierResampler(8000, 24000)
	lastSpeech := time.Time{}
	for {
		messageType, raw, err := media.conn.ReadMessage()
		if err != nil {
			sendVoiceRelayResult(ctx, result, err)
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var frame struct {
			Event     string `json:"event"`
			StreamSID string `json:"streamSid"`
			Media     *struct {
				Payload string `json:"payload"`
			} `json:"media"`
			Mark *struct {
				Name string `json:"name"`
			} `json:"mark"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		if frame.Event == "mark" && frame.Mark != nil {
			_ = media.writeJSON(map[string]any{"event": "mark", "streamSid": frame.StreamSID, "mark": map[string]string{"name": frame.Mark.Name}})
			continue
		}
		if frame.Event != "media" || frame.Media == nil {
			continue
		}
		mu, decodeErr := base64.StdEncoding.DecodeString(frame.Media.Payload)
		if decodeErr != nil {
			continue
		}
		pcm24 := resampler.Process(carrierUlawToPCM(mu))
		data := carrierPCMToBytes(pcm24)
		if len(data) == 0 {
			continue
		}
		now := time.Now()
		if carrierRMS(pcm24) > 300 && (lastSpeech.IsZero() || now.Sub(lastSpeech) > 500*time.Millisecond) {
			_ = caller.write(websocket.TextMessage, []byte(`{"type":"input.speech_started"}`))
		}
		if carrierRMS(pcm24) > 300 {
			lastSpeech = now
		}
		capture.append(data)
		activity.update("receptionist", true, now)
		if err := caller.write(websocket.BinaryMessage, data); err != nil {
			sendVoiceRelayResult(ctx, result, err)
			return
		}
	}
}

type carrierResampler struct {
	inRate, outRate float64
	step, cutoff    float64
	half            int
	base            int64
	next            float64
	samples         []float64
}

func newCarrierResampler(inRate, outRate int) *carrierResampler {
	const half = 16
	cutoff := 0.94
	if outRate < inRate {
		cutoff *= float64(outRate) / float64(inRate)
	}
	return &carrierResampler{inRate: float64(inRate), outRate: float64(outRate), step: float64(inRate) / float64(outRate), cutoff: cutoff, half: half, base: -half, samples: make([]float64, half)}
}

func (r *carrierResampler) Process(input []int16) []int16 {
	for _, sample := range input {
		r.samples = append(r.samples, float64(sample))
	}
	last := r.base + int64(len(r.samples)) - 1
	out := make([]int16, 0, int(math.Ceil(float64(len(input))*r.outRate/r.inRate))+2)
	for r.next+float64(r.half) <= float64(last) {
		center := int64(math.Floor(r.next))
		var sum, weights float64
		for n := center - int64(r.half) + 1; n <= center+int64(r.half); n++ {
			index := n - r.base
			if index < 0 || index >= int64(len(r.samples)) {
				continue
			}
			distance := r.next - float64(n)
			weight := r.cutoff * carrierSinc(r.cutoff*distance) * carrierBlackman(distance/float64(r.half))
			sum += r.samples[index] * weight
			weights += weight
		}
		if math.Abs(weights) > 1e-12 {
			sum /= weights
		}
		out = append(out, carrierClamp(sum))
		r.next += r.step
	}
	keepFrom := int64(math.Floor(r.next)) - int64(r.half) - 1
	if drop := keepFrom - r.base; drop > 0 {
		if drop > int64(len(r.samples)) {
			drop = int64(len(r.samples))
		}
		r.samples = append(r.samples[:0], r.samples[drop:]...)
		r.base += drop
	}
	return out
}

func carrierSinc(value float64) float64 {
	if math.Abs(value) < 1e-12 {
		return 1
	}
	value *= math.Pi
	return math.Sin(value) / value
}

func carrierBlackman(position float64) float64 {
	if position <= -1 || position >= 1 {
		return 0
	}
	return 0.42 + 0.5*math.Cos(math.Pi*position) + 0.08*math.Cos(2*math.Pi*position)
}

func carrierClamp(value float64) int16 {
	if value > 32767 {
		return 32767
	}
	if value < -32768 {
		return -32768
	}
	return int16(math.Round(value))
}

func carrierBytesToPCM(data []byte) []int16 {
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return out
}

func carrierPCMToBytes(pcm []int16) []byte {
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, pcm)
	return out.Bytes()
}

func carrierUlawToPCM(mu []byte) []int16 {
	out := make([]int16, len(mu))
	for i, value := range mu {
		value = ^value
		sign := int16(value & 0x80)
		exponent := int16((value >> 4) & 0x07)
		mantissa := int16(value & 0x0f)
		sample := ((mantissa << 3) + 0x84) << exponent
		sample -= 0x84
		if sign != 0 {
			sample = -sample
		}
		out[i] = sample
	}
	return out
}

func carrierPCMToUlaw(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, sample := range pcm {
		const bias = 0x84
		const clip = 32635
		sign := byte(0)
		value := int(sample)
		if value < 0 {
			value = -value
			sign = 0x80
		}
		if value > clip {
			value = clip
		}
		value += bias
		exponent := 7
		for mask := 0x4000; exponent > 0 && value&mask == 0; exponent-- {
			mask >>= 1
		}
		mantissa := (value >> (exponent + 3)) & 0x0f
		out[i] = ^(sign | byte(exponent<<4) | byte(mantissa))
	}
	return out
}

func carrierRMS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range pcm {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(pcm)))
}
