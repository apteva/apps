package main

// URL minting + visibility-aware serve-content tests.
//
// One URL per file (S3-shaped); whether it works without auth is
// decided server-side based on the file's visibility:
//
//   public  → anyone can fetch
//   signed  → requires ?sig=&exp=
//   private → requires X-User-ID set by authMiddleware OR valid sig

import (
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type urlPlatform struct {
	tk.BasePlatformClient
	publicURL string
}

type cdnCountingPlatform struct {
	tk.BasePlatformClient
	calls int
}

func (p *cdnCountingPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls++
	if app != "cdn" || tool != "cdn_url_for" || input["origin_path"] != "/" {
		return nil
	}
	dst := out.(*struct {
		URL string `json:"url"`
	})
	dst.URL = "https://cdn.example.com/"
	return nil
}

func (p *urlPlatform) PlatformInfo() (*sdk.PlatformInfo, error) {
	return &sdk.PlatformInfo{PublicURL: p.publicURL}, nil
}

func TestAbsoluteContentURL_WithEnv(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	f := &File{ID: 42, Name: "video.mp4"}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/files/42/content/video.mp4"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAbsoluteContentURL_NoEnv(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "")
	t.Setenv("APTEVA_PUBLIC_URL", "")
	f := &File{ID: 42, Name: "video.mp4"}
	got := absoluteContentURL(nil, f)
	want := "/api/apps/storage/files/42/content/video.mp4"
	if got != want {
		t.Fatalf("got %q, want %q (relative when no public_url)", got, want)
	}
}

func TestAbsoluteContentURL_StripsTrailingSlash(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com/")
	f := &File{ID: 7, Name: "x.png"}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/files/7/content/x.png"
	if got != want {
		t.Fatalf("got %q, want %q (trailing slash should be stripped)", got, want)
	}
}

func TestAbsoluteContentURL_PrefersPlatformInfo(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://stale.example.com")
	ctx := newTestCtx(t, tk.WithPlatform(&urlPlatform{publicURL: "https://fresh.example.com/"}))
	f := &File{ID: 7, Name: "x.png"}
	got := absoluteContentURL(ctx, f)
	want := "https://fresh.example.com/api/apps/storage/files/7/content/x.png"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAbsoluteContentURL_CachesCDNBaseAcrossFileRows(t *testing.T) {
	resetCDNBaseCache()
	t.Cleanup(resetCDNBaseCache)
	platform := &cdnCountingPlatform{}
	ctx := newTestCtx(t,
		tk.WithPlatform(platform),
		tk.WithConfig(map[string]string{"cdn_zone_id": "7"}),
	)
	for i := int64(1); i <= 100; i++ {
		got := absoluteContentURL(ctx, &File{
			ID: i, Name: "x.png", ProjectID: "test-proj", Visibility: "public",
		})
		if !strings.HasPrefix(got, "https://cdn.example.com/files/") {
			t.Fatalf("file %d URL = %q", i, got)
		}
	}
	if platform.calls != 1 {
		t.Fatalf("cdn_url_for calls = %d, want 1 for 100 files", platform.calls)
	}
}

// Filename appears URL-escaped — spaces, special chars, unicode all
// land safely in the path. This is the property browsers/CDN edges
// + Twitter cards rely on for content sniffing.
func TestAbsoluteContentURL_EscapesFilename(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	f := &File{ID: 9, Name: "my video (final).mp4"}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/files/9/content/my%20video%20%28final%29.mp4"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAbsoluteContentURL_NameMissingFallsBack(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	// Defensive — empty name shouldn't produce a trailing slash.
	f := &File{ID: 5}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/files/5/content"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Public files use the constrained no-auth route. Signed and private
// metadata still point at the authenticated file route until a share
// URL is explicitly minted.
func TestAbsoluteContentURL_UsesPublicRouteOnlyForPublicFiles(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	pub := absoluteContentURL(nil, &File{ID: 1, Visibility: "public"})
	sig := absoluteContentURL(nil, &File{ID: 1, Visibility: "signed"})
	priv := absoluteContentURL(nil, &File{ID: 1, Visibility: "private"})
	if pub != "https://agents.example.com/api/apps/storage/public/files/1/content" {
		t.Fatalf("public URL = %q", pub)
	}
	if sig != priv || sig != "https://agents.example.com/api/apps/storage/files/1/content" {
		t.Fatalf("authenticated URLs: signed=%q private=%q", sig, priv)
	}
}

func TestSignedAbsoluteURL(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	got := signedAbsoluteURL(nil, &File{ID: 42, Name: "video.mp4"}, "abcdef", 1234567890)
	want := "https://agents.example.com/api/apps/storage/public/files/42/content/video.mp4?sig=abcdef&exp=1234567890"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Regression: signed URLs must carry project_id so the platform's
// /api/apps/storage/... proxy routes to the install that owns the
// file. Without it, the proxy falls back to byName (last-wins) and
// 404s for any file not in that arbitrarily-chosen install's DB —
// the symptom that landed DLNA's MeGusta-mkv "device disconnected"
// failure in v0.1.17/18.
func TestSignedAbsoluteURL_IncludesProjectID(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	got := signedAbsoluteURL(nil, &File{
		ID:        42,
		Name:      "video.mp4",
		ProjectID: "1776532035349-7aca99abbd8afe9e",
	}, "abcdef", 1234567890)
	want := "https://agents.example.com/api/apps/storage/public/files/42/content/video.mp4?sig=abcdef&exp=1234567890&project_id=1776532035349-7aca99abbd8afe9e"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDBGetByID_PopulatesURL(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "video.mp4", "/clips/", "fakebytes")

	wantURL := "https://agents.example.com/api/apps/storage/files/" +
		intToString(f.ID) + "/content/video.mp4"
	if f.URL != wantURL {
		t.Errorf("URL = %q, want %q", f.URL, wantURL)
	}
}

// httpServeContent decides anonymous-or-not based on visibility.
// Public files serve to anyone; private/signed need either a valid
// sig or X-User-ID set (the authMiddleware-side signal of an
// authenticated request).

func TestHttpServeContent_Public_AnonymousAllowed(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "doc.txt", "/", "P")
	app := &App{}
	if _, err := app.toolSetVisibility(ctx, map[string]any{
		"id": f.ID, "visibility": "public",
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/files/"+intToString(f.ID)+"/content?project_id=test-proj", nil)
	// No X-User-ID — fully anonymous.
	rec := httptest.NewRecorder()
	app.httpServeContent(rec, req, f.ID)
	if rec.Code != 200 {
		t.Fatalf("anonymous public-file fetch: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHttpServeContent_Private_AnonymousRefused(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "secret.txt", "/", "S") // default visibility = private
	app := &App{}
	req := httptest.NewRequest(http.MethodGet,
		"/files/"+intToString(f.ID)+"/content?project_id=test-proj", nil)
	// Anonymous — no X-User-ID, no sig. This is the gap the relaxed
	// auth middleware opened: storage MUST refuse.
	rec := httptest.NewRecorder()
	app.httpServeContent(rec, req, f.ID)
	if rec.Code != 403 {
		t.Fatalf("anonymous private-file fetch: status=%d, want 403", rec.Code)
	}
}

func TestHttpServeContent_Private_AuthedAllowed(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "doc.txt", "/", "X")
	app := &App{}
	req := httptest.NewRequest(http.MethodGet,
		"/files/"+intToString(f.ID)+"/content?project_id=test-proj", nil)
	// authMiddleware sets X-User-ID for sessioned/API-keyed/install
	// requests — simulate that here.
	req.Header.Set("X-User-ID", "1")
	rec := httptest.NewRecorder()
	app.httpServeContent(rec, req, f.ID)
	if rec.Code != 200 {
		t.Fatalf("authed private fetch: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHttpServeContent_Signed_AnonymousAllowedWithSig(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "doc.txt", "/", "X")
	app := &App{}
	if _, err := app.toolSetVisibility(ctx, map[string]any{
		"id": f.ID, "visibility": "signed",
	}); err != nil {
		t.Fatal(err)
	}
	// Mint a valid sig the same way files_get_url does.
	out, err := app.toolGetURL(ctx, map[string]any{"id": f.ID})
	if err != nil {
		t.Fatal(err)
	}
	url := out.(map[string]any)["url"].(string)
	// Strip everything before the public path so httptest sees a clean URL.
	if i := indexOfStr(url, "/public/files/"); i >= 0 {
		url = url[i:]
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	app.handlePublicFilesItem(rec, req)
	if rec.Code != 200 {
		t.Fatalf("signed fetch with valid sig: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicFilesRoute_ReadOnlyAndVisibilityAware(t *testing.T) {
	ctx := newTestCtx(t)
	publicFile := mustUpload(t, ctx, "public.txt", "/", "public bytes")
	if _, err := (&App{}).toolSetVisibility(ctx, map[string]any{
		"id": publicFile.ID, "visibility": "public",
	}); err != nil {
		t.Fatal(err)
	}
	privateFile := mustUpload(t, ctx, "private.txt", "/", "private bytes")
	app := &App{}

	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"public content", http.MethodGet, "/public/files/" + intToString(publicFile.ID) + "/content/public.txt?project_id=test-proj", http.StatusOK},
		{"private content", http.MethodGet, "/public/files/" + intToString(privateFile.ID) + "/content/private.txt?project_id=test-proj", http.StatusForbidden},
		{"metadata", http.MethodGet, "/public/files/" + intToString(publicFile.ID) + "?project_id=test-proj", http.StatusNotFound},
		{"mutation", http.MethodPost, "/public/files/" + intToString(publicFile.ID) + "/content?project_id=test-proj", http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			rec := httptest.NewRecorder()
			app.handlePublicFilesItem(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), test.status)
			}
		})
	}
}

func TestAbsoluteContentURL_PublicIncludesContentVersion(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	f := &File{
		ID: 42, Name: "image.png", Visibility: "public",
		SHA256: "b46cc4504014abcdef0123456789",
	}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/public/files/42/content/image.png?v=b46cc4504014abcd"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSignedAbsoluteURL_AttachmentUsesDownloadRouteAndVersion(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	f := &File{ID: 42, Name: "résumé.pdf", ProjectID: "p 1", SHA256: "b46cc4504014abcdef"}
	got := signedAbsoluteURLWithDisposition(nil, f, "abcdef", 1234567890, DispositionAttachment)
	want := "https://agents.example.com/api/apps/storage/public/files/42/download/r%C3%A9sum%C3%A9.pdf?sig=abcdef&exp=1234567890&v=b46cc4504014abcd&project_id=p+1"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestS3ContentRedirect_IsInlineNoStoreAndNever304(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "image/png", "public")
	_ = ctx
	req := httptest.NewRequest(http.MethodGet,
		"/public/files/"+intToString(f.ID)+"/content/image.png?project_id=test-proj", nil)
	req.Header.Set("If-None-Match", `"`+f.SHA256+`"`)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control=%q", got)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("remote redirect exposed validator %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff=%q", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "https://fake-s3.example.com/") {
		t.Fatalf("location=%q", rec.Header().Get("Location"))
	}
	if stub.getOptions.Disposition != DispositionInline || stub.getOptions.ContentType != "image/png" {
		t.Fatalf("get options=%+v", stub.getOptions)
	}
	if stub.getTTL > 15*time.Minute || stub.getTTL < 14*time.Minute {
		t.Fatalf("presign TTL=%v", stub.getTTL)
	}
}

func TestS3DownloadRedirect_UsesAttachment(t *testing.T) {
	_, f, stub := newRemoteFile(t, "image/png", "public")
	req := httptest.NewRequest(http.MethodGet,
		"/public/files/"+intToString(f.ID)+"/download/image.png?project_id=test-proj", nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.getOptions.Disposition != DispositionAttachment {
		t.Fatalf("disposition=%q", stub.getOptions.Disposition)
	}
}

func TestUnsafeS3Content_IsForcedToAttachment(t *testing.T) {
	_, f, stub := newRemoteFile(t, "text/html", "public")
	req := httptest.NewRequest(http.MethodGet,
		"/public/files/"+intToString(f.ID)+"/content/page.html?project_id=test-proj", nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d", rec.Code)
	}
	if stub.getOptions.Disposition != DispositionAttachment {
		t.Fatalf("HTML disposition=%q", stub.getOptions.Disposition)
	}
}

func TestDiskContentAndDownloadMatchDispositionAndCaching(t *testing.T) {
	ctx := newTestCtx(t)
	globalBackend = newDiskBackend(ctx)
	t.Cleanup(func() { globalBackend = nil })
	f := mustUploadTyped(t, ctx, "note.txt", "text/plain", "disk bytes")
	if _, err := (&App{}).toolSetVisibility(ctx, map[string]any{"id": f.ID, "visibility": "public"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		action string
		want   string
	}{
		{"content", "inline"},
		{"download", "attachment"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/public/files/"+intToString(f.ID)+"/"+tc.action+"/note.txt?project_id=test-proj", nil)
			rec := httptest.NewRecorder()
			(&App{}).handlePublicFilesItem(rec, req)
			if rec.Code != http.StatusOK || rec.Body.String() != "disk bytes" {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), tc.want+";") {
				t.Fatalf("content-disposition=%q", rec.Header().Get("Content-Disposition"))
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("cache-control=%q", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("nosniff=%q", got)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet,
		"/public/files/"+intToString(f.ID)+"/content/note.txt?project_id=test-proj", nil)
	req.Header.Set("If-None-Match", `"`+f.SHA256+`"`)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("disk conditional status=%d", rec.Code)
	}
}

func TestS3AuthorizationRulesRemainUnchanged(t *testing.T) {
	ctx, f, _ := newRemoteFile(t, "image/png", "private")
	app := &App{}

	request := func(visibility string, authed, signed bool) int {
		if _, err := ctx.AppDB().Exec(`UPDATE files SET visibility = ? WHERE id = ?`, visibility, f.ID); err != nil {
			t.Fatal(err)
		}
		path := "/public/files/" + intToString(f.ID) + "/content/image.png?project_id=test-proj"
		if signed {
			exp := time.Now().Add(time.Minute).Unix()
			path += "&sig=" + signFile(f.ID, exp) + "&exp=" + intToString(exp)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authed {
			req.Header.Set("X-User-ID", "1")
		}
		rec := httptest.NewRecorder()
		app.handlePublicFilesItem(rec, req)
		return rec.Code
	}

	if got := request("public", false, false); got != http.StatusFound {
		t.Fatalf("public anonymous status=%d", got)
	}
	if got := request("private", false, false); got != http.StatusForbidden {
		t.Fatalf("private anonymous status=%d", got)
	}
	if got := request("private", true, false); got != http.StatusFound {
		t.Fatalf("private authenticated status=%d", got)
	}
	if got := request("private", false, true); got != http.StatusFound {
		t.Fatalf("private signed status=%d", got)
	}
	if got := request("signed", false, false); got != http.StatusForbidden {
		t.Fatalf("signed without signature status=%d", got)
	}
	if got := request("signed", false, true); got != http.StatusFound {
		t.Fatalf("signed with signature status=%d", got)
	}
}

func TestSignedS3RedirectTTLDoesNotOutliveShare(t *testing.T) {
	_, f, stub := newRemoteFile(t, "image/png", "signed")
	exp := time.Now().Add(75 * time.Second).Unix()
	path := "/public/files/" + intToString(f.ID) + "/content/image.png?project_id=test-proj" +
		"&sig=" + signFile(f.ID, exp) + "&exp=" + intToString(exp)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.getTTL <= 60*time.Second || stub.getTTL > 75*time.Second {
		t.Fatalf("presign TTL=%v, want within signed URL remainder", stub.getTTL)
	}
}

func TestFilesGetURL_DeliveryCompatibilityAndExplicitOptions(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "image/png", "private")
	app := &App{}

	legacy, err := app.toolGetURL(ctx, map[string]any{"id": f.ID, "ttl_seconds": 60})
	if err != nil {
		t.Fatal(err)
	}
	legacyMap := legacy.(map[string]any)
	if legacyMap["delivery"] != "direct" || legacyMap["disposition"] != "attachment" {
		t.Fatalf("legacy response=%v", legacyMap)
	}

	stable, err := app.toolGetURL(ctx, map[string]any{
		"id": f.ID, "ttl_seconds": 60, "delivery": "apteva", "disposition": "inline",
	})
	if err != nil {
		t.Fatal(err)
	}
	stableMap := stable.(map[string]any)
	if stableMap["delivery"] != "apteva" || stableMap["disposition"] != "inline" {
		t.Fatalf("stable response=%v", stableMap)
	}
	if !strings.Contains(stableMap["url"].(string), "/public/files/") || !strings.Contains(stableMap["url"].(string), "/content/") {
		t.Fatalf("stable URL=%q", stableMap["url"])
	}
	if stub.getCalls != 1 {
		t.Fatalf("stable URL unexpectedly presigned; calls=%d", stub.getCalls)
	}

	direct, err := app.toolGetURL(ctx, map[string]any{
		"id": f.ID, "ttl_seconds": 60, "delivery": "direct", "disposition": "inline",
	})
	if err != nil {
		t.Fatal(err)
	}
	directMap := direct.(map[string]any)
	if directMap["delivery"] != "direct" || directMap["disposition"] != "inline" || stub.getOptions.Disposition != DispositionInline {
		t.Fatalf("direct response=%v options=%+v", directMap, stub.getOptions)
	}
}

func TestHTTPMintURL_AcceptsDeliveryAndDisposition(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "application/pdf", "private")
	_ = ctx
	body := strings.NewReader(`{"ttl_seconds":60,"delivery":"apteva","disposition":"attachment"}`)
	req := httptest.NewRequest(http.MethodPost,
		"/files/"+intToString(f.ID)+"/url?project_id=test-proj", body)
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["delivery"] != "apteva" || response["disposition"] != "attachment" {
		t.Fatalf("response=%v", response)
	}
	if !strings.Contains(response["url"].(string), "/download/") {
		t.Fatalf("URL=%q", response["url"])
	}
	if stub.getCalls != 0 {
		t.Fatalf("stable mint presigned backend URL; calls=%d", stub.getCalls)
	}
}

func TestRemoteHeadReturnsMetadataWithoutGETPresign(t *testing.T) {
	_, f, stub := newRemoteFile(t, "image/png", "public")
	req := httptest.NewRequest(http.MethodHead,
		"/public/files/"+intToString(f.ID)+"/content/image.png?project_id=test-proj", nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Length") != "3" {
		t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
	}
	if stub.getCalls != 0 {
		t.Fatalf("HEAD minted GET URL; calls=%d", stub.getCalls)
	}
}

func TestBrowserStylePublicPNGRedirectDecodesWithDimensions(t *testing.T) {
	_, f, stub := newRemoteFile(t, "image/png", "public")
	pngBytes, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	objectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", contentDispositionHeader(DispositionInline, "image.png"))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(objectServer.Close)
	stub.getURL = objectServer.URL + "/image.png"

	app := &App{}
	storageServer := httptest.NewServer(http.HandlerFunc(app.handlePublicFilesItem))
	t.Cleanup(storageServer.Close)
	resp, err := storageServer.Client().Get(storageServer.URL + "/public/files/" +
		intToString(f.ID) + "/content/image.png?project_id=test-proj")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	config, format, err := image.DecodeConfig(resp.Body)
	if err != nil {
		t.Fatalf("decode redirected PNG: %v", err)
	}
	if format != "png" || config.Width <= 0 || config.Height <= 0 {
		t.Fatalf("format=%q dimensions=%dx%d", format, config.Width, config.Height)
	}
}

func newRemoteFile(t *testing.T, contentType, visibility string) (*sdk.AppCtx, *File, *fakeS3Backend) {
	t.Helper()
	ctx := newTestCtx(t)
	globalBackend = newDiskBackend(ctx)
	f := mustUploadTyped(t, ctx, "image.png", contentType, "PNG")
	if _, err := (&App{}).toolSetVisibility(ctx, map[string]any{"id": f.ID, "visibility": visibility}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := dbGetByID(ctx.AppDB(), "test-proj", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	stub := newFakeS3()
	stub.objects[objectKey(refreshed.SHA256, refreshed.StorageKey)] = []byte("PNG")
	globalBackend = stub
	t.Cleanup(func() { globalBackend = nil })
	return ctx, refreshed, stub
}

func mustUploadTyped(t *testing.T, ctx *sdk.AppCtx, name, contentType, body string) *File {
	t.Helper()
	out, err := (&App{}).toolUpload(ctx, map[string]any{
		"name": name, "content_type": contentType, "content_base64": b64(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(int64)
	f, err := dbGetByID(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// --- helpers ---

func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
