package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInboundDedupeMigrationsUpgradeExistingDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for version := 1; version <= 8; version++ {
		path := fmt.Sprintf("migrations/%03d_", version)
		matches, err := filepath.Glob(path + "*.sql")
		if err != nil || len(matches) != 1 {
			t.Fatalf("migration %03d lookup: matches=%v err=%v", version, matches, err)
		}
		applySQLFile(t, db, matches[0])
	}

	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`INSERT INTO messages
			(project_id, channel, direction, from_addr, status, message_id_header, provider_message_id, s3_key)
			VALUES ('project-a', 'email', 'in', 'sender@example.com', 'received', '<duplicate@example.com>', 'ses-duplicate', 'bucket/key')`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO messages
		(project_id, channel, direction, from_addr, status, provider_message_id)
		VALUES ('project-a', 'email', 'out', 'sender@example.com', 'sent', 'ses-duplicate')`); err != nil {
		t.Fatal(err)
	}

	applySQLFile(t, db, "migrations/009_inbound_dedupe_indexes.sql")
	var inbound, outbound int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE direction='in'`).Scan(&inbound); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE direction='out'`).Scan(&outbound); err != nil {
		t.Fatal(err)
	}
	if inbound != 1 || outbound != 1 {
		t.Fatalf("message counts after migration: inbound=%d outbound=%d", inbound, outbound)
	}
	if _, err := db.Exec(`INSERT INTO messages
		(project_id, channel, direction, from_addr, status, provider_message_id)
		VALUES ('project-a', 'email', 'in', 'sender@example.com', 'received', 'ses-duplicate')`); err == nil {
		t.Fatal("duplicate inbound provider id was accepted")
	}

	applySQLFile(t, db, "migrations/010_provider_event_dedupe.sql")
	var messageID int64
	if err := db.QueryRow(`SELECT id FROM messages WHERE direction='out'`).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_events (message_id, kind, provider_event_id) VALUES (?, 'delivered', 'event-1')`, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_events (message_id, kind, provider_event_id) VALUES (?, 'delivered', 'event-1')`, messageID); err == nil {
		t.Fatal("duplicate provider event id was accepted")
	}

	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`INSERT INTO message_attachments
			(project_id, message_id, filename, disposition, source, provider_ref)
			VALUES ('project-a', ?, 'invoice.pdf', 'attachment', 'mime', 'mime:0.1')`, messageID); err != nil {
			t.Fatal(err)
		}
	}
	applySQLFile(t, db, "migrations/011_attachment_processing.sql")
	var attachmentCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM message_attachments WHERE message_id = ?`, messageID).Scan(&attachmentCount); err != nil {
		t.Fatal(err)
	}
	if attachmentCount != 1 {
		t.Fatalf("attachment count after migration=%d, want 1", attachmentCount)
	}
	var processingStatus string
	if err := db.QueryRow(`SELECT processing_status FROM message_attachments WHERE message_id = ?`, messageID).Scan(&processingStatus); err != nil {
		t.Fatal(err)
	}
	if processingStatus != "ready" {
		t.Fatalf("processing_status=%q, want ready", processingStatus)
	}
	if _, err := db.Exec(`INSERT INTO message_attachments
		(project_id, message_id, filename, disposition, source, provider_ref)
		VALUES ('project-a', ?, 'invoice-again.pdf', 'attachment', 'mime', 'mime:0.1')`, messageID); err == nil {
		t.Fatal("duplicate attachment provider_ref was accepted")
	}
}

func applySQLFile(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}
