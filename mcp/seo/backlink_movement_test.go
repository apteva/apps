package main

import (
	"database/sql"
	"testing"
	"time"
)

func TestCachedBacklinkMovementUsesProviderHistoryFields(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")
	result, err := db.Exec(`INSERT INTO domains (project_id, host, label) VALUES ('project-1', 'example.com', 'Example')`)
	if err != nil {
		t.Fatal(err)
	}
	domainID, _ := result.LastInsertId()
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	insertMovementBacklink(t, db, domainID, "dataforseo", "active-new", now.AddDate(0, 0, -1).Unix(), now.Unix(), false)
	insertMovementBacklink(t, db, domainID, "dataforseo", "active-old", now.AddDate(0, 0, -120).Unix(), now.Unix(), false)
	insertMovementBacklink(t, db, domainID, "dataforseo", "lost-recent", now.AddDate(0, 0, -5).Unix(), now.AddDate(0, 0, -2).Unix(), true)
	insertMovementBacklink(t, db, domainID, "dataforseo", "lost-old", now.AddDate(0, 0, -200).Unix(), now.AddDate(0, 0, -150).Unix(), true)
	insertMovementBacklink(t, db, domainID, "yepapi", "other-provider", now.Unix(), now.Unix(), false)

	movement, err := cachedBacklinkMovement(db, domainID, "dataforseo", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if movement.ActiveLinks != 2 || movement.LostLinks != 2 {
		t.Fatalf("active/lost = %d/%d, want 2/2", movement.ActiveLinks, movement.LostLinks)
	}
	if movement.GainedInRange != 2 || movement.LostInRange != 1 || movement.NetChange != 1 {
		t.Fatalf("range gained/lost/net = %d/%d/%d, want 2/1/1", movement.GainedInRange, movement.LostInRange, movement.NetChange)
	}
	if movement.KnownFirstSeen != 4 || movement.KnownLostDate != 2 {
		t.Fatalf("coverage = first %d, lost %d, want 4/2", movement.KnownFirstSeen, movement.KnownLostDate)
	}
	if len(movement.Points) != 30 || movement.Points[29].Date != "2026-08-27" {
		t.Fatalf("points = %d, final = %+v", len(movement.Points), movement.Points[len(movement.Points)-1])
	}
	if got := movementPoint(movement.Points, "2026-08-26"); got == nil || got.Gained != 1 {
		t.Fatalf("2026-08-26 point = %+v", got)
	}
	if got := movementPoint(movement.Points, "2026-08-25"); got == nil || got.Lost != 1 {
		t.Fatalf("2026-08-25 point = %+v", got)
	}
}

func TestCachedBacklinkMovementDefaultsAndCapsRange(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")
	result, err := cachedBacklinkMovement(db, 1, "", 0, time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Days != 90 || result.Provider != "all" || len(result.Points) != 90 {
		t.Fatalf("default movement = %+v", result)
	}
	result, err = cachedBacklinkMovement(db, 1, "", 900, time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Days != 730 || len(result.Points) != 730 {
		t.Fatalf("capped days = %d, points = %d", result.Days, len(result.Points))
	}
}

func insertMovementBacklink(t *testing.T, db *sql.DB, domainID int64, provider, source string, firstSeen, lastSeen int64, lost bool) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO backlinks
		(domain_id, provider, source_url, dest_url, anchor, first_seen, last_seen, is_lost)
		VALUES (?, ?, ?, 'https://example.com/', '', ?, ?, ?)`,
		domainID, provider, "https://"+source+".example/link", firstSeen, lastSeen, boolToInt(lost))
	if err != nil {
		t.Fatal(err)
	}
}

func movementPoint(points []BacklinkMovementPoint, date string) *BacklinkMovementPoint {
	for i := range points {
		if points[i].Date == date {
			return &points[i]
		}
	}
	return nil
}
