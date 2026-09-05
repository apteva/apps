//go:build integration

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws/wsutil"
)

// Full compiled sidecar, signed provider callbacks, independent outbound legs,
// parent answer/cancellation, and both actual Telnyx audio bridge directions.
func TestTier2RingGroupPhoneCallsEndToEnd(t *testing.T) {
	gateway := newTier2PlatformGateway(t)
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(tier2Project), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.server.URL))
	created := tier2MCPAs(t, sc, "telephony_routes_create", map[string]any{"phone_number": tier2Number, "answer_mode": "human_browser", "recording_mode": "off"})
	route := created["route"].(map[string]any)
	routeID := route["id"].(string)
	inboundURL := created["inbound_url"].(string)
	if configured := tier2MCPAs(t, sc, "telephony_routes_configure_carrier", map[string]any{"route_id": routeID}); configured["ok"] != true {
		t.Fatal(configured)
	}
	post := func(path string, body any) map[string]any {
		t.Helper()
		var out map[string]any
		status, raw := tier2Request(t, sc, http.MethodPost, path+"?project_id="+tier2Project, body, &out, tier2Headers())
		if status != 200 {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		return out
	}
	members := []any{}
	for i := 0; i < 3; i++ {
		dest := post("/routing/destinations/save", map[string]any{"name": fmt.Sprint("Phone ", i), "kind": "pstn", "config": map[string]any{"phone_number": fmt.Sprintf("+1202555010%d", i)}, "enabled": true})
		members = append(members, map[string]any{"destination_id": dest["id"], "enabled": true})
	}
	group := post("/routing/ring-groups/save", map[string]any{"name": "Phone team", "strategy": "simultaneous", "timeout_sec": 30, "members": members})
	flow := post("/routing/flows/save", map[string]any{"name": "Ring phones", "draft": map[string]any{"entry": "team", "nodes": []any{map[string]any{"id": "team", "type": "ring_group", "config": map[string]any{"ring_group_id": group["id"]}}}}})
	if published := post("/routing/flows/publish", map[string]any{"id": flow["id"]}); published["valid"] != true {
		t.Fatal(published)
	}
	if assigned := post("/routing/flows/numbers/assign", map[string]any{"flow_id": flow["id"], "route_ids": []string{routeID}}); assigned["valid"] != true {
		t.Fatal(assigned)
	}
	event := func(endpoint, kind, sid, eventID string, extra map[string]any) {
		t.Helper()
		payload := map[string]any{"call_control_id": sid}
		for key, value := range extra {
			payload[key] = value
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"id": eventID, "event_type": kind, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "payload": payload}})
		resp := tier2SignedPOST(t, gateway, localSidecarURL(t, sc, endpoint), body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s callback=%d %s", kind, resp.StatusCode, raw)
		}
	}
	event(inboundURL, "call.initiated", "call-control-test-1", "ring-incoming", map[string]any{"connection_id": "application-test-1", "direction": "incoming", "from": tier2Caller, "to": tier2Number})
	calls := tier2CallList(t, sc)
	if len(calls) != 1 {
		t.Fatal(calls)
	}
	parentID := calls[0]["id"].(string)
	dials := []tier2CarrierCall{}
	for i := 0; i < 3; i++ {
		dials = append(dials, gateway.waitCarrierTool(t, "dial_call"))
	}
	winner := dials[1]
	winnerSID := "ring:" + fmt.Sprint(winner.Input["to"])
	event(winner.Input["webhook_url"].(string), "call.answered", winnerSID, "ring-winner", nil)
	parentAnswer := gateway.waitCarrierTool(t, "answer_call")
	waitTier2CallStatus(t, sc, parentID, "answered")
	canceled := map[string]bool{}
	for i := 0; i < 2; i++ {
		call := gateway.waitCarrierTool(t, "hangup_call")
		canceled[fmt.Sprint(call.Input["call_control_id"])] = true
	}
	if canceled[winnerSID] || len(canceled) != 2 {
		t.Fatalf("incorrect losers %v winner=%s", canceled, winnerSID)
	}
	openCarrier := func(streamURL, sid, name string) net.Conn {
		t.Helper()
		conn := dialTier2WS(t, rawSidecarMediaURL(t, sc, streamURL))
		frame, _ := json.Marshal(map[string]any{"event": "start", "stream_id": name, "start": map[string]any{"call_control_id": sid, "stream_id": name}})
		if err := wsutil.WriteClientText(conn, frame); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	caller := openCarrier(parentAnswer.Input["stream_url"].(string), "call-control-test-1", "parent-stream")
	callee := openCarrier(winner.Input["stream_url"].(string), winnerSID, "winner-stream")
	sendVoice := func(conn net.Conn, frequency int) {
		t.Helper()
		pcm := sinePCM(16000, frequency, 8000)
		for offset := 0; offset < len(pcm); offset += 320 {
			raw, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]any{"payload": base64.StdEncoding.EncodeToString(pcm16ToBytes(pcm[offset : offset+320]))}})
			if err := wsutil.WriteClientText(conn, raw); err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	sendVoice(caller, 440)
	if samples := readTier2CarrierAudio(t, callee, 5*time.Second); len(samples) == 0 || rmsPCM(samples) < 2000 {
		t.Fatalf("caller->callee degraded: %d samples rms %.1f", len(samples), rmsPCM(samples))
	}
	sendVoice(callee, 880)
	if samples := readTier2CarrierAudio(t, caller, 5*time.Second); len(samples) == 0 || rmsPCM(samples) < 2000 {
		t.Fatalf("callee->caller degraded: %d samples rms %.1f", len(samples), rmsPCM(samples))
	}
	event(winner.Input["webhook_url"].(string), "call.hangup", winnerSID, "ring-winner-ended", map[string]any{"hangup_cause": "normal_clearing", "hangup_source": "callee", "sip_hangup_cause": "200"})
	waitTier2CallStatus(t, sc, parentID, "completed")
}
