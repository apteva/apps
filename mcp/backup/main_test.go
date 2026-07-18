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
	"bytes"
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
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
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
	objs, err := d.List(ctx)
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
	objs, _ := d.List(ctx)
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
	objs, _ := w.List(context.Background())
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
	objs, _ := w.List(context.Background())
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
	objects, err := w.List(context.Background())
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

func TestRunBackup_FailureIsReturnedAndRecorded(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "")
	ctx := newTestCtx(t)
	dir := t.TempDir()
	dest, err := dbCreateDestination(ctx.AppDB(), &Destination{
		Name: "local", Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runBackup(ctx, dest, nil, defaultScope())
	if err == nil || !strings.Contains(err.Error(), "APTEVA_GATEWAY_URL") {
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

// ─── snapshot streamer ─────────────────────────────────────────────

func TestStreamSnapshot_NoGatewayEnv(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "")
	if _, err := streamSnapshot(io.Discard); err == nil {
		t.Errorf("expected error when APTEVA_GATEWAY_URL unset")
	}
}

func TestStreamSnapshot_HappyPath(t *testing.T) {
	body := []byte("synthetic-snapshot-bytes")
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/platform/snapshot" {
			t.Errorf("path = %s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "dev-42")

	var buf bytes.Buffer
	n, err := streamSnapshot(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(body)) || !bytes.Equal(buf.Bytes(), body) {
		t.Errorf("body mismatch (n=%d, want=%d)", n, len(body))
	}
	if sawAuth != "Bearer dev-42" {
		t.Errorf("missing/incorrect auth header: %q", sawAuth)
	}
}

func TestStreamSnapshot_NonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "admin only", 403)
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "dev-42")

	if _, err := streamSnapshot(io.Discard); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got %v", err)
	}
}

// ─── postRestore ───────────────────────────────────────────────────

func TestPostRestore_HappyPath(t *testing.T) {
	var sawConfirm, sawCT string
	var sawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawConfirm = r.Header.Get("X-Confirm-Restore")
		sawCT = r.Header.Get("Content-Type")
		sawBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"format_version_seen": 1,
			"server_db":           "staged",
			"restart_required":    true,
			"installs":            []any{},
		})
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "dev-42")

	report, err := postRestore([]byte("fake-tarball"))
	if err != nil {
		t.Fatal(err)
	}
	if sawConfirm != "yes" || sawCT != "application/gzip" || !bytes.Equal(sawBody, []byte("fake-tarball")) {
		t.Errorf("request shape wrong: confirm=%q ct=%q body=%q", sawConfirm, sawCT, sawBody)
	}
	if report["restart_required"] != true || report["server_db"] != "staged" {
		t.Errorf("report = %+v", report)
	}
}
