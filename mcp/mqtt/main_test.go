// Tests pinning MQTT app contracts:
//
//   * topic ACL glob matching is MQTT-conformant (+ one level, # rest)
//   * bcrypt verify round-trips
//   * port-busy fallback returns a different free port
//   * HA discovery JSON populates mqtt_devices
//   * panel-state contract: every section the panel shows must
//     correspond to a real backend route or the UI breaks silently
//   * manifest parses against the SDK
//
// These intentionally avoid bringing the broker up — that path
// requires socket binding which is fine in CI but slow. Each test
// targets a single helper.

package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	"github.com/mochi-mqtt/server/v2/packets"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	body, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestTopicMatch covers every wildcard rule the broker depends on.
// Failures here mean ACLs silently let the wrong traffic through.
func TestTopicMatch(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		// exact
		{"foo/bar", "foo/bar", true},
		{"foo/bar", "foo/baz", false},
		{"foo", "foo/bar", false},
		// + one level
		{"foo/+", "foo/bar", true},
		{"foo/+", "foo/bar/baz", false},
		{"+/bar", "foo/bar", true},
		{"+/+", "foo/bar", true},
		// # remainder. Per MQTT 3.1.1 §4.7.1.2 a single "#" at the end
		// matches everything from that level INCLUDING the parent
		// level, so "foo/#" matches "foo" as well as "foo/bar".
		{"foo/#", "foo", true},
		{"foo/#", "foo/bar", true},
		{"foo/#", "foo/bar/baz", true},
		{"#", "anything/at/all", true},
		// case-sensitive
		{"foo/bar", "Foo/bar", false},
	}
	for _, c := range cases {
		got := mqttTopicMatch(c.filter, c.topic)
		if got != c.want {
			t.Errorf("mqttTopicMatch(%q, %q) = %v, want %v", c.filter, c.topic, got, c.want)
		}
	}
}

// TestUserAuth_AddListVerify — round-trip a user through the DB
// helpers and verify the password hashes correctly with bcrypt.
func TestUserAuth_AddListVerify(t *testing.T) {
	db := openTestDB(t)
	if err := addUser(db, "alice", "swordfish", []string{"home/+"}, []string{"#"}); err != nil {
		t.Fatal(err)
	}
	got, err := getUser(db, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("user not found")
	}
	if !got.Enabled {
		t.Errorf("expected enabled by default")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(got.passwordHash), []byte("swordfish")); err != nil {
		t.Errorf("password verify failed: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(got.passwordHash), []byte("wrong")); err == nil {
		t.Errorf("wrong password verified successfully")
	}
	if len(got.AllowPublishTopics) != 1 || got.AllowPublishTopics[0] != "home/+" {
		t.Errorf("publish ACL = %v", got.AllowPublishTopics)
	}
}

// TestPortFallback — listen on a port, then ask pickListenerPort
// for that same port; should return a different free one without
// erroring out, mirroring the torrent-engine fallback behavior.
func TestPortFallback(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port
	ctx := testkit.NewAppCtx(t, "apteva.yaml",
		testkit.WithProjectID("test"),
		testkit.WithConfig(map[string]string{"listen_port": strconv.Itoa(busy)}),
	)
	app := &App{ctx: ctx}
	got, err := pickListenerPort(app)
	if err != nil {
		t.Fatalf("pickListenerPort: %v", err)
	}
	if got == busy {
		t.Errorf("returned busy port %d; expected fallback to a different one", busy)
	}
	if got <= 0 {
		t.Errorf("invalid port %d", got)
	}
}

// TestHADiscoveryParser ensures the convention parser pulls fields
// out of a typical HA-format payload and writes a row.
func TestHADiscoveryParser(t *testing.T) {
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("proj-test"))
	app := &App{ctx: ctx}
	const fixture = `{
		"name":"Living Room Light","unique_id":"lr1",
		"state_topic":"home/livingroom/light/state",
		"command_topic":"home/livingroom/light/set",
		"device":{"manufacturer":"Aqara","model":"ZB-CL01","name":"Aqara CL01","identifiers":["aqara-cl01-1"]}
	}`
	app.handleHAConfig("homeassistant/light/livingroom/config", []byte(fixture))

	var name, manuf, model, st string
	err := ctx.AppDB().QueryRow(`SELECT display_name, manufacturer, model, state_topic
		FROM mqtt_devices WHERE slug = 'homeassistant/light/livingroom'`).
		Scan(&name, &manuf, &model, &st)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "Living Room Light" || manuf != "Aqara" || model != "ZB-CL01" {
		t.Errorf("got %q / %q / %q", name, manuf, model)
	}
	if st != "home/livingroom/light/state" {
		t.Errorf("state_topic = %q", st)
	}

	// Empty payload should remove.
	app.handleHAConfig("homeassistant/light/livingroom/config", []byte(""))
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM mqtt_devices`).Scan(&n)
	if n != 0 {
		t.Errorf("empty payload didn't delete: %d rows remaining", n)
	}
}

func TestRetainedMessagesRoundTripThroughSQLite(t *testing.T) {
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("proj-test"))
	app := &App{ctx: ctx}
	hook := &retainedHook{app: app}
	pk := packets.Packet{
		TopicName: "devices/lamp/state", Payload: []byte(`{"on":true}`),
		FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1, Retain: true},
	}
	hook.OnRetainMessage(nil, pk, 1)
	stored, err := hook.StoredRetainedMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].TopicName != pk.TopicName || string(stored[0].Payload) != string(pk.Payload) {
		t.Fatalf("stored retained = %#v", stored)
	}
	hook.OnRetainMessage(nil, packets.Packet{TopicName: pk.TopicName}, -1)
	stored, err = hook.StoredRetainedMessages()
	if err != nil || len(stored) != 0 {
		t.Fatalf("retained delete: len=%d err=%v", len(stored), err)
	}
}

func TestBusSubscriptionUpsertReturnsStableIDAndScopesByProject(t *testing.T) {
	db := openTestDB(t)
	first, err := addBusSubscription(db, "p1", "devices/+/state", "device.state", "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := addBusSubscription(db, "p1", "devices/+/state", "device.state", "test")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID <= 0 || second.ID != first.ID {
		t.Fatalf("IDs first=%d second=%d", first.ID, second.ID)
	}
	if _, err := addBusSubscription(db, "p2", "devices/+/state", "device.state", "test"); err != nil {
		t.Fatal(err)
	}
	subs, err := listBusSubscriptions(db, "p1")
	if err != nil || len(subs) != 1 || subs[0].ProjectID != "p1" {
		t.Fatalf("p1 subscriptions=%#v err=%v", subs, err)
	}
}

func TestPublishValidation(t *testing.T) {
	for _, tc := range []struct {
		topic string
		qos   int
		ok    bool
	}{
		{"devices/lamp/set", 0, true},
		{"devices/+/set", 0, false},
		{"", 0, false},
		{"devices/lamp/set", 3, false},
	} {
		if got := validatePublish(tc.topic, tc.qos) == nil; got != tc.ok {
			t.Errorf("validatePublish(%q,%d) ok=%v, want %v", tc.topic, tc.qos, got, tc.ok)
		}
	}
}

func TestBrokerRawTCPPublishReachesEventBusAndLog(t *testing.T) {
	recorder := testkit.NewEmitRecorder()
	ctx := testkit.NewAppCtx(t, "apteva.yaml",
		testkit.WithProjectID("proj-test"),
		testkit.WithEmitter(recorder),
		testkit.WithConfig(map[string]string{
			"listen_port":          "0",
			"allow_anonymous":      "true",
			"ha_discovery_enabled": "false",
		}),
	)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	brokerDone := make(chan error, 1)
	persistenceDone := make(chan error, 1)
	go func() { brokerDone <- app.broker.Serve(workerCtx) }()
	go func() { persistenceDone <- app.runPersistence(workerCtx) }()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(app.broker.Port()))
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, _ = net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if conn != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("broker did not listen on %s", addr)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// MQTT 3.1.1 CONNECT with clean-session and client id "test1".
	connectPacket := []byte{
		0x10, 0x11,
		0x00, 0x04, 'M', 'Q', 'T', 'T', 0x04, 0x02, 0x00, 0x3c,
		0x00, 0x05, 't', 'e', 's', 't', '1',
	}
	if _, err := conn.Write(connectPacket); err != nil {
		t.Fatal(err)
	}
	connack := make([]byte, 4)
	if _, err := io.ReadFull(conn, connack); err != nil {
		t.Fatal(err)
	}
	if connack[0] != 0x20 || connack[3] != 0x00 {
		t.Fatalf("CONNACK = %x", connack)
	}

	topic := "devices/test/state"
	payload := []byte(`{"on":true}`)
	remaining := 2 + len(topic) + len(payload)
	if remaining >= 128 {
		t.Fatal("test packet unexpectedly needs multi-byte remaining length")
	}
	publish := []byte{0x30, byte(remaining), 0, 0}
	binary.BigEndian.PutUint16(publish[2:4], uint16(len(topic)))
	publish = append(publish, topic...)
	publish = append(publish, payload...)
	if _, err := conn.Write(publish); err != nil {
		t.Fatal(err)
	}

	var data map[string]any
	eventDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(eventDeadline) {
		for _, event := range recorder.EventsByTopic("mqtt.message") {
			candidate, _ := event.Data.(map[string]any)
			if candidate["topic"] == topic {
				data = candidate
				break
			}
		}
		if data != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if data == nil || data["payload"] != string(payload) {
		t.Fatalf("matching mqtt.message data = %#v", data)
	}
	logDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(logDeadline) {
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM mqtt_message_log WHERE topic = ?`, topic).Scan(&count); err == nil && count == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM mqtt_message_log WHERE topic = ?`, topic).Scan(&count); err != nil || count != 1 {
		t.Fatalf("message log count=%d err=%v", count, err)
	}

	cancel()
	select {
	case err := <-brokerDone:
		if err != nil {
			t.Fatalf("broker shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not stop")
	}
	select {
	case err := <-persistenceDone:
		if err != nil {
			t.Fatalf("persistence shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("persistence worker did not stop")
	}
}

// TestManifestValidates — the same belt-and-braces check we do for
// torrent. The embedded const must round-trip through ParseManifest.
func TestManifestValidates(t *testing.T) {
	handlers := (&App{}).EventHandlers()
	if len(handlers) != 1 || handlers[0].Event != "mqtt.publish_request" || handlers[0].Handler == nil {
		t.Fatalf("event handlers=%#v", handlers)
	}
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("embedded manifest invalid: %v", err)
	}
	if len(m.Provides.Publishes) != 1 || m.Provides.Publishes[0].Name != "mqtt.message" {
		t.Fatalf("publishes=%#v", m.Provides.Publishes)
	}
	if len(m.Runtime.Ports) != 1 || m.Runtime.Ports[0].ContainerPort != 1883 {
		t.Fatalf("embedded runtime ports=%#v", m.Runtime.Ports)
	}
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fileManifest, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatalf("apteva.yaml invalid: %v", err)
	}
	if len(fileManifest.Runtime.Ports) != 1 || fileManifest.Runtime.Ports[0].ContainerPort != 1883 {
		t.Fatalf("runtime ports=%#v", fileManifest.Runtime.Ports)
	}
}

// TestPanelContract — every backend route the panel hits must be
// declared in HTTPRoutes(); panel reads of a missing route hang the
// install in "Loading…" forever. Defensive twin to torrent's
// TestPanelStateContract.
func TestPanelContract(t *testing.T) {
	app := &App{}
	declared := map[string]bool{}
	for _, r := range app.HTTPRoutes() {
		declared[r.Pattern] = true
	}
	body, err := os.ReadFile("ui/MQTTPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	tsx := string(body)
	if !strings.Contains(tsx, "/_install/${encodeURIComponent(String(installId))}") {
		t.Error("panel API must select its exact install; unscoped project installs are rejected by the app proxy")
	}
	// Every fetch(`${API}/foo…`) — extract the literal path segment
	// and assert the route exists. Simple-grep, not a parser; good
	// enough until someone aliases API.
	for _, want := range []string{"/status", "/clients", "/messages", "/users", "/subscriptions", "/devices", "/test_publish"} {
		if !strings.Contains(tsx, "${API}"+want) {
			t.Errorf("panel doesn't fetch %s — drift?", want)
		}
		// Either an exact route or a prefix-route exists.
		if !declared[want] && !declared[want+"/"] {
			t.Errorf("backend missing route %s — panel will hang", want)
		}
	}
}
