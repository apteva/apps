package main

import (
	"encoding/json"
	"errors"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestManagedRuntimeBrowserScopeAndHeaderSpoofing(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	a.installID = 42
	a.runtime = runtimeTestActive()
	a.runtime.endpoints["browser"] = "https://voice.test"
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_PUBLIC_URL", "https://management.test")
	var revoked atomic.Bool
	var unavailable atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path != "/api/apps/callback/runtime/browser-authorize" || r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Error("invalid callback boundary")
			w.WriteHeader(403)
			return
		}
		var request struct{ Token, Origin, Operation string }
		json.NewDecoder(r.Body).Decode(&request)
		if request.Token != "browser-grant" || request.Origin != "https://management.test" || revoked.Load() {
			w.WriteHeader(403)
			return
		}
		writeJSON(w, runtimeBrowserGrant{InstallID: 42, Project: "project-a", UserID: 9, Deployment: "deployment-a", Generation: 1, Origin: request.Origin, Expires: time.Now().Add(5 * time.Minute).Unix(), Principal: &sdk.TrustedPrincipal{Version: 1, UserID: 9, ProjectID: "project-a", ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}})
	}))
	defer gateway.Close()
	a.runtime.http = gateway.Client()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	row := testCall("browser-scope", "completed")
	row.ProjectID = "project-a"
	a.db().insertCall(row)
	request := func(path, origin, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", runtimeBrowserPrefix+path, nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Apteva-Project-ID", "foreign-project")
		r.Header.Set("X-Apteva-Issuer-App", "forged")
		w := httptest.NewRecorder()
		a.handleRuntimeBrowser(w, r)
		return w
	}
	if w := request("/calls", "https://management.test", "browser-grant"); w.Code != 200 || !strings.Contains(w.Body.String(), row.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/calls?project_id=foreign-project", "/mcp", "/_apteva/runtime/app/activate", "/peer/call/secret"} {
		if w := request(path, "https://management.test", "browser-grant"); w.Code < 400 {
			t.Fatal("unsafe direct request", path, w.Code)
		}
	}
	if w := request("/calls", "https://evil.test", "browser-grant"); w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if w := request("/calls", "https://management.test", "forged"); w.Code != 403 {
		t.Fatal("forged credential accepted")
	}
	unavailable.Store(true)
	if w := request("/calls", "https://management.test", "browser-grant"); w.Code != 503 {
		t.Fatal("temporary callback failure was classified as revocation", w.Code)
	}
	unavailable.Store(false)
	revoked.Store(true)
	if w := request("/calls", "https://management.test", "browser-grant"); w.Code != 403 {
		t.Fatal("revocation ignored")
	}
}
func TestManagedRuntimeBrowserGrantIsolationAndBounds(t *testing.T) {
	a := &App{runtime: runtimeTestActive()}
	grant := runtimeBrowserGrant{Token: "credential", UserID: 1, Origin: "https://management.test", Expires: time.Now().Add(time.Minute).Unix()}
	a.rememberRuntimeMedia([]byte(`{"call_id":"call-a","session_token":"media-a"}`), grant)
	_, got, ok := a.runtimeMediaGrant("/softphone/media/call-a/media-a")
	if !ok || got.Token != grant.Token {
		t.Fatal("session grant missing")
	}
	for _, path := range []string{"/softphone/media/call-b/media-a", "/softphone/media/call-a/media-b"} {
		if _, _, ok := a.runtimeMediaGrant(path); ok {
			t.Fatal("grant crossed sessions")
		}
	}
	if a.runtimeBrowserHandler("/mcp") != nil || a.runtimeBrowserHandler(runtimeAppPrefix+"activate") != nil {
		t.Fatal("direct ingress exposes administration")
	}
	for i := 0; i < 1100; i++ {
		a.rememberRuntimeGrant(strings.Repeat("x", i+1), grant)
	}
	if len(a.runtimeBrowsers.grants) > 1024 {
		t.Fatal("unbounded credentials")
	}
}
func TestManagedRuntimeLifecycleRoutePrivacy(t *testing.T) {
	a := &App{}
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	var protected, scoped bool
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == runtimeAppPrefix {
			protected = !route.NoAuth
		}
		if route.Pattern == runtimeBrowserPrefix+"/" {
			scoped = route.NoAuth
		}
	}
	if !protected || !scoped {
		t.Fatal("invalid runtime route authentication contract")
	}
	w := httptest.NewRecorder()
	a.handleRuntimeLifecycle(w, httptest.NewRequest("GET", runtimeAppPrefix+"describe", nil))
	if w.Code != 403 {
		t.Fatal("unauthenticated lifecycle exposed")
	}
}

func TestManagedRuntimeBrowserCapacityReservedBeforeSessionEffects(t *testing.T) {
	a := &App{runtime: runtimeTestActive()}
	grant := runtimeBrowserGrant{Token: "credential", UserID: 1, Expires: time.Now().Add(time.Minute).Unix()}
	for i := 0; i < 1023; i++ {
		a.rememberRuntimeGrant(strconv.Itoa(i), grant)
	}
	release, ok := a.reserveRuntimeBrowserSession("/softphone/place")
	if !ok {
		t.Fatal("last slot unavailable")
	}
	if _, ok = a.reserveRuntimeBrowserSession("/softphone/answer/new"); ok {
		t.Fatal("parallel action exceeded capacity")
	}
	a.rememberRuntimeGrant("new-call", grant)
	release()
	if _, ok = a.reserveRuntimeBrowserSession("/softphone/takeover/existing"); ok {
		t.Fatal("full map admitted token replacement")
	}
	if done, ok := a.reserveRuntimeBrowserSession("/softphone/renew/existing"); !ok {
		t.Fatal("existing renewal rejected")
	} else {
		done()
	}
	grant.Token = "renewed"
	a.rememberRuntimeGrant("new-call", grant)
	if a.runtimeBrowsers.grants["new-call"].Token != "renewed" {
		t.Fatal("capacity prevented renewal")
	}
}

func TestManagedRuntimeDirectBrowserPCMAndRevocation(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42, runtime: runtimeTestActive()}
	a.runtime.endpoints["browser"] = "https://voice.test"
	insertSoftphoneCall(t, a, "in-progress")
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	t.Setenv("APTEVA_PUBLIC_URL", "https://management.test")
	var revoked atomic.Bool
	grant := runtimeBrowserGrant{Token: "browser-grant", InstallID: 42, Project: "project-a", UserID: 9, Deployment: "deployment-a", Generation: 1, Origin: "https://management.test", Expires: time.Now().Add(5 * time.Minute).Unix(), Principal: &sdk.TrustedPrincipal{Version: 1, UserID: 9, ProjectID: "project-a", ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revoked.Load() {
			w.WriteHeader(403)
			return
		}
		writeJSON(w, grant)
	}))
	defer gateway.Close()
	a.runtime.http = gateway.Client()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)

	request := httptest.NewRequest("POST", runtimeBrowserPrefix+"/softphone/attach/call-soft-1", strings.NewReader("{}"))
	request.Header.Set("Origin", grant.Origin)
	request.Header.Set("Authorization", "Bearer "+grant.Token)
	response := httptest.NewRecorder()
	a.handleRuntimeBrowser(response, request)
	if response.Code != 200 {
		t.Fatal("direct session creation", response.Code, response.Body.String())
	}
	var session softphoneSession
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || session.SessionToken == "" || session.LeaseSeconds == 0 {
		t.Fatal("renewable direct lease missing", err)
	}

	var live sync.WaitGroup
	tracked := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { live.Add(1); defer live.Done(); handler(w, r) }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/", tracked(a.runtimeHTTP(a.handlePeerSocket, false)))
	mux.HandleFunc(runtimeBrowserPrefix+"/", tracked(a.handleRuntimeBrowser))
	server := httptest.NewServer(mux)
	defer func() { server.Close(); live.Wait() }()
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	defer peer.Close()
	dialer := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"Origin": []string{grant.Origin}})}
	conn, buffer, _, err := dialer.Dial(t.Context(), websocketURL(server.URL)+runtimeBrowserPrefix+"/softphone/media/call-soft-1/"+session.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	var browser net.Conn = conn
	if buffer != nil {
		browser = hijackedConn{Conn: conn, reader: buffer}
	}
	defer browser.Close()
	readSoftphoneEventWithin(t, browser, "ready", time.Second)
	pcm := pcm16ToBytes([]int16{1000, -4000, 3000})
	if err = wsutil.WriteClientBinary(browser, pcm); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, peer, time.Second); string(got) != string(pcm) {
		t.Fatal("direct microphone PCM changed")
	}
	if err = wsutil.WriteClientBinary(peer, pcm); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, browser, time.Second); string(got) != string(pcm) {
		t.Fatal("direct playback PCM changed")
	}
	revoked.Store(true)
	browser.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err = wsutil.ReadServerData(browser)
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatal("revoked stream retained access")
			}
			break
		}
	}
	row, err := a.db().findCall("call-soft-1")
	if err != nil || row == nil || isTerminalStatus(row.Status) {
		t.Fatal("browser revocation terminated carrier call", row, err)
	}
}

func TestManagedRuntimeDirectOperatorLegacySessionGetsRenewableLease(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42, runtime: runtimeTestActive()}
	row := insertSoftphoneCall(t, a, "in-progress")
	grant := runtimeBrowserGrant{Project: row.ProjectID, UserID: 9, Token: "scoped", Expires: time.Now().Add(300 * time.Second).Unix()}
	raw, _ := json.Marshal(softphoneSession{CallID: row.ID, MediaURL: a.softphoneMediaURL(row.ID, row.PeerToken)})
	leased, err := a.runtimeLeasedSession(raw, grant)
	if err != nil {
		t.Fatal(err)
	}
	var session softphoneSession
	if err = json.Unmarshal(leased, &session); err != nil || session.LeaseSeconds != phoneLeaseSeconds || session.SessionToken == row.PeerToken {
		t.Fatal("operator session was not converted", session.LeaseSeconds, err)
	}
	a.runtime.endpoints["browser"] = "https://voice.test"
	// Standard operator renewal updates the database lease, then direct ingress
	// can associate the same media token with newly issued browser credentials.
	request := httptest.NewRequest("POST", "/softphone/renew/"+row.ID, strings.NewReader(`{"session_token":"`+session.SessionToken+`"}`))
	request.Header.Set("X-Apteva-Project-ID", row.ProjectID)
	request.Header.Set("X-Apteva-Operator-ID", "9")
	w := httptest.NewRecorder()
	a.handleSoftphoneAction(w, request)
	if w.Code != 200 {
		t.Fatal("operator renewal failed", w.Code, w.Body.String())
	}
	original := grant
	original.Expires = time.Now().Add(-time.Second).Unix()
	a.rememberRuntimeMedia(leased, original)
	grant.Token = "renewed-browser"
	a.rememberRuntimeGrant(phoneHash(row.ID+":"+session.SessionToken), grant)
	_, got, ok := a.runtimeMediaGrant("/softphone/media/" + row.ID + "/" + session.SessionToken)
	if !ok || got.Token != grant.Token {
		t.Fatal("browser credential renewal did not retain media scope")
	}
}

func TestManagedRuntimeBootstrapUsesActualServerOperatorFormat(t *testing.T) {
	a := &App{installID: 42, runtime: runtimeTestActive()}
	a.runtime.endpoints["browser"] = "https://voice.test"
	t.Setenv("APTEVA_APP_TOKEN", "runtime-secret")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/installs/42/deployment/browser-credentials":
			if r.Header.Get("Authorization") != "Bearer original-user-credential" {
				t.Error("bootstrap did not use original user auth")
				w.WriteHeader(403)
				return
			}
			writeJSON(w, map[string]any{"token": "scoped-browser", "expires_at": time.Now().Add(5 * time.Minute).Unix(), "endpoint": "https://voice.test"})
		case "/api/apps/callback/runtime/browser-authorize":
			if r.Header.Get("Authorization") != "Bearer runtime-secret" {
				t.Error("callback did not use process auth")
				w.WriteHeader(403)
				return
			}
			// Current Server deliberately omits Principal for ordinary platform users.
			writeJSON(w, runtimeBrowserGrant{InstallID: 42, Project: "project-a", UserID: 9, Deployment: "deployment-a", Generation: 1, Origin: "https://management.test", Expires: time.Now().Add(5 * time.Minute).Unix()})
		default:
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	a.runtime.http = gateway.Client()
	bootstrap := func(operator, subject string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/runtime-browser-bootstrap", strings.NewReader(`{"origin":"https://management.test"}`))
		r.Header.Set("X-Apteva-Operator-ID", operator)
		r.Header.Set("X-Apteva-Subject-ID", subject)
		r.Header.Set("X-Apteva-Original-Authorization", "Bearer original-user-credential")
		w := httptest.NewRecorder()
		a.handleRuntimeBrowserBootstrap(w, r)
		return w
	}
	if w := bootstrap("9", ""); w.Code != 200 || strings.Contains(w.Body.String(), "runtime-secret") || !strings.Contains(w.Body.String(), "scoped-browser") {
		t.Fatal("actual operator format failed", w.Code, w.Body.String())
	}
	for _, tc := range [][2]string{{"10", ""}, {"9", "customer-subject"}, {"", ""}} {
		if w := bootstrap(tc[0], tc[1]); w.Code != 403 {
			t.Fatal("identity mismatch/delegation accepted", tc, w.Code)
		}
	}
}

func TestManagedRuntimeDirectSessionProjectAndGlobalScope(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42, runtime: runtimeTestActive()}
	row := insertSoftphoneCall(t, a, "in-progress")
	raw, _ := json.Marshal(softphoneSession{CallID: row.ID, MediaURL: a.softphoneMediaURL(row.ID, row.PeerToken), SessionToken: row.PeerToken})
	if _, err := a.runtimeLeasedSession(raw, runtimeBrowserGrant{Project: "foreign-project", UserID: 9}); err == nil {
		t.Fatal("project grant crossed calls")
	}
	// Server permits a global installation credential only to platform admins.
	// Their existing authorized call action can select a project within that app.
	if _, err := a.runtimeLeasedSession(raw, runtimeBrowserGrant{Project: "", UserID: 9}); err != nil {
		t.Fatal("global operator cannot lease an authorized project call", err)
	}
}
