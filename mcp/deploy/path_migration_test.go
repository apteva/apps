package main

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestRebaseLegacyPaths(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "site", SourceKind: "local", SourceRef: "/src", Framework: "static",
	})
	if err != nil {
		t.Fatal(err)
	}

	b, err := dbCreateBuild(db, d.ID, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	legacyArtifact := filepath.Join(string(filepath.Separator), "legacy-data", "builds", strconv.FormatInt(b.ID, 10), "dist")
	legacyBuildLog := filepath.Join(string(filepath.Separator), "legacy-data", "builds", strconv.FormatInt(b.ID, 10), "build.log")
	if err := dbUpdateBuild(db, b.ID, map[string]any{
		"artifact_path": legacyArtifact,
		"log_path":      legacyBuildLog,
	}); err != nil {
		t.Fatal(err)
	}

	rel, err := dbCreateRelease(db, d.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyReleaseLog := filepath.Join(string(filepath.Separator), "legacy-data", "releases", strconv.FormatInt(rel.ID, 10), "runtime.log")
	if err := dbUpdateRelease(db, rel.ID, map[string]any{"log_path": legacyReleaseLog}); err != nil {
		t.Fatal(err)
	}

	// Noncanonical absolute paths are not owned by the migration.
	custom, err := dbCreateBuild(db, d.ID, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	customArtifact := filepath.Join(string(filepath.Separator), "external", "artifact")
	customLog := filepath.Join(string(filepath.Separator), "external", "build.log")
	if err := dbUpdateBuild(db, custom.ID, map[string]any{
		"artifact_path": customArtifact,
		"log_path":      customLog,
	}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	summary, err := rebaseLegacyPaths(db, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BuildArtifacts != 1 || summary.BuildLogs != 1 || summary.ReleaseLogs != 1 || summary.total() != 3 {
		t.Fatalf("summary = %+v, want one rebase for each path kind", summary)
	}

	gotBuild, err := dbGetBuild(db, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, "builds", strconv.FormatInt(b.ID, 10), "dist"); gotBuild.ArtifactPath != want {
		t.Fatalf("artifact_path = %q, want %q", gotBuild.ArtifactPath, want)
	}
	if want := filepath.Join(dataDir, "builds", strconv.FormatInt(b.ID, 10), "build.log"); gotBuild.LogPath != want {
		t.Fatalf("build log_path = %q, want %q", gotBuild.LogPath, want)
	}
	gotRelease, err := dbGetRelease(db, rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, "releases", strconv.FormatInt(rel.ID, 10), "runtime.log"); gotRelease.LogPath != want {
		t.Fatalf("release log_path = %q, want %q", gotRelease.LogPath, want)
	}

	gotCustom, err := dbGetBuild(db, custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotCustom.ArtifactPath != customArtifact || gotCustom.LogPath != customLog {
		t.Fatalf("custom paths changed: %+v", gotCustom)
	}

	second, err := rebaseLegacyPaths(db, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if second.total() != 0 {
		t.Fatalf("second migration summary = %+v, want idempotent no-op", second)
	}
}

func TestRebaseCanonicalPathRejectsWrongRecordIDAndRelativePaths(t *testing.T) {
	dataDir := t.TempDir()
	wrongID := filepath.Join(string(filepath.Separator), "legacy", "builds", "99", "dist")
	if got, changed := rebaseCanonicalPath(wrongID, dataDir, "builds", 12, "dist"); changed || got != wrongID {
		t.Fatalf("wrong-ID path changed to %q", got)
	}
	if got, changed := rebaseCanonicalPath("builds/12/dist", dataDir, "builds", 12, "dist"); changed || got != "builds/12/dist" {
		t.Fatalf("relative path changed to %q", got)
	}
}
