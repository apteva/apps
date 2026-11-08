package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
)

func consumerCall(t *testing.T, a *App, owner string, args map[string]any) map[string]any {
	t.Helper()
	v, err := a.consumerAction("a", owner, args)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}
func TestDurableConsumersLeaseRetryReplayAndIsolation(t *testing.T) {
	a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	consumerCall(t, a, "app:1", map[string]any{"action": "create", "name": "worker", "topic_filter": root + "#"})
	payload := make([]byte, 16384)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	id, err := a.broker.PublishMessage(root+"binary", payload, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	batch := consumerCall(t, a, "app:1", map[string]any{"action": "claim", "name": "worker"})
	messages := batch["messages"].([]durableMessage)
	if len(messages) != 1 || messages[0].ID != id || messages[0].Payload != base64.StdEncoding.EncodeToString(payload) {
		t.Fatal("full durable body/identity lost", batch)
	}
	if _, err = a.consumerAction("a", "app:2", map[string]any{"action": "ack", "name": "worker", "lease_token": batch["lease_token"]}); err == nil {
		t.Fatal("another app acknowledged consumer")
	}
	if _, err = a.consumerAction("b", "app:1", map[string]any{"action": "ack", "name": "worker", "lease_token": batch["lease_token"]}); err == nil {
		t.Fatal("another scope acknowledged consumer")
	}
	if _, err = a.consumerAction("a", "app:1", map[string]any{"action": "ack", "name": "worker", "lease_token": "forged"}); err == nil {
		t.Fatal("forged token accepted")
	}
	consumerCall(t, a, "app:1", map[string]any{"action": "retry", "name": "worker", "lease_token": batch["lease_token"]})
	next := consumerCall(t, a, "app:1", map[string]any{"action": "claim", "name": "worker"})
	if next["messages"].([]durableMessage)[0].ID != id || next["lease_token"] == batch["lease_token"] {
		t.Fatal("retry lost identity or reused token")
	}
	// The process-local App is replaced; cursor and lease remain in SQLite.
	restarted := &App{ctx: ctx}
	if _, err = restarted.consumerAction("a", "app:1", map[string]any{"action": "claim", "name": "worker"}); err == nil {
		t.Fatal("live lease was lost on restart")
	}
	if _, err = ctx.AppDB().Exec(`UPDATE mqtt_consumers SET lease_until=unixepoch()-1`); err != nil {
		t.Fatal(err)
	}
	reclaimed := consumerCall(t, restarted, "app:1", map[string]any{"action": "claim", "name": "worker"})
	if reclaimed["messages"].([]durableMessage)[0].ID != id {
		t.Fatal("expired lease lost message")
	}
	consumerCall(t, restarted, "app:1", map[string]any{"action": "ack", "name": "worker", "lease_token": reclaimed["lease_token"]})
	empty := consumerCall(t, restarted, "app:1", map[string]any{"action": "claim", "name": "worker"})
	if len(empty["messages"].([]durableMessage)) != 0 {
		t.Fatal("ack wasn't durable")
	}
	consumerCall(t, restarted, "app:1", map[string]any{"action": "replay", "name": "worker", "after_sequence": "0"})
	replay := consumerCall(t, restarted, "app:1", map[string]any{"action": "claim", "name": "worker"})
	if replay["messages"].([]durableMessage)[0].ID != id {
		t.Fatal("replay changed identifier")
	}
	if _, err = ctx.AppDB().Exec(`UPDATE mqtt_journal SET accepted_at=unixepoch()-1000000`); err != nil {
		t.Fatal(err)
	}
	if err = a.pruneJournal(); err != nil {
		t.Fatal(err)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal`).Scan(&count)
	if count != 1 {
		t.Fatal("unacknowledged consumer did not pin history")
	}
	consumerCall(t, restarted, "app:1", map[string]any{"action": "ack", "name": "worker", "lease_token": replay["lease_token"]})
	if err = a.pruneJournal(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.consumerAction("a", "app:1", map[string]any{"action": "replay", "name": "worker", "after_sequence": "0"}); err == nil {
		t.Fatal("pruned replay silently skipped history")
	}
}

func diskContext(t *testing.T, path string) *sdk.AppCtx {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	var initialized int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='mqtt_journal'`).Scan(&initialized); err != nil {
		t.Fatal(err)
	}
	if initialized == 0 {
		paths, err := filepath.Glob("migrations/*.sql")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(string(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(m, db, sdk.Config{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"}, nil, nil)
	ctx.SetEmitter(testkit.NewEmitRecorder())
	return ctx
}

type crashPersistence struct{ *durableStore }

func (c *crashPersistence) Commit(s *mqtt.Server, cl *mqtt.Client, pk *packets.Packet, plan []mqtt.Delivery) error {
	if err := c.durableStore.Commit(s, cl, pk, plan); err != nil {
		return err
	}
	if pk != nil {
		os.Exit(86)
	} // Deliberately bypass all defers and broker shutdown.
	return nil
}
func TestDurableCrashHelper(t *testing.T) {
	path := os.Getenv("MQTT_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	ctx := diskContext(t, path)
	var n int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_users`).Scan(&n)
	if n == 0 {
		if err := createUser(ctx.AppDB(), "a", "sensor", "test-password", []string{scopeTopicRoot("a") + "#"}, []string{scopeTopicRoot("a") + "#"}); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MQTT_CRASH_AFTER_COMMIT") == "1" {
		a.broker.server.Options.Persistence = &crashPersistence{a.durable}
	}
	go func() {
		if err := a.broker.Serve(context.Background()); err != nil {
			panic(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !a.durable.ready.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !a.durable.ready.Load() {
		t.Fatal("not ready")
	}
	if err := os.WriteFile(os.Getenv("MQTT_CRASH_READY"), []byte(strconv.Itoa(a.broker.Port())), 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}

type childBroker struct {
	cmd     *exec.Cmd
	port    int
	stopped bool
	logPath string
}

func startChildBroker(t *testing.T, db string, crash bool) *childBroker {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	logPath := filepath.Join(dir, "log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	cmd := exec.Command(exe, "-test.run=^TestDurableCrashHelper$", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "APTEVA_PROJECT_ID=", "MQTT_CRASH_DB="+db, "MQTT_CRASH_READY="+ready, "MQTT_CRASH_AFTER_COMMIT="+map[bool]string{true: "1", false: "0"}[crash])
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &childBroker{cmd: cmd, logPath: logPath}
	t.Cleanup(func() { child.stop() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(ready)
		if err == nil {
			child.port, _ = strconv.Atoi(string(data))
			return child
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	t.Fatalf("child failed to start: %s", data)
	return nil
}
func (c *childBroker) stop() {
	if !c.stopped {
		c.cmd.Process.Kill()
		c.cmd.Wait()
		c.stopped = true
	}
}
func childConnect(t *testing.T, c *childBroker, id string) (net.Conn, bool) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Connect}, ProtocolVersion: 5, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: id, Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte(base64.RawURLEncoding.EncodeToString([]byte("a")) + ":sensor"), Password: []byte("test-password")}, Properties: packets.Properties{SessionExpiryInterval: 3600, SessionExpiryIntervalFlag: true, ReceiveMaximum: 16}})
	typ, body, err := wireRead(conn)
	if err != nil || typ != packets.Connack || len(body) < 2 || body[1] != 0 {
		t.Fatalf("connect %s %d %x %v", id, typ, body, err)
	}
	return conn, body[0]&1 == 1
}
func requireWire(t *testing.T, conn net.Conn, want byte) []byte {
	t.Helper()
	typ, body, err := wireRead(conn)
	if err != nil || typ != want {
		t.Fatalf("want packet %d got %d %x: %v", want, typ, body, err)
	}
	return body
}
func TestDurableCrashAcceptanceAndQoS2SessionRecovery(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	db := filepath.Join(t.TempDir(), "mqtt.db")
	root := scopeTopicRoot("a")
	first := startChildBroker(t, db, false)
	subscriber, _ := childConnect(t, first, "subscriber")
	wireWrite(t, subscriber, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 2}}})
	requireWire(t, subscriber, packets.Suback)
	// SIGKILL leaves an active, persisted subscription rather than graceful state.
	first.stop()
	accepting := startChildBroker(t, db, true)
	publisher, _ := childConnect(t, accepting, "publisher")
	publish := packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 2}, PacketID: 7, TopicName: root + "binary", Payload: []byte{0, 255, 2, 3}}
	wireWrite(t, publisher, publish)
	if typ, _, err := wireRead(publisher); err == nil {
		t.Fatalf("crash point sent success before exit: %d", typ)
	}
	err := accepting.cmd.Wait()
	accepting.stopped = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		data, _ := os.ReadFile(accepting.logPath)
		t.Fatalf("unexpected crash: %v %s", err, data)
	}
	recovered := startChildBroker(t, db, false)
	sender, present := childConnect(t, recovered, "publisher")
	if !present {
		t.Fatal("incoming QoS2 session not restored")
	}
	publish.FixedHeader.Dup = true
	wireWrite(t, sender, publish)
	requireWire(t, sender, packets.Pubrec)
	wireWrite(t, sender, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pubrel, Qos: 1}, PacketID: 7})
	requireWire(t, sender, packets.Pubcomp)
	conn, present := childConnect(t, recovered, "subscriber")
	if !present {
		t.Fatal("subscription not restored")
	}
	body := requireWire(t, conn, packets.Publish)
	delivery := packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 2, Remaining: len(body)}}
	if err = delivery.PublishDecode(body); err != nil {
		t.Fatal(err)
	}
	if string(delivery.Payload) != string(publish.Payload) || mqtt.MessageID(delivery) == "" {
		t.Fatal("atomic subscriber obligation lost")
	}
	id := mqtt.MessageID(delivery)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pubrec}, PacketID: delivery.PacketID})
	requireWire(t, conn, packets.Pubrel)
	recovered.stop()
	again := startChildBroker(t, db, false)
	conn, present = childConnect(t, again, "subscriber")
	if !present {
		t.Fatal("outgoing QoS2 session lost")
	}
	requireWire(t, conn, packets.Pubrel)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pubcomp}, PacketID: delivery.PacketID})
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pingreq}})
	requireWire(t, conn, packets.Pingresp)
	again.stop()
	ctx := diskContext(t, db)
	a := &App{ctx: ctx}
	history := consumerCall(t, a, "app:1", map[string]any{"action": "history", "topic_filter": root + "#"})
	records := history["messages"].([]durableMessage)
	if len(records) != 1 || records[0].ID != id {
		t.Fatal("QoS2 retry duplicated accepted message", records)
	}
	var snapshot []byte
	if err = ctx.AppDB().QueryRow(`SELECT snapshot FROM mqtt_sessions WHERE client_id='subscriber'`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var session mqtt.Session
	if err = json.Unmarshal(snapshot, &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Outgoing) != 0 {
		t.Fatal("completed QoS2 delivery resurrected")
	}
}

func TestDurableStorageFailureNeverAcknowledgesOrPartiallyAccepts(t *testing.T) {
	for _, target := range []string{"mqtt_journal", "mqtt_retained", "mqtt_sessions"} {
		t.Run(target, func(t *testing.T) {
			a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"})
			root := scopeTopicRoot("a")
			u := scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{root + "#"})
			conn, code := wireConnect(t, a, u, "failure", 5, "")
			if code != 0 {
				t.Fatal(code)
			}
			if _, err := ctx.AppDB().Exec(`CREATE TRIGGER fail_write BEFORE INSERT ON ` + target + ` BEGIN SELECT RAISE(ABORT,'simulated disk failure'); END`); err != nil {
				t.Fatal(err)
			}
			wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1, Retain: true}, PacketID: 1, TopicName: root + "state", Payload: []byte("must not accept")})
			typ, _, err := wireRead(conn)
			if err == nil && typ == packets.Puback {
				t.Fatal("failed write acknowledged")
			}
			var count int
			ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal`).Scan(&count)
			if count != 0 {
				t.Fatal("partial journal acceptance")
			}
			ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_retained`).Scan(&count)
			if count != 0 {
				t.Fatal("partial retained acceptance")
			}
			if !a.durable.failed.Load() || a.broker.endpointStatus()["ready"] != false {
				t.Fatal("storage fault not surfaced")
			}
		})
	}
}
func TestDurableCapacityRejectsAtomicallyAndPerScope(t *testing.T) {
	a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false", "max_journal_messages_per_scope": "1"})
	root := scopeTopicRoot("a")
	if err := a.broker.Publish(root+"state", []byte("first"), true, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.broker.Publish(root+"state", []byte("discard"), true, 1); !errors.Is(err, errOverloaded) {
		t.Fatal("capacity wasn't explicit", err)
	}
	retained, err := getRetainedMessage(ctx.AppDB(), "a", root+"state")
	if err != nil || retained.Payload != "first" {
		t.Fatal("failed acceptance replaced retained", retained, err)
	}
	if a.durable.failed.Load() {
		t.Fatal("capacity should be retryable")
	}
	if err := a.broker.Publish(scopeTopicRoot("b")+"state", []byte("other"), false, 1); err != nil {
		t.Fatal("scope a consumed scope b capacity", err)
	}
}

func persistentConnect(t *testing.T, a *App, u *MQTTUser, id string, protocol byte, clean bool) (net.Conn, bool) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", a.broker.Port()), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	pk := packets.Packet{ProtocolVersion: protocol, FixedHeader: packets.FixedHeader{Type: packets.Connect}, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: id, Clean: clean, Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte(u.Username), Password: []byte("test-password")}}
	if protocol == 5 {
		pk.Properties.SessionExpiryInterval = 3600
		pk.Properties.SessionExpiryIntervalFlag = true
		pk.Properties.ReceiveMaximum = 1
	}
	wireWrite(t, conn, pk)
	body := requireWire(t, conn, packets.Connack)
	if len(body) < 2 || body[1] != 0 {
		t.Fatal("connect rejected", body)
	}
	return conn, body[0]&1 == 1
}
func TestDurableOfflineQueueBackpressureAndCleanStart(t *testing.T) {
	for _, protocol := range []byte{4, 5} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false", "max_inflight_per_client": "1"})
			root := scopeTopicRoot("a")
			u := scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{root + "#"})
			conn, _ := persistentConnect(t, a, u, "offline", protocol, false)
			wireWrite(t, conn, packets.Packet{ProtocolVersion: protocol, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 1}}})
			requireWire(t, conn, packets.Suback)
			conn.Close()
			if err := a.broker.Publish(root+"first", []byte("queued"), false, 1); err != nil {
				t.Fatal(err)
			}
			if err := a.broker.Publish(root+"second", []byte("rejected"), true, 1); !errors.Is(err, errOverloaded) {
				t.Fatal("full offline queue did not backpressure", err)
			}
			var n int
			ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal`).Scan(&n)
			if n != 1 {
				t.Fatal("partial journal acceptance")
			}
			ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_retained`).Scan(&n)
			if n != 0 {
				t.Fatal("partial retained acceptance")
			}
			fresh, present := persistentConnect(t, a, u, "offline", protocol, true)
			if present {
				t.Fatal("clean start retained old session")
			}
			wireWrite(t, fresh, packets.Packet{ProtocolVersion: protocol, FixedHeader: packets.FixedHeader{Type: packets.Pingreq}})
			requireWire(t, fresh, packets.Pingresp)
			if err := a.broker.Publish(root+"second", []byte("now accepted"), true, 1); err != nil {
				t.Fatal("clean session did not release queue", err)
			}
		})
	}
}
func TestDurableConsumerBoundariesAndPublicationReceipt(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	body := `{"action":"create","name":"http-worker","topic_filter":"` + root + `#"}`
	if response := appRequest(t, a, "POST", "/consumers", body, "a"); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := appRequest(t, a, "POST", "/consumers", body, "b"); response.Code != 400 {
		t.Fatal("foreign topic filter accepted", response.Code)
	}
	publish := appRequest(t, a, "POST", "/test_publish", `{"topic":"`+root+`state","payload":"value","qos":1}`, "a")
	var receipt map[string]any
	if err := json.Unmarshal(publish.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if publish.Code != 200 || receipt["message_id"] == "" || receipt["durable"] != true {
		t.Fatal("missing durable receipt", receipt)
	}
	var tool sdk.Tool
	for _, candidate := range a.MCPTools() {
		if candidate.Name == "mqtt_consumer" {
			tool = candidate
		}
	}
	args := map[string]any{"action": "create", "name": "mcp-worker", "topic_filter": root + "#"}
	call := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "a", AppInstallID: 1})
	if _, err := tool.HandlerCtx(call, ctx, args); err != nil {
		t.Fatal(err)
	}
	claim := map[string]any{"action": "claim", "name": "mcp-worker"}
	result, err := tool.HandlerCtx(call, ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	batch := result.(map[string]any)
	if batch["messages"].([]durableMessage)[0].ID != receipt["message_id"] {
		t.Fatal("MCP journal differs from HTTP receipt")
	}
	other := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "a", AppInstallID: 2})
	if _, err = tool.HandlerCtx(other, ctx, map[string]any{"action": "ack", "name": "mcp-worker", "lease_token": batch["lease_token"], "owner": "app:1"}); err == nil {
		t.Fatal("model argument selected another app owner")
	}
	if _, err = tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "a"}), ctx, args); err == nil {
		t.Fatal("anonymous caller owned a consumer")
	}
}
func TestDurableSessionExpiryNotExtendedByRestartAndRevocation(t *testing.T) {
	a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	u := scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{root + "#"})
	conn, _ := persistentConnect(t, a, u, "session", 5, false)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 1}}})
	requireWire(t, conn, packets.Suback)
	conn.Close()
	// Stop the broker so no housekeeping can overwrite the controlled timestamp.
	if err := a.broker.Close(); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := ctx.AppDB().QueryRow(`SELECT snapshot FROM mqtt_sessions WHERE client_id='session'`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var session mqtt.Session
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Second).Unix()
	session.Disconnected = old
	session.Properties.Props.SessionExpiryInterval = 30
	data, _ = json.Marshal(session)
	if _, err := ctx.AppDB().Exec(`UPDATE mqtt_sessions SET snapshot=?,last_seen=? WHERE client_id='session'`, data, old); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		restored := &App{ctx: ctx}
		if err := restored.reloadUserCache(); err != nil {
			t.Fatal(err)
		}
		d, err := newDurableStore(restored)
		if err != nil {
			t.Fatal(err)
		}
		restored.durable = d
		s := mqtt.New(&mqtt.Options{Persistence: d, Capabilities: mqtt.NewDefaultServerCapabilities()})
		if err = d.Restore(s); err != nil {
			t.Fatal(err)
		}
		cl, ok := s.Clients.Get("session")
		if !ok || cl.StopTime() != old {
			t.Fatal("restart extended session lifetime")
		}
	}
	if _, err := a.toolUsersRotatePassword(ctx.WithProject("a"), map[string]any{"username": u.Username, "password": "new-password"}); err != nil {
		t.Fatal(err)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_sessions`).Scan(&count)
	if count != 0 {
		t.Fatal("credential rotation left recoverable session")
	}
}

func TestDurableQoS2IndependentPacketNamespaces(t *testing.T) {
	a, _, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	u := scopedUser(t, a, "a", "sensor", []string{root + "#"}, []string{root + "#"})
	conn, _ := persistentConnect(t, a, u, "both-directions", 5, false)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 2}}})
	requireWire(t, conn, packets.Suback)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 2}, PacketID: 1, TopicName: root + "state", Payload: []byte("both")})
	seen := map[byte]bool{}
	for range 2 {
		typ, body, err := wireRead(conn)
		if err != nil {
			t.Fatal(err)
		}
		seen[typ] = true
		if typ == packets.Publish {
			var pk = packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 2, Remaining: len(body)}}
			if err = pk.PublishDecode(body); err != nil || pk.PacketID != 1 {
				t.Fatal("expected independent outgoing ID 1", pk.PacketID, err)
			}
		}
	}
	if !seen[packets.Pubrec] || !seen[packets.Publish] {
		t.Fatal("missing QoS2 handshake", seen)
	}
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pubrel, Qos: 1}, PacketID: 1})
	requireWire(t, conn, packets.Pubcomp)
	cl, _ := a.broker.server.Clients.Get("both-directions")
	if cl.State.Incoming.Len() != 0 || cl.State.Inflight.Len() != 1 {
		t.Fatal("incoming PUBREL erased outgoing packet")
	}
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pubrec}, PacketID: 1})
	requireWire(t, conn, packets.Pubrel)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pubcomp}, PacketID: 1})
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Pingreq}})
	requireWire(t, conn, packets.Pingresp)
	if cl.State.Inflight.Len() != 0 {
		t.Fatal("outgoing completion not cleared")
	}
}
func TestDurableCrashWillIsAcceptedOnce(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	db := filepath.Join(t.TempDir(), "mqtt.db")
	first := startChildBroker(t, db, false)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", first.port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	root := scopeTopicRoot("a")
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Connect}, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: "will-client", Clean: true, Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte("YQ:sensor"), Password: []byte("test-password"), WillFlag: true, WillTopic: root + "will", WillPayload: []byte("offline"), WillQos: 1, WillRetain: true}})
	requireWire(t, conn, packets.Connack)
	first.stop()
	second := startChildBroker(t, db, false)
	second.stop()
	third := startChildBroker(t, db, false)
	third.stop()
	ctx := diskContext(t, db)
	var n int
	if err = ctx.AppDB().QueryRow(`SELECT count(*) FROM mqtt_journal WHERE topic=?`, root+"will").Scan(&n); err != nil || n != 1 {
		t.Fatal("will lost or duplicated across restart", n, err)
	}
	retained, err := getRetainedMessage(ctx.AppDB(), "a", root+"will")
	if err != nil || retained.Payload != "offline" {
		t.Fatal("will retained state not committed", retained, err)
	}
}

func TestDurableRetainedSubscriptionRejectsPartialQueue(t *testing.T) {
	a, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false", "max_inflight_per_client": "1"})
	root := scopeTopicRoot("a")
	for _, topic := range []string{"one", "two"} {
		if err := a.broker.Publish(root+topic, []byte(topic), true, 1); err != nil {
			t.Fatal(err)
		}
	}
	u := scopedUser(t, a, "a", "reader", []string{}, []string{root + "#"})
	conn, _ := persistentConnect(t, a, u, "reader", 5, false)
	wireWrite(t, conn, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 1}}})
	typ, _, err := wireRead(conn)
	if err == nil && (typ == packets.Suback || typ == packets.Publish) {
		t.Fatal("subscription exposed partially committed retained batch")
	}
	var snapshot []byte
	if err = ctx.AppDB().QueryRow(`SELECT snapshot FROM mqtt_sessions WHERE client_id='reader'`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var v mqtt.Session
	if err = json.Unmarshal(snapshot, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Outgoing) != 0 || len(v.Subscriptions) != 0 {
		t.Fatal("rejected subscription altered durable state", v)
	}
}

func TestDurableLegacyQoS1CrashRecovery(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	db := filepath.Join(t.TempDir(), "mqtt.db")
	first := startChildBroker(t, db, false)
	root := scopeTopicRoot("a")
	connect := func(c *childBroker) (net.Conn, bool) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		wireWrite(t, conn, packets.Packet{ProtocolVersion: 4, FixedHeader: packets.FixedHeader{Type: packets.Connect}, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: "legacy", Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte("YQ:sensor"), Password: []byte("test-password")}})
		body := requireWire(t, conn, packets.Connack)
		if len(body) < 2 || body[1] != 0 {
			t.Fatal("legacy connect rejected", body)
		}
		return conn, body[0]&1 == 1
	}
	subscriber, _ := connect(first)
	wireWrite(t, subscriber, packets.Packet{ProtocolVersion: 4, FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: 1, Filters: packets.Subscriptions{{Filter: root + "#", Qos: 1}}})
	requireWire(t, subscriber, packets.Suback)
	publisher, _ := childConnect(t, first, "publisher")
	wireWrite(t, publisher, packets.Packet{ProtocolVersion: 5, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, PacketID: 3, TopicName: root + "legacy", Payload: []byte("recover")})
	requireWire(t, publisher, packets.Puback)
	first.stop()
	second := startChildBroker(t, db, false)
	subscriber, present := connect(second)
	if !present {
		t.Fatal("legacy session lost")
	}
	body := requireWire(t, subscriber, packets.Publish)
	pk := packets.Packet{ProtocolVersion: 4, FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1, Remaining: len(body)}}
	if err := pk.PublishDecode(body); err != nil || string(pk.Payload) != "recover" {
		t.Fatal("legacy queue lost", pk, err)
	}
	wireWrite(t, subscriber, packets.Packet{ProtocolVersion: 4, FixedHeader: packets.FixedHeader{Type: packets.Puback}, PacketID: pk.PacketID})
	wireWrite(t, subscriber, packets.Packet{ProtocolVersion: 4, FixedHeader: packets.FixedHeader{Type: packets.Pingreq}})
	requireWire(t, subscriber, packets.Pingresp)
	second.stop()
	ctx := diskContext(t, db)
	var data []byte
	if err := ctx.AppDB().QueryRow(`SELECT snapshot FROM mqtt_sessions WHERE client_id='legacy'`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var state mqtt.Session
	if err := json.Unmarshal(data, &state); err != nil || len(state.Outgoing) != 0 {
		t.Fatal("legacy acknowledgment was not durable", state, err)
	}
}
