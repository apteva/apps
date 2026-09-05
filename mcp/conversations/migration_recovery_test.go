package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeFrom0182PreservesHistoryAndBackfillsRecovery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	run := func(statement string) {
		t.Helper()
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	run("PRAGMA foreign_keys=ON")
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "009_") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		run(string(raw))
	}
	run(`INSERT INTO conversations(id,project_id,lead_agent_id,title,owner_user_id) VALUES('old','project',41,'Existing private conversation',7)`)
	run(`INSERT INTO participants(conversation_id,agent_id) VALUES('old',41)`)
	run(`INSERT INTO messages(conversation_id,role,content,component_kind,components_json,client_message_id) VALUES('old','agent','Original report','report','[{"app":"conversations","name":"report-card","props":{"dismissed":true}}]','old-request')`)
	run(`INSERT INTO deliveries(message_id,target,status) VALUES(1,'web:conv','delivered')`)
	migration, err := os.ReadFile("migrations/009_recovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(string(migration)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var content string
	var owner, dismissed, changes int
	if err = db.QueryRow(`SELECT m.content,m.dismissed,c.owner_user_id FROM messages m JOIN conversations c ON c.id=m.conversation_id WHERE m.id=1`).Scan(&content, &dismissed, &owner); err != nil {
		t.Fatal(err)
	}
	if content != "Original report" || dismissed != 1 || owner != 7 {
		t.Fatalf("upgrade changed history/access: %q %d %d", content, dismissed, owner)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM message_changes WHERE message_id=1`).Scan(&changes); err != nil || changes != 1 {
		t.Fatalf("backfill=%d err=%v", changes, err)
	}
	run(`UPDATE messages SET content='Updated report' WHERE id=1`)
	var status string
	var generation int
	if err = db.QueryRow(`SELECT status,generation FROM deliveries WHERE message_id=1`).Scan(&status, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || generation != 1 {
		t.Fatalf("upgraded surface did not requeue: %s %d", status, generation)
	}
	var result string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		t.Fatalf("integrity %s %v", result, err)
	}
}
