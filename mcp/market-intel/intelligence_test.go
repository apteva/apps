package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEvidenceSearchIsBitemporalAndIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := os.ReadFile("migrations/003_intelligence_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	known := time.Date(2025, 1, 10, 0, 0, 0, 0, time.UTC)
	e := Evidence{Kind: "claim", Title: "Battery rule", Body: "New tariff announced", Source: "regulator", SourceRef: "r-1", EntityRefs: []string{"acme"}, EventTime: ptrTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)), ObservedAt: &known, Payload: json.RawMessage(`{"status":"new"}`)}
	if _, err = recordEvidence(db, e, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err = recordEvidence(db, e, "p"); err != nil {
		t.Fatal(err)
	}
	rows, err := searchEvidence(db, EvidenceQuery{ProjectID: "p", Text: "battery tariff", AsOf: ptrTime(time.Date(2025, 1, 9, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("future evidence leaked into as-of query: %+v", rows)
	}
	rows, err = searchEvidence(db, EvidenceQuery{ProjectID: "p", Entity: "acme", AsOf: &known})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one deduplicated known record, got %d", len(rows))
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
