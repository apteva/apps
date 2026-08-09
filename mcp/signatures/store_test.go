package main

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func createPreparedEnvelope(t *testing.T, db *sql.DB) (*Envelope, []Recipient, []Field) {
	t.Helper()
	env, err := createEnvelope(db, "project-a", StorageFile{ID: 44, Name: "contract.pdf", ContentType: "application/pdf"}, "source-hash", "Service agreement", "Acme", "Please sign", time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	recipients, err := setRecipients(db, "project-a", env.ID, []map[string]any{
		{"name": "Ada", "email": "ada@example.test", "role": "signer", "signing_order": 1},
		{"name": "Linus", "email": "linus@example.test", "role": "signer", "signing_order": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := setFields(db, "project-a", env.ID, []map[string]any{
		{"recipient_id": recipients[0].ID, "field_type": "signature", "page": 1, "x": .1, "y": .7, "width": .3, "height": .08, "label": "Client signature", "required": true},
		{"recipient_id": recipients[1].ID, "field_type": "signature", "page": 1, "x": .55, "y": .7, "width": .3, "height": .08, "label": "Company signature", "required": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env, recipients, fields
}

func TestSequentialSigningAndTokenRevocation(t *testing.T) {
	db := testDB(t)
	env, recipients, fields := createPreparedEnvelope(t, db)
	if errs := validateEnvelope(db, "project-a", env.ID, 1, "manual"); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	env, current, err := activateEnvelope(db, "project-a", env.ID, "manual", "send-1")
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != "sent" || current == nil || current.ID != recipients[0].ID {
		t.Fatalf("unexpected activation: env=%+v current=%+v", env, current)
	}

	_, _, tokenOne, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessionByToken(db, tokenOne)
	if err != nil || session == nil || session.Recipient.ID != recipients[0].ID {
		t.Fatalf("first session: session=%+v err=%v", session, err)
	}
	completedEnv, completedRecipient, next, finalize, err := completeRecipient(db, tokenOne, "Ada Lovelace", map[int64]string{fields[0].ID: "Ada Lovelace"})
	if err != nil {
		t.Fatal(err)
	}
	if finalize || next == nil || next.ID != recipients[1].ID || completedRecipient.Status != "signed" {
		t.Fatalf("unexpected first completion: finalize=%v next=%+v recipient=%+v", finalize, next, completedRecipient)
	}
	if stale, _ := sessionByToken(db, tokenOne); stale != nil {
		t.Fatal("completed recipient token remained active")
	}
	if _, _, _, err := createRecipientToken(db, "project-a", completedEnv.ID, recipients[0].ID); err == nil {
		t.Fatal("completed recipient received a new link")
	}

	_, _, tokenTwo, err := createRecipientToken(db, "project-a", completedEnv.ID, recipients[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, next, finalize, err = completeRecipient(db, tokenTwo, "Linus Torvalds", map[int64]string{fields[1].ID: "Linus Torvalds"})
	if err != nil {
		t.Fatal(err)
	}
	if !finalize || next != nil {
		t.Fatalf("last recipient should trigger finalization: finalize=%v next=%+v", finalize, next)
	}
}

func TestRotatingLinkRevokesPreviousToken(t *testing.T) {
	db := testDB(t)
	env, recipients, _ := createPreparedEnvelope(t, db)
	if _, _, err := activateEnvelope(db, "project-a", env.ID, "manual", ""); err != nil {
		t.Fatal(err)
	}
	_, _, first, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, second, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("token rotation reused token")
	}
	if session, _ := sessionByToken(db, first); session != nil {
		t.Fatal("old token remained active")
	}
	if session, _ := sessionByToken(db, second); session == nil {
		t.Fatal("new token is not active")
	}
}

func TestProjectIsolationAndDraftMutability(t *testing.T) {
	db := testDB(t)
	env, _, _ := createPreparedEnvelope(t, db)
	if got, err := getEnvelope(db, "project-b", env.ID); err != nil || got != nil {
		t.Fatalf("cross-project lookup leaked envelope: got=%+v err=%v", got, err)
	}
	if _, _, err := activateEnvelope(db, "project-a", env.ID, "manual", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := updateEnvelope(db, "project-a", env.ID, map[string]any{"title": "Changed"}); err == nil {
		t.Fatal("sent envelope was mutable")
	}
}

func TestValidationRequiresSignatureForEachSigner(t *testing.T) {
	db := testDB(t)
	env, recipients, _ := createPreparedEnvelope(t, db)
	if _, err := setFields(db, "project-a", env.ID, []map[string]any{
		{"recipient_id": recipients[0].ID, "field_type": "signature", "page": 1, "x": .1, "y": .7, "width": .3, "height": .08},
	}); err != nil {
		t.Fatal(err)
	}
	errs := validateEnvelope(db, "project-a", env.ID, 1, "manual")
	if len(errs) != 1 {
		t.Fatalf("expected one validation error, got %v", errs)
	}
}
