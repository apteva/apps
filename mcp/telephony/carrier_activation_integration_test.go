//go:build integration

package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws/wsutil"
)

// All endpoints are loopback doubles. Exercises a real compiled sidecar,
// exhausted human offer, signed carrier events, and bidirectional AI audio.
func TestTier2AIHandoffCarrierActivation(t *testing.T) {
	gateway := newTier2PlatformGateway(t)
	coreBridge, coreServer := newFakeCoreAudioBridge(t)
	var spawns atomic.Int32
	var decisions atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/callback/threads/spawn-realtime":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			spawns.Add(1)
			writeTier2JSON(w, map[string]any{"status": "created", "id": req["thread_id"], "audio_bridge_url": "ws" + strings.TrimPrefix(coreServer.URL, "http")})
		case "/api/apps/callback/apps/functions/call":
			var req struct {
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad JSON", 400)
				return
			}
			event := req.Input["event"].(map[string]any)
			response := decisionResponse{DecisionID: event["decision_id"].(string), Action: "exhausted"}
			if decisions.Add(1) == 1 {
				response.Action = "offer"
				response.DestinationID = "adviser"
			}
			raw, _ := json.Marshal(response)
			inner, _ := json.Marshal(map[string]any{"status": "ok", "response": string(raw)})
			writeTier2JSON(w, map[string]any{"jsonrpc": "2.0", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(inner)}}}})
		default:
			gateway.serveHTTP(w, r)
		}
	}))
	defer proxy.Close()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(tier2Project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL))
	created := tier2MCPAs(t, sc, "telephony_routes_create", map[string]any{"phone_number": tier2Number, "answer_mode": "human_browser", "recording_mode": "off"})
	route := created["route"].(map[string]any)
	tier2MCPAs(t, sc, "telephony_routes_configure_carrier", map[string]any{"route_id": route["id"]})
	request := func(method, path string, body any) map[string]any {
		t.Helper()
		var out map[string]any
		status, raw := tier2Request(t, sc, method, path+"?project_id="+tier2Project, body, &out, tier2Headers())
		if status != 200 {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		return out
	}
	identity := phoneTestIdentity("adviser")
	request("POST", "/routing/destinations/save", map[string]any{"id": "adviser", "name": "Adviser", "kind": "browser", "config": map[string]any{"capacity": destinationCapacity{Identity: identity, Limit: 1}}})
	request("PUT", "/access/policy", phonePolicy{Users: []phoneUser{{Identity: identity, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"adviser"}}}}})
	request("POST", "/routing/destinations/save", map[string]any{"id": "ai", "name": "AI", "kind": "ai", "config": map[string]any{"agent_id": 7, "directive": "Test only."}})
	flow := request("POST", "/routing/flows/save", map[string]any{"name": "AI recovery", "draft": routingDefinition{Entry: "choose", Nodes: []routingNode{
		{ID: "choose", Type: "decision", Config: map[string]any{"function_id": 42, "timeout_ms": 5000, "ring_timeout_seconds": 5, "max_attempts": 4, "destination_ids": []string{"adviser"}}, Branches: map[string]string{"fallback": "ai"}},
		{ID: "ai", Type: "destination", Config: map[string]any{"destination_id": "ai"}},
	}}})
	if out := request("POST", "/routing/flows/publish", map[string]any{"id": flow["id"]}); out["valid"] != true {
		t.Fatal(out)
	}
	request("POST", "/routing/flows/numbers/assign", map[string]any{"flow_id": flow["id"], "route_ids": []any{route["id"]}})

	callbackURL := localSidecarURL(t, sc, created["inbound_url"].(string))
	sendEvent := func(event string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"id": event + "-ai", "event_type": event, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "payload": map[string]any{"call_control_id": "ai-call", "connection_id": "application-test-1", "direction": "incoming", "from": tier2Caller, "to": tier2Number}}})
		resp := tier2SignedPOST(t, gateway, callbackURL, body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatalf("%s: %d %s", event, resp.StatusCode, raw)
		}
	}
	// Drain route configuration commands before checking strict call ordering.
	for len(gateway.carrierCalls) > 0 {
		<-gateway.carrierCalls
	}
	sendEvent("call.initiated")
	sendEvent("call.initiated")
	var answer tier2CarrierCall
	select {
	case answer = <-gateway.carrierCalls:
	case <-time.After(12 * time.Second):
		t.Fatal("AI did not answer after the human offer expired")
	}
	if answer.Tool != "answer_call" || answer.Input["stream_url"] != nil {
		t.Fatalf("first activation command: %+v", answer)
	}
	calls := tier2CallList(t, sc)
	if len(calls) != 1 {
		t.Fatal(calls)
	}
	id := calls[0]["id"].(string)
	detail := request("GET", "/calls/"+id, nil)["call"].(map[string]any)
	if detail["status"] != "answering" || detail["answered_at"] != "" || detail["media_connected_at"] != "" {
		t.Fatalf("premature answer/media: %v", detail)
	}
	ds := tier2MCPAs(t, sc, "telephony_decisions_list", map[string]any{"call_id": id})["decisions"].([]any)
	pending := false
	for _, value := range ds {
		d := value.(map[string]any)
		if d["applied"] == false {
			pending = true
		}
	}
	if len(ds) != 2 || !pending || decisions.Load() != 2 {
		t.Fatalf("expected offer then unapplied AI fallback: %v", ds)
	}
	// Several scheduler ticks must not replay an accepted answer or stream early.
	time.Sleep(2100 * time.Millisecond)
	select {
	case cmd := <-gateway.carrierCalls:
		t.Fatalf("command before answer confirmation: %+v", cmd)
	default:
	}
	callbackURL = localSidecarURL(t, sc, answer.Input["webhook_url"].(string))
	sendEvent("call.answered")
	sendEvent("call.answered")
	var stream tier2CarrierCall
	select {
	case stream = <-gateway.carrierCalls:
	case <-time.After(3 * time.Second):
		t.Fatal("no stream after carrier confirmation")
	}
	if stream.Tool != "start_streaming" {
		t.Fatal(stream)
	}
	carrier := dialTier2WS(t, rawSidecarMediaURL(t, sc, stream.Input["stream_url"].(string)))
	defer carrier.Close()
	start, _ := json.Marshal(map[string]any{"event": "start", "stream_id": "ai-stream", "start": map[string]any{"call_control_id": "ai-call", "stream_id": "ai-stream"}})
	if err := wsutil.WriteClientText(carrier, start); err != nil {
		t.Fatal(err)
	}
	core := waitTestConnection(t, coreBridge.conn)
	if err := wsutil.WriteServerBinary(core, pcm16ToBytes(sinePCM(24000, 900, 960))); err != nil {
		t.Fatal(err)
	}
	if audio := readTier2CarrierAudio(t, carrier, 3*time.Second); len(audio) == 0 || rmsPCM(audio) < 3000 {
		t.Fatal("AI audio did not reach carrier")
	}
	media, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(pcm16ToBytes(sinePCM(16000, 440, 320)))}})
	if err := wsutil.WriteClientText(carrier, media); err != nil {
		t.Fatal(err)
	}
	select {
	case audio := <-coreBridge.inbound:
		if rmsPCM(bytesToPCM16(audio)) < 3000 {
			t.Fatal("caller audio damaged")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller audio did not reach AI")
	}
	sendEvent("streaming.started")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		detail = request("GET", "/calls/"+id, nil)["call"].(map[string]any)
		ds = tier2MCPAs(t, sc, "telephony_decisions_list", map[string]any{"call_id": id})["decisions"].([]any)
		applied := true
		for _, value := range ds {
			if value.(map[string]any)["applied"] != true {
				applied = false
			}
		}
		if applied && detail["media_connected_at"] != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, value := range ds {
		if value.(map[string]any)["applied"] != true {
			t.Fatal("connected handoff remained unapplied")
		}
	}
	if detail["media_connected_at"] == "" || detail["call_classification"] != "ai_handled" || spawns.Load() != 1 {
		t.Fatalf("bad media result: %v spawns=%d", detail, spawns.Load())
	}
	select {
	case cmd := <-gateway.carrierCalls:
		t.Fatalf("duplicate carrier command: %+v", cmd)
	default:
	}
	sendEvent("call.hangup")
}
