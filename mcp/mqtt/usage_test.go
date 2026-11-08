package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
)

func usageApp(t *testing.T, policy, overrides string) *App {
	t.Helper()
	a, _, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false", "quota_defaults_json": policy, "scope_quotas_json": overrides})
	return a
}
func usageValue(t *testing.T, a *App, scope, key string, period int64) int64 {
	t.Helper()
	var n int64
	err := a.ctx.AppDB().QueryRow(`SELECT value FROM mqtt_usage_counters WHERE scope_id=? AND metric=? AND period_start=?`, scope, key, period).Scan(&n)
	if err != nil {
		t.Fatal(key, err)
	}
	return n
}
func quotaConnect(t *testing.T, port int, scope, id string, protocol byte) (net.Conn, byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	username, _ := credentialName(scope, "sensor")
	pk := packets.Packet{ProtocolVersion: protocol, FixedHeader: packets.FixedHeader{Type: packets.Connect}, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: id, Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte(username), Password: []byte("test-password")}}
	if protocol == 5 {
		pk.Properties.SessionExpiryInterval = 3600
		pk.Properties.SessionExpiryIntervalFlag = true
	}
	wireWrite(t, conn, pk)
	body := requireWire(t, conn, packets.Connack)
	if len(body) < 2 {
		t.Fatal(body)
	}
	return conn, body[1]
}
func TestUsageAcceptedQuotaAtomicAndScoped(t *testing.T) {
	a := usageApp(t, `{"accepted_messages":10}`, `{"a":{"accepted_messages":2,"accepted_payload_bytes":6,"payload_bytes":4}}`)
	root := scopeTopicRoot("a")
	for _, body := range []string{"one", "two"} {
		if err := a.broker.Publish(root+"state", []byte(body), true, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.broker.Publish(root+"state", []byte("xxx"), true, 1); !errors.Is(err, errOverloaded) {
		t.Fatal("expected quota rejection", err)
	}
	if n := usageValue(t, a, "a", "accepted_messages", 0); n != 2 {
		t.Fatal("rejected publish consumed acceptance", n)
	}
	if n := usageValue(t, a, "a", "accepted_payload_bytes", 0); n != 6 {
		t.Fatal(n)
	}
	if n := usageValue(t, a, "a", "quota_rejections", 0); n != 1 {
		t.Fatal(n)
	}
	value, err := getRetainedMessage(a.ctx.AppDB(), "a", root+"state")
	if err != nil || value.Payload != "two" {
		t.Fatal("quota rejection changed retained data", value, err)
	}
	var n int
	a.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal WHERE scope_id='a'`).Scan(&n)
	if n != 2 {
		t.Fatal("partially accepted journal", n)
	}
	if err = a.broker.Publish(scopeTopicRoot("b")+"state", []byte("other scope"), false, 1); err != nil {
		t.Fatal("scope quota leaked", err)
	}
	if a.durable.failed.Load() {
		t.Fatal("normal quota denial made broker unhealthy")
	}
}
func TestUsageConcurrentAcceptanceCannotOverspend(t *testing.T) {
	a := usageApp(t, `{"accepted_messages":10}`, `{}`)
	var wg sync.WaitGroup
	var success atomic.Int64
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := a.broker.Publish(scopeTopicRoot("a")+"state", []byte("data"), false, 1)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, errOverloaded) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 10 || usageValue(t, a, "a", "accepted_messages", 0) != 10 {
		t.Fatal("quota overspent", success.Load())
	}
	var n int
	a.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal`).Scan(&n)
	if n != 10 {
		t.Fatal(n)
	}
}
func TestUsageRetainedCapacityReplaceDeleteAndNoEviction(t *testing.T) {
	a := usageApp(t, `{"retained_messages":1,"retained_bytes":1024}`, `{}`)
	root := scopeTopicRoot("a")
	if err := a.broker.Publish(root+"one", []byte("keep"), true, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.broker.Publish(root+"two", []byte("deny"), true, 1); !errors.Is(err, errOverloaded) {
		t.Fatal(err)
	}
	if err := a.broker.Publish(root+"one", []byte(strings.Repeat("x", 1024)), true, 1); !errors.Is(err, errOverloaded) {
		t.Fatal("retained byte growth accepted", err)
	}
	if n := usageValue(t, a, "a", "accepted_messages", 0); n != 1 {
		t.Fatal("retained rejection charged acceptance", n)
	}
	if err := a.broker.Publish(root+"one", []byte("replacement"), true, 1); err != nil {
		t.Fatal(err)
	}
	a.sweepOnce()
	record, err := getRetainedMessage(a.ctx.AppDB(), "a", root+"one")
	if err != nil || record.Payload != "replacement" {
		t.Fatal("retained eviction", record, err)
	}
	if err = a.broker.DeleteRetained(root + "one"); err != nil {
		t.Fatal(err)
	}
	var count, bytes int64
	if err = a.ctx.AppDB().QueryRow(`SELECT messages,bytes FROM mqtt_retained_usage WHERE scope_id='a'`).Scan(&count, &bytes); err != nil || count != 0 || bytes != 0 {
		t.Fatal("delete did not release capacity", count, bytes, err)
	}
	if err = a.broker.Publish(root+"two", []byte("now allowed"), true, 1); err != nil {
		t.Fatal(err)
	}
	// Lowering capacity preserves data and permits draining/reducing it.
	a.usage.defaults["retained_bytes"] = 1
	if err = a.broker.Publish(root+"two", []byte("x"), true, 1); err != nil {
		t.Fatal("could not reduce over-quota retained storage", err)
	}
	if err = a.broker.DeleteRetained(root + "two"); err != nil {
		t.Fatal(err)
	}
}
func TestUsageFixedWindowRestartClockAndConfig(t *testing.T) {
	a := usageApp(t, `{"accepted_messages":1}`, `{}`)
	var now atomic.Int64
	now.Store(1800000000)
	a.usage.now = func() time.Time { return time.Unix(now.Load(), 0) }
	root := scopeTopicRoot("a")
	if err := a.broker.Publish(root+"state", []byte("one"), false, 1); err != nil {
		t.Fatal(err)
	}
	var start, end int64
	a.ctx.AppDB().QueryRow(`SELECT starts_at,ends_at FROM mqtt_usage_windows WHERE scope_id='a'`).Scan(&start, &end)
	// A new meter represents restart with different window length; active budget survives.
	if err := a.broker.Close(); err != nil {
		t.Fatal(err)
	}
	a.ctx.Config()["quota_period_seconds"] = "60"
	restarted, err := newUsageMeter(a)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return time.Unix(now.Load(), 0) }
	a.usage = restarted
	charge := func() error {
		return restarted.transaction("a", func(tx *sql.Tx) error { return restarted.charge(tx, "a", map[string]int64{"accepted_messages": 1}) })
	}
	if err = charge(); !errors.Is(err, mqtt.ErrDurableCapacity) {
		t.Fatal("restart reset budget", err)
	}
	var after int64
	a.ctx.AppDB().QueryRow(`SELECT ends_at FROM mqtt_usage_windows WHERE scope_id='a'`).Scan(&after)
	if after != end {
		t.Fatal("policy change shortened active window")
	}
	now.Store(end + 1)
	if err = charge(); err != nil {
		t.Fatal("window failed to replenish", err)
	}
	now.Store(start - 1)
	if err = charge(); !errors.Is(err, mqtt.ErrDurableCapacity) {
		t.Fatal("clock rollback replenished quota", err)
	}
	if n := usageValue(t, a, "a", "accepted_messages", 0); n != 2 {
		t.Fatal("lifetime counters reset", n)
	}
}
func TestUsageConnectionsLimitTakeoverAndScopes(t *testing.T) {
	a := usageApp(t, `{"connections":1}`, `{}`)
	for _, scope := range []string{"a", "b"} {
		root := scopeTopicRoot(scope)
		scopedUser(t, a, scope, "sensor", []string{root + "#"}, []string{root + "#"})
	}
	_, code := quotaConnect(t, a.broker.Port(), "a", "first", 5)
	if code != 0 {
		t.Fatal(code)
	}
	_, code = quotaConnect(t, a.broker.Port(), "a", "second", 5)
	if code != packets.ErrQuotaExceeded.Code {
		t.Fatal("scope connection limit not rejected", code)
	}
	_, code = quotaConnect(t, a.broker.Port(), "a", "third", 4)
	if code != 3 {
		t.Fatal("legacy connection rejection", code)
	}
	_, code = quotaConnect(t, a.broker.Port(), "a", "first", 5)
	if code != 0 {
		t.Fatal("takeover couldn't reuse slot", code)
	}
	_, code = quotaConnect(t, a.broker.Port(), "b", "other-scope", 5)
	if code != 0 {
		t.Fatal("scope a blocked b", code)
	}
	var active int
	a.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_usage_connections WHERE scope_id='a'`).Scan(&active)
	if active != 1 {
		t.Fatal("old disconnect erased new reservation", active)
	}
	if n := usageValue(t, a, "a", "connection_admissions", 0); n != 2 {
		t.Fatal(n)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		count := 0
		a.authenticatedClients.Range(func(_, _ any) bool { count++; return true })
		if count == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("refused or replaced transports retained authenticated snapshots")
}
func TestUsageMQTTIngressQuotaAllVersionsAndQoS(t *testing.T) {
	for _, protocol := range []byte{4, 5} {
		for _, qos := range []byte{0, 1, 2} {
			t.Run(fmt.Sprintf("v%d-q%d", protocol, qos), func(t *testing.T) {
				a := usageApp(t, `{"mqtt_ingress_messages":1}`, `{}`)
				root := scopeTopicRoot("a")
				scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{})
				conn, code := quotaConnect(t, a.broker.Port(), "a", "sender", protocol)
				if code != 0 {
					t.Fatal(code)
				}
				pk := packets.Packet{ProtocolVersion: protocol, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: qos, Retain: true}, PacketID: 1, TopicName: root + "state", Payload: []byte("first")}
				wireWrite(t, conn, pk)
				if qos == 1 {
					requireWire(t, conn, packets.Puback)
				} else if qos == 2 {
					requireWire(t, conn, packets.Pubrec)
				} else {
					wireWrite(t, conn, packets.Packet{ProtocolVersion: protocol, FixedHeader: packets.FixedHeader{Type: packets.Pingreq}})
					requireWire(t, conn, packets.Pingresp)
				}
				pk.PacketID = 2
				pk.Payload = []byte("denied")
				wireWrite(t, conn, pk)
				typ, body, err := wireRead(conn)
				if err == nil && (typ != packets.Disconnect || protocol != 5 || len(body) == 0 || body[0] != packets.ErrQuotaExceeded.Code) {
					t.Fatal("quota wasn't explicit before ack", typ, body)
				}
				if n := usageValue(t, a, "a", "mqtt_ingress_messages", 0); n != 1 {
					t.Fatal(n)
				}
				if n := usageValue(t, a, "a", "accepted_messages", 0); n != 1 {
					t.Fatal(n)
				}
				record, err := getRetainedMessage(a.ctx.AppDB(), "a", root+"state")
				if err != nil || record.Payload != "first" {
					t.Fatal("rejected transport publish mutated retained", record, err)
				}
			})
		}
	}
}
func TestUsageEgressReservationAndReplayQuota(t *testing.T) {
	a := usageApp(t, `{"mqtt_egress_messages":1}`, `{}`)
	root := scopeTopicRoot("a")
	u := scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{root + "#"})
	conn, _ := persistentConnect(t, a, u, "subscriber", 5, false)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 1}}})
	requireWire(t, conn, packets.Suback)
	if err := a.broker.Publish(root+"state", []byte("recover"), false, 1); err != nil {
		t.Fatal(err)
	}
	body := requireWire(t, conn, packets.Publish)
	if n := usageValue(t, a, "a", "mqtt_egress_bytes", 0); n != int64(mqtt.PacketBytes(packets.Packet{FixedHeader: packets.FixedHeader{Remaining: len(body)}})) {
		t.Fatal("egress bytes did not match encoded MQTT packet", n)
	}
	conn.Close()
	if n := usageValue(t, a, "a", "mqtt_egress_messages", 0); n != 1 {
		t.Fatal(n)
	}
	replay, _ := persistentConnect(t, a, u, "subscriber", 5, false)
	if typ, _, err := wireRead(replay); err == nil {
		t.Fatal("egress quota bypassed by reconnect", typ)
	}
	var wirePacket = packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1, Remaining: len(body)}}
	if err := wirePacket.PublishDecode(body); err != nil {
		t.Fatal(err)
	}
	cl, _ := a.broker.server.Clients.Get("subscriber")
	if cl.State.Inflight.Len() != 1 {
		t.Fatal("egress rejection discarded durable packet")
	}
	if n := usageValue(t, a, "a", "mqtt_egress_messages", 0); n != 1 {
		t.Fatal("rejected write consumed admitted counter", n)
	}
	if a.durable.failed.Load() {
		t.Fatal("egress quota disabled healthy broker")
	}
}
func TestUsageStorageFailureCannotAcknowledge(t *testing.T) {
	a := usageApp(t, `{}`, `{}`)
	root := scopeTopicRoot("a")
	scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{})
	conn, code := quotaConnect(t, a.broker.Port(), "a", "sender", 5)
	if code != 0 {
		t.Fatal(code)
	}
	if _, err := a.ctx.AppDB().Exec(`CREATE TRIGGER fail_usage BEFORE INSERT ON mqtt_usage_counters BEGIN SELECT RAISE(ABORT,'simulated usage failure'); END`); err != nil {
		t.Fatal(err)
	}
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, PacketID: 1, TopicName: root + "state", Payload: []byte("must reject")})
	typ, _, err := wireRead(conn)
	if err == nil && typ == packets.Puback {
		t.Fatal("usage persistence failure acknowledged")
	}
	var n int
	a.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal`).Scan(&n)
	if n != 0 || !a.durable.failed.Load() {
		t.Fatal("usage storage failure not closed", n)
	}
}
func TestUsageCrashCountersAndBudgetsSurvive(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	t.Setenv("MQTT_TEST_QUOTAS", `{"accepted_messages":1,"connection_admissions":2,"connections":1,"mqtt_ingress_messages":2}`)
	db := filepath.Join(t.TempDir(), "mqtt.db")
	first := startChildBroker(t, db, true)
	conn, _ := childConnect(t, first, "publisher")
	pk := packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 2}, PacketID: 7, TopicName: scopeTopicRoot("a") + "state", Payload: []byte("durable")}
	wireWrite(t, conn, pk)
	if typ, _, err := wireRead(conn); err == nil {
		t.Fatal("crash fixture acknowledged", typ)
	}
	err := first.cmd.Wait()
	first.stopped = true
	var ex *exec.ExitError
	if !errors.As(err, &ex) || ex.ExitCode() != 86 {
		t.Fatal(err)
	}
	second := startChildBroker(t, db, false)
	conn, _ = childConnect(t, second, "publisher")
	pk.FixedHeader.Dup = true
	wireWrite(t, conn, pk)
	requireWire(t, conn, packets.Pubrec)
	// Recovery receipt was transport usage, but not another durable acceptance.
	second.stop()
	third := startChildBroker(t, db, false)
	_, code := quotaConnect(t, third.port, "a", "new-client", 5)
	if code != packets.ErrQuotaExceeded.Code {
		t.Fatal("restart reset connection admission window", code)
	}
	third.stop()
	ctx := diskContext(t, db)
	a := &App{ctx: ctx}
	if n := usageValue(t, a, "a", "accepted_messages", 0); n != 1 {
		t.Fatal("acceptance counter lost/doubled", n)
	}
	if n := usageValue(t, a, "a", "mqtt_ingress_messages", 0); n != 2 {
		t.Fatal("retry transport was not accounted", n)
	}
	if n := usageValue(t, a, "a", "connection_admissions", 0); n != 2 {
		t.Fatal(n)
	}
	var active int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_usage_connections`).Scan(&active)
	if active != 0 {
		t.Fatal("crashed transport slots resurrected", active)
	}
}
func TestUsageOnlyOneBrokerOwnsDatabase(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	path := filepath.Join(t.TempDir(), "mqtt.db")
	ctx := diskContext(t, path)
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.broker.Serve(run) }()
	t.Cleanup(func() { cancel(); a.broker.Close(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for !a.durable.ready.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	root := scopeTopicRoot("a")
	scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{})
	_, code := quotaConnect(t, a.broker.Port(), "a", "client", 5)
	if code != 0 {
		t.Fatal(code)
	}
	second := &App{}
	other := diskContext(t, path)
	if err := second.OnMount(other); err == nil {
		second.broker.Close()
		t.Fatal("second broker opened the same durable DB")
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_usage_connections`).Scan(&n)
	if n != 1 {
		t.Fatal("failed second mount cleared live slots", n)
	}
	if err := a.broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.OnMount(other); err != nil {
		t.Fatal("clean shutdown failed to release ownership", err)
	}
	second.broker.Close()
}
func TestUsageHTTPMCPAndConfigurationBoundaries(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a := usageApp(t, `{}`, `{"a":{"payload_bytes":3,"accepted_messages":1}}`)
	if err := a.broker.Publish(scopeTopicRoot("b")+"secret", []byte("hidden"), false, 1); err != nil {
		t.Fatal(err)
	}
	oversized := appRequest(t, a, "POST", "/test_publish", `{"topic":"`+scopeTopicRoot("a")+`state","payload":"four"}`, "a")
	if oversized.Code != 413 {
		t.Fatal("scoped payload limit not HTTP 413", oversized.Code, oversized.Body.String())
	}
	response := appRequest(t, a, "GET", "/usage?scope_id=b", "", "a")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["scope_id"] != "a" {
		t.Fatal("caller-selected scope", result)
	}
	if response = appRequest(t, a, "POST", "/usage", `{"quota_defaults_json":{}}`, "a"); response.Code != 405 {
		t.Fatal("untrusted quota write accepted")
	}
	var tool sdk.Tool
	for _, candidate := range a.MCPTools() {
		if candidate.Name == "mqtt_usage" {
			tool = candidate
		}
	}
	out, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "a", AppInstallID: 1}), a.ctx, map[string]any{"scope_id": "b", "accepted_messages": 0})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["scope_id"] != "a" {
		t.Fatal("MCP scope leak", out)
	}
	for _, raw := range []string{`null`, `[]`, `{"unknown":1}`, `{"connections":-1}`, `{"accepted_messages":1.5}`, `{"connections":"2"}`, `{"connections":9007199254740992}`, `{} {}`} {
		if _, err := decodePolicy(raw); err == nil {
			t.Fatal("invalid policy accepted", raw)
		}
	}
}

func TestUsageExactIngressBytesAndOversizePayload(t *testing.T) {
	root := scopeTopicRoot("a")
	pk := packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, PacketID: 1, TopicName: root + "state", Payload: []byte("body")}
	var encoded bytes.Buffer
	if err := pk.PublishEncode(&encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.Bytes()[1] >= 128 {
		t.Fatal("fixture needs a short Remaining Length")
	}
	wire := append([]byte{encoded.Bytes()[0], encoded.Bytes()[1] | 128, 0}, encoded.Bytes()[2:]...)
	a := usageApp(t, fmt.Sprintf(`{"mqtt_ingress_bytes":%d,"payload_bytes":4}`, len(wire)), `{}`)
	scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{})
	conn, code := quotaConnect(t, a.broker.Port(), "a", "sender", 5)
	if code != 0 {
		t.Fatal(code)
	}
	if _, err := conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	requireWire(t, conn, packets.Puback)
	if got := usageValue(t, a, "a", "mqtt_ingress_bytes", 0); got != int64(len(wire)) {
		t.Fatal("overlong header was undercounted", got, len(wire))
	}
	wireWrite(t, conn, pk)
	typ, body, err := wireRead(conn)
	if err == nil && (typ != packets.Disconnect || len(body) == 0 || body[0] != packets.ErrQuotaExceeded.Code) {
		t.Fatal("byte quota did not reject", typ, body)
	}
	// Scope policy independently applies to payloads on a fresh connection.
	another, code := quotaConnect(t, a.broker.Port(), "a", "oversized", 5)
	if code != 0 {
		t.Fatal(code)
	}
	pk.Payload = []byte("large")
	wireWrite(t, another, pk)
	typ, body, err = wireRead(another)
	if err == nil && (typ != packets.Disconnect || len(body) == 0 || body[0] != packets.ErrPacketTooLarge.Code) {
		t.Fatal("payload quota wasn't explicit", typ, body)
	}
}
func TestUsageConcurrentConnectionsCannotOverspend(t *testing.T) {
	a := usageApp(t, `{"connections":1}`, `{}`)
	root := scopeTopicRoot("a")
	scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{})
	var wg sync.WaitGroup
	var admitted atomic.Int64
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, code := quotaConnect(t, a.broker.Port(), "a", fmt.Sprint("client-", i), 5)
			if code == 0 {
				admitted.Add(1)
			} else if code != packets.ErrQuotaExceeded.Code {
				t.Errorf("unexpected rejection %d", code)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 || usageValue(t, a, "a", "peak_connections", 0) != 1 {
		t.Fatal("connection quota overspent", admitted.Load())
	}
}
func TestUsageAcceptanceCounterSharesJournalTransaction(t *testing.T) {
	a := usageApp(t, `{"accepted_messages":1}`, `{}`)
	if _, err := a.ctx.AppDB().Exec(`CREATE TRIGGER fail_journal BEFORE INSERT ON mqtt_journal BEGIN SELECT RAISE(ABORT,'no space'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.broker.Publish(scopeTopicRoot("a")+"state", []byte("rollback"), true, 1); err == nil {
		t.Fatal("failed journal accepted")
	}
	var n int64
	if err := a.ctx.AppDB().QueryRow(`SELECT coalesce(sum(value),0) FROM mqtt_usage_counters WHERE scope_id='a' AND metric='accepted_messages' AND period_start=0`).Scan(&n); err != nil || n != 0 {
		t.Fatal("rolled-back acceptance consumed quota", n, err)
	}
}
func startUsageDiskApp(t *testing.T, ctx *sdk.AppCtx) *App {
	t.Helper()
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.broker.Serve(run) }()
	t.Cleanup(func() {
		cancel()
		a.broker.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !a.durable.ready.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !a.durable.ready.Load() {
		t.Fatal("disk broker not ready")
	}
	return a
}
func TestUsageQuotaDeferredWillSurvivesAndStaysScoped(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	t.Setenv("MQTT_TEST_QUOTAS", `{"accepted_messages":1}`)
	path := filepath.Join(t.TempDir(), "mqtt.db")
	a := startUsageDiskApp(t, diskContext(t, path))
	root := scopeTopicRoot("a")
	scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{})
	if err := a.broker.Publish(root+"first", []byte("fill quota"), false, 1); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", a.broker.Port()), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Connect}, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: "will", Clean: true, Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte("YQ:sensor"), Password: []byte("test-password"), WillFlag: true, WillTopic: root + "will", WillQos: 1, WillPayload: []byte("offline")}})
	requireWire(t, conn, packets.Connack)
	conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	var pending int
	for time.Now().Before(deadline) {
		a.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_sessions WHERE client_id='will' AND json_extract(snapshot,'$.PendingWill')=1`).Scan(&pending)
		if pending == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pending != 1 || a.durable.failed.Load() {
		t.Fatal("quota-denied will was lost or disabled broker", pending)
	}
	if err = a.broker.Publish(scopeTopicRoot("b")+"state", []byte("unaffected"), false, 1); err != nil {
		t.Fatal("scope quota poisoned other scope", err)
	}
	a.broker.Close()
	second := startUsageDiskApp(t, diskContext(t, path))
	second.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_sessions WHERE client_id='will' AND json_extract(snapshot,'$.PendingWill')=1`).Scan(&pending)
	if pending != 1 || second.durable.failed.Load() {
		t.Fatal("pending will did not recover", pending)
	}
	var end int64
	second.ctx.AppDB().QueryRow(`SELECT ends_at FROM mqtt_usage_windows WHERE scope_id='a'`).Scan(&end)
	second.usage.now = func() time.Time { return time.Unix(end+1, 0) }
	// Reconnect forces a pending will retry before inheriting/replacing its shell.
	_, code := quotaConnect(t, second.broker.Port(), "a", "will", 5)
	if code != 0 {
		t.Fatal("will did not resume at quota renewal", code)
	}
	var n int
	second.ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal WHERE topic=?`, root+"will").Scan(&n)
	if n != 1 {
		t.Fatal("pending will lost/duplicated", n)
	}
	if got := usageValue(t, second, "a", "accepted_messages", 0); got != 2 {
		t.Fatal("will accounting lost", got)
	}
}

func TestUsageMigrationBackfillsRetainedAndTracksScopeMoves(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.Contains(path, "005_") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	topic := scopeTopicRoot("a") + "é"
	if _, err = db.Exec(`INSERT INTO mqtt_retained(scope_id,topic,payload,properties) VALUES(?,?,?,?)`, "a", topic, []byte{0, 255, 1}, `{"content":"é"}`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/005_usage.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var count, size int64
	if err = db.QueryRow(`SELECT messages,bytes FROM mqtt_retained_usage WHERE scope_id='a'`).Scan(&count, &size); err != nil || count != 1 || size != int64(len(topic)+3+len(`{"content":"é"}`)) {
		t.Fatal("legacy retained storage not backfilled as bytes", count, size, err)
	}
	other := scopeTopicRoot("b") + "moved"
	if _, err = db.Exec(`UPDATE mqtt_retained SET scope_id='b',topic=? WHERE topic=?`, other, topic); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT messages,bytes FROM mqtt_retained_usage WHERE scope_id='a'`).Scan(&count, &size); err != nil || count != 0 || size != 0 {
		t.Fatal("scope move retained old usage", count, size, err)
	}
	if err = db.QueryRow(`SELECT messages,bytes FROM mqtt_retained_usage WHERE scope_id='b'`).Scan(&count, &size); err != nil || count != 1 || size != int64(len(other)+3+len(`{"content":"é"}`)) {
		t.Fatal("scope move lost usage", count, size, err)
	}
	if _, err = db.Exec(`DELETE FROM mqtt_retained`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT messages,bytes FROM mqtt_retained_usage WHERE scope_id='b'`).Scan(&count, &size); err != nil || count != 0 || size != 0 {
		t.Fatal("retained deletion left usage", count, size, err)
	}
}
