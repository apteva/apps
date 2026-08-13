package main

// Smoke + unit tests for the backup app.
//
// What's covered:
//   - Manifest parses and lists every advertised tool
//   - Destination validation (per-kind config required)
//   - Local destination Put/Get/List/Delete round-trip
//   - DB inserts/selects for destinations, policies, runs
//   - Retention prune keeps the newest N
//   - Snapshot streamer respects gateway env + auth
//   - Restore handler decodes a successful run + posts to platform
//
// What's deferred to integration tests (with the spawned binary):
//   - The full sidecar boots and serves /health
//   - jobs integration (CallApp wiring)
//   - End-to-end snapshot → upload → restore round-trip against
//     a real apteva-server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	_ "modernc.org/sqlite"
)

// ─── helpers ───────────────────────────────────────────────────────

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "backup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, file := range files {
		migration, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("migration %s: %v", file, err)
		}
	}
	return db
}

func newTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	db := openTestDB(t)
	m := (&App{}).Manifest()
	return sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, &silentLogger{})
}

type backupPlatform struct {
	tk.BasePlatformClient
	snapshot       []byte
	snapshotErr    error
	waitForContext bool
	restoreReport  map[string]any
	restoreErr     error
	restoreBody    []byte
	restoreSize    int64
}

func (p *backupPlatform) OpenPlatformSnapshot(ctx context.Context) (io.ReadCloser, error) {
	if p.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if p.snapshotErr != nil {
		return nil, p.snapshotErr
	}
	return io.NopCloser(bytes.NewReader(p.snapshot)), nil
}

func (p *backupPlatform) RestorePlatformSnapshot(_ context.Context, body io.Reader, size int64) (map[string]any, error) {
	p.restoreSize = size
	p.restoreBody, _ = io.ReadAll(body)
	if p.restoreErr != nil {
		return nil, p.restoreErr
	}
	return p.restoreReport, nil
}

func newBackupTestCtx(t *testing.T, platform *backupPlatform) *sdk.AppCtx {
	t.Helper()
	manifest := (&App{}).Manifest()
	return sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, platform, silentLogger{})
}

type silentLogger struct{}

func (silentLogger) Info(string, ...any)  {}
func (silentLogger) Warn(string, ...any)  {}
func (silentLogger) Error(string, ...any) {}

// ─── manifest ──────────────────────────────────────────────────────

func TestManifest_Parses(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "backup" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Errorf("version is empty")
	}
	if len(m.Provides.MCPTools) != 4 {
		t.Errorf("expected 4 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	wantTools := map[string]bool{"backup_now": false, "backup_schedule": false, "backup_list": false, "backup_restore": false}
	for _, tool := range m.Provides.MCPTools {
		wantTools[tool.Name] = true
	}
	for name, seen := range wantTools {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestManifest_ToolsMatchHandlers(t *testing.T) {
	a := &App{}
	manifestNames := map[string]bool{}
	for _, t := range a.Manifest().Provides.MCPTools {
		manifestNames[t.Name] = true
	}
	for _, tool := range a.MCPTools() {
		if !manifestNames[tool.Name] {
			t.Errorf("tool %q has a handler but isn't in the manifest", tool.Name)
		}
	}
}

func TestManifestsAgreeOnReleaseContract(t *testing.T) {
	embedded := (&App{}).Manifest()
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if embedded.Version != disk.Version || embedded.Runtime.Source == nil || disk.Runtime.Source == nil || embedded.Runtime.Source.Ref != disk.Runtime.Source.Ref {
		t.Fatalf("manifest release drift: embedded=%s/%v disk=%s/%v", embedded.Version, embedded.Runtime.Source, disk.Version, disk.Runtime.Source)
	}
	for label, manifest := range map[string]sdk.Manifest{"embedded": embedded, "disk": *disk} {
		permissions := map[string]bool{}
		for _, permission := range manifest.Requires.Permissions {
			permissions[string(permission)] = true
		}
		if !permissions["platform.connections.read_credentials"] {
			t.Errorf("%s manifest is missing restricted credential permission", label)
		}
		if !permissions["platform.backup.read"] || !permissions["platform.backup.restore"] {
			t.Errorf("%s manifest is missing dedicated platform backup permissions", label)
		}
		if permissions["platform.connections.execute"] {
			t.Errorf("%s manifest retains unused connection execution permission", label)
		}
	}
}

// ─── destination validation ────────────────────────────────────────

func TestValidateDestination(t *testing.T) {
	cases := []struct {
		name string
		in   Destination
		ok   bool
	}{
		{"missing name", Destination{Kind: "local", Config: json.RawMessage(`{"path":"/x"}`)}, false},
		{"missing kind", Destination{Name: "n", Config: json.RawMessage(`{}`)}, false},
		{"local relative path", Destination{Name: "n", Kind: "local", Config: json.RawMessage(`{"path":"rel"}`)}, false},
		{"local ok", Destination{Name: "n", Kind: "local", Config: json.RawMessage(`{"path":"/abs"}`)}, true},
		{"s3 missing bucket", Destination{Name: "n", Kind: "s3", Config: json.RawMessage(`{}`)}, false},
		// Config validation is independent of binding resolution. The
		// create handler records the currently bound connection ID.
		{"s3 ok no connection_id", Destination{Name: "n", Kind: "s3", Config: json.RawMessage(`{"bucket":"b"}`)}, true},
		{"unknown kind", Destination{Name: "n", Kind: "weird", Config: json.RawMessage(`{}`)}, false},
		{"storage_app reserved", Destination{Name: "n", Kind: "storage_app", Config: json.RawMessage(`{}`)}, false},
	}
	for _, c := range cases {
		err := validateDestination(&c.in)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// ─── local destination ─────────────────────────────────────────────

func TestLocalDestination_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	d := &localDest{cfg: localConfig{Path: dir}}
	ctx := context.Background()

	payload := []byte("hello, backup")
	if err := d.Put(ctx, "apteva-1.tar.gz", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	rc, err := d.Get(ctx, "apteva-1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, payload) {
		t.Errorf("get returned %q want %q", got, payload)
	}
	objs, err := d.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Key != "apteva-1.tar.gz" || objs[0].Size != int64(len(payload)) {
		t.Errorf("list = %+v", objs)
	}
	if err := d.Delete(ctx, "apteva-1.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get(ctx, "apteva-1.tar.gz"); err == nil {
		t.Errorf("expected get-after-delete to fail")
	}
	// Delete is idempotent.
	if err := d.Delete(ctx, "apteva-1.tar.gz"); err != nil {
		t.Errorf("second delete should be no-op, got %v", err)
	}
}

func TestLocalDestination_ListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	d := &localDest{cfg: localConfig{Path: dir}}
	ctx := context.Background()

	// Write three files with synthetic mtimes so the order is
	// deterministic regardless of filesystem timestamp resolution.
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("apteva-%d.tar.gz", i)
		if err := d.Put(ctx, key, bytes.NewReader([]byte("x")), 1); err != nil {
			t.Fatal(err)
		}
		when := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		_ = os.Chtimes(filepath.Join(dir, key), when, when)
	}
	objs, _ := d.List(ctx, "")
	if len(objs) != 3 {
		t.Fatalf("want 3, got %d", len(objs))
	}
	if objs[0].Key != "apteva-2.tar.gz" {
		t.Errorf("expected newest first, got %s", objs[0].Key)
	}
}

func TestLocalDestination_RejectsPathTraversal(t *testing.T) {
	d := &localDest{cfg: localConfig{Path: t.TempDir()}}
	for _, key := range []string{"../outside", "/absolute", "nested/../../outside"} {
		if err := d.Put(context.Background(), key, strings.NewReader("no"), 2); err == nil {
			t.Errorf("Put(%q) accepted a key outside the destination", key)
		}
		if _, err := d.Get(context.Background(), key); err == nil {
			t.Errorf("Get(%q) accepted a key outside the destination", key)
		}
		if err := d.Delete(context.Background(), key); err == nil {
			t.Errorf("Delete(%q) accepted a key outside the destination", key)
		}
	}
}

// ─── DB layer ──────────────────────────────────────────────────────

func TestDestinationCRUD(t *testing.T) {
	db := openTestDB(t)
	in := &Destination{Name: "nightly", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`)}
	out, err := dbCreateDestination(db, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID == 0 {
		t.Errorf("no id assigned")
	}
	if !out.Enabled {
		t.Errorf("new destination should default to enabled")
	}
	listed, _ := dbListDestinations(db)
	if len(listed) != 1 {
		t.Errorf("want 1 listed, got %d", len(listed))
	}
	got, err := dbGetDestination(db, out.ID)
	if err != nil || got.Name != "nightly" {
		t.Errorf("get failed: %v / %+v", err, got)
	}
}

func TestSoftDeleteDestination_HidesButPreservesForRestore(t *testing.T) {
	db := openTestDB(t)
	dest, err := dbCreateDestination(db, &Destination{
		Name: "restore-source", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbSoftDeleteDestination(db, dest.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := dbListDestinations(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("soft-deleted destination is still listed: %+v", listed)
	}
	got, err := dbGetDestination(db, dest.ID)
	if err != nil {
		t.Fatalf("soft-deleted destination must remain restorable: %v", err)
	}
	if got.Enabled || !strings.Contains(got.Name, "-deleted-") {
		t.Fatalf("unexpected soft-deleted row: %+v", got)
	}
	if err := dbSoftDeleteDestination(db, dest.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second delete error = %v, want sql.ErrNoRows", err)
	}
}

func TestSoftDeleteDestination_RejectsPolicyReference(t *testing.T) {
	db := openTestDB(t)
	dest, _ := dbCreateDestination(db, &Destination{Name: "d", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`)})
	_, _ = dbCreatePolicy(db, &Policy{Name: "p", Schedule: "0 3 * * *", DestinationID: dest.ID})
	if err := dbSoftDeleteDestination(db, dest.ID); !errors.Is(err, errDestinationInUse) {
		t.Fatalf("delete error = %v, want errDestinationInUse", err)
	}
}

func TestPolicyCRUD(t *testing.T) {
	db := openTestDB(t)
	dest, _ := dbCreateDestination(db, &Destination{
		Name: "d", Kind: "local", Config: json.RawMessage(`{"path":"/x"}`),
	})
	p := &Policy{Name: "nightly", Schedule: "0 3 * * *", DestinationID: dest.ID, RetentionKeep: 7}
	out, err := dbCreatePolicy(db, p)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID == 0 {
		t.Errorf("no id")
	}
	got, _ := dbGetPolicy(db, out.ID)
	if got.Schedule != "0 3 * * *" {
		t.Errorf("schedule mismatch")
	}
	all, _ := dbListPolicies(db)
	if len(all) != 1 {
		t.Errorf("want 1, got %d", len(all))
	}
}

func TestRunLifecycle(t *testing.T) {
	db := openTestDB(t)
	dest, _ := dbCreateDestination(db, &Destination{
		Name: "d", Kind: "local", Config: json.RawMessage(`{"path":"/x"}`),
	})
	r := &Run{DestinationID: dest.ID, DestinationName: dest.Name}
	id, err := dbInsertRun(db, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbFinishRun(db, id, "success", 1234, "deadbeef", "platform/adhoc/apteva-x.tar.gz", `{"format_version":1}`, "", true); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetRun(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "success" || got.BytesCompressed != 1234 || got.SHA256 != "deadbeef" || !got.Encrypted {
		t.Errorf("finish fields wrong: %+v", got)
	}
	if got.FinishedAt == "" {
		t.Errorf("finished_at not set")
	}
	listed, _ := dbListRuns(db, dest.ID, 10)
	if len(listed) != 1 {
		t.Errorf("want 1 listed, got %d", len(listed))
	}
}

// ─── pickDestination ───────────────────────────────────────────────

func TestPickDestination_NoDests(t *testing.T) {
	db := openTestDB(t)
	if _, err := pickDestination(db, 0); err == nil {
		t.Errorf("expected error on no destinations")
	}
}

func TestPickDestination_OneDest(t *testing.T) {
	db := openTestDB(t)
	d, _ := dbCreateDestination(db, &Destination{
		Name: "only", Kind: "local", Config: json.RawMessage(`{"path":"/x"}`),
	})
	got, err := pickDestination(db, 0)
	if err != nil || got.ID != d.ID {
		t.Errorf("pick failed: err=%v got=%+v", err, got)
	}
}

func TestPickDestination_ManyDestsRequiresExplicit(t *testing.T) {
	db := openTestDB(t)
	_, _ = dbCreateDestination(db, &Destination{Name: "a", Kind: "local", Config: json.RawMessage(`{"path":"/a"}`)})
	_, _ = dbCreateDestination(db, &Destination{Name: "b", Kind: "local", Config: json.RawMessage(`{"path":"/b"}`)})
	if _, err := pickDestination(db, 0); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Errorf("expected multi-dest error, got %v", err)
	}
}

func TestPickDestination_RejectsSoftDeletedExplicitID(t *testing.T) {
	db := openTestDB(t)
	dest, _ := dbCreateDestination(db, &Destination{Name: "old", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`)})
	if err := dbSoftDeleteDestination(db, dest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pickDestination(db, dest.ID); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("pick error = %v", err)
	}
}

// ─── retention ─────────────────────────────────────────────────────

func TestPruneRetention_KeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	dest := &Destination{ID: 1, Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`)}
	w := &localDest{cfg: localConfig{Path: dir}}

	ctx := newTestCtx(t)
	keys := []string{
		"platform/policy-1/apteva-20260101-000000.tar.gz",
		"platform/policy-1/apteva-20260102-000000.tar.gz",
		"platform/policy-1/apteva-20260103-000000.tar.gz",
		"platform/policy-1/apteva-20260104-000000.tar.gz",
		"platform/policy-1/apteva-20260105-000000.tar.gz",
	}
	for i, key := range keys {
		if err := w.Put(context.Background(), key, bytes.NewReader([]byte("x")), 1); err != nil {
			t.Fatal(err)
		}
		when := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		_ = os.Chtimes(filepath.Join(dir, key), when, when)
	}
	policy := &Policy{ID: 1, RetentionKeep: 2, Scope: defaultScope()}
	if err := pruneRetention(context.Background(), ctx, w, dest, policy); err != nil {
		t.Fatal(err)
	}
	objs, _ := w.List(context.Background(), "")
	if len(objs) != 2 {
		t.Errorf("want 2 left after prune, got %d (%v)", len(objs), objs)
	}
	got := []string{objs[0].Key, objs[1].Key}
	want := map[string]bool{
		"platform/policy-1/apteva-20260104-000000.tar.gz": true,
		"platform/policy-1/apteva-20260105-000000.tar.gz": true,
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected survivor %s — newest two should remain", k)
		}
	}
}

func TestPruneRetention_IgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	dest := &Destination{ID: 1, Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`)}
	w := &localDest{cfg: localConfig{Path: dir}}
	ctx := newTestCtx(t)

	// One apteva file + one stranger-in-the-bucket. Keep=1 should be a no-op
	// because there's only one apteva file; the stranger must survive.
	_ = w.Put(context.Background(), "platform/policy-1/apteva-x.tar.gz", bytes.NewReader([]byte("y")), 1)
	_ = w.Put(context.Background(), "operator-readme.txt", bytes.NewReader([]byte("hands off")), 9)

	if err := pruneRetention(context.Background(), ctx, w, dest, &Policy{ID: 1, RetentionKeep: 1, Scope: defaultScope()}); err != nil {
		t.Fatal(err)
	}
	objs, _ := w.List(context.Background(), "")
	if len(objs) != 2 {
		t.Errorf("expected both files to survive, got %d", len(objs))
	}
}

func TestPruneRetention_IsolatedByPolicyAndScope(t *testing.T) {
	dir := t.TempDir()
	dest := &Destination{ID: 1, Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`)}
	w := &localDest{cfg: localConfig{Path: dir}}
	ctx := newTestCtx(t)
	keys := []string{
		"platform/policy-1/apteva-old.tar.gz",
		"platform/policy-1/apteva-new.tar.gz.age",
		"platform/policy-2/apteva-other-policy.tar.gz",
		"fleet_tenant/tenant-1/policy-1/apteva-other-scope.tar.gz",
	}
	for i, key := range keys {
		if err := w.Put(context.Background(), key, strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
		when := time.Date(2026, 2, i+1, 0, 0, 0, 0, time.UTC)
		_ = os.Chtimes(filepath.Join(dir, key), when, when)
	}
	if err := pruneRetention(context.Background(), ctx, w, dest, &Policy{ID: 1, RetentionKeep: 1, Scope: defaultScope()}); err != nil {
		t.Fatal(err)
	}
	objects, err := w.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, object := range objects {
		remaining[object.Key] = true
	}
	if remaining[keys[0]] || !remaining[keys[1]] || !remaining[keys[2]] || !remaining[keys[3]] {
		t.Fatalf("retention crossed a namespace boundary: %#v", remaining)
	}
}

func TestLocalDestination_ListUsesPrefix(t *testing.T) {
	dir := t.TempDir()
	w := &localDest{cfg: localConfig{Path: dir}}
	for _, key := range []string{
		"platform/policy-1/apteva-one.tar.gz",
		"platform/policy-2/apteva-two.tar.gz",
		"unrelated.txt",
	} {
		if err := w.Put(context.Background(), key, strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := w.List(context.Background(), "platform/policy-1/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != "platform/policy-1/apteva-one.tar.gz" {
		t.Fatalf("prefix list returned %#v", objects)
	}
}

func makeSnapshotArchive(t *testing.T, includeManifest bool) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if includeManifest {
		manifest := []byte(`{"format_version":1}`)
		if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(manifest); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte("database")
	if err := tw.WriteHeader(&tar.Header{Name: "server/apteva-server.db", Mode: 0o600, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestValidateSnapshotArchiveReadsThroughGzipTrailer(t *testing.T) {
	valid := makeSnapshotArchive(t, true)
	path := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := validateSnapshotArchive(path)
	if err != nil || !strings.Contains(manifest, "format_version") {
		t.Fatalf("valid archive: manifest=%q err=%v", manifest, err)
	}
	if err := os.WriteFile(path, valid[:len(valid)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateSnapshotArchive(path); err == nil {
		t.Fatal("truncated gzip archive was accepted")
	}
	if err := os.WriteFile(path, makeSnapshotArchive(t, false), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateSnapshotArchive(path); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("missing-manifest error = %v", err)
	}
}

func TestOperationCoordinatorExcludesBackupAndRestore(t *testing.T) {
	release, err := acquireOperation("backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireOperation("restore"); err == nil || !strings.Contains(err.Error(), "backup is running") {
		t.Fatalf("concurrent operation error = %v", err)
	}
	release()
	releaseRestore, err := acquireOperation("restore")
	if err != nil {
		t.Fatal(err)
	}
	releaseRestore()
}

func TestReconcileInterruptedRuns(t *testing.T) {
	ctx := newTestCtx(t)
	dest, _ := dbCreateDestination(ctx.AppDB(), &Destination{Name: "local", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`)})
	id, _ := dbInsertRun(ctx.AppDB(), &Run{DestinationID: dest.ID, DestinationName: dest.Name})
	if err := reconcileInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := dbGetRun(ctx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.Stage != "failed" || !strings.Contains(run.Error, "restarted") {
		t.Fatalf("interrupted run was not reconciled: %+v", run)
	}
}

func TestPruneFailedRunHistoryKeepsSuccessfulRuns(t *testing.T) {
	ctx := newTestCtx(t)
	old := time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339Nano)
	failedID, err := dbInsertRun(ctx.AppDB(), &Run{DestinationID: 1, DestinationName: "local", StartedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbFinishRun(ctx.AppDB(), failedID, "failed", 0, "", "", "", "old failure", false); err != nil {
		t.Fatal(err)
	}
	successID, err := dbInsertRun(ctx.AppDB(), &Run{DestinationID: 1, DestinationName: "local", StartedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbFinishRun(ctx.AppDB(), successID, "success", 1, "sha", "key", "{}", "", false); err != nil {
		t.Fatal(err)
	}
	if err := pruneFailedRunHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dbGetRun(ctx.AppDB(), failedID); err == nil {
		t.Fatal("old failed run was not pruned")
	}
	if _, err := dbGetRun(ctx.AppDB(), successID); err != nil {
		t.Fatalf("successful run was pruned: %v", err)
	}
}

func TestAnnotateRestoreReportSurfacesPartialFailure(t *testing.T) {
	report := annotateRestoreReport(map[string]any{
		"installs": []any{
			map[string]any{"install_id": float64(1), "status": "applied"},
			map[string]any{"install_id": float64(2), "archive_path": "apps/2-crm/app.db", "status": "error", "note": "missing install"},
		},
	})
	if report["partial_failure"] != true || report["failure_count"] != 1 {
		t.Fatalf("partial report = %#v", report)
	}
	failures, _ := report["failures"].([]string)
	if len(failures) != 1 || !strings.Contains(failures[0], "missing install") {
		t.Fatalf("failures = %#v", failures)
	}
	missingFields := annotateRestoreReport(map[string]any{
		"installs": []any{map[string]any{"status": "error"}},
	})
	missingFailures, _ := missingFields["failures"].([]string)
	if len(missingFailures) != 1 || missingFailures[0] != "install" {
		t.Fatalf("missing field failure = %#v", missingFailures)
	}
}

func TestRunBackup_FailureIsReturnedAndRecorded(t *testing.T) {
	ctx := newTestCtx(t)
	dir := t.TempDir()
	dest, err := dbCreateDestination(ctx.AppDB(), &Destination{
		Name: "local", Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runBackup(ctx, dest, nil, defaultScope())
	if err == nil || !strings.Contains(err.Error(), "server does not support app-authorized platform backups") {
		t.Fatalf("run error = %v", err)
	}
	if run == nil || run.Status != "failed" || run.Error == "" {
		t.Fatalf("failed run was not recorded: %+v", run)
	}
}

func TestEncryptedSnapshot_RoundTripAndWrongPassphrase(t *testing.T) {
	raw := []byte("synthetic tar.gz bytes that must not remain visible")
	rawPath := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rawHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{"encryption_passphrase": "correct horse battery staple"}, nil, silentLogger{})
	storedPath, storedSize, storedHash, encrypted, cleanup, err := prepareStoredSnapshot(ctx, rawPath, int64(len(raw)), rawHash)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !encrypted || storedSize <= 0 || storedHash == rawHash {
		t.Fatalf("unexpected encrypted metadata: encrypted=%v size=%d sha=%q", encrypted, storedSize, storedHash)
	}
	storedBytes, _ := os.ReadFile(storedPath)
	if bytes.Contains(storedBytes, raw) {
		t.Fatal("encrypted object contains the plaintext")
	}
	decryptedPath, cleanupDecrypted, err := decryptStoredSnapshot(ctx, storedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupDecrypted()
	decrypted, _ := os.ReadFile(decryptedPath)
	if !bytes.Equal(decrypted, raw) {
		t.Fatalf("decrypted bytes differ: %q", decrypted)
	}
	wrongCtx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{"encryption_passphrase": "wrong"}, nil, silentLogger{})
	if _, _, err := decryptStoredSnapshot(wrongCtx, storedPath); err == nil {
		t.Fatal("wrong passphrase unexpectedly decrypted the backup")
	}
}

func TestRestoreRejectsIntegrityMismatch(t *testing.T) {
	ctx := newTestCtx(t)
	dir := t.TempDir()
	dest, _ := dbCreateDestination(ctx.AppDB(), &Destination{
		Name: "local", Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`),
	})
	key := "platform/adhoc/apteva-corrupt.tar.gz"
	w := &localDest{cfg: localConfig{Path: dir}}
	if err := w.Put(context.Background(), key, strings.NewReader("corrupted"), 9); err != nil {
		t.Fatal(err)
	}
	run := &Run{DestinationID: dest.ID, DestinationName: dest.Name, Scope: defaultScope()}
	id, _ := dbInsertRun(ctx.AppDB(), run)
	if err := dbFinishRun(ctx.AppDB(), id, "success", 9, strings.Repeat("0", 64), key, "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreFromRun(ctx, id); err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("restore error = %v", err)
	}
}

type jobsPlatform struct {
	tk.BasePlatformClient
	scheduleErr error
	input       map[string]any
}

func (p *jobsPlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	if appName != "jobs" || tool != "jobs_cancel" {
		return nil, fmt.Errorf("unexpected app call %s.%s", appName, tool)
	}
	return json.RawMessage(`{"cancelled":true}`), nil
}

func (p *jobsPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if appName != "jobs" || tool != "jobs_schedule" {
		return fmt.Errorf("unexpected app call %s.%s", appName, tool)
	}
	p.input = input
	if p.scheduleErr != nil {
		return p.scheduleErr
	}
	raw, _ := json.Marshal(map[string]any{"job": map[string]any{"id": 42}})
	return json.Unmarshal(raw, out)
}

func TestBackupSchedule_UsesAppToolAndStoresProject(t *testing.T) {
	db := openTestDB(t)
	platform := &jobsPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, platform, silentLogger{})
	dest, _ := dbCreateDestination(db, &Destination{Name: "local", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`)})
	out, err := (&App{}).toolBackupSchedule(ctx, map[string]any{
		"name": "nightly", "schedule": "0 3 * * *", "destination_id": dest.ID, "project_id": "project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := out.(map[string]any)["policy"].(*Policy)
	if policy.JobsID != "42" || policy.JobsProjectID != "project-a" {
		t.Fatalf("jobs metadata not persisted: %+v", policy)
	}
	target, _ := platform.input["target"].(map[string]any)
	if target["kind"] != "app_tool" || target["app"] != "backup" || target["tool"] != "backup_now" {
		t.Fatalf("unexpected scheduled target: %#v", target)
	}
	input, _ := target["input"].(map[string]any)
	if input["policy_id"] != policy.ID {
		t.Fatalf("scheduled input = %#v, want policy_id %d", input, policy.ID)
	}
	if input["async"] != true {
		t.Fatalf("scheduled backup must be asynchronous: %#v", input)
	}
}

func TestBackupSchedule_RollsBackPolicyWhenJobsFails(t *testing.T) {
	db := openTestDB(t)
	platform := &jobsPlatform{scheduleErr: errors.New("jobs unavailable")}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, platform, silentLogger{})
	dest, _ := dbCreateDestination(db, &Destination{Name: "local", Kind: "local", Config: json.RawMessage(`{"path":"/tmp"}`)})
	_, err := (&App{}).toolBackupSchedule(ctx, map[string]any{
		"name": "nightly", "schedule": "0 3 * * *", "destination_id": dest.ID,
	})
	if err == nil || !strings.Contains(err.Error(), "jobs unavailable") {
		t.Fatalf("schedule error = %v", err)
	}
	policies, listErr := dbListPolicies(db)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(policies) != 0 {
		t.Fatalf("failed schedule left policy rows: %+v", policies)
	}
}

func TestHTTPRoutes_LocalLifecycle(t *testing.T) {
	db := openTestDB(t)
	platform := &jobsPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, platform, silentLogger{})
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{}
	dir := t.TempDir()

	request := func(method, target, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		recorder := httptest.NewRecorder()
		handler(recorder, req)
		return recorder
	}

	created := request(http.MethodPost, "/destinations", fmt.Sprintf(`{"name":"local","kind":"local","config":{"path":%q}}`, dir), app.handleDestinationsCollection)
	if created.Code != http.StatusOK {
		t.Fatalf("create destination: status=%d body=%s", created.Code, created.Body.String())
	}
	destinations, _ := dbListDestinations(db)
	if len(destinations) != 1 {
		t.Fatalf("destinations = %#v", destinations)
	}
	destinationID := destinations[0].ID

	checked := request(http.MethodPost, fmt.Sprintf("/destinations/%d/test", destinationID), "", app.handleDestinationItem)
	if checked.Code != http.StatusOK {
		t.Fatalf("test destination: status=%d body=%s", checked.Code, checked.Body.String())
	}
	listed := request(http.MethodGet, "/destinations", "", app.handleDestinationsCollection)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"local"`) {
		t.Fatalf("list destinations: status=%d body=%s", listed.Code, listed.Body.String())
	}

	policyBody := fmt.Sprintf(`{"name":"nightly","schedule":"0 3 * * *","destination_id":%d,"retention_keep":2,"scope":{"kind":"platform"}}`, destinationID)
	policyResponse := request(http.MethodPost, "/policies?project_id=project-a", policyBody, app.handlePoliciesCollection)
	if policyResponse.Code != http.StatusOK {
		t.Fatalf("create policy: status=%d body=%s", policyResponse.Code, policyResponse.Body.String())
	}
	policies, _ := dbListPolicies(db)
	if len(policies) != 1 || policies[0].JobsID == "" {
		t.Fatalf("policies = %#v", policies)
	}

	runResponse := request(http.MethodPost, "/run", fmt.Sprintf(`{"destination_id":%d}`, destinationID), app.handleRunNow)
	if runResponse.Code != http.StatusInternalServerError || !strings.Contains(runResponse.Body.String(), "server does not support app-authorized platform backups") {
		t.Fatalf("run now: status=%d body=%s", runResponse.Code, runResponse.Body.String())
	}
	allRuns, _ := dbListRuns(db, destinationID, 10)
	if len(allRuns) != 1 || allRuns[0].Status != "failed" {
		t.Fatalf("failed runs = %#v", allRuns)
	}
	runItem := request(http.MethodGet, fmt.Sprintf("/runs/%d", allRuns[0].ID), "", app.handleRunItem)
	if runItem.Code != http.StatusOK {
		t.Fatalf("run item: status=%d body=%s", runItem.Code, runItem.Body.String())
	}
	restoreResponse := request(http.MethodPost, "/restore", fmt.Sprintf(`{"run_id":%d,"confirm":true}`, allRuns[0].ID), app.handleRestore)
	if restoreResponse.Code != http.StatusInternalServerError || !strings.Contains(restoreResponse.Body.String(), "only successful") {
		t.Fatalf("restore failed run: status=%d body=%s", restoreResponse.Code, restoreResponse.Body.String())
	}

	runs := request(http.MethodGet, "/runs?limit=1", "", app.handleRunsCollection)
	if runs.Code != http.StatusOK || !strings.Contains(runs.Body.String(), `"has_more":false`) {
		t.Fatalf("list runs: status=%d body=%s", runs.Code, runs.Body.String())
	}
	scopes := request(http.MethodGet, "/scopes", "", app.handleScopes)
	if scopes.Code != http.StatusOK || !strings.Contains(scopes.Body.String(), `"platform"`) {
		t.Fatalf("scopes: status=%d body=%s", scopes.Code, scopes.Body.String())
	}

	deletedPolicy := request(http.MethodDelete, fmt.Sprintf("/policies/%d", policies[0].ID), "", app.handlePolicyItem)
	if deletedPolicy.Code != http.StatusOK {
		t.Fatalf("delete policy: status=%d body=%s", deletedPolicy.Code, deletedPolicy.Body.String())
	}
	deletedDestination := request(http.MethodDelete, fmt.Sprintf("/destinations/%d", destinationID), "", app.handleDestinationItem)
	if deletedDestination.Code != http.StatusOK {
		t.Fatalf("delete destination: status=%d body=%s", deletedDestination.Code, deletedDestination.Body.String())
	}
}

type cloudPlatform struct {
	tk.BasePlatformClient
	credentialID int64
}

func (p *cloudPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"cloud_storage": int64(77)}}, nil
}

func (p *cloudPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "cloudflare-r2", Status: "active"}, nil
}

func (p *cloudPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	p.credentialID = id
	return &sdk.ConnectionCredentials{
		ConnectionID: id,
		Slug:         "cloudflare-r2",
		Fields: map[string]string{
			"account_id": "account", "access_key_id": "access", "secret_access_key": "secret",
		},
	}, nil
}

func TestOpenCloudDestination_UsesBoundCredentialAPI(t *testing.T) {
	platform := &cloudPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, platform, silentLogger{})
	dest := &Destination{
		Name: "r2", Kind: "s3", ConnectionID: 77,
		Config: json.RawMessage(`{"bucket":"backups","key_prefix":"prod"}`),
	}
	writer, err := openDestination(dest, ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := writer.(*cloudDest); !ok || platform.credentialID != 77 {
		t.Fatalf("writer=%T credential_id=%d", writer, platform.credentialID)
	}
	dest.ConnectionID = 88
	if _, err := openDestination(dest, ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "currently bound") {
		t.Fatalf("connection mismatch error = %v", err)
	}
}

func TestCloudDestination_RoundTripAndPrefixList(t *testing.T) {
	var stored []byte
	var deleted bool
	modified := time.Now().UTC().Format(http.TimeFormat)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>backups</Name><Prefix>root/platform/policy-1/</Prefix><KeyCount>1</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>
  <Contents><Key>root/platform/policy-1/apteva-one.tar.gz</Key><LastModified>%s</LastModified><ETag>&quot;abc&quot;</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>
</ListBucketResult>`, time.Now().UTC().Format(time.RFC3339), len(stored))
			return
		}
		switch r.Method {
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			stored = raw
			if bytes.Contains(raw, []byte("snapshot")) {
				stored = []byte("snapshot")
			}
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			w.Header().Set("Content-Length", strconv.Itoa(len(stored)))
			w.Header().Set("Last-Modified", modified)
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.Header().Set("Content-Length", strconv.Itoa(len(stored)))
			w.Header().Set("Last-Modified", modified)
			w.Header().Set("ETag", `"abc"`)
			_, _ = w.Write(stored)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("access", "secret", ""), Secure: false,
		Region: "us-east-1", BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := &cloudDest{cfg: s3Config{Bucket: "backups", KeyPrefix: "root"}, client: client, region: "us-east-1"}
	key := "platform/policy-1/apteva-one.tar.gz"
	if err := destination.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := destination.Put(context.Background(), key, strings.NewReader("snapshot"), 8); err != nil {
		t.Fatal(err)
	}
	body, err := destination.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(got) != "snapshot" {
		t.Fatalf("get body=%q err=%v", got, err)
	}
	objects, err := destination.List(context.Background(), "platform/policy-1/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != key {
		t.Fatalf("objects = %#v", objects)
	}
	if err := destination.Delete(context.Background(), key); err != nil || !deleted {
		t.Fatalf("delete err=%v deleted=%v", err, deleted)
	}
}

func TestCreateCloudDestinationRejectsUnboundConnectionID(t *testing.T) {
	platform := &cloudPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, platform, silentLogger{})
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })

	body := `{"name":"r2","kind":"s3","connection_id":88,"config":{"bucket":"backups"}}`
	req := httptest.NewRequest(http.MethodPost, "/destinations", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	(&App{}).handleDestinationsCollection(recorder, req)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "currently bound") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// ─── snapshot streamer ─────────────────────────────────────────────

func TestStreamSnapshot_UnsupportedServer(t *testing.T) {
	if _, err := streamSnapshot(context.Background(), newTestCtx(t), io.Discard); err == nil || !strings.Contains(err.Error(), "update Apteva Server") {
		t.Errorf("expected clear unsupported-server error, got %v", err)
	}
}

func TestStreamSnapshot_HappyPath(t *testing.T) {
	body := []byte("synthetic-snapshot-bytes")
	platform := &backupPlatform{snapshot: body}
	ctx := newBackupTestCtx(t, platform)

	var buf bytes.Buffer
	n, err := streamSnapshot(context.Background(), ctx, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(body)) || !bytes.Equal(buf.Bytes(), body) {
		t.Errorf("body mismatch (n=%d, want=%d)", n, len(body))
	}
}

func TestStreamSnapshot_NonOKResponse(t *testing.T) {
	platform := &backupPlatform{snapshotErr: errors.New("snapshot endpoint returned 403: admin only")}
	ctx := newBackupTestCtx(t, platform)

	if _, err := streamSnapshot(context.Background(), ctx, io.Discard); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got %v", err)
	}
}

func TestStreamSnapshot_OlderServerReturnsClearUpgradeError(t *testing.T) {
	platform := &backupPlatform{snapshotErr: errors.New("platform /api/apps/callback/platform/snapshot: http 404: not found")}
	ctx := newBackupTestCtx(t, platform)
	if _, err := streamSnapshot(context.Background(), ctx, io.Discard); err == nil || err.Error() != platformBackupUnsupportedMessage {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamSnapshot_RespectsCancellation(t *testing.T) {
	platform := &backupPlatform{waitForContext: true}
	appCtx := newBackupTestCtx(t, platform)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := streamSnapshot(ctx, appCtx, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

// ─── postRestore ───────────────────────────────────────────────────

func TestPostRestore_HappyPath(t *testing.T) {
	platform := &backupPlatform{restoreReport: map[string]any{
		"format_version_seen": 1,
		"server_db":           "staged",
		"restart_required":    true,
		"installs":            []any{},
	}}
	ctx := newBackupTestCtx(t, platform)

	report, err := postRestore(ctx, []byte("fake-tarball"))
	if err != nil {
		t.Fatal(err)
	}
	if platform.restoreSize != int64(len("fake-tarball")) || !bytes.Equal(platform.restoreBody, []byte("fake-tarball")) {
		t.Errorf("request shape wrong: size=%d body=%q", platform.restoreSize, platform.restoreBody)
	}
	if report["restart_required"] != true || report["server_db"] != "staged" {
		t.Errorf("report = %+v", report)
	}
}

func TestBackupRestoreRequiresExplicitConfirmation(t *testing.T) {
	app := &App{}
	ctx := newTestCtx(t)
	if _, err := app.toolBackupRestore(ctx, map[string]any{"run_id": float64(42)}); err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("tool restore without confirmation error = %v", err)
	}

	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	req := httptest.NewRequest(http.MethodPost, "/restore", strings.NewReader(`{"run_id":42}`))
	recorder := httptest.NewRecorder()
	app.handleRestore(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "confirm=true") {
		t.Fatalf("REST restore without confirmation: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
