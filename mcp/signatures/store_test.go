package main

import (
	"database/sql"
	"os"
	"path/filepath"
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
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	for _, file := range files {
		migration, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
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
	env, active, err := activateEnvelope(db, "project-a", env.ID, "manual", "send-1")
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != "sent" || len(active) != 1 || active[0].ID != recipients[0].ID {
		t.Fatalf("unexpected activation: env=%+v active=%+v", env, active)
	}
	if _, _, _, err := createRecipientToken(db, "project-a", env.ID, recipients[1].ID); err == nil {
		t.Fatal("second sequential signer received a link before the first signed")
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
	if finalize || len(next) != 1 || next[0].ID != recipients[1].ID || completedRecipient.Status != "signed" {
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
	if !finalize || len(next) != 0 {
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

func TestParallelSigningGroup(t *testing.T) {
	db := testDB(t)
	env, err := createEnvelope(db, "project-a", StorageFile{ID: 45, Name: "release.pdf", ContentType: "application/pdf"}, "source-hash", "Model release", "Studio", "", time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	recipients, err := setRecipients(db, "project-a", env.ID, []map[string]any{
		{"name": "Model", "role": "signer", "signing_order": 1},
		{"name": "Producer", "role": "signer", "signing_order": 1},
		{"name": "Counsel", "role": "approver", "signing_order": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := setFields(db, "project-a", env.ID, []map[string]any{
		{"recipient_id": recipients[0].ID, "field_type": "signature", "page": 1, "x": .1, "y": .1, "width": .3, "height": .05},
		{"recipient_id": recipients[1].ID, "field_type": "signature", "page": 1, "x": .1, "y": .3, "width": .3, "height": .05},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, active, err := activateEnvelope(db, "project-a", env.ID, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("both first-group signers should be active, got %+v", active)
	}
	_, _, modelToken, err := createRecipientToken(db, "project-a", env.ID, recipients[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, producerToken, err := createRecipientToken(db, "project-a", env.ID, recipients[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := createRecipientToken(db, "project-a", env.ID, recipients[2].ID); err == nil {
		t.Fatal("second-group approver received a link early")
	}
	// The producer finishes first; the group is not done, nothing advances.
	_, _, next, finalize, err := completeRecipient(db, producerToken, "Pat Producer", map[int64]string{fields[1].ID: "Pat Producer"})
	if err != nil {
		t.Fatal(err)
	}
	if finalize || len(next) != 0 {
		t.Fatalf("group still signing: finalize=%v next=%+v", finalize, next)
	}
	if session, _ := sessionByToken(db, modelToken); session == nil {
		t.Fatal("model link stopped working after the producer signed")
	}
	_, _, next, finalize, err = completeRecipient(db, modelToken, "Mo Model", map[int64]string{fields[0].ID: "Mo Model"})
	if err != nil {
		t.Fatal(err)
	}
	if finalize || len(next) != 1 || next[0].ID != recipients[2].ID {
		t.Fatalf("group done should activate the approver: finalize=%v next=%+v", finalize, next)
	}
	_, _, approverToken, err := createRecipientToken(db, "project-a", env.ID, recipients[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, next, finalize, err = completeRecipient(db, approverToken, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !finalize || len(next) != 0 {
		t.Fatalf("last group should finalize: finalize=%v next=%+v", finalize, next)
	}
}

func TestSigningOrderGroupsMustBeContiguous(t *testing.T) {
	db := testDB(t)
	env, err := createEnvelope(db, "project-a", StorageFile{ID: 46, Name: "x.pdf", ContentType: "application/pdf"}, "h", "X", "", "", time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setRecipients(db, "project-a", env.ID, []map[string]any{{"name": "A", "signing_order": 1}, {"name": "B", "signing_order": 3}}); err == nil {
		t.Fatal("gap in signing_order accepted")
	}
	if _, err := setRecipients(db, "project-a", env.ID, []map[string]any{{"name": "A", "signing_order": 2}, {"name": "B", "signing_order": 2}}); err == nil {
		t.Fatal("group not starting at 1 accepted")
	}
}
