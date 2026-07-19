package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

type dlnaPlatformStub struct {
	tk.BasePlatformClient
	mu       sync.Mutex
	files    map[int64]storageFile
	calls    []string
	lastArgs map[string]any
}

func (p *dlnaPlatformStub) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, app+":"+tool)
	p.lastArgs = make(map[string]any, len(args))
	for key, value := range args {
		p.lastArgs[key] = value
	}
	p.mu.Unlock()

	var payload any
	switch app + ":" + tool {
	case "storage:files_get":
		payload = p.files[toInt64(args["id"])]
	case "storage:files_get_url":
		payload = map[string]any{"url": "https://storage.example/media.mp4?sig=test&exp=999"}
	case "storage:files_list_folders":
		payload = map[string]any{"folders": []string{}}
	case "storage:files_list":
		payload = map[string]any{"files": []storageFile{}}
	case "media:media_get":
		payload = map[string]any{"found": false}
	case "media:media_index_status":
		payload = map[string]any{"status": "ok"}
	default:
		return tk.ErrNotImplemented
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

func newDLNATestApp(t *testing.T, platform *dlnaPlatformStub) *App {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("project-a"),
		tk.WithPlatform(platform),
		tk.WithEnv("APTEVA_GATEWAY_URL", ""),
		tk.WithConfig(map[string]string{"lan_ip": "192.168.1.10", "media_metadata": "false"}),
	)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestManifestAndToolSurfaceMatch(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "dlna" || manifest.Version != "0.2.0" {
		t.Fatalf("unexpected manifest identity: %s %s", manifest.Name, manifest.Version)
	}
	if len(manifest.Scopes) != 1 || string(manifest.Scopes[0]) != "project" {
		t.Fatalf("DLNA must have one unambiguous project scope: %#v", manifest.Scopes)
	}
	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	for _, tool := range app.MCPTools() {
		if !declared[tool.Name] {
			t.Errorf("implemented tool %q is absent from manifest", tool.Name)
		}
		delete(declared, tool.Name)
	}
	for name := range declared {
		t.Errorf("manifest tool %q has no implementation", name)
	}
	open := map[string]bool{}
	for _, route := range app.HTTPRoutes() {
		if route.NoAuth {
			open[route.Pattern] = true
		}
	}
	for _, path := range []string{"/device.xml", "/ContentDirectory/control", "/ConnectionManager/control", "/media", "/media/"} {
		if !open[path] {
			t.Errorf("TV-facing route %s must stay open on the LAN", path)
		}
	}
	for _, route := range app.HTTPRoutes() {
		if route.Pattern == "/settings" || route.Pattern == "/published_folders" || route.Pattern == "/status" {
			if route.NoAuth {
				t.Errorf("management route %s must remain authenticated", route.Pattern)
			}
		}
	}
}

func TestOnMountRequiresProjectAndPersistsUUID(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	globalCtx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(&dlnaPlatformStub{}))
	if err := (&App{}).OnMount(globalCtx); err == nil {
		t.Fatal("global mount should be rejected because TV requests have no project context")
	}

	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("project-a"),
		tk.WithPlatform(&dlnaPlatformStub{}),
		tk.WithConfig(map[string]string{"lan_ip": "192.168.1.10"}),
	)
	first := &App{}
	if err := first.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	second := &App{}
	if err := second.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if first.deviceID == "" || first.deviceID != second.deviceID {
		t.Fatalf("device UUID was not stable: %q vs %q", first.deviceID, second.deviceID)
	}
}

func TestPublishedPathsRejectTraversal(t *testing.T) {
	for _, input := range []string{"/movies/../private", "/movies/./kids", "../private", "/x\x00y"} {
		if _, err := normalisePublishedPath(input); err == nil {
			t.Errorf("normalisePublishedPath(%q) accepted traversal/invalid input", input)
		}
	}
	if got, err := secureJoinPublished("/movies", "kids/animation"); err != nil || got != "/movies/kids/animation" {
		t.Fatalf("safe join = %q, %v", got, err)
	}
	if _, err := secureJoinPublished("/movies", "../private"); err == nil {
		t.Fatal("secure join accepted escape")
	}
	forged := encodeFolderID(7, "../private")
	if _, _, err := decodeFolderID(forged); err == nil {
		t.Fatal("forged folder ID accepted escape")
	}
}

func TestOpenMediaEndpointOnlyServesPublishedFiles(t *testing.T) {
	platform := &dlnaPlatformStub{files: map[int64]storageFile{
		1: {ID: 1, Name: "secret.mp4", Folder: "/private", ContentType: "video/mp4"},
		2: {ID: 2, Name: "movie.mp4", Folder: "/movies", ContentType: "video/mp4"},
	}}
	app := newDLNATestApp(t, platform)
	if _, err := addPublishedFolder(app.ctx.AppDB(), app.projectID, "/movies", "Movies"); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	app.handleMediaRedirect(recorder, httptest.NewRequest(http.MethodGet, "/media?id=1", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unpublished file status=%d, want 404", recorder.Code)
	}
	platform.mu.Lock()
	callsAfterSecret := strings.Join(platform.calls, ",")
	platform.mu.Unlock()
	if strings.Contains(callsAfterSecret, "files_get_url") {
		t.Fatal("unpublished file minted a signed URL")
	}

	recorder = httptest.NewRecorder()
	app.handleMediaRedirect(recorder, httptest.NewRequest(http.MethodGet, "/media?id=2", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("published file status=%d, want 302 fallback", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "sig=test") {
		t.Fatalf("missing signed redirect: %q", location)
	}
	platform.mu.Lock()
	project := platform.lastArgs["_project_id"]
	platform.mu.Unlock()
	if project != "project-a" {
		t.Fatalf("dependency call project=%v, want project-a", project)
	}
}

func TestSettingsOverrideManifestConfigAndUpdateID(t *testing.T) {
	app := newDLNATestApp(t, &dlnaPlatformStub{})
	if err := app.setSetting("media_metadata", "true"); err != nil {
		t.Fatal(err)
	}
	if !app.configFlag("media_metadata", false) {
		t.Fatal("persisted setting did not override install config")
	}
	before := app.updateID.Load()
	got := app.bumpUpdateID()
	if got != before+1 {
		t.Fatalf("update ID=%d, want %d", got, before+1)
	}
	if value, ok := app.getSetting("system_update_id"); !ok || value != "2" {
		t.Fatalf("persisted update ID=%q, %v", value, ok)
	}
}
