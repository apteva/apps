//go:build smoke

package main

// Real end-to-end smoke test:
//
//   1. Spin up the app's HTTP routes on a free port.
//   2. streams_create — spawns the real ffmpeg RTMP listener.
//   3. Spawn a publisher: ffmpeg -re testsrc → RTMP push.
//   4. Wait for status=live + populated manifest.
//   5. Run streams_load_test against the live URL.
//   6. Print the result.
//
// Run with:
//   go test -tags smoke -run TestSmokeLoadTest -v -timeout 5m
//
// Override viewer count + duration via env:
//   STREAMING_SMOKE_VIEWERS=50 STREAMING_SMOKE_DURATION_S=30 \
//     go test -tags smoke -run TestSmokeLoadTest -v -timeout 5m
//
// Requires ffmpeg + libx264 on $PATH.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestSmokeLoadTest(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH — skipping smoke test")
	}

	viewers := envInt("STREAMING_SMOKE_VIEWERS", 10)
	durationSec := envInt("STREAMING_SMOKE_DURATION_S", 20)

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "smoke.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=2000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	mig, err := os.ReadFile(filepath.Join("migrations", "001_init.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	manifestBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	cfg := sdk.Config{
		"rtmp_port_range":     "1955-1965",
		"hls_segment_seconds": "2",
		"viewer_idle_seconds": "30",
		"ffmpeg_path":         "ffmpeg",
	}
	t.Setenv("APTEVA_DATA_DIR", dataDir)
	t.Setenv("APTEVA_PROJECT_ID", "smoke-proj")

	appCtx := sdk.NewAppCtxForTest(manifest, db, cfg, nil, nil)

	app := &App{
		runners:       map[int64]*streamRunner{},
		runnerFactory: newFFmpegRunner,
	}
	pa, err := newPortAllocator(cfg.Get("rtmp_port_range"))
	if err != nil {
		t.Fatalf("port allocator: %v", err)
	}
	app.ports = pa
	globalCtx = appCtx
	globalApp = app

	// HTTP server for playback routes.
	httpPort, err := pickFreePort()
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	mux := http.NewServeMux()
	for _, r := range app.HTTPRoutes() {
		mux.HandleFunc(r.Pattern, r.Handler)
	}
	srv := &http.Server{Addr: fmt.Sprintf(":%d", httpPort), Handler: mux}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() { srv.Close() })
	t.Setenv("APTEVA_LISTEN_PORT", strconv.Itoa(httpPort))

	t.Logf("sidecar HTTP on :%d", httpPort)
	t.Logf("data dir %s", dataDir)

	// Watchdog ticker — drives idle→live transitions and dead-runner
	// detection. SDK schedules this in production; here we run it
	// manually.
	stopWD := make(chan struct{})
	go func() {
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stopWD:
				return
			case <-tk.C:
				_ = app.runWatchdog(context.Background(), appCtx)
			}
		}
	}()
	t.Cleanup(func() { close(stopWD) })

	// 1. Allocate the stream — spawns ffmpeg listening on RTMP.
	createOut, err := app.toolCreate(appCtx, map[string]any{
		"name":      "smoke",
		"owner_app": "smoke",
		"record":    false,
	})
	if err != nil {
		t.Fatalf("streams_create: %v", err)
	}
	stream := createOut.(map[string]any)["stream"].(*Stream)
	t.Logf("stream id=%d rtmp_port=%d ingest=%s",
		stream.ID, stream.IngestPort, stream.IngestURL)

	// 2. Spawn the publisher.
	publisher := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "warning",
		"-re",
		"-f", "lavfi", "-i", "testsrc=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-b:v", "2000k", "-pix_fmt", "yuv420p", "-g", "60",
		"-c:a", "aac", "-b:a", "64k",
		"-f", "flv",
		fmt.Sprintf("rtmp://localhost:%d/live/%s", stream.IngestPort, stream.StreamKey),
	)
	publisher.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	publisher.Stderr = os.Stderr
	// Give the listener a moment to bind before pushing.
	time.Sleep(800 * time.Millisecond)
	if err := publisher.Start(); err != nil {
		t.Fatalf("publisher start: %v", err)
	}
	t.Cleanup(func() {
		if publisher.Process != nil {
			_ = syscall.Kill(-publisher.Process.Pid, syscall.SIGTERM)
		}
		_ = publisher.Wait()
	})
	t.Logf("publisher pid=%d pushing testsrc 720p30 @ 2 Mbps", publisher.Process.Pid)

	// 3. Wait for the manifest to populate.
	indexPath := filepath.Join(dataDir, stream.StoragePrefix, "index.m3u8")
	deadline := time.Now().Add(45 * time.Second)
	startedWait := time.Now()
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for stream to go live (manifest=%s)", indexPath)
		}
		time.Sleep(500 * time.Millisecond)
		body, err := os.ReadFile(indexPath)
		if err != nil {
			continue
		}
		hasSeg := false
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				hasSeg = true
				break
			}
		}
		if hasSeg {
			t.Logf("live; manifest has segments after %.1fs", time.Since(startedWait).Seconds())
			break
		}
	}

	// Let one more segment land so the playlist isn't single-element.
	time.Sleep(2 * time.Second)

	// 4. Load test against the locally-served playback URL.
	playbackURL := fmt.Sprintf("http://localhost:%d/streams/%d/index.m3u8?t=%s&project_id=smoke-proj",
		httpPort, stream.ID, stream.PlaybackToken)
	t.Logf("playback_url %s", playbackURL)
	t.Logf("load_test viewers=%d duration=%ds", viewers, durationSec)

	startedAt := time.Now()
	result := runLoadTest(appCtx, playbackURL, viewers, durationSec)
	t.Logf("wall=%.1fs", time.Since(startedAt).Seconds())

	pretty, _ := json.MarshalIndent(result, "", "  ")
	t.Logf("\n=== load test result ===\n%s", string(pretty))

	// Sanity assertions — these are the smoke contract for "did the
	// engine actually serve real bytes".
	if result.ServedMbps <= 0 {
		t.Errorf("served_mbps=0 — engine didn't serve any bytes")
	}
	if result.HTTP5xx > 0 {
		t.Errorf("http_5xx=%d — server errors during load test", result.HTTP5xx)
	}
	// At 10 viewers × 2 Mbps × 20s ≈ 50 MB minimum. Be loose so the
	// assertion fires only when something is genuinely broken.
	expectedMin := int64(viewers) * int64(durationSec) * 100_000 // 100 KB/s/viewer
	if result.BytesServed < expectedMin {
		t.Errorf("bytes_served=%d, expected at least %d (10%% of nominal)",
			result.BytesServed, expectedMin)
	}

	// 5. Stop.
	if _, err := app.toolStop(appCtx, map[string]any{"id": stream.ID}); err != nil {
		t.Logf("stop error (non-fatal): %v", err)
	}
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
