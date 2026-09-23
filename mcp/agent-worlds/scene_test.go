package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestManifest(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "agent-worlds" || len(m.Provides.UIPanels) != 1 {
		t.Fatalf("unexpected manifest: name=%q panels=%d", m.Name, len(m.Provides.UIPanels))
	}
}

func TestNormalizeEventDoesNotExposeContent(t *testing.T) {
	raw := json.RawMessage(`{"name":"crm_find_contact","message":"private conversation","arguments":{"api_key":"secret"}}`)
	event := normalizeEvent("evt-1", 42, "main", "tool.call", time.Now(), raw)
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != "tool" || event.Target != "crm_find_contact" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if strings.Contains(string(encoded), "private conversation") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("private event content leaked: %s", encoded)
	}
}

func TestRemoteQueryKeepsAuthAndQueryOnBackend(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.String(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	a := &App{ctx: sdk.NewAppCtxForTest(nil, nil, sdk.Config{"remote_url": server.URL, "remote_api_key": "test-key"}, nil, nil)}
	var events []remoteEvent
	if err := a.remoteGET("/api/telemetry?agent_id=4&limit=80", &events); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/telemetry?agent_id=4&limit=80" || gotAuth != "Bearer test-key" {
		t.Fatalf("request path=%q auth=%q", gotPath, gotAuth)
	}
}

func TestRemoteRejectsRedirectAndUnsafeOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.org/collect", http.StatusFound)
	}))
	defer server.Close()
	a := &App{ctx: sdk.NewAppCtxForTest(nil, nil, sdk.Config{"remote_url": server.URL, "remote_api_key": "test-key"}, nil, nil)}
	var agents []remoteAgent
	if err := a.remoteGET("/api/agents", &agents); err == nil {
		t.Fatal("redirect must not be followed")
	}
	a.ctx = sdk.NewAppCtxForTest(nil, nil, sdk.Config{"remote_url": "http://example.org", "remote_api_key": "test-key"}, nil, nil)
	if _, err := a.remoteBase(); err == nil {
		t.Fatal("non-loopback HTTP must be rejected")
	}
}

func TestRemoteEnvironmentScene(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project_id") != "remote-project" || r.URL.Query().Get("install_id") != "17" {
			http.Error(w, "missing remote scope", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/apps/environments/api/environments":
			_, _ = w.Write([]byte(`[{"name":"Sandbox","active_run":{"id":"run_1","status":"running"},"runtime":{"status":"running","apps":[{"name":"crm","status":"running"}],"agents":[{"id":42,"alias":"research","status":"running"}]}}]`))
		case "/api/apps/environments/api/runs/run_1/inspect":
			if r.URL.Query().Get("agent") != "research" {
				t.Errorf("agent query = %q", r.URL.Query().Get("agent"))
			}
			_, _ = w.Write([]byte(`{"telemetry":[{"id":"evt-1","instance_id":42,"thread_id":"main","type":"tool.call","time":"2026-09-23T00:00:00Z","data":{"name":"crm_search","message":"private"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := &App{ctx: sdk.NewAppCtxForTest(nil, nil, sdk.Config{"remote_url": server.URL, "remote_api_key": "test-key", "remote_project_id": "remote-project", "remote_environments_install_id": "17"}, nil, nil)}
	scene, err := a.remoteRunScene("run_1")
	if err != nil {
		t.Fatal(err)
	}
	if scene.Source.Kind != "remote-runtime" || len(scene.Agents) != 1 || len(scene.Apps) != 1 || len(scene.Events) != 1 {
		t.Fatalf("unexpected scene: %#v", scene)
	}
	encoded, _ := json.Marshal(scene)
	if strings.Contains(string(encoded), "private") {
		t.Fatalf("private telemetry leaked: %s", encoded)
	}
}
