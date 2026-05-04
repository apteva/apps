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
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
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
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("migration: %v", err)
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
	if len(m.Provides.MCPTools) != 3 {
		t.Errorf("expected 3 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	wantTools := map[string]bool{"backup_now": false, "backup_list": false, "backup_restore": false}
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
		{"s3 missing bucket", Destination{Name: "n", Kind: "s3", Config: json.RawMessage(`{}`), ConnectionID: 1}, false},
		{"s3 missing connection", Destination{Name: "n", Kind: "s3", Config: json.RawMessage(`{"bucket":"b"}`)}, false},
		{"s3 ok", Destination{Name: "n", Kind: "s3", Config: json.RawMessage(`{"bucket":"b"}`), ConnectionID: 7}, true},
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
	if err := dbFinishRun(db, id, "success", 1234, "deadbeef", "apteva-x.tar.gz", `{"format_version":1}`, ""); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetRun(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "success" || got.BytesCompressed != 1234 || got.SHA256 != "deadbeef" {
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

// ─── retention ─────────────────────────────────────────────────────

func TestPruneRetention_KeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	dest := &Destination{ID: 1, Kind: "local", Config: json.RawMessage(`{"path":"` + dir + `"}`)}
	w := &localDest{cfg: localConfig{Path: dir}}

	ctx := newTestCtx(t)
	keys := []string{
		"apteva-20260101-000000.tar.gz",
		"apteva-20260102-000000.tar.gz",
		"apteva-20260103-000000.tar.gz",
		"apteva-20260104-000000.tar.gz",
		"apteva-20260105-000000.tar.gz",
	}
	for i, key := range keys {
		if err := w.Put(context.Background(), key, bytes.NewReader([]byte("x")), 1); err != nil {
			t.Fatal(err)
		}
		when := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		_ = os.Chtimes(filepath.Join(dir, key), when, when)
	}
	if err := pruneRetention(context.Background(), ctx, w, dest, 2); err != nil {
		t.Fatal(err)
	}
	objs, _ := w.List(context.Background())
	if len(objs) != 2 {
		t.Errorf("want 2 left after prune, got %d (%v)", len(objs), objs)
	}
	got := []string{objs[0].Key, objs[1].Key}
	want := map[string]bool{
		"apteva-20260104-000000.tar.gz": true,
		"apteva-20260105-000000.tar.gz": true,
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
	_ = w.Put(context.Background(), "apteva-x.tar.gz", bytes.NewReader([]byte("y")), 1)
	_ = w.Put(context.Background(), "operator-readme.txt", bytes.NewReader([]byte("hands off")), 9)

	if err := pruneRetention(context.Background(), ctx, w, dest, 1); err != nil {
		t.Fatal(err)
	}
	objs, _ := w.List(context.Background())
	if len(objs) != 2 {
		t.Errorf("expected both files to survive, got %d", len(objs))
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
