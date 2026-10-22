package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
)

type turnTestPlatform struct {
	answerPlatform
	calls    atomic.Int64
	input    map[string]any
	result   rtcTURNCredentials
	response func(int64) rtcTURNCredentials
	failure  bool
	wait     bool
}

func (p *turnTestPlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, errors.New("not used")
}
func (p *turnTestPlatform) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, errors.New("not used")
}
func (p *turnTestPlatform) CallAppResultContext(ctx context.Context, app, tool string, args map[string]any, out any) error {
	sequence := p.calls.Add(1)
	if app != "turn" || tool != "turn_credentials" {
		return errors.New("wrong boundary")
	}
	p.input = args
	if p.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if p.failure {
		return errors.New("credential-bearing provider URL must not leak")
	}
	result := p.result
	if p.response != nil {
		result = p.response(sequence)
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}
func turnTestCredentials() rtcTURNCredentials {
	return rtcTURNCredentials{ICEServers: []webrtc.ICEServer{{URLs: []string{"turn:relay.example.test:3478?transport=udp", "turns:relay.example.test:5349?transport=tcp", "turns:relay.example.test:8443?transport=tcp"}, Username: "temporary-user", Credential: "temporary-password"}}, ExpiresAt: time.Now().Add(time.Hour).Unix(), TTLSeconds: 3600}
}
func turnTestFixture(t *testing.T) (*App, *sdk.AppCtx, *turnTestPlatform) {
	t.Helper()
	p := &turnTestPlatform{answerPlatform: answerPlatform{bindings: map[string]any{"turn": int64(90)}}, result: turnTestCredentials()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p), tk.WithConfig(map[string]string{"softphone_webrtc_enabled": "true"}))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	return &App{installID: 42}, ctx, p
}
func saveTURNTestPolicy(t *testing.T, a *App, ctx *sdk.AppCtx, identity *phoneIdentity, p phoneTURNPolicy) {
	t.Helper()
	args := map[string]any{"policy": p}
	if identity != nil {
		args["identity"] = identity
	}
	if _, err := a.turnPolicies(ctx, args, nil); err != nil {
		t.Fatal(err)
	}
}
func TestTURNDefaultsOptionalAndPoliciesAreIsolated(t *testing.T) {
	a, ctx, platform := turnTestFixture(t)
	phoneTestPolicy(t, a)
	row := insertSoftphoneCall(t, a, "in-progress")
	config := softphoneRTCConfig{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}}
	if err := a.configureManagedTURN(&config, &row, row.PeerToken, phoneIdentity{}, false, nil); err != nil {
		t.Fatal(err)
	}
	if config.TURN != nil || platform.calls.Load() != 0 || config.ClientICEServers[0].URLs[0] != "stun:stun.example.test:3478" {
		t.Fatal("default contacted TURN or changed existing ICE")
	}
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	base := phoneTURNPolicy{Source: "app", AllowUserOverrides: true}
	saveTURNTestPolicy(t, a, ctx, nil, base)
	saveTURNTestPolicy(t, a, ctx, &alice, phoneTURNPolicy{Source: "app", Transport: "tls", TLSPort: 8443, ICEPolicy: "relay"})
	p, err := a.resolvedTURNPolicy("project-a", alice, nil)
	if err != nil || p.TLSPort != 8443 || p.ICEPolicy != "relay" {
		t.Fatal("adviser override missing", p, err)
	}
	p, err = a.resolvedTURNPolicy("project-a", bob, nil)
	if err != nil || p.Transport != "all" || p.ICEPolicy != "all" {
		t.Fatal("policy crossed advisers", p, err)
	}
	collision := alice
	collision.IssuerInstallID = "another-install"
	p, _ = a.resolvedTURNPolicy("project-a", collision, nil)
	if p.ICEPolicy != "all" {
		t.Fatal("bare subject ID collision")
	}
	p, _ = a.resolvedTURNPolicy("project-b", alice, nil)
	if p.Source != "existing" {
		t.Fatal("policy crossed project")
	}
	p, err = a.resolvedTURNPolicy("project-a", alice, url.Values{"turn_transport": {"udp"}, "ice_policy": {"all"}})
	if err != nil || p.Transport != "udp" || p.TLSPort != 0 || p.ICEPolicy != "all" {
		t.Fatal("permitted session override failed", err)
	}
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app"})
	if _, err = a.resolvedTURNPolicy("project-a", alice, url.Values{"ice_policy": {"all"}}); err == nil {
		t.Fatal("session bypassed project restriction")
	}
	if _, err = a.turnPolicies(ctx, map[string]any{"inherit": true}, &phonePrincipal{Identity: alice}); err == nil {
		t.Fatal("user bypassed edit restriction")
	}
	if _, err = a.turnPolicies(ctx, map[string]any{"identity": bob}, &phonePrincipal{Identity: alice}); err == nil {
		t.Fatal("user selected another identity")
	}
}
func TestTURNManagedAuthorizationContextAndNoDoubleRelay(t *testing.T) {
	a, ctx, p := turnTestFixture(t)
	row := insertSoftphoneCall(t, a, "in-progress")
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app", Transport: "tls", TLSPort: 8443, ICEPolicy: "relay"})
	c := softphoneRTCConfig{ICEServers: []webrtc.ICEServer{{URLs: []string{"turn:old.example:3478", "stun:stun.example:3478"}, Username: "old", Credential: "old"}}}
	if err := a.configureManagedTURN(&c, &row, row.PeerToken, phoneIdentity{}, false, url.Values{"project_id": {"attacker"}}); err != nil {
		t.Fatal(err)
	}
	result, err := c.TURN.issue(t.Context())
	if err != nil || len(result.ICEServers) != 1 {
		t.Fatal(err)
	}
	if hasTURN(c.ICEServers) || len(c.ICEServers) != 1 {
		t.Fatal("server acquired a managed/double relay")
	}
	if p.input["_project_id"] != "project-a" || p.input["project_id"] != nil || p.input["transport"] != "tls" || p.input["tls_port"] != 8443 || p.input["session_id"] == "" {
		t.Fatal("wrong authenticated TURN request", p.input)
	}
	before := p.calls.Load()
	if err = a.db().updateStatus(row.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = c.TURN.issue(t.Context()); err == nil || p.calls.Load() != before {
		t.Fatal("ended call obtained new credentials")
	}
}
func TestTURNUserHTTPAuthorizationAndRequiredRelay(t *testing.T) {
	a, ctx, _ := turnTestFixture(t)
	phoneTestPolicy(t, a)
	alice := phoneTestIdentity("alice")
	bob := phoneTestIdentity("bob")
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app", AllowUserOverrides: true})
	w := phoneTestRequest(a, &alice, "POST", "/softphone/turn-policy", map[string]any{"policy": phoneTURNPolicy{Source: "app", Transport: "tls", TLSPort: 5349, ICEPolicy: "relay"}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = phoneTestRequest(a, &alice, "POST", "/softphone/turn-policy", map[string]any{"identity": bob, "inherit": true})
	if w.Code == 200 {
		t.Fatal("forged identity accepted")
	}
	w = phoneTestRequest(a, &alice, "POST", "/numbers/turn-policy", map[string]any{"policy": phoneTURNPolicy{AllowUserOverrides: true}})
	if w.Code != 403 {
		t.Fatal("application user changed project defaults", w.Code)
	}
	row := insertSoftphoneCall(t, a, "in-progress")
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app", ICEPolicy: "relay"})
	server := softphoneTestServer(t, a)
	defer server.Close()
	response, err := http.Get(server.URL + "/softphone/media/" + row.ID + "/" + row.PeerToken)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 426 {
		t.Fatal("required relay downgraded to PCM", response.StatusCode)
	}
}
func TestTURNMalformedCredentialsAndCancellation(t *testing.T) {
	for _, mode := range []string{"expiry", "empty", "url", "credential", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			a, ctx, p := turnTestFixture(t)
			row := insertSoftphoneCall(t, a, "in-progress")
			saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app"})
			c := softphoneRTCConfig{}
			if err := a.configureManagedTURN(&c, &row, row.PeerToken, phoneIdentity{}, false, nil); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expiry":
				p.result.ExpiresAt = time.Now().Unix()
			case "empty":
				p.result.ICEServers = nil
			case "url":
				p.result.ICEServers[0].URLs = []string{"https://user:secret@example"}
			case "credential":
				p.result.ICEServers[0].Credential = nil
			case "failure":
				p.failure = true
			case "canceled":
				p.wait = true
			}
			request, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if _, err := c.TURN.issue(request); err == nil || strings.Contains(err.Error(), "provider URL") {
				t.Fatal("malformed or sensitive result accepted", err)
			}
		})
	}
}
func TestTURNPolicyValidationAndUnboundOptionalIntegration(t *testing.T) {
	a, ctx, p := turnTestFixture(t)
	for _, policy := range []phoneTURNPolicy{{Source: "other"}, {Transport: "other"}, {ICEPolicy: "other"}, {TTLSeconds: 59}, {TTLSeconds: 86401}, {Transport: "udp", TLSPort: 8443}, {Transport: "tls", TLSPort: 65536}} {
		if _, err := a.turnPolicies(ctx, map[string]any{"policy": policy}, nil); err == nil {
			t.Fatal("invalid policy accepted", policy)
		}
	}
	p.bindings = map[string]any{}
	unbound := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-b"), tk.WithPlatform(p))
	if _, err := a.turnPolicies(unbound, map[string]any{"policy": phoneTURNPolicy{Source: "app"}}, nil); err == nil {
		t.Fatal("unbound managed TURN enabled")
	}
}
func TestTURNRelayRequiredFailureAndCredentialRedaction(t *testing.T) {
	for _, policy := range []string{"all", "relay"} {
		t.Run(policy, func(t *testing.T) {
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			writer := newWebSocketWriterPump(local, ws.StateServerSide)
			defer writer.Stop()
			done := make(chan struct{})
			defer close(done)
			config := softphoneRTCConfig{ICEPolicy: policy, TURN: &rtcTURNProvider{fetch: func(context.Context) (rtcTURNCredentials, error) {
				return rtcTURNCredentials{}, errors.New("temporary-password credential-bearing URL")
			}}}
			received := make(chan []byte, 1)
			go func() { data, _, _ := wsutil.ReadServerData(remote); received <- data }()
			session, err := newRTCICESession(t.Context(), config, writer, done)
			if policy == "relay" {
				if err == nil {
					t.Fatal("required TURN silently fell back")
				}
				data := <-received
				if strings.Contains(string(data), "temporary-password") || !strings.Contains(string(data), "turn_relay_unavailable") {
					t.Fatal("unsafe failure frame")
				}
			}
			if policy == "all" {
				if err != nil {
					t.Fatal("optional outage broke direct setup", err)
				}
				if strings.Contains(string(session.initial(false)), "temporary-password") || hasTURN(session.credentials.ICEServers) {
					t.Fatal("optional outage leaked credentials")
				}
			}
		})
	}
}
func TestTURNRefreshSchedulingIsBounded(t *testing.T) {
	now := time.Now()
	for _, ttl := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour} {
		expiry := now.Add(ttl).Unix()
		remaining := time.Unix(expiry, 0).Sub(now)
		delay := rtcTURNRenewAfter(expiry, now)
		if delay <= 0 || delay >= remaining || remaining-delay > 6*time.Minute {
			t.Fatal("unsafe refresh schedule", ttl, delay)
		}
	}
}

func TestTURNCredentialConcurrencyBoundAndCanceledWaiters(t *testing.T) {
	var active, peak, calls atomic.Int64
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	provider := &rtcTURNProvider{fetch: func(ctx context.Context) (rtcTURNCredentials, error) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
			return turnTestCredentials(), nil
		case <-ctx.Done():
			return rtcTURNCredentials{}, ctx.Err()
		}
	}}
	results := make(chan error, 4)
	for range 4 {
		go func() { _, e := provider.issue(t.Context()); results <- e }()
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("issuance didn't start")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := provider.issue(canceled); e == nil {
		t.Fatal("canceled issuance admitted")
	}
	waiting, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stop()
	if _, e := provider.issue(waiting); e == nil {
		t.Fatal("saturated request ignored deadline")
	}
	if calls.Load() != 4 || peak.Load() != 4 {
		t.Fatal("concurrency limit exceeded", calls.Load(), peak.Load())
	}
	close(release)
	for range 4 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	if _, e := provider.issue(t.Context()); e != nil {
		t.Fatal("slot leaked", e)
	}
}

func TestTURNRequestedTransportFiltering(t *testing.T) {
	for _, mode := range []string{"udp", "tcp", "tls"} {
		t.Run(mode, func(t *testing.T) {
			credentials := turnTestCredentials()
			provider := &rtcTURNProvider{policy: phoneTURNPolicy{Transport: mode, TLSPort: map[string]int{"tls": 8443}[mode]}, fetch: func(context.Context) (rtcTURNCredentials, error) { return credentials, nil }}
			got, e := provider.issue(t.Context())
			if mode == "tcp" {
				if e == nil {
					t.Fatal("missing TCP transport silently downgraded")
				}
				return
			}
			if e != nil || len(got.ICEServers) != 1 || len(got.ICEServers[0].URLs) != 1 {
				t.Fatal("transport filter", got, e)
			}
			if mode == "tls" && got.ICEServers[0].URLs[0] != "turns:relay.example.test:8443?transport=tcp" {
				t.Fatal("TLS port filter")
			}
		})
	}
}

func TestTURNRefreshWorkerRenewsAndStopsWithoutAudioWork(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	writer := newWebSocketWriterPump(local, ws.StateServerSide)
	defer writer.Stop()
	done := make(chan struct{})
	var calls atomic.Int64
	provider := &rtcTURNProvider{fetch: func(context.Context) (rtcTURNCredentials, error) { calls.Add(1); return turnTestCredentials(), nil }}
	session, e := newRTCICESession(t.Context(), softphoneRTCConfig{TURN: provider}, writer, done)
	if e != nil {
		t.Fatal(e)
	}
	stopped := make(chan struct{})
	go func() { session.runAfter(writer, time.Millisecond); close(stopped) }()
	remote.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, _, e := wsutil.ReadServerData(remote)
	if e != nil {
		t.Fatal(e)
	}
	var value struct {
		Type      string `json:"type"`
		Revision  uint64 `json:"revision"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Type != "webrtc.ice_config" || value.Revision != 2 || value.ExpiresAt <= time.Now().Unix() || calls.Load() != 2 {
		t.Fatal("renewal failed", string(raw), calls.Load())
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("refresh worker leaked")
	}
}

func TestTURNOptionalManifestContract(t *testing.T) {
	raw, e := os.ReadFile("apteva.yaml")
	if e != nil {
		t.Fatal(e)
	}
	disk, e := sdk.ParseManifest(raw)
	if e != nil {
		t.Fatal(e)
	}
	embedded := (&App{}).Manifest()
	if !reflect.DeepEqual(disk.Requires, embedded.Requires) {
		t.Fatal("embedded TURN requirements drift")
	}
	for _, manifest := range []*sdk.Manifest{disk, &embedded} {
		appFound, bindingFound, permissionFound := false, false, false
		for _, dep := range manifest.Requires.Apps {
			if dep.Name == "turn" {
				appFound = dep.Optional && dep.Version == ">=0.3.0"
			}
		}
		for _, dep := range manifest.Requires.Integrations {
			if dep.Role == "turn" {
				bindingFound = dep.Kind == "app" && !dep.Required && reflect.DeepEqual(dep.CompatibleAppNames, []string{"turn"})
			}
		}
		for _, permission := range manifest.Requires.Permissions {
			if permission == "platform.apps.call" {
				permissionFound = true
			}
		}
		if !appFound || !bindingFound || !permissionFound {
			t.Fatal("missing optional TURN contract")
		}
	}
}

func TestTURNListenerUsesSupervisorPolicyRatherThanCallOwner(t *testing.T) {
	app, ctx, platform := turnTestFixture(t)
	policy := phoneTestPolicy(t, app)
	policy.Users[3].Listen = true
	rec := phoneTestRequest(app, nil, "PUT", "/access/policy", policy)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	row := phoneTestCall(t, app, "turn-listened", "in-progress")
	if e := app.db().updateMediaStatus(row.ID, "connected", "", 0, ""); e != nil {
		t.Fatal(e)
	}
	row.MediaStatus = "connected"
	alice, boss := phoneTestIdentity("alice"), phoneTestIdentity("boss")
	owner, e := app.phonePrincipal(row.ProjectID, alice)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.setPhoneOwner(&row, owner, "sales"); e != nil {
		t.Fatal(e)
	}
	tap := app.listeners.openBridge(row.ID)
	defer app.listeners.closeBridge(row.ID, tap)
	saveTURNTestPolicy(t, app, ctx, &alice, phoneTURNPolicy{Source: "app", Transport: "udp"})
	saveTURNTestPolicy(t, app, ctx, &boss, phoneTURNPolicy{Source: "app", Transport: "tls", TLSPort: 8443, ICEPolicy: "relay"})
	session := listenerIssue(t, app, row.ID, &boss)
	server := listenerServer(t, app)
	socket := dialWS(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42")+"?transport=webrtc")
	defer socket.Close()
	socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, _, e := wsutil.ReadServerData(socket)
	if e != nil {
		t.Fatal(e)
	}
	var config struct {
		Type    string             `json:"type"`
		Policy  string             `json:"ice_transport_policy"`
		Servers []webrtc.ICEServer `json:"ice_servers"`
	}
	if json.Unmarshal(data, &config) != nil || config.Type != "webrtc.config" || config.Policy != "relay" || len(config.Servers) != 1 || config.Servers[0].URLs[0] != "turns:relay.example.test:8443?transport=tcp" {
		t.Fatal("supervisor policy not used")
	}
	if platform.input["transport"] != "tls" {
		t.Fatal("call owner's policy leaked to supervisor")
	}
}

func TestTURNTLSDefaultPortAndDTLSAreNotConfused(t *testing.T) {
	servers := []webrtc.ICEServer{{URLs: []string{"turns:relay.example?transport=tcp", "turns:relay.example:5349?transport=udp"}}}
	got := selectTURNTransport(servers, "tls", 5349)
	if len(got) != 1 || len(got[0].URLs) != 1 || got[0].URLs[0] != "turns:relay.example?transport=tcp" {
		t.Fatal("TLS default port or DTLS filtering failed", got)
	}
}

func TestTURNStaticTransportFilterDoesNotRestoreExcludedEndpoints(t *testing.T) {
	app, ctx, _ := turnTestFixture(t)
	row := insertSoftphoneCall(t, app, "in-progress")
	saveTURNTestPolicy(t, app, ctx, nil, phoneTURNPolicy{Source: "existing", Transport: "tcp"})
	config := softphoneRTCConfig{ICEServers: []webrtc.ICEServer{{URLs: []string{"turn:relay.example:3478?transport=udp"}, Username: "u", Credential: "p"}}}
	if e := app.configureManagedTURN(&config, &row, row.PeerToken, phoneIdentity{}, false, nil); e != nil {
		t.Fatal(e)
	}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	writer := newWebSocketWriterPump(local, ws.StateServerSide)
	defer writer.Stop()
	done := make(chan struct{})
	defer close(done)
	session, e := newRTCICESession(t.Context(), config, writer, done)
	if e != nil {
		t.Fatal(e)
	}
	if len(session.credentials.ICEServers) != 0 || strings.Contains(string(session.initial(false)), "relay.example") {
		t.Fatal("excluded UDP endpoint silently restored")
	}
}
