package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPNG() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 100, 200)))
	return b.Bytes()
}

type fakeBackend struct {
	mu          sync.Mutex
	source      string
	closeErr    error
	openErr     error
	action      func(context.Context) error
	actions     int
	inflight    int
	maxInflight int
	closes      int
}

func (b *fakeBackend) Open(_ context.Context, d Device, _ string) (string, error) {
	provider := ""
	if d.Driver == "appium" {
		provider = "fake-session"
	}
	return provider, b.openErr
}
func (b *fakeBackend) Close(context.Context, Session) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	return b.closeErr
}
func (b *fakeBackend) Observe(context.Context, Session, bool) (Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return observation(testPNG(), b.source)
}
func (b *fakeBackend) Action(ctx context.Context, _ Session, _ string, _ map[string]any) (any, error) {
	b.mu.Lock()
	b.actions++
	b.inflight++
	if b.inflight > b.maxInflight {
		b.maxInflight = b.inflight
	}
	hook := b.action
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.inflight--; b.mu.Unlock() }()
	if hook != nil {
		return nil, hook(ctx)
	}
	return map[string]any{"ok": true}, nil
}
func (b *fakeBackend) App(context.Context, Session, string, map[string]any) (any, error) {
	return map[string]any{"ok": true}, nil
}
func testDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(migrationSQL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func fixture(t *testing.T) (*engine, *fakeBackend) {
	t.Helper()
	dir := t.TempDir()
	db := testDB(t, filepath.Join(dir, "mobile.db"))
	d := newDrivers(runnerConfig{LockDir: filepath.Join(dir, "locks")})
	d.command = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("List of devices attached\nphone device model:Pixel_8\n"), nil
	}
	e, err := newEngine(db, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{source: "<node text=\"Welcome\"/>"}
	e.backend = fake
	t.Cleanup(e.Shutdown)
	return e, fake
}
func openFixture(t *testing.T, e *engine, pid string) Session {
	t.Helper()
	s, err := e.open(context.Background(), pid, map[string]any{"device_id": "android:phone"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func execute(t *testing.T, e Executor, pid, tool string, args map[string]any) any {
	t.Helper()
	v, err := e.Execute(context.Background(), pid, tool, args)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func waitRun(t *testing.T, e *engine, pid, rid string) TestRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := e.run(pid, rid)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != "queued" && r.Status != "running" {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("test did not finish")
	return TestRun{}
}
func submit(t *testing.T, e *engine, steps []Step) string {
	t.Helper()
	v := execute(t, e, "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "name": "Smoke", "build_ref": "build-immutable-42", "steps": steps})
	return v.(map[string]any)["run_id"].(string)
}

func TestExclusiveLeaseAndProjectIsolation(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "a")
	if _, err := e.open(context.Background(), "b", map[string]any{"device_id": "android:phone"}); errorCode(err) != "device_unavailable" {
		t.Fatalf("duplicate lease: %v", err)
	}
	for _, tool := range []string{"mobile_observe", "mobile_action", "mobile_app", "mobile_stream"} {
		if _, err := e.Execute(context.Background(), "b", tool, map[string]any{"session_id": s.ID}); errorCode(err) != "not_found" {
			t.Fatalf("cross-project %s: %v", tool, err)
		}
	}
	for _, action := range []string{"get", "renew", "close", "recover"} {
		if _, err := e.Execute(context.Background(), "b", "mobile_session", map[string]any{"session_id": s.ID, "action": action, "confirm": true}); errorCode(err) != "not_found" {
			t.Fatalf("cross-project session %s: %v", action, err)
		}
	}
	execute(t, e, "a", "mobile_session", map[string]any{"session_id": s.ID, "action": "close"})
	execute(t, e, "a", "mobile_session", map[string]any{"session_id": s.ID, "action": "close"})
	_ = openFixture(t, e, "b")
}
func TestFileLockAcrossIndependentEngines(t *testing.T) {
	e, _ := fixture(t)
	_ = openFixture(t, e, "a")
	dir := t.TempDir()
	db := testDB(t, filepath.Join(dir, "mobile.db"))
	d := newDrivers(runnerConfig{LockDir: e.drivers.cfg.LockDir})
	d.command = e.drivers.command
	other, err := newEngine(db, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Shutdown()
	other.backend = &fakeBackend{}
	if _, err = other.open(context.Background(), "a", map[string]any{"device_id": "android:phone"}); errorCode(err) != "device_unavailable" {
		t.Fatalf("separate engine acquired leased hardware: %v", err)
	}
}
func TestExpiredLeaseCannotActAndCanBeReused(t *testing.T) {
	e, b := fixture(t)
	s := openFixture(t, e, "p")
	_, _ = e.db.Exec(`UPDATE mobile_sessions SET expires_at=? WHERE id=?`, time.Now().Unix()-1, s.ID)
	if _, err := e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "tap"}); errorCode(err) != "session_expired" {
		t.Fatal(err)
	}
	if b.actions != 0 {
		t.Fatal("expired session dispatched input")
	}
	_ = openFixture(t, e, "p")
}
func TestRenewThenReapDoesNotExpireActiveLease(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	execute(t, e, "p", "mobile_session", map[string]any{"action": "renew", "session_id": s.ID, "ttl_seconds": 30})
	e.reap()
	fresh, err := e.session("p", s.ID)
	if err != nil || fresh.Status != "active" {
		t.Fatalf("renewed lease: %+v %v", fresh, err)
	}
}
func TestCommandsSerializedAndCloseWaits(t *testing.T) {
	e, b := fixture(t)
	s := openFixture(t, e, "p")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	b.action = func(ctx context.Context) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, _ = e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "tap"})
		}()
	}
	<-started
	time.Sleep(15 * time.Millisecond)
	b.mu.Lock()
	max := b.maxInflight
	b.mu.Unlock()
	if max != 1 {
		t.Fatalf("concurrent device actions: %d", max)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.Execute(context.Background(), "p", "mobile_session", map[string]any{"session_id": s.ID, "action": "close"})
	}()
	select {
	case <-done:
		t.Fatal("close raced with active command")
	case <-time.After(15 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	<-done
}
func TestCleanupFailureKeepsDeviceReserved(t *testing.T) {
	e, b := fixture(t)
	s := openFixture(t, e, "p")
	b.closeErr = errors.New("offline")
	if _, err := e.Execute(context.Background(), "p", "mobile_session", map[string]any{"session_id": s.ID, "action": "close"}); err == nil {
		t.Fatal("cleanup unexpectedly succeeded")
	}
	fresh, _ := e.session("p", s.ID)
	if fresh.Status != "cleanup_failed" {
		t.Fatal(fresh.Status)
	}
	if _, err := e.open(context.Background(), "p", map[string]any{"device_id": s.DeviceID}); err == nil {
		t.Fatal("dirty device reused")
	}
	b.mu.Lock()
	b.closeErr = nil
	b.mu.Unlock()
	e.reap()
	_ = openFixture(t, e, "p")
}
func TestUnknownAppiumCreationRequiresExplicitRecovery(t *testing.T) {
	e, b := fixture(t)
	e.drivers.cfg.DevicesJSON = `[{"id":"ios","platform":"ios","udid":"iphone","physical":true}]`
	e.drivers.cfg.AppiumURL = "http://127.0.0.1:4723"
	b.openErr = context.DeadlineExceeded
	if _, err := e.open(context.Background(), "p", map[string]any{"device_id": "ios"}); err == nil {
		t.Fatal("creation unexpectedly succeeded")
	}
	s, err := e.sessionForDevice("ios")
	if err != nil || s.Status != "cleanup_failed" {
		t.Fatalf("missing quarantine: %+v %v", s, err)
	}
	e.reap()
	s, _ = e.session("p", s.ID)
	if s.Status != "cleanup_failed" {
		t.Fatal("unknown outcome automatically released")
	}
	if _, err = e.Execute(context.Background(), "p", "mobile_session", map[string]any{"action": "recover", "session_id": s.ID}); err == nil {
		t.Fatal("unconfirmed recovery")
	}
	execute(t, e, "p", "mobile_session", map[string]any{"action": "recover", "session_id": s.ID, "confirm": true})
	b.openErr = nil
	_, err = e.open(context.Background(), "p", map[string]any{"device_id": "ios"})
	if err != nil {
		t.Fatal(err)
	}
}
func TestRestartInvalidatesSessionsAndRunningTests(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	e.Shutdown()
	// Simulate records written immediately before a process exited.
	_, _ = e.db.Exec(`UPDATE mobile_sessions SET status='active' WHERE id=?`, s.ID)
	_, _ = e.db.Exec(`INSERT INTO mobile_runs(id,project_id,device_id,name,status,created_at) VALUES ('restart','p','android:phone','run','running',?)`, time.Now().Unix())
	next, err := newEngine(e.db, e.dataDir, e.drivers)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Shutdown()
	s, err = next.session("p", s.ID)
	if err != nil || s.Status != "interrupted" {
		t.Fatalf("stale lease: %+v %v", s, err)
	}
	r, _ := next.run("p", "restart")
	if r.Status != "interrupted" {
		t.Fatal(r.Status)
	}
}
func TestDeterministicRunEvidenceAndBuildReference(t *testing.T) {
	e, _ := fixture(t)
	rid := submit(t, e, []Step{{Action: "navigate", Params: map[string]any{"url": "https://example.com"}}, {Action: "assert_text", Params: map[string]any{"text": "Welcome"}}})
	r := waitRun(t, e, "p", rid)
	if r.Status != "passed" || r.BuildRef != "build-immutable-42" || len(r.Results) != 2 {
		t.Fatalf("report: %+v", r)
	}
	for _, step := range r.Results {
		if step.ArtifactID == "" {
			t.Fatal("missing evidence")
		}
		if _, err := e.evidence("p", step.ArtifactID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.evidence("other", step.ArtifactID); errorCode(err) != "not_found" {
			t.Fatal("cross-project artifact read")
		}
	}
	if _, err := e.run("other", rid); errorCode(err) != "not_found" {
		t.Fatal("cross-project report read")
	}
}
func TestAssertionFailureIsNotInfrastructureFailure(t *testing.T) {
	e, _ := fixture(t)
	r := waitRun(t, e, "p", submit(t, e, []Step{{Action: "assert_text", Params: map[string]any{"text": "missing"}}}))
	if r.Status != "failed" || r.FailureKind != "assertion_failed" || r.Results[0].ArtifactID == "" {
		t.Fatalf("report: %+v", r)
	}
}

func TestRunCannotPassWhenCleanupFails(t *testing.T) {
	e, b := fixture(t)
	b.closeErr = errors.New("driver did not close")
	r := waitRun(t, e, "p", submit(t, e, []Step{{Action: "screenshot"}}))
	if r.Status != "failed" || r.FailureKind != "infrastructure_error" {
		t.Fatalf("passed with failed teardown: %+v", r)
	}
	b.mu.Lock()
	b.closeErr = nil
	b.mu.Unlock()
}
func TestDeviceBusyHasDistinctFailure(t *testing.T) {
	e, _ := fixture(t)
	_ = openFixture(t, e, "p")
	r := waitRun(t, e, "p", submit(t, e, []Step{{Action: "screenshot"}}))
	if r.FailureKind != "device_unavailable" {
		t.Fatalf("report: %+v", r)
	}
}
func TestCancellationInterruptsActionAndReleasesDevice(t *testing.T) {
	e, b := fixture(t)
	entered := make(chan struct{})
	b.action = func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
	rid := submit(t, e, []Step{{Action: "tap"}})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("action never started")
	}
	execute(t, e, "p", "mobile_test_cancel", map[string]any{"run_id": rid})
	r := waitRun(t, e, "p", rid)
	if r.Status != "cancelled" {
		t.Fatalf("report: %+v", r)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s, _ := e.session("p", r.SessionID)
		if s.Status == "closed" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cancelled run retained its lease")
}
func TestRejectsInvalidAndOversizedTests(t *testing.T) {
	e, _ := fixture(t)
	for _, steps := range []any{nil, []Step{}, []Step{{Action: "shell"}}, make([]Step, 51), []Step{{Action: "assert_text"}}} {
		if _, err := e.Execute(context.Background(), "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "steps": steps}); errorCode(err) != "invalid_argument" {
			t.Fatalf("bad plan accepted: %v %v", steps, err)
		}
	}
}
func TestReportsDoNotPersistTextInputs(t *testing.T) {
	e, _ := fixture(t)
	r := waitRun(t, e, "p", submit(t, e, []Step{{Action: "text", Params: map[string]any{"text": "secret-password"}}}))
	if strings.Contains(jsonText(r), "secret-password") {
		t.Fatal("text input persisted in report")
	}
}
func TestArtifactDirectoryBoundary(t *testing.T) {
	e, _ := fixture(t)
	root := e.drivers.cfg.ArtifactDir
	inside := filepath.Join(root, "test.apk")
	_ = os.WriteFile(inside, []byte("apk"), 0600)
	if _, err := e.drivers.installPath("test.apk", "android"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.apk")
	_ = os.WriteFile(outside, []byte("apk"), 0600)
	_ = os.Symlink(outside, filepath.Join(root, "escape.apk"))
	for _, name := range []string{outside, "escape.apk", "../../etc/passwd", "test.aab"} {
		if _, err := e.drivers.installPath(name, "android"); err == nil {
			t.Fatalf("accepted outside/wrong artifact %s", name)
		}
	}
	if _, err := e.drivers.installPath("test.apk", "ios"); err == nil {
		t.Fatal("APK accepted for iOS")
	}
}
func TestScreenshotValidation(t *testing.T) {
	obs, err := observation(testPNG(), "")
	if err != nil || obs.Width != 100 || obs.Height != 200 {
		t.Fatal(err)
	}
	if _, err = base64.StdEncoding.DecodeString(obs.Image); err != nil {
		t.Fatal(err)
	}
	if _, err = observation([]byte("not a PNG"), ""); err == nil {
		t.Fatal("accepted invalid screenshot")
	}
}
