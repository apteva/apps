//go:build integration

package main

// Tier 2: compiled sidecar + real HTTP and WebSocket boundaries. The carrier
// and browser are deterministic protocol peers, so this exercises the complete
// Telephony-owned call chain without credentials, PSTN spend, an LLM, or a
// human tester.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const (
	tier2Project = "telephony-tier2"
	tier2Number  = "+33189313431"
	tier2Caller  = "+34600000001"
)

type tier2CarrierCall struct {
	Tool  string
	Input map[string]any
}

type tier2AppEvent struct {
	Topic     string         `json:"topic"`
	ProjectID string         `json:"project_id"`
	Data      map[string]any `json:"data"`
}

type tier2PlatformGateway struct {
	server       *httptest.Server
	privateKey   ed25519.PrivateKey
	publicKeyB64 string
	carrierCalls chan tier2CarrierCall
	events       chan tier2AppEvent
}

func newTier2PlatformGateway(t *testing.T) *tier2PlatformGateway {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &tier2PlatformGateway{
		privateKey: privateKey, publicKeyB64: base64.StdEncoding.EncodeToString(publicKey),
		carrierCalls: make(chan tier2CarrierCall, 32), events: make(chan tier2AppEvent, 64),
	}
	gateway.server = httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	t.Cleanup(gateway.server.Close)
	return gateway
}

func (g *tier2PlatformGateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/whoami":
		writeTier2JSON(w, map[string]any{
			"app_name": "telephony", "version": "test", "install_id": 42,
			"project_id": tier2Project, "bindings": map[string]any{"carrier": 113},
			"public_url": "https://public.example.test",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/connections/113":
		writeTier2JSON(w, map[string]any{
			"id": 113, "app_slug": "telnyx", "name": "Tier 2 Telnyx", "status": "connected", "project_id": tier2Project,
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/apps/callback/connections/113/credentials":
		writeTier2JSON(w, map[string]any{
			"id": 113, "slug": "telnyx", "fields": map[string]string{
				"phone_number": tier2Number, "connection_id": "previous-app-1", "public_key": g.publicKeyB64,
			},
		})
	case r.Method == http.MethodPost && r.URL.Path == "/api/apps/callback/integrations/113/execute":
		var request struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid integration request", http.StatusBadRequest)
			return
		}
		g.carrierCalls <- tier2CarrierCall{Tool: request.Tool, Input: request.Input}
		data := map[string]any{"data": map[string]any{"ok": true}}
		switch request.Tool {
		case "list_phone_numbers":
			data = map[string]any{"data": []any{map[string]any{
				"id": "phone-test-1", "phone_number": tier2Number, "connection_id": "previous-app-1",
			}}}
		case "list_outbound_voice_profiles":
			data = map[string]any{"data": []any{}, "meta": map[string]any{"total_pages": 1}}
		case "create_call_control_application":
			data = map[string]any{"data": map[string]any{"id": "application-test-1"}}
		case "update_phone_number":
			data = map[string]any{"data": map[string]any{"id": "phone-test-1"}}
		case "dial_call":
			data = map[string]any{"data": map[string]any{"call_control_id": "ring:" + fmt.Sprint(request.Input["to"])}}
		case "answer_call":
			data = map[string]any{"data": map[string]any{"call_control_id": "call-control-test-1"}}
		}
		writeTier2JSON(w, map[string]any{"success": true, "status": 200, "data": data})
	case r.Method == http.MethodPost && r.URL.Path == "/api/app-events/internal/emit":
		var event tier2AppEvent
		if json.NewDecoder(r.Body).Decode(&event) == nil {
			select {
			case g.events <- event:
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/apps/callback/agents/"):
		// The incoming-call outbox independently delivers the durable thread
		// event. A Tier-2 platform double only needs to acknowledge that delivery.
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported test platform route", http.StatusNotFound)
	}
}

func writeTier2JSON(w http.ResponseWriter, value any) {
	_ = json.NewEncoder(w).Encode(value)
}

func (g *tier2PlatformGateway) waitCarrierTool(t *testing.T, tool string) tier2CarrierCall {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case call := <-g.carrierCalls:
			if call.Tool == tool {
				return call
			}
		case <-deadline:
			t.Fatalf("timed out waiting for carrier tool %s", tool)
		}
	}
}

func (g *tier2PlatformGateway) waitEvent(t *testing.T, topic string) tier2AppEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-g.events:
			if event.Topic == topic {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for app event %s", topic)
		}
	}
}

func tier2Headers() map[string]string {
	return map[string]string{"X-User-ID": "1", "X-Apteva-Project-ID": tier2Project}
}

func tier2Request(t *testing.T, sc *tk.Sidecar, method, path string, body, out any, headers map[string]string) (int, string) {
	t.Helper()
	var encoded io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		encoded = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, sc.URL()+path, encoded)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s %s: %v; body=%s", method, path, err, raw)
		}
	}
	return resp.StatusCode, string(raw)
}

func tier2MCPAs(t *testing.T, sc *tk.Sidecar, tool string, args map[string]any) map[string]any {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	headers := map[string]string{
		"X-Apteva-Caller-Agent": "7", "X-Apteva-Caller-Thread": "main", "X-Apteva-Project-ID": tier2Project,
	}
	status, raw := tier2Request(t, sc, http.MethodPost, "/mcp", payload, &response, headers)
	if status != http.StatusOK || response.Error != nil || len(response.Result.Content) == 0 {
		t.Fatalf("MCP %s failed: status=%d error=%v body=%s", tool, status, response.Error, raw)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &result); err != nil {
		t.Fatalf("decode MCP %s result: %v; text=%s", tool, err, response.Result.Content[0].Text)
	}
	if response.Result.IsError {
		t.Fatalf("MCP %s returned tool error: %v", tool, result)
	}
	return result
}

func tier2SignedPOST(t *testing.T, g *tier2PlatformGateway, endpoint string, body []byte) *http.Response {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := append([]byte(timestamp+"|"), body...)
	signature := ed25519.Sign(g.privateKey, message)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Telnyx-Timestamp", timestamp)
	req.Header.Set("Telnyx-Signature-Ed25519", base64.StdEncoding.EncodeToString(signature))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func localSidecarURL(t *testing.T, sc *tk.Sidecar, remote string) string {
	t.Helper()
	parsed, err := url.Parse(remote)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(parsed.Path, "/api/apps/telephony")
	if path == parsed.Path {
		t.Fatalf("public app URL has unexpected path: %s", remote)
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return sc.URL() + path
}

func rawSidecarMediaURL(t *testing.T, sc *tk.Sidecar, remote string) string {
	t.Helper()
	parsed, err := url.Parse(remote)
	if err != nil {
		t.Fatal(err)
	}
	index := strings.Index(parsed.Path, "/media/")
	if index < 0 {
		t.Fatalf("carrier stream URL has no media path: %s", remote)
	}
	return sc.URL() + parsed.Path[index:]
}

func dialTier2WS(t *testing.T, endpoint string) net.Conn {
	t.Helper()
	conn, buffered, _, err := ws.Dial(t.Context(), "ws"+strings.TrimPrefix(endpoint, "http"))
	if err != nil {
		t.Fatalf("dial %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if buffered != nil {
		return hijackedConn{Conn: conn, reader: buffered}
	}
	return conn
}

func readTier2Binary(t *testing.T, conn net.Conn, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_ = conn.SetReadDeadline(deadline)
		data, op, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatalf("read binary WebSocket frame: %v", err)
		}
		if op == ws.OpBinary {
			return data
		}
	}
}

func readTier2CarrierAudio(t *testing.T, conn net.Conn, timeout time.Duration) []int16 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_ = conn.SetReadDeadline(deadline)
		data, op, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatalf("read carrier audio: %v", err)
		}
		if op != ws.OpText {
			continue
		}
		var frame carrierMediaFrame
		if json.Unmarshal(data, &frame) != nil || frame.Event != "media" || frame.Media == nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(frame.Media.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return bytesToPCM16(raw)
	}
}

func tier2CallList(t *testing.T, sc *tk.Sidecar) []map[string]any {
	t.Helper()
	var response struct {
		Calls []map[string]any `json:"calls"`
	}
	status, body := tier2Request(t, sc, http.MethodGet, "/calls?project_id="+tier2Project, nil, &response, tier2Headers())
	if status != http.StatusOK {
		t.Fatalf("list calls: %d %s", status, body)
	}
	return response.Calls
}

func waitTier2CallStatus(t *testing.T, sc *tk.Sidecar, callID, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, call := range tier2CallList(t, sc) {
			if call["id"] == callID && call["status"] == status {
				return call
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("call %s did not reach %s; calls=%v", callID, status, tier2CallList(t, sc))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestTier2HumanBrowserCallEndToEnd(t *testing.T) {
	gateway := newTier2PlatformGateway(t)
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(tier2Project),
		tk.WithEnv("APTEVA_GATEWAY_URL", gateway.server.URL))

	created := tier2MCPAs(t, sc, "telephony_routes_create", map[string]any{
		"phone_number": tier2Number, "answer_mode": "human_browser", "recording_mode": "always",
	})
	route, _ := created["route"].(map[string]any)
	routeID, _ := route["id"].(string)
	inboundURL, _ := created["inbound_url"].(string)
	if routeID == "" || inboundURL == "" {
		t.Fatalf("create route=%v", created)
	}
	configured := tier2MCPAs(t, sc, "telephony_routes_configure_carrier", map[string]any{"route_id": routeID})
	if configured["ok"] != true {
		t.Fatalf("configure route=%v", configured)
	}

	incomingBody, _ := json.Marshal(map[string]any{"data": map[string]any{
		"id": "event-incoming-1", "event_type": "call.initiated", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"call_control_id": "call-control-test-1", "connection_id": "application-test-1",
			"direction": "incoming", "from": tier2Caller, "to": tier2Number,
		},
	}})
	resp := tier2SignedPOST(t, gateway, localSidecarURL(t, sc, inboundURL), incomingBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("incoming webhook=%d %s", resp.StatusCode, body)
	}

	calls := tier2CallList(t, sc)
	if len(calls) != 1 || calls[0]["status"] != "pending" {
		t.Fatalf("pending calls=%v", calls)
	}
	callID, _ := calls[0]["id"].(string)
	var session softphoneSession
	answerStatus, answerBody := tier2Request(t, sc, http.MethodPost,
		"/softphone/answer/"+callID+"?project_id="+tier2Project, map[string]any{}, &session, tier2Headers())
	if answerStatus != http.StatusOK || session.SessionToken == "" {
		t.Fatalf("answer=%d %s session=%+v", answerStatus, answerBody, session)
	}

	browser := dialTier2WS(t, sc.URL()+"/softphone/media/"+callID+"/"+session.SessionToken)
	readSoftphoneEventWithin(t, browser, "ready", 5*time.Second)
	answerCall := gateway.waitCarrierTool(t, "answer_call")
	streamURL, _ := answerCall.Input["stream_url"].(string)
	callbackURL, _ := answerCall.Input["webhook_url"].(string)
	if streamURL == "" || callbackURL == "" || answerCall.Input["record"] != "record-from-answer" {
		t.Fatalf("answer_call input=%v", answerCall.Input)
	}
	waitTier2CallStatus(t, sc, callID, "answered")

	carrierEndpoint := rawSidecarMediaURL(t, sc, streamURL)
	carrier := dialTier2WS(t, carrierEndpoint)
	start, _ := json.Marshal(map[string]any{
		"event": "start", "stream_id": "stream-test-1",
		"start": map[string]any{"call_control_id": "call-control-test-1", "stream_id": "stream-test-1"},
	})
	if err := wsutil.WriteClientText(carrier, start); err != nil {
		t.Fatal(err)
	}
	readSoftphoneEventWithin(t, browser, "peer.connected", 5*time.Second)

	// Confirm that the start frame has activated the carrier writer before
	// exercising the opposite direction. This also prevents the test from
	// mistaking a carrier protocol failure for a browser playback failure.
	// Two 20 ms browser chunks cover the resampler's filter delay and produce
	// at least one complete 20 ms Telnyx packet.
	probePCM := sinePCM(24000, 900, 960)
	if err := wsutil.WriteClientBinary(browser, pcm16ToBytes(probePCM)); err != nil {
		t.Fatal(err)
	}
	if probe := readTier2CarrierAudio(t, carrier, 5*time.Second); len(probe) == 0 || rmsPCM(probe) < 3000 {
		t.Fatalf("browser->carrier audio degraded: samples=%d rms=%.1f", len(probe), rmsPCM(probe))
	}

	// Carrier -> browser through the real Telnyx L16 decoder, resampler,
	// frontend, loopback peer, and softphone hub.
	callerPCM := sinePCM(16000, 440, 3200)
	for offset := 0; offset < len(callerPCM); offset += 320 {
		payload := base64.StdEncoding.EncodeToString(pcm16ToBytes(callerPCM[offset : offset+320]))
		frame, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]string{"payload": payload}})
		if err := wsutil.WriteClientText(carrier, frame); err != nil {
			t.Fatal(err)
		}
		// Send at carrier cadence: bursting 200ms instantaneously is an overload
		// scenario and intentionally exercises the browser's drop-oldest queue.
		time.Sleep(20 * time.Millisecond)
	}
	var browserPCM []int16
	// A streaming band-limited resampler retains its filter tail for the next
	// packet rather than inventing samples at this artificial test boundary.
	for len(browserPCM) < 4500 {
		browserPCM = append(browserPCM, bytesToPCM16(readTier2Binary(t, browser, 5*time.Second))...)
	}
	if rmsPCM(browserPCM) < 3000 || longestNearZeroRun(browserPCM, 8) >= 240 {
		t.Fatalf("carrier->browser audio degraded: samples=%d rms=%.1f gap=%d", len(browserPCM), rmsPCM(browserPCM), longestNearZeroRun(browserPCM, 8))
	}

	diagnostics := []byte(`{"type":"diagnostics","diagnostics":{"rtt_ms":42,"playback_queue_ms":60,"playback_target_ms":60,"playback_underruns":0,"playback_dropped_ms":0,"audio_context_rate":24000,"microphone_sample_rate":48000,"microphone_channel_count":1,"echo_cancellation":true,"noise_suppression":false,"auto_gain_control":false,"mic_active_rms_dbfs":-19.5,"mic_peak_dbfs":-3.0}}`)
	if err := wsutil.WriteClientText(browser, diagnostics); err != nil {
		t.Fatal(err)
	}

	// A carrier media reconnect must keep the browser attached and restore
	// bidirectional audio rather than ending the call.
	if err := carrier.Close(); err != nil {
		t.Fatal(err)
	}
	readSoftphoneEventWithin(t, browser, "peer.disconnected", 5*time.Second)
	carrier = dialTier2WS(t, carrierEndpoint)
	if err := wsutil.WriteClientText(carrier, start); err != nil {
		t.Fatal(err)
	}
	readSoftphoneEventWithin(t, browser, "peer.connected", 5*time.Second)
	reconnectedCaller := sinePCM(16000, 660, 640)
	for offset := 0; offset < len(reconnectedCaller); offset += 320 {
		payload := base64.StdEncoding.EncodeToString(pcm16ToBytes(reconnectedCaller[offset : offset+320]))
		frame, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]string{"payload": payload}})
		if err := wsutil.WriteClientText(carrier, frame); err != nil {
			t.Fatal(err)
		}
	}
	if got := bytesToPCM16(readTier2Binary(t, browser, 5*time.Second)); len(got) == 0 || rmsPCM(got) < 3000 {
		t.Fatalf("carrier->browser audio did not resume: samples=%d rms=%.1f", len(got), rmsPCM(got))
	}
	reconnectedMic := sinePCM(24000, 1100, 960)
	if err := wsutil.WriteClientBinary(browser, pcm16ToBytes(reconnectedMic)); err != nil {
		t.Fatal(err)
	}
	if got := readTier2CarrierAudio(t, carrier, 5*time.Second); len(got) == 0 || rmsPCM(got) < 3000 {
		t.Fatalf("browser->carrier audio did not resume: samples=%d rms=%.1f", len(got), rmsPCM(got))
	}

	recordingBody, _ := json.Marshal(map[string]any{"data": map[string]any{
		"event_type": "call.recording.saved", "payload": map[string]any{
			"call_control_id": "call-control-test-1", "recording_id": "recording-test-1", "format": "wav",
			"channels": "dual", "recording_started_at": "2026-08-21T10:00:00Z", "recording_ended_at": "2026-08-21T10:00:03Z",
		},
	}})
	resp = tier2SignedPOST(t, gateway, localSidecarURL(t, sc, callbackURL), recordingBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("recording callback=%d %s", resp.StatusCode, body)
	}
	recordings := tier2MCPAs(t, sc, "telephony_recordings_list", map[string]any{"call_id": callID})
	if list, _ := recordings["recordings"].([]any); len(list) != 1 {
		t.Fatalf("recordings=%v", recordings)
	}
	gateway.waitEvent(t, "recording.ready")

	hangupBody, _ := json.Marshal(map[string]any{"data": map[string]any{
		"id": "event-hangup-1", "event_type": "call.hangup", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{
			"call_control_id": "call-control-test-1", "hangup_cause": "normal_clearing",
			"hangup_source": "callee", "sip_hangup_cause": "200",
		},
	}})
	resp = tier2SignedPOST(t, gateway, localSidecarURL(t, sc, callbackURL), hangupBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("hangup callback=%d %s", resp.StatusCode, body)
	}
	completed := waitTier2CallStatus(t, sc, callID, "completed")
	if completed["browser_audio_diagnostics"] != nil {
		t.Fatal("list should omit detailed audio history")
	}
	var detail struct {
		Calls []map[string]any `json:"calls"`
	}
	status, body := tier2Request(t, sc, http.MethodGet, "/calls?project_id="+tier2Project+"&call_id="+callID, nil, &detail, tier2Headers())
	if status != http.StatusOK || len(detail.Calls) != 1 {
		t.Fatalf("call detail=%d %s", status, body)
	}
	completed = detail.Calls[0]
	if diagnostics, _ := completed["browser_audio_diagnostics"].(map[string]any); diagnostics["auto_gain_control"] != false || diagnostics["rtt_ms"] != float64(42) {
		t.Fatalf("persisted browser diagnostics=%v", completed["browser_audio_diagnostics"])
	}
	events := tier2MCPAs(t, sc, "telephony_call_events_list", map[string]any{"call_id": callID})
	if !strings.Contains(fmt.Sprint(events), "completed") || !strings.Contains(fmt.Sprint(events), "answered") {
		t.Fatalf("lifecycle events=%v", events)
	}
	gateway.waitEvent(t, "call.completed")
}
