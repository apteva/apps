package main

import (
	"context"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestSanitizedBuildEnv(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "platform-secret")
	t.Setenv("SENTRY_AUTH_TOKEN", "project-secret")

	base := envMap(sanitizedBuildEnv(nil))
	if _, ok := base["APTEVA_APP_TOKEN"]; ok {
		t.Fatal("APTEVA_APP_TOKEN leaked into build environment")
	}
	if _, ok := base["SENTRY_AUTH_TOKEN"]; ok {
		t.Fatal("sensitive project token was not blocked by default")
	}
	if _, ok := base["PATH"]; !ok {
		t.Fatal("PATH was removed from build environment")
	}

	allowed := envMap(sanitizedBuildEnv([]string{"SENTRY_AUTH_TOKEN", "APTEVA_APP_TOKEN"}))
	if allowed["SENTRY_AUTH_TOKEN"] != "project-secret" {
		t.Fatal("explicit build env allowlist was ignored")
	}
	if _, ok := allowed["APTEVA_APP_TOKEN"]; ok {
		t.Fatal("APTEVA_* credential must never be allowlisted")
	}
}

func TestRunBuildProcessCancelsProcessGroup(t *testing.T) {
	logFile, err := os.CreateTemp(t.TempDir(), "build-log-*")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = runBuildProcess(ctx, "/bin/sh", []string{"-c", "sleep 10"}, t.TempDir(), logFile, nil)
	if err == nil {
		t.Fatal("expected cancelled build error")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancelled build took %s to stop", elapsed)
	}
}

func TestEmulatorPortReservationIsAtomic(t *testing.T) {
	first, _, releaseFirst, err := allocateEmulatorPorts()
	if err != nil {
		t.Skipf("no emulator ports available: %v", err)
	}
	defer releaseFirst()
	second, _, releaseSecond, err := allocateEmulatorPorts()
	if err != nil {
		t.Skipf("only one emulator port pair available: %v", err)
	}
	defer releaseSecond()
	if first == second {
		t.Fatalf("concurrent reservations reused port %d", first)
	}
}

func TestStreamOriginPolicy(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "http://example.test/stream/sim", nil)
	req.Host = "example.test"
	req.Header.Set("Origin", "http://example.test")
	if !app.streamOriginAllowed(req) {
		t.Fatal("same-origin stream was rejected")
	}
	req.Header.Set("Origin", "https://attacker.test")
	if app.streamOriginAllowed(req) {
		t.Fatal("cross-origin stream was accepted")
	}
}

func TestStreamURLIncludesProjectScope(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "project-a")
	raw := (&App{}).streamURL(new(sdk.AppCtx), "sim-a", "secret-token")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("project_id"); got != "project-a" {
		t.Fatalf("project_id=%q, want project-a in %q", got, raw)
	}
	if got := u.Query().Get("t"); got != "secret-token" {
		t.Fatalf("token=%q, want secret-token", got)
	}
}

func TestActiveStreamsPerSimAreCappedAtTwo(t *testing.T) {
	app := &App{streams: map[string][]activeStream{}}
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	endFirst := app.beginStream("sim", firstCancel)
	defer endFirst()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	endSecond := app.beginStream("sim", secondCancel)
	defer endSecond()
	select {
	case <-firstCtx.Done():
		t.Fatal("second viewer cancelled the first viewer")
	default:
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("second viewer was cancelled unexpectedly")
	default:
	}

	thirdCtx, thirdCancel := context.WithCancel(context.Background())
	defer thirdCancel()
	endThird := app.beginStream("sim", thirdCancel)
	defer endThird()
	select {
	case <-firstCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("third viewer did not evict the oldest stream")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("third viewer cancelled the second viewer")
	default:
	}
	select {
	case <-thirdCtx.Done():
		t.Fatal("third viewer was cancelled unexpectedly")
	default:
	}
}

func TestHTTPRouteMethodsAreConstrained(t *testing.T) {
	routes := (&App{}).HTTPRoutes()
	want := map[string]string{
		"/api/capabilities": "GET",
		"/api/run":          "POST",
		"/api/sims":         "GET",
		"/api/sims/boot":    "POST",
	}
	for _, route := range routes {
		if method, ok := want[route.Pattern]; ok && route.Method != method {
			t.Errorf("route %s method=%q, want %q", route.Pattern, route.Method, method)
		}
	}
}

func TestInputAndLogLimits(t *testing.T) {
	if err := validateInputEvent(inputEvent{Kind: "tap", X: 0.5, Y: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := validateInputEvent(inputEvent{Kind: "tap", X: 2, Y: 0.5}); err == nil {
		t.Fatal("out-of-range tap accepted")
	}
	if err := validateInputEvent(inputEvent{Kind: "text", Text: strings.Repeat("x", maxInputTextBytes+1)}); err == nil {
		t.Fatal("oversized text input accepted")
	}
	if got := normalizeLogLines(1_000_000); got != 5000 {
		t.Fatalf("log limit=%d, want 5000", got)
	}
}

func TestCompressedArchiveReaderEnforcesHardLimit(t *testing.T) {
	reader := &hardLimitReader{r: strings.NewReader("1234"), max: 3}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("compressed archive reader accepted bytes beyond its limit")
	}
}

func TestCappedBufferBoundsCapturedCommandOutput(t *testing.T) {
	buffer := &cappedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("write=(%d,%v), want (8,nil)", n, err)
	}
	if got := buffer.String(); got != "abcd" {
		t.Fatalf("captured %q, want %q", got, "abcd")
	}
}

func TestValidateArtifactPath(t *testing.T) {
	root := t.TempDir()
	app := &App{artifactsDir: root}
	digest := strings.Repeat("a", 64)
	apk := filepath.Join(root, digest+".apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.validateArtifactPath(apk, "android"); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	outside := filepath.Join(t.TempDir(), digest+".apk")
	if err := os.WriteFile(outside, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.validateArtifactPath(outside, "android"); err == nil {
		t.Fatal("artifact outside storage root accepted")
	}
	symlink := filepath.Join(root, strings.Repeat("c", 64)+".apk")
	if err := os.Symlink(outside, symlink); err == nil {
		if _, err := app.validateArtifactPath(symlink, "android"); err == nil {
			t.Fatal("symlinked artifact accepted")
		}
	}
}

func TestCleanupStorageKeepsLatestAndPrunesOldUnreferenced(t *testing.T) {
	db := openTestDB(t)
	root := t.TempDir()
	app := &App{
		artifactsDir: filepath.Join(root, "artifacts"),
		simLogsDir:   filepath.Join(root, "sim-logs"),
	}
	if err := os.MkdirAll(app.artifactsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app.simLogsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertSim(db, Sim{ID: "sim", ProjectID: "p", Platform: "android", Status: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	oldArtifact := filepath.Join(app.artifactsDir, strings.Repeat("b", 64)+".apk")
	oldLog := filepath.Join(app.simLogsDir, "1.log")
	for _, path := range []string{oldArtifact, oldLog} {
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-storageRetention - time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	oldRun, err := dbInsertSimRun(db, SimRun{SimID: "sim", ProjectID: "p", SourceApp: "test", Status: "stopped", ArtifactPath: oldArtifact, LogPath: "1.log"})
	if err != nil {
		t.Fatal(err)
	}
	oldTimestamp := time.Now().Add(-storageRetention - time.Hour).Format(time.RFC3339)
	if err := dbUpdateSimRun(db, oldRun.ID, map[string]any{"started_at": oldTimestamp, "stopped_at": oldTimestamp}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbInsertSimRun(db, SimRun{SimID: "sim", ProjectID: "p", SourceApp: "test", Status: "running", StartedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := app.cleanupStorage(db); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldArtifact); !os.IsNotExist(err) {
		t.Fatalf("old artifact still exists: %v", err)
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatalf("old log still exists: %v", err)
	}
}

func envMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
