package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
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
	mu        sync.Mutex
	calls     []mqttCall
	users     map[string]string
	failTool  string
	failAfter bool
}

func (s *mqttStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "mqtt" {
		return errors.New("unexpected app: " + app)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, mqttCall{Tool: tool, Input: cloneMap(input)})
	if s.users == nil {
		s.users = map[string]string{}
	}
	failing := s.failTool == tool
	if failing && !s.failAfter {
		s.failTool = ""
		return errors.New("injected MQTT failure")
	}
	var value any
	switch tool {
	case "mqtt_status":
		value = map[string]any{"endpoint": "mqtt://broker.test:1883", "advertised_host": "broker.test", "advertised_port": 1883, "tls": false}
	case "mqtt_subscribe_ensure":
		value = map[string]any{"id": 1, "topic_pattern": str(input, "topic_pattern"), "bus_topic": "devices.message"}
	case "mqtt_clients_list", "mqtt_retained_list":
		value = []map[string]any{}
	case "mqtt_users_list":
		users := []map[string]any{}
		for name := range s.users {
			users = append(users, map[string]any{"username": name})
		}
		value = users
	case "mqtt_users_create":
		username := base64.RawURLEncoding.EncodeToString([]byte(str(input, "_project_id"))) + ":" + str(input, "username")
		if _, exists := s.users[username]; exists {
			return errors.New("credential already exists")
		}
		s.users[username] = str(input, "password")
		value = map[string]any{"ok": true, "username": username}
	case "mqtt_users_rotate_password":
		s.users[str(input, "username")] = str(input, "password")
		value = map[string]any{"ok": true}
	case "mqtt_users_delete":
		delete(s.users, str(input, "username"))
		value = map[string]any{"ok": true}
	default:
		value = map[string]any{"ok": true}
	}
	if failing {
		s.failTool = ""
		return errors.New("injected response loss after MQTT commit")
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
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
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
	if m.Name != "devices" || m.Version != "0.2.0" || len(m.Scopes) != 1 || m.Scopes[0] != sdk.ScopeProject {
		t.Fatalf("unexpected manifest identity/scope: %#v", m)
	}
	if len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "mqtt" || m.Requires.Apps[0].Version != ">=0.4.0" {
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
		t.Fatalf("provisioned device = %#v", response)
	}
	if response["status"] != "completed" {
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
	if !strings.HasPrefix(mqtt["username"].(string), app.wireUsername("dev_op_")) || mqtt["password"] == "" || mqtt["endpoint"] != "mqtt://broker.test:1883" {
		t.Fatalf("enrollment = %#v", result)
	}
	calls := platform.callsFor("mqtt_users_create")
	if len(calls) != 1 {
		t.Fatalf("create calls = %#v", calls)
	}
	wantPublish := []string{
		app.topicRoot() + "devices/greenhouse-1/response", app.topicRoot() + "devices/greenhouse-1/telemetry", app.topicRoot() + "devices/greenhouse-1/state",
		app.topicRoot() + "devices/greenhouse-1/manifest", app.topicRoot() + "devices/greenhouse-1/availability",
	}
	gotPublish := toStrings(calls[0].Input["allow_publish"])
	if !reflect.DeepEqual(gotPublish, wantPublish) {
		t.Fatalf("publish ACL = %#v", gotPublish)
	}
	if got := toStrings(calls[0].Input["allow_subscribe"]); !reflect.DeepEqual(got, []string{app.topicRoot() + "devices/greenhouse-1/commands", app.topicRoot() + "devices/greenhouse-1/receipts"}) {
		t.Fatalf("subscribe ACL = %#v", got)
	}
	if len(testEvents(t, app, recorder, "devices.device.created")) != 1 {
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
	spoof := sdk.Event{SourceApp: "mqtt", ProjectID: "project-test", Data: map[string]any{"scope_id": "project-test",
		"topic": app.topicRoot() + "devices/sensor-1/state", "username": "another-device", "payload": `{"temperature":99}`,
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
	sensor, _ := getDevice(app.ctx.AppDB(), app.projectID, "sensor-1", false)
	valid.Data["username"] = sensor.MQTTUsername
	valid.Data["payload"] = `{"temperature":23.5,"relay":true}`
	if err := app.handleMQTTMessage(app.ctx, valid); err != nil {
		t.Fatal(err)
	}
	values, _ = listState(app.ctx.AppDB(), "sensor-1", "")
	if len(values) != 2 {
		t.Fatalf("valid state = %#v", values)
	}
	if len(testEvents(t, app, recorder, "devices.state.changed")) != 1 {
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
	if err := app.dispatchCommands(); err != nil {
		t.Fatal(err)
	}
	publishes := platform.callsFor("mqtt_publish")
	if len(publishes) != 1 {
		t.Fatalf("publish calls = %#v", publishes)
	}
	input := publishes[0].Input
	if input["topic"] != app.topicRoot()+"devices/controller-1/commands" || input["qos"] != 1 {
		t.Fatalf("publish input = %#v", input)
	}
	payload := input["payload"].(map[string]any)
	if payload["command_id"] != c.ID || payload["type"] != "function" || payload["operation"] != "function.call" {
		t.Fatalf("wire payload = %#v", payload)
	}
	response, _ := json.Marshal(map[string]any{"command_id": c.ID, "device_id": d.ID, "success": true, "result": map[string]any{"return_value": 1}})
	if err := app.handleMQTTMessage(app.ctx, sdk.Event{SourceApp: "mqtt", ProjectID: app.projectID, Data: map[string]any{"scope_id": app.projectID,
		"topic": app.topicRoot() + "devices/controller-1/response", "username": d.MQTTUsername, "payload": string(response),
	}}); err != nil {
		t.Fatal(err)
	}
	completed, err := getCommand(app.ctx.AppDB(), c.ID)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("completed command = %#v, %v", completed, err)
	}
	if len(testEvents(t, app, recorder, "devices.command.completed")) != 1 {
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
		return app.handleMQTTConnected(app.ctx, sdk.Event{SourceApp: "mqtt", ProjectID: app.projectID, Data: map[string]any{"scope_id": app.projectID,
			"username": deviceUsername(t, app, "board-2"), "client_id": clientID, "connected_at": nowText(),
		}})
	}
	if err := connect("old-session"); err != nil {
		t.Fatal(err)
	}
	if err := connect("new-session"); err != nil {
		t.Fatal(err)
	}
	recorder.Reset()
	if err := app.handleMQTTDisconnected(app.ctx, sdk.Event{SourceApp: "mqtt", ProjectID: app.projectID, Data: map[string]any{"scope_id": app.projectID,
		"username": deviceUsername(t, app, "board-2"), "client_id": "old-session", "disconnected_at": nowText(),
	}}); err != nil {
		t.Fatal(err)
	}
	d, _ := getDevice(app.ctx.AppDB(), app.projectID, "board-2", false)
	if d.Status != "online" || d.MQTTClientID != "new-session" {
		t.Fatalf("stale disconnect changed replacement session: %#v", d)
	}
	if len(testEvents(t, app, recorder, "devices.device.disconnected")) != 0 {
		t.Fatal("stale disconnect emitted a lifecycle event")
	}
}

func TestMalformedPayloadIsRecordedWithoutRetryError(t *testing.T) {
	app, _, _ := newTestApp(t)
	if _, err := app.provision(map[string]any{"name": "Sensor", "device_id": "sensor-2"}); err != nil {
		t.Fatal(err)
	}
	err := app.handleMQTTMessage(app.ctx, sdk.Event{SourceApp: "mqtt", ProjectID: app.projectID, Data: map[string]any{"scope_id": app.projectID,
		"topic": app.topicRoot() + "devices/sensor-2/state", "username": deviceUsername(t, app, "sensor-2"), "payload": `{not-json`,
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

func deviceUsername(t *testing.T, a *App, id string) string {
	t.Helper()
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		t.Fatal(err)
	}
	return d.MQTTUsername
}

func testEvents(t *testing.T, a *App, r *tk.EmitRecorder, topic string) []tk.EmittedEvent {
	t.Helper()
	if err := a.flushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r.EventsByTopic(topic)
}
