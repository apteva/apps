package browserbase

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/target"
)

func boolPointer(v bool) *bool { return &v }

func TestCreateSessionMapsProxyCountryToBrowserbaseGeolocation(t *testing.T) {
	var payload struct {
		Proxies []struct {
			Type        string `json:"type"`
			Geolocation struct {
				Country string `json:"country"`
			} `json:"geolocation"`
		} `json:"proxies"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode session request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"bb_session","status":"RUNNING","connectUrl":"ws://browserbase.test/session"}`)
	}))
	defer srv.Close()

	previousAPIBase := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = previousAPIBase })

	c := &Computer{
		apiKey:    "bb_key",
		projectID: "bb_project",
		display:   computer.DisplaySize{Width: 1600, Height: 800},
		http:      srv.Client(),
	}
	if _, err := c.createSession(computer.OpenOptions{ProxyCountry: " de "}); err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if len(payload.Proxies) != 1 {
		t.Fatalf("proxies: got %d entries, want 1", len(payload.Proxies))
	}
	if got := payload.Proxies[0].Type; got != "browserbase" {
		t.Fatalf("proxy type: got %q, want browserbase", got)
	}
	if got := payload.Proxies[0].Geolocation.Country; got != "DE" {
		t.Fatalf("proxy country: got %q, want DE", got)
	}
}

func TestResolveProxiesRejectsContradictoryOrInvalidCountry(t *testing.T) {
	tests := []struct {
		name string
		opts computer.OpenOptions
		want string
	}{
		{name: "proxy disabled", opts: computer.OpenOptions{Proxy: boolPointer(false), ProxyCountry: "DE"}, want: "proxy=false"},
		{name: "invalid country", opts: computer.OpenOptions{ProxyCountry: "Germany"}, want: "two-letter ISO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := resolveProxies(nil, tt.opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolveProxies error: got %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestResolveProxiesPreservesExistingBooleanSemantics(t *testing.T) {
	defaults := []map[string]any{{"type": "external"}}
	if got, err := resolveProxies(defaults, computer.OpenOptions{}); err != nil || len(got.([]map[string]any)) != 1 {
		t.Fatalf("defaults: got %#v, err=%v", got, err)
	}
	if got, err := resolveProxies(defaults, computer.OpenOptions{Proxy: boolPointer(true)}); err != nil || got != true {
		t.Fatalf("proxy=true: got %#v, err=%v", got, err)
	}
	if got, err := resolveProxies(defaults, computer.OpenOptions{Proxy: boolPointer(false)}); err != nil || got != nil {
		t.Fatalf("proxy=false: got %#v, err=%v", got, err)
	}
}

func TestResolveProxiesMapsExternalCredentials(t *testing.T) {
	got, err := resolveProxies(nil, computer.OpenOptions{ExternalProxy: &computer.ExternalProxy{
		Server: "http://proxy.example:823", Username: "agent-user", Password: "agent-pass",
	}})
	if err != nil {
		t.Fatalf("resolveProxies: %v", err)
	}
	proxies, ok := got.([]map[string]any)
	if !ok || len(proxies) != 1 {
		t.Fatalf("proxies = %#v", got)
	}
	want := map[string]any{
		"type": "external", "server": "http://proxy.example:823",
		"username": "agent-user", "password": "agent-pass",
	}
	for key, expected := range want {
		if proxies[0][key] != expected {
			t.Fatalf("%s = %#v, want %#v", key, proxies[0][key], expected)
		}
	}
}

func TestPickInitialPageTargetReusesExistingContentPage(t *testing.T) {
	infos := []*target.Info{
		{TargetID: "worker", Type: "service_worker", URL: "https://example.test/sw.js"},
		{TargetID: "blank", Type: "page", URL: "about:blank"},
		{TargetID: "prerender", Type: "page", Subtype: "prerender", URL: "https://future.test"},
		{TargetID: "content", Type: "page", URL: "https://example.test/app"},
	}
	if got := pickInitialPageTarget(infos); got != target.ID("content") {
		t.Fatalf("pickInitialPageTarget = %q, want content", got)
	}
}

func TestPickInitialPageTargetKeepsProviderBlankPage(t *testing.T) {
	infos := []*target.Info{{TargetID: "provider-page", Type: "page", URL: "about:blank"}}
	if got := pickInitialPageTarget(infos); got != target.ID("provider-page") {
		t.Fatalf("pickInitialPageTarget = %q, want provider-page", got)
	}
	if got := pickInitialPageTarget(nil); got != "" {
		t.Fatalf("pickInitialPageTarget(nil) = %q", got)
	}
}

func TestActionTimeoutsAreBounded(t *testing.T) {
	cases := map[string]time.Duration{
		"click":         clickActionTimeout,
		"double_click":  clickActionTimeout,
		"type":          textActionTimeout,
		"key":           keyActionTimeout,
		"scroll":        scrollActionTimeout,
		"wait":          waitActionTimeout,
		"navigate":      navigateActionTimeout,
		"back":          navigateActionTimeout,
		"reload":        navigateActionTimeout,
		"upload_file":   30 * time.Second,
		"select_option": 20 * time.Second,
		"set_checked":   20 * time.Second,
		"set_temporal":  20 * time.Second,
	}
	for action, want := range cases {
		if got := actionTimeout(action); got != want {
			t.Fatalf("actionTimeout(%q): want %s, got %s", action, want, got)
		}
	}
}

func TestSleepWithContextReportsActionTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := sleepWithContext(ctx, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "action_timeout") {
		t.Fatalf("sleepWithContext timeout: want action_timeout, got %v", err)
	}
}

func TestUploadSessionFileUsesBrowserbaseUploadsAPI(t *testing.T) {
	dir := t.TempDir()
	localFile := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(localFile, []byte("image bytes"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	var sawPath, sawAPIKey, sawFilename, sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawAPIKey = r.Header.Get("X-BB-API-Key")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		raw, _ := io.ReadAll(file)
		sawFilename = header.Filename
		sawBody = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
	}))
	defer srv.Close()

	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = prev })

	c := &Computer{
		apiKey:    "bb_key",
		sessionID: "bb_session",
		http:      srv.Client(),
	}
	remote, err := c.uploadSessionFile(localFile)
	if err != nil {
		t.Fatalf("uploadSessionFile: %v", err)
	}
	if remote != "/tmp/.uploads/photo.jpg" {
		t.Fatalf("remote path: got %q", remote)
	}
	if sawPath != "/sessions/bb_session/uploads" || sawAPIKey != "bb_key" {
		t.Fatalf("request path/key: path=%q key=%q", sawPath, sawAPIKey)
	}
	if sawFilename != "photo.jpg" || sawBody != "image bytes" {
		t.Fatalf("multipart file: filename=%q body=%q", sawFilename, sawBody)
	}
}

func TestCloseReleasesThenReportsFinalProxyUsage(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode release: %v", err)
			}
			if body["status"] != "REQUEST_RELEASE" {
				t.Fatalf("release status = %#v", body["status"])
			}
			_, _ = io.WriteString(w, `{"id":"bb_usage","status":"PENDING","proxyBytes":10}`)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"bb_usage","status":"COMPLETED","proxyBytes":4213920}`)
		}
	}))
	defer srv.Close()
	previousAPIBase := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = previousAPIBase })

	c := &Computer{apiKey: "bb_key", projectID: "bb_project", sessionID: "bb_usage", http: srv.Client()}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.sessionID != "" || c.closedSessionID != "bb_usage" {
		t.Fatalf("session ids after close: active=%q closed=%q", c.sessionID, c.closedSessionID)
	}
	usage, err := c.SessionUsage(context.Background())
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if usage.Status != computer.SessionUsageReady || usage.ProxyBytes == nil || *usage.ProxyBytes != 4213920 || usage.MeasuredAt.IsZero() {
		t.Fatalf("usage = %+v", usage)
	}
	want := []string{"POST /sessions/bb_usage", "GET /sessions/bb_usage"}
	if len(requests) != len(want) || requests[0] != want[0] || requests[1] != want[1] {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestSessionUsageRetriesUntilProviderSessionIsTerminal(t *testing.T) {
	getCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"status":"PENDING","proxyBytes":1}`)
			return
		}
		getCalls++
		if getCalls < 3 {
			_, _ = io.WriteString(w, `{"status":"RUNNING","proxyBytes":123}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"COMPLETED","proxyBytes":456}`)
	}))
	defer srv.Close()
	previousAPIBase := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = previousAPIBase })

	c := &Computer{apiKey: "bb_key", sessionID: "bb_delayed", http: srv.Client()}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	usage, err := c.SessionUsage(ctx)
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if getCalls != 3 || usage.ProxyBytes == nil || *usage.ProxyBytes != 456 {
		t.Fatalf("calls=%d usage=%+v", getCalls, usage)
	}
	// Ready usage is cached; repeated readers do not hit Browserbase again.
	if _, err := c.SessionUsage(context.Background()); err != nil || getCalls != 3 {
		t.Fatalf("cached usage: calls=%d err=%v", getCalls, err)
	}
}

func TestUsageFailureNeverPreventsProviderClose(t *testing.T) {
	postCalls, getCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalls++
			http.Error(w, "release unavailable", http.StatusBadGateway)
			return
		}
		getCalls++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	previousAPIBase := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = previousAPIBase })

	c := &Computer{apiKey: "bb_key", sessionID: "bb_failure", http: srv.Client()}
	if err := c.Close(); err != nil {
		t.Fatalf("telemetry/release failure made Close fail: %v", err)
	}
	if c.sessionID != "" || c.closedSessionID != "bb_failure" || postCalls != 1 {
		t.Fatalf("close state: active=%q closed=%q posts=%d", c.sessionID, c.closedSessionID, postCalls)
	}
	if _, err := c.SessionUsage(context.Background()); err == nil || getCalls != 1 {
		t.Fatalf("usage failure: calls=%d err=%v", getCalls, err)
	}
}
