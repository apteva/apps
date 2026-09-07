package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func auditFixture(t *testing.T) (*sql.DB, *sdk.AppCtx, *Site) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "audit")
	db := hardeningTestDB(t)
	db.SetMaxOpenConns(1)
	s, err := dbCreateSite(db, "audit", "main", "Audit", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(nil, db, nil, nil, nil)
	prior := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = prior; invalidatePageCache() })
	if err := initializeThemes(); err != nil {
		t.Fatal(err)
	}
	return db, ctx, s
}
func auditPost(t *testing.T, db *sql.DB, site int64, title, locale string) *Post {
	t.Helper()
	p, err := dbCreatePost(db, "audit", site, PostCreate{Title: title, Locale: locale})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestFormPrivacyAndHistoricalCleanup(t *testing.T) {
	db, _, site := auditFixture(t)
	fields := []any{map[string]any{"name": "passcode", "type": "password", "required": true}, map[string]any{"name": "email", "type": "email"}}
	doc := Document{Version: 1, Blocks: []Block{{ID: "b_signup", Type: "core/form", Attrs: map[string]any{"fields": fields}}}}
	p, err := dbCreatePost(db, "audit", site.ID, PostCreate{Title: "Signup", Blocks: &doc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbPublishPost(db, "audit", site.ID, p.ID, "", "test"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/_forms/submit/b_signup", strings.NewReader(`{"passcode":"synthetic-secret","email":"a@example.test","nested":{"refresh_token":"nested-secret"}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	(&App{}).handleFormSubmit(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	subs, err := dbListFormSubmissions(db, "audit", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(subs)
	if strings.Contains(string(body), "synthetic-secret") || strings.Contains(string(body), "nested-secret") {
		t.Fatalf("secret persisted: %s", body)
	}
	if !strings.Contains(string(body), "a@example.test") {
		t.Fatal("non-sensitive field lost")
	}
	result := privateFormResults([]ActionResult{{App: "auth", Tool: "signup", OK: true, Output: map[string]any{"unusual_credential_name": "value"}}, {App: "auth", Error: "provider included a secret"}})
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "value") || strings.Contains(string(raw), "provider included") {
		t.Fatal("provider output retained")
	}
	_, err = db.Exec(`INSERT INTO form_submissions(project_id,site_id,post_id,block_id,payload,results,status,error,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, "audit", site.ID, p.ID, "b_signup", `{"passcode":"old-secret","password":"old-password","email":"keep@example.test"}`, `[{"app":"auth","ok":true,"output":{"access_token":"old-token"}}]`, "ok", "secret in error", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = scrubHistoricalFormSecrets(db); err != nil {
		t.Fatal(err)
	}
	if err = scrubHistoricalFormSecrets(db); err != nil {
		t.Fatal(err)
	}
	subs, err = dbListFormSubmissions(db, "audit", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(subs)
	for _, secret := range []string{"old-secret", "old-password", "old-token", "secret in error"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("historical secret retained: %s", secret)
		}
	}
	if !strings.Contains(string(body), "keep@example.test") {
		t.Fatal("historical non-sensitive field lost")
	}
}
func TestFormsListDoesNotDeadlockSDKPool(t *testing.T) {
	db, ctx, site := auditFixture(t)
	_, err := dbCreatePost(db, "audit", site.ID, PostCreate{Title: "Form", Blocks: &Document{Version: 1, Blocks: []Block{{ID: "b_form", Type: "core/form"}}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := (&App{}).toolFormsList(ctx, map[string]any{}); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forms_list deadlocked with single connection")
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
}
func TestScheduledPublishingNormalizesAndValidates(t *testing.T) {
	db, ctx, site := auditFixture(t)
	p := auditPost(t, db, site.ID, "Scheduled", "en")
	input := time.Now().Add(-time.Hour).In(time.FixedZone("offset", 5*3600)).Format(time.RFC3339)
	if _, err := dbPublishPost(db, "audit", site.ID, p.ID, input, "test"); err != nil {
		t.Fatal(err)
	}
	if err := runScheduledPublisher(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := dbGetPost(db, "audit", site.ID, p.ID)
	if err != nil || p.Status != "published" {
		t.Fatalf("due post: %+v %v", p, err)
	}
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	if _, err := dbPublishPost(db, "audit", site.ID, p.ID, future, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbPublishPost(db, "audit", site.ID, p.ID, "not a date", "test"); err == nil {
		t.Fatal("invalid schedule accepted")
	}
	if err := runScheduledPublisher(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = dbGetPost(db, "audit", site.ID, p.ID)
	if err != nil || p.Status != "scheduled" {
		t.Fatal("future post published early")
	}
}
func TestScheduleUpgradeMigration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	names := []string{"001_init.sql", "002_templates.sql", "003_sites.sql", "004_site_id.sql", "005_form_submissions.sql", "006_hardening.sql", "007_extensions.sql"}
	for _, name := range names {
		script, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(script)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO posts(project_id,slug,status,scheduled_at) VALUES('p','old','scheduled','2026-09-07T12:00:00+02:00')`); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("migrations/008_audit_hardening.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	var stamp string
	var version int
	if err = db.QueryRow(`SELECT CAST(scheduled_at AS TEXT),edit_version FROM posts`).Scan(&stamp, &version); err != nil {
		t.Fatal(err)
	}
	if stamp != "2026-09-07 10:00:00" || version != 1 {
		t.Fatalf("upgrade %q/%d", stamp, version)
	}
}
func TestEditsAreAtomicAndRejectStaleVersions(t *testing.T) {
	db, _, site := auditFixture(t)
	p := auditPost(t, db, site.ID, "Original", "en")
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, title := range []string{"A", "B"} {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			_, err := dbUpdatePost(db, "audit", site.ID, p.ID, PostPatch{Title: &title, ExpectedVersion: &p.EditVersion}, "", "test", "")
			out <- err
		}(title)
	}
	wg.Wait()
	close(out)
	ok, conflicts := 0, 0
	for err := range out {
		if err == nil {
			ok++
		} else if errors.Is(err, errEditConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("updates=%d conflicts=%d", ok, conflicts)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM revisions`).Scan(&count)
	if count != 1 {
		t.Fatalf("revision count %d", count)
	}
	if _, err := dbPublishPost(db, "audit", site.ID, p.ID, "", "test", p.EditVersion); !errors.Is(err, errEditConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	invalid := Document{Version: 1, Blocks: []Block{{ID: "x", Type: "invalid"}}}
	if _, err := dbUpdatePost(db, "audit", site.ID, p.ID, PostPatch{Blocks: &invalid}, "", "test", ""); err == nil {
		t.Fatal("invalid document accepted")
	}
	db.QueryRow(`SELECT COUNT(*) FROM revisions`).Scan(&count)
	if count != 1 {
		t.Fatal("failed update created revision")
	}
	// Database failure after revision insertion must roll the revision back.
	_, err := db.Exec(`CREATE TRIGGER reject_post_update BEFORE UPDATE ON posts BEGIN SELECT RAISE(ABORT,'forced update error'); END`)
	if err != nil {
		t.Fatal(err)
	}
	title := "Rejected"
	if _, err := dbUpdatePost(db, "audit", site.ID, p.ID, PostPatch{Title: &title}, "", "test", ""); err == nil {
		t.Fatal("trigger ignored")
	}
	db.QueryRow(`SELECT COUNT(*) FROM revisions`).Scan(&count)
	if count != 1 {
		t.Fatal("failed transaction retained revision")
	}
}
func TestAdministrativeActionsRejectGETAndStalePATCH(t *testing.T) {
	db, _, site := auditFixture(t)
	p := auditPost(t, db, site.ID, "Draft", "en")
	for _, action := range []string{"publish", "unpublish", "archive"} {
		w := httptest.NewRecorder()
		(&App{}).handleHTTPPostItem(w, httptest.NewRequest("GET", fmt.Sprintf("/admin/posts/%d/%s", p.ID, action), nil))
		if w.Code != 405 {
			t.Fatalf("%s: %d", action, w.Code)
		}
	}
	w := httptest.NewRecorder()
	(&App{}).handleHTTPPostItem(w, httptest.NewRequest("PATCH", fmt.Sprintf("/admin/posts/%d", p.ID), strings.NewReader(`{"title":"stale","expected_version":99}`)))
	if w.Code != 409 {
		t.Fatalf("stale patch: %d %s", w.Code, w.Body.String())
	}
	p, _ = dbGetPost(db, "audit", site.ID, p.ID)
	if p.Status != "draft" || p.Title != "Draft" {
		t.Fatal("rejected request changed content")
	}
}
func TestAppendPreservesExistingResourcesAndPreviewMatches(t *testing.T) {
	db, ctx, site := auditFixture(t)
	original := `name: sample
settings: {site_title: Original}
menus:
  - slug: primary
    name: Original navigation
    items: [{label: Original, target_url: /original}]
redirects: [{from_path: /old, to_path: /original}]
pages: [{slug: home, title: Original Home}]
homepage_slug: home
`
	_, err := dbUpsertTemplate(db, "audit", Template{Name: "sample", Body: original})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyTemplate(ctx, "audit", site.ID, "sample", ApplyOverwrite, false); err != nil {
		t.Fatal(err)
	}
	originalHome, _ := dbGetSetting(db, "audit", site.ID, "homepage_page_id")
	changed := strings.ReplaceAll(original, "Original", "Replacement") + "posts: [{slug: new-post, title: New Post}]\n"
	changed = strings.ReplaceAll(changed, "/original", "/replacement")
	changed = strings.Replace(changed, "homepage_slug: home", "homepage_slug: new-home", 1)
	changed = strings.Replace(changed, "pages: [{slug: home, title: Replacement Home}]", "pages: [{slug: home, title: Replacement Home}, {slug: new-home, title: New Home}]", 1)
	if _, err = dbUpsertTemplate(db, "audit", Template{Name: "sample", Body: changed}); err != nil {
		t.Fatal(err)
	}
	preview, err := applyTemplate(ctx, "audit", site.ID, "sample", ApplyAppend, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbGetPostBySlug(db, "audit", site.ID, "post", "en", "new-post"); err != sql.ErrNoRows {
		t.Fatal("preview persisted content")
	}
	applied, err := applyTemplate(ctx, "audit", site.ID, "sample", ApplyAppend, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview.Created, applied.Created) || !reflect.DeepEqual(preview.Skipped, applied.Skipped) {
		t.Fatalf("preview/apply differ: %+v %+v", preview, applied)
	}
	title, _ := dbGetSetting(db, "audit", site.ID, "site_title")
	home, _ := dbGetSetting(db, "audit", site.ID, "homepage_page_id")
	menu, _ := dbGetMenuBySlug(db, "audit", site.ID, "primary")
	redirect, _ := dbLookupRedirect(db, "audit", site.ID, "/old")
	if title != "Original" || home != originalHome || menu.Items[0].Label != "Original" || redirect.To != "/original" {
		t.Fatalf("append overwrote resources: %s %s %+v %+v", title, home, menu, redirect)
	}
}
func TestPublicLocalesAndTaxonomyAreIsolated(t *testing.T) {
	db, _, site := auditFixture(t)
	fr := auditPost(t, db, site.ID, "Bonjour", "fr")
	en := auditPost(t, db, site.ID, "Hello", "en")
	for _, p := range []*Post{fr, en} {
		if _, err := dbPublishPost(db, "audit", site.ID, p.ID, "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbSetSetting(db, "audit", site.ID, "default_locale", "fr"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/posts/bonjour", "/", "/?locale=en", "/"} {
		w := httptest.NewRecorder()
		(&App{}).handlePublic(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if path == "/?locale=en" {
			if !strings.Contains(body, "Hello") || strings.Contains(body, ">Bonjour<") {
				t.Fatal("English list wrong")
			}
		} else if !strings.Contains(body, "Bonjour") || strings.Contains(body, ">Hello<") {
			t.Fatal("French list wrong")
		}
	}
	category, _ := dbCreateTerm(db, "audit", site.ID, "category", "News", "news", "", nil)
	tag, _ := dbCreateTerm(db, "audit", site.ID, "tag", "News", "news", "", nil)
	if err := dbAssignTerms(db, "audit", site.ID, fr.ID, []int64{category.ID, tag.ID}); err != nil {
		t.Fatal(err)
	}
	if err := dbAssignTerms(db, "audit", site.ID, en.ID, []int64{tag.ID}); err != nil {
		t.Fatal(err)
	}
	all, total, err := dbSearchPosts(db, "audit", site.ID, PostSearch{TermSlug: "news"})
	if err != nil || len(all) != 2 || total != 2 {
		t.Fatalf("duplicate taxonomy rows: %d %d %v", len(all), total, err)
	}
	only, total, err := dbSearchPosts(db, "audit", site.ID, PostSearch{TermID: category.ID})
	if err != nil || total != 1 || only[0].ID != fr.ID {
		t.Fatalf("mixed taxonomy: %+v %d %v", only, total, err)
	}
}
func TestSitemapUsesMetadataAndCachedResponse(t *testing.T) {
	db, _, site := auditFixture(t)
	p := auditPost(t, db, site.ID, "Indexed", "en")
	if _, err := dbPublishPost(db, "audit", site.ID, p.ID, "", "test"); err != nil {
		t.Fatal(err)
	}
	// A malformed body must not affect a metadata-only sitemap query.
	if _, err := db.Exec(`UPDATE posts SET body_blocks='invalid json' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/sitemap.xml", nil)
	(&App{}).handleSitemap(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/posts/indexed") {
		t.Fatalf("sitemap: %d %s", w.Code, w.Body.String())
	}
	// Without an invalidation, a cache hit must retain the existing response.
	db.Exec(`UPDATE posts SET slug='changed' WHERE id=?`, p.ID)
	hit := httptest.NewRecorder()
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	(&App{}).handleSitemap(hit, r)
	if hit.Code != 304 {
		t.Fatalf("cached sitemap status %d", hit.Code)
	}
	invalidatePageCacheForSite(site.ID)
	fresh := httptest.NewRecorder()
	(&App{}).handleSitemap(fresh, httptest.NewRequest("GET", "/sitemap.xml", nil))
	if !strings.Contains(fresh.Body.String(), "/posts/changed") {
		t.Fatal("sitemap failed to invalidate")
	}
}
func TestExtensionRouteOwnershipProtectsPublishedAndChecksPublish(t *testing.T) {
	db, _, site := auditFixture(t)
	first := testExtensionManifest()
	if _, err := dbExtensionUpsert(db, "audit", site.ID, "store", "commerce", first, true); err != nil {
		t.Fatal(err)
	}
	first.Routes[0].Pattern = "/new-products/:handle"
	if _, err := dbExtensionUpsert(db, "audit", site.ID, "store", "commerce", first, false); err != nil {
		t.Fatal(err)
	}
	second := testExtensionManifest()
	if _, err := dbExtensionUpsert(db, "audit", site.ID, "reviews", "reviews", second, true); err == nil {
		t.Fatal("claimed another extension's live route")
	}
	if _, err := dbExtensionPublish(db, "audit", site.ID, "store"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbExtensionUpsert(db, "audit", site.ID, "reviews", "reviews", second, true); err != nil {
		t.Fatal(err)
	}
	// Simulate conflicting draft left by a prior release. Publication rechecks it.
	first.Routes[0].Pattern = second.Routes[0].Pattern
	raw, _ := json.Marshal(first)
	db.Exec(`UPDATE content_extensions SET draft_manifest=? WHERE extension_key='store'`, string(raw))
	if _, err := dbExtensionPublish(db, "audit", site.ID, "store"); err == nil {
		t.Fatal("publication skipped ownership validation")
	}
}
func TestExtensionCachesInvalidateAndTemplatesKeepRequestIsolation(t *testing.T) {
	db, _, site := auditFixture(t)
	manifest := testExtensionManifest()
	manifest.Templates["product"] = `{{.SiteTitle}} <a href="{{asset "store.css"}}">asset</a>`
	ext, err := dbExtensionUpsert(db, "audit", site.ID, "store", "commerce", manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	first, err := cachedPublishedExtensions(db, "audit", site.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := cachedPublishedExtensions(db, "audit", site.ID)
	if err != nil || &first[0] != &again[0] {
		t.Fatal("manifest cache miss")
	}
	base, err := cachedExtensionTemplate(*ext, "product")
	if err != nil {
		t.Fatal(err)
	}
	base2, _ := cachedExtensionTemplate(*ext, "product")
	if base != base2 {
		t.Fatal("template reparsed")
	}
	for _, title := range []string{"first-request", "second-request"} {
		body, err := renderExtensionSource(*ext, "product", extensionPageData{SiteTitle: title, URLPrefix: "/" + title + "/"})
		if err != nil || !strings.Contains(body, title) {
			t.Fatalf("request data lost: %s %v", body, err)
		}
		other := "first-request"
		if title == other {
			other = "second-request"
		}
		if strings.Contains(body, other) {
			t.Fatal("request data leaked")
		}
	}
	manifest.Name = "Updated"
	if _, err := dbExtensionUpsert(db, "audit", site.ID, "store", "commerce", manifest, true); err != nil {
		t.Fatal(err)
	}
	fresh, _ := cachedPublishedExtensions(db, "audit", site.ID)
	if fresh[0].PublishedManifest.Name != "Updated" {
		t.Fatal("published manifest cache stale")
	}
}
func TestExtensionRateLimiterExpiresAndBoundsKeys(t *testing.T) {
	extensionRateMu.Lock()
	extensionRateLog = map[string][]int64{}
	extensionRateLastPrune = 0
	extensionRateMu.Unlock()
	t.Cleanup(func() {
		extensionRateMu.Lock()
		extensionRateLog = map[string][]int64{}
		extensionRateLastPrune = 0
		extensionRateMu.Unlock()
	})
	for i := 0; i < maxExtensionRateKeys; i++ {
		if !extensionRateLimitAt(fmt.Sprint(i), 1000) {
			t.Fatal("key rejected before capacity")
		}
	}
	if extensionRateLimitAt("overflow", 1000) {
		t.Fatal("capacity unbounded")
	}
	if !extensionRateLimitAt("new-visitor", 1061) {
		t.Fatal("expired entries not removed")
	}
	if len(extensionRateLog) != 1 {
		t.Fatalf("expired keys retained: %d", len(extensionRateLog))
	}
	for i := 1; i < 30; i++ {
		if !extensionRateLimitAt("new-visitor", 1061) {
			t.Fatal("rate limit early")
		}
	}
	if extensionRateLimitAt("new-visitor", 1061) {
		t.Fatal("rate limit bypassed")
	}
}

func TestReleaseManifestPinsItsOwnVersion(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Runtime.Source == nil || manifest.Runtime.Source.Ref != "content/v"+manifest.Version {
		t.Fatal("release manifest must resolve its own source tag")
	}
	if err := sdk.ValidateManifest(&manifest); err != nil {
		t.Fatal(err)
	}
}
