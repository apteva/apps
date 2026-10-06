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

func TestGmailBackfillMigrationPreservesExistingMailboxAndMessages(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version := 1; version <= 15; version++ {
		matches, err := filepath.Glob(fmt.Sprintf("migrations/%03d_*.sql", version))
		if err != nil || len(matches) != 1 {
			t.Fatalf("migration %d lookup: %v %v", version, matches, err)
		}
		applySQLFile(t, db, matches[0])
	}
	if _, err := db.Exec(`INSERT INTO gmail_sync_state(project_id,connection_id,mailbox,history_id) VALUES('project-a',3,'support@example.com','2365');
		INSERT INTO messages(id,project_id,channel,direction,from_addr,status,body_text,provider_slug,provider_connection_id,provider_message_id)
		VALUES(40,'project-a','email','in','customer@example.org','received','Incoming preserved','gmail',3,'in-1'),
		(41,'project-a','email','out','support@example.com','sent','Outgoing preserved','gmail',3,'out-1')`); err != nil {
		t.Fatal(err)
	}
	applySQLFile(t, db, "migrations/016_gmail_mailbox_backfill.sql")
	var cursor, mailbox, token, incoming, outgoing string
	var complete int
	if err := db.QueryRow(`SELECT history_id,mailbox,backfill_complete,backfill_page_token FROM gmail_sync_state`).Scan(&cursor, &mailbox, &complete, &token); err != nil || cursor != "2365" || mailbox != "support@example.com" || complete != 0 || token != "" {
		t.Fatalf("existing mailbox changed: %s %s %d %s %v", cursor, mailbox, complete, token, err)
	}
	if err := db.QueryRow(`SELECT body_text FROM messages WHERE id=40`).Scan(&incoming); err != nil || incoming != "Incoming preserved" {
		t.Fatalf("incoming message lost: %s %v", incoming, err)
	}
	if err := db.QueryRow(`SELECT body_text FROM messages WHERE id=41`).Scan(&outgoing); err != nil || outgoing != "Outgoing preserved" {
		t.Fatalf("outgoing message lost: %s %v", outgoing, err)
	}
	if _, err := db.Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,status,provider_slug,provider_connection_id,provider_message_id) VALUES('project-a','email','in','support@example.com','received','gmail',3,'out-1')`); err == nil {
		t.Fatal("same Gmail delivery duplicated across directions")
	}
}

func TestUpgradeFromMessaging01346PreservesMessageContentAndIDs(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version := 1; version <= 11; version++ {
		matches, err := filepath.Glob(fmt.Sprintf("migrations/%03d_*.sql", version))
		if err != nil || len(matches) != 1 {
			t.Fatalf("migration %d lookup: %v err=%v", version, matches, err)
		}
		applySQLFile(t, db, matches[0])
	}
	if _, err := db.Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,subject,body_text,body_html,message_id_header,provider_message_id,status,route_status)
		VALUES(31898,'project-a','email','in','sender@example.test','["inbox@example.test"]','Example','',?,'<original@example.test>','ses-original','received','ok'),
		(31414,'project-a','email','out','inbox@example.test','["sender@example.test"]','Reply','Original outbound text','','<outbound@example.test>','ses-outbound','sent','pending')`, testHTMLOnlyEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message_attachments(project_id,message_id,storage_id,filename,disposition,source,provider_ref) VALUES('project-a',31898,88,'document.pdf','attachment','mime','mime:0.1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_events(message_id,kind,recipient,provider_event_id) VALUES(31414,'delivered','sender@example.test','event-original')`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"migrations/012_reliable_processing.sql", "migrations/013_message_search.sql", "migrations/014_email_providers.sql"} {
		applySQLFile(t, db, path)
	}
	var html, text, header, provider, route string
	if err := db.QueryRow(`SELECT body_html,body_text,message_id_header,provider_message_id,route_status FROM messages WHERE id=31898`).Scan(&html, &text, &header, &provider, &route); err != nil {
		t.Fatal(err)
	}
	if html != testHTMLOnlyEmail || text != "" || header != "<original@example.test>" || provider != "ses-original" || route != "ok" {
		t.Fatal("upgrade changed the original inbound message")
	}
	if err := db.QueryRow(`SELECT body_text FROM messages WHERE id=31414`).Scan(&text); err != nil || text != "Original outbound text" {
		t.Fatalf("upgrade changed outbound content: %q err=%v", text, err)
	}
	for table, want := range map[string]int{"messages": 2, "message_attachments": 1, "delivery_events": 1, "inbound_jobs": 0, "message_search": 2, "recipient_delivery_status": 1} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != want {
			t.Fatalf("upgrade %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("upgraded database integrity=%q err=%v", check, err)
	}
}
