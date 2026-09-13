//go:build integration

package main

import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Real compiled Telephony, migrations, scheduler, MCP, signed Telnyx ingress and
// SDK HTTP transport. Only Functions/business rules and the carrier are fixtures.
func TestTier2RoutingDecision(t *testing.T) {
	gateway := newTier2PlatformGateway(t)
	var invokes atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/functions/call" {
			gateway.serveHTTP(w, r)
			return
		}
		var request struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Tool != "functions_invoke" || request.Input["_project_id"] != tier2Project {
			http.Error(w, "bad invocation scope", 400)
			return
		}
		invokes.Add(1)
		event, ok := request.Input["event"].(map[string]any)
		if !ok {
			http.Error(w, "missing event", 400)
			return
		}
		response, _ := json.Marshal(decisionResponse{DecisionID: event["decision_id"].(string), Action: "offer", DestinationID: "individual", ReservationID: "business-reservation"})
		inner, _ := json.Marshal(map[string]any{"status": "ok", "response": string(response)})
		writeTier2JSON(w, map[string]any{"jsonrpc": "2.0", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(inner)}}}})
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
			t.Fatalf("%s %d %s", path, status, raw)
		}
		return out
	}
	identity := phoneTestIdentity("alice")
	request("POST", "/routing/destinations/save", map[string]any{"id": "individual", "name": "Individual", "kind": "browser", "config": map[string]any{"capacity": destinationCapacity{Identity: identity, Limit: 1}}})
	request("PUT", "/access/policy", phonePolicy{Users: []phoneUser{{Identity: identity, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"individual"}}}}})
	flow := request("POST", "/routing/flows/save", map[string]any{"name": "Decision test", "draft": routingDefinition{Entry: "choose", Nodes: []routingNode{{ID: "choose", Type: "decision", Config: map[string]any{"function_id": 42, "timeout_ms": 5000, "destination_ids": []string{"individual"}}, Branches: map[string]string{"fallback": "end"}}, {ID: "end", Type: "hangup"}}}})
	if out := request("POST", "/routing/flows/publish", map[string]any{"id": flow["id"]}); out["valid"] != true {
		t.Fatal(out)
	}
	request("POST", "/routing/flows/numbers/assign", map[string]any{"flow_id": flow["id"], "route_ids": []any{route["id"]}})
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "decision-inbound", "event_type": "call.initiated", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "payload": map[string]any{"call_control_id": "decision-call", "connection_id": "application-test-1", "direction": "incoming", "from": tier2Caller, "to": tier2Number}}})
	for range 2 {
		response := tier2SignedPOST(t, gateway, localSidecarURL(t, sc, created["inbound_url"].(string)), body)
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatalf("ingress %d %s", response.StatusCode, raw)
		}
	}
	calls := tier2CallList(t, sc)
	if len(calls) != 1 {
		t.Fatal(calls)
	}
	id := calls[0]["id"].(string)
	until := time.Now().Add(8 * time.Second)
	accepted := false
	for time.Now().Before(until) {
		out := tier2MCPAs(t, sc, "telephony_decisions_list", map[string]any{"call_id": id})
		ds := out["decisions"].([]any)
		if len(ds) == 1 {
			d := ds[0].(map[string]any)
			if d["status"] == "accepted" {
				if d["result"].(map[string]any)["reservation_id"] != "business-reservation" {
					t.Fatal(d)
				}
				accepted = true
				break
			}
			if d["status"] != "pending" && d["status"] != "running" {
				t.Fatal(d)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !accepted || invokes.Load() != 1 {
		t.Fatalf("accepted=%t invokes=%d", accepted, invokes.Load())
	}
	var session map[string]any
	status, raw := tier2Request(t, sc, "POST", "/softphone/answer/"+id+"?project_id="+tier2Project, map[string]any{"call_id": id, "destination_id": "individual"}, &session, tier2Headers())
	if status != 200 {
		t.Fatalf("answer %d %s", status, raw)
	}
	if session["call_id"] != id || session["media_url"] == "" {
		t.Fatal(session)
	}
}
