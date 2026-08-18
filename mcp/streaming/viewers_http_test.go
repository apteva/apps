package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── Finding 17: viewer identity + anti-spoof throttle ─────────────

func beat(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// beatFrom sends a heartbeat as apteva-server's reverse proxy would:
// loopback peer, real client address in X-Forwarded-For.
func beatFrom(t *testing.T, url, ip string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Playback deliberately allows cross-origin embedding, and a
// SameSite=Lax cookie is not sent on those requests — so v0.1 minted a
// fresh random id on every beat and one viewer counted as many. The
// documented ?v= fallback (README) was never read by the handler.
func TestHeartbeat_VParamKeepsOneViewerCountedOnce(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "hb"})
	s := out.(map[string]any)["stream"].(*Stream)
	srv := newTestServer(t, app)

	url := fmt.Sprintf("%s/heartbeat/%d?t=%s&v=viewer-abc", srv.URL, s.ID, s.PlaybackToken)
	for i := 0; i < 5; i++ {
		if code := beat(t, url); code != http.StatusOK {
			t.Fatalf("beat %d returned %d", i, code)
		}
	}
	if got := app.viewers.count(s.ID); got != 1 {
		t.Errorf("current viewers=%d after 5 beats from one client, want 1", got)
	}
}

func TestHeartbeat_RejectsUnusableVParam(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "hb"})
	s := out.(map[string]any)["stream"].(*Stream)
	srv := newTestServer(t, app)

	// A junk id falls back to a generated one rather than being
	// trusted as-is.
	url := fmt.Sprintf("%s/heartbeat/%d?t=%s&v=%s", srv.URL, s.ID, s.PlaybackToken, "bad%20id%21")
	if code := beat(t, url); code != http.StatusOK {
		t.Fatalf("beat returned %d", code)
	}
	if !validViewerID("abc-123_XYZ") {
		t.Error("a normal opaque id should be accepted")
	}
	for _, bad := range []string{"", "has space", "semi;colon", string(make([]byte, 100))} {
		if validViewerID(bad) {
			t.Errorf("bad viewer id accepted: %q", bad)
		}
	}
}

// A client looping beats with rotating identities must not be able to
// drive the headcount arbitrarily high.
func TestHeartbeat_ThrottleCapsIdentitiesPerIP(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "spoof"})
	s := out.(map[string]any)["stream"].(*Stream)
	srv := newTestServer(t, app)

	for i := 0; i < throttleMaxIdentities+12; i++ {
		url := fmt.Sprintf("%s/heartbeat/%d?t=%s&v=spoof-%d", srv.URL, s.ID, s.PlaybackToken, i)
		if code := beatFrom(t, url, "203.0.113.9"); code != http.StatusOK {
			t.Fatalf("beat %d returned %d", i, code)
		}
	}
	got := app.viewers.count(s.ID)
	if got > throttleMaxIdentities+1 {
		t.Errorf("counted %d viewers from one IP, cap is %d(+1 synthetic)",
			got, throttleMaxIdentities)
	}
}

func TestHeartbeat_RateLimited(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "flood"})
	s := out.(map[string]any)["stream"].(*Stream)
	srv := newTestServer(t, app)

	url := fmt.Sprintf("%s/heartbeat/%d?t=%s&v=one", srv.URL, s.ID, s.PlaybackToken)
	limited := false
	for i := 0; i < throttleMaxBeats+5; i++ {
		if beatFrom(t, url, "203.0.113.9") == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Errorf("no 429 after %d beats from one IP", throttleMaxBeats+5)
	}

	// A different client is unaffected by its neighbour's flood.
	if code := beatFrom(t, url, "203.0.113.10"); code != http.StatusOK {
		t.Errorf("second client returned %d — the limit isn't per-IP", code)
	}
	// And when the platform forwards no client address at all we fail
	// open rather than rate-limiting an entire audience as one client.
	if code := beat(t, url); code != http.StatusOK {
		t.Errorf("unattributable request returned %d, want 200 (fail open)", code)
	}
}

func TestClientIP_TrustsForwardedHeaderOnlyFromLoopback(t *testing.T) {
	proxied := httptest.NewRequest(http.MethodGet, "/heartbeat/1", nil)
	proxied.RemoteAddr = "127.0.0.1:5555"
	proxied.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := clientIP(proxied); got != "203.0.113.5" {
		t.Errorf("proxied clientIP=%q, want the forwarded client", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "/heartbeat/1", nil)
	direct.RemoteAddr = "198.51.100.7:5555"
	direct.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(direct); got != "198.51.100.7" {
		t.Errorf("direct clientIP=%q — a non-proxy peer must not be able to spoof its IP", got)
	}

	// Loopback peer, no forwarded address: every viewer would land in
	// one bucket, so report "unattributable" and let the throttle fail
	// open instead of 429ing a whole audience.
	unattributable := httptest.NewRequest(http.MethodGet, "/heartbeat/1", nil)
	unattributable.RemoteAddr = "127.0.0.1:5555"
	if got := clientIP(unattributable); got != "" {
		t.Errorf("clientIP=%q for an unattributable request, want \"\"", got)
	}
}

// ─── Finding 18: no heartbeats for finished streams ────────────────

func TestHeartbeat_RefusedOnceTheStreamEnded(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "over"})
	s := out.(map[string]any)["stream"].(*Stream)
	srv := newTestServer(t, app)
	url := fmt.Sprintf("%s/heartbeat/%d?t=%s&v=v1", srv.URL, s.ID, s.PlaybackToken)

	if code := beat(t, url); code != http.StatusOK {
		t.Fatalf("live beat returned %d", code)
	}
	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	if code := beat(t, url); code != http.StatusGone {
		t.Errorf("beat on an ended stream returned %d, want 410", code)
	}
	if got := app.viewers.count(s.ID); got != 0 {
		t.Errorf("ended stream still tracking %d viewers", got)
	}
}

func TestViewerCounter_StopsAccruingOnTerminalStreams(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "acc"})
	s := out.(map[string]any)["stream"].(*Stream)

	app.viewers.bump(s.ID, "a")
	app.viewers.bump(s.ID, "b")
	if err := app.runViewerCounter(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	live := getStream(t, app, ctx, s.ID)
	if live.TotalViewerSeconds != 20 {
		t.Fatalf("total_viewer_seconds=%d, want 20", live.TotalViewerSeconds)
	}

	app.toolStop(ctx, map[string]any{"id": s.ID})
	// Simulate a replay viewer that slipped a beat past the handler.
	app.viewers.bump(s.ID, "a")
	if err := app.runViewerCounter(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	after := getStream(t, app, ctx, s.ID)
	if after.TotalViewerSeconds != live.TotalViewerSeconds {
		t.Errorf("total_viewer_seconds grew after the stream ended: %d → %d",
			live.TotalViewerSeconds, after.TotalViewerSeconds)
	}
	if after.CurrentViewers != 0 {
		t.Errorf("current_viewers=%d on an ended stream", after.CurrentViewers)
	}
}

// ─── Signed heartbeat URLs ─────────────────────────────────────────

// The heartbeat endpoint runs the same playbackAuthorized gate as the
// media routes, so flipping require_signed_urls breaks a consumer that
// can only build a bare ?t= heartbeat — playback keeps working while
// viewer counts silently fall to zero. streams_signed_url therefore has
// to be able to sign the heartbeat endpoint too, not just hls/mp4.
//
// This is the webinars-app integration contract: its live room posts
// heartbeats for streams it has itself put under require_signed_urls.
func TestSignedURL_HeartbeatKindPassesThePolicyGate(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "hb-signed"})
	s := out.(map[string]any)["stream"].(*Stream)
	srv := newTestServer(t, app)

	if _, err := app.toolSetURLPolicy(ctx, map[string]any{
		"id": s.ID, "require_signed_urls": true,
	}); err != nil {
		t.Fatalf("set url policy: %v", err)
	}

	// A bare token must now be rejected...
	bare := fmt.Sprintf("%s/heartbeat/%d?t=%s&v=viewer-1", srv.URL, s.ID, s.PlaybackToken)
	if code := beat(t, bare); code == http.StatusOK {
		t.Fatalf("bare ?t= heartbeat returned 200 under require_signed_urls")
	}

	// ...while a signed heartbeat URL still works.
	signed, err := app.toolSignedURL(ctx, map[string]any{
		"id": s.ID, "expires_in_seconds": 3600, "kind": "heartbeat",
	})
	if err != nil {
		t.Fatalf("signed url (heartbeat): %v", err)
	}
	raw := signed.(map[string]any)["url"].(string)
	if !strings.Contains(raw, "/heartbeat/") {
		t.Fatalf("kind=heartbeat returned a non-heartbeat URL: %s", raw)
	}
	if !strings.Contains(raw, "sig=") || !strings.Contains(raw, "exp=") {
		t.Fatalf("heartbeat URL is not signed: %s", raw)
	}

	// The tool returns a public-base URL; retarget it at the test server.
	idx := strings.Index(raw, "/heartbeat/")
	if code := beat(t, srv.URL+raw[idx:]+"&v=viewer-1"); code != http.StatusOK {
		t.Fatalf("signed heartbeat returned %d, want 200", code)
	}
	if got := app.viewers.count(s.ID); got != 1 {
		t.Errorf("current viewers=%d after a signed beat, want 1", got)
	}
}
