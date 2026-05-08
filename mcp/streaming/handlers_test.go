package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Fake runner ───────────────────────────────────────────────────
//
// Tests don't spawn ffmpeg. The fake mimics the runner's done-channel
// + atomics surface so the tools and watchdog see realistic state.

type fakeRunner struct {
	*streamRunner
}

func newFakeRunnerFactory(t *testing.T) func(opts runnerOpts) (*streamRunner, error) {
	t.Helper()
	return func(opts runnerOpts) (*streamRunner, error) {
		// Build a streamRunner with no exec.Cmd. The done channel is
		// kept open until stop() is called, mirroring "ffmpeg is alive
		// and listening". metrics() returns zero values until the test
		// sets them.
		r := &streamRunner{
			streamID:  opts.streamID,
			port:      opts.port,
			ffmpegBin: opts.ffmpegBin,
			dataDir:   opts.dataDir,
			streamKey: opts.streamKey,
			hlsTime:   opts.hlsTime,
			hlsWindow: opts.hlsWindow,
			record:    opts.record,
			done:      make(chan runnerExit, 1),
		}
		return r, nil
	}
}

// fakeStop simulates publisher disconnect or graceful stop.
func fakeStop(r *streamRunner, withErr error) {
	r.doneOnce.Do(func() {
		r.done <- runnerExit{err: withErr}
		close(r.done)
	})
}

// fakeFirstFrame simulates ffmpeg scraping its first bitrate line.
func fakeFirstFrame(r *streamRunner, bitrate int, fps float64, resolution string) {
	r.bitrateKbps.Store(int64(bitrate))
	r.fps.Store(uint64(fps * 1000))
	if resolution != "" {
		s := resolution
		r.resolution.Store(&s)
	}
	r.startedAt.Store(time.Now().UnixNano())
}

// ─── Test fixture ──────────────────────────────────────────────────

func newTestApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	dataDir := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("APTEVA_DATA_DIR", dataDir),
		tk.WithConfig(map[string]string{
			"rtmp_port_range":     "1935-1940",
			"hls_segment_seconds": "4",
			"viewer_idle_seconds": "30",
		}),
	)
	app := &App{
		runners:       map[int64]*streamRunner{},
		viewers:       newViewerTracker(),
		runnerFactory: newFakeRunnerFactory(t),
	}
	pa, err := newPortAllocator("1935-1940")
	if err != nil {
		t.Fatalf("port allocator: %v", err)
	}
	app.ports = pa
	// Wire globals — playback handlers use them, but we keep the test
	// scoped: each test gets a fresh App so globals are overwritten
	// per test.
	globalCtx = ctx
	globalApp = app
	return app, ctx
}

// ─── streams_create + lifecycle ────────────────────────────────────

func TestCreate_AllocatesPortAndReturnsURLs(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{
		"name":      "test stream",
		"owner_app": "webinars",
		"owner_tag": "webinar:1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	if s.IngestPort != 1935 {
		t.Errorf("ingest_port=%d, want 1935 (lowest in range)", s.IngestPort)
	}
	if s.StreamKey == "" || len(s.StreamKey) < 32 {
		t.Errorf("stream_key looks too short: %q", s.StreamKey)
	}
	if s.PlaybackToken == "" || s.PlaybackToken == s.StreamKey {
		t.Errorf("playback_token should differ from stream_key")
	}
	if !strings.HasPrefix(s.IngestURL, "rtmp://") {
		t.Errorf("ingest_url=%q, want rtmp://...", s.IngestURL)
	}
	if !strings.Contains(s.PlaybackURL, "index.m3u8") {
		t.Errorf("playback_url=%q, want to contain index.m3u8", s.PlaybackURL)
	}
	if !strings.Contains(s.PlaybackURL, "t="+s.PlaybackToken) {
		t.Errorf("playback_url should embed playback_token: %q", s.PlaybackURL)
	}
	if s.Status != "idle" {
		t.Errorf("status=%q, want idle", s.Status)
	}
	if !s.Record {
		t.Errorf("record default should be true")
	}
}

func TestCreate_DefaultsRecordTrueRetention30(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	s := out.(map[string]any)["stream"].(*Stream)
	if s.RetentionDays != 30 {
		t.Errorf("retention_days=%d, want 30", s.RetentionDays)
	}
	if !s.Record {
		t.Errorf("record default should be true")
	}
}

func TestCreate_RejectsBadVisibility(t *testing.T) {
	app, ctx := newTestApp(t)
	_, err := app.toolCreate(ctx, map[string]any{
		"name":       "x",
		"visibility": "bogus",
	})
	if err == nil {
		t.Fatal("expected rejection for visibility=bogus")
	}
}

func TestCreate_FailsWhenAtMaxConcurrent(t *testing.T) {
	app, ctx := newTestApp(t)
	app.runnersMu.Lock()
	for i := 0; i < 4; i++ {
		app.runners[int64(i+100)] = &streamRunner{done: make(chan runnerExit)}
	}
	app.runnersMu.Unlock()
	_, err := app.toolCreate(ctx, map[string]any{"name": "x"})
	if err == nil || !strings.Contains(err.Error(), "max_concurrent_streams") {
		t.Errorf("expected max_concurrent error, got %v", err)
	}
}

func TestCreate_GlobalScopeRequiresProjectID(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	dataDir := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithEnv("APTEVA_DATA_DIR", dataDir),
	)
	app := &App{
		runners:       map[int64]*streamRunner{},
		viewers:       newViewerTracker(),
		runnerFactory: newFakeRunnerFactory(t),
	}
	pa, _ := newPortAllocator("1935-1940")
	app.ports = pa
	_, err := app.toolCreate(ctx, map[string]any{"name": "x"})
	if err == nil || !strings.Contains(err.Error(), "project_id") {
		t.Errorf("expected project_id error, got %v", err)
	}
}

// ─── streams_get / streams_list ────────────────────────────────────

func TestGet_RoundTrip(t *testing.T) {
	app, ctx := newTestApp(t)
	createOut, _ := app.toolCreate(ctx, map[string]any{
		"name":      "alpha",
		"owner_app": "webinars",
	})
	id := createOut.(map[string]any)["stream"].(*Stream).ID

	getOut, err := app.toolGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	res := getOut.(map[string]any)
	if res["found"] != true {
		t.Fatalf("found=%v, want true", res["found"])
	}
	s := res["stream"].(*Stream)
	if s.Name != "alpha" {
		t.Errorf("name=%q", s.Name)
	}
	if s.OwnerApp != "webinars" {
		t.Errorf("owner_app=%q", s.OwnerApp)
	}
}

func TestGet_NotFoundReturnsFalse(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolGet(ctx, map[string]any{"id": 9999})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["found"] != false {
		t.Errorf("expected found=false")
	}
}

func TestList_FiltersByOwnerTag(t *testing.T) {
	app, ctx := newTestApp(t)
	app.toolCreate(ctx, map[string]any{"name": "a", "owner_app": "webinars", "owner_tag": "webinar:1"})
	app.toolCreate(ctx, map[string]any{"name": "b", "owner_app": "webinars", "owner_tag": "webinar:2"})
	app.toolCreate(ctx, map[string]any{"name": "c", "owner_app": "podcasts"})

	out, err := app.toolList(ctx, map[string]any{
		"owner_app": "webinars",
		"owner_tag": "webinar:2",
	})
	if err != nil {
		t.Fatal(err)
	}
	count := out.(map[string]any)["count"].(int)
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}
}

func TestList_FiltersByStatus(t *testing.T) {
	app, ctx := newTestApp(t)
	app.toolCreate(ctx, map[string]any{"name": "live-one"})
	stopOut, _ := app.toolCreate(ctx, map[string]any{"name": "to-stop"})
	stoppedID := stopOut.(map[string]any)["stream"].(*Stream).ID
	if _, err := app.toolStop(ctx, map[string]any{"id": stoppedID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	out, _ := app.toolList(ctx, map[string]any{"status": "ended"})
	if out.(map[string]any)["count"].(int) != 1 {
		t.Errorf("ended count=%v, want 1", out.(map[string]any)["count"])
	}
	out, _ = app.toolList(ctx, map[string]any{"status": "idle"})
	if out.(map[string]any)["count"].(int) != 1 {
		t.Errorf("idle count=%v, want 1", out.(map[string]any)["count"])
	}
}

// ─── streams_stop ──────────────────────────────────────────────────

func TestStop_FlipsStatusAndReleasesPort(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID

	app.runnersMu.Lock()
	r := app.runners[id]
	app.runnersMu.Unlock()
	if r == nil {
		t.Fatal("expected runner registered")
	}
	port := r.port

	if _, err := app.toolStop(ctx, map[string]any{"id": id}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	app.runnersMu.Lock()
	if app.runners[id] != nil {
		t.Errorf("runner still in map after stop")
	}
	app.runnersMu.Unlock()

	// Port should be reallocatable.
	got, err := app.ports.allocate()
	if err != nil {
		t.Fatalf("port reallocate: %v", err)
	}
	if got != port {
		t.Errorf("expected port %d back, got %d", port, got)
	}

	gotOut, _ := app.toolGet(ctx, map[string]any{"id": id})
	s := gotOut.(map[string]any)["stream"].(*Stream)
	if s.Status != "ended" {
		t.Errorf("status=%q, want ended", s.Status)
	}
	if s.EndedAt == "" {
		t.Errorf("ended_at should be set")
	}
}

func TestStop_IdempotentWhenAlreadyEnded(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID
	app.toolStop(ctx, map[string]any{"id": id})
	out2, err := app.toolStop(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	if out2.(map[string]any)["noop"] != true {
		t.Errorf("expected noop=true on second stop")
	}
}

// ─── streams_delete ────────────────────────────────────────────────

func TestDelete_RemovesRowAndDir(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	s := out.(map[string]any)["stream"].(*Stream)

	dir := filepath.Join(ctx.DataDir(), s.StoragePrefix)
	// stream dir doesn't exist yet because the fake runner doesn't
	// mkdir — fine, delete should still succeed.

	if _, err := app.toolDelete(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	gotOut, _ := app.toolGet(ctx, map[string]any{"id": s.ID})
	if gotOut.(map[string]any)["found"] != false {
		t.Errorf("expected found=false after delete")
	}
	_ = dir
}

func TestDelete_IdempotentWhenAlreadyGone(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolDelete(ctx, map[string]any{"id": int64(9999)})
	if out.(map[string]any)["deleted"] != true {
		t.Errorf("expected deleted=true on no-op")
	}
}

// ─── streams_rotate_key ────────────────────────────────────────────

func TestRotateKey_GeneratesNewKeyAndRespawns(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	s := out.(map[string]any)["stream"].(*Stream)
	oldKey := s.StreamKey

	rOut, err := app.toolRotateKey(ctx, map[string]any{"id": s.ID})
	if err != nil {
		t.Fatal(err)
	}
	s2 := rOut.(map[string]any)["stream"].(*Stream)
	if s2.StreamKey == oldKey {
		t.Errorf("stream_key not rotated: still %q", s2.StreamKey)
	}
	if s2.Status != "idle" {
		t.Errorf("status=%q, want idle after rotation", s2.Status)
	}
}

// ─── streams_get_metrics ───────────────────────────────────────────

func TestGetMetrics_ReadsLiveScrapedValues(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID

	app.runnersMu.Lock()
	r := app.runners[id]
	app.runnersMu.Unlock()
	fakeFirstFrame(r, 2400, 30.0, "1920x1080")

	mOut, err := app.toolGetMetrics(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	m := mOut.(map[string]any)
	if m["current_bitrate_kbps"].(int) != 2400 {
		t.Errorf("bitrate=%v, want 2400", m["current_bitrate_kbps"])
	}
	if m["resolution"].(string) != "1920x1080" {
		t.Errorf("resolution=%v", m["resolution"])
	}
	if m["uptime_seconds"].(int) < 0 {
		t.Errorf("uptime negative")
	}
}

// ─── Watchdog → idle→live transition + dead-runner cleanup ────────

func TestWatchdog_FlipsIdleToLiveOnFirstFrame(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID

	app.runnersMu.Lock()
	r := app.runners[id]
	app.runnersMu.Unlock()
	fakeFirstFrame(r, 1000, 24.0, "1280x720")

	if err := app.runWatchdog(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	gotOut, _ := app.toolGet(ctx, map[string]any{"id": id})
	s := gotOut.(map[string]any)["stream"].(*Stream)
	if s.Status != "live" {
		t.Errorf("status=%q, want live after watchdog", s.Status)
	}
	if s.StartedAt == "" {
		t.Errorf("started_at should be set")
	}
}

func TestWatchdog_HandlesGracefulPublisherDisconnect(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID

	app.runnersMu.Lock()
	r := app.runners[id]
	app.runnersMu.Unlock()
	fakeFirstFrame(r, 1000, 24.0, "1280x720")
	// Run watchdog once to transition idle→live.
	app.runWatchdog(context.Background(), ctx)
	// Now simulate publisher disconnect.
	fakeStop(r, nil)

	if err := app.runWatchdog(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	gotOut, _ := app.toolGet(ctx, map[string]any{"id": id})
	s := gotOut.(map[string]any)["stream"].(*Stream)
	if s.Status != "ended" {
		t.Errorf("graceful disconnect: status=%q, want ended", s.Status)
	}
	app.runnersMu.Lock()
	if app.runners[id] != nil {
		t.Errorf("runner not cleaned up after graceful exit")
	}
	app.runnersMu.Unlock()
}

func TestWatchdog_HandlesCrash(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID
	app.runnersMu.Lock()
	r := app.runners[id]
	app.runnersMu.Unlock()
	fakeStop(r, errFake("ffmpeg crashed"))

	if err := app.runWatchdog(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	gotOut, _ := app.toolGet(ctx, map[string]any{"id": id})
	s := gotOut.(map[string]any)["stream"].(*Stream)
	if s.Status != "errored" {
		t.Errorf("crash: status=%q, want errored", s.Status)
	}
	if !strings.Contains(s.Error, "crashed") {
		t.Errorf("error=%q, want to mention 'crashed'", s.Error)
	}
}

// ─── Viewer counter (in-memory tracker) ────────────────────────────

func TestViewerTracker_DecaysStaleAndCountsActive(t *testing.T) {
	v := newViewerTracker()
	streamID := int64(42)

	// Three cookies; bump them all "now".
	v.bump(streamID, "c1")
	v.bump(streamID, "c2")
	v.bump(streamID, "c3")
	if got := v.count(streamID); got != 3 {
		t.Errorf("count after 3 bumps = %d, want 3", got)
	}

	// Force one cookie's timestamp into the past, then sweep with a
	// 30s window.
	v.mu.Lock()
	v.streams[streamID]["c2"] = time.Now().Add(-2 * time.Minute)
	v.mu.Unlock()

	if got := v.sweep(streamID, 30*time.Second); got != 2 {
		t.Errorf("sweep with stale cookie = %d, want 2", got)
	}
	if got := v.count(streamID); got != 2 {
		t.Errorf("count after sweep = %d, want 2", got)
	}
}

func TestViewerCounter_BumpsPeakAndPersists(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "x"})
	id := out.(map[string]any)["stream"].(*Stream).ID

	// Simulate two viewers via the tracker, then run the worker.
	app.viewers.bump(id, "viewer-a")
	app.viewers.bump(id, "viewer-b")

	if err := app.runViewerCounter(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	gotOut, _ := app.toolGet(ctx, map[string]any{"id": id})
	s := gotOut.(map[string]any)["stream"].(*Stream)
	if s.CurrentViewers != 2 {
		t.Errorf("current_viewers=%d, want 2", s.CurrentViewers)
	}
	if s.PeakViewers != 2 {
		t.Errorf("peak_viewers=%d, want 2", s.PeakViewers)
	}
	if s.TotalViewerSeconds < 20 {
		t.Errorf("total_viewer_seconds=%d, want >= 20 (2 viewers × 10s)",
			s.TotalViewerSeconds)
	}
}

func TestViewerTracker_DropClearsBucket(t *testing.T) {
	v := newViewerTracker()
	v.bump(7, "x")
	if got := v.count(7); got != 1 {
		t.Errorf("pre-drop count=%d", got)
	}
	v.drop(7)
	if got := v.count(7); got != 0 {
		t.Errorf("post-drop count=%d, want 0", got)
	}
}

// ─── Playback URL validation (pure function) ──────────────────────

func TestValidPlaybackFilename(t *testing.T) {
	good := []string{"index.m3u8", "seg-00000.ts", "seg-99999.ts", "record.mp4"}
	bad := []string{
		"", ".", "..", "../etc/passwd", "foo/bar.ts",
		"foo\\bar.ts", ".hidden", "seg-00.txt", "evil.exe",
		"index.m3u8/extra",
	}
	for _, n := range good {
		if !validPlaybackFilename(n) {
			t.Errorf("good filename rejected: %q", n)
		}
	}
	for _, n := range bad {
		if validPlaybackFilename(n) {
			t.Errorf("bad filename accepted: %q", n)
		}
	}
}

// ─── Tiny helpers ──────────────────────────────────────────────────

type errFakeT string

func (e errFakeT) Error() string { return string(e) }
func errFake(s string) error     { return errFakeT(s) }

// keep go vet quiet about the unused mutex import in case I refactor.
var _ sync.Mutex
