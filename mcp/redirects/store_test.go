package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// freshDB spins up an in-memory SQLite, applies the v0.1 schema, and
// returns a writable handle. Each test gets its own DB so insertion
// order quirks don't leak between cases.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	wd, _ := os.Getwd()
	migrations, err := filepath.Glob(filepath.Join(wd, "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, migration := range migrations {
		schema, err := os.ReadFile(migration)
		if err != nil {
			t.Fatalf("read migration %s: %v", migration, err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("apply migration %s: %v", migration, err)
		}
	}
	return db
}

func mustInsert(t *testing.T, db *sql.DB, in RedirectInput) *Redirect {
	t.Helper()
	r, err := dbInsertRedirect(db, in)
	if err != nil {
		t.Fatalf("insert %+v: %v", in, err)
	}
	return r
}

func TestInsert_DefaultsAndValidation(t *testing.T) {
	db := freshDB(t)
	r := mustInsert(t, db, RedirectInput{
		Hostname:    "go.example.com",
		Destination: "https://example.com/launch",
	})
	if r.Path != "/" || r.MatchMode != "exact" || r.StatusCode != 302 {
		t.Fatalf("defaults wrong: %+v", r)
	}
	if r.PreserveQuery {
		t.Errorf("preserve_query should default to false from raw insert (handler layer flips to true)")
	}

	// preserve_path requires match=prefix
	_, err := dbInsertRedirect(db, RedirectInput{
		Hostname:     "other.example.com",
		Destination:  "https://example.com",
		PreservePath: true,
	})
	if err == nil {
		t.Fatalf("expected preserve_path+exact to fail")
	}

	// bad status code
	_, err = dbInsertRedirect(db, RedirectInput{
		Hostname:    "x.example.com",
		Destination: "https://example.com",
		StatusCode:  418,
	})
	if err == nil {
		t.Fatalf("expected 418 to fail")
	}

	// bad destination scheme
	_, err = dbInsertRedirect(db, RedirectInput{
		Hostname:    "y.example.com",
		Destination: "ftp://example.com",
	})
	if err == nil {
		t.Fatalf("expected ftp:// to fail")
	}
}

func TestInsert_Conflict(t *testing.T) {
	db := freshDB(t)
	mustInsert(t, db, RedirectInput{
		Hostname:    "go.example.com",
		Path:        "/promo",
		Destination: "https://example.com/a",
	})
	_, err := dbInsertRedirect(db, RedirectInput{
		Hostname:    "go.example.com",
		Path:        "/promo",
		Destination: "https://example.com/b",
	})
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestMatch_ExactBeatsPrefix(t *testing.T) {
	db := freshDB(t)
	mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Path: "/", MatchMode: "prefix",
		Destination: "https://example.com/default",
	})
	exact := mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Path: "/promo", MatchMode: "exact",
		Destination: "https://example.com/promo",
	})

	got, err := matchRedirect(db, "go.example.com", "/promo")
	if err != nil || got == nil {
		t.Fatalf("match err=%v got=%v", err, got)
	}
	if got.ID != exact.ID {
		t.Fatalf("exact rule should beat the catch-all prefix; got id=%d", got.ID)
	}
}

func TestMatch_LongestPrefixWins(t *testing.T) {
	db := freshDB(t)
	mustInsert(t, db, RedirectInput{
		Hostname: "old.example.com", Path: "/", MatchMode: "prefix",
		Destination: "https://new.example.com",
	})
	specific := mustInsert(t, db, RedirectInput{
		Hostname: "old.example.com", Path: "/blog", MatchMode: "prefix",
		Destination: "https://new.example.com/posts",
	})

	got, err := matchRedirect(db, "old.example.com", "/blog/2026/welcome")
	if err != nil || got == nil {
		t.Fatalf("match err=%v got=%v", err, got)
	}
	if got.ID != specific.ID {
		t.Fatalf("longer prefix should win; got id=%d", got.ID)
	}
}

func TestMatch_PrefixBoundary(t *testing.T) {
	db := freshDB(t)
	mustInsert(t, db, RedirectInput{
		Hostname: "old.example.com", Path: "/blog", MatchMode: "prefix",
		Destination: "https://new.example.com",
	})

	// /blogfoo should NOT match /blog as a prefix — boundary is at
	// path-segment ('/') not at any string prefix.
	got, _ := matchRedirect(db, "old.example.com", "/blogfoo")
	if got != nil {
		t.Fatalf("/blogfoo should not match /blog prefix; got id=%d", got.ID)
	}
	// /blog and /blog/x SHOULD match.
	for _, p := range []string{"/blog", "/blog/x", "/blog/x/y"} {
		got, _ := matchRedirect(db, "old.example.com", p)
		if got == nil {
			t.Fatalf("%s should match /blog prefix", p)
		}
	}
}

func TestApplyRule_PreservePath(t *testing.T) {
	r := &Redirect{
		Path: "/blog", MatchMode: "prefix",
		Destination:  "https://new.example.com/posts",
		PreservePath: true, PreserveQuery: false,
	}
	got := applyRule(r, "/blog/2026/welcome", "")
	want := "https://new.example.com/posts/2026/welcome"
	if got != want {
		t.Fatalf("applyRule: want %q got %q", want, got)
	}
}

func TestApplyRule_PreserveQuery(t *testing.T) {
	r := &Redirect{
		Path: "/promo", MatchMode: "exact",
		Destination:   "https://example.com/landing?campaign=spring",
		PreserveQuery: true,
	}
	got := applyRule(r, "/promo", "src=email&campaign=summer")
	// inbound `campaign` should win.
	if !contains(got, "campaign=summer") || !contains(got, "src=email") {
		t.Fatalf("preserve_query should merge with inbound winning; got %q", got)
	}
}

func TestApplyRule_NoOpWhenFlagsOff(t *testing.T) {
	r := &Redirect{
		Path: "/promo", MatchMode: "exact",
		Destination:   "https://example.com/landing",
		PreservePath:  false,
		PreserveQuery: false,
	}
	got := applyRule(r, "/promo", "src=email")
	if got != "https://example.com/landing" {
		t.Fatalf("flags off should yield bare destination; got %q", got)
	}
}

func TestUpdate_PreservesOmittedBooleansAndClearsNotes(t *testing.T) {
	db := freshDB(t)
	rule := mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com/old",
		MatchMode: "prefix", PreservePath: true, PreserveQuery: false,
		ProjectID: "project-a", Notes: "remove me",
	})
	destination := "https://example.com/new"
	emptyNotes := ""
	updated, err := dbUpdateRedirect(db, rule.ID, "project-a", RedirectPatch{
		Destination: &destination,
		Notes:       &emptyNotes,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.PreservePath || updated.PreserveQuery {
		t.Fatalf("omitted booleans changed: %+v", updated)
	}
	if updated.Notes != "" {
		t.Fatalf("notes were not cleared: %q", updated.Notes)
	}
}

func TestUpdate_NormalisesHostnameAndPath(t *testing.T) {
	db := freshDB(t)
	rule := mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com", ProjectID: "project-a",
	})
	hostname := "GO2.Example.COM."
	path := "promo/"
	updated, err := dbUpdateRedirect(db, rule.ID, "project-a", RedirectPatch{Hostname: &hostname, Path: &path})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Hostname != "go2.example.com" || updated.Path != "/promo/" {
		t.Fatalf("not normalised: %+v", updated)
	}
	matched, err := matchRedirectInProject(db, "GO2.EXAMPLE.COM", "/promo/", "project-a")
	if err != nil || matched == nil || matched.ID != rule.ID {
		t.Fatalf("updated rule did not match: rule=%+v err=%v", matched, err)
	}
}

func TestHostnameClaim_PreventsCrossProjectOwnership(t *testing.T) {
	db := freshDB(t)
	mustInsert(t, db, RedirectInput{
		Hostname: "Go.Example.com.", Path: "/a", Destination: "https://example.com/a", ProjectID: "project-a",
	})
	_, err := dbInsertRedirect(db, RedirectInput{
		Hostname: "go.example.com", Path: "/b", Destination: "https://example.com/b", ProjectID: "project-b",
	})
	if !errors.Is(err, ErrHostnameOwned) {
		t.Fatalf("expected ErrHostnameOwned, got %v", err)
	}
}

func TestScopedItemOperationsRejectOtherProject(t *testing.T) {
	db := freshDB(t)
	rule := mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com", ProjectID: "project-b",
	})
	if _, err := dbGetRedirect(db, rule.ID, "project-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project get: %v", err)
	}
	if _, err := dbDeleteRedirect(db, rule.ID, "project-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project delete: %v", err)
	}
	if _, err := dbGetRedirect(db, rule.ID, "project-b"); err != nil {
		t.Fatalf("owner rule disappeared: %v", err)
	}
}

func TestExactMatch_PreservesTrailingSlash(t *testing.T) {
	db := freshDB(t)
	mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Path: "/promo/", Destination: "https://example.com/slash", ProjectID: "project-a",
	})
	if rule, _ := matchRedirectInProject(db, "go.example.com", "/promo", "project-a"); rule != nil {
		t.Fatalf("/promo unexpectedly matched trailing-slash exact rule")
	}
	if rule, _ := matchRedirectInProject(db, "go.example.com", "/promo/", "project-a"); rule == nil {
		t.Fatalf("/promo/ did not match")
	}
}

func TestRecordHits_BatchesCounts(t *testing.T) {
	db := freshDB(t)
	rule := mustInsert(t, db, RedirectInput{Hostname: "go.example.com", Destination: "https://example.com"})
	if err := dbRecordHits(db, map[int64]int64{rule.ID: 7}); err != nil {
		t.Fatalf("record hits: %v", err)
	}
	got, err := dbGetRedirect(db, rule.ID, "")
	if err != nil || got.Hits != 7 || got.LastHitAt == "" {
		t.Fatalf("hits=%+v err=%v", got, err)
	}
}

func TestHostnameClaimMigrationMakesLegacyDuplicatesDeterministic(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"001_init.sql"} {
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ project, destination string }{{"project-a", "https://a.example"}, {"project-b", "https://b.example"}} {
		if _, err := db.Exec(`INSERT INTO redirects (hostname,path,match_mode,destination,status_code,project_id) VALUES ('go.example.com','/','exact',?,302,?)`, row.destination, row.project); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join("migrations", "002_hostname_claims.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	rule, err := matchRedirect(db, "go.example.com", "/")
	if err != nil || rule == nil || rule.ProjectID != "project-a" {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
