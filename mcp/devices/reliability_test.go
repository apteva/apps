package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func provisionTest(t *testing.T, a *App, id, protocol string) Device {
	t.Helper()
	args := map[string]any{"name": id, "device_id": id, "protocol": protocol}
	if protocol == "apteva.devices/v2" {
		args["capabilities"] = map[string]any{"operations": []any{map[string]any{"name": "lamp.set", "input_schema": map[string]any{"type": "object", "required": []any{"on"}, "properties": map[string]any{"on": map[string]any{"type": "boolean"}}, "additionalProperties": false}, "output_schema": map[string]any{"type": "object"}}}}
	}
	result, err := a.provision(args)
	if err != nil || result["status"] != "completed" {
		t.Fatalf("provision=%#v err=%v", result, err)
	}
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func dataPayload(id, at string, data any) string {
	raw, _ := json.Marshal(map[string]any{"message_id": id, "observed_at": at, "data": data})
	return string(raw)
}
func rowCount(t *testing.T, a *App, table string) int {
	t.Helper()
	var n int
	if err := a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestGenericDataAtomicDedupOrderingAndQueries(t *testing.T) {
	a, p, _ := newTestApp(t)
	d := provisionTest(t, a, "sensor", "apteva.devices/v2")
	at := nowText()
	payload := dataPayload("m1", at, map[string]any{"id": "opaque", "name": "value", "timestamp": 123, "nested": map[string]any{"list": []any{true, nil, 4}}})
	for i := 0; i < 2; i++ {
		if err := a.ingest(d, "state", payload, nowText()); err != nil {
			t.Fatal(err)
		}
	}
	if rowCount(t, a, "device_messages") != 1 {
		t.Fatal("duplicate sample")
	}
	states, err := listState(a.ctx.AppDB(), d.ID, "")
	if err != nil || len(states) != 4 {
		t.Fatalf("state=%#v %v", states, err)
	}
	var accepted int
	if err = a.ctx.AppDB().QueryRow(`SELECT SUM(quantity) FROM device_usage WHERE measurement='data.messages'`).Scan(&accepted); err != nil || accepted != 1 {
		t.Fatalf("usage=%d %v", accepted, err)
	}
	if err = a.ingest(d, "state", dataPayload("m1", at, map[string]any{"id": "changed"}), nowText()); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict: %v", err)
	}
	old := formatTime(time.Now().Add(-time.Hour))
	if err = a.ingest(d, "state", dataPayload("old", old, map[string]any{"id": "stale"}), nowText()); err != nil {
		t.Fatal(err)
	}
	state, _ := listState(a.ctx.AppDB(), d.ID, "id")
	if state[0].Value != "opaque" {
		t.Fatalf("stale overwrite: %#v", state)
	}
	page, err := a.queryHistory("data", map[string]any{"device_id": d.ID, "limit": 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page=%#v %v", page, err)
	}
	// A later insert does not move the high-water mark of an existing cursor.
	if err = a.ingest(d, "telemetry", dataPayload("later", nowText(), []any{1, 2}), nowText()); err != nil {
		t.Fatal(err)
	}
	second, err := a.queryHistory("data", map[string]any{"device_id": d.ID, "limit": 1, "cursor": page.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second=%#v %v", second, err)
	}
	if _, err = a.queryHistory("data", map[string]any{"device_id": "different", "cursor": page.NextCursor}); err == nil {
		t.Fatal("cursor changed filters")
	}
	receipts := 0
	for _, call := range p.callsFor("mqtt_publish") {
		if strings.HasSuffix(str(call.Input, "topic"), "/receipts") {
			receipts++
		}
	}
	if receipts != 4 {
		t.Fatalf("receipts=%d", receipts)
	}
}
func TestIngestionRollbackAndSnapshot(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "snapshot", "apteva.devices/v2")
	if _, err := a.ctx.AppDB().Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON device_outbox BEGIN SELECT RAISE(ABORT,'injected event failure');END`); err != nil {
		t.Fatal(err)
	}
	if err := a.ingest(d, "state", dataPayload("one", nowText(), map[string]any{"a": 1}), nowText()); err == nil {
		t.Fatal("expected failure")
	}
	if rowCount(t, a, "device_messages") != 0 || rowCount(t, a, "device_state") != 0 {
		t.Fatal("partial ingest committed")
	}
	_, _ = a.ctx.AppDB().Exec(`DROP TRIGGER fail_event`)
	if err := a.ingest(d, "state", dataPayload("two", nowText(), map[string]any{"a": 1, "b": 2}), nowText()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"message_id": "three", "observed_at": nowText(), "data": map[string]any{}, "mode": "snapshot"})
	if err := a.ingest(d, "state", string(raw), nowText()); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, a, "device_state") != 0 {
		t.Fatal("snapshot did not clear state")
	}
}
func TestSequenceAndClockPolicy(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "sequence", "apteva.devices/v2")
	at := nowText()
	for _, seq := range []int{4, 3} {
		raw, _ := json.Marshal(map[string]any{"message_id": string(rune('a' + seq)), "observed_at": at, "session_id": "boot", "sequence": seq, "data": map[string]any{"value": seq}})
		if err := a.ingest(d, "state", string(raw), nowText()); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := listState(a.ctx.AppDB(), d.ID, "value")
	if fmt.Sprint(state[0].Value) != "4" {
		t.Fatalf("sequence=%#v", state)
	}
	if err := a.ingest(d, "state", dataPayload("future", formatTime(time.Now().Add(time.Hour)), 1), nowText()); err == nil {
		t.Fatal("future clock accepted")
	}
}
func TestCommandConflictDeadlineAndRecovery(t *testing.T) {
	a, p, _ := newTestApp(t)
	d := provisionTest(t, a, "commands", "apteva.devices/v2")
	c, err := a.sendCommand(d, "lamp.set", "", map[string]any{"on": true}, false, 5000, "one")
	if e := a.dispatchCommands(); e != nil {
		t.Fatal(e)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.sendCommand(d, "lamp.set", "", map[string]any{"on": false}, false, 5000, "one"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	if _, err = a.sendCommand(d, "lamp.set", "", map[string]any{"on": "wrong"}, false, 5000, "wrong"); err == nil {
		t.Fatal("schema ignored")
	}
	_, _ = a.ctx.AppDB().Exec(`UPDATE device_commands SET next_attempt_at=NULL WHERE id=?`, c.ID)
	if err = a.dispatchCommands(); err != nil {
		t.Fatal(err)
	}
	calls := p.callsFor("mqtt_publish")
	var ids []string
	for _, call := range calls {
		if strings.HasSuffix(str(call.Input, "topic"), "/commands") {
			ids = append(ids, str(call.Input["payload"].(map[string]any), "command_id"))
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("retry ids=%v", ids)
	}
	late := formatTime(time.Now().Add(-time.Second))
	_, _ = a.ctx.AppDB().Exec(`UPDATE device_commands SET deadline_at=? WHERE id=?`, late, c.ID)
	response := `{"command_id":"` + c.ID + `","success":true,"result":{}}`
	if err = a.processResponse(d, response, nowText()); err != nil {
		t.Fatal(err)
	}
	got, _ := getCommand(a.ctx.AppDB(), c.ID)
	if got.Status != "timed_out" {
		t.Fatalf("late response succeeded: %#v", got)
	}
	if err = a.processResponse(d, response, nowText()); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, a, "device_responses") != 1 {
		t.Fatal("duplicate response history")
	}
	var count int
	_ = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_usage WHERE measurement='commands.timed_out'`).Scan(&count)
	if count != 1 {
		t.Fatal("terminal usage duplicated")
	}
}
func TestConcurrentCapacity(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	p := &mqttStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-test"), tk.WithPlatform(p), tk.WithEmitter(tk.NewEmitRecorder()), tk.WithConfig(map[string]string{"max_devices": "1", "max_pending_commands_per_device": "1", "max_concurrent_commands_per_device": "1"}))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	d := provisionTest(t, a, "bounded", "apteva.devices/v2")
	if _, err := a.provision(map[string]any{"name": "extra", "device_id": "extra"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("device limit: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, rejected := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.sendCommand(d, "lamp.set", "", map[string]any{"on": true}, false, 5000, "")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else if errors.Is(err, ErrLimit) {
				rejected++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted != 1 || rejected != 11 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
}
func TestLifecycleRecoversAfterLocalCommitFailure(t *testing.T) {
	a, p, _ := newTestApp(t)
	_, err := a.ctx.AppDB().Exec(`CREATE TRIGGER fail_device BEFORE INSERT ON devices BEGIN SELECT RAISE(ABORT,'injected device write');END`)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "recover", "device_id": "recover", "idempotency_key": "create-one"}
	result, err := a.provision(args)
	if err != nil || result["status"] != "pending" {
		t.Fatalf("pending=%#v %v", result, err)
	}
	calls := p.callsFor("mqtt_users_create")
	if len(calls) != 1 {
		t.Fatal("missing remote write")
	}
	secret := str(calls[0].Input, "password")
	var stored string
	_ = a.ctx.AppDB().QueryRow(`SELECT secret_cipher FROM device_operations`).Scan(&stored)
	if stored == secret || strings.Contains(stored, secret) {
		t.Fatal("plaintext persisted")
	}
	_, _ = a.ctx.AppDB().Exec(`DROP TRIGGER fail_device`)
	if err = a.reconcileOperations(); err != nil {
		t.Fatal(err)
	}
	result, err = a.provision(args)
	if err != nil || result["status"] != "completed" {
		t.Fatalf("recovered=%#v %v", result, err)
	}
	if result["mqtt"].(map[string]any)["password"] != secret {
		t.Fatal("retry changed password")
	}
	args["name"] = "different"
	if _, err = a.provision(args); !errors.Is(err, ErrConflict) {
		t.Fatalf("lifecycle conflict=%v", err)
	}
}
func TestLifecycleRotationDisableDeletionAndUsage(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "lifecycle", "apteva.devices/v2")
	rotate := map[string]any{"device_id": d.ID, "idempotency_key": "rotate-once"}
	first, err := a.lifecycle("rotate", rotate)
	if err != nil || first["status"] != "completed" {
		t.Fatalf("rotation=%#v %v", first, err)
	}
	again, err := a.lifecycle("rotate", rotate)
	if err != nil || first["password"] != again["password"] {
		t.Fatal("rotation replay changed secret")
	}
	updated, _ := getDevice(a.ctx.AppDB(), a.projectID, d.ID, false)
	if updated.CredentialVersion != 2 {
		t.Fatalf("version=%d", updated.CredentialVersion)
	}
	c, err := a.sendCommand(d, "lamp.set", "", map[string]any{"on": true}, false, 5000, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.setEnabled(d.ID, false); err != nil {
		t.Fatal(err)
	}
	command, _ := getCommand(a.ctx.AppDB(), c.ID)
	if command.Status != "cancelled" {
		t.Fatal("disable left active command")
	}
	if _, err = a.setEnabled(d.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = a.setEnabled(d.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = a.setEnabled(d.ID, true); err != nil {
		t.Fatal(err)
	}
	before := rowCount(t, a, "device_usage")
	result, err := a.lifecycle("delete", map[string]any{"device_id": d.ID})
	if err != nil || result["status"] != "completed" {
		t.Fatalf("delete=%#v %v", result, err)
	}
	result, err = a.lifecycle("delete", map[string]any{"device_id": d.ID})
	if err != nil || result["status"] != "completed" {
		t.Fatal("delete replay")
	}
	if rowCount(t, a, "device_usage") <= before {
		t.Fatal("usage lost on deletion")
	}
	if rowCount(t, a, "devices") != 0 {
		t.Fatal("device not deleted")
	}
	if _, err = a.provision(map[string]any{"name": "lifecycle", "device_id": d.ID}); err == nil {
		t.Fatal("retired identity reused")
	}
}
func TestScopeBoundariesAndMissingToken(t *testing.T) {
	a, _, _ := newTestApp(t)
	provisionTest(t, a, "scoped", "apteva.devices/v2")
	for _, caller := range []*sdk.Caller{nil, {ProjectID: "other"}, {ProjectID: ""}} {
		ctx := context.Background()
		if caller != nil {
			ctx = sdk.WithCaller(ctx, caller)
		}
		for _, tool := range a.MCPTools() {
			if _, err := tool.HandlerCtx(ctx, a.ctx, map[string]any{"device_id": "scoped", "scope_id": a.projectID}); err == nil {
				t.Fatalf("tool %s accepted unauthorized caller", tool.Name)
			}
		}
	}
	good := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: a.projectID})
	if err := a.authorizeCaller(good); err != nil {
		t.Fatal(err)
	}
	for _, route := range a.HTTPRoutes() {
		req := httptest.NewRequest(http.MethodGet, route.Pattern, nil)
		req.Header.Set("X-Apteva-Project-ID", "other")
		w := httptest.NewRecorder()
		route.Handler(w, req)
		if w.Code != 403 {
			t.Fatalf("route %s scope=%d", route.Pattern, w.Code)
		}
	}
	// Even an authenticated bus envelope needs the matching ownership fields.
	before := rowCount(t, a, "device_messages")
	err := a.handleMQTTMessage(a.ctx, sdk.Event{SourceApp: "mqtt", ProjectID: "other", Data: map[string]any{"scope_id": a.projectID, "topic": a.topicRoot() + "devices/scoped/state", "username": deviceUsername(t, a, "scoped"), "payload": dataPayload("spoof", nowText(), 1)}})
	if err != nil || rowCount(t, a, "device_messages") != before {
		t.Fatal("foreign event accepted")
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	if err = a.authorizeCaller(good); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestMQTTPartialFailuresConverge(t *testing.T) {
	for _, kind := range []string{"provision", "rotate", "disable", "delete"} {
		for _, after := range []bool{false, true} {
			t.Run(kind+fmt.Sprint(after), func(t *testing.T) {
				a, p, _ := newTestApp(t)
				args := map[string]any{"device_id": "fault", "idempotency_key": "fault-key"}
				if kind == "provision" {
					args["name"] = "fault"
				} else {
					provisionTest(t, a, "fault", "apteva.devices/v2")
				}
				tools := map[string]string{"provision": "mqtt_users_create", "rotate": "mqtt_users_rotate_password", "disable": "mqtt_users_set_enabled", "delete": "mqtt_users_delete"}
				p.failTool = tools[kind]
				p.failAfter = after
				result, err := a.lifecycle(kind, args)
				if err != nil || result["status"] != "pending" {
					t.Fatalf("pending=%#v %v", result, err)
				}
				if kind == "disable" || kind == "delete" {
					d, _ := getDevice(a.ctx.AppDB(), a.projectID, "fault", false)
					if d.Enabled {
						t.Fatal("pending revocation permits execution")
					}
				}
				if err = a.reconcileOperations(); err != nil {
					t.Fatal(err)
				}
				recovered, err := a.lifecycle(kind, args)
				if err != nil || recovered["status"] != "completed" || recovered["operation_id"] != result["operation_id"] {
					t.Fatalf("recovered=%#v %v", recovered, err)
				}
				if kind == "rotate" {
					d, _ := getDevice(a.ctx.AppDB(), a.projectID, "fault", false)
					if d.CredentialVersion != 2 {
						t.Fatal("credential version incremented more than once")
					}
				}
			})
		}
	}
}

type failingEmitter struct {
	*tk.EmitRecorder
	fail bool
}

func (e *failingEmitter) EmitWithProjectAck(ctx context.Context, topic, scope string, data any) error {
	if e.fail {
		return errors.New("gateway unavailable")
	}
	return e.EmitRecorder.EmitWithProjectAck(ctx, topic, scope, data)
}
func TestOutboxReplayAndRetention(t *testing.T) {
	a, _, rec := newTestApp(t)
	e := &failingEmitter{EmitRecorder: rec, fail: true}
	a.ctx.SetEmitter(e)
	d := provisionTest(t, a, "outbox", "apteva.devices/v2")
	if err := a.flushOutbox(context.Background()); err == nil {
		t.Fatal("unacknowledged event marked delivered")
	}
	var eventID string
	_ = a.ctx.AppDB().QueryRow(`SELECT event_id FROM device_outbox ORDER BY seq LIMIT 1`).Scan(&eventID)
	e.fail = false
	if err := a.flushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := rec.EventsByTopic("devices.device.created")
	if len(events) != 1 || events[0].Data.(map[string]any)["event_id"] != eventID {
		t.Fatal("outbox changed event identity")
	}
	if err := a.flushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.EventsByTopic("devices.device.created")) != 1 {
		t.Fatal("delivered outbox repeated")
	}
	if err := a.ingest(d, "state", dataPayload("one", nowText(), 1), nowText()); err != nil {
		t.Fatal(err)
	}
	if err := a.ingest(d, "state", dataPayload("two", nowText(), 2), nowText()); err != nil {
		t.Fatal(err)
	}
	page, err := a.queryHistory("data", map[string]any{"limit": 1})
	if err != nil || page.NextCursor == "" {
		t.Fatal("history cursor missing")
	}
	old := formatTime(time.Now().Add(-400 * 24 * time.Hour))
	_, _ = a.ctx.AppDB().Exec(`UPDATE device_messages SET received_at=?`, old)
	if err = a.runTelemetryPrune(context.Background(), a.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = a.queryHistory("data", map[string]any{"limit": 1, "cursor": page.NextCursor}); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("expired cursor=%v", err)
	}
	if rowCount(t, a, "device_state") != 1 {
		t.Fatal("pruning removed current state")
	}
	// Pending notifications are retained even beyond the replay retention window.
	_, _ = a.ctx.AppDB().Exec(`UPDATE device_outbox SET created_at=?,delivered_at=NULL`, old)
	if err = a.runTelemetryPrune(context.Background(), a.ctx); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, a, "device_outbox") == 0 {
		t.Fatal("pending outbox pruned")
	}
}
func TestMessageIdentityIsPerDevice(t *testing.T) {
	a, _, _ := newTestApp(t)
	for _, id := range []string{"one", "two"} {
		d := provisionTest(t, a, id, "apteva.devices/v2")
		if err := a.ingest(d, "state", dataPayload("same-id", nowText(), id), nowText()); err != nil {
			t.Fatal(err)
		}
	}
	var measured int
	_ = a.ctx.AppDB().QueryRow(`SELECT SUM(quantity) FROM device_usage WHERE measurement='data.messages'`).Scan(&measured)
	if measured != 2 {
		t.Fatalf("usage merged device identities: %d", measured)
	}
}

func TestExactNumbersAndSubmissionSchemaSnapshot(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "exact", "apteva.devices/v2")
	raw := `{"message_id":"exact","observed_at":"` + nowText() + `","data":{"big":9007199254740993,"fraction":0.123456789123456789}}`
	if err := a.ingest(d, "state", raw, nowText()); err != nil {
		t.Fatal(err)
	}
	state, err := listState(a.ctx.AppDB(), d.ID, "big")
	if err != nil || fmt.Sprint(state[0].Value) != "9007199254740993" {
		t.Fatalf("number rounded: %#v %v", state, err)
	}
	page, err := a.queryHistory("data", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if !strings.Contains(string(encoded), "0.123456789123456789") {
		t.Fatalf("history rounded: %s", encoded)
	}
	c, err := a.sendCommand(d, "lamp.set", "", map[string]any{"on": true}, false, 5000, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.updateCapabilities(d.ID, map[string]any{"operations": []any{map[string]any{"name": "lamp.set", "output_schema": map[string]any{"type": "string"}}}}); err != nil {
		t.Fatal(err)
	}
	d, _ = getDevice(a.ctx.AppDB(), a.projectID, d.ID, false)
	if err = a.processResponse(d, `{"command_id":"`+c.ID+`","success":true,"result":{}}`, nowText()); err != nil {
		t.Fatalf("inflight schema changed: %v", err)
	}
}
func TestHTTPConflictLimitAndNullBody(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "token")
	p := &mqttStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-test"), tk.WithPlatform(p), tk.WithConfig(map[string]string{"max_devices": "1"}))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	var devices sdk.Route
	for _, r := range a.HTTPRoutes() {
		if r.Pattern == "/devices" {
			devices = r
		}
	}
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/devices", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "one")
		w := httptest.NewRecorder()
		devices.Handler(w, r)
		return w.Code
	}
	if code := post(`null`); code != 400 {
		t.Fatalf("null=%d", code)
	}
	if code := post(`{"name":"one","device_id":"one"}`); code != 201 {
		t.Fatalf("create=%d", code)
	}
	if code := post(`{"name":"changed","device_id":"one"}`); code != 409 {
		t.Fatalf("conflict=%d", code)
	}
	r := httptest.NewRequest("POST", "/devices", strings.NewReader(`{"name":"two","device_id":"two"}`))
	r.Header.Set("Idempotency-Key", "two")
	w := httptest.NewRecorder()
	devices.Handler(w, r)
	if w.Code != 429 {
		t.Fatalf("limit=%d body=%s", w.Code, w.Body)
	}
}

func TestInvalidLimitsFailClosed(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "token")
	p := &mqttStub{}
	for _, config := range []map[string]string{{"max_devices": "0"}, {"usage_retention_days": "1000000000"}, {"max_device_payload_bytes": "1048576"}, {"command_timeout_ms": "nonsense"}} {
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-test"), tk.WithPlatform(p), tk.WithConfig(config))
		if err := (&App{}).OnMount(ctx); err == nil {
			t.Fatalf("invalid config accepted: %#v", config)
		}
	}
}
func TestDataLimitsAreAtomicAndCountUTF8Bytes(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "token")
	p := &mqttStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-test"), tk.WithPlatform(p), tk.WithConfig(map[string]string{"max_state_bytes_per_device": "10"}))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	d := provisionTest(t, a, "utf8", "apteva.devices/v2")
	err := a.ingest(d, "state", dataPayload("large", nowText(), map[string]any{"text": "ééééé"}), nowText())
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("byte limit: %v", err)
	}
	if rowCount(t, a, "device_messages") != 0 || rowCount(t, a, "device_state") != 0 {
		t.Fatal("over-limit data partially committed")
	}
}

func TestCommandReplaySurvivesCapabilityAndEnabledChanges(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "replay-policy", "apteva.devices/v2")
	c, err := a.sendCommand(d, "lamp.set", "", map[string]any{"on": true}, false, 0, "original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.updateCapabilities(d.ID, map[string]any{"operations": []any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.setEnabled(d.ID, false); err != nil {
		t.Fatal(err)
	}
	replay, err := a.sendCommand(d, "lamp.set", "", map[string]any{"on": true}, false, 0, "original")
	if err != nil || replay.ID != c.ID || replay.Status != "cancelled" {
		t.Fatalf("replay=%#v %v", replay, err)
	}
	if _, err = a.sendCommand(d, "lamp.set", "", map[string]any{"on": false}, false, 0, "original"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry=%v", err)
	}
}
