package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func issueHTTP(t *testing.T, a *App, id string) string {
	t.Helper()
	result, err := a.issueHTTPCredential(map[string]any{"device_id": id, "idempotency_key": "http-" + id})
	if err != nil {
		t.Fatal(err)
	}
	return result["token"].(string)
}
func ingestHTTP(a *App, device, token, kind, payload string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/ingest/"+device+"/"+kind, strings.NewReader(payload))
	if token != "" {
		r.Header.Set("X-Device-Token", token)
	}
	w := httptest.NewRecorder()
	a.handleDeviceIngest(w, r)
	return w
}
func TestHTTPDeviceCredentialIsolationAndAtomicReceipts(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "https-one", "apteva.devices/v2")
	other := provisionTest(t, a, "https-two", "apteva.devices/v2")
	token := issueHTTP(t, a, d.ID)
	at := nowText()
	payload := dataPayload("http-one", at, map[string]any{"temperature": 22.5, "switch": false})
	for i, expected := range []int{201, 200} {
		w := ingestHTTP(a, d.ID, token, "state", payload)
		if w.Code != expected {
			t.Fatalf("attempt=%d status=%d %s", i, w.Code, w.Body)
		}
		var receipt map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &receipt)
		if receipt["accepted"] != true || receipt["duplicate"] != (i == 1) {
			t.Fatalf("receipt=%#v", receipt)
		}
	}
	if rowCount(t, a, "device_messages") != 1 {
		t.Fatal("duplicate HTTPS submission")
	}
	if w := ingestHTTP(a, other.ID, token, "state", payload); w.Code != 401 {
		t.Fatalf("cross-device=%d", w.Code)
	}
	if w := ingestHTTP(a, d.ID, "", "state", payload); w.Code != 401 {
		t.Fatalf("missing credential=%d", w.Code)
	}
	if w := ingestHTTP(a, d.ID, token, "manifest", payload); w.Code != 404 {
		t.Fatalf("overbroad device privilege=%d", w.Code)
	}
	if w := ingestHTTP(a, d.ID, token, "state", dataPayload("http-one", at, 99)); w.Code != 409 {
		t.Fatalf("changed reuse=%d %s", w.Code, w.Body)
	}
	if w := ingestHTTP(a, d.ID, token, "telemetry", dataPayload("http-telemetry", at, []any{1, true, nil})); w.Code != 201 {
		t.Fatalf("telemetry=%d %s", w.Code, w.Body)
	}
	// A database failure must not acknowledge or partially persist a new message.
	_, _ = a.ctx.AppDB().Exec(`CREATE TRIGGER fail_http_event BEFORE INSERT ON device_outbox BEGIN SELECT RAISE(ABORT,'injected');END`)
	before := rowCount(t, a, "device_messages")
	w := ingestHTTP(a, d.ID, token, "state", dataPayload("fail-commit", nowText(), 1))
	if w.Code < 400 || rowCount(t, a, "device_messages") != before {
		t.Fatal("failed commit acknowledged")
	}
	_, _ = a.ctx.AppDB().Exec(`DROP TRIGGER fail_http_event`)
}
func TestHTTPSAndMQTTShareLogicalDeduplication(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "both", "apteva.devices/v2")
	token := issueHTTP(t, a, d.ID)
	payload := dataPayload("same-logical-id", nowText(), map[string]any{"value": 42})
	if w := ingestHTTP(a, d.ID, token, "state", payload); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	record := MQTTRecord{ID: "broker-one", Topic: a.topicRoot() + "devices/" + d.ID + "/state", Payload: base64.StdEncoding.EncodeToString([]byte(payload)), Username: d.MQTTUsername, AcceptedAt: time.Now().Unix()}
	if err := a.processMQTTRecord(record); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, a, "device_messages") != 1 || rowCount(t, a, "device_transport_receipts") != 1 {
		t.Fatal("transport duplicate was not atomically recorded")
	}
	var count int
	_ = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_usage WHERE measurement='data.messages'`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate usage")
	}
}
func TestHTTPRevocationExpiryAndLifecycle(t *testing.T) {
	for _, action := range []string{"revoke", "expire", "disable", "rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
			a, _, _ := newTestApp(t)
			d := provisionTest(t, a, "revoke", "apteva.devices/v2")
			token := issueHTTP(t, a, d.ID)
			switch action {
			case "revoke":
				creds, e := a.listHTTPCredentials(d.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = a.revokeHTTPCredential(d.ID, creds[0].ID); e != nil {
					t.Fatal(e)
				}
			case "expire":
				_, _ = a.ctx.AppDB().Exec(`UPDATE device_http_credentials SET expires_at='2000-01-01T00:00:00.000000000Z'`)
			case "disable", "rotate", "delete":
				if _, err := a.lifecycle(action, map[string]any{"device_id": d.ID, "idempotency_key": "lifecycle"}); err != nil {
					t.Fatal(err)
				}
			}
			if w := ingestHTTP(a, d.ID, token, "state", dataPayload("after", nowText(), 1)); w.Code != 401 {
				t.Fatalf("%s credential remains valid: %d", action, w.Code)
			}
			if action == "disable" {
				if _, err := a.setEnabled(d.ID, true); err != nil {
					t.Fatal(err)
				}
				if w := ingestHTTP(a, d.ID, token, "state", dataPayload("after-enable", nowText(), 1)); w.Code != 401 {
					t.Fatal("re-enabled revoked HTTP token")
				}
			}
		})
	}
}
func TestLivePolicyCASAuthorizationAndRestart(t *testing.T) {
	a, _, _ := newTestApp(t)
	provisionTest(t, a, "policy-one", "apteva.devices/v2")
	provisionTest(t, a, "policy-two", "apteva.devices/v2")
	request := map[string]any{"expected_revision": 0, "request_id": "downgrade", "limits": map[string]any{"max_devices": 1, "max_concurrent_commands": 1}}
	result, err := a.applyPolicy(request)
	if err != nil {
		t.Fatal(err)
	}
	if a.limit("max_devices", 10000) != 1 {
		t.Fatal("live policy not applied")
	}
	if len(result["over_limit"].([]string)) == 0 {
		t.Fatal("lowered quota not reported")
	}
	if rowCount(t, a, "devices") != 2 {
		t.Fatal("downgrade deleted resources")
	}
	if _, err = a.provision(map[string]any{"name": "blocked", "device_id": "blocked"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("policy did not block new resource: %v", err)
	}
	replay, err := a.applyPolicy(request)
	if err != nil || replay["replayed"] != true {
		t.Fatalf("lost response replay=%#v %v", replay, err)
	}
	if _, err = a.applyPolicy(map[string]any{"expected_revision": 0, "request_id": "stale", "limits": map[string]any{"max_devices": 3}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision=%v", err)
	}
	if _, err = a.applyPolicy(map[string]any{"expected_revision": 1, "request_id": "unknown", "limits": map[string]any{"price": 100}}); err == nil {
		t.Fatal("commercial/unknown field accepted")
	}
	if err = a.loadPolicy(); err != nil || a.limit("max_devices", 10000) != 1 {
		t.Fatal("policy did not survive reload")
	}
	for _, caller := range []*sdk.Caller{nil, {ProjectID: a.projectID, AgentID: 1}, {ProjectID: a.projectID, AppInstallID: 10, AppName: "other"}, {ProjectID: "other", AppInstallID: 10, AppName: "saas"}} {
		ctx := context.Background()
		if caller != nil {
			ctx = sdk.WithCaller(ctx, caller)
		}
		if err = a.policyWriter(ctx); err == nil {
			t.Fatalf("unauthorized policy caller %#v", caller)
		}
	}
	if err = a.policyWriter(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: a.projectID, AppInstallID: 10, AppName: "saas"})); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "/policy", nil)
	r.Header.Set("X-Apteva-Operator-ID", "1")
	if err = a.httpPolicyWriter(r); err == nil {
		t.Fatal("unconfigured HTTP policy operator accepted")
	}
}
func TestPolicyConcurrentRevisionsAndRollback(t *testing.T) {
	a, _, _ := newTestApp(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := a.applyPolicy(map[string]any{"expected_revision": 0, "request_id": fmt.Sprint(i), "limits": map[string]any{"max_devices": i + 1}})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	before := a.limit("max_devices", 10000)
	_, _ = a.ctx.AppDB().Exec(`CREATE TRIGGER fail_policy_event BEFORE INSERT ON device_outbox BEGIN SELECT RAISE(ABORT,'fail');END`)
	if _, err := a.applyPolicy(map[string]any{"expected_revision": 1, "request_id": "rollback", "limits": map[string]any{"max_devices": 99}}); err == nil {
		t.Fatal("failed transaction succeeded")
	}
	if a.limit("max_devices", 10000) != before {
		t.Fatal("uncommitted policy applied")
	}
}
func TestUsageSummaryWindowsCountsAndCoverage(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "usage", "apteva.devices/v2")
	token := issueHTTP(t, a, d.ID)
	if w := ingestHTTP(a, d.ID, token, "state", dataPayload("usage-sample", nowText(), map[string]any{"value": 1})); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	// Pin the sample to an exact boundary and verify the exclusive end.
	at := "2026-01-02T12:00:00.000000000Z"
	_, _ = a.ctx.AppDB().Exec(`UPDATE device_usage SET created_at=? WHERE measurement LIKE 'data.%'`, at)
	result, err := a.usageSummary(map[string]any{"from": "2026-01-02T00:00:00Z", "to": "2026-01-02T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if result["totals"].(map[string]string)["data.messages"] != "" {
		t.Fatal("end boundary included")
	}
	result, err = a.usageSummary(map[string]any{"from": "2026-01-02T12:00:00Z", "to": "2026-01-03T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if result["totals"].(map[string]string)["data.messages"] != "1" {
		t.Fatalf("totals=%#v", result)
	}
	if result["resources"].(map[string]int64)["devices"] != 1 || result["resources"].(map[string]int64)["active_http_credentials"] != 1 {
		t.Fatal("resource counts")
	}
	if result["coverage"].(map[string]any)["complete"] != false {
		t.Fatal("pre-upgrade coverage falsely claimed")
	}
}
func TestMQTTTransportReceiptPreventsReplayEffects(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "durable-native", "apteva.devices/v1")
	r := MQTTRecord{ID: "native-stable", Topic: a.topicRoot() + "devices/" + d.ID + "/telemetry", Payload: base64.StdEncoding.EncodeToString([]byte(`{"temperature":24}`)), Username: d.MQTTUsername, AcceptedAt: time.Now().Unix()}
	for i := 0; i < 2; i++ {
		if err := a.processMQTTRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if rowCount(t, a, "device_messages") != 1 {
		t.Fatal("native replay duplicated")
	}
	_, _ = a.ctx.AppDB().Exec(`CREATE TRIGGER fail_receipt BEFORE INSERT ON device_transport_receipts BEGIN SELECT RAISE(ABORT,'injected');END`)
	r.ID = "native-failed"
	if err := a.processMQTTRecord(r); err == nil {
		t.Fatal("expected receipt failure")
	}
	if rowCount(t, a, "device_messages") != 1 {
		t.Fatal("receipt and data were not atomic")
	}
	_, _ = a.ctx.AppDB().Exec(`DROP TRIGGER fail_receipt`)
	r.ID = "poison"
	r.Payload = base64.StdEncoding.EncodeToString([]byte(`not-json`))
	if err := a.processMQTTRecord(r); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, a, "device_ingest_rejections") != 1 {
		t.Fatal("poison message not quarantined")
	}
}

func TestConsumerAcknowledgesOnlyCommittedEffectsAndRecoversLostAck(t *testing.T) {
	a, p, _ := newTestApp(t)
	d := provisionTest(t, a, "consumer-ack", "apteva.devices/v2")
	payload := dataPayload("stable-logical", nowText(), map[string]any{"value": 1})
	p.consumerRecords = []MQTTRecord{{ID: "stable-transport", Topic: a.topicRoot() + "devices/" + d.ID + "/state", Payload: base64.StdEncoding.EncodeToString([]byte(payload)), Username: d.MQTTUsername, AcceptedAt: time.Now().Unix()}}
	p.consumerAckHook = func() {
		if rowCount(t, a, "device_messages") != 1 || rowCount(t, a, "device_transport_receipts") != 1 {
			t.Fatal("broker ack preceded durable effects")
		}
	}
	p.consumerAckFail = true
	if err := a.consumeMQTTBatch(context.Background()); err == nil {
		t.Fatal("ack failure ignored")
	}
	p.consumerAckFail = false
	if err := a.consumeMQTTBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, a, "device_messages") != 1 {
		t.Fatal("lost ack reprocessed data")
	}
	var acknowledged *string
	if err := a.ctx.AppDB().QueryRow(`SELECT acknowledged_at FROM device_transport_receipts`).Scan(&acknowledged); err != nil || acknowledged == nil {
		t.Fatal("receipt checkpoint missing", err)
	}
	// A new message whose database transaction fails must request retry, never ack.
	_, _ = a.ctx.AppDB().Exec(`CREATE TRIGGER fail_consumer BEFORE INSERT ON device_messages BEGIN SELECT RAISE(ABORT,'failed');END`)
	p.consumerRecords[0].ID = "new-transport"
	p.consumerRecords[0].Payload = base64.StdEncoding.EncodeToString([]byte(dataPayload("new-logical", nowText(), 2)))
	before := len(p.callsFor("mqtt_consumer"))
	if err := a.consumeMQTTBatch(context.Background()); err == nil {
		t.Fatal("storage failure ignored")
	}
	calls := p.callsFor("mqtt_consumer")[before:]
	for _, call := range calls {
		if str(call.Input, "action") == "ack" {
			t.Fatal("uncommitted batch acknowledged")
		}
	}
	if str(calls[len(calls)-1].Input, "action") != "retry" {
		t.Fatal("retry not requested")
	}
}
func TestAcceptedDuplicateSurvivesLoweredPayloadPolicy(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := provisionTest(t, a, "lower-payload", "apteva.devices/v2")
	token := issueHTTP(t, a, d.ID)
	payload := dataPayload("old-accepted", nowText(), map[string]any{"message": strings.Repeat("x", 100)})
	if w := ingestHTTP(a, d.ID, token, "state", payload); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := a.applyPolicy(map[string]any{"expected_revision": 0, "request_id": "lower-payload", "limits": map[string]any{"max_device_payload_bytes": 1}}); err != nil {
		t.Fatal(err)
	}
	if w := ingestHTTP(a, d.ID, token, "state", payload); w.Code != 200 {
		t.Fatalf("accepted replay blocked by new limit: %d %s", w.Code, w.Body)
	}
	if w := ingestHTTP(a, d.ID, token, "state", dataPayload("new", nowText(), 1)); w.Code != 429 {
		t.Fatalf("new admission ignored live limit: %d %s", w.Code, w.Body)
	}
}
func TestPeriodicRecoveryDoesNotRequireAnotherRequest(t *testing.T) {
	a, p, _ := newTestApp(t)
	p.failTool = "mqtt_users_create"
	a.mutationMu.Lock()
	pending, err := a.lifecycleLocked("provision", map[string]any{"name": "periodic", "device_id": "periodic"})
	a.mutationMu.Unlock()
	if err != nil || pending["status"] != "pending" {
		t.Fatal("expected pending operation", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = a.runReconciliation(ctx, a.ctx); close(done) }()
	deadline := time.Now().Add(4 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		var status string
		err = a.ctx.AppDB().QueryRow(`SELECT status FROM device_operations WHERE id=?`, pending["operation_id"]).Scan(&status)
		if err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			completed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if !completed {
		t.Fatal("periodic recovery did not resume without a wake")
	}
}
