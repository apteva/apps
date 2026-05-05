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
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAbsoluteContentURL_WithEnv(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	f := &File{ID: 42}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/files/42/content"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAbsoluteContentURL_NoEnv(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "")
	t.Setenv("APTEVA_PUBLIC_URL", "")
	f := &File{ID: 42}
	got := absoluteContentURL(nil, f)
	want := "/api/apps/storage/files/42/content"
	if got != want {
		t.Fatalf("got %q, want %q (relative when no public_url)", got, want)
	}
}

func TestAbsoluteContentURL_StripsTrailingSlash(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com/")
	f := &File{ID: 7}
	got := absoluteContentURL(nil, f)
	want := "https://agents.example.com/api/apps/storage/files/7/content"
	if got != want {
		t.Fatalf("got %q, want %q (trailing slash should be stripped)", got, want)
	}
}

// Same URL shape regardless of visibility — only auth requirements
// differ. Confirms we don't accidentally fork URL paths by
// visibility.
func TestAbsoluteContentURL_VisibilityIndependent(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	pub := absoluteContentURL(nil, &File{ID: 1, Visibility: "public"})
	sig := absoluteContentURL(nil, &File{ID: 1, Visibility: "signed"})
	priv := absoluteContentURL(nil, &File{ID: 1, Visibility: "private"})
	if pub != sig || sig != priv {
		t.Fatalf("URLs differ by visibility: public=%q signed=%q private=%q", pub, sig, priv)
	}
}

func TestSignedAbsoluteURL(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	got := signedAbsoluteURL(nil, 42, "abcdef", 1234567890)
	want := "https://agents.example.com/api/apps/storage/files/42/content?sig=abcdef&exp=1234567890"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDBGetByID_PopulatesURL(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "video.mp4", "/clips/", "fakebytes")

	wantURL := "https://agents.example.com/api/apps/storage/files/" +
		intToString(f.ID) + "/content"
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
	// Strip everything before the path so httptest sees a clean URL.
	if i := indexOfStr(url, "/files/"); i >= 0 {
		url = url[i:] + "&project_id=test-proj"
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	app.httpServeContent(rec, req, f.ID)
	if rec.Code != 200 {
		t.Fatalf("signed fetch with valid sig: status=%d body=%s", rec.Code, rec.Body.String())
	}
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
