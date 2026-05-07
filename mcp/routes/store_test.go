package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB applies every migration in migrations/ to a fresh in-
// memory SQLite. Same shape every other Apteva app's tests use.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db") + "?_journal_mode=WAL&_busy_timeout=5000"
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

// TestValidateHostname pins the rejection rules. The matcher relies
// on no-port / no-scheme / no-path so a typo here would let invalid
// rows in that the server-side cache then mishandles.
func TestValidateHostname(t *testing.T) {
	good := []string{
		"example.com", "blog.example.com", "a.b.c.d.example",
		"abc-def.example.com", "ABC.example.com",
	}
	bad := []string{
		"", "  ", "blog.example.com:8080", "https://blog.example.com",
		"blog.example.com/path", "blog example.com",
	}
	for _, h := range good {
		if err := validateHostname(h); err != nil {
			t.Errorf("validateHostname(%q) = %v, want ok", h, err)
		}
	}
	for _, h := range bad {
		if err := validateHostname(h); err == nil {
			t.Errorf("validateHostname(%q) = nil, want error", h)
		}
	}
}

// TestValidateTarget pins the http/https-only rule. Unix sockets,
// tcp://, ssh:// etc are out of scope for v0.1 — the matcher only
// knows how to reverse-proxy HTTP.
func TestValidateTarget(t *testing.T) {
	good := []string{
		"http://127.0.0.1:7100",
		"https://internal.svc:443",
		"http://localhost:8080/",
	}
	bad := []string{
		"", "127.0.0.1:7100", "tcp://127.0.0.1:5432",
		"unix:///tmp/sock", "ssh://example.com",
	}
	for _, t1 := range good {
		if err := validateTarget(t1); err != nil {
			t.Errorf("validateTarget(%q) = %v, want ok", t1, err)
		}
	}
	for _, t1 := range bad {
		if err := validateTarget(t1); err == nil {
			t.Errorf("validateTarget(%q) = nil, want error", t1)
		}
	}
}

// TestUpsert_CreateThenUpdate covers the idempotency rule: same owner
// re-registering returns "updated" with the same row ID. Different
// owner returns ErrHostnameOwnedElsewhere.
func TestUpsert_CreateThenUpdate(t *testing.T) {
	db := openTestDB(t)

	first, action, err := dbUpsertRoute(db, RegisterInput{
		Hostname:       "blog.example.com",
		Target:         "http://127.0.0.1:7100",
		OwnerInstallID: 19,
		OwnerKind:      "deploy",
	})
	if err != nil || action != "created" {
		t.Fatalf("first upsert: action=%q err=%v", action, err)
	}
	if first.CertFQDN != "" {
		t.Errorf("CertFQDN should default to empty in store; got %q", first.CertFQDN)
	}

	// Same owner upserts: action=updated, same row id.
	second, action, err := dbUpsertRoute(db, RegisterInput{
		Hostname:       "blog.example.com",
		Target:         "http://127.0.0.1:7200", // changed
		OwnerInstallID: 19,
		OwnerKind:      "deploy",
	})
	if err != nil || action != "updated" {
		t.Fatalf("second upsert: action=%q err=%v", action, err)
	}
	if second.ID != first.ID {
		t.Errorf("upsert created a new row: %d vs %d", second.ID, first.ID)
	}
	if second.Target != "http://127.0.0.1:7200" {
		t.Errorf("target not updated: %q", second.Target)
	}

	// Different owner: 409-equivalent.
	if _, _, err := dbUpsertRoute(db, RegisterInput{
		Hostname:       "blog.example.com",
		Target:         "http://127.0.0.1:9999",
		OwnerInstallID: 22, // different
	}); !errors.Is(err, ErrHostnameOwnedElsewhere) {
		t.Errorf("cross-owner upsert: err=%v, want ErrHostnameOwnedElsewhere", err)
	}
}

// TestDelete_OwnerCheck enforces that you can only delete what you
// own. The panel's "manual delete-anything" path is at the REST
// layer (httpDeleteRoute), not in dbDeleteRouteByHostname — this
// test pins the strict ownership semantics the store provides.
func TestDelete_OwnerCheck(t *testing.T) {
	db := openTestDB(t)
	dbUpsertRoute(db, RegisterInput{
		Hostname: "blog.example.com", Target: "http://127.0.0.1:7100",
		OwnerInstallID: 19,
	})

	// Wrong owner: ErrNotOwner.
	if _, err := dbDeleteRouteByHostname(db, "blog.example.com", 22); !errors.Is(err, ErrNotOwner) {
		t.Errorf("wrong-owner delete: err=%v, want ErrNotOwner", err)
	}
	// Right owner: removed=true.
	removed, err := dbDeleteRouteByHostname(db, "blog.example.com", 19)
	if err != nil || !removed {
		t.Fatalf("right-owner delete: removed=%v err=%v", removed, err)
	}
	// Already gone: removed=false, no error.
	if removed, err := dbDeleteRouteByHostname(db, "blog.example.com", 19); err != nil || removed {
		t.Errorf("re-delete: removed=%v err=%v, want false/nil", removed, err)
	}
}

// TestDeleteForOwner sweeps every route an install owns — the path
// the orphan reconciler takes when an install is uninstalled. Must
// return the hostnames removed so the caller can fan out events.
func TestDeleteForOwner(t *testing.T) {
	db := openTestDB(t)
	dbUpsertRoute(db, RegisterInput{Hostname: "a.example.com", Target: "http://127.0.0.1:1", OwnerInstallID: 19})
	dbUpsertRoute(db, RegisterInput{Hostname: "b.example.com", Target: "http://127.0.0.1:2", OwnerInstallID: 19})
	dbUpsertRoute(db, RegisterInput{Hostname: "c.example.com", Target: "http://127.0.0.1:3", OwnerInstallID: 22})

	hosts, err := dbDeleteRoutesForOwner(db, 19)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(hosts)
	if len(hosts) != 2 || hosts[0] != "a.example.com" || hosts[1] != "b.example.com" {
		t.Errorf("hosts removed = %v, want [a,b]", hosts)
	}
	// Other owner's row should still be there.
	left, _ := dbListRoutes(db, nil)
	if len(left) != 1 || left[0].Hostname != "c.example.com" {
		t.Errorf("remaining = %v, want 1× c.example.com", left)
	}
}

// TestList_OwnerFilter covers the panel's "show everything" path
// (filter=nil) and the orphan-detection path (filter=specific id).
func TestList_OwnerFilter(t *testing.T) {
	db := openTestDB(t)
	dbUpsertRoute(db, RegisterInput{Hostname: "a.example.com", Target: "http://127.0.0.1:1", OwnerInstallID: 19})
	dbUpsertRoute(db, RegisterInput{Hostname: "b.example.com", Target: "http://127.0.0.1:2", OwnerInstallID: 22})

	all, _ := dbListRoutes(db, nil)
	if len(all) != 2 {
		t.Errorf("all count = %d, want 2", len(all))
	}
	ownerFilter := int64(19)
	owned, _ := dbListRoutes(db, &ownerFilter)
	if len(owned) != 1 || owned[0].OwnerInstallID != 19 {
		t.Errorf("filter=19 returned %v", owned)
	}
}
