package main

// Unit tests for queueSummary — the single DB call that powers the
// MediaPanel's render-queue widget. We pin the shape (counts +
// running/pending/recent lists) against a known-state DB so future
// refactors of the underlying queries can't silently drop fields the
// panel depends on.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newRenderTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "renders.db") + "?_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Minimal renders table — same shape as the prod migration.
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE renders (
		  id              INTEGER PRIMARY KEY,
		  project_id      TEXT    NOT NULL,
		  operation       TEXT    NOT NULL,
		  source_file_ids TEXT    NOT NULL,
		  params          TEXT    NOT NULL DEFAULT '{}',
		  status          TEXT    NOT NULL DEFAULT 'pending',
		  progress_pct    INTEGER NOT NULL DEFAULT 0,
		  output_file_id  TEXT,
		  output_name     TEXT,
		  error           TEXT    NOT NULL DEFAULT '',
		  requested_by    TEXT,
		  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		  started_at      TIMESTAMP,
		  completed_at    TIMESTAMP,
		  output_folder   TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestQueueSummary_EmptyProject(t *testing.T) {
	db := newRenderTestDB(t)
	defer db.Close()
	s, err := queueSummary(db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Counts.Pending != 0 || s.Counts.Running != 0 {
		t.Errorf("empty project should have zero counts, got %+v", s.Counts)
	}
	if len(s.Pending) != 0 || len(s.Running) != 0 || len(s.Recent) != 0 {
		t.Errorf("empty project should have empty lists; pending=%d running=%d recent=%d",
			len(s.Pending), len(s.Running), len(s.Recent))
	}
}

func TestQueueSummary_PartitionsByStatus(t *testing.T) {
	db := newRenderTestDB(t)
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Seed: 3 pending, 2 running, 1 ok within 24h, 1 failed within
	// 24h, 1 cancelled within 24h, 1 ok beyond 24h (must NOT count).
	exec(`INSERT INTO renders (id, project_id, operation, source_file_ids, status) VALUES
		(1,'p1','trim','["20"]','pending'),
		(2,'p1','resize','["20"]','pending'),
		(3,'p1','transcode','["20"]','pending')`)
	exec(`INSERT INTO renders (id, project_id, operation, source_file_ids, status, started_at) VALUES
		(4,'p1','extract_reel','["20"]','running', datetime('now','-30 seconds')),
		(5,'p1','extract_frame','["20"]','running', datetime('now','-5 seconds'))`)
	exec(`INSERT INTO renders (id, project_id, operation, source_file_ids, status, output_file_id, completed_at) VALUES
		(6,'p1','trim','["20"]','ok','24', datetime('now','-1 hour')),
		(7,'p1','crop','["20"]','failed',NULL, datetime('now','-2 hours')),
		(8,'p1','resize','["20"]','cancelled',NULL, datetime('now','-3 hours'))`)
	exec(`INSERT INTO renders (id, project_id, operation, source_file_ids, status, output_file_id, completed_at) VALUES
		(9,'p1','transcode','["20"]','ok','25', datetime('now','-2 days'))`)
	// Different project — should be invisible to p1's summary.
	exec(`INSERT INTO renders (id, project_id, operation, source_file_ids, status) VALUES
		(10,'p2','trim','["99"]','pending')`)

	s, err := queueSummary(db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := RenderCounts{
		Pending: 3, Running: 2,
		Ok24h: 1, Failed24h: 1, Cancelled24h: 1,
	}
	if s.Counts != wantCounts {
		t.Errorf("counts = %+v, want %+v", s.Counts, wantCounts)
	}
	if len(s.Pending) != 3 {
		t.Errorf("pending list len = %d, want 3", len(s.Pending))
	}
	if len(s.Running) != 2 {
		t.Errorf("running list len = %d, want 2", len(s.Running))
	}
	// Recent = ok/failed/cancelled (regardless of 24h window;
	// the window only filters counts, not the list).
	if len(s.Recent) != 4 {
		t.Errorf("recent list len = %d, want 4 (1 ok+1 failed+1 cancelled in 24h + 1 ok beyond 24h)", len(s.Recent))
	}
	// FIFO order for pending — id 1 was inserted first so it must
	// be the head of the list. The worker claims this row next.
	if s.Pending[0].ID != 1 {
		t.Errorf("pending[0].ID = %d, want 1 (oldest pending = head of queue)", s.Pending[0].ID)
	}
	// Running ordered by started_at ASC — id=4 started 30s ago, id=5
	// started 5s ago, so 4 must come first.
	if s.Running[0].ID != 4 || s.Running[1].ID != 5 {
		t.Errorf("running order = [%d, %d], want [4, 5]", s.Running[0].ID, s.Running[1].ID)
	}
	// Recent ordered by completed_at DESC — id=6 is most recent.
	if s.Recent[0].ID != 6 {
		t.Errorf("recent[0].ID = %d, want 6 (newest terminal)", s.Recent[0].ID)
	}
}

func TestQueueSummary_RejectsEmptyProjectID(t *testing.T) {
	db := newRenderTestDB(t)
	defer db.Close()
	if _, err := queueSummary(db, ""); err == nil {
		t.Error("queueSummary(\"\") should error — no global lookup allowed; ambiguous which project's queue to return")
	}
}
