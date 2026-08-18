package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ─── Finding 13: the load generator only ever targets loopback ─────

func TestAssertLoopback(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:8080/streams/1/index.m3u8",
		"http://localhost:8080/streams/1/index.m3u8",
		"http://[::1]:8080/streams/1/index.m3u8",
	}
	for _, u := range ok {
		if err := assertLoopback(u); err != nil {
			t.Errorf("assertLoopback(%q) = %v, want nil", u, err)
		}
	}
	// This is the production self-DoS v0.1 shipped: PUBLIC_URL set →
	// 2000 goroutines × 300s against the public host, through
	// apteva-server's proxy.
	bad := []string{
		"https://agents.example.com/api/apps/streaming/streams/1/index.m3u8",
		"http://10.0.0.5:8080/streams/1/index.m3u8",
	}
	for _, u := range bad {
		if err := assertLoopback(u); err == nil {
			t.Errorf("assertLoopback(%q) accepted a non-loopback target", u)
		}
	}
}

func TestLoopbackPlaybackURL_TargetsTheSidecarsOwnRoute(t *testing.T) {
	t.Setenv("APTEVA_LISTEN_PORT", "9123")
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "lt"})
	s := out.(map[string]any)["stream"].(*Stream)

	got := app.loopbackPlaybackURL(s, indexPlaylistFile)
	want := fmt.Sprintf("http://127.0.0.1:9123/streams/%d/%s", s.ID, indexPlaylistFile)
	if !strings.HasPrefix(got, want) {
		t.Errorf("loopbackPlaybackURL=%q, want prefix %q", got, want)
	}
	// v0.1 pointed at the proxy's path (/api/apps/streaming/...), which
	// the sidecar itself doesn't serve — every request 404'd.
	if strings.Contains(got, "/api/apps/streaming") {
		t.Errorf("target must be the sidecar's own route, got %q", got)
	}
	if !strings.Contains(got, "t="+s.PlaybackToken) {
		t.Errorf("target lost the playback token: %q", got)
	}
	if err := assertLoopback(got); err != nil {
		t.Errorf("generated target isn't loopback: %v", err)
	}
}

// ─── Finding 14: classification + error propagation ────────────────

func TestClassifyResponse_EveryNon2xxIsAFailure(t *testing.T) {
	cases := []struct {
		code       int
		isManifest bool
		served     bool
	}{
		{200, true, true},
		{206, false, true},
		{400, true, false},  // v0.1 counted this as success
		{403, false, false}, // ditto
		{404, false, false},
		{404, true, false},
		{500, true, false},
		{503, false, false},
	}
	for _, c := range cases {
		cnt := newLoadCounters()
		got := classifyResponse(cnt, c.code, c.isManifest)
		if got != c.served {
			t.Errorf("classifyResponse(%d) served=%v, want %v", c.code, got, c.served)
		}
		wantFailures := int64(0)
		if !c.served {
			wantFailures = 1
		}
		if n := cnt.failures.Load(); n != wantFailures {
			t.Errorf("classifyResponse(%d) failures=%d, want %d", c.code, n, wantFailures)
		}
	}

	// 5xx and 404 keep their dedicated counters.
	cnt := newLoadCounters()
	classifyResponse(cnt, 503, false)
	if cnt.fivexx.Load() != 1 {
		t.Error("503 not counted in http_5xx")
	}
	classifyResponse(cnt, 404, false)
	if cnt.late.Load() != 1 {
		t.Error("missing segment not counted as late")
	}
	classifyResponse(cnt, 404, true)
	if cnt.refusals.Load() != 1 {
		t.Error("missing manifest not counted as a refusal")
	}
}

// A probe that doesn't answer must be an error, not an all-zero
// result that reads like "ran fine, served nothing".
func TestRunLoadTest_PropagatesProbeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	res, err := runLoadTest(nil, srv.URL+"/streams/1/index.m3u8", 2, 1)
	if err == nil {
		t.Fatalf("expected an error for a 404 manifest, got result %+v", res)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should name the status, got %q", err)
	}
}

func TestRunLoadTest_RefusesRemoteTargets(t *testing.T) {
	if _, err := runLoadTest(nil, "https://agents.example.com/streams/1/index.m3u8", 1, 1); err == nil {
		t.Fatal("load test against a public host should be refused")
	}
}

// End-to-end against the app's own handlers: this is what the tool
// actually drives, and it's the only test that catches the
// interaction between the server's manifest rewriting and the load
// generator's segment-URL construction (double-appending the auth
// query would 404 every segment).
func TestRunLoadTest_AgainstTheRealPlaybackHandlers(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "lt-e2e"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, indexPlaylistFile,
		"#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.000,\nseg-00000.ts\n")
	writeStreamFile(t, ctx, s, "seg-00000.ts", strings.Repeat("x", 2048))
	srv := newTestServer(t, app)

	target := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, indexPlaylistFile, s.PlaybackToken)
	res, err := runLoadTest(ctx, target, 2, 1)
	if err != nil {
		t.Fatalf("load test: %v", err)
	}
	if res.SegmentRequests == 0 {
		t.Errorf("no segments fetched — segment URLs the server advertised weren't usable: %v",
			res.StatusBreakdown)
	}
	if res.Failures != 0 {
		t.Errorf("failures=%d against a healthy sidecar: %v", res.Failures, res.StatusBreakdown)
	}
}

func TestRunLoadTest_ReportsServedBytesAndStatuses(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.000,\nseg-00000.ts\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte(manifest))
		case strings.HasSuffix(r.URL.Path, ".ts"):
			_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res, err := runLoadTest(nil, srv.URL+"/streams/1/index.m3u8?t=tok", 2, 1)
	if err != nil {
		t.Fatalf("load test: %v", err)
	}
	if res.ManifestRequests == 0 {
		t.Error("no manifest requests recorded")
	}
	if res.SegmentRequests == 0 {
		t.Error("no segment requests recorded")
	}
	if res.BytesServed == 0 {
		t.Error("no bytes recorded")
	}
	if res.Failures != 0 {
		t.Errorf("failures=%d on a healthy server: %v", res.Failures, res.StatusBreakdown)
	}
	if res.StatusBreakdown[strconv.Itoa(http.StatusOK)] == 0 {
		t.Errorf("status breakdown missing 200s: %v", res.StatusBreakdown)
	}
}
