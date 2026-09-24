package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type catalogPlatform struct {
	tk.BasePlatformClient
	starts int
	files  []map[string]any
}

func (*catalogPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"storage": float64(11), "gigs": float64(12), "media": float64(13), "video_host": float64(21)}}, nil
}
func (*catalogPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: map[int64]string{11: "storage", 12: "gigs", 13: "media"}[id]}, nil
}
func (*catalogPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "bunny-stream"}, nil
}
func (p *catalogPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	var data any
	switch app + "/" + tool {
	case "storage/files_get":
		id := number(input, "id")
		contentType := "video/mp4"
		if id == 2 {
			contentType = "image/jpeg"
		}
		data = map[string]any{"found": true, "file": map[string]any{"id": id, "name": fmt.Sprintf("clip-%d.mp4", id), "sha256": fmt.Sprintf("sha-%d", id), "size_bytes": 1000, "content_type": contentType, "project_id": "project-a"}}
	case "storage/files_get_url":
		data = map[string]any{"url": "https://storage.example/signed"}
	case "storage/files_list":
		data = map[string]any{"files": p.files}
	case "gigs/gigs_status":
		data = map[string]any{"gig": map[string]any{"id": input["id"]}}
	case "media/media_get":
		data = map[string]any{"found": true, "media": map[string]any{"file_id": input["file_id"], "probe_status": "ok", "audience_rating": "general", "derivations": []map[string]any{{"kind": "thumbnail", "status": "ok", "storage_file_id": "901"}}}}
	default:
		return fmt.Errorf("unexpected app call %s/%s", app, tool)
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}
func (p *catalogPlatform) ExecuteIntegrationTool(id int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	if id != 21 {
		return nil, fmt.Errorf("unexpected connection %d", id)
	}
	switch tool {
	case "fetch_video":
		p.starts++
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"id":"video-1"}`)}, nil
	case "get_video":
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"guid":"video-1","videoLibraryId":42,"status":4}`)}, nil
	}
	return nil, fmt.Errorf("unexpected tool %s", tool)
}

func setupCatalog(t *testing.T) (*App, *sdk.AppCtx, *catalogPlatform, string, string) {
	t.Helper()
	p := &catalogPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	a := &App{}
	created, err := a.brandCreate(ctx, map[string]any{"slug": "brand", "name": "Brand", "storage_root": "/brand/", "host_provider": "bunny", "host_connection_id": int64(21), "host_library_id": "42"})
	if err != nil {
		t.Fatal(err)
	}
	brand := created.(map[string]any)["brand"].(map[string]any)["id"].(string)
	result, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Shoot", "session_date": "2026-09-24"})
	if err != nil {
		t.Fatal(err)
	}
	session := result.(map[string]any)["session"].(Session).ID
	return a, ctx, p, brand, session
}

func attach(t *testing.T, a *App, ctx *sdk.AppCtx, session string, file int64) string {
	t.Helper()
	r, err := a.assetAttach(ctx, map[string]any{"session_id": session, "storage_file_id": file})
	if err != nil {
		t.Fatal(err)
	}
	return r.(map[string]any)["asset"].(*Asset).ID
}

func TestManifestToolsAndMigrations(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"))
	m := (&App{}).Manifest()
	declared := map[string]bool{}
	for _, spec := range m.Provides.MCPTools {
		declared[spec.Name] = true
	}
	for _, tool := range (&App{}).MCPTools() {
		if !declared[tool.Name] {
			t.Errorf("tool %s missing in manifest", tool.Name)
		}
		delete(declared, tool.Name)
	}
	if len(declared) != 0 {
		t.Fatalf("manifest tools lack handlers: %v", declared)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO brands(id,project_id,slug,name,storage_root) VALUES('test','p','test','Test','/')`); err != nil {
		t.Fatalf("migration not applied: %v", err)
	}
}

func TestProjectIsolationAndLineage(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	first, second, third := attach(t, a, ctx, session, 1), attach(t, a, ctx, session, 2), attach(t, a, ctx, session, 3)
	for _, source := range []string{first, second} {
		if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": third, "source_asset_id": source}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": first, "source_asset_id": third}); err == nil {
		t.Fatal("lineage cycle accepted")
	}
	other := ctx.WithProject("project-b")
	if _, err := a.assetGet(other, map[string]any{"id": first}); err == nil {
		t.Fatal("asset leaked across projects")
	}
	if _, err := a.sessionGet(other, map[string]any{"id": session}); err == nil {
		t.Fatal("session leaked across projects")
	}
}

func TestMediaCompletionUpdatesOnlyLinkedProjectAssets(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	event := sdk.Event{ProjectID: "project-a", SourceInstallID: 13, Data: map[string]any{"file_id": "1", "audience_rating": "mature"}}
	if err := a.onMediaCompleted(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := a.onMediaCompleted(ctx, event); err != nil {
		t.Fatal(err)
	}
	got, err := assetByID(ctx.AppDB(), "project-a", asset)
	if err != nil || got.MediaStatus != "completed" || got.MediaRating != "mature" {
		t.Fatalf("media state=%+v err=%v", got, err)
	}
	event.SourceInstallID = 99
	event.Data["audience_rating"] = "adult"
	if err := a.onMediaCompleted(ctx, event); err != nil {
		t.Fatal(err)
	}
	got, _ = assetByID(ctx.AppDB(), "project-a", asset)
	if got.MediaRating != "mature" {
		t.Fatal("unbound Media event changed asset")
	}
}

func TestSessionAndAssetPreviewResolveToStorage(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	videoID := attach(t, a, ctx, session, 1)
	attach(t, a, ctx, session, 2)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })

	request := httptest.NewRequest(http.MethodGet, "/sessions/"+session+"/preview?project_id=project-a", nil)
	response := httptest.NewRecorder()
	a.handleSession(response, request)
	if response.Code != http.StatusFound || !strings.Contains(response.Header().Get("Location"), "/files/2/content?") {
		t.Fatalf("session preview redirect: status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	request = httptest.NewRequest(http.MethodGet, "/assets/"+videoID+"/preview?project_id=project-a", nil)
	response = httptest.NewRecorder()
	a.handleAsset(response, request)
	if response.Code != http.StatusFound || !strings.Contains(response.Header().Get("Location"), "/files/901/content?") {
		t.Fatalf("asset preview redirect: status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	// Global installs take the project from the request; project-scoped
	// installs correctly ignore a conflicting query parameter.
	t.Setenv("APTEVA_PROJECT_ID", "")
	request = httptest.NewRequest(http.MethodGet, "/sessions/"+session+"/preview?project_id=project-b", nil)
	response = httptest.NewRecorder()
	a.handleSession(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-project preview status=%d", response.Code)
	}
}

func TestHostingIsIdempotentAndProviderNeutralRecord(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	if _, err := a.assetReview(ctx, map[string]any{"asset_id": asset, "review_status": "approved"}); err != nil {
		t.Fatal(err)
	}
	first, err := a.hostingRequest(ctx, map[string]any{"asset_id": asset})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.hostingRequest(ctx, map[string]any{"asset_id": asset})
	if err != nil {
		t.Fatal(err)
	}
	if p.starts != 1 || second.(map[string]any)["was_existing"] != true {
		t.Fatalf("duplicate upload: starts=%d, second=%v", p.starts, second)
	}
	h := first.(map[string]any)["hosting"].(*Hosting)
	if h.Provider != "bunny" || h.Status != "processing" {
		t.Fatalf("unexpected hosting: %+v", h)
	}
	checked, err := a.hostingCheck(ctx, map[string]any{"id": h.ID})
	if err != nil {
		t.Fatal(err)
	}
	ready := checked.(map[string]any)["hosting"].(*Hosting)
	if ready.Status != "ready" || !strings.Contains(ready.EmbedURL, "/42/video-1") {
		t.Fatalf("unexpected readiness: %+v", ready)
	}
	otherResult, err := a.sessionCreate(ctx, map[string]any{"brand_id": sessionBrand(t, ctx, session), "title": "Second shoot", "session_date": "2026-09-25"})
	if err != nil {
		t.Fatal(err)
	}
	otherSession := otherResult.(map[string]any)["session"].(Session).ID
	otherAsset := attach(t, a, ctx, otherSession, 1)
	if _, err := a.assetReview(ctx, map[string]any{"asset_id": otherAsset, "review_status": "approved"}); err != nil {
		t.Fatal(err)
	}
	shared, err := a.hostingRequest(ctx, map[string]any{"asset_id": otherAsset})
	if err != nil || p.starts != 1 || shared.(map[string]any)["reused_remote"] != true {
		t.Fatalf("checksum reuse failed: starts=%d, shared=%v, err=%v", p.starts, shared, err)
	}
}

func sessionBrand(t *testing.T, ctx *sdk.AppCtx, sessionID string) string {
	t.Helper()
	s, err := sessionByID(ctx.AppDB(), ctx.CurrentProject(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return s.BrandID
}

func TestReleaseEvidenceAndImportPreview(t *testing.T) {
	a, ctx, p, brand, session := setupCatalog(t)
	asset := attach(t, a, ctx, session, 1)
	p.files = []map[string]any{{"id": 1, "name": "clip-1.mp4", "folder": "/brand/"}, {"id": 2, "name": "clip-2.mp4", "folder": "/brand/legacy/"}}
	preview, err := a.importPreview(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	items := preview.(map[string]any)["candidates"].([]map[string]any)
	if len(items) != 2 || !items[1]["needs_review"].(bool) {
		t.Fatalf("unexpected preview: %+v", items)
	}
	var count int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&count)
	if count != 1 {
		t.Fatal("preview changed asset records")
	}
	release, err := a.releaseCreate(ctx, map[string]any{"brand_id": brand, "title": "Launch"})
	if err != nil {
		t.Fatal(err)
	}
	rid := release.(map[string]any)["release"].(*Release).ID
	otherBrand, err := a.brandCreate(ctx, map[string]any{"slug": "other", "name": "Other", "storage_root": "/other/"})
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := a.sessionCreate(ctx, map[string]any{"brand_id": otherBrand.(map[string]any)["brand"].(map[string]any)["id"], "title": "Other shoot", "session_date": "2026-09-24"})
	if err != nil {
		t.Fatal(err)
	}
	foreignAsset := attach(t, a, ctx, otherSession.(map[string]any)["session"].(Session).ID, 3)
	if _, err := a.releaseTargetAdd(ctx, map[string]any{"release_id": rid, "destination": "instagram", "asset_ids": []string{foreignAsset}}); err == nil {
		t.Fatal("release accepted asset from another brand")
	}
	target, err := a.releaseTargetAdd(ctx, map[string]any{"release_id": rid, "destination": "patreon", "asset_ids": []string{asset}})
	if err != nil {
		t.Fatal(err)
	}
	tid := target.(map[string]any)["target"].(ReleaseTarget).ID
	if _, err := a.publicationRecord(ctx, map[string]any{"target_id": tid, "status": "verified_published", "evidence_source": "creator_page_manual", "external_url": "https://patreon.example/post"}); err == nil {
		t.Fatal("publication without actual time accepted")
	}
	when := time.Now().UTC().Format(time.RFC3339)
	if _, err := a.publicationRecord(ctx, map[string]any{"target_id": tid, "status": "verified_published", "evidence_source": "creator_page_manual", "external_url": "https://patreon.example/post", "actual_at": when}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := ctx.AppDB().QueryRow(`SELECT current_status FROM release_targets WHERE id=?`, tid).Scan(&state); err != nil || state != "verified_published" {
		t.Fatalf("target status=%q err=%v", state, err)
	}
}
