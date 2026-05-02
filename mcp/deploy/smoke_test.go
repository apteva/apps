package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Smoke test: bring up the schema in an in-memory DB, run the
// store helpers end-to-end so refactors break loudly.
func TestStoreSmoke(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "api", SourceKind: "local", SourceRef: "/tmp/src", Framework: "go",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.Name != "api" {
		t.Fatalf("name = %q", d.Name)
	}

	// Duplicate name is rejected by UNIQUE.
	if _, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "api", SourceKind: "local", SourceRef: "/tmp/src",
	}); err == nil {
		t.Fatalf("expected duplicate name to fail")
	}

	got, err := dbGetDeploymentByName(db, "p1", "api")
	if err != nil || got == nil || got.ID != d.ID {
		t.Fatalf("get by name: %v %+v", err, got)
	}

	// Build → release flow.
	b, err := dbCreateBuild(db, d.ID, "go", "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := dbUpdateBuild(db, b.ID, map[string]any{
		"status": "succeeded", "artifact_path": "/data/b1/dist", "artifact_size": int64(1234),
	}); err != nil {
		t.Fatalf("update build: %v", err)
	}
	rel, err := dbCreateRelease(db, d.ID, b.ID)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := dbSetCurrentRelease(db, d.ID, &rel.ID); err != nil {
		t.Fatalf("set current: %v", err)
	}
	again, _ := dbGetDeployment(db, "p1", d.ID)
	if again.CurrentReleaseID == nil || *again.CurrentReleaseID != rel.ID {
		t.Fatalf("current_release_id = %v", again.CurrentReleaseID)
	}

	// Port lease semantics.
	if ok, _ := dbAcquirePortLease(db, 7000, rel.ID); !ok {
		t.Fatal("first lease should succeed")
	}
	if ok, _ := dbAcquirePortLease(db, 7000, rel.ID); ok {
		t.Fatal("second lease on same port should fail")
	}
	held, _ := dbHeldPorts(db)
	if !held[7000] {
		t.Fatal("held should include 7000")
	}
	if err := dbReleasePortLease(db, 7000); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	// Cascade: deleting the deployment wipes builds + releases.
	if err := dbDeleteDeployment(db, "p1", d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, _ := dbListBuilds(db, d.ID, 10)
	if len(rows) != 0 {
		t.Fatalf("builds should cascade, got %d", len(rows))
	}
}

func TestValidateName(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"ok":       true,
		"a-b_c123": true,
		"BAD":      false, // uppercase rejected
		"has space": false,
		"slash/x":  false,
	}
	for in, want := range cases {
		err := validateName(in)
		if (err == nil) != want {
			t.Errorf("validateName(%q): err=%v, want ok=%v", in, err, want)
		}
	}
}

func TestTailLines(t *testing.T) {
	body := "a\nb\nc\nd\ne\n"
	if got := tailLines(body, 2); got != "d\ne\n" {
		t.Errorf("tailLines(2) = %q", got)
	}
	if got := tailLines(body, 100); got != body {
		t.Errorf("tailLines(100) = %q", got)
	}
}

func TestDetectFramework(t *testing.T) {
	tmp := t.TempDir()
	if detectFramework(tmp) != "" {
		t.Fatal("empty dir should detect nothing")
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectFramework(tmp); got != "go" {
		t.Fatalf("detectFramework after go.mod = %q", got)
	}
}

// openSchemaDB opens an in-memory SQLite and applies the v1 migration
// inline. Simpler than wiring the SDK's migration runner for unit
// tests; just keep them in sync.
func openSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	cleaned := stripSQLComments(string(body))
	for _, stmt := range strings.Split(cleaned, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("migration: %v\n%s", err, s)
		}
	}
	return db
}

// stripSQLComments removes -- line comments. The migration has no
// /* */ blocks so this is enough.
func stripSQLComments(s string) string {
	out := make([]string, 0)
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
