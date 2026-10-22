package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func validConfig() sdk.Config {
	return sdk.Config{"public_ip": "8.8.8.8", "public_host": "turn.example.com", "shared_secret": strings.Repeat("test-secret-", 4)}
}
func testApp(t *testing.T) *App {
	t.Helper()
	cfg, err := parseConfig(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{}
	if err = a.initialize(cfg, "project-a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.OnUnmount(nil) })
	return a
}

func TestManifestDeploymentContract(t *testing.T) {
	m := (&App{}).Manifest()
	if err := sdk.ValidateManifest(&m); err != nil {
		t.Fatal(err)
	}
	if m.Runtime.Source.Entry != "mcp/turn" || m.Runtime.ActivationStrategy != "exclusive" || m.Runtime.Package.CGO {
		t.Fatal("nonportable runtime declaration")
	}
	caps := m.Runtime.Package.AppCapabilities
	if !caps.PassiveStart || !caps.Drain || caps.ReplicaSafe || caps.Routing != "stateless" {
		t.Fatal("incorrect deployment capabilities")
	}
	if len(m.Runtime.Ports) != 3+relayPortMax-relayPortMin+1 {
		t.Fatal("relay port reservation is incomplete")
	}
	ports := map[string]bool{}
	for _, p := range m.Runtime.Ports {
		if p.ContainerPort != p.HostPort {
			t.Fatal("TURN requires port-preserving publication")
		}
		ports[fmt.Sprintf("%s/%d", p.Protocol, p.HostPort)] = true
	}
	if !ports["tcp/3478"] || !ports["udp/3478"] || !ports["tcp/5349"] {
		t.Fatal("TURN listener missing")
	}
	for p := relayPortMin; p <= relayPortMax; p++ {
		if !ports[fmt.Sprintf("udp/%d", p)] {
			t.Fatalf("unreserved relay port %d", p)
		}
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "turn_credentials" && tool.Exposure != sdk.ToolExposureAppOnly {
			t.Fatal("credential tool exposed to agents")
		}
	}
}

func TestConfigAndPeerPolicy(t *testing.T) {
	for _, test := range []struct{ key, value string }{{"public_ip", "127.0.0.1"}, {"public_ip", "::ffff:8.8.8.8"}, {"public_ip", "169.254.169.254"}, {"public_host", "https://turn.example.com"}, {"public_host", "host/path"}, {"shared_secret", "short"}, {"max_allocations", "65"}, {"credential_ttl_seconds", "0"}, {"allocations_per_credential", "17"}, {"bytes_per_second", "-1"}, {"allowed_peer_cidrs", "oops"}} {
		values := validConfig()
		values[test.key] = test.value
		if _, err := parseConfig(values); err == nil {
			t.Errorf("accepted %s=%s", test.key, test.value)
		}
	}
	cfg, err := parseConfig(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"0.0.0.0", "10.0.0.1", "100.100.100.100", "127.0.0.1", "169.254.169.254", "168.63.129.16", "172.16.0.1", "192.168.1.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "255.255.255.255", "::1", "::ffff:127.0.0.1"} {
		if cfg.peerAllowed(netip.MustParseAddr(ip)) {
			t.Errorf("unsafe peer accepted: %s", ip)
		}
	}
	if !cfg.peerAllowed(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public peer rejected")
	}
	cfg.allowedPeers = []netip.Prefix{netip.MustParsePrefix("1.1.1.0/24"), netip.MustParsePrefix("10.0.0.0/8")}
	if !cfg.peerAllowed(netip.MustParseAddr("1.1.1.1")) || cfg.peerAllowed(netip.MustParseAddr("8.8.4.4")) || cfg.peerAllowed(netip.MustParseAddr("10.0.0.1")) {
		t.Fatal("allowlist overrides hard deny or does not restrict public peers")
	}
}

func TestTemporaryCredentialsAndProjectIsolation(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	creds, err := a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "call-123", TTLSeconds: 60}, now)
	if err != nil {
		t.Fatal(err)
	}
	server := creds.ICEServers[0]
	if creds.ExpiresAt != now.Unix()+60 || len(server.URLs) != 2 || server.Credential != restPassword(a.cfg.secret, server.Username) {
		t.Fatal("incorrect TURN REST credentials")
	}
	if strings.Contains(server.Username, "project-a") || strings.Contains(server.Username, "call-123") {
		t.Fatal("raw project or session leaked into public username")
	}
	_, key, ok := a.authenticate(&turn.RequestAttributes{Username: server.Username, Realm: a.cfg.realm})
	if !ok || !bytes.Equal(key, turn.GenerateAuthKey(server.Username, a.cfg.realm, server.Credential)) {
		t.Fatal("generated credentials cannot authenticate")
	}
	for _, req := range []credentialRequest{{ProjectID: "foreign", SessionID: "x"}, {ProjectID: "project-a", SessionID: ""}, {ProjectID: "project-a", SessionID: "x", TTLSeconds: 1}, {ProjectID: "project-a", SessionID: "x", TTLSeconds: 86401}} {
		if _, err := a.issueCredentials(req, now); err == nil {
			t.Fatal("invalid credential request accepted")
		}
	}
	for _, username := range []string{"bad", fmt.Sprintf("%d:a:b:c", now.Unix()-1), fmt.Sprintf("%d:%s:%s:%s", now.Unix()+100000, shortHash("project-a"), shortHash("session"), shortHash("nonce"))} {
		if _, _, ok := a.authenticate(&turn.RequestAttributes{Username: username, Realm: a.cfg.realm}); ok {
			t.Fatal("invalid username accepted")
		}
	}
	if _, _, ok := a.authenticate(&turn.RequestAttributes{Username: server.Username, Realm: "foreign"}); ok {
		t.Fatal("foreign realm accepted")
	}
	a.project = "foreign"
	if _, _, ok := a.authenticate(&turn.RequestAttributes{Username: server.Username, Realm: a.cfg.realm}); ok {
		t.Fatal("foreign project credential accepted")
	}
	a.project = "project-a"
	for i := 0; i < 20; i++ {
		_, _ = a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "burst"}, now)
	}
	if _, err := a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "burst"}, now); !errors.Is(err, errRateLimited) {
		t.Fatal("credential issuance unbounded")
	}
}

func TestProtectedHTTPAndCredentialScope(t *testing.T) {
	a := testApp(t)
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	var credentialsHandler http.HandlerFunc
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == "/credentials" {
			credentialsHandler = route.Handler
		}
	}
	request := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/credentials", strings.NewReader(body))
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		credentialsHandler(w, req)
		return w
	}
	if w := request("", `{"session_id":"x"}`); w.Code != 401 {
		t.Fatalf("unauthorized: %d", w.Code)
	}
	if w := request("Bearer test-app-token", `{"session_id":"x","project_id":"foreign"}`); w.Code != 400 {
		t.Fatal("project mismatch accepted")
	}
	if w := request("Bearer test-app-token", `{"session_id":"x"} {}`); w.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
	w := request("Bearer test-app-token", `{"session_id":"x"}`)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential request failed: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), a.cfg.secret) {
		t.Fatal("signing secret leaked")
	}
	a.project = ""
	if w := request("Bearer test-app-token", `{"session_id":"x"}`); w.Code != 400 {
		t.Fatal("global credential request accepted without a project")
	}
	for _, tool := range a.MCPTools() {
		if tool.Name == "turn_credentials" {
			result, err := tool.Handler((&sdk.AppCtx{}).WithProject("project-b"), map[string]any{"session_id": "call"})
			if err != nil || result == nil {
				t.Fatalf("scoped tool failed: %v", err)
			}
			if _, err = tool.Handler(&sdk.AppCtx{}, map[string]any{"session_id": "call"}); err == nil {
				t.Fatal("tool accepted missing project context")
			}
		}
	}
	t.Setenv("APTEVA_APP_TOKEN", "")
	if w := request("", `{"session_id":"x"}`); w.Code != 401 {
		t.Fatal("SDK development mode bypassed credential protection")
	}
}

func startTestRelay(t *testing.T) *App {
	t.Helper()
	a := testApp(t)
	// Local loopback is deliberately available only to in-process tests. No
	// install setting can enable this policy on the production relay.
	a.cfg.publicIP = netip.MustParseAddr("127.0.0.1")
	a.cfg.bindIP = "127.0.0.1"
	a.peerFilter = func(ip net.IP) bool { return ip.IsLoopback() }
	if err := a.listenNetwork("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return a
}

func newTURNClient(t *testing.T, a *App, transport string, credential iceServer) *turn.Client {
	t.Helper()
	var packet net.PacketConn
	var address string
	if transport == "tcp" {
		address = a.tcp.Addr().String()
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		packet = turn.NewSTUNConn(conn)
	} else {
		address = a.udp.LocalAddr().String()
		var err error
		packet, err = net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
	}
	factory := logging.NewDefaultLoggerFactory()
	factory.DefaultLogLevel = logging.LogLevelDisabled
	client, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: address, STUNServerAddr: address, Conn: packet, Username: credential.Username, Password: credential.Credential, Realm: a.cfg.realm, LoggerFactory: factory, RTO: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); _ = packet.Close() })
	return client
}

func issueTestCredential(t *testing.T, a *App) iceServer {
	t.Helper()
	creds, err := a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "call"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return creds.ICEServers[0]
}

func echoThroughRelay(t *testing.T, relay net.PacketConn) {
	t.Helper()
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	_ = relay.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = relay.WriteTo([]byte("outbound-audio"), peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, source, err := peer.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "outbound-audio" {
		t.Fatalf("relay send failed: %v", err)
	}
	if _, err = peer.WriteTo([]byte("inbound-audio"), source); err != nil {
		t.Fatal(err)
	}
	n, _, err = relay.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "inbound-audio" {
		t.Fatalf("relay receive failed: %v", err)
	}
}

func TestRealUDPAndTCPRelaying(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) {
			a := startTestRelay(t)
			client := newTURNClient(t, a, transport, issueTestCredential(t, a))
			relay, err := client.Allocate()
			if err != nil {
				t.Fatal(err)
			}
			defer relay.Close()
			port := relay.LocalAddr().(*net.UDPAddr).Port
			if port < relayPortMin || port > relayPortMax {
				t.Fatal("allocation escaped reserved pool")
			}
			echoThroughRelay(t, relay)
			if a.stats.sent.Load() == 0 || a.stats.received.Load() == 0 {
				t.Fatal("traffic counters missing")
			}
		})
	}
}

func TestWrongCredentialCannotAllocate(t *testing.T) {
	a := startTestRelay(t)
	credential := issueTestCredential(t, a)
	credential.Credential = "wrong-password"
	client := newTURNClient(t, a, "udp", credential)
	if relay, err := client.Allocate(); err == nil {
		relay.Close()
		t.Fatal("bad credential allocated a relay")
	}
	if a.status()["active_allocations"].(int) != 0 {
		t.Fatal("bad credential consumed capacity")
	}
}

func TestAtomicAllocationQuotasAndPoolBounds(t *testing.T) {
	a := testApp(t)
	a.cfg.bindIP = "127.0.0.1"
	a.cfg.perCredential = 2
	a.cfg.maxAllocations = 3
	if err := a.openPool(); err != nil {
		t.Fatal(err)
	}
	g := &relayGenerator{app: a}
	var wg sync.WaitGroup
	var lock sync.Mutex
	var conns []net.PacketConn
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "same-user"})
			if err == nil {
				lock.Lock()
				conns = append(conns, c)
				lock.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(conns) != 2 {
		t.Fatalf("per-credential quota raced: %d", len(conns))
	}
	other, _, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	conns = append(conns, other)
	if c, _, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "third"}); err == nil {
		c.Close()
		t.Fatal("global quota exceeded")
	}
	for _, c := range conns {
		_ = c.Close()
		_ = c.Close()
	}
	if len(a.relays) != 0 || len(a.users) != 0 {
		t.Fatal("closed allocations leaked capacity")
	}
	for _, port := range []int{1, relayPortMin - 1, relayPortMax + 1, 65535} {
		if c, _, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "x", RequestedPort: port}); err == nil {
			c.Close()
			t.Fatal("requested port escaped pool")
		}
	}
	if _, _, err := g.AllocateListener(turn.AllocateListenerConfig{}); err == nil {
		t.Fatal("unreserved TCP relay listener enabled")
	}
}

func TestBandwidthAndPrivatePeerEnforcement(t *testing.T) {
	a := testApp(t)
	a.cfg.bindIP = "127.0.0.1"
	if err := a.openPool(); err != nil {
		t.Fatal(err)
	}
	c, _, err := (&relayGenerator{app: a}).AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.WriteTo([]byte("blocked"), &net.UDPAddr{IP: net.IPv4(169, 254, 169, 254), Port: 80}); err == nil {
		t.Fatal("metadata peer accepted")
	}
	relay := c.(*relayConn)
	relay.limiter.SetLimit(0)
	relay.limiter.AllowN(time.Now(), 65536)
	if n, err := c.WriteTo([]byte("drop"), &net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 9999}); err != nil || n != 4 || a.stats.dropped.Load() != 4 {
		t.Fatal("bandwidth excess not dropped")
	}
}

func TestManagedStandbyDrainAndFencing(t *testing.T) {
	a := startTestRelay(t)
	a.managed = true
	a.deployment = "deployment-a"
	a.generation = 2
	a.state = "standby"
	if _, err := a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "x"}, time.Now()); !errors.Is(err, errNotAdmitting) {
		t.Fatal("standby issued credentials")
	}
	g := &relayGenerator{app: a}
	if c, _, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "x"}); err == nil {
		c.Close()
		t.Fatal("standby allocated")
	}
	now := time.Now()
	own := ownership{Active: true, DeploymentID: a.deployment, Generation: a.generation, ExpiresAt: now.Unix() + 15}
	if a.applyOwnership(own, 200, nil, now) || a.state != "standby" {
		t.Fatal("ownership alone activated without an activation request")
	}
	a.armed = true
	a.applyOwnership(own, 200, nil, now)
	if a.state != "active" {
		t.Fatal("armed owner did not activate")
	}
	client := newTURNClient(t, a, "tcp", issueTestCredential(t, a))
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	t.Setenv("APTEVA_APP_TOKEN", "runtime-token")
	call := func(action, id string, generation int64) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"deployment_id":%q,"generation":%d}`, id, generation)
		req := httptest.NewRequest("POST", runtimePrefix+action, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-token")
		w := httptest.NewRecorder()
		a.protected(a.handleLifecycle)(w, req)
		return w
	}
	if w := call("drain", a.deployment, 3); w.Code != 409 {
		t.Fatal("wrong generation accepted")
	}
	w := call("drain", a.deployment, 2)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"drained":false`) {
		t.Fatal("drain claimed completion with live allocations")
	}
	if _, err := a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "x"}, time.Now()); err == nil {
		t.Fatal("draining execution admitted credentials")
	}
	if c, _, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "new"}); err == nil {
		c.Close()
		t.Fatal("drain admitted a new allocation")
	}
	echoThroughRelay(t, relay)
	if !a.applyOwnership(ownership{}, 503, errors.New("gateway unavailable"), now.Add(20*time.Second)) {
		t.Fatal("expired ownership did not fence")
	}
	if a.trafficAllowed() {
		t.Fatal("fenced relay still forwards")
	}
	a.stopNetwork()
	if a.status()["active_allocations"].(int) != 0 {
		t.Fatal("fence retained relay sockets")
	}
	if w := call("activate", a.deployment, 2); w.Code != 409 {
		t.Fatal("fenced process reactivated")
	}
}

func TestDrainCompletesAndCanResume(t *testing.T) {
	a := testApp(t)
	a.managed = true
	a.deployment = "d"
	a.generation = 1
	a.leaseExpiry = time.Now().Add(time.Minute)
	t.Setenv("APTEVA_APP_TOKEN", "token")
	for _, action := range []string{"drain", "activate"} {
		req := httptest.NewRequest("POST", runtimePrefix+action, strings.NewReader(`{"deployment_id":"d","generation":1}`))
		req.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		a.protected(a.handleLifecycle)(w, req)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if action == "drain" && (!strings.Contains(w.Body.String(), `"drained":true`) || a.state != "drained") {
			t.Fatal("empty relay did not drain")
		}
	}
	if !a.trafficAllowed() {
		t.Fatal("drained execution could not resume")
	}
}

func TestOwnershipCallbackAndRuntimeEnvironment(t *testing.T) {
	a := testApp(t)
	for _, key := range []string{"APTEVA_RUNTIME_MODE", "APTEVA_DEPLOYMENT_ID", "APTEVA_DEPLOYMENT_GENERATION", "APTEVA_OUTBOUND_TOKEN"} {
		t.Setenv(key, "")
	}
	if err := a.prepareRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_RUNTIME_MODE", "standby")
	if err := a.prepareRuntime(); err == nil {
		t.Fatal("partial managed identity accepted")
	}
	t.Setenv("APTEVA_DEPLOYMENT_ID", "d")
	t.Setenv("APTEVA_DEPLOYMENT_GENERATION", "1")
	t.Setenv("APTEVA_APP_TOKEN", "incoming")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outgoing")
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/runtime/ownership" || r.Header.Get("Authorization") != "Bearer outgoing" {
			http.Error(w, "bad callback", 403)
			return
		}
		writeJSON(w, 200, ownership{Active: true, DeploymentID: "d", Generation: 1, ExpiresAt: time.Now().Unix() + 15})
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	if err := a.prepareRuntime(); err != nil {
		t.Fatal(err)
	}
	if a.state != "standby" {
		t.Fatal("managed startup not passive")
	}
	own, status, err := a.fetchOwnership(context.Background())
	if err != nil || status != 200 || own.DeploymentID != "d" {
		t.Fatalf("ownership callback failed: %v", err)
	}
	if secureGateway("http://remote.example.com") || secureGateway("https://user:pass@example.com") || !secureGateway("https://gateway.example.com") {
		t.Fatal("gateway transport validation failed")
	}
	// Deployment metadata and runtime status must not disclose configuration secrets.
	raw, _ := json.Marshal(a.status())
	if strings.Contains(string(raw), a.cfg.secret) || strings.Contains(string(raw), os.Getenv("APTEVA_APP_TOKEN")) {
		t.Fatal("status leaked secrets")
	}
}
