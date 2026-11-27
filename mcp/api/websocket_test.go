package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

// fakeWSPlatform stands in for the platform app proxy in front of an app
// that serves a WebSocket at /api/stream/<id>, like Mobile's live video.
type fakeWSPlatform struct {
	mu      sync.Mutex
	headers []http.Header
	queries []string
}

func (p *fakeWSPlatform) lastHeader() http.Header {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.headers) == 0 {
		return nil
	}
	return p.headers[len(p.headers)-1]
}

func (p *fakeWSPlatform) lastQuery() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queries) == 0 {
		return ""
	}
	return p.queries[len(p.queries)-1]
}

func (p *fakeWSPlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/apps/callback/apps/mobile/proxy"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == "/health" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.HasPrefix(path, "/api/stream/") {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	p.headers = append(p.headers, r.Header.Clone())
	p.queries = append(p.queries, r.URL.RawQuery)
	p.mu.Unlock()
	if r.URL.Query().Get("token") == "bad" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"stream token is invalid or expired"}`))
		return
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch string(data) {
		case "stop":
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stream ended: stopped"), time.Now().Add(time.Second))
			return
		case "big":
			_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 4096))
			continue
		}
		if err := conn.WriteMessage(mt, data); err != nil {
			return
		}
	}
}

type wsFixture struct {
	app      *App
	ctx      *sdk.AppCtx
	platform *fakeWSPlatform
	gateway  *httptest.Server
	apiID    int64
}

func newWSFixture(t *testing.T, routeArgs map[string]any) *wsFixture {
	t.Helper()
	platform := &fakeWSPlatform{}
	upstream := httptest.NewServer(platform)
	t.Cleanup(upstream.Close)
	t.Setenv("APTEVA_GATEWAY_URL", upstream.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")
	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "live", "auth": map[string]any{"kind": "api_key"}}); err != nil {
		t.Fatal(err)
	}
	api, err := dbGetAPIBySlug(ctx.AppDB(), testProject, "live")
	if err != nil || api == nil {
		t.Fatalf("api = %+v err=%v", api, err)
	}
	args := map[string]any{
		"api_slug": "live", "method": "GET", "path_pattern": "/api/stream/*",
		"target_kind": "app", "target_ref": "mobile",
		"websocket": true,
	}
	for k, v := range routeArgs {
		args[k] = v
	}
	if _, err := app.toolRouteAdd(ctx, args); err != nil {
		t.Fatalf("route add: %v", err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "live", "method": "POST", "path_pattern": "/ws-ticket",
		"target_kind": "ws_ticket", "target_ref": "gateway",
	}); err != nil {
		t.Fatalf("ticket route add: %v", err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
	t.Cleanup(gateway.Close)
	return &wsFixture{app: app, ctx: ctx, platform: platform, gateway: gateway, apiID: api.ID}
}

func (f *wsFixture) key(t *testing.T, scopes ...string) (int64, string) {
	t.Helper()
	args := map[string]any{"api_slug": "live", "name": "viewer"}
	if len(scopes) > 0 {
		list := make([]any, len(scopes))
		for i, s := range scopes {
			list[i] = s
		}
		args["scopes"] = list
	}
	out, err := f.app.toolKeyCreate(f.ctx, args)
	if err != nil {
		t.Fatalf("key create: %v", err)
	}
	m := out.(map[string]any)
	raw, _ := json.Marshal(m["key"])
	var key struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(raw, &key)
	return key.ID, m["secret"].(string)
}

func (f *wsFixture) url(path, query string) string {
	u := "ws" + strings.TrimPrefix(f.gateway.URL, "http") + "/gw/live" + path + "?project_id=" + testProject
	if query != "" {
		u += "&" + query
	}
	return u
}

func (f *wsFixture) dial(t *testing.T, path, query string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := d.Dial(f.url(path, query), header)
	if conn != nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, resp, err
}

func (f *wsFixture) ticket(t *testing.T, secret string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.gateway.URL+"/gw/live/ws-ticket?project_id="+testProject, nil)
	req.Header.Set("X-API-Key", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusCreated || out.Ticket == "" || out.ExpiresIn != 60 {
		t.Fatalf("ticket status=%d out=%+v", resp.StatusCode, out)
	}
	return out.Ticket
}

func keyHeader(secret string) http.Header { return http.Header{"X-Api-Key": {secret}} }

func expectClose(t *testing.T, conn *websocket.Conn, code int, reason string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("read error = %v, want close %d", err, code)
		}
		if ce.Code != code || (reason != "" && ce.Text != reason) {
			t.Fatalf("close = %d %q, want %d %q", ce.Code, ce.Text, code, reason)
		}
		return
	}
}

func lastLog(t *testing.T, f *wsFixture) *RequestLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := dbListLogs(f.ctx.AppDB(), testProject, f.apiID, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range logs {
			if l.StatusCode == http.StatusSwitchingProtocols {
				return l
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no websocket log row")
	return nil
}

func TestWebSocketRelaysFramesCallerAndUpstreamClose(t *testing.T) {
	f := newWSFixture(t, nil)
	keyID, secret := f.key(t)
	header := keyHeader(secret)
	header.Set("X-Apteva-Gateway-Key-ID", "999") // forged; must be replaced
	conn, resp, err := f.dial(t, "/api/stream/s1", "token=good", header)
	if err != nil {
		t.Fatalf("dial: %v status=%v", err, resp)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if mt, data, err := conn.ReadMessage(); err != nil || mt != websocket.TextMessage || string(data) != "hello" {
		t.Fatalf("text echo = %d %q %v", mt, data, err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 2, 255}); err != nil {
		t.Fatal(err)
	}
	if mt, data, err := conn.ReadMessage(); err != nil || mt != websocket.BinaryMessage || string(data) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("binary echo = %d %v %v", mt, data, err)
	}

	h := f.platform.lastHeader()
	if h.Get("Authorization") != "Bearer outbound-token" || h.Get("X-Api-Key") != "" {
		t.Fatalf("upstream credentials: auth=%q key=%q", h.Get("Authorization"), h.Get("X-Api-Key"))
	}
	if h.Get("X-Apteva-Gateway-Key-Id") != strconv.FormatInt(keyID, 10) || h.Get("X-Apteva-Gateway-Auth-Kind") != "api_key" || h.Get("X-Apteva-Gateway-Principal") == "" {
		t.Fatalf("caller headers = %v", h)
	}
	if q := f.platform.lastQuery(); !strings.Contains(q, "token=good") || strings.Contains(q, "api_key") || !strings.Contains(q, "project_id="+testProject) {
		t.Fatalf("upstream query = %q", q)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte("stop")); err != nil {
		t.Fatal(err)
	}
	expectClose(t, conn, websocket.CloseNormalClosure, "stream ended: stopped")

	row := lastLog(t, f)
	if row.BytesIn != int64(len("hello")+4+len("stop")) || row.BytesOut != int64(len("hello")+4) {
		t.Fatalf("bytes in/out = %d/%d", row.BytesIn, row.BytesOut)
	}
	if row.CloseCode != 1000 || row.CloseReason != "stream ended: stopped" || row.OpenedAt == "" || row.Subject == "" {
		t.Fatalf("log row = %+v", row)
	}
}

func TestWebSocketScopeRequired(t *testing.T) {
	f := newWSFixture(t, map[string]any{"auth": map[string]any{"kind": "api_key", "required_scopes": []any{"stream:view"}}})
	_, noScope := f.key(t)
	if _, resp, err := f.dial(t, "/api/stream/s1", "", keyHeader(noScope)); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-scope dial err=%v resp=%v", err, resp)
	}
	// A ticket carries the key, not its rights: still refused.
	ticket := f.ticket(t, noScope)
	if _, resp, err := f.dial(t, "/api/stream/s1", "ticket="+ticket, nil); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-scope ticket dial err=%v resp=%v", err, resp)
	}
	_, scoped := f.key(t, "stream:view")
	if _, resp, err := f.dial(t, "/api/stream/s1", "", keyHeader(scoped)); err != nil {
		t.Fatalf("scoped dial err=%v resp=%v", err, resp)
	}
}

func TestWebSocketTicketIsSingleUseAndKeyNeverInURL(t *testing.T) {
	f := newWSFixture(t, nil)
	_, secret := f.key(t)
	if _, resp, err := f.dial(t, "/api/stream/s1", "api_key="+secret, nil); err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("api_key in URL err=%v resp=%v", err, resp)
	}
	ticket := f.ticket(t, secret)
	conn, resp, err := f.dial(t, "/api/stream/s1", "ticket="+ticket, nil)
	if err != nil {
		t.Fatalf("ticket dial err=%v resp=%v", err, resp)
	}
	if q := f.platform.lastQuery(); strings.Contains(q, "ticket") {
		t.Fatalf("ticket forwarded upstream: %q", q)
	}
	conn.Close()
	if _, resp, err := f.dial(t, "/api/stream/s1", "ticket="+ticket, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket err=%v resp=%v", err, resp)
	}
	if _, resp, err := f.dial(t, "/api/stream/s1", "ticket=wst_unknown", nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown ticket err=%v resp=%v", err, resp)
	}
	// Expired tickets fail.
	raw, _, err := f.app.wsTickets.mint(testProject, f.apiID, 1, time.Now().Add(-2*wsTicketTTL))
	if err != nil {
		t.Fatal(err)
	}
	if _, resp, err := f.dial(t, "/api/stream/s1", "ticket="+raw, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired ticket err=%v resp=%v", err, resp)
	}
}

func TestWebSocketRevokeClosesOpenConnection(t *testing.T) {
	f := newWSFixture(t, nil)
	keyID, secret := f.key(t)
	ticket := f.ticket(t, secret)
	conn, resp, err := f.dial(t, "/api/stream/s1", "ticket="+ticket, nil)
	if err != nil {
		t.Fatalf("dial err=%v resp=%v", err, resp)
	}
	// Outstanding tickets die with the key too.
	spare := f.ticket(t, secret)
	start := time.Now()
	if _, err := f.app.toolKeyRevoke(f.ctx, map[string]any{"id": float64(keyID)}); err != nil {
		t.Fatal(err)
	}
	expectClose(t, conn, wsCloseCredentialRevoked, "credential revoked")
	if time.Since(start) > 3*time.Second {
		t.Fatalf("revocation took %v", time.Since(start))
	}
	if _, resp, err := f.dial(t, "/api/stream/s1", "ticket="+spare, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ticket after revoke err=%v resp=%v", err, resp)
	}
	if _, resp, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret)); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key err=%v resp=%v", err, resp)
	}
	if row := lastLog(t, f); row.CloseCode != wsCloseCredentialRevoked {
		t.Fatalf("log close = %d %q", row.CloseCode, row.CloseReason)
	}
}

func TestWebSocketLimits(t *testing.T) {
	t.Run("connections per key", func(t *testing.T) {
		f := newWSFixture(t, map[string]any{"websocket": map[string]any{"max_connections_per_key": 1}})
		_, secret := f.key(t)
		if _, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret)); err != nil {
			t.Fatal(err)
		}
		if _, resp, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret)); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("second connection err=%v resp=%v", err, resp)
		}
		_, other := f.key(t)
		if _, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(other)); err != nil {
			t.Fatalf("other key: %v", err)
		}
	})
	t.Run("message size", func(t *testing.T) {
		f := newWSFixture(t, map[string]any{"websocket": map[string]any{"max_message_bytes": 1024}})
		_, secret := f.key(t)
		conn, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 2048))
		expectClose(t, conn, websocket.CloseMessageTooBig, "")
	})
	t.Run("upstream message size", func(t *testing.T) {
		f := newWSFixture(t, map[string]any{"websocket": map[string]any{"max_message_bytes": 1024}})
		_, secret := f.key(t)
		conn, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte("big"))
		expectClose(t, conn, websocket.CloseMessageTooBig, "upstream message exceeds the route's max_message_bytes")
	})
	t.Run("idle timeout", func(t *testing.T) {
		f := newWSFixture(t, map[string]any{"websocket": map[string]any{"idle_timeout_seconds": 1}})
		_, secret := f.key(t)
		conn, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret))
		if err != nil {
			t.Fatal(err)
		}
		expectClose(t, conn, wsCloseIdleTimeout, "idle timeout")
	})
	t.Run("max duration", func(t *testing.T) {
		f := newWSFixture(t, map[string]any{"websocket": map[string]any{"max_duration_seconds": 1, "idle_timeout_seconds": 60}})
		_, secret := f.key(t)
		conn, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret))
		if err != nil {
			t.Fatal(err)
		}
		expectClose(t, conn, wsCloseMaxDuration, "max connection duration reached")
	})
	t.Run("bytes per second", func(t *testing.T) {
		f := newWSFixture(t, map[string]any{"websocket": map[string]any{"bytes_per_second": 2048}})
		_, secret := f.key(t)
		conn, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for i := 0; i < 3; i++ {
			if err := conn.WriteMessage(websocket.BinaryMessage, make([]byte, 2048)); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 3; i++ {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}
		}
		// 6 KiB at 2 KiB/s with a one-second burst takes about two seconds.
		if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
			t.Fatalf("throttle not applied: %v", elapsed)
		}
	})
}

func TestWebSocketRouteChangeAndRefusals(t *testing.T) {
	f := newWSFixture(t, nil)
	_, secret := f.key(t)
	if _, resp, err := f.dial(t, "/api/stream/s1", "token=bad", keyHeader(secret)); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("upstream refusal err=%v resp=%v", err, resp)
	}
	conn, _, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret))
	if err != nil {
		t.Fatal(err)
	}
	// Turning websocket off on the route closes open connections.
	if _, err := f.app.toolRouteAdd(f.ctx, map[string]any{
		"api_slug": "live", "method": "GET", "path_pattern": "/api/stream/*",
		"target_kind": "app", "target_ref": "mobile",
	}); err != nil {
		t.Fatal(err)
	}
	expectClose(t, conn, websocket.CloseServiceRestart, "route configuration changed")
	if _, resp, err := f.dial(t, "/api/stream/s1", "", keyHeader(secret)); err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disabled route err=%v resp=%v", err, resp)
	}
}

func TestWebSocketPublicRouteRefused(t *testing.T) {
	f := newWSFixture(t, map[string]any{"path_pattern": "/open/*", "auth": map[string]any{"kind": "public"}})
	if _, resp, err := f.dial(t, "/open/s1", "", nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("public websocket err=%v resp=%v", err, resp)
	}
}

func TestWebSocketRouteValidation(t *testing.T) {
	for name, raw := range map[string]string{
		"http target": `{"enabled":true}`,
	} {
		if _, err := normalizeWebSocketSetting("http", "GET", raw); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := normalizeWebSocketSetting("app", "POST", `{"enabled":true}`); err == nil {
		t.Fatal("POST websocket route accepted")
	}
	if _, err := normalizeWebSocketSetting("app", "GET", `{"enabled":true,"max_message_bytes":999999999}`); err == nil {
		t.Fatal("oversized max_message_bytes accepted")
	}
	if _, err := normalizeWebSocketSetting("app", "GET", `{"enabled":true,"bogus":1}`); err == nil {
		t.Fatal("unknown websocket field accepted")
	}
	got, err := normalizeWebSocketSetting("app", "GET", `{"enabled":true}`)
	if err != nil || !strings.Contains(got, `"max_connections_per_key":4`) || !strings.Contains(got, `"max_message_bytes":4194304`) {
		t.Fatalf("defaults = %s err=%v", got, err)
	}
	if got, err := normalizeWebSocketSetting("http", "GET", `{}`); err != nil || got != "{}" {
		t.Fatalf("disabled = %s err=%v", got, err)
	}
	if err := validateTarget("ws_ticket", "other"); err == nil {
		t.Fatal("ws_ticket accepted a target_ref other than gateway")
	}
}
