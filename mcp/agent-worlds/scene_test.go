package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Embed the unrelated platform/control interfaces; any accidental control or
// legacy call fails rather than silently satisfying an observation test.
type observationPlatform struct {
	sdk.PlatformClient
	sdk.TelemetryClient
}

func (observationPlatform) ListProjects() ([]sdk.PlatformProject, error) {
	return []sdk.PlatformProject{{ID: "project-1", Name: "Project One"}}, nil
}

func observationContext(client sdk.TelemetryClient, config sdk.Config) *sdk.AppCtx {
	return sdk.NewAppCtxForTest(nil, nil, config, observationPlatform{TelemetryClient: client}, nil).WithProject("project-1")
}

func TestManifestUsesReadOnlyMetadataPermissions(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "agent-worlds" || len(m.Provides.UIPanels) != 1 {
		t.Fatalf("unexpected manifest: %#v", m)
	}
	if len(m.Provides.UIComponents) != 1 {
		t.Fatalf("ui components=%d, want 1", len(m.Provides.UIComponents))
	}
	widget := m.Provides.UIComponents[0]
	if widget.Name != "world-view" || widget.Entry != "/ui/AgentWorldsWidget.mjs" || widget.SettingsUI == nil {
		t.Fatalf("unexpected world widget: %+v", widget)
	}
	manifest := string(manifestYAML)
	for _, permission := range []string{"platform.instances.read", "platform.telemetry.observe", "platform.runtimes.observe"} {
		if !strings.Contains(manifest, permission) {
			t.Errorf("missing %s", permission)
		}
	}
	for _, obsolete := range []string{"platform.telemetry.read", "platform.runtimes.read", "platform.runtimes.call", "platform.runtimes.manage", "remote_environments_install_id"} {
		if strings.Contains(manifest, obsolete) {
			t.Errorf("unnecessary permission/config %s", obsolete)
		}
	}
}

func TestNormalizeEventUsesSDKMetadataWithoutContent(t *testing.T) {
	for _, field := range []string{"tool_name", "name", "tool"} {
		t.Run(field, func(t *testing.T) {
			event := normalizeEvent(sdk.TelemetryStreamEvent{
				ServerID: "server-1", ID: "event-1", AgentID: 42, ThreadID: "worker", Type: "tool.call", Time: time.Now(),
				Data: json.RawMessage(fmt.Sprintf(`{"%s":"crm_find_contact","message":"private conversation","arguments":{"api_key":"secret"}}`, field)),
			})
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if event.ID != "server-1:event-1" || event.Kind != "tool" || event.Target != "crm_find_contact" {
				t.Fatalf("unexpected event: %#v", event)
			}
			if strings.Contains(string(encoded), "private conversation") || strings.Contains(string(encoded), "secret") {
				t.Fatalf("private event content leaked: %s", encoded)
			}
		})
	}
	failed := normalizeEvent(sdk.TelemetryStreamEvent{Type: "tool.result", Data: json.RawMessage(`{"success":false,"error":"private error"}`)})
	if failed.Kind != "error" || failed.Label != "tool failed" {
		t.Fatalf("failed tool: %#v", failed)
	}
}

func TestLocalAndRemoteScenesShareBoundedSDKQueries(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, runtime := range []bool{false, true} {
			t.Run(fmt.Sprintf("remote=%t/runtime=%t", remote, runtime), func(t *testing.T) {
				queryCalls, inventoryCalls := 0, 0
				project := "project-1"
				token := "local-test-token"
				if remote {
					project, token = "remote-project", "remote-test-token"
				}
				now := time.Now().UTC()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+token {
						t.Errorf("incorrect credential")
					}
					if r.URL.Query().Get("project_id") != project {
						t.Errorf("project filter = %q", r.URL.Query().Get("project_id"))
					}
					if strings.HasSuffix(r.URL.Path, "/topology") {
						_ = json.NewEncoder(w).Encode(sdk.AgentTopology{AttachmentsAvailable: true, ThreadsAvailable: true, Attachments: []sdk.ObservedAttachment{{Name: "crm", InstallID: 17, Tools: []string{"crm_search"}}}})
						return
					}
					switch r.URL.Path {
					case "/api/telemetry/runtimes":
						_ = json.NewEncoder(w).Encode([]sdk.ObservedRuntime{{ID: "runtime-1", ProjectID: project}})
					case "/api/telemetry/agents":
						inventoryCalls++
						agents := []sdk.ObservedAgent{{ID: 900, RuntimeID: "runtime-1", Name: "isolated"}}
						for i := int64(1); i <= 50; i++ {
							agents = append(agents, sdk.ObservedAgent{ID: i, Name: fmt.Sprintf("agent-%02d", i), Status: "running"})
						}
						_ = json.NewEncoder(w).Encode(agents)
					case "/api/telemetry/runtimes/runtime-1":
						inventoryCalls++
						_ = json.NewEncoder(w).Encode(sdk.ObservedRuntime{
							ID: "runtime-1", ProjectID: project, Status: "running",
							Apps:   []sdk.RuntimeApp{{InstallID: 17, Name: "crm", Status: "running"}},
							Agents: []sdk.RuntimeAgent{{ID: 900, Alias: "isolated", Status: "running", Model: "test-model"}},
						})
					case "/api/telemetry":
						queryCalls++
						q := r.URL.Query()
						if q.Get("projection") != "metadata" || q.Get("format") != "page" || q.Get("limit") != "500" || q.Get("agent_id") != "" {
							t.Errorf("history must be one bounded metadata query: %s", r.URL.RawQuery)
						}
						since, err := time.Parse(time.RFC3339Nano, q.Get("since"))
						if err != nil || time.Since(since) < 14*time.Minute || time.Since(since) > 16*time.Minute {
							t.Errorf("invalid history window: %q", q.Get("since"))
						}
						if !strings.Contains(q.Get("events"), "tool.call") || strings.Contains(q.Get("events"), "chunk") {
							t.Errorf("event filter: %q", q.Get("events"))
						}
						runtimeID := ""
						agentID := int64(1)
						if runtime {
							runtimeID, agentID = "runtime-1", 900
						}
						if q.Get("runtime_id") != runtimeID {
							t.Errorf("runtime filter: %q", q.Get("runtime_id"))
						}
						events := []sdk.TelemetryStreamEvent{
							{ServerID: "srv", ID: "new", AgentID: agentID, RuntimeID: runtimeID, Type: "tool.call", Time: now, Data: json.RawMessage(`{"tool_name":"crm_search","message":"private-content","arguments":{"key":"secret-content"}}`)},
							{ServerID: "srv", ID: "old", AgentID: agentID, RuntimeID: runtimeID, Type: "llm.start", Time: now},
							{ServerID: "srv", ID: "other-agent", AgentID: 8888, RuntimeID: runtimeID, Type: "thread.message", Time: now},
							{ServerID: "srv", ID: "other-runtime", AgentID: agentID, RuntimeID: "another-runtime", Type: "tool.call", Time: now},
						}
						_ = json.NewEncoder(w).Encode(sdk.TelemetryPage{ServerID: "srv", Events: events, Order: "newest"})
					default:
						t.Errorf("unexpected legacy/per-agent endpoint: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				client, err := sdk.NewTelemetryClient(server.URL, token)
				if err != nil {
					t.Fatal(err)
				}
				config := sdk.Config{}
				source := "main"
				if remote {
					config = sdk.Config{"remote_url": server.URL, "remote_api_key": token, "remote_project_id": project}
					source = "remote"
				}
				if runtime {
					source = "runtime:runtime-1"
					if remote {
						source = "remote-runtime:runtime-1"
					}
				}
				app := observationContext(client, config)
				worlds := &App{ctx: app}
				// A second refresh must read retained server history too; nothing
				// relies on a collector having run since the app mounted.
				for refresh := 0; refresh < 2; refresh++ {
					scene, err := worlds.scene(context.Background(), app, source)
					if err != nil {
						t.Fatal(err)
					}
					wantAgents := 50
					if runtime {
						wantAgents = 1
					}
					if scene.Source.ID != source || scene.Source.Agents != wantAgents || len(scene.Agents) != wantAgents || len(scene.Events) != 2 {
						t.Fatalf("unexpected scene: %#v", scene)
					}
					if scene.Events[0].ID != "srv:old" || scene.Events[1].Target != "crm_search" {
						t.Fatalf("metadata/order: %#v", scene.Events)
					}
					if runtime && (len(scene.Apps) != 1 || scene.Apps[0].ID != "17" || scene.Agents[0].Model != "test-model") {
						t.Fatalf("runtime inventory: %#v", scene)
					}
					encoded, _ := json.Marshal(scene)
					for _, private := range []string{token, "private-content", "secret-content", "other-agent", "other-runtime"} {
						if strings.Contains(string(encoded), private) {
							t.Errorf("scene includes %s", private)
						}
					}
				}
				if queryCalls != 2 || inventoryCalls != 2 {
					t.Fatalf("requests scale with refreshes, not agents: inventory=%d history=%d", inventoryCalls, queryCalls)
				}
			})
		}
	}
}

func TestSourcesUseGenericRuntimeInventory(t *testing.T) {
	for _, remoteFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("remoteFailure=%t", remoteFailure), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if remoteFailure && r.Header.Get("Authorization") == "Bearer remote-token" {
					http.Error(w, "unavailable", 503)
					return
				}
				switch r.URL.Path {
				case "/api/telemetry/agents":
					_, _ = w.Write([]byte(`[{"id":1,"name":"main"},{"id":2,"name":"isolated","runtime_id":"runtime-1"}]`))
				case "/api/telemetry/runtimes":
					_, _ = w.Write([]byte(`[{"id":"runtime-1","status":"running","agents":[{"id":2,"alias":"isolated"}]}]`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			local, err := sdk.NewTelemetryClient(server.URL, "local-token")
			if err != nil {
				t.Fatal(err)
			}
			app := observationContext(local, sdk.Config{"remote_url": server.URL, "remote_api_key": "remote-token", "remote_label": "Production", "remote_project_id": "project-1"})
			sources, err := (&App{ctx: app}).sources(context.Background(), app)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 4
			if remoteFailure {
				wantCount = 3
			}
			if len(sources) != wantCount || sources[0].Agents != 1 || sources[1].ID != "runtime:runtime-1" || sources[2].Label != "Production" {
				t.Fatalf("sources: %#v", sources)
			}
			if remoteFailure && sources[2].Status != "error" {
				t.Fatalf("remote failure hidden: %#v", sources)
			}
			if !remoteFailure && sources[3].ID != "remote-runtime:runtime-1" {
				t.Fatalf("runtime discovery: %#v", sources)
			}
		})
	}
}

func TestRemoteUsesSDKValidationAndRejectsRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.org/collect", http.StatusFound)
	}))
	defer server.Close()
	for _, origin := range []string{server.URL, "http://example.org", "https://example.org/path"} {
		app := observationContext(nil, sdk.Config{"remote_url": origin, "remote_api_key": "test-token"})
		if _, err := (&App{}).scene(context.Background(), app, "remote"); err == nil {
			t.Fatalf("unsafe/redirected origin accepted: %s", origin)
		}
	}
}

type emptyObserver struct{ sdk.TelemetryClient }

func (emptyObserver) ListObservedAgents(context.Context, string) ([]sdk.ObservedAgent, error) {
	return nil, nil
}

func TestEmptySceneDoesNotQueryHistory(t *testing.T) {
	app := observationContext(emptyObserver{}, nil)
	scene, err := (&App{}).scene(context.Background(), app, "main")
	if err != nil {
		t.Fatal(err)
	}
	if scene.Source.Agents != 0 || scene.Events == nil || scene.Agents == nil {
		t.Fatalf("empty scene must have arrays: %#v", scene)
	}
}

func TestSceneCancellationReachesSDK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached server") }))
	defer server.Close()
	client, err := sdk.NewTelemetryClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&App{}).scene(ctx, observationContext(client, nil), "main"); err == nil {
		t.Fatal("cancelled scene succeeded")
	}
}

func TestFinishSceneKeepsLatestChronologicalWindow(t *testing.T) {
	scene := &Scene{}
	now := time.Now()
	for i := 0; i < 500; i++ {
		scene.Events = append(scene.Events, SceneEvent{ID: fmt.Sprint(i), Time: now.Add(-time.Duration(i) * time.Second)})
	}
	finishScene(scene)
	if len(scene.Events) != sceneEventLimit || scene.Events[0].ID != "239" || scene.Events[sceneEventLimit-1].ID != "0" {
		t.Fatalf("wrong replay window: %#v", scene.Events)
	}
}

type projectObserver struct {
	observationPlatform
	projects []sdk.PlatformProject
}

func (p projectObserver) ListProjects() ([]sdk.PlatformProject, error) { return p.projects, nil }

func TestGlobalSceneFansOutAuthorizedProjectsAndCachesTopologyPerViewer(t *testing.T) {
	var mu sync.Mutex
	queries, topologies := map[string]int{}, map[string]int{}
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project_id")
		if project != "one" && project != "two" && project != "empty" {
			t.Errorf("unscoped project request: %s", r.URL)
			http.Error(w, "scope", 403)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/topology"):
			mu.Lock()
			topologies[project]++
			mu.Unlock()
			json.NewEncoder(w).Encode(sdk.AgentTopology{Threads: []sdk.ObservedThread{{ID: "main"}}, Attachments: []sdk.ObservedAttachment{{Name: "CRM", InstallID: 17, Tools: []string{"crm_search"}}}})
		case r.URL.Path == "/api/telemetry/agents":
			if project == "empty" {
				json.NewEncoder(w).Encode([]sdk.ObservedAgent{})
				return
			}
			id := int64(1)
			if project == "two" {
				id = 2
			}
			json.NewEncoder(w).Encode([]sdk.ObservedAgent{{ID: id, ProjectID: project, Name: project, Status: "running"}})
		case r.URL.Path == "/api/telemetry":
			mu.Lock()
			queries[project]++
			mu.Unlock()
			id := int64(1)
			if project == "two" {
				id = 2
			}
			json.NewEncoder(w).Encode(sdk.TelemetryPage{Events: []sdk.TelemetryStreamEvent{{ID: project, AgentID: id, ProjectID: project, Type: "tool.call", Time: now, Data: json.RawMessage(`{"tool_name":"crm_search","target_agent_id":2,"message":"private"}`)}}})
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := sdk.NewTelemetryClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	platform := projectObserver{observationPlatform: observationPlatform{TelemetryClient: client}, projects: []sdk.PlatformProject{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}, {ID: "empty", Name: "Empty"}}}
	app := sdk.NewAppCtxForTest(nil, nil, nil, platform, nil)
	worlds := &App{ctx: app}
	for i := 0; i < 2; i++ {
		scene, err := worlds.scene(context.WithValue(context.Background(), viewerKey{}, "alice"), app, "main")
		if err != nil {
			t.Fatal(err)
		}
		if scene.Scope != "instance" || scene.Source.ProjectID != "" || len(scene.Projects) != 3 || len(scene.Agents) != 2 || len(scene.Events) != 2 || len(scene.Destinations) != 2 {
			t.Fatalf("global scene: %#v", scene)
		}
		if scene.Destinations[0].ID == scene.Destinations[1].ID {
			t.Fatal("same app across projects lost its identity")
		}
		for _, event := range scene.Events {
			if event.TargetID == "" || event.TargetAgentID != "2" {
				t.Fatalf("event metadata missing: %#v", event)
			}
		}
	}
	if queries["one"] != 2 || queries["two"] != 2 || queries["empty"] != 0 || topologies["one"] != 1 || topologies["two"] != 1 {
		t.Fatalf("request budget queries=%v topology=%v", queries, topologies)
	}
	if _, err := worlds.scene(context.WithValue(context.Background(), viewerKey{}, "bob"), app, "main"); err != nil {
		t.Fatal(err)
	}
	if topologies["one"] != 2 || topologies["two"] != 2 {
		t.Fatal("viewer reused another user's cached topology")
	}
}

func TestGlobalSceneReportsPartialProjectFailure(t *testing.T) {
	platform := projectObserver{observationPlatform: observationPlatform{TelemetryClient: partialObserver{}}, projects: []sdk.PlatformProject{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}}
	app := sdk.NewAppCtxForTest(nil, nil, nil, platform, nil)
	scene, err := (&App{ctx: app}).scene(context.Background(), app, "main")
	if err != nil || scene.Source.Status != "partial" || len(scene.Projects) != 1 || len(scene.Warnings) != 1 {
		t.Fatalf("partial scene=%#v err=%v", scene, err)
	}
}

type partialObserver struct{ sdk.TelemetryClient }

func (partialObserver) ListObservedAgents(_ context.Context, project string) ([]sdk.ObservedAgent, error) {
	if project == "two" {
		return nil, fmt.Errorf("denied")
	}
	return []sdk.ObservedAgent{}, nil
}
