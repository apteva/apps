package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func hardeningTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		script, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestThemeAssetRequiresSlugAndExactVersion(t *testing.T) {
	if err := initializeThemes(); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	good := httptest.NewRecorder()
	a.handleThemeAsset(good, httptest.NewRequest(http.MethodGet, "/_theme/default/2/style.css", nil))
	if good.Code != http.StatusOK || !strings.Contains(good.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("valid asset status/type = %d/%q", good.Code, good.Header().Get("Content-Type"))
	}
	bad := httptest.NewRecorder()
	a.handleThemeAsset(bad, httptest.NewRequest(http.MethodGet, "/_theme/default/999/style.css", nil))
	if bad.Code != http.StatusNotFound {
		t.Fatalf("mismatched version status = %d, want 404", bad.Code)
	}
}

func TestActiveThemeIsResolvedPerSite(t *testing.T) {
	db := hardeningTestDB(t)
	if err := initializeThemes(); err != nil {
		t.Fatal(err)
	}
	s1, err := dbCreateSite(db, "p1", "one", "One", "")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := dbCreateSite(db, "p1", "two", "Two", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSetSetting(db, "p1", s2.ID, "active_theme", "magazine"); err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(nil, db, nil, nil, nil)
	if got := activeThemeForSite(ctx, "p1", s1.ID).Name; got != "default" {
		t.Fatalf("site one theme = %q", got)
	}
	if got := activeThemeForSite(ctx, "p1", s2.ID).Name; got != "magazine" {
		t.Fatalf("site two theme = %q", got)
	}
}

func TestSiteCreationInitializesTitleFromName(t *testing.T) {
	db := hardeningTestDB(t)
	site, err := dbCreateSite(db, "site-title-project", "docs", "Product Documentation", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := dbGetSetting(db, "site-title-project", site.ID, "site_title"); err != nil || got != "Product Documentation" {
		t.Fatalf("site title = %q, err=%v", got, err)
	}
	fallback, err := dbCreateSite(db, "site-title-project", "support-center", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := dbGetSetting(db, "site-title-project", fallback.ID, "site_title"); err != nil || got != "support-center" {
		t.Fatalf("fallback site title = %q, err=%v", got, err)
	}
}

func TestMarkdownBlockRendersBlockLevelMarkup(t *testing.T) {
	if err := initializeThemes(); err != nil {
		t.Fatal(err)
	}
	source := "# Heading\n\nFirst paragraph.\n\nSecond paragraph.\n\n- Alpha\n- Beta"
	body := string(renderBlockWithTheme(getTheme("default"), Block{
		Type: "core/markdown", Attrs: map[string]any{"source": source},
	}))
	for _, expected := range []string{
		"<h1>Heading</h1>", "<p>First paragraph.</p>", "<p>Second paragraph.</p>",
		"<ul>", "<li>Alpha</li>", "<li>Beta</li>",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("markdown output missing %q: %s", expected, body)
		}
	}
	if unsafe := string(renderMarkdown("<script>alert(1)</script>")); strings.Contains(unsafe, "<script") {
		t.Fatalf("markdown renderer retained unsafe script: %s", unsafe)
	}
}

func TestRenderedThemeAssetsKeepProxyProjectAndSite(t *testing.T) {
	if err := initializeThemes(); err != nil {
		t.Fatal(err)
	}
	body, err := renderSingle(PageData{
		Theme:         getTheme("default"),
		URLPrefix:     "/api/apps/content/",
		ResourceQuery: "?project_id=p1&site=one",
		Post:          &Post{Kind: "page", BodyBlocks: Document{Version: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `_theme/default/2/style.css?project_id=p1&amp;site=one`) {
		t.Fatalf("rendered stylesheet lost resource query: %s", body)
	}
}

func TestCrossSiteIDsAreRejected(t *testing.T) {
	db := hardeningTestDB(t)
	s1, _ := dbCreateSite(db, "p1", "one", "One", "")
	s2, _ := dbCreateSite(db, "p1", "two", "Two", "")
	p1, err := dbCreatePost(db, "p1", s1.ID, PostCreate{Kind: "post", Title: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	term2, err := dbCreateTerm(db, "p1", s2.ID, "tag", "Other", "other", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbAssignTerms(db, "p1", s1.ID, p1.ID, []int64{term2.ID}); err == nil {
		t.Fatal("cross-site term assignment succeeded")
	}
	menu2, err := dbCreateMenu(db, "p1", s2.ID, "primary", "Primary")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSetMenuItems(db, "p1", s1.ID, menu2.ID, nil); err == nil {
		t.Fatal("cross-site menu mutation succeeded")
	}
}

func TestHydratePostMediaPathsIsTenantScoped(t *testing.T) {
	db := hardeningTestDB(t)
	s1, _ := dbCreateSite(db, "p1", "one", "One", "")
	s2, _ := dbCreateSite(db, "p1", "two", "Two", "")
	media2, err := dbCreateMedia(db, Media{ProjectID: "p1", SiteID: s2.ID, StoragePath: "/.media/secret.png"})
	if err != nil {
		t.Fatal(err)
	}
	post := &Post{BodyBlocks: Document{Version: 1, Blocks: []Block{{Type: "core/image", Attrs: map[string]any{"media_id": media2.ID}}}}}
	hydrated := hydratePostMediaPaths(db, "p1", s1.ID, post, "?project_id=p1&site=one")
	if got := hydrated.BodyBlocks.Blocks[0].Attrs["media_id"]; got != "" {
		t.Fatalf("cross-site media resolved to %v", got)
	}
	if _, ok := post.BodyBlocks.Blocks[0].Attrs["media_id"].(int64); !ok {
		t.Fatal("hydration mutated persisted post")
	}
}

func TestPublicProjectResolvesBoundHostname(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	db := hardeningTestDB(t)
	if _, err := dbCreateSite(db, "project-host", "main", "Main", "site.example.com"); err != nil {
		t.Fatal(err)
	}
	prior := globalCtx
	globalCtx = sdk.NewAppCtxForTest(nil, db, nil, nil, nil)
	t.Cleanup(func() { globalCtx = prior })
	r := httptest.NewRequest(http.MethodGet, "https://site.example.com/", nil)
	if got, err := publicProject(r); err != nil || got != "project-host" {
		t.Fatalf("publicProject = %q, %v", got, err)
	}
}

func TestFormJSONBodyLimit(t *testing.T) {
	body := strings.NewReader(`{"value":"` + strings.Repeat("x", int(maxFormBodyBytes)) + `"}`)
	r := httptest.NewRequest(http.MethodPost, "/", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if _, err := readFormPayload(r); err == nil {
		t.Fatal("oversized form payload was accepted")
	}
}

func TestExtractIPHashUsesProxyAppendedAddress(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test")
	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.8")
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.8")
	if extractIPHash(r1) != extractIPHash(r2) {
		t.Fatal("spoofed first X-Forwarded-For entry changed client hash")
	}
}

func TestPrivateIPsRejectedForURLImport(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1"} {
		if isPublicIP(net.ParseIP(raw)) {
			t.Fatalf("%s accepted as public", raw)
		}
	}
}

func TestPageCacheIsBoundedAndDropsExpiredEntries(t *testing.T) {
	invalidatePageCache()
	t.Cleanup(invalidatePageCache)
	cacheSet("expired", "x", "text/plain", "e", "", false)
	pageCacheMu.Lock()
	e := pageCache["expired"]
	e.storedAt = time.Now().Add(-2 * time.Hour)
	pageCache["expired"] = e
	pageCacheMu.Unlock()
	if _, ok := cacheGet("expired"); ok {
		t.Fatal("expired entry returned")
	}
	if _, exists := pageCache["expired"]; exists {
		t.Fatal("expired entry retained")
	}
	for i := 0; i < 2050; i++ {
		cacheSet(string(rune(i))+"-key", "x", "text/plain", "e", "", false)
	}
	if len(pageCache) != 2048 {
		t.Fatalf("cache size = %d, want 2048", len(pageCache))
	}
}

type storagePlatformStub struct {
	tk.BasePlatformClient
	input map[string]any
}

func (s *storagePlatformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"storage": float64(1)}}, nil
}

func (s *storagePlatformStub) GetInstance(int64) (*sdk.PlatformInstance, error) { return nil, nil }

func (s *storagePlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "storage" || tool != "files_upload" {
		return errors.New("unexpected call")
	}
	s.input = input
	b, _ := json.Marshal(map[string]any{"id": 42})
	return json.Unmarshal(b, out)
}

func TestStorageWriteUsesCurrentStorageContract(t *testing.T) {
	platform := &storagePlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, nil, nil, platform, nil)
	if ctx.IntegrationFor("storage") == nil {
		t.Fatalf("storage fixture not bound; manifest integrations=%#v", manifest.Requires.Integrations)
	}
	id, err := storageWrite(ctx, "p1", "/.media/random.png", []byte("png"), "image/png")
	if err != nil || id != 42 {
		t.Fatalf("storageWrite = %d, %v", id, err)
	}
	if platform.input["name"] != "random.png" || platform.input["folder"] != "/.media" || platform.input["content_base64"] == nil {
		t.Fatalf("storage input = %#v", platform.input)
	}
	if platform.input["_project_id"] != "p1" {
		t.Fatalf("project not threaded: %#v", platform.input)
	}
}

type routingPlatformStub struct {
	tk.BasePlatformClient
	failRegister bool
	calls        []struct {
		tool  string
		input map[string]any
	}
}

func (s *routingPlatformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"routing": float64(1)}}, nil
}

func (s *routingPlatformStub) GetInstance(int64) (*sdk.PlatformInstance, error) { return nil, nil }

func (s *routingPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.calls = append(s.calls, struct {
		tool  string
		input map[string]any
	}{tool: tool, input: input})
	if app != "routes" {
		return errors.New("unexpected app")
	}
	if tool == "routes_register" && s.failRegister {
		return errors.New("register failed")
	}
	b, _ := json.Marshal(map[string]any{"action": "created", "route": map[string]any{}})
	return json.Unmarshal(b, out)
}

func TestAttachDomainRegistersRestartSafeRouteBeforeDatabase(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	db := hardeningTestDB(t)
	site, _ := dbCreateSite(db, "p1", "main", "Main", "")
	platform := &routingPlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	_, err := (&App{}).toolSitesAttachDomain(ctx, map[string]any{
		"_project_id": "p1", "id": site.ID, "fqdn": "site.example.com",
		"target": "203.0.113.10", "auto_dns": false, "auto_tls": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(platform.calls) == 0 || platform.calls[0].tool != "routes_register" {
		t.Fatalf("route calls = %#v", platform.calls)
	}
	input := platform.calls[0].input
	if input["target"] != "app://content?project_id=p1" || input["_project_id"] != "p1" {
		t.Fatalf("route input = %#v", input)
	}
	updated, _ := dbGetSite(db, "p1", site.ID)
	if updated.Hostname != "site.example.com" {
		t.Fatalf("hostname = %q", updated.Hostname)
	}
}

func TestAttachDomainDoesNotMutateDatabaseWhenRouteFails(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	db := hardeningTestDB(t)
	site, _ := dbCreateSite(db, "p1", "main", "Main", "")
	platform := &routingPlatformStub{failRegister: true}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	_, err := (&App{}).toolSitesAttachDomain(ctx, map[string]any{
		"_project_id": "p1", "id": site.ID, "fqdn": "site.example.com",
		"target": "203.0.113.10", "auto_dns": false, "auto_tls": false,
	})
	if err == nil {
		t.Fatal("route failure was ignored")
	}
	updated, _ := dbGetSite(db, "p1", site.ID)
	if updated.Hostname != "" {
		t.Fatalf("hostname mutated to %q", updated.Hostname)
	}
}
