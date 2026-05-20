package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB applies the migrations to a fresh sqlite DB with the same
// foreign-key + busy-timeout pragmas the SDK uses in prod (modernc
// `_pragma=` syntax), so FK enforcement matches runtime behavior.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join("migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return db
}

func TestSimUpsertAndGet(t *testing.T) {
	db := openTestDB(t)
	s := Sim{
		ID: "avd-1", ProjectID: "p1", Platform: "android",
		Runtime: "android-34", DeviceType: "pixel_6", Status: "booting",
		Serial: "emulator-5554",
	}
	if err := dbUpsertSim(db, s); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetSim(db, "avd-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.Platform != "android" || got.Serial != "emulator-5554" {
		t.Errorf("unexpected row: %+v", got)
	}

	// Upsert flips status; created_at must survive.
	if err := dbUpdateSim(db, "avd-1", map[string]any{"status": "booted"}); err != nil {
		t.Fatal(err)
	}
	got, _ = dbGetSim(db, "avd-1")
	if got.Status != "booted" {
		t.Errorf("status=%q, want booted", got.Status)
	}
}

func TestSimListFilters(t *testing.T) {
	db := openTestDB(t)
	_ = dbUpsertSim(db, Sim{ID: "a", ProjectID: "p1", Platform: "android", Status: "booted"})
	_ = dbUpsertSim(db, Sim{ID: "i", ProjectID: "p1", Platform: "ios", Status: "shutdown"})
	_ = dbUpsertSim(db, Sim{ID: "z", ProjectID: "p2", Platform: "android", Status: "booted"})

	got, err := dbListSims(db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("p1 sims = %d, want 2", len(got))
	}
}

func TestSimRunFKAndLifecycle(t *testing.T) {
	db := openTestDB(t)
	// FK on: a sim_run for a missing sim must fail.
	if _, err := dbInsertSimRun(db, SimRun{SimID: "ghost", ProjectID: "p1"}); err == nil {
		t.Error("expected FK violation for sim_run with no sim, got nil")
	}
	if err := dbUpsertSim(db, Sim{ID: "avd-1", ProjectID: "p1", Platform: "android", Status: "booted"}); err != nil {
		t.Fatal(err)
	}
	run, err := dbInsertSimRun(db, SimRun{
		SimID: "avd-1", ProjectID: "p1", SourceApp: "code", Framework: "android", Status: "building",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateSimRun(db, run.ID, map[string]any{"status": "running", "bundle_id": "com.x"}); err != nil {
		t.Fatal(err)
	}
	latest, err := dbLatestSimRun(db, "avd-1")
	if err != nil || latest == nil {
		t.Fatalf("latest: %v / %v", latest, err)
	}
	if latest.Status != "running" || latest.BundleID != "com.x" {
		t.Errorf("unexpected run: %+v", latest)
	}
}

func TestStreamTokenMintResolveExpire(t *testing.T) {
	db := openTestDB(t)
	_ = dbUpsertSim(db, Sim{ID: "avd-1", ProjectID: "p1", Platform: "android", Status: "booted"})

	st, err := dbMintStreamToken(db, "avd-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := dbResolveStreamToken(db, st.WSToken)
	if err != nil || resolved != "avd-1" {
		t.Fatalf("resolve: %q / %v", resolved, err)
	}

	// Re-mint rotates the token: the old one no longer resolves.
	old := st.WSToken
	if _, err := dbMintStreamToken(db, "avd-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := dbResolveStreamToken(db, old); err == nil {
		t.Error("old token still resolves after rotation")
	}

	// An already-expired token is rejected.
	exp, _ := dbMintStreamToken(db, "avd-1", -time.Minute)
	if _, err := dbResolveStreamToken(db, exp.WSToken); err == nil {
		t.Error("expired token resolved")
	}
}
