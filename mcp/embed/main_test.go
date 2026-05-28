package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type platformStub struct {
	tk.BasePlatformClient
	file      StorageFile
	signedURL string
	calls     []string
	bareFile  bool
}

func (p *platformStub) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{PublicURL: "https://agents.example.com"}, nil
}

func (p *platformStub) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.calls = append(p.calls, app+":"+tool)
	var payload any
	switch app + ":" + tool {
	case "storage:files_get":
		if p.bareFile {
			payload = p.file
		} else {
			payload = map[string]any{"found": true, "file": p.file}
		}
	case "storage:files_get_url":
		payload = map[string]any{"url": p.signedURL}
	default:
		return tk.ErrNotImplemented
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

func newEmbedCtx(t *testing.T, pf *platformStub) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(pf),
		tk.WithConfig(map[string]string{
			"default_width":          "800",
			"default_height":         "450",
			"signed_url_ttl_seconds": "600",
		}),
	)
	globalCtx = ctx
	return ctx
}

func TestEmbeddedManifestAndToolsMatch(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "embed" {
		t.Fatalf("name=%q", m.Name)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Fatal("db.migrations missing")
	}
	if len(m.Provides.MCPTools) != len(app.MCPTools()) {
		t.Fatalf("manifest tools=%d handlers=%d", len(m.Provides.MCPTools), len(app.MCPTools()))
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	for _, tool := range app.MCPTools() {
		if !declared[tool.Name] {
			t.Errorf("handler %q missing from manifest", tool.Name)
		}
	}
}

func TestCreateReturnsShareableViewerAndOEmbed(t *testing.T) {
	pf := &platformStub{
		file: StorageFile{
			ID:          77,
			ProjectID:   "media-proj",
			Name:        "launch.mp4",
			ContentType: "video/mp4",
			SizeBytes:   12345,
		},
		signedURL: "/api/apps/storage/files/77/content/launch.mp4?sig=abc&exp=999&project_id=media-proj",
	}
	ctx := newEmbedCtx(t, pf)
	out, err := (&App{}).toolCreate(ctx, map[string]any{
		"storage_file_id":    77,
		"storage_project_id": "media-proj",
		"title":              "Launch video",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	viewer := res["viewer_url"].(string)
	if !strings.Contains(viewer, "https://agents.example.com/api/apps/embed/embed/") {
		t.Fatalf("viewer_url unexpected: %q", viewer)
	}
	if !strings.Contains(res["oembed_url"].(string), "/api/apps/embed/oembed?url=") {
		t.Fatalf("oembed_url unexpected: %q", res["oembed_url"])
	}
	if !strings.Contains(res["html"].(string), "<iframe") {
		t.Fatalf("html missing iframe: %s", res["html"])
	}
	if strings.Join(pf.calls, ",") != "storage:files_get" {
		t.Fatalf("calls=%v", pf.calls)
	}
}

func TestCreateAcceptsBareStorageGetResponse(t *testing.T) {
	pf := &platformStub{
		file: StorageFile{
			ID:          42,
			ProjectID:   "media-proj",
			Name:        "content.MOV",
			ContentType: "video/quicktime",
			SizeBytes:   959100000,
		},
		bareFile: true,
	}
	ctx := newEmbedCtx(t, pf)
	out, err := (&App{}).toolCreate(ctx, map[string]any{
		"storage_file_id": 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	embed := res["embed"].(*Embed)
	if embed.StorageFileID != 42 || embed.Name != "content.MOV" {
		t.Fatalf("unexpected embed: %+v", embed)
	}
}

func TestViewerRendersVideoPlayerWithSignedStorageURL(t *testing.T) {
	pf := &platformStub{
		file:      StorageFile{ID: 77, ProjectID: "media-proj", Name: "launch.mp4", ContentType: "video/mp4"},
		signedURL: "/api/apps/storage/files/77/content/launch.mp4?sig=abc&exp=999&project_id=media-proj",
	}
	ctx := newEmbedCtx(t, pf)
	out, err := (&App{}).toolCreate(ctx, map[string]any{"storage_file_id": 77})
	if err != nil {
		t.Fatal(err)
	}
	token := out.(map[string]any)["embed"].(*Embed).Token

	req := httptest.NewRequest(http.MethodGet, "/embed/"+token, nil)
	rec := httptest.NewRecorder()
	(&App{}).handleViewer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<video") || !strings.Contains(body, "controls") {
		t.Fatalf("viewer did not render a video player: %s", body)
	}
	if !strings.Contains(body, "https://agents.example.com/api/apps/storage/files/77/content/launch.mp4?sig=abc") {
		t.Fatalf("viewer did not include signed storage URL: %s", body)
	}
}

func TestOEmbedEndpointReturnsVideoIframe(t *testing.T) {
	pf := &platformStub{
		file:      StorageFile{ID: 77, ProjectID: "media-proj", Name: "launch.mp4", ContentType: "video/mp4"},
		signedURL: "https://agents.example.com/api/apps/storage/files/77/content/launch.mp4?sig=abc",
	}
	ctx := newEmbedCtx(t, pf)
	out, err := (&App{}).toolCreate(ctx, map[string]any{"storage_file_id": 77, "title": "Launch video"})
	if err != nil {
		t.Fatal(err)
	}
	viewer := out.(map[string]any)["viewer_url"].(string)

	req := httptest.NewRequest(http.MethodGet, "/oembed?url="+viewer, nil)
	rec := httptest.NewRecorder()
	(&App{}).handleOEmbed(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "video" || got["version"] != "1.0" {
		t.Fatalf("unexpected oembed payload: %+v", got)
	}
	if !strings.Contains(got["html"].(string), "<iframe") {
		t.Fatalf("oembed html missing iframe: %+v", got)
	}
}
