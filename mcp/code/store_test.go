package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB sets up a fresh sqlite DB with the migration applied,
// so store tests don't depend on app-sdk's migration runner.
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

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My App":         "my-app",
		"  My  App  ":    "my-app",
		"My_App.v2":      "my-app-v2",
		"foo---bar":      "foo-bar",
		"!!!hello!!!":    "hello",
		"alreadySlugged": "alreadyslugged",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateRepo_AndGet(t *testing.T) {
	db := openTestDB(t)
	in := CreateRepoInput{Name: "Marketing Site", Framework: "nextjs"}
	r, err := dbCreateRepo(db, "p1", in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Slug != "marketing-site" {
		t.Errorf("slug = %q, want marketing-site", r.Slug)
	}
	if r.Framework != "nextjs" {
		t.Errorf("framework = %q", r.Framework)
	}
	if r.StorageRoot != "/repos/marketing-site/" {
		t.Errorf("storage_root = %q", r.StorageRoot)
	}

	got, err := dbGetRepoBySlug(db, "p1", "marketing-site")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != r.ID {
		t.Errorf("get-by-slug round-trip failed: %+v", got)
	}
}

func TestCreateRepo_RejectsBadFramework(t *testing.T) {
	db := openTestDB(t)
	_, err := dbCreateRepo(db, "p1", CreateRepoInput{Name: "x", Framework: "fortran"})
	if err == nil {
		t.Fatal("want error for unknown framework")
	}
}

func TestCreateRepo_DuplicateSlugRejected(t *testing.T) {
	db := openTestDB(t)
	_, err := dbCreateRepo(db, "p1", CreateRepoInput{Name: "Foo"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbCreateRepo(db, "p1", CreateRepoInput{Name: "Foo"})
	if err == nil {
		t.Fatal("want unique-constraint error on second create")
	}
}

func TestCreateRepo_ProjectScopingIsolatesSlugs(t *testing.T) {
	// Two different Apteva projects can both have a "site" repo.
	db := openTestDB(t)
	if _, err := dbCreateRepo(db, "alice", CreateRepoInput{Name: "site"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbCreateRepo(db, "bob", CreateRepoInput{Name: "site"}); err != nil {
		t.Fatal(err)
	}
	got, _ := dbListRepos(db, "alice", false, "")
	if len(got) != 1 {
		t.Errorf("alice should see 1 repo, got %d", len(got))
	}
}

func TestSetDeployHints(t *testing.T) {
	db := openTestDB(t)
	r, err := dbCreateRepo(db, "p", CreateRepoInput{Name: "x", Framework: "go"})
	if err != nil {
		t.Fatal(err)
	}
	bcmd := "go build -o app ."
	port := 8080
	envs := `{"FOO":"bar"}`
	if _, err := dbSetDeployHints(db, "p", r.Slug, DeployHints{
		BuildCmd: &bcmd, Port: &port, EnvJSON: &envs,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetRepoBySlug(db, "p", r.Slug)
	if got.BuildCmd != bcmd || got.Port != port || got.EnvJSON != envs {
		t.Errorf("hints not persisted: %+v", got)
	}
}

func TestArchiveAndList(t *testing.T) {
	db := openTestDB(t)
	r, _ := dbCreateRepo(db, "p", CreateRepoInput{Name: "alpha"})
	_, _ = dbCreateRepo(db, "p", CreateRepoInput{Name: "beta"})
	if err := dbArchiveRepo(db, "p", r.Slug); err != nil {
		t.Fatal(err)
	}

	active, _ := dbListRepos(db, "p", false, "")
	if len(active) != 1 || active[0].Slug != "beta" {
		t.Errorf("active list = %+v", active)
	}
	all, _ := dbListRepos(db, "p", true, "")
	if len(all) != 2 {
		t.Errorf("includeArchived list = %+v", all)
	}
}

func TestIssues_CreateUpdateCommentAndLink(t *testing.T) {
	db := openTestDB(t)
	repo, err := dbCreateRepo(db, "p1", CreateRepoInput{Name: "App"})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := dbCreateIssue(db, "p1", repo, IssueCreateInput{
		Title:     "Login button does nothing",
		Body:      "Clicking the button has no effect.",
		Type:      "bug",
		Priority:  "high",
		CreatedBy: "human:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if iss.Number != 1 || iss.Status != "open" || iss.Type != "bug" {
		t.Fatalf("created issue wrong: %+v", iss)
	}

	next, err := dbCreateIssue(db, "p1", repo, IssueCreateInput{Title: "Add dark mode", Type: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Number != 2 {
		t.Fatalf("second issue number=%d, want 2", next.Number)
	}

	active, err := dbListIssues(db, "p1", repo.ID, IssueListOptions{Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active issues=%d, want 2", len(active))
	}

	status := issueStatusClosed
	closed, err := dbUpdateIssue(db, iss, IssuePatch{Status: &status, Actor: "agent:test"})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != issueStatusClosed || closed.ClosedAt == "" {
		t.Fatalf("close failed: %+v", closed)
	}

	if _, err := dbAddIssueComment(db, iss.ID, "agent:test", "Fixed in input handler."); err != nil {
		t.Fatal(err)
	}
	if _, err := issuePathLink(db, iss, "src/App.tsx", 12, 15, "", "agent:test"); err != nil {
		t.Fatal(err)
	}
	detail, err := dbGetIssueDetail(db, "p1", repo.ID, iss.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 || len(detail.Links) != 1 || len(detail.Events) < 4 {
		t.Fatalf("detail missing related records: comments=%d links=%d events=%d", len(detail.Comments), len(detail.Links), len(detail.Events))
	}
}

func TestIssues_ProjectScopeAndRepoScope(t *testing.T) {
	db := openTestDB(t)
	a, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "App"})
	b, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "Other"})
	c, _ := dbCreateRepo(db, "p2", CreateRepoInput{Name: "App"})
	dbCreateIssue(db, "p1", a, IssueCreateInput{Title: "p1 app"})
	dbCreateIssue(db, "p1", b, IssueCreateInput{Title: "p1 other"})
	dbCreateIssue(db, "p2", c, IssueCreateInput{Title: "p2 app"})

	got, err := dbListIssues(db, "p1", a.ID, IssueListOptions{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "p1 app" {
		t.Fatalf("scope leak: %+v", got)
	}
}

// TestDevRun_UpsertAndGet pins the per-(project, repo) uniqueness
// guarantee — re-upserting the same key updates in place rather than
// creating a second row, so the supervisor can't end up with two
// "starting" rows racing the spawn.
func TestDevRun_UpsertAndGet(t *testing.T) {
	db := openTestDB(t)
	repo, err := dbCreateRepo(db, "p1", CreateRepoInput{Name: "site"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := dbUpsertDevRun(db, DevRun{
		ProjectID: "p1", RepoID: repo.ID,
		Status: "starting", Port: 6101, PID: 1234,
		Framework: "nextjs", LogPath: "/tmp/log",
		StartedAt: "2026-05-06T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if first.Status != "starting" || first.Port != 6101 {
		t.Fatalf("upsert read-back wrong: %+v", first)
	}

	// Second upsert with same (project, repo) updates; doesn't create.
	if _, err := dbUpsertDevRun(db, DevRun{
		ProjectID: "p1", RepoID: repo.ID,
		Status: "live", Port: 6101, PID: 1234,
		Framework: "nextjs", LogPath: "/tmp/log",
		StartedAt: "2026-05-06T10:00:00Z",
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := dbGetDevRun(db, "p1", repo.ID)
	if err != nil || got == nil {
		t.Fatalf("get: dr=%v err=%v", got, err)
	}
	if got.Status != "live" {
		t.Errorf("status after second upsert = %q, want live", got.Status)
	}
	if got.ID != first.ID {
		t.Errorf("upsert created a new row: %d vs %d", got.ID, first.ID)
	}
}

// TestDevRun_UpdateAllowlist guards the column allowlist in
// dbUpdateDevRun — same shape as deploy's framework regression. If
// "status" or "stopped_at" silently drops, the supervisor's clean-stop
// path leaves rows in 'starting' forever.
func TestDevRun_UpdateAllowlist(t *testing.T) {
	db := openTestDB(t)
	repo, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "site"})
	dr, _ := dbUpsertDevRun(db, DevRun{
		ProjectID: "p1", RepoID: repo.ID, Status: "starting", Port: 6101, PID: 1234,
	})
	if err := dbUpdateDevRun(db, dr.ID, map[string]any{
		"status":     "stopped",
		"stopped_at": "2026-05-06T10:05:00Z",
		"error":      "user clicked stop",
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := dbGetDevRun(db, "p1", repo.ID)
	if after.Status != "stopped" {
		t.Errorf("status not updated: %q", after.Status)
	}
	if after.Error != "user clicked stop" {
		t.Errorf("error not updated: %q", after.Error)
	}
	if after.StoppedAt == "" {
		t.Errorf("stopped_at not updated")
	}
}

// TestDevRun_ListLive returns starting|live rows; orphans the
// reconciler will check on boot. Stopped rows must not leak in or
// reconcile would touch dead rows.
func TestDevRun_ListLive(t *testing.T) {
	db := openTestDB(t)
	r1, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "a"})
	r2, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "b"})
	r3, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "c"})

	dbUpsertDevRun(db, DevRun{ProjectID: "p1", RepoID: r1.ID, Status: "live", PID: 1, Port: 6101})
	dbUpsertDevRun(db, DevRun{ProjectID: "p1", RepoID: r2.ID, Status: "starting", PID: 2, Port: 6102})
	dbUpsertDevRun(db, DevRun{ProjectID: "p1", RepoID: r3.ID, Status: "stopped", PID: 0, Port: 0})

	live, err := dbListLiveDevRuns(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("live count = %d, want 2 (starting + live; stopped excluded)", len(live))
	}
}
