package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	db.SetMaxOpenConns(1)
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

func TestRecordHitIsAtomicUnderConcurrency(t *testing.T) {
	db := freshDB(t)
	rule := mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com", ProjectID: "project-a",
	})
	at := time.Date(2026, 7, 13, 12, 10, 0, 0, time.UTC)
	const requests = 20
	results := make(chan *HitCounts, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts, err := dbRecordHit(db, rule.ID, "project-a", at)
			if err != nil {
				errs <- err
				return
			}
			results <- counts
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("record hit: %v", err)
	}
	seenTotals := map[int64]bool{}
	for counts := range results {
		seenTotals[counts.HitsTotal] = true
	}
	if len(seenTotals) != requests {
		t.Fatalf("unique absolute totals=%d want=%d (%v)", len(seenTotals), requests, seenTotals)
	}
	stats, total, _, err := dbListRedirectStats(db, RedirectStatsQuery{ProjectID: "project-a", RuleID: rule.ID})
	if err != nil || total != 1 || len(stats) != 1 || stats[0].HitsTotal != requests || stats[0].DayHits != requests {
		t.Fatalf("stats=%+v total=%d err=%v", stats, total, err)
	}
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

func TestRecordHitReturnsAtomicTotalAndDailyCounts(t *testing.T) {
	db := freshDB(t)
	rule := mustInsert(t, db, RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com", ProjectID: "project-a",
	})
	dayOne := time.Date(2026, 7, 13, 23, 59, 0, 0, time.UTC)
	for i := int64(1); i <= 3; i++ {
		counts, err := dbRecordHit(db, rule.ID, "project-a", dayOne)
		if err != nil {
			t.Fatalf("record hit %d: %v", i, err)
		}
		if counts.HitsTotal != i || counts.DayHits != i || counts.Date != "2026-07-13" {
			t.Fatalf("counts %d=%+v", i, counts)
		}
	}
	counts, err := dbRecordHit(db, rule.ID, "project-a", dayOne.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("record next-day hit: %v", err)
	}
	if counts.HitsTotal != 4 || counts.DayHits != 1 || counts.Date != "2026-07-14" {
		t.Fatalf("next-day counts=%+v", counts)
	}
	got, err := dbGetRedirect(db, rule.ID, "project-a")
	if err != nil || got.Hits != 4 || got.LastHitAt == "" {
		t.Fatalf("hits=%+v err=%v", got, err)
	}
	stats, total, query, err := dbListRedirectStats(db, RedirectStatsQuery{
		ProjectID: "project-a", RuleID: rule.ID, From: "2026-07-13", To: "2026-07-14",
	})
	if err != nil || total != 2 || len(stats) != 2 || query.Limit != 50 {
		t.Fatalf("stats=%+v total=%d query=%+v err=%v", stats, total, query, err)
	}
	if stats[0].Date != "2026-07-14" || stats[0].DayHits != 1 || stats[0].HitsTotal != 4 ||
		stats[1].Date != "2026-07-13" || stats[1].DayHits != 3 {
		t.Fatalf("unexpected stats=%+v", stats)
	}
}

func TestRedirectStatsAreProjectScopedAndDeletedWithRule(t *testing.T) {
	db := freshDB(t)
	ruleA := mustInsert(t, db, RedirectInput{
		Hostname: "a.example.com", Destination: "https://example.com/a", ProjectID: "project-a",
	})
	ruleB := mustInsert(t, db, RedirectInput{
		Hostname: "b.example.com", Destination: "https://example.com/b", ProjectID: "project-b",
	})
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if _, err := dbRecordHit(db, ruleA.ID, "project-a", at); err != nil {
		t.Fatal(err)
	}
	if _, err := dbRecordHit(db, ruleB.ID, "project-b", at); err != nil {
		t.Fatal(err)
	}
	stats, total, _, err := dbListRedirectStats(db, RedirectStatsQuery{ProjectID: "project-a"})
	if err != nil || total != 1 || len(stats) != 1 || stats[0].RuleID != ruleA.ID {
		t.Fatalf("project-a stats=%+v total=%d err=%v", stats, total, err)
	}
	if _, err := dbDeleteRedirect(db, ruleA.ID, "project-a"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM redirect_daily_stats WHERE rule_id=?`, ruleA.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("deleted rule stats remaining=%d err=%v", remaining, err)
	}
	if _, _, _, err := dbListRedirectStats(db, RedirectStatsQuery{ProjectID: "project-b", From: "07/13/2026"}); err == nil {
		t.Fatalf("invalid date was accepted")
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

func TestDailyStatsMigrationPreservesExistingTotal(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, name := range []string{"001_init.sql", "002_hostname_claims.sql"} {
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.Exec(`INSERT INTO redirects (hostname, path, match_mode, destination, status_code, project_id, hits)
		VALUES ('go.example.com', '/', 'exact', 'https://example.com', 302, 'project-a', 840)`)
	if err != nil {
		t.Fatal(err)
	}
	ruleID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("migrations", "003_daily_stats.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	counts, err := dbRecordHit(db, ruleID, "project-a", time.Date(2026, 7, 13, 12, 10, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if counts.HitsTotal != 841 || counts.DayHits != 1 || counts.Date != "2026-07-13" {
		t.Fatalf("post-migration counts=%+v", counts)
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
