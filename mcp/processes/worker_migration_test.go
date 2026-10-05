package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutorWorkerMigrationPreservesHistoricalOwnership(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE process_runs (id TEXT PRIMARY KEY); INSERT INTO process_runs VALUES('existing');`); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("migrations/008_run_workers.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO process_run_workers VALUES('existing',7,'original-worker','original-created-at')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/014_executor_workers.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var thread, created string
	if err = db.QueryRow(`SELECT thread_id,created_at FROM process_run_workers WHERE run_id='existing' AND agent_id=7`).Scan(&thread, &created); err != nil || thread != "original-worker" || created != "original-created-at" {
		t.Fatal("historical ownership changed", thread, created, err)
	}
	if _, err = db.Exec(`INSERT INTO process_run_workers VALUES('existing',8,'second-executor','new')`); err != nil {
		t.Fatal("second executor rejected", err)
	}
	if _, err = db.Exec(`INSERT INTO process_run_workers VALUES('existing',7,'competitor','new')`); err == nil {
		t.Fatal("same executor owner replaced")
	}
}
