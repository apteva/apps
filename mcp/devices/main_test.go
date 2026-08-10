package main

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type mqttCall struct {
	Tool  string
	Input map[string]any
}

type mqttStub struct {
	tk.BasePlatformClient
	mu    sync.Mutex
	calls []mqttCall
}

func (s *mqttStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "mqtt" {
		return errors.New("unexpected app: " + app)
	}
	s.mu.Lock()
	copyInput := cloneMap(input)
	s.calls = append(s.calls, mqttCall{Tool: tool, Input: copyInput})
	s.mu.Unlock()
	var value any
	switch tool {
	case "mqtt_status":
		value = map[string]any{"endpoint": "mqtt://broker.test:1883", "advertised_host": "broker.test", "advertised_port": 1883, "tls": false}
	case "mqtt_subscribe_ensure":
		value = map[string]any{"id": 1, "topic_pattern": "devices/+/+", "bus_topic": "devices.message"}
	case "mqtt_clients_list", "mqtt_retained_list":
		value = []map[string]any{}
	default:
		value = map[string]any{"ok": true}
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}

func (s *mqttStub) callsFor(tool string) []mqttCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []mqttCall{}
	for _, call := range s.calls {
		if call.Tool == tool {
			out = append(out, call)
		}
	}
	return out
}

func newTestApp(t *testing.T) (*App, *mqttStub, *tk.EmitRecorder) {
	t.Helper()
	platform := &mqttStub{}
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-test"), tk.WithPlatform(platform), tk.WithEmitter(recorder))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return app, platform, recorder
}

func TestManifestAndRuntimeSurfaceMatch(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "devices" || m.Version != "0.1.1" || len(m.Scopes) != 1 || m.Scopes[0] != sdk.ScopeProject {
		t.Fatalf("unexpected manifest identity/scope: %#v", m)
	}
	if len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "mqtt" || m.Requires.Apps[0].Version != ">=0.3.0" {
		t.Fatalf("MQTT dependency = %#v", m.Requires.Apps)
	}
	if !reflect.DeepEqual(m.Requires.Apps[0].Events, []string{"mqtt.devices.message", "mqtt.client.connected", "mqtt.client.disconnected"}) {
		t.Fatalf("MQTT events = %#v", m.Requires.Apps[0].Events)
	}
	declared := map[string]bool{}
	for _, entry := range m.Provides.MCPTools {
		declared[entry.Name] = true
	}
	for _, runtime := range (&App{}).MCPTools() {
		if !declared[runtime.Name] {
			t.Errorf("runtime tool %q missing from manifest", runtime.Name)
		}
		delete(declared, runtime.Name)
	}
	if len(declared) > 0 {
		t.Fatalf("manifest-only tools: %#v", declared)
	}
	for _, route := range (&App{}).HTTPRoutes() {
		if route.NoAuth {
			t.Errorf("management route %s must be authenticated", route.Pattern)
		}
	}
}

func TestProvisionUsesNativeProtocol(t *testing.T) {
	app, _, _ := newTestApp(t)
	response, err := app.provision(map[string]any{"name": "Native board", "device_id": "native-board"})
	if err != nil {
		t.Fatal(err)
	}
	device, ok := response["device"].(Device)
	if !ok || device.Protocol != "apteva.devices/v1" {
		t.Fatalf("provisioned device = %#v", response["device"])
	}
	if len(response) != 4 {
		t.Fatalf("unexpected enrollment fields: %#v", response)
	}
	if _, err := app.provision(map[string]any{"name": "Unsupported board", "device_id": "unsupported-board", "protocol": "legacy/v1"}); err == nil {
		t.Fatal("unsupported protocol was accepted")
	}
}

func TestProvisionCreatesExactACLAndOneTimeEnrollment(t *testing.T) {
	app, platform, recorder := newTestApp(t)
	result, err := app.provision(map[string]any{
		"name": "Greenhouse", "device_id": "greenhouse-1",
		"capabilities": map[string]any{"pins": []any{map[string]any{"name": "led", "number": 2.0, "type": "digital", "writable": true, "modes": []any{"input", "output"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mqtt := result["mqtt"].(map[string]any)
	if mqtt["username"] != "greenhouse-1" || mqtt["password"] == "" || mqtt["endpoint"] != "mqtt://broker.test:1883" {
		t.Fatalf("enrollment = %#v", result)
	}
	calls := platform.callsFor("mqtt_users_create")
	if len(calls) != 1 {
		t.Fatalf("create calls = %#v", calls)
	}
	wantPublish := []string{
		"devices/greenhouse-1/response", "devices/greenhouse-1/telemetry", "devices/greenhouse-1/state",
		"devices/greenhouse-1/manifest", "devices/greenhouse-1/availability",
	}
	gotPublish := toStrings(calls[0].Input["allow_publish"])
	if !reflect.DeepEqual(gotPublish, wantPublish) {
		t.Fatalf("publish ACL = %#v", gotPublish)
	}
	if got := toStrings(calls[0].Input["allow_subscribe"]); !reflect.DeepEqual(got, []string{"devices/greenhouse-1/commands"}) {
		t.Fatalf("subscribe ACL = %#v", got)
	}
	if len(recorder.EventsByTopic("devices.device.created")) != 1 {
		t.Fatal("missing device.created event")
	}
	var passwordColumns int
	if err := app.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name LIKE '%password%' OR name LIKE '%secret%'`).Scan(&passwordColumns); err != nil || passwordColumns != 0 {
		t.Fatalf("plaintext credential column count=%d err=%v", passwordColumns, err)
	}
}

func TestIdentityEnforcementAndStateIngestion(t *testing.T) {
	app, _, recorder := newTestApp(t)
	if _, err := app.provision(map[string]any{"name": "Sensor", "device_id": "sensor-1"}); err != nil {
		t.Fatal(err)
	}
	spoof := sdk.Event{ProjectID: "project-test", Data: map[string]any{
		"topic": "devices/sensor-1/state", "username": "another-device", "payload": `{"temperature":99}`,
	}}
	if err := app.handleMQTTMessage(app.ctx, spoof); err != nil {
		t.Fatal(err)
	}
	values, _ := listState(app.ctx.AppDB(), "sensor-1", "")
	if len(values) != 0 {
		t.Fatalf("spoofed state accepted: %#v", values)
	}
	valid := spoof
	valid.Data = cloneMap(spoof.Data)
	valid.Data["username"] = "sensor-1"
	valid.Data["payload"] = `{"temperature":23.5,"relay":true}`
	if err := app.handleMQTTMessage(app.ctx, valid); err != nil {
		t.Fatal(err)
	}
	values, _ = listState(app.ctx.AppDB(), "sensor-1", "")
	if len(values) != 2 {
		t.Fatalf("valid state = %#v", values)
	}
	if len(recorder.EventsByTopic("devices.state.changed")) != 1 {
		t.Fatal("missing state.changed event")
	}
}

func TestCommandEnvelopeAndResponseCorrelation(t *testing.T) {
	app, platform, recorder := newTestApp(t)
	if _, err := app.provision(map[string]any{"name": "Controller", "device_id": "controller-1"}); err != nil {
		t.Fatal(err)
	}
	d, _ := getDevice(app.ctx.AppDB(), app.projectID, "controller-1", false)
	c, err := app.sendCommand(d, "function.call", "set_led", map[string]any{"value": 1.0}, false, 5000, "turn-led-on")
	if err != nil {
		t.Fatal(err)
	}
	publishes := platform.callsFor("mqtt_publish")
	if len(publishes) != 1 {
		t.Fatalf("publish calls = %#v", publishes)
	}
	input := publishes[0].Input
	if input["topic"] != "devices/controller-1/commands" || input["qos"] != 1 {
		t.Fatalf("publish input = %#v", input)
	}
	payload := input["payload"].(map[string]any)
	if payload["command_id"] != c.ID || payload["type"] != "function" || payload["operation"] != "function.call" {
		t.Fatalf("wire payload = %#v", payload)
	}
	response, _ := json.Marshal(map[string]any{"command_id": c.ID, "device_id": d.ID, "success": true, "result": map[string]any{"return_value": 1}})
	if err := app.handleMQTTMessage(app.ctx, sdk.Event{ProjectID: app.projectID, Data: map[string]any{
		"topic": "devices/controller-1/response", "username": "controller-1", "payload": string(response),
	}}); err != nil {
		t.Fatal(err)
	}
	completed, err := getCommand(app.ctx.AppDB(), c.ID)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("completed command = %#v, %v", completed, err)
	}
	if len(recorder.EventsByTopic("devices.command.completed")) != 1 {
		t.Fatal("missing command.completed event")
	}
	again, err := app.sendCommand(d, "function.call", "set_led", map[string]any{"value": 1.0}, false, 5000, "turn-led-on")
	if err != nil || again.ID != c.ID || len(platform.callsFor("mqtt_publish")) != 1 {
		t.Fatalf("idempotent command=%#v err=%v publishes=%d", again, err, len(platform.callsFor("mqtt_publish")))
	}
}

func TestGPIORequiresAllowlist(t *testing.T) {
	app, _, _ := newTestApp(t)
	capabilities := map[string]any{
		"pins": []any{
			map[string]any{
				"name": "led", "number": 2.0, "type": "digital", "writable": true,
				"modes": []any{"input", "output"},
			},
		},
	}
	if _, err := app.provision(map[string]any{
		"name": "Board", "device_id": "board-1", "capabilities": capabilities,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPinWrite(app.ctx, map[string]any{"device_id": "board-1", "pin": 13.0, "value": 1.0, "wait": false}); err == nil {
		t.Fatal("unlisted pin accepted")
	}
	d, _ := getDevice(app.ctx.AppDB(), app.projectID, "board-1", false)
	if _, err := app.sendCommand(d, "pin.write", "13", map[string]any{"pin": 13.0, "value": 1.0, "kind": "digital"}, false, 5000, ""); err == nil {
		t.Fatal("generic command bypassed the GPIO allowlist")
	}
	if _, err := app.toolPinWrite(app.ctx, map[string]any{"device_id": "board-1", "pin": "led", "value": 1.0, "wait": false}); err != nil {
		t.Fatalf("allowlisted pin rejected: %v", err)
	}
}

func TestReplacementSessionIgnoresStaleDisconnect(t *testing.T) {
	app, _, recorder := newTestApp(t)
	if _, err := app.provision(map[string]any{"name": "Board", "device_id": "board-2"}); err != nil {
		t.Fatal(err)
	}
	connect := func(clientID string) error {
		return app.handleMQTTConnected(app.ctx, sdk.Event{ProjectID: app.projectID, Data: map[string]any{
			"username": "board-2", "client_id": clientID, "connected_at": nowText(),
		}})
	}
	if err := connect("old-session"); err != nil {
		t.Fatal(err)
	}
	if err := connect("new-session"); err != nil {
		t.Fatal(err)
	}
	recorder.Reset()
	if err := app.handleMQTTDisconnected(app.ctx, sdk.Event{ProjectID: app.projectID, Data: map[string]any{
		"username": "board-2", "client_id": "old-session", "disconnected_at": nowText(),
	}}); err != nil {
		t.Fatal(err)
	}
	d, _ := getDevice(app.ctx.AppDB(), app.projectID, "board-2", false)
	if d.Status != "online" || d.MQTTClientID != "new-session" {
		t.Fatalf("stale disconnect changed replacement session: %#v", d)
	}
	if len(recorder.EventsByTopic("devices.device.disconnected")) != 0 {
		t.Fatal("stale disconnect emitted a lifecycle event")
	}
}

func TestMalformedPayloadIsRecordedWithoutRetryError(t *testing.T) {
	app, _, _ := newTestApp(t)
	if _, err := app.provision(map[string]any{"name": "Sensor", "device_id": "sensor-2"}); err != nil {
		t.Fatal(err)
	}
	err := app.handleMQTTMessage(app.ctx, sdk.Event{ProjectID: app.projectID, Data: map[string]any{
		"topic": "devices/sensor-2/state", "username": "sensor-2", "payload": `{not-json`,
	}})
	if err != nil {
		t.Fatalf("malformed device input should be rejected without retrying the platform event: %v", err)
	}
	var rejected int
	if err := app.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_events WHERE device_id='sensor-2' AND kind='payload.rejected'`).Scan(&rejected); err != nil || rejected != 1 {
		t.Fatalf("rejected payload audit rows=%d err=%v", rejected, err)
	}
	if got := parseAvailability(`"online"`); got != "online" {
		t.Fatalf("JSON availability = %q", got)
	}
}

func toStrings(value any) []string {
	items, _ := value.([]string)
	if items != nil {
		return items
	}
	raw, _ := value.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(string))
	}
	return out
}
