package main

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrationsAddAndBackfillDownloadStage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(initial)); err != nil {
		t.Fatal(err)
	}
	ts := nowRFC3339()
	if _, err := db.Exec(`INSERT INTO downloads(id, url, status, progress, storage_folder, created_at, updated_at) VALUES ('running', 'https://example.com', 'running', 20, '/out', ?, ?)`, ts, ts); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/002_job_stages.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var stage string
	if err := db.QueryRow(`SELECT stage FROM downloads WHERE id = 'running'`).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if stage != stageDownloading {
		t.Fatalf("backfilled stage = %q, want %q", stage, stageDownloading)
	}
	ingestionMigration, err := os.ReadFile("migrations/003_ingestion_artifacts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ingestionMigration)); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"metadata_json", "warnings_json", "ingest"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('downloads') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("migration did not add downloads.%s", column)
		}
	}
}

func TestInterruptActiveDownloadsMarksRunningAndQueuedFailed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE downloads (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL,
    status TEXT NOT NULL,
    stage TEXT NOT NULL DEFAULT 'queued',
    progress REAL NOT NULL DEFAULT 0,
    title TEXT,
    extractor TEXT,
    mode TEXT NOT NULL DEFAULT 'video',
    ingest INTEGER NOT NULL DEFAULT 0,
    quality TEXT NOT NULL DEFAULT 'best',
    format_id TEXT,
    source_profile_id TEXT,
    storage_folder TEXT NOT NULL,
    storage_visibility TEXT NOT NULL DEFAULT 'private',
    storage_file_id INTEGER,
    storage_url TEXT,
    output_name TEXT,
    output_bytes INTEGER NOT NULL DEFAULT 0,
    metadata_json TEXT NOT NULL DEFAULT '',
    warnings_json TEXT NOT NULL DEFAULT '[]',
    error TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE TABLE download_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    download_id TEXT NOT NULL,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE download_artifacts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    download_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    storage_file_id INTEGER NOT NULL,
    storage_url TEXT,
    name TEXT NOT NULL,
    content_type TEXT,
    bytes INTEGER NOT NULL DEFAULT 0,
    language TEXT,
    caption_source TEXT,
    created_at TEXT NOT NULL
);`); err != nil {
		t.Fatal(err)
	}
	ts := nowRFC3339()
	for _, row := range []struct {
		id     string
		status string
	}{
		{"running-1", statusRunning},
		{"queued-1", statusQueued},
		{"done-1", statusCompleted},
	} {
		if _, err := db.Exec(`INSERT INTO downloads(id, project_id, url, status, progress, mode, quality, storage_folder, storage_visibility, created_at, updated_at) VALUES (?, 'p1', 'https://example.com/v', ?, 84, 'video', 'best', '/out', 'private', ?, ?)`, row.id, row.status, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := interruptActiveDownloads(context.Background(), db, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("interrupted %d jobs, want 2", len(jobs))
	}
	for _, id := range []string{"running-1", "queued-1"} {
		var status, stage, msg string
		if err := db.QueryRow(`SELECT status, stage, error FROM downloads WHERE id = ?`, id).Scan(&status, &stage, &msg); err != nil {
			t.Fatal(err)
		}
		if status != statusFailed || stage != stageFailed || msg != "restart" {
			t.Fatalf("%s status=%q stage=%q error=%q, want failed/failed/restart", id, status, stage, msg)
		}
		var logs int
		if err := db.QueryRow(`SELECT count(*) FROM download_logs WHERE download_id = ? AND message = 'restart'`, id).Scan(&logs); err != nil {
			t.Fatal(err)
		}
		if logs != 1 {
			t.Fatalf("%s logs=%d, want 1", id, logs)
		}
	}
	var completed string
	if err := db.QueryRow(`SELECT status FROM downloads WHERE id = 'done-1'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != statusCompleted {
		t.Fatalf("completed job status changed to %q", completed)
	}
}

func TestDeleteProfileScrubsEncryptedPayload(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE source_profiles (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    encrypted_payload TEXT NOT NULL,
    status TEXT NOT NULL,
    last_error TEXT,
    updated_at TEXT NOT NULL
);
INSERT INTO source_profiles(id, project_id, encrypted_payload, status, last_error, updated_at)
VALUES ('src1', 'p1', 'v1:secret', 'valid', 'old error', 'now');`); err != nil {
		t.Fatal(err)
	}
	if err := deleteProfile(context.Background(), db, "src1", "p1"); err != nil {
		t.Fatal(err)
	}
	var status, encrypted, lastErr string
	if err := db.QueryRow(`SELECT status, encrypted_payload, last_error FROM source_profiles WHERE id = 'src1'`).Scan(&status, &encrypted, &lastErr); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || encrypted != "" || lastErr != "" {
		t.Fatalf("deleted profile status=%q encrypted=%q last_error=%q", status, encrypted, lastErr)
	}
}

func TestDownloadRoundTripIncludesIngestionArtifacts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"001_init.sql", "002_job_stages.sql", "003_ingestion_artifacts.sql"} {
		body, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	job := downloadJob{ID: "dl1", ProjectID: "p1", URL: "https://youtube.com/watch?v=x", Status: statusQueued, Stage: stageQueued, Mode: "video", Ingest: true, Quality: "best", StorageFolder: "/out", StorageVisibility: "private"}
	if err := insertDownload(context.Background(), db, job); err != nil {
		t.Fatal(err)
	}
	metadata := sourceMetadata{ID: "x", Title: "Video", Description: "Description", PublishDate: "2026-09-04"}
	if err := setDownloadMetadata(context.Background(), db, job.ID, metadata); err != nil {
		t.Fatal(err)
	}
	artifact := downloadArtifact{Kind: "captions", StorageFileID: 42, Name: "video.en.vtt", ContentType: "text/vtt", Language: "en", CaptionSource: "manual"}
	if err := insertDownloadArtifact(context.Background(), db, job.ID, artifact); err != nil {
		t.Fatal(err)
	}
	if err := addDownloadWarning(context.Background(), db, job.ID, "thumbnail unavailable"); err != nil {
		t.Fatal(err)
	}
	got, err := getDownload(context.Background(), db, "p1", "dl1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ingest || got.Metadata == nil || got.Metadata.Description != "Description" || len(got.Artifacts) != 1 || got.Artifacts[0].CaptionSource != "manual" || len(got.Warnings) != 1 {
		t.Fatalf("ingestion job round trip = %#v", got)
	}
}
