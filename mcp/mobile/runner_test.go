package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

const testToken = "runner-test-token-at-least-32-characters"

func TestRunnerAuthenticationAndProjectIsolation(t *testing.T) {
	e, _ := fixture(t)
	server := httptest.NewServer(runnerHandler(e, testToken))
	defer server.Close()
	res, err := http.Post(server.URL+"/v1/call", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("unauthenticated request accepted")
	}
	client, err := newRemote(server.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	v := execute(t, client, "p", "mobile_session", map[string]any{"action": "open", "device_id": "android:phone"})
	var out struct {
		Session Session `json:"session"`
	}
	if err = decodeMap(v, &out); err != nil {
		t.Fatal(err)
	}
	v = execute(t, client, "p", "mobile_observe", map[string]any{"session_id": out.Session.ID})
	var obs Observation
	_ = decodeMap(v, &obs)
	if obs.Width != 100 || obs.Image == "" {
		t.Fatal("screenshot lost across runner boundary")
	}
	if _, err = client.Execute(context.Background(), "other", "mobile_observe", map[string]any{"session_id": out.Session.ID}); errorCode(err) != "not_found" {
		t.Fatalf("project leak: %v", err)
	}
	execute(t, client, "p", "mobile_session", map[string]any{"action": "close", "session_id": out.Session.ID})
	rid := submit(t, e, []Step{{Action: "screenshot"}})
	r := waitRun(t, e, "p", rid)
	if r.Status != "passed" {
		t.Fatalf("report: %+v", r)
	}
	if _, err = client.Evidence(context.Background(), "p", r.Results[0].ArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Evidence(context.Background(), "other", r.Results[0].ArtifactID); errorCode(err) != "not_found" {
		t.Fatal("artifact crossed project boundary")
	}
}
func TestRemoteEndpointValidation(t *testing.T) {
	for _, raw := range []string{"http://public.example.com", "file:///tmp/runner", "https://token:secret@example.com", "https://example.com?token=secret", "https://example.com/#fragment"} {
		if _, err := newRemote(raw, testToken); err == nil {
			t.Fatalf("unsafe runner URL %s", raw)
		}
	}
	if _, err := newRemote("https://example.com", "short"); err == nil {
		t.Fatal("short token accepted")
	}
}
func TestRunnerRejectsCrossOriginAndOversizedBodies(t *testing.T) {
	e, _ := fixture(t)
	handler := runnerHandler(e, testToken)
	for _, tc := range []struct {
		origin, body string
		status       int
	}{{"https://untrusted.example", `{}`, 403}, {"", `{} {}`, 400}, {"", strings.Repeat(" ", 2<<20+1), 400}, {"", `{"protocol":"wrong"}`, 400}} {
		req := httptest.NewRequest("POST", "http://runner/v1/call", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("got %d want %d: %s", rec.Code, tc.status, rec.Body.String())
		}
	}
}
func TestRemoteDoesNotForwardTokenThroughRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("runner credential followed redirect") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	client, _ := newRemote(redirect.URL, testToken)
	if _, err := client.Execute(context.Background(), "p", "mobile_devices", nil); err == nil {
		t.Fatal("redirect accepted")
	}
}
func TestPanelHTTPRejectsProjectOverride(t *testing.T) {
	e, _ := fixture(t)
	app := &App{executor: e, project: "p"}
	request := httptest.NewRequest("POST", "/api/call?project_id=other", strings.NewReader(`{"tool":"mobile_devices"}`))
	rec := httptest.NewRecorder()
	app.handleCall(rec, request)
	if rec.Code != 403 {
		t.Fatalf("project override: %d", rec.Code)
	}
}
func TestPanelAndMCPUseSameTools(t *testing.T) {
	app := &App{}
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range app.MCPTools() {
		if tool.HandlerCtx == nil {
			t.Fatalf("%s has no cancellable handler", tool.Name)
		}
		names[tool.Name] = true
		if required, ok := tool.InputSchema["required"]; ok && required == nil {
			t.Fatalf("invalid null required schema for %s", tool.Name)
		}
		if _, err := json.Marshal(tool.InputSchema); err != nil {
			t.Fatal(err)
		}
	}
	if len(names) != 9 || len(m.Provides.MCPTools) != len(names) {
		t.Fatalf("manifest and implementation disagree: %d tools vs %d declarations", len(names), len(m.Provides.MCPTools))
	}
	for _, tool := range m.Provides.MCPTools {
		if !names[tool.Name] {
			t.Errorf("missing tool %s", tool.Name)
		}
	}
}
