package main

import (
	"encoding/json"
	"fmt"
	"io"
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

// ─── HTTP fixtures ─────────────────────────────────────────────────

// newTestServer mounts the app's real routes. apteva-server proxies
// /api/apps/streaming/* to the sidecar with the prefix stripped, so
// this mux sees exactly what production does.
func newTestServer(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range app.HTTPRoutes() {
		mux.HandleFunc(r.Pattern, r.Handler)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// localize turns a URL the app generated (relative, because there's no
// platform PublicURL in tests) into one that hits the test server —
// the same prefix-strip apteva-server's reverse proxy performs.
func localize(t *testing.T, srv *httptest.Server, generated string) string {
	t.Helper()
	const prefix = "/api/apps/streaming"
	if !strings.HasPrefix(generated, prefix) {
		t.Fatalf("expected a relative sidecar URL, got %q", generated)
	}
	return srv.URL + strings.TrimPrefix(generated, prefix)
}

// newGlobalTestApp is newTestApp for a GLOBAL-scope install: no
// APTEVA_PROJECT_ID, so every request has to name the project itself.
func newGlobalTestApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	dataDir := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithEnv("APTEVA_DATA_DIR", dataDir),
		tk.WithConfig(map[string]string{
			"rtmp_port_range":     "1935-1940",
			"hls_segment_seconds": "4",
		}),
	)
	app := &App{
		runners:       map[int64]*streamRunner{},
		viewers:       newViewerTracker(),
		throttle:      newViewerThrottle(),
		playback:      newPlaybackCache(playbackCacheTTL),
		runnerFactory: newFakeRunnerFactory(t),
	}
	pa, err := newPortAllocator("1935-1940")
	if err != nil {
		t.Fatalf("port allocator: %v", err)
	}
	app.ports = pa
	globalCtx = ctx
	globalApp = app
	return app, ctx
}

// writeStreamFile drops a file into a stream's data dir.
func writeStreamFile(t *testing.T, ctx *sdk.AppCtx, s *Stream, name, body string) string {
	t.Helper()
	dir := streamDataDir(ctx, s.StoragePrefix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func getStatus(t *testing.T, url string) (int, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// ─── Finding 3: global-scope installs ──────────────────────────────

// apteva.yaml declares scopes: [project, global]. On a global install
// APTEVA_PROJECT_ID is unset, so resolveProjectFromRequest 400s any
// request without ?project_id — and v0.1 generated every playback URL
// with only ?t=, i.e. every viewer request on every global install
// failed. (The smoke test hid it by hand-appending project_id.)
func TestGlobalScope_GeneratedURLsAreFetchable(t *testing.T) {
	app, ctx := newGlobalTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{
		"name":        "global stream",
		"_project_id": "proj-77",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
	srv := newTestServer(t, app)

	if !strings.Contains(s.PlaybackURL, "project_id=proj-77") {
		t.Fatalf("playback_url must carry project_id on a global install: %q", s.PlaybackURL)
	}
	if code, _ := getStatus(t, localize(t, srv, s.PlaybackURL)); code != http.StatusOK {
		t.Errorf("generated playback_url returned %d, want 200", code)
	}

	if !strings.Contains(s.HeartbeatURL, "project_id=proj-77") {
		t.Fatalf("heartbeat_url must carry project_id: %q", s.HeartbeatURL)
	}
	if code, _ := getStatus(t, localize(t, srv, s.HeartbeatURL)); code != http.StatusOK {
		t.Errorf("generated heartbeat_url returned %d, want 200", code)
	}

	// The v0.1 shape — token only — is still the failure it always was.
	bare := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, indexPlaylistFile, s.PlaybackToken)
	if code, _ := getStatus(t, bare); code != http.StatusBadRequest {
		t.Errorf("URL without project_id returned %d, want 400", code)
	}
}

func TestProjectScope_URLsOmitProjectID(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "scoped"})
	s := out.(map[string]any)["stream"].(*Stream)
	if strings.Contains(s.PlaybackURL, "project_id=") {
		t.Errorf("project-scoped install shouldn't need project_id in URLs: %q", s.PlaybackURL)
	}
}

// ─── Finding 5: token comparison ───────────────────────────────────

func TestPlayback_WrongTokenIs404(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile, "#EXTM3U\n")
	srv := newTestServer(t, app)

	for _, tok := range []string{"", "nope", s.PlaybackToken[:len(s.PlaybackToken)-1]} {
		url := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, indexPlaylistFile, tok)
		if code, _ := getStatus(t, url); code != http.StatusNotFound {
			t.Errorf("token %q returned %d, want 404", tok, code)
		}
	}
	ok := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, indexPlaylistFile, s.PlaybackToken)
	if code, _ := getStatus(t, ok); code != http.StatusOK {
		t.Errorf("correct token returned %d, want 200", code)
	}
}

// ─── Finding 6: record.mp4 status gate + cache headers ─────────────

func TestPlayback_RecordingOnlyServedOnceEnded(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "rec", "record": true})
	s := out.(map[string]any)["stream"].(*Stream)
	writeTestMP4(t, filepath.Join(streamDataDir(ctx, s.StoragePrefix), recordingFile), true)
	srv := newTestServer(t, app)

	url := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, recordingFile, s.PlaybackToken)
	// Mid-stream the file has no usable index; serving it (and letting
	// a cache keep it for an hour) is how v0.1 poisoned replays.
	if code, _ := getStatus(t, url); code != http.StatusNotFound {
		t.Errorf("mid-stream record.mp4 returned %d, want 404", code)
	}

	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	code, hdr := getStatus(t, url)
	if code != http.StatusOK {
		t.Fatalf("post-stop record.mp4 returned %d, want 200", code)
	}
	cc := hdr.Get("Cache-Control")
	if !strings.Contains(cc, "private") {
		t.Errorf("token-gated mp4 Cache-Control=%q, want private", cc)
	}
}

func TestPlayback_PublicStreamKeepsPublicCaching(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "pub", "visibility": "public"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "tsdata")
	srv := newTestServer(t, app)

	code, hdr := getStatus(t, fmt.Sprintf("%s/streams/%d/seg-00000.ts", srv.URL, s.ID))
	if code != http.StatusOK {
		t.Fatalf("public segment returned %d, want 200", code)
	}
	if cc := hdr.Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("public segment Cache-Control=%q", cc)
	}
}

// ─── Finding 4: signed, expiring URLs ──────────────────────────────

func TestSignedURL_HappyPathTamperAndExpiry(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "signed"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile, "#EXTM3U\n")
	srv := newTestServer(t, app)

	signed, err := app.toolSignedURL(ctx, map[string]any{
		"id": s.ID, "expires_in_seconds": 300,
	})
	if err != nil {
		t.Fatalf("streams_signed_url: %v", err)
	}
	raw := signed.(map[string]any)["url"].(string)
	for _, want := range []string{"exp=", "sig=", "t=" + s.PlaybackToken} {
		if !strings.Contains(raw, want) {
			t.Errorf("signed url %q missing %q", raw, want)
		}
	}
	if code, _ := getStatus(t, localize(t, srv, raw)); code != http.StatusOK {
		t.Errorf("valid signed url returned %d, want 200", code)
	}

	// Tampered signature.
	bad := strings.Replace(localize(t, srv, raw), "sig=", "sig=00", 1)
	if code, _ := getStatus(t, bad); code != http.StatusNotFound {
		t.Errorf("tampered signature returned %d, want 404", code)
	}

	// Expired: sign for a moment in the past.
	past := time.Now().Add(-time.Minute).Unix()
	expired := fmt.Sprintf("%s/streams/%d/%s?t=%s&exp=%d&sig=%s",
		srv.URL, s.ID, indexPlaylistFile, s.PlaybackToken, past,
		signPlayback(s.URLSigningSecret, s.ID, past))
	if code, _ := getStatus(t, expired); code != http.StatusNotFound {
		t.Errorf("expired signed url returned %d, want 404", code)
	}
}

func TestURLPolicy_RequireSignedRejectsBareToken(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "strict"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile, "#EXTM3U\n")
	srv := newTestServer(t, app)

	bare := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, indexPlaylistFile, s.PlaybackToken)
	if code, _ := getStatus(t, bare); code != http.StatusOK {
		t.Fatalf("bare token should still work by default, got %d", code)
	}

	if _, err := app.toolSetURLPolicy(ctx, map[string]any{
		"id": s.ID, "require_signed_urls": true,
	}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if code, _ := getStatus(t, bare); code != http.StatusNotFound {
		t.Errorf("bare token under require_signed_urls returned %d, want 404", code)
	}

	signed, err := app.toolSignedURL(ctx, map[string]any{"id": s.ID, "expires_in_seconds": 120})
	if err != nil {
		t.Fatalf("signed url: %v", err)
	}
	if code, _ := getStatus(t, localize(t, srv, signed.(map[string]any)["url"].(string))); code != http.StatusOK {
		t.Errorf("signed url under strict policy returned %d, want 200", code)
	}

	// Heartbeats obey the same policy.
	hb := fmt.Sprintf("%s/heartbeat/%d?t=%s", srv.URL, s.ID, s.PlaybackToken)
	if code, _ := getStatus(t, hb); code != http.StatusNotFound {
		t.Errorf("bare-token heartbeat under strict policy returned %d, want 404", code)
	}
}

func TestRotateKey_RotatePlaybackTokenRevokesOutstandingURLs(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "revoke"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile, "#EXTM3U\n")
	srv := newTestServer(t, app)

	signed, _ := app.toolSignedURL(ctx, map[string]any{"id": s.ID, "expires_in_seconds": 600})
	live := localize(t, srv, signed.(map[string]any)["url"].(string))
	if code, _ := getStatus(t, live); code != http.StatusOK {
		t.Fatalf("pre-rotation signed url returned %d", code)
	}

	rotated, err := app.toolRotateKey(ctx, map[string]any{
		"id": s.ID, "rotate_playback_token": true,
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	s2 := rotated.(map[string]any)["stream"].(*Stream)
	if s2.PlaybackToken == s.PlaybackToken {
		t.Error("playback_token not rotated")
	}
	if s2.URLSigningSecret == s.URLSigningSecret {
		t.Error("url_signing_secret not rotated")
	}
	if code, _ := getStatus(t, live); code != http.StatusNotFound {
		t.Errorf("URL outstanding across a rotation returned %d, want 404", code)
	}
}

// The signing secret is a server-side key — it must never appear in a
// tool result.
func TestStream_SigningSecretNeverSerialized(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "secret"})
	s := out.(map[string]any)["stream"].(*Stream)
	if s.URLSigningSecret == "" {
		t.Fatal("create should mint a signing secret")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), s.URLSigningSecret) {
		t.Error("url_signing_secret leaked into the tool result JSON")
	}
}

// ─── Manifest credential propagation ───────────────────────────────
//
// Players resolve a playlist's relative "seg-00000.ts" lines against
// the manifest URL WITHOUT its query, so gated HLS only works if the
// server rewrites the URIs. (Not in the original finding list — found
// while wiring signed URLs; without it every signed HLS session 404s
// on its first segment.)

func TestPlayback_ManifestSegmentsInheritCredentials(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "propagate"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile,
		"#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.000,\nseg-00000.ts\n")
	writeStreamFile(t, ctx, s, "seg-00000.ts", "tsdata")
	srv := newTestServer(t, app)

	signed, err := app.toolSignedURL(ctx, map[string]any{"id": s.ID, "expires_in_seconds": 300})
	if err != nil {
		t.Fatal(err)
	}
	manifestURL := localize(t, srv, signed.(map[string]any)["url"].(string))

	resp, err := http.Get(manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest returned %d", resp.StatusCode)
	}

	segLine := ""
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "seg-") {
			segLine = strings.TrimSpace(line)
		}
	}
	if segLine == "" {
		t.Fatalf("no segment line in manifest:\n%s", body)
	}
	if !strings.Contains(segLine, "sig=") || !strings.Contains(segLine, "t=") {
		t.Fatalf("segment URI lost the request's credentials: %q", segLine)
	}

	// The URI a player would actually request must be servable.
	base := manifestURL[:strings.LastIndex(manifestURL, "/")+1]
	if code, _ := getStatus(t, base+segLine); code != http.StatusOK {
		t.Errorf("segment as advertised by the manifest returned %d, want 200", code)
	}
}

func TestRewriteManifestQuery_LeavesTagsAndAbsoluteURIsAlone(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-TARGETDURATION:4\nseg-00000.ts\nhttps://cdn.example/seg-1.ts\nseg-2.ts?x=1\n"
	got := string(rewriteManifestQuery([]byte(in), "t=abc"))
	if !strings.Contains(got, "seg-00000.ts?t=abc") {
		t.Errorf("relative segment not rewritten:\n%s", got)
	}
	if strings.Contains(got, "https://cdn.example/seg-1.ts?t=abc") {
		t.Errorf("absolute URI rewritten:\n%s", got)
	}
	if strings.Contains(got, "seg-2.ts?x=1?t=abc") {
		t.Errorf("URI that already had a query was mangled:\n%s", got)
	}
	if !strings.Contains(got, "#EXT-X-TARGETDURATION:4\n") {
		t.Errorf("tag line mangled:\n%s", got)
	}
	if same := string(rewriteManifestQuery([]byte(in), "")); same != in {
		t.Error("no query should mean no rewrite")
	}
}

// ─── Finding 20: playback record cache ─────────────────────────────

func TestPlaybackCache_ServesFromCacheAndInvalidates(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "cached"})
	s := out.(map[string]any)["stream"].(*Stream)

	if _, err := app.playbackFor(ctx, s.ProjectID, s.ID); err != nil {
		t.Fatal(err)
	}
	// Mutate behind the cache's back.
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET playback_token = 'changed' WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	rec, _ := app.playbackFor(ctx, s.ProjectID, s.ID)
	if rec.PlaybackToken != s.PlaybackToken {
		t.Error("second lookup hit the DB — the record isn't actually cached")
	}
	app.invalidatePlayback(s.ProjectID, s.ID)
	rec, _ = app.playbackFor(ctx, s.ProjectID, s.ID)
	if rec.PlaybackToken != "changed" {
		t.Errorf("post-invalidate token=%q, want the new value", rec.PlaybackToken)
	}
}

// ─── Finding 10: admin POST with a null body ───────────────────────

func TestAdminStreams_NullBodyDoesNotPanic(t *testing.T) {
	app, _ := newTestApp(t)
	srv := newTestServer(t, app)

	resp, err := http.Post(srv.URL+"/admin/streams", "application/json", strings.NewReader("null"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	// `null` decodes to a nil map; v0.1 then assigned into it and
	// panicked. Any clean HTTP answer is a pass.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 (name required)", resp.StatusCode)
	}
}

func TestAdminStreams_CreateStillWorks(t *testing.T) {
	app, _ := newTestApp(t)
	srv := newTestServer(t, app)
	resp, err := http.Post(srv.URL+"/admin/streams", "application/json",
		strings.NewReader(`{"name":"via-admin"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["stream"] == nil {
		t.Errorf("expected a stream in the response, got %v", body)
	}
}
