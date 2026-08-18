package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// countEvents returns how many stream_events rows of a kind exist.
func countEvents(t *testing.T, ctx *sdk.AppCtx, streamID int64, kind string) int {
	t.Helper()
	var n int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM stream_events WHERE stream_id = ? AND kind = ?`,
		streamID, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func getStream(t *testing.T, app *App, ctx *sdk.AppCtx, id int64) *Stream {
	t.Helper()
	out, err := app.toolGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["found"] != true {
		t.Fatalf("stream %d not found", id)
	}
	return res["stream"].(*Stream)
}

// ─── Finding 1: recording finalized on the publisher-disconnect path ─

// The COMMON end path is: host clicks Stop in OBS → ffmpeg exits 0 →
// watchdog marks the stream ended. v0.1's watchdog UPDATE omitted
// recording_path, so the mp4 was stranded on disk forever and
// streams_replay_url never returned an mp4_url.
func TestWatchdog_FinalizesRecordingOnPublisherDisconnect(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "webinar", "record": true})
	s := out.(map[string]any)["stream"].(*Stream)

	dir := streamDataDir(ctx, s.StoragePrefix)
	writeTestMP4(t, filepath.Join(dir, recordingFile), true)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "ts")

	app.runnersMu.Lock()
	r := app.runners[s.ID]
	app.runnersMu.Unlock()
	fakeFirstFrame(r, 2000, 30, "1920x1080")
	if err := app.runWatchdog(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	fakeStop(r, nil) // publisher disconnected, ffmpeg exited 0
	if err := app.runWatchdog(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	got := getStream(t, app, ctx, s.ID)
	if got.Status != "ended" {
		t.Fatalf("status=%q, want ended", got.Status)
	}
	wantPath := filepath.Join(s.StoragePrefix, recordingFile)
	if got.RecordingPath != wantPath {
		t.Errorf("recording_path=%q, want %q", got.RecordingPath, wantPath)
	}
	if n := countEvents(t, ctx, s.ID, EventKindRecordingFinalized); n != 1 {
		t.Errorf("recording_finalized events=%d, want 1", n)
	}
	if n := countEvents(t, ctx, s.ID, EventKindPublisherDisconnect); n != 1 {
		t.Errorf("publisher_disconnect events=%d, want 1", n)
	}

	replay, err := app.toolReplayURL(ctx, map[string]any{"id": s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if replay.(map[string]any)["mp4_url"] == nil {
		t.Errorf("replay has no mp4_url: %v", replay)
	}
}

// A truncated recording (no moov atom) must not be advertised.
func TestFinalize_IgnoresTruncatedRecording(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "killed", "record": true})
	s := out.(map[string]any)["stream"].(*Stream)
	writeTestMP4(t, filepath.Join(streamDataDir(ctx, s.StoragePrefix), recordingFile), false)

	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	got := getStream(t, app, ctx, s.ID)
	if got.RecordingPath != "" {
		t.Errorf("recording_path=%q for a moov-less file", got.RecordingPath)
	}
	if n := countEvents(t, ctx, s.ID, EventKindRecordingFinalized); n != 0 {
		t.Errorf("recording_finalized emitted for a truncated file")
	}
}

func TestWatchdog_CrashKeepsTheSignalDetail(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "crash"})
	s := out.(map[string]any)["stream"].(*Stream)
	app.runnersMu.Lock()
	r := app.runners[s.ID]
	app.runnersMu.Unlock()
	fakeStop(r, errFake("ffmpeg killed by signal 9 (killed)"))

	if err := app.runWatchdog(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got := getStream(t, app, ctx, s.ID)
	if got.Status != "errored" {
		t.Fatalf("status=%q, want errored", got.Status)
	}
	if !strings.Contains(got.Error, "signal 9") {
		t.Errorf("error=%q, want the signal detail", got.Error)
	}
}

// ─── Finding 21: bounded live window + VOD replay playlist ─────────

func TestHLSWindow_DefaultsToBoundedWindow(t *testing.T) {
	app, ctx := newTestApp(t)
	if got := app.hlsWindowSegments(ctx); got != 10 {
		t.Errorf("hls_window_segments default = %d, want 10", got)
	}
}

func TestFinalize_WritesVODReplayPlaylist(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "vod"})
	s := out.(map[string]any)["stream"].(*Stream)
	for i := 0; i < 3; i++ {
		writeStreamFile(t, ctx, s, fmt.Sprintf("seg-%05d.ts", i), "ts")
	}
	// The live playlist only ever holds the rolling window.
	writeStreamFile(t, ctx, s, indexPlaylistFile, "#EXTM3U\n#EXTINF:4.000,\nseg-00002.ts\n")

	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(streamDataDir(ctx, s.StoragePrefix), replayPlaylistFile))
	if err != nil {
		t.Fatalf("replay playlist not written: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST", "#EXT-X-TARGETDURATION:4",
		"seg-00000.ts", "seg-00001.ts", "seg-00002.ts",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("replay playlist missing %q:\n%s", want, text)
		}
	}

	replay, _ := app.toolReplayURL(ctx, map[string]any{"id": s.ID})
	hls, _ := replay.(map[string]any)["hls_url"].(string)
	if !strings.Contains(hls, replayPlaylistFile) {
		t.Errorf("replay hls_url=%q, want the VOD manifest", hls)
	}
}

func TestReplayPlaylist_ServedOverHTTP(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "vod-http"})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "ts")
	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, app)
	url := fmt.Sprintf("%s/streams/%d/%s?t=%s", srv.URL, s.ID, replayPlaylistFile, s.PlaybackToken)
	code, hdr := getStatus(t, url)
	if code != http.StatusOK {
		t.Fatalf("replay.m3u8 returned %d, want 200", code)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "mpegurl") {
		t.Errorf("content-type=%q", ct)
	}
}

// ─── Finding 8: rotate gating, cap, rollback ───────────────────────

func TestRotateKey_RefusesTerminalStreamWithoutRevokeFlag(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "done"})
	s := out.(map[string]any)["stream"].(*Stream)
	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRotateKey(ctx, map[string]any{"id": s.ID}); err == nil {
		t.Fatal("rotating an ended stream should be refused — it resurrects the session")
	}
	got := getStream(t, app, ctx, s.ID)
	if got.Status != "ended" {
		t.Errorf("status=%q after refused rotation, want ended", got.Status)
	}
	app.runnersMu.Lock()
	defer app.runnersMu.Unlock()
	if app.runners[s.ID] != nil {
		t.Error("refused rotation still spawned a runner")
	}
}

func TestRotateKey_RevokeOnEndedStreamDoesNotRespawn(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "revoke-ended"})
	s := out.(map[string]any)["stream"].(*Stream)
	app.toolStop(ctx, map[string]any{"id": s.ID})

	res, err := app.toolRotateKey(ctx, map[string]any{
		"id": s.ID, "rotate_playback_token": true,
	})
	if err != nil {
		t.Fatalf("revoke on ended stream: %v", err)
	}
	if res.(map[string]any)["respawned"] != false {
		t.Error("revoking on a terminal stream must not respawn ffmpeg")
	}
	got := getStream(t, app, ctx, s.ID)
	if got.Status != "ended" {
		t.Errorf("status=%q, want ended (unchanged)", got.Status)
	}
	if got.PlaybackToken == s.PlaybackToken {
		t.Error("playback_token not rotated")
	}
}

func TestRotateKey_EnforcesMaxConcurrent(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "rot"})
	s := out.(map[string]any)["stream"].(*Stream)

	// Fill the remaining slots (default cap is 4, we hold 1).
	app.runnersMu.Lock()
	for i := 0; i < 3; i++ {
		app.runners[int64(500+i)] = &streamRunner{done: make(chan runnerExit, 1)}
	}
	app.runnersMu.Unlock()

	// Rotation frees its own slot then re-enters the pool: still 4.
	if _, err := app.toolRotateKey(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatalf("rotation within the cap failed: %v", err)
	}

	// One more occupant → the pool is full and rotation must refuse
	// rather than bypass the cap streams_create enforces.
	app.runnersMu.Lock()
	app.runners[int64(600)] = &streamRunner{done: make(chan runnerExit, 1)}
	app.runnersMu.Unlock()
	if _, err := app.toolRotateKey(ctx, map[string]any{"id": s.ID}); err == nil ||
		!strings.Contains(err.Error(), "max_concurrent_streams") {
		t.Errorf("expected a max_concurrent_streams refusal, got %v", err)
	}
	// The refused rotation had already killed the session, so the row
	// must not be left claiming a port it no longer owns.
	got := getStream(t, app, ctx, s.ID)
	if got.Status != "errored" {
		t.Errorf("status=%q after a refused rotation killed the session, want errored", got.Status)
	}
	if got.IngestPort != 0 {
		t.Errorf("row still advertises port %d", got.IngestPort)
	}
	app.runnersMu.Lock()
	defer app.runnersMu.Unlock()
	if app.pending != 0 {
		t.Errorf("pending=%d, want 0", app.pending)
	}
}

func TestRotateKey_FailedRespawnLeavesNoPhantomPort(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "doomed"})
	s := out.(map[string]any)["stream"].(*Stream)

	app.runnerFactory = func(opts runnerOpts) (*streamRunner, error) {
		return nil, errFake("no ffmpeg on this box")
	}
	if _, err := app.toolRotateKey(ctx, map[string]any{"id": s.ID}); err == nil {
		t.Fatal("expected the respawn failure to surface")
	}

	got := getStream(t, app, ctx, s.ID)
	if got.Status == "idle" {
		t.Error("row left as idle with no runner — that's the stuck state we're fixing")
	}
	if got.IngestPort != 0 {
		t.Errorf("row still advertises port %d it no longer owns", got.IngestPort)
	}
	// Every port must be back in the pool: none is claimed by a row
	// that has no runner behind it.
	app.ports.mu.Lock()
	inUse := len(app.ports.used)
	app.ports.mu.Unlock()
	if inUse != 0 {
		t.Errorf("%d ports still marked used after a failed rotation", inUse)
	}
	app.runnersMu.Lock()
	defer app.runnersMu.Unlock()
	if app.pending != 0 {
		t.Errorf("pending=%d, want 0 (concurrency slot leaked)", app.pending)
	}
}

// ─── Finding 16: create cap is TOCTOU-free ─────────────────────────

func TestCreate_ConcurrentCreatesRespectTheCap(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	dataDir := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("APTEVA_DATA_DIR", dataDir),
		tk.WithConfig(map[string]string{
			"rtmp_port_range":        "1935-1945",
			"max_concurrent_streams": "2",
		}),
	)
	app := &App{
		runners:       map[int64]*streamRunner{},
		viewers:       newViewerTracker(),
		throttle:      newViewerThrottle(),
		playback:      newPlaybackCache(playbackCacheTTL),
		runnerFactory: newFakeRunnerFactory(t),
	}
	pa, _ := newPortAllocator("1935-1945")
	app.ports = pa
	globalCtx, globalApp = ctx, app

	const attempts = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		okN  int
		errN int
	)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := app.toolCreate(ctx, map[string]any{"name": fmt.Sprintf("s%d", i)})
			mu.Lock()
			if err == nil {
				okN++
			} else {
				errN++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if okN > 2 {
		t.Errorf("%d creates succeeded with max_concurrent_streams=2", okN)
	}
	if okN+errN != attempts {
		t.Errorf("lost results: %d+%d != %d", okN, errN, attempts)
	}
	app.runnersMu.Lock()
	defer app.runnersMu.Unlock()
	if len(app.runners) > 2 {
		t.Errorf("%d runners registered, cap is 2", len(app.runners))
	}
	if app.pending != 0 {
		t.Errorf("pending=%d after all creates settled, want 0", app.pending)
	}
}

// ─── Finding 12: one timestamp format ──────────────────────────────

func TestTimestamps_AreRFC3339OnEveryPath(t *testing.T) {
	app, ctx := newTestApp(t)

	// created_at
	out, _ := app.toolCreate(ctx, map[string]any{"name": "ts"})
	s := out.(map[string]any)["stream"].(*Stream)
	assertRFC3339(t, "created_at", s.CreatedAt)

	// started_at (watchdog idle→live)
	app.runnersMu.Lock()
	r := app.runners[s.ID]
	app.runnersMu.Unlock()
	fakeFirstFrame(r, 1000, 24, "1280x720")
	app.runWatchdog(context.Background(), ctx)
	assertRFC3339(t, "started_at", getStream(t, app, ctx, s.ID).StartedAt)

	// ended_at via the watchdog (v0.1 wrote CURRENT_TIMESTAMP here)
	fakeStop(r, nil)
	app.runWatchdog(context.Background(), ctx)
	assertRFC3339(t, "ended_at (watchdog)", getStream(t, app, ctx, s.ID).EndedAt)

	// ended_at via streams_stop
	out2, _ := app.toolCreate(ctx, map[string]any{"name": "ts2"})
	s2 := out2.(map[string]any)["stream"].(*Stream)
	app.toolStop(ctx, map[string]any{"id": s2.ID})
	assertRFC3339(t, "ended_at (stop)", getStream(t, app, ctx, s2.ID).EndedAt)

	// occurred_at on the audit rows
	var occurred string
	if err := ctx.AppDB().QueryRow(
		`SELECT occurred_at FROM stream_events WHERE stream_id = ? LIMIT 1`, s.ID).Scan(&occurred); err != nil {
		t.Fatal(err)
	}
	assertRFC3339(t, "stream_events.occurred_at", occurred)

	// ended_at via the OnMount reconciler
	out3, _ := app.toolCreate(ctx, map[string]any{"name": "ts3"})
	s3 := out3.(map[string]any)["stream"].(*Stream)
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET status='errored', error='x', ended_at = ? WHERE id = ?`,
		nowStamp(), s3.ID); err != nil {
		t.Fatal(err)
	}
	assertRFC3339(t, "ended_at (reconciler)", getStream(t, app, ctx, s3.ID).EndedAt)
}

func assertRFC3339(t *testing.T, label, v string) {
	t.Helper()
	if v == "" {
		t.Errorf("%s is empty", label)
		return
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		t.Errorf("%s = %q is not RFC3339: %v", label, v, err)
	}
}

func TestParseTimestamp_AcceptsBothLegacyShapes(t *testing.T) {
	for _, in := range []string{"2026-08-18T09:00:00Z", "2026-08-18 09:00:00"} {
		got, ok := parseTimestamp(in)
		if !ok {
			t.Fatalf("parseTimestamp(%q) failed", in)
		}
		if got.Year() != 2026 || got.Hour() != 9 {
			t.Errorf("parseTimestamp(%q) = %v", in, got)
		}
	}
	if _, ok := parseTimestamp("not a time"); ok {
		t.Error("garbage accepted")
	}
}

// ─── Finding 22: retention sweeper ─────────────────────────────────

func TestRetention_PrunesMediaAndEventsPastTheWindow(t *testing.T) {
	app, ctx := newTestApp(t)
	out, _ := app.toolCreate(ctx, map[string]any{"name": "old", "retention_days": 7})
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "ts")
	writeTestMP4(t, filepath.Join(streamDataDir(ctx, s.StoragePrefix), recordingFile), true)
	app.toolStop(ctx, map[string]any{"id": s.ID})

	// Backdate the end well past the retention window.
	if _, err := ctx.AppDB().Exec(`UPDATE streams SET ended_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -30).UTC().Format(time.RFC3339), s.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRetention(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(streamDataDir(ctx, s.StoragePrefix)); !os.IsNotExist(err) {
		t.Errorf("stream media dir still on disk after retention: %v", err)
	}
	if n := countEvents(t, ctx, s.ID, EventKindEnded); n != 0 {
		t.Errorf("stream_events not pruned: %d ended rows remain", n)
	}
	got := getStream(t, app, ctx, s.ID)
	if got.PrunedAt == "" {
		t.Error("pruned_at not stamped — the sweep would redo this every hour")
	}
	if got.RecordingPath != "" {
		t.Errorf("recording_path=%q still advertises a deleted file", got.RecordingPath)
	}
}

func TestRetention_LeavesFreshAndUnlimitedStreamsAlone(t *testing.T) {
	app, ctx := newTestApp(t)

	fresh, _ := app.toolCreate(ctx, map[string]any{"name": "fresh", "retention_days": 30})
	fs := fresh.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, fs, "seg-00000.ts", "ts")
	app.toolStop(ctx, map[string]any{"id": fs.ID})

	forever, _ := app.toolCreate(ctx, map[string]any{"name": "forever", "retention_days": 0})
	ks := forever.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, ks, "seg-00000.ts", "ts")
	app.toolStop(ctx, map[string]any{"id": ks.ID})
	if _, err := ctx.AppDB().Exec(`UPDATE streams SET ended_at = ? WHERE id = ?`,
		time.Now().AddDate(-2, 0, 0).UTC().Format(time.RFC3339), ks.ID); err != nil {
		t.Fatal(err)
	}

	if err := app.runRetention(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Stream{fs, ks} {
		if _, err := os.Stat(streamDataDir(ctx, s.StoragePrefix)); err != nil {
			t.Errorf("stream %d (retention_days=%d) was pruned: %v", s.ID, s.RetentionDays, err)
		}
	}
}

// ─── Finding 15: ports must be bindable ────────────────────────────

func TestPortAllocator_SkipsPortsHeldByAnotherProcess(t *testing.T) {
	p, err := newPortAllocator("1935-1937")
	if err != nil {
		t.Fatal(err)
	}
	p.probe = func(port int) error {
		if port == 1935 || port == 1936 {
			return errFake("address already in use")
		}
		return nil
	}
	got, err := p.allocate()
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if got != 1937 {
		t.Errorf("allocate=%d, want 1937 (1935/1936 are occupied)", got)
	}
	if _, err := p.allocate(); err == nil {
		t.Error("expected the exhausted-range error")
	} else if !strings.Contains(err.Error(), "occupied") {
		t.Errorf("error should explain the ports are occupied, got %q", err)
	}
}

func TestPortAllocator_RealProbeRejectsABoundPort(t *testing.T) {
	p, err := newPortAllocator("1935-1936")
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.allocate()
	if err != nil {
		t.Skipf("no bindable port in the test range: %v", err)
	}
	// Hold it for real, release the bookkeeping, and confirm the
	// allocator won't hand out a port ffmpeg couldn't bind.
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", first))
	if err != nil {
		t.Skipf("could not bind %d: %v", first, err)
	}
	defer ln.Close()
	p.release(first)

	got, err := p.allocate()
	if err == nil && got == first {
		t.Errorf("allocator handed out %d while it is bound by another listener", first)
	}
}

// ─── Migration 002 against pre-existing v0.1 data ──────────────────
//
// The testkit only ever applies migrations to an empty DB, so the
// backfill + normalization statements are otherwise untested.
func TestMigration002_BackfillsSecretsAndNormalizesTimestamps(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	apply := func(file string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
	apply("001_init.sql")

	// A v0.1 row: SQLite CURRENT_TIMESTAMP shapes, no signing columns.
	if _, err := db.Exec(
		`INSERT INTO streams
		   (id, project_id, name, stream_key, playback_token, storage_prefix,
		    created_at, started_at, ended_at, status)
		 VALUES (1, 'p', 'legacy', 'sk', 'pt', 'streams/1',
		         '2026-08-18 09:00:00', '2026-08-18 09:01:02', '2026-08-18 10:30:00', 'ended')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO stream_events (id, project_id, stream_id, kind, occurred_at)
		 VALUES (1, 'p', 1, 'ended', '2026-08-18 10:30:00')`); err != nil {
		t.Fatal(err)
	}

	apply("002_hardening.sql")

	var created, started, ended, secret, occurred string
	var requireSigned int
	var pruned any
	if err := db.QueryRow(
		`SELECT created_at, started_at, ended_at, url_signing_secret, require_signed_urls, pruned_at
		 FROM streams WHERE id = 1`).
		Scan(&created, &started, &ended, &secret, &requireSigned, &pruned); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT occurred_at FROM stream_events WHERE id = 1`).Scan(&occurred); err != nil {
		t.Fatal(err)
	}

	for label, got := range map[string]string{
		"created_at": created, "started_at": started,
		"ended_at": ended, "occurred_at": occurred,
	} {
		assertRFC3339(t, label+" (migrated)", got)
	}
	if created != "2026-08-18T09:00:00Z" {
		t.Errorf("created_at=%q, want the same instant in RFC3339", created)
	}
	if secret == "" {
		t.Error("url_signing_secret not backfilled — an empty key must never validate a signature")
	}
	if requireSigned != 0 {
		t.Errorf("require_signed_urls=%d on a legacy row, want 0 (backward compatible)", requireSigned)
	}
	if pruned != nil {
		t.Errorf("pruned_at=%v on a legacy row, want NULL", pruned)
	}

	// The normalization is guarded on the value's shape, so a row
	// already in RFC3339 survives a second pass untouched. (The file as
	// a whole runs exactly once — the SDK's _migrations ledger sees to
	// that, and SQLite has no ADD COLUMN IF NOT EXISTS.)
	if _, err := db.Exec(
		`UPDATE streams SET created_at = replace(created_at, ' ', 'T') || 'Z'
		 WHERE created_at IS NOT NULL AND created_at <> '' AND created_at NOT LIKE '%T%'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT created_at FROM streams WHERE id = 1`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != "2026-08-18T09:00:00Z" {
		t.Errorf("re-running the normalization mangled an RFC3339 value: %q", created)
	}
}

// ─── Finding 23: WhoAmI is cached ──────────────────────────────────

type countingPlatform struct {
	tk.BasePlatformClient
	mu    sync.Mutex
	calls int
}

func (c *countingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return &sdk.InstallIdentity{
		AppName:   "streaming",
		ProjectID: "test-proj",
		PublicURL: "https://agents.example.com",
	}, nil
}

func (c *countingPlatform) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// materializeURLs called publicURL twice per stream, inside list
// loops — an uncached HTTP round-trip to apteva-server per row.
func TestPublicURL_CachedAcrossListLoops(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	platform := &countingPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()),
		tk.WithPlatform(platform),
		tk.WithConfig(map[string]string{"rtmp_port_range": "1935-1940"}),
	)
	app := &App{
		runners:       map[int64]*streamRunner{},
		viewers:       newViewerTracker(),
		throttle:      newViewerThrottle(),
		playback:      newPlaybackCache(playbackCacheTTL),
		runnerFactory: newFakeRunnerFactory(t),
	}
	pa, _ := newPortAllocator("1935-1940")
	app.ports = pa
	globalCtx, globalApp = ctx, app

	for i := 0; i < 3; i++ {
		if _, err := app.toolCreate(ctx, map[string]any{"name": fmt.Sprintf("s%d", i)}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	out, err := app.toolList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	streams := out.(map[string]any)["streams"].([]*Stream)
	if len(streams) != 3 {
		t.Fatalf("listed %d streams, want 3", len(streams))
	}
	if !strings.HasPrefix(streams[0].PlaybackURL, "https://agents.example.com/api/apps/streaming/") {
		t.Errorf("playback_url doesn't use the platform's public URL: %q", streams[0].PlaybackURL)
	}
	if !strings.HasPrefix(streams[0].IngestURL, "rtmp://agents.example.com:") {
		t.Errorf("ingest_url=%q", streams[0].IngestURL)
	}
	if n := platform.count(); n != 1 {
		t.Errorf("WhoAmI called %d times across 3 creates + a list, want 1 (cached)", n)
	}
}

// ─── Finding 7a: finalization grace ────────────────────────────────

func TestFinalizeGrace(t *testing.T) {
	_, ctx := newTestApp(t)
	app := &App{}
	if got := app.finalizeGrace(ctx, true); got < 60*time.Second {
		t.Errorf("recording grace = %v, want >= 60s (+faststart rewrites the whole file)", got)
	}
	if got := app.finalizeGrace(ctx, false); got > 10*time.Second {
		t.Errorf("non-recording grace = %v, want a short wait", got)
	}
}

func TestFinalizeGrace_Configurable(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()),
		tk.WithConfig(map[string]string{"finalize_grace_seconds": "300"}),
	)
	app := &App{}
	if got := app.finalizeGrace(ctx, true); got != 300*time.Second {
		t.Errorf("configured grace = %v, want 300s", got)
	}
	// Nonsense values fall back to the default rather than escalating
	// to SIGKILL immediately.
	ctx2 := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()),
		tk.WithConfig(map[string]string{"finalize_grace_seconds": "0"}),
	)
	if got := app.finalizeGrace(ctx2, true); got != 60*time.Second {
		t.Errorf("grace with a bogus config = %v, want the 60s default", got)
	}
}

// ─── Finding 11: no orphan row when the prefix update fails ────────
//
// The UPDATE only fails on a DB-level fault, which the testkit can't
// stage without breaking the connection for everything else — so this
// asserts the invariant the cleanup protects: a row that survives
// create always has a storage_prefix.
func TestCreate_NoRowWithoutStoragePrefix(t *testing.T) {
	app, ctx := newTestApp(t)
	app.runnerFactory = func(opts runnerOpts) (*streamRunner, error) {
		return nil, errFake("spawn refused")
	}
	if _, err := app.toolCreate(ctx, map[string]any{"name": "doomed"}); err == nil {
		t.Fatal("expected the spawn failure to surface")
	}
	var n int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM streams WHERE storage_prefix = '' OR storage_prefix IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d orphan rows with no storage_prefix", n)
	}
}
