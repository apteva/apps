package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	mqtt "github.com/apteva/apps/mcp/mqtt/internal/broker"
	"github.com/mochi-mqtt/server/v2/packets"
)

func ownershipApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithConfig(map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"}))
	return &App{ctx: ctx, messageLogCh: make(chan messageRecord, 100), aclLogCh: make(chan aclRecord, 100)}, ctx
}

func scopedUser(t *testing.T, a *App, scope, local string, pub, sub []string) *MQTTUser {
	t.Helper()
	if err := createUser(a.ctx.AppDB(), scope, local, "test-password", pub, sub); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadUserCache(); err != nil {
		t.Fatal(err)
	}
	user, err := getUser(a.ctx.AppDB(), scope, local)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestOwnershipFiltersRejectCrossScopeAndBroaderWildcards(t *testing.T) {
	root := scopeTopicRoot("a")
	u := &MQTTUser{ScopeID: "a", Enabled: true, AllowPublishTopics: []string{root + "sensors/+"}, AllowSubscribeTopics: []string{root + "sensors/+"}}
	for _, tc := range []struct {
		filter      string
		write, want bool
	}{
		{root + "sensors/one", true, true},
		{root + "sensors/+", false, true},
		{root + "sensors/#", false, false},
		{root + "sensors/+/state", false, false},
		{"#", false, false}, {"$SYS/#", false, false},
		{scopeTopicRoot("b") + "sensors/one", true, false},
		{"$share/workers/" + root + "sensors/+", false, true},
		{"$share/workers/" + scopeTopicRoot("b") + "sensors/+", false, false},
		{"$share/+/" + root + "sensors/+", false, false},
		{root + "sensors/part+ial", false, false},
		{root + "sensors\x00", false, false},
	} {
		if got := authorizedFilter(u, tc.filter, tc.write); got != tc.want {
			t.Errorf("filter %q write=%v: %v", tc.filter, tc.write, got)
		}
	}
	if !filterCovered(root+"sensors/#", root+"sensors") {
		t.Fatal("parent level should be covered by trailing #")
	}
	for _, scope := range []string{"a", "a/b", "a+/#", "é"} {
		if got := topicScope(scopeTopicRoot(scope) + "state"); got != scope {
			t.Fatalf("scope round trip: %q", got)
		}
	}
}

func TestCredentialPolicyExplicitEmptyAndNamespacedCredentials(t *testing.T) {
	a, ctx := ownershipApp(t)
	root := scopeTopicRoot("a")
	for _, args := range []map[string]any{
		{"username": "sensor"},
		{"username": "sensor", "allow_publish": nil, "allow_subscribe": []string{}},
		{"username": "sensor", "allow_publish": []any{1}, "allow_subscribe": []string{}},
		{"username": "sensor", "allow_publish": []string{"#"}, "allow_subscribe": []string{}},
		{"username": "sensor", "allow_publish": []string{scopeTopicRoot("b") + "#"}, "allow_subscribe": []string{}},
	} {
		if _, err := a.toolUsersCreate(ctx.WithProject("a"), args); err == nil {
			t.Fatalf("accepted unsafe credential args: %#v", args)
		}
	}
	result, err := a.toolUsersCreate(ctx.WithProject("a"), map[string]any{"username": "sensor", "allow_publish": []string{}, "allow_subscribe": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	created := result.(map[string]any)
	password, ok := created["password"].(string)
	if !ok || len(password) != 32 || created["topic_root"] != root {
		t.Fatal("generated bootstrap credential missing")
	}
	u, err := getUser(ctx.AppDB(), "a", "sensor")
	if err != nil || u == nil || len(u.AllowPublishTopics) != 0 || len(u.AllowSubscribeTopics) != 0 {
		t.Fatalf("empty ACL was expanded: %#v %v", u, err)
	}
	encoded, _ := json.Marshal(u)
	if bytes.Contains(encoded, []byte(password)) || bytes.Contains(encoded, []byte(u.passwordHash)) {
		t.Fatal("credential read exposed secret")
	}
	// Same local name is independent in another scope and cannot replace A.
	scopedUser(t, a, "b", "sensor", []string{}, []string{})
	if _, err := a.toolUsersAdd(ctx.WithProject("b"), map[string]any{"username": u.Username, "password": "replacement", "allow_publish": []string{}, "allow_subscribe": []string{}}); err == nil {
		t.Fatal("cross-scope legacy overwrite accepted")
	}
	if err := addUser(ctx.AppDB(), "a", "sensor", "replacement", []string{}, []string{}); err == nil {
		t.Fatal("legacy add replaced existing credential")
	}
	listed, err := listUsers(ctx.AppDB(), "a")
	if err != nil || len(listed) != 1 || listed[0].ScopeID != "a" {
		t.Fatalf("scoped users: %#v %v", listed, err)
	}
	// Every mutation is owner-qualified, including known wire usernames.
	for _, change := range []func() error{
		func() error { return deleteUser(ctx.AppDB(), "b", u.Username) },
		func() error { return rotateUserPassword(ctx.AppDB(), "b", u.Username, "replacement") },
		func() error { return setUserEnabled(ctx.AppDB(), "b", u.Username, false) },
		func() error { return updateUserACL(ctx.AppDB(), "b", u.Username, []string{}, nil) },
	} {
		if err := change(); err == nil {
			t.Fatal("cross-scope mutation succeeded")
		}
	}
}

func TestMCPUsesAuthenticatedScopeAndIgnoresArgumentScope(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a, ctx := ownershipApp(t)
	scopedUser(t, a, "a", "one", []string{}, []string{})
	scopedUser(t, a, "b", "two", []string{}, []string{})
	for _, tool := range a.MCPTools() {
		if tool.Handler != nil || tool.HandlerCtx == nil {
			t.Fatalf("tool bypasses scope boundary: %s", tool.Name)
		}
		if _, err := tool.HandlerCtx(context.Background(), ctx.WithProject("b"), map[string]any{"_project_id": "b"}); err == nil {
			t.Fatalf("%s accepted missing global caller scope", tool.Name)
		}
		if tool.Name != "mqtt_users_list" {
			continue
		}
		call := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 1, ProjectID: "a"})
		out, err := tool.HandlerCtx(call, ctx.WithProject("b"), map[string]any{"_project_id": "b", "scope_id": "b"})
		if err != nil {
			t.Fatal(err)
		}
		users := out.([]MQTTUser)
		if len(users) != 1 || users[0].ScopeID != "a" {
			t.Fatalf("argument selected another scope: %#v", users)
		}
	}
	a.ctx = ctx.WithProject("a")
	if _, err := a.toolScope(context.Background()); err == nil {
		t.Fatal("fixed install accepted an unidentified MCP caller")
	}
	if _, err := a.toolScope(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "b"})); err == nil {
		t.Fatal("fixed install accepted foreign scope")
	}
}

func TestManagementFailsClosedWithoutInstallToken(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "")
	a, ctx := ownershipApp(t)
	a.ctx = ctx.WithProject("a")
	if _, err := a.toolScope(sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "a"})); err == nil {
		t.Fatal("token-free MCP management was accepted")
	}
	if rec := appRequest(t, a, "GET", "/users", "", ""); rec.Code != http.StatusForbidden {
		t.Fatal("token-free HTTP management was accepted")
	}
}

func TestMigrationCollisionRollsBackAndRetainedPruningIsPerScope(t *testing.T) {
	a, ctx := ownershipApp(t)
	a.ctx = ctx.WithProject("a")
	_, err := ctx.AppDB().Exec(`INSERT INTO mqtt_users(username,password_hash) VALUES ('legacy','unchanged');
        INSERT INTO mqtt_retained(topic,payload) VALUES ('state','legacy');
        INSERT INTO mqtt_retained(scope_id,topic,payload) VALUES ('a',?,'owned');`, scopeTopicRoot("a")+"state")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.initializeOwnership(); err == nil {
		t.Fatal("namespace collision overwrote owned data")
	}
	var scope string
	var enabled int
	if err := ctx.AppDB().QueryRow(`SELECT scope_id,enabled FROM mqtt_users WHERE username='legacy'`).Scan(&scope, &enabled); err != nil || scope != "" || enabled != 1 {
		t.Fatalf("migration partially committed: %q %d %v", scope, enabled, err)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM mqtt_retained`); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"a", "b"} {
		for i := 0; i < 2; i++ {
			putRetainedFixture(t, a, packets.Packet{TopicName: scopeTopicRoot(scope) + fmt.Sprint(i), Payload: []byte("state")})
		}
	}
	// Both scopes retain one message. A broker-wide pruning query would leave
	// only one message in total, allowing one owner to evict the other's state.
	ctx.Config()["retain_max_count"] = "1"
	a.sweepOnce()
	for _, scope := range []string{"a", "b"} {
		messages, err := listRetainedMessages(ctx.AppDB(), scope, "", 10)
		if err != nil || len(messages) != 1 {
			t.Fatalf("scope %s lost retained state: %v %v", scope, messages, err)
		}
	}
}

func TestResponseTopicsAndDiscoveryCannotReferenceAnotherScope(t *testing.T) {
	a, ctx := ownershipApp(t)
	root := scopeTopicRoot("a")
	u := scopedUser(t, a, "a", "one", []string{root + "#"}, []string{root + "#"})
	cl := &mqtt.Client{ID: "responses"}
	hook := &aclHook{app: a}
	if !hook.OnConnectAuthenticate(cl, packets.Packet{Connect: packets.ConnectParams{Username: []byte(u.Username), Password: []byte("test-password")}}) {
		t.Fatal("authentication failed")
	}
	for _, topic := range []string{root + "reply", scopeTopicRoot("b") + "reply"} {
		_, err := hook.OnPacketRead(cl, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish}, Properties: packets.Properties{ResponseTopic: topic}})
		if (err == nil) != (topic == root+"reply") {
			t.Fatalf("response topic %s: %v", topic, err)
		}
	}
	a.handleHAConfig(root+"homeassistant/sensor/one/config", []byte(`{"state_topic":"`+scopeTopicRoot("b")+`state"}`))
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM mqtt_devices`).Scan(&count); err != nil || count != 0 {
		t.Fatal("foreign discovery reference stored")
	}
	a.broker = &Broker{}
	if err := a.handleOutboundPublishRequest(ctx.WithProject("a"), map[string]any{"topic": scopeTopicRoot("b") + "state", "payload": "attack"}); err == nil {
		t.Fatal("foreign event publish accepted")
	}
}

func signedScope(t *testing.T, r *http.Request, scope string) {
	t.Helper()
	raw, _ := json.Marshal(sdk.TrustedPrincipal{Version: sdk.TrustedPrincipalVersion, UserID: 1, ProjectID: scope, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte("test-app-token"))
	mac.Write([]byte(payload))
	r.Header.Set(sdk.HeaderTrustedPrincipal, payload)
	r.Header.Set(sdk.HeaderTrustedPrincipalSignature, hex.EncodeToString(mac.Sum(nil)))
}

func appRequest(t *testing.T, a *App, method, path, body, scope string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range a.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if scope != "" {
		signedScope(t, req, scope)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHTTPDataAndMetricsAreScoped(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	a, ctx := ownershipApp(t)
	scopedUser(t, a, "a", "one", []string{}, []string{})
	b := scopedUser(t, a, "b", "two", []string{}, []string{})
	for _, scope := range []string{"a", "b"} {
		topic := scopeTopicRoot(scope) + "private"
		putRetainedFixture(t, a, packets.Packet{TopicName: topic, Payload: []byte("secret-" + scope)})
		if err := a.persistRecords([]messageRecord{{scope: scope, topic: topic, payload: []byte("secret-" + scope), payloadSize: 8}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := addBusSubscription(ctx.AppDB(), scope, scopeTopicRoot(scope)+"#", "private", "test"); err != nil {
			t.Fatal(err)
		}
		a.counters(scope).eventsEmitted.Add(map[string]uint64{"a": 3, "b": 999}[scope])
	}
	for _, path := range []string{"/users", "/retained", "/messages", "/subscriptions", "/status", "/clients", "/devices"} {
		rec := appRequest(t, a, "GET", path+"?scope_id=b&_project_id=b", "", "a")
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret-b") || strings.Contains(rec.Body.String(), b.Username) || strings.Contains(rec.Body.String(), "999") {
			t.Fatalf("scope leak on %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	for _, method := range []string{"GET", "DELETE"} {
		rec := appRequest(t, a, method, "/retained/"+scopeTopicRoot("b")+"private", "", "a")
		if rec.Code != 404 {
			t.Fatalf("foreign retained %s returned %d", method, rec.Code)
		}
	}
	if rec := appRequest(t, a, "POST", "/test_publish", `{"topic":"`+scopeTopicRoot("b")+`private","payload":"attack"}`, "a"); rec.Code != 403 {
		t.Fatalf("foreign publish returned %d", rec.Code)
	}
	if rec := appRequest(t, a, "GET", "/users?project_id=a", "", ""); rec.Code != 403 {
		t.Fatal("unsigned global scope was accepted")
	}
	if rec := appRequest(t, a, "PATCH", "/users/one", "null", "a"); rec.Code != 400 {
		t.Fatal("null credential update was accepted")
	}
	// A supplied but invalid signature must never fall back to install scope.
	a.ctx = ctx.WithProject("a")
	req := httptest.NewRequest("GET", "/users", nil)
	signedScope(t, req, "a")
	req.Header.Set(sdk.HeaderTrustedPrincipalSignature, "bad")
	rec := httptest.NewRecorder()
	a.scopedHTTP(a.handleUsers)(rec, req)
	if rec.Code != 403 {
		t.Fatal("invalid signature fell back to fixed scope")
	}
}

func TestClientOwnershipAtomicPersistentAndCredentialRecreation(t *testing.T) {
	a, ctx := ownershipApp(t)
	one := scopedUser(t, a, "a", "one", []string{}, []string{})
	two := scopedUser(t, a, "b", "two", []string{}, []string{})
	var wg sync.WaitGroup
	winners := make(chan *MQTTUser, 2)
	for _, u := range []*MQTTUser{one, two} {
		wg.Add(1)
		go func(u *MQTTUser) {
			defer wg.Done()
			if a.bindClientID("contested", u) == nil {
				winners <- u
			}
		}(u)
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("ownership winners: %d", len(winners))
	}
	winner := <-winners
	restarted := &App{ctx: ctx}
	if err := restarted.bindClientID("contested", winner); err != nil {
		t.Fatal(err)
	}
	loser := one
	if winner.ID == one.ID {
		loser = two
	}
	if err := restarted.bindClientID("contested", loser); err == nil {
		t.Fatal("restart lost client ownership")
	}
	if err := deleteUser(ctx.AppDB(), winner.ScopeID, winner.Username); err != nil {
		t.Fatal(err)
	}
	replacement := scopedUser(t, a, winner.ScopeID, winner.Username, []string{}, []string{})
	if replacement.ID == winner.ID {
		t.Fatal("credential id reused")
	}
	if err := restarted.bindClientID("contested", replacement); err == nil {
		t.Fatal("recreated credential inherited client id")
	}
}

func TestRevokedIdentityAndWillCannotUseNewPermissions(t *testing.T) {
	a, ctx := ownershipApp(t)
	root := scopeTopicRoot("a")
	u := scopedUser(t, a, "a", "one", []string{root + "#"}, []string{root + "#"})
	cl := &mqtt.Client{ID: "client"}
	hook := &aclHook{app: a}
	connect := packets.Packet{Connect: packets.ConnectParams{Username: []byte(u.Username), Password: []byte("test-password")}}
	if !hook.OnConnectAuthenticate(cl, connect) {
		t.Fatal("auth failed")
	}
	if !hook.OnACLCheck(cl, root+"state", true) {
		t.Fatal("initial ACL failed")
	}
	if _, err := a.toolUsersUpdateACL(ctx.WithProject("a"), map[string]any{"username": u.Username, "allow_publish": []string{}, "allow_subscribe": []string{}}); err != nil {
		t.Fatal(err)
	}
	if hook.OnACLCheck(cl, root+"state", true) {
		t.Fatal("stale identity retained permissions")
	}
	will, err := hook.OnWill(cl, mqtt.Will{TopicName: root + "state", Payload: []byte("stale"), Retain: true})
	if err != nil || will.TopicName != "" || will.Retain {
		t.Fatal("revoked will was retained")
	}
	if _, err := hook.OnPacketRead(cl, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish}}); err == nil {
		t.Fatal("stale connection accepted a packet")
	}
	if err := rotateUserPassword(ctx.AppDB(), "a", u.Username, "changed-password"); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadUserCache(); err != nil {
		t.Fatal(err)
	}
	after, _ := getUser(ctx.AppDB(), "a", u.Username)
	if after.ID != u.ID || after.Revision <= u.Revision {
		t.Fatal("rotation changed identity or failed to invalidate session")
	}
	badWill := connect
	badWill.Connect.WillFlag = true
	badWill.Connect.WillTopic = scopeTopicRoot("b") + "state"
	badWill.Connect.Password = []byte("changed-password")
	if hook.OnConnectAuthenticate(&mqtt.Client{ID: "bad-will"}, badWill) {
		t.Fatal("foreign will accepted")
	}
}

func TestOwnershipMigrationQuarantinesUnknownAndDisablesKnownCredentials(t *testing.T) {
	a, ctx := ownershipApp(t)
	_, err := ctx.AppDB().Exec(`INSERT INTO mqtt_users(username,password_hash) VALUES ('legacy','never-logged');
        INSERT INTO mqtt_retained(topic,payload) VALUES ('device/state','old');
        INSERT INTO mqtt_message_log(topic,payload) VALUES ('device/state','old');
        INSERT INTO mqtt_subscriptions(project_id,topic_pattern,bus_topic) VALUES ('default','device/#','legacy');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.initializeOwnership(); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadUserCache(); err != nil {
		t.Fatal(err)
	}
	stored, err := (&retainedHook{app: a}).StoredRetainedMessages()
	if err != nil || len(stored) != 0 {
		t.Fatal("unknown retained state restored")
	}
	if (&aclHook{app: a}).OnConnectAuthenticate(&mqtt.Client{ID: "unknown"}, packets.Packet{Connect: packets.ConnectParams{Username: []byte("legacy"), Password: []byte("never-logged")}}) {
		t.Fatal("unknown credential authenticated")
	}
	a.ctx = ctx.WithProject("a")
	if err := a.initializeOwnership(); err != nil {
		t.Fatal(err)
	}
	if err := a.initializeOwnership(); err != nil {
		t.Fatal("migration not idempotent:", err)
	}
	if err := a.persistRecords(nil, []aclRecord{{username: "unknown-identity", action: "connect", allowed: false}}); err != nil {
		t.Fatal(err)
	}
	if err := a.initializeOwnership(); err != nil {
		t.Fatal(err)
	}
	var unknownScope string
	if err := ctx.AppDB().QueryRow(`SELECT scope_id FROM mqtt_acl_log WHERE username='unknown-identity'`).Scan(&unknownScope); err != nil || unknownScope != "" {
		t.Fatal("restart assigned unknown authentication attempts to the legacy owner")
	}
	u, err := getUser(ctx.AppDB(), "a", "legacy")
	if err != nil || u == nil || u.Enabled || len(u.AllowPublishTopics) != 1 || u.AllowPublishTopics[0] != scopeTopicRoot("a")+"#" {
		t.Fatalf("legacy migration: %#v %v", u, err)
	}
	retained, err := getRetainedMessage(ctx.AppDB(), "a", scopeTopicRoot("a")+"device/state")
	if err != nil || retained.Payload != "old" {
		t.Fatalf("retained migration: %#v %v", retained, err)
	}
	subs, err := listBusSubscriptions(ctx.AppDB(), "a")
	if err != nil || len(subs) != 1 || subs[0].TopicPattern != scopeTopicRoot("a")+"device/#" {
		t.Fatalf("subscription migration: %#v %v", subs, err)
	}
}

// Wire tests use actual MQTT packets and sockets so hooks are tested in the
// broker's real ordering, including authentication before client takeover.
func startOwnershipBroker(t *testing.T) (*App, *sdk.AppCtx, *testkit.EmitRecorder) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	recorder := testkit.NewEmitRecorder()
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithEmitter(recorder), testkit.WithConfig(map[string]string{"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"}))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	var users int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM mqtt_users`).Scan(&users); err != nil || users != 0 {
		t.Fatal("mount seeded an unsolicited bootstrap credential")
	}
	run, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- a.broker.Serve(run) }()
	go func() { done <- a.runPersistence(run) }()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("broker worker did not stop")
			}
		}
	})
	return a, ctx, recorder
}

func wireWrite(t *testing.T, conn net.Conn, pk packets.Packet) {
	t.Helper()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	pk.Mods.AllowResponseInfo = true // encode response-topic/correlation properties on client publishes.
	var buf bytes.Buffer
	var err error
	switch pk.FixedHeader.Type {
	case packets.Connect:
		err = pk.ConnectEncode(&buf)
	case packets.Subscribe:
		err = pk.SubscribeEncode(&buf)
	case packets.Publish:
		err = pk.PublishEncode(&buf)
	case packets.Puback:
		err = pk.PubackEncode(&buf)
	case packets.Pubrec:
		err = pk.PubrecEncode(&buf)
	case packets.Pubrel:
		err = pk.PubrelEncode(&buf)
	case packets.Pubcomp:
		err = pk.PubcompEncode(&buf)
	case packets.Unsubscribe:
		err = pk.UnsubscribeEncode(&buf)
	case packets.Disconnect:
		err = pk.DisconnectEncode(&buf)
	case packets.Pingreq:
		err = pk.PingreqEncode(&buf)
	default:
		t.Fatalf("unsupported test packet %d", pk.FixedHeader.Type)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func wireRead(conn net.Conn) (byte, []byte, error) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil {
		return 0, nil, err
	}
	size, multiplier := 0, 1
	for range 4 {
		var encoded [1]byte
		if _, err := io.ReadFull(conn, encoded[:]); err != nil {
			return 0, nil, err
		}
		size += int(encoded[0]&127) * multiplier
		if encoded[0]&128 == 0 {
			body := make([]byte, size)
			_, err := io.ReadFull(conn, body)
			return first[0] >> 4, body, err
		}
		multiplier *= 128
	}
	return 0, nil, fmt.Errorf("invalid packet length")
}

func wireConnect(t *testing.T, a *App, u *MQTTUser, id string, protocol byte, will string) (net.Conn, byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(a.broker.Port())), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	pk := packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Connect}, ProtocolVersion: protocol,
		Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: id, UsernameFlag: true, PasswordFlag: true, Username: []byte(u.Username), Password: []byte("test-password"), Keepalive: 60, Clean: false}}
	if will != "" {
		pk.Connect.WillFlag = true
		pk.Connect.WillTopic = will
		pk.Connect.WillPayload = []byte("will")
	}
	wireWrite(t, conn, pk)
	typ, body, err := wireRead(conn)
	if err != nil || typ != packets.Connack || len(body) < 2 {
		t.Fatalf("CONNACK type=%d body=%x err=%v", typ, body, err)
	}
	return conn, body[1]
}

func TestWireScopeIsolationAndTakeover(t *testing.T) {
	for _, protocol := range []byte{4, 5} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			a, ctx, recorder := startOwnershipBroker(t)
			ra, rb := scopeTopicRoot("a"), scopeTopicRoot("b")
			alice := scopedUser(t, a, "a", "sensor", []string{ra + "#"}, []string{ra + "#"})
			bob := scopedUser(t, a, "b", "sensor", []string{rb + "#"}, []string{rb + "#"})
			first, code := wireConnect(t, a, alice, "owned-client", protocol, "")
			if code != 0 {
				t.Fatalf("owner connect rejected: %x", code)
			}
			_, code = wireConnect(t, a, bob, "owned-client", protocol, "")
			if code == 0 {
				t.Fatal("foreign identity took over active client")
			}
			wireWrite(t, first, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Pingreq}, ProtocolVersion: protocol})
			if typ, _, err := wireRead(first); err != nil || typ != packets.Pingresp {
				t.Fatalf("owner was disconnected: %d %v", typ, err)
			}
			_, code = wireConnect(t, a, alice, "bad-will", protocol, rb+"state")
			if code == 0 {
				t.Fatal("cross-scope will accepted on wire")
			}
			receiver, code := wireConnect(t, a, bob, "receiver", protocol, "")
			if code != 0 {
				t.Fatal("receiver connect rejected")
			}
			wireWrite(t, receiver, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, ProtocolVersion: protocol, PacketID: 1,
				Filters: packets.Subscriptions{{Filter: ra + "#"}, {Filter: "#"}, {Filter: rb + "#"}}})
			typ, body, err := wireRead(receiver)
			if err != nil || typ != packets.Suback {
				t.Fatalf("SUBACK: %d %x %v", typ, body, err)
			}
			offset := 2
			if protocol == 5 {
				offset = 3
			}
			if len(body) < offset+3 || body[offset] < 128 || body[offset+1] < 128 || body[offset+2] >= 128 {
				t.Fatalf("cross-scope subscription accepted: %x", body)
			}
			wireWrite(t, first, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1, Retain: true}, ProtocolVersion: protocol, PacketID: 2, TopicName: ra + "state", Payload: []byte("private-a")})
			if typ, _, err := wireRead(first); err != nil || typ != packets.Puback {
				t.Fatalf("publish: %d %v", typ, err)
			}
			// A B-scope event bridge cannot observe A, even with same suffix.
			if _, err := a.toolSubscribe(ctx.WithProject("b"), map[string]any{"topic_pattern": rb + "#", "bus_topic": "filtered"}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) && len(recorder.EventsByTopic("mqtt.message")) == 0 {
				time.Sleep(time.Millisecond)
			}
			events := recorder.EventsByTopic("mqtt.message")
			if len(events) != 1 || events[0].ProjectID != "a" || len(recorder.EventsByTopic("mqtt.filtered")) != 0 {
				t.Fatalf("wrong event ownership: %#v", events)
			}
			// PING fences all prior writes; no A publish may precede PINGRESP.
			wireWrite(t, receiver, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Pingreq}, ProtocolVersion: protocol})
			if typ, _, err := wireRead(receiver); err != nil || typ != packets.Pingresp {
				t.Fatalf("foreign traffic delivered: %d %v", typ, err)
			}
			// ACL revocation closes a live owner and purges its dormant state.
			if _, err := a.toolUsersSetEnabled(ctx.WithProject("a"), map[string]any{"username": alice.Username, "enabled": false}); err != nil {
				t.Fatal(err)
			}
			if typ, _, err := wireRead(first); err == nil && typ != packets.Disconnect {
				t.Fatalf("revoked connection remained usable: %d", typ)
			}
		})
	}
}

func TestWireAliasesResolveBeforeAuthorizationAndDoNotCrossScopes(t *testing.T) {
	a, _, recorder := startOwnershipBroker(t)
	root := scopeTopicRoot("a")
	u := scopedUser(t, a, "a", "alias-user", []string{root + "#"}, []string{})
	conn, code := wireConnect(t, a, u, "aliases", 5, "")
	if code != 0 {
		t.Fatal("alias client failed authentication")
	}
	for i, topic := range []string{root + "state", ""} {
		wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1, Retain: true}, ProtocolVersion: 5, PacketID: uint16(i + 1), TopicName: topic,
			Payload: []byte(fmt.Sprint(i)), Properties: packets.Properties{TopicAlias: 1, TopicAliasFlag: true}})
		typ, body, err := wireRead(conn)
		if err != nil || typ != packets.Puback || (len(body) > 2 && body[2] >= 128) {
			t.Fatalf("aliased publish rejected: %d %x %v", typ, body, err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(recorder.EventsByTopic("mqtt.message")) < 2 {
		time.Sleep(time.Millisecond)
	}
	events := recorder.EventsByTopic("mqtt.message")
	if len(events) != 2 {
		t.Fatalf("alias-only publish lost: %#v", events)
	}
	for _, event := range events {
		if data := event.Data.(map[string]any); event.ProjectID != "a" || data["topic"] != root+"state" {
			t.Fatalf("wrong alias ownership: %#v", event)
		}
	}
	stored, err := (&retainedHook{app: a}).StoredRetainedMessages()
	if err != nil || len(stored) != 1 || stored[0].TopicName != root+"state" || stored[0].Properties.TopicAlias != 0 {
		t.Fatalf("connection alias leaked to storage: %#v %v", stored, err)
	}
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, ProtocolVersion: 5, PacketID: 3, TopicName: scopeTopicRoot("b") + "state", Payload: []byte("attack"), Properties: packets.Properties{TopicAlias: 1, TopicAliasFlag: true}})
	if typ, _, err := wireRead(conn); err == nil && typ != packets.Disconnect {
		t.Fatalf("foreign alias reassignment accepted: %d", typ)
	}
	responseConn, code := wireConnect(t, a, u, "response-topics", 5, "")
	if code != 0 {
		t.Fatal("response client failed authentication")
	}
	wireWrite(t, responseConn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, ProtocolVersion: 5, PacketID: 1, TopicName: root + "state", Payload: []byte("attack"), Properties: packets.Properties{ResponseTopic: scopeTopicRoot("b") + "reply"}})
	if typ, _, err := wireRead(responseConn); err == nil && typ != packets.Disconnect {
		t.Fatalf("foreign response topic accepted: %d", typ)
	}
	if events := recorder.EventsByTopic("mqtt.message"); len(events) != 2 {
		t.Fatalf("rejected packet reached event bus: %#v", events)
	}
}
