package main

import (
	"bytes"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The gateway uses server-owned caller scope and event source metadata, like
// production. MQTT and Devices are real, separately built sidecars.
func TestSidecarProvisionWithRealMQTT(t *testing.T) {
	if testing.Short() {
		t.Skip("real sidecar integration")
	}
	const scope = "project-integration"
	var mu sync.RWMutex
	var broker, devices *tk.Sidecar
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		b, d := broker, devices
		mu.RUnlock()
		if r.URL.Path == "/api/apps/callback/apps/mqtt/call" {
			if b == nil {
				http.Error(w, "broker unavailable", 503)
				return
			}
			var body struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": body.Tool, "arguments": body.Input}})
			req, _ := http.NewRequestWithContext(r.Context(), "POST", b.URL()+"/mcp", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+b.Token())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Apteva-Project-ID", scope)
			req.Header.Set("X-Apteva-Bound-Caller-App-Name", "devices")
			req.Header.Set("X-Apteva-Bound-Caller-Install-ID", "2")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
		if r.URL.Path == "/api/app-events/internal/emit" {
			var body struct {
				Topic     string         `json:"topic"`
				ProjectID string         `json:"project_id"`
				Data      map[string]any `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid event", 400)
				return
			}
			if strings.HasPrefix(body.Topic, "mqtt.") && d != nil && b != nil && r.Header.Get("Authorization") == "Bearer "+b.Token() {
				raw, _ := json.Marshal(sdk.Event{Event: body.Topic, ProjectID: body.ProjectID, SourceApp: "mqtt", SourceInstallID: 1, Data: body.Data})
				req, _ := http.NewRequestWithContext(r.Context(), "POST", d.URL()+"/events", bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+d.Token())
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), 502)
					return
				}
				defer resp.Body.Close()
				w.WriteHeader(resp.StatusCode)
				_, _ = io.Copy(w, resp.Body)
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer gateway.Close()
	b := tk.SpawnSidecar(t, "../mqtt", tk.WithProjectID(scope), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithConfig(map[string]string{"listen_port": "0", "bind_interface": "127.0.0.1", "ha_discovery_enabled": "false"}))
	mu.Lock()
	broker = b
	mu.Unlock()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(scope), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	mu.Lock()
	devices = sc
	mu.Unlock()
	call := func(tool string, args map[string]any) map[string]any {
		return sc.MCPAs(tool, args, 1, "integration", scope)
	}
	enrollment := call("devices_provision", map[string]any{"name": "Integration board", "device_id": "integration-board", "protocol": "apteva.devices/v2", "idempotency_key": "one", "capabilities": map[string]any{"operations": []any{map[string]any{"name": "lamp.set"}}}})
	cred, _ := enrollment["mqtt"].(map[string]any)
	if cred == nil || cred["password"] == "" {
		t.Fatalf("enrollment=%#v", enrollment)
	}
	topics := enrollment["topics"].(map[string]any)
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + cred["host"].(string) + ":" + jsonNumberString(cred["port"])).SetUsername(cred["username"].(string)).SetPassword(cred["password"].(string)).SetClientID("integration-board-client").SetAutoReconnect(false)
	client := mqtt.NewClient(opts)
	if token := client.Connect(); !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatalf("MQTT connect: %v", token.Error())
	}
	defer client.Disconnect(100)
	receipts := make(chan string, 10)
	commands := make(chan string, 10)
	token := client.Subscribe(topics["receipts"].(string), 1, func(_ mqtt.Client, m mqtt.Message) { receipts <- string(m.Payload()) })
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatal("subscribe receipts")
	}
	token = client.Subscribe(topics["commands"].(string), 1, func(_ mqtt.Client, m mqtt.Message) {
		var wire map[string]any
		_ = json.Unmarshal(m.Payload(), &wire)
		id, _ := wire["command_id"].(string)
		commands <- id
		response, _ := json.Marshal(map[string]any{"command_id": id, "success": true, "result": map[string]any{"on": true}})
		client.Publish(topics["response"].(string), 1, false, response)
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatal("subscribe commands")
	}
	payload := dataPayload("sample-1", nowText(), map[string]any{"id": "opaque", "temperature": 23.5})
	for i := 0; i < 2; i++ {
		token = client.Publish(topics["state"].(string), 1, false, payload)
		if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
			t.Fatal("publish state")
		}
		select {
		case <-receipts:
		case <-time.After(5 * time.Second):
			t.Fatal("no application receipt")
		}
	}
	history := call("devices_data_list", map[string]any{"device_id": "integration-board"})
	if len(history["items"].([]any)) != 1 {
		t.Fatalf("history=%#v", history)
	}
	command := call("devices_command_send", map[string]any{"device_id": "integration-board", "operation": "lamp.set", "arguments": map[string]any{"on": true}, "idempotency_key": "command-1", "timeout_ms": 10000, "wait": true})
	if command["command"].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("command=%#v", command)
	}
	select {
	case <-commands:
	case <-time.After(time.Second):
		t.Fatal("command not delivered")
	}
	var out map[string]any
	response := sc.RequestWithHeaders("GET", "/devices", nil, &out, map[string]string{"X-Apteva-Project-ID": "foreign"})
	if response.Status != 403 {
		t.Fatalf("HTTP scope: %d", response.Status)
	}
	deleted := call("devices_delete", map[string]any{"device_id": "integration-board", "confirm": true, "idempotency_key": "delete-1"})
	if deleted["status"] != "completed" {
		t.Fatalf("delete=%#v", deleted)
	}
	replay := call("devices_delete", map[string]any{"device_id": "integration-board", "confirm": true, "idempotency_key": "delete-1"})
	if replay["operation_id"] != deleted["operation_id"] {
		t.Fatal("delete replay changed operation")
	}
}
func jsonNumberString(value any) string { raw, _ := json.Marshal(value); return string(raw) }
