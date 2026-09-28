//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Real sidecar, scheduler, SQLite migrations and SDK HTTP errors. The AI service
// and Telnyx are local doubles, so this never calls production or a phone number.
func TestTier2AIHandoffFailureRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		status   int
		attempts int32
	}{
		{"permanent_configuration", 400, 1}, {"temporary_exhausted", 503, 3},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			gateway := newTier2PlatformGateway(t)
			var spawns atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/callback/threads/spawn-realtime":
					spawns.Add(1)
					http.Error(w, fmt.Sprintf("spawn realtime thread %q: HTTP %d startup unavailable", "test", scenario.status), 502)
				case "/api/apps/callback/apps/functions/call":
					var req struct {
						Input map[string]any `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						http.Error(w, "invalid JSON", 400)
						return
					}
					event := req.Input["event"].(map[string]any)
					response, _ := json.Marshal(decisionResponse{DecisionID: event["decision_id"].(string), Action: "exhausted"})
					inner, _ := json.Marshal(map[string]any{"status": "ok", "response": string(response)})
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
				{ID: "choose", Type: "decision", Config: map[string]any{"function_id": 42, "timeout_ms": 5000, "destination_ids": []string{"adviser"}}, Branches: map[string]string{"fallback": "ai"}},
				{ID: "ai", Type: "destination", Config: map[string]any{"destination_id": "ai"}},
			}}})
			if out := request("POST", "/routing/flows/publish", map[string]any{"id": flow["id"]}); out["valid"] != true {
				t.Fatal(out)
			}
			request("POST", "/routing/flows/numbers/assign", map[string]any{"flow_id": flow["id"], "route_ids": []any{route["id"]}})
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "ai-inbound", "event_type": "call.initiated", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "payload": map[string]any{"call_control_id": "ai-call", "connection_id": "application-test-1", "direction": "incoming", "from": tier2Caller, "to": tier2Number}}})
			for range 20 {
				resp := tier2SignedPOST(t, gateway, localSidecarURL(t, sc, created["inbound_url"].(string)), body)
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 204 {
					t.Fatalf("ingress: %d %s", resp.StatusCode, raw)
				}
			}
			calls := tier2CallList(t, sc)
			if len(calls) != 1 {
				t.Fatalf("calls=%v", calls)
			}
			id := calls[0]["id"].(string)
			var finished map[string]any
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				call := request("GET", "/calls/"+id, nil)["call"].(map[string]any)
				if call["call_classification"] == "ai_startup_failed" {
					finished = call
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if finished == nil {
				t.Fatal("startup failure did not finish the caller leg")
			}
			if spawns.Load() != scenario.attempts {
				t.Fatalf("spawns=%d want=%d", spawns.Load(), scenario.attempts)
			}
			startup := finished["ai_startup"].(map[string]any)
			if startup["attempt_count"] != float64(scenario.attempts) || startup["fallback_applied"] != true {
				t.Fatal(startup)
			}
			if finished["missed_pool_eligible"] != true || finished["callback_opportunity_id"] != "callback:"+id {
				t.Fatal(finished)
			}
			command := gateway.waitCarrierTool(t, "reject_call")
			if command.Input["cause"] != "CALL_REJECTED" {
				t.Fatalf("rejection=%v", command)
			}
			decisions := tier2MCPAs(t, sc, "telephony_decisions_list", map[string]any{"call_id": id})["decisions"].([]any)
			if len(decisions) != 1 || decisions[0].(map[string]any)["applied"] != true {
				t.Fatalf("decision not retired: %v", decisions)
			}
		})
	}
}
