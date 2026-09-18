package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// Regression cover for the v0.3.0 hardening pass — one test per defect
// fixed, each named for the behavior it pins rather than the symptom.
//
// The rotate_key case is the one place the fake runner limits what can
// be asserted: nothing writes segments in a test, so "the earlier
// segments still hold their original bytes" guards against a
// destructive cleanup rather than proving what ffmpeg would do. The
// load-bearing assertions there are the computed start_number and the
// rolled-aside recording.

// ─── X-Forwarded-For ──────────────────────────────────────────────

// v0.2 read the LEFTMOST entry, which is whatever the client sent —
// httputil.ReverseProxy appends the peer it saw rather than replacing
// the header, and the server's proxy Director doesn't scrub it. So a
// client could pick its own throttle bucket, and by varying it get an
// unlimited number of them.
func TestClientIP_IgnoresSpoofedLeadingForwardedFor(t *testing.T) {
	cases := []struct {
		name, remote, fwd, want string
	}{
		{"spoofed leading entry", "127.0.0.1:5555", "9.9.9.9, 203.0.113.7", "203.0.113.7"},
		{"single real entry", "127.0.0.1:5555", "203.0.113.7", "203.0.113.7"},
		{"extra proxy hop in front", "127.0.0.1:5555", "9.9.9.9, 203.0.113.7, 10.0.0.8", "203.0.113.7"},
		{"loopback tail hop", "127.0.0.1:5555", "203.0.113.7, 127.0.0.1", "203.0.113.7"},
		{"all-internal chain falls back", "127.0.0.1:5555", "10.0.0.4, 10.0.0.8", "10.0.0.8"},
		{"no forwarded header disables throttle", "127.0.0.1:5555", "", ""},
		{"non-loopback peer wins outright", "198.51.100.4:9000", "9.9.9.9", "198.51.100.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet, "/heartbeat/1", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.RemoteAddr = tc.remote
			if tc.fwd != "" {
				r.Header.Set("X-Forwarded-For", tc.fwd)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── Signature scoping ────────────────────────────────────────────

// v0.2 signed only "<id>:<exp>", so streams_signed_url's `kind`
// argument scoped nothing: the long-lived heartbeat URL a viewer needs
// for the whole webinar also unlocked record.mp4 for that lifetime,
// whatever TTL the consumer chose for the replay link.
func TestSignature_IsScopedToURLKind(t *testing.T) {
	const secret = "s3cret"
	exp := time.Now().Add(time.Hour).Unix()
	rec := &playbackRecord{
		ID: 42, Visibility: "signed", PlaybackToken: "tok",
		SigningSecret: secret, RequireSignedURLs: true, Status: "ended",
	}
	q := func(scope string) url.Values {
		return url.Values{
			"t":   {"tok"},
			"exp": {fmt.Sprint(exp)},
			"sig": {signPlayback(secret, rec.ID, exp, scope)},
		}
	}

	for _, scope := range []string{scopeHLS, scopeMP4, scopeHeartbeat} {
		if !playbackAuthorized(rec, q(scope), scope, time.Now()) {
			t.Errorf("%s signature rejected on its own scope", scope)
		}
	}
	crossings := []struct{ minted, used string }{
		{scopeHeartbeat, scopeMP4},
		{scopeHeartbeat, scopeHLS},
		{scopeHLS, scopeMP4},
		{scopeMP4, scopeHLS},
		{scopeMP4, scopeHeartbeat},
		{scopeHLS, scopeHeartbeat},
	}
	for _, c := range crossings {
		if playbackAuthorized(rec, q(c.minted), c.used, time.Now()) {
			t.Errorf("signature minted for %q was accepted on %q", c.minted, c.used)
		}
	}
}

// The manifest and its segments must share a scope: the playback
// handler rewrites segment URIs to carry the manifest's own query, so
// a per-file signature would 404 every segment.
func TestScopeForFile_ManifestAndSegmentsShareAScope(t *testing.T) {
	for _, f := range []string{indexPlaylistFile, replayPlaylistFile, "seg-00007.ts"} {
		if got := scopeForFile(f); got != scopeHLS {
			t.Errorf("scopeForFile(%q) = %q, want %q", f, got, scopeHLS)
		}
	}
	if got := scopeForFile(recordingFile); got != scopeMP4 {
		t.Errorf("scopeForFile(%q) = %q, want %q", recordingFile, got, scopeMP4)
	}
}

// End to end: a heartbeat-signed URL must not open the recording.
func TestHeartbeatSignature_DoesNotUnlockRecording(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "scoped"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	writeTestMP4(t, filepath.Join(streamDataDir(ctx, s.StoragePrefix), recordingFile), true)
	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSetURLPolicy(ctx, map[string]any{
		"id": s.ID, "require_signed_urls": true,
	}); err != nil {
		t.Fatal(err)
	}
	beat, err := app.toolSignedURL(ctx, map[string]any{
		"id": s.ID, "expires_in_seconds": 3600, "kind": "heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := url.Parse(beat.(map[string]any)["url"].(string))

	srv := newTestServer(t, app)
	// Same credentials, pointed at the recording.
	target := fmt.Sprintf("%s/streams/%d/%s?%s", srv.URL, s.ID, recordingFile, hb.RawQuery)
	if code, _ := getStatus(t, target); code != http.StatusNotFound {
		t.Errorf("heartbeat-signed URL opened record.mp4 with %d, want 404", code)
	}
}

// ─── Negative caching ─────────────────────────────────────────────

// The media routes are NoAuth and take the id off the path, so an
// uncached miss let anyone queue serialized reads on the app DB's one
// connection ahead of every real viewer.
func TestPlaybackCache_CachesMisses(t *testing.T) {
	app, ctx := newTestApp(t)

	rec, err := app.playbackFor(ctx, "test-proj", 999999)
	if err != nil || rec != nil {
		t.Fatalf("playbackFor(missing) = %v, %v; want nil, nil", rec, err)
	}
	if _, missing, fresh := app.playback.get("test-proj", 999999); !fresh || !missing {
		t.Error("a miss should be cached as a negative entry")
	}
	// Still reports absent while cached.
	if rec, err := app.playbackFor(ctx, "test-proj", 999999); err != nil || rec != nil {
		t.Fatalf("cached miss = %v, %v; want nil, nil", rec, err)
	}
}

// A negative parked by a probe must not shadow a stream created after
// it.
func TestCreate_ClearsACachedMiss(t *testing.T) {
	app, ctx := newTestApp(t)
	// Probe id 1 before it exists.
	if _, err := app.playbackFor(ctx, "test-proj", 1); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolCreate(ctx, map[string]any{"name": "after probe"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	rec, err := app.playbackFor(ctx, "test-proj", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("freshly created stream is shadowed by a cached miss")
	}
}

// ─── Restart / shutdown finalization ──────────────────────────────

// v0.2's OnMount reconciler wrote status with a bare UPDATE, so it
// never picked up the recording — the common restart-during-a-webinar
// case left a complete, playable mp4 permanently unreachable.
func TestReconcileOrphans_RecoversStrandedRecording(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "interrupted"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	dir := streamDataDir(ctx, s.StoragePrefix)
	writeTestMP4(t, filepath.Join(dir, recordingFile), true)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "ts")

	// Simulate the next boot: the row is still idle/live, no runner.
	app.runners = map[int64]*streamRunner{}
	if err := app.reconcileOrphans(ctx); err != nil {
		t.Fatalf("reconcileOrphans: %v", err)
	}

	got := getStream(t, app, ctx, s.ID)
	if got.Status != "errored" {
		t.Errorf("status=%q, want errored", got.Status)
	}
	if got.RecordingPath == "" {
		t.Error("a complete recording was left stranded by the reconciler")
	}
	if !replayPlaylistExists(ctx, got.StoragePrefix) {
		t.Error("reconciler did not write the VOD replay playlist")
	}
	if got.EndedAt == "" {
		t.Error("ended_at not set")
	}
}

// A clean shutdown must record the outcome, not just stop ffmpeg.
func TestOnUnmount_FinalizesRunningStreams(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "shutting down"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	writeTestMP4(t, filepath.Join(streamDataDir(ctx, s.StoragePrefix), recordingFile), true)

	if err := app.OnUnmount(ctx); err != nil {
		t.Fatalf("OnUnmount: %v", err)
	}
	got := getStream(t, app, ctx, s.ID)
	if got.Status != "ended" {
		t.Errorf("status=%q after unmount, want ended", got.Status)
	}
	if got.RecordingPath == "" {
		t.Error("unmount left the recording stranded")
	}
}

// A runner that died on its own before shutdown must keep its crash
// status: unmount is when we NOTICE it, not what caused it. stop() now
// reports an already-recorded exit instead of returning nil.
func TestOnUnmount_KeepsACrashAsErrored(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "crashed"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	app.runnersMu.Lock()
	r := app.runners[s.ID]
	app.runnersMu.Unlock()
	fakeStop(r, errors.New("ffmpeg killed by signal 9 (killed)"))

	if err := app.OnUnmount(ctx); err != nil {
		t.Fatal(err)
	}
	got := getStream(t, app, ctx, s.ID)
	if got.Status != "errored" {
		t.Errorf("status=%q after unmounting a crashed runner, want errored", got.Status)
	}
	if !strings.Contains(got.Error, "signal 9") {
		t.Errorf("crash detail lost: error=%q", got.Error)
	}
}

// slowExitRunner wraps a real child that takes ~1s to die after
// SIGINT, so stop() genuinely blocks. The fake runner can't show this:
// its cmd is nil and stop() returns immediately.
func slowExitRunner(t *testing.T, streamID int64) *streamRunner {
	t.Helper()
	// Handle INT, linger, then exit cleanly — the shape of ffmpeg
	// flushing its outputs on the way out.
	//
	// Readiness is announced from a BACKGROUND subshell, deliberately
	// after the parent is already blocked in its long sleep. Echoing
	// inline instead leaves a window between the write and the sleep
	// starting: a SIGINT landing there is deferred by the shell until
	// the command it then starts returns, i.e. never within the grace,
	// and the runner looks slow for the wrong reason. Signalling before
	// the trap exists is the opposite failure — the shell just dies and
	// the test silently measures nothing.
	cmd := exec.Command("/bin/sh", "-c",
		`trap 'sleep 1; exit 0' INT; (sleep 0.2; echo ready) & sleep 30`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, len("ready\n"))
		if _, err := io.ReadFull(stdout, buf); err == nil {
			close(ready)
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("helper process never installed its signal trap")
	}
	r := &streamRunner{
		streamID: streamID,
		cmd:      cmd,
		done:     make(chan runnerExit, 1),
		quit:     make(chan struct{}),
	}
	t.Cleanup(func() {
		r.stopRequested.Store(true)
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	go r.wait()
	return r
}

// The serial stop meant shutdown took grace × N, the platform's budget
// expired, and the SIGKILL landed on ffmpeg children still rewriting
// their moov atom — the generous grace that exists to protect those
// recordings was the reason they got truncated.
func TestOnUnmount_StopsRunnersConcurrently(t *testing.T) {
	app, ctx := newTestApp(t)
	const streams = 3
	for i := 0; i < streams; i++ {
		out, err := app.toolCreate(ctx, map[string]any{
			"name": fmt.Sprintf("s%d", i), "record": false,
		})
		if err != nil {
			t.Fatal(err)
		}
		s := out.(map[string]any)["stream"].(*Stream)
		// Swap the fake for a child that actually takes time to exit.
		app.runnersMu.Lock()
		app.runners[s.ID] = slowExitRunner(t, s.ID)
		app.runnersMu.Unlock()
	}

	start := time.Now()
	if err := app.OnUnmount(ctx); err != nil {
		t.Fatalf("OnUnmount: %v", err)
	}
	elapsed := time.Since(start)

	// Each child takes ~1s to go down. Serially that is ~3s; run
	// together it is ~1s plus the finalize writes.
	if elapsed > 2*time.Second {
		t.Errorf("OnUnmount took %v for %d runners that take ~1s each — looks serial",
			elapsed, streams)
	}
	// And it still records the outcome for every one of them.
	app.runnersMu.Lock()
	left := len(app.runners)
	app.runnersMu.Unlock()
	if left != 0 {
		t.Errorf("%d runners left registered after unmount", left)
	}
}

// ─── rotate_key must not destroy the session it replaces ──────────

func TestRotateKey_PreservesPriorSegmentsAndRecording(t *testing.T) {
	app, ctx := newTestApp(t)

	var gotOpts runnerOpts
	inner := app.runnerFactory
	app.runnerFactory = func(opts runnerOpts) (*streamRunner, error) {
		gotOpts = opts
		return inner(opts)
	}

	out, err := app.toolCreate(ctx, map[string]any{"name": "rotating"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	dir := streamDataDir(ctx, s.StoragePrefix)

	// A session already in progress: three segments and a recording.
	for i, body := range []string{"first", "second", "third"} {
		writeStreamFile(t, ctx, s, fmt.Sprintf("seg-%05d.ts", i), body)
	}
	writeTestMP4(t, filepath.Join(dir, recordingFile), true)
	originalMP4, err := os.ReadFile(filepath.Join(dir, recordingFile))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := app.toolRotateKey(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// The respawn continues the numbering rather than starting over.
	if gotOpts.startNumber != 3 {
		t.Errorf("respawn startNumber=%d, want 3 (past seg-00002.ts)", gotOpts.startNumber)
	}
	// The earlier segments are untouched.
	for i, want := range []string{"first", "second", "third"} {
		body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("seg-%05d.ts", i)))
		if err != nil {
			t.Fatalf("segment %d gone after rotate: %v", i, err)
		}
		if string(body) != want {
			t.Errorf("seg-%05d.ts = %q, want %q — overwritten by the respawn", i, body, want)
		}
	}
	// The recording was rolled aside, not truncated.
	rolled, err := os.ReadFile(filepath.Join(dir, "record-1.mp4"))
	if err != nil {
		t.Fatalf("prior recording not preserved: %v", err)
	}
	if string(rolled) != string(originalMP4) {
		t.Error("preserved recording does not match the original")
	}
	if _, err := os.Stat(filepath.Join(dir, recordingFile)); !os.IsNotExist(err) {
		t.Error("record.mp4 should be free for the new session to write")
	}
}

func TestNextSegmentIndex(t *testing.T) {
	dir := t.TempDir()
	if n, err := nextSegmentIndex(dir); err != nil || n != 0 {
		t.Fatalf("empty dir = %d, %v; want 0, nil", n, err)
	}
	if n, err := nextSegmentIndex(filepath.Join(dir, "nope")); err != nil || n != 0 {
		t.Fatalf("missing dir = %d, %v; want 0, nil", n, err)
	}
	for _, name := range []string{"seg-00000.ts", "seg-00004.ts", "seg-00012.ts", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := nextSegmentIndex(dir); err != nil || n != 13 {
		t.Errorf("nextSegmentIndex = %d, %v; want 13, nil", n, err)
	}
}

func TestRollRecording_FindsAFreeSlot(t *testing.T) {
	dir := t.TempDir()
	// Nothing to roll.
	if err := rollRecording(dir); err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	for n := 1; n <= 3; n++ {
		if err := os.WriteFile(filepath.Join(dir, recordingFile), []byte(fmt.Sprint(n)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rollRecording(dir); err != nil {
			t.Fatalf("roll %d: %v", n, err)
		}
		body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("record-%d.mp4", n)))
		if err != nil {
			t.Fatalf("roll %d: %v", n, err)
		}
		if string(body) != fmt.Sprint(n) {
			t.Errorf("record-%d.mp4 = %q, want %q", n, body, fmt.Sprint(n))
		}
	}
}

// ─── Signed-URL lifetimes ─────────────────────────────────────────

// v0.2 gave a LIVE stream the 1h replay TTL, so a longer webinar went
// dark for every viewer at once mid-broadcast with no renewal path.
func TestSignedURLTTL_LiveOutlivesReplay(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "long webinar"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	if _, err := app.toolSetURLPolicy(ctx, map[string]any{
		"id": s.ID, "require_signed_urls": true,
	}); err != nil {
		t.Fatal(err)
	}

	live := getStream(t, app, ctx, s.ID)
	liveTTL := time.Until(time.Unix(live.PlaybackURLExpiresAt, 0))
	if liveTTL <= replayURLTTL {
		t.Errorf("live playback URL lasts %v, no longer than the replay TTL %v", liveTTL, replayURLTTL)
	}
	if liveTTL < liveURLTTL-time.Minute || liveTTL > liveURLTTL+time.Minute {
		t.Errorf("live TTL %v, want ~%v", liveTTL, liveURLTTL)
	}

	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	ended := getStream(t, app, ctx, s.ID)
	endedTTL := time.Until(time.Unix(ended.PlaybackURLExpiresAt, 0))
	if endedTTL > replayURLTTL+time.Minute {
		t.Errorf("terminal stream TTL %v, want ~%v", endedTTL, replayURLTTL)
	}
}

// The expiry has to be reported, or a consumer can only discover it
// from a 404 mid-session.
func TestMaterializeURLs_ReportsExpiryOnlyWhenSigned(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "unsigned"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	if s.PlaybackURLExpiresAt != 0 {
		t.Errorf("unsigned URL reported an expiry of %d", s.PlaybackURLExpiresAt)
	}
	if _, err := app.toolSetURLPolicy(ctx, map[string]any{
		"id": s.ID, "require_signed_urls": true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := getStream(t, app, ctx, s.ID); got.PlaybackURLExpiresAt == 0 {
		t.Error("signed URL did not report its expiry")
	}
}

func TestSignedURLTTL_Configurable(t *testing.T) {
	app, _ := newTestApp(t)
	ctx := newCtxWithConfig(t, map[string]string{
		"live_url_ttl_seconds":   "7200",
		"replay_url_ttl_seconds": "300",
	})
	if got := app.signedURLTTL(ctx, true); got != 2*time.Hour {
		t.Errorf("live TTL = %v, want 2h", got)
	}
	if got := app.signedURLTTL(ctx, false); got != 5*time.Minute {
		t.Errorf("replay TTL = %v, want 5m", got)
	}
	// Out-of-range values fall back to the defaults.
	bad := newCtxWithConfig(t, map[string]string{"live_url_ttl_seconds": "1"})
	if got := app.signedURLTTL(bad, true); got != liveURLTTL {
		t.Errorf("out-of-range TTL = %v, want the default %v", got, liveURLTTL)
	}
}

// ─── Pruned replay ────────────────────────────────────────────────

// v0.2 answered available:true with no URLs at all once retention had
// reclaimed the media.
func TestReplayURL_ReportsPrunedMedia(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "old webinar"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "ts")
	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	// What the retention sweeper leaves behind.
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET pruned_at = ?, recording_path = NULL WHERE id = ?`,
		nowStamp(), s.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(streamDataDir(ctx, s.StoragePrefix)); err != nil {
		t.Fatal(err)
	}

	got, err := app.toolReplayURL(ctx, map[string]any{"id": s.ID})
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["available"] != false {
		t.Errorf("available=%v for pruned media, want false", m["available"])
	}
	if _, ok := m["mp4_url"]; ok {
		t.Error("pruned stream returned an mp4_url")
	}
	if _, ok := m["hls_url"]; ok {
		t.Error("pruned stream returned an hls_url")
	}
}

// ─── VOD manifest accuracy ────────────────────────────────────────

// v0.2 wrote a uniform EXTINF and a TARGETDURATION equal to -hls_time,
// which understates any keyframe-driven overshoot and makes the
// playlist invalid per RFC 8216.
func TestReplayPlaylist_UsesRealSegmentDurations(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"seg-00000.ts", "seg-00001.ts", "seg-00002.ts"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("ts"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A keyframe overshoot and a short final partial.
	log := "seg-00000.ts\t4.000000\nseg-00001.ts\t5.200000\nseg-00002.ts\t0.640000\n"
	if err := os.WriteFile(filepath.Join(dir, segmentLogFile), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeReplayPlaylist(dir, 4); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, replayPlaylistFile))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:6", // >= the 5.2s longest, rounded up
		"#EXTINF:4.000,",
		"#EXTINF:5.200,",
		"#EXTINF:0.640,",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("replay playlist missing %q:\n%s", want, got)
		}
	}
	// TARGETDURATION must be >= every EXTINF.
	for _, d := range parsePlaylistDurations(got) {
		if d > 6 {
			t.Errorf("EXTINF %v exceeds the declared TARGETDURATION", d)
		}
	}
}

// Segments with no logged duration still get the configured fallback.
func TestReplayPlaylist_FallsBackForUnloggedSegments(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"seg-00000.ts", "seg-00001.ts"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("ts"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeReplayPlaylist(dir, 4); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, replayPlaylistFile))
	if n := strings.Count(string(body), "#EXTINF:4.000,"); n != 2 {
		t.Errorf("want 2 fallback EXTINFs, got %d:\n%s", n, body)
	}
	if !strings.Contains(string(body), "#EXT-X-TARGETDURATION:4") {
		t.Errorf("TARGETDURATION should follow the fallback:\n%s", body)
	}
}

func TestParsePlaylistDurations(t *testing.T) {
	body := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:4",
		"#EXTINF:4.004,",
		"seg-00000.ts",
		"#EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:00:00.000+0000",
		"#EXTINF:3.968,",
		"seg-00001.ts?t=tok&exp=123", // a rewritten URI still keys on the name
		"",
	}, "\n")
	got := parsePlaylistDurations(body)
	if len(got) != 2 {
		t.Fatalf("parsed %d entries, want 2: %v", len(got), got)
	}
	if got["seg-00000.ts"] != 4.004 {
		t.Errorf("seg-00000.ts = %v, want 4.004", got["seg-00000.ts"])
	}
	if got["seg-00001.ts"] != 3.968 {
		t.Errorf("seg-00001.ts = %v, want 3.968", got["seg-00001.ts"])
	}
}

// ─── Load test target ─────────────────────────────────────────────

// v0.2 read APTEVA_LISTEN_PORT, which nothing sets; the SDK binds
// APTEVA_APP_PORT.
func TestLoopbackPlaybackURL_UsesTheSDKPortVar(t *testing.T) {
	app, _ := newTestApp(t)
	s := &Stream{ID: 7, ProjectID: "test-proj", PlaybackToken: "tok", URLSigningSecret: "sec"}

	t.Setenv("APTEVA_APP_PORT", "34567")
	got := app.loopbackPlaybackURL(s, indexPlaylistFile, 0)
	if !strings.HasPrefix(got, "http://127.0.0.1:34567/") {
		t.Errorf("load test target = %q, want the SDK's assigned port", got)
	}
	if err := assertLoopback(got); err != nil {
		t.Errorf("target is not loopback: %v", err)
	}
}

// A stream under a signed-URL policy could not be load-tested at all.
func TestLoopbackPlaybackURL_CarriesASignature(t *testing.T) {
	app, _ := newTestApp(t)
	s := &Stream{ID: 7, ProjectID: "test-proj", PlaybackToken: "tok", URLSigningSecret: "sec"}
	exp := time.Now().Add(time.Hour).Unix()

	got := app.loopbackPlaybackURL(s, indexPlaylistFile, exp)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	rec := &playbackRecord{
		ID: s.ID, Visibility: "signed", PlaybackToken: "tok",
		SigningSecret: "sec", RequireSignedURLs: true,
	}
	if !playbackAuthorized(rec, u.Query(), scopeHLS, time.Now()) {
		t.Errorf("signed load-test URL does not satisfy the policy: %q", got)
	}
}

// ─── Throttle shape ───────────────────────────────────────────────

// One stream's audience must not exhaust another's budget.
func TestThrottle_BucketsArePerStream(t *testing.T) {
	th := newViewerThrottle(2)
	budget := th.maxBeats()

	for i := 0; i < budget; i++ {
		if _, ok := th.admit("203.0.113.9", 1, "viewer"); !ok {
			t.Fatalf("stream 1 refused beat %d inside its budget", i)
		}
	}
	if _, ok := th.admit("203.0.113.9", 1, "viewer"); ok {
		t.Error("stream 1 should be over budget")
	}
	if _, ok := th.admit("203.0.113.9", 2, "viewer"); !ok {
		t.Error("stream 2 was throttled by stream 1's traffic from the same IP")
	}
}

// The beat ceiling has to scale with the identity budget — they
// describe the same population, and in v0.2 they drifted apart badly
// enough to 429 a NAT'd audience of ~20.
func TestThrottle_BeatBudgetScalesWithIdentities(t *testing.T) {
	if newViewerThrottle(64).maxBeats() <= newViewerThrottle(8).maxBeats() {
		t.Error("a larger identity budget must raise the beat ceiling")
	}
	th := newViewerThrottle(defaultMaxViewersPerIP)
	// A real audience beats every viewerIdleSeconds/3-ish; the default
	// cadence is one per 10s, so a full bucket of viewers must fit.
	beatsPerViewerPerWindow := int(throttleWindow / (10 * time.Second))
	if need := defaultMaxViewersPerIP * beatsPerViewerPerWindow; th.maxBeats() < need {
		t.Errorf("maxBeats=%d cannot carry %d viewers at %d beats each",
			th.maxBeats(), defaultMaxViewersPerIP, beatsPerViewerPerWindow)
	}
}

// Over the identity budget the request still succeeds — it just stops
// adding headcount. Only the beat ceiling 429s.
func TestThrottle_ExcessIdentitiesCollapseWithout429(t *testing.T) {
	th := newViewerThrottle(2)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		id, ok := th.admit("203.0.113.9", 1, fmt.Sprintf("viewer-%d", i))
		if !ok {
			t.Fatalf("beat %d was refused; excess identities should collapse, not 429", i)
		}
		seen[id] = true
	}
	if len(seen) > 3 { // 2 real + 1 synthetic
		t.Errorf("counted %d identities, want at most 3", len(seen))
	}
}

// ─── Retention scoping ────────────────────────────────────────────

func TestRetention_ScopesWritesToTheProject(t *testing.T) {
	app, ctx := newTestApp(t)
	out, err := app.toolCreate(ctx, map[string]any{"name": "prunable"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.(map[string]any)["stream"].(*Stream)
	writeStreamFile(t, ctx, s, "seg-00000.ts", "ts")
	if _, err := app.toolStop(ctx, map[string]any{"id": s.ID}); err != nil {
		t.Fatal(err)
	}
	// Age it past the window.
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET ended_at = ?, retention_days = 30 WHERE id = ?`, old, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRetention(context.Background(), ctx); err != nil {
		t.Fatalf("runRetention: %v", err)
	}
	got := getStream(t, app, ctx, s.ID)
	if got.PrunedAt == "" {
		t.Error("project-scoped write did not mark the row pruned")
	}
	if _, err := os.Stat(streamDataDir(ctx, s.StoragePrefix)); !os.IsNotExist(err) {
		t.Error("media not reclaimed")
	}
}

// ─── Worker interval ──────────────────────────────────────────────

// The watch-seconds arithmetic and the worker's schedule must come
// from one constant — v0.2 hardcoded ×10 next to "@every 10s".
func TestViewerCounter_ScheduleMatchesItsArithmetic(t *testing.T) {
	app := &App{}
	var sched string
	for _, w := range app.Workers() {
		if w.Name == "viewer-counter" {
			sched = w.Schedule
		}
	}
	want := fmt.Sprintf("@every %s", viewerCounterInterval)
	if sched != want {
		t.Errorf("viewer-counter schedule = %q, want %q", sched, want)
	}
}

// newCtxWithConfig builds an AppCtx carrying a specific config map.
func newCtxWithConfig(t *testing.T, cfg map[string]string) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()),
		tk.WithConfig(cfg),
	)
}
