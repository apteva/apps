package main

import (
	"context"
	"errors"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Only the test adapter shortens installed deadlines. The production Gateway
// handler, HTTP server, proxy pool and AppBus implementation run unchanged.
type shortReadDeadlineWriter struct{ http.ResponseWriter }

func (w shortReadDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w shortReadDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		deadline = time.Now().Add(30 * time.Millisecond)
	}
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}
func (w shortReadDeadlineWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func TestSessionAfterSSEReusesHealthyConnection(t *testing.T) {
	testStreamSession(t, 80*time.Millisecond, 3, false)
}
func TestLongStreamSessionWithRealAuth(t *testing.T) {
	if os.Getenv("API_LONG_STREAM") != "1" {
		t.Skip("set API_LONG_STREAM=1 for three >30-second cycles with a real Auth sidecar")
	}
	testStreamSession(t, 32*time.Second, 3, true)
}
func testStreamSession(t *testing.T, lifetime time.Duration, cycles int, realAuth bool) {
	token := "test-token"
	var authProxy *httputil.ReverseProxy
	if realAuth {
		auth := tk.SpawnSidecar(t, "../auth", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""), tk.WithConfig(map[string]string{"email_verification_required": "false", "app_url": "http://localhost:8080"}))
		out := auth.MCP("auth_clients_create", map[string]any{"organization_slug": "default", "name": "incident", "type": "spa", "redirect_uris": []any{"http://localhost:3000/callback"}})
		var signup map[string]any
		response := auth.POST("/signup", map[string]any{"email": "incident@example.com", "password": "GoodPassword123", "client_id": out["client_id"]}, &signup)
		if response.Status != 201 {
			t.Fatalf("Auth signup status: %d", response.Status)
		}
		token, _ = signup["access_token"].(string)
		if token == "" {
			t.Fatal("Auth returned no access token")
		}
		target, _ := url.Parse(auth.URL())
		authProxy = httputil.NewSingleHostReverseProxy(target)
		director := authProxy.Director
		authProxy.Director = func(r *http.Request) { director(r); r.URL.Path = "/me" }
	}
	var early atomic.Bool
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/auth/me":
			if authProxy != nil {
				authProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"user":{"id":1}}`)
		case "/api/app-events/tables":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-time.After(lifetime):
			case <-r.Context().Done():
				early.Store(true)
			}
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-test")
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "incident", AuthJSON: `{"kind":"auth_jwt"}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []routeInput{
		{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/live", TargetKind: "app_events", TargetRef: "tables", Enabled: true, EventsJSON: `{"topics":["row.updated"],"output":{"type":"invalidate"}}`},
		{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/session", TargetKind: "http", TargetRef: platform.URL, Enabled: true},
	} {
		if _, _, err := dbUpsertRoute(ctx.AppDB(), route); err != nil {
			t.Fatal(err)
		}
	}
	var connections atomic.Int32
	gateway := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Err() != nil {
			t.Error("gateway received a poisoned request context")
		}
		if !realAuth {
			w = shortReadDeadlineWriter{w}
		}
		app.handleGateway(w, r)
	}))
	gateway.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	gateway.Start()
	defer gateway.Close()
	target, _ := url.Parse(gateway.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConnsPerHost = 1
	transport.ForceAttemptHTTP2 = false
	proxy.Transport = transport
	defer transport.CloseIdleConnections()
	edge := httptest.NewServer(proxy)
	defer edge.Close()
	client := edge.Client()
	client.Timeout = 45 * time.Second
	for cycle := 0; cycle < cycles; cycle++ {
		for _, path := range []string{"live", "session", "session"} {
			req, _ := http.NewRequest("GET", edge.URL+"/gw/incident/"+path+"?project_id="+testProject, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			started := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("cycle %d %s: status=%d err=%v body=%s", cycle, path, resp.StatusCode, err, body)
			}
			if path == "live" && (time.Since(started) < lifetime || !strings.Contains(string(body), "event: ready")) {
				t.Fatal("stream ended before its expected lifetime")
			}
		}
	}
	if early.Load() {
		t.Fatal("upstream stream canceled early")
	}
	if connections.Load() != 1 {
		t.Fatalf("test did not reuse a single gateway connection: %d", connections.Load())
	}
	t.Logf("%d stream/session cycles passed on one HTTP/1.1 connection; stream duration=%s real_auth=%v", cycles, lifetime, realAuth)
}

type incidentRoundTripper func(*http.Request) (*http.Response, error)

func (f incidentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGatewayAuthFailureClassification(t *testing.T) {
	for _, item := range []struct {
		name           string
		upstream, want int
		body           string
		cause          error
	}{
		{"invalid", 401, 401, "secret-token", nil}, {"forbidden", 403, 403, "", nil}, {"busy", 429, 503, "", nil}, {"unavailable", 503, 503, "", nil}, {"failure", 500, 502, "", nil}, {"timeout", 504, 504, "", nil}, {"malformed", 200, 502, "{}", nil}, {"transport", 0, 502, "", errors.New("dial failed")}, {"deadline", 0, 504, "", context.DeadlineExceeded}, {"canceled upstream", 0, 503, "", context.Canceled},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("APTEVA_GATEWAY_URL", "http://internal.invalid")
			app, ctx := mountTestApp(t)
			app.httpClient = &http.Client{Transport: incidentRoundTripper(func(r *http.Request) (*http.Response, error) {
				if item.cause != nil {
					return nil, item.cause
				}
				return &http.Response{StatusCode: item.upstream, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(item.body))}, nil
			})}
			api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "errors", AuthJSON: `{"kind":"auth_jwt"}`})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/", TargetKind: "http", TargetRef: "http://origin.invalid", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/gw/errors/?project_id="+testProject, nil)
			r.Header.Set("Authorization", "Bearer secret-token")
			rec := httptest.NewRecorder()
			app.handleGateway(rec, r)
			if rec.Code != item.want || rec.Header().Get("X-Request-ID") == "" {
				t.Fatalf("status=%d want=%d", rec.Code, item.want)
			}
			for _, secret := range []string{"secret-token", "internal.invalid", "origin.invalid"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatal("internal details leaked")
				}
			}
		})
	}
}
func TestGatewayCanceledAuthAbortsInsteadOf401(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "cancel", AuthJSON: `{"kind":"auth_jwt"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/", TargetKind: "http", TargetRef: "http://origin.invalid", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/gw/cancel/?project_id="+testProject, nil).WithContext(parent)
	r.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("canceled connection was not aborted")
		}
		if rec.Code == 401 || rec.Body.Len() != 0 {
			t.Error("cancellation became a credential response")
		}
	}()
	app.handleGateway(rec, r)
}
