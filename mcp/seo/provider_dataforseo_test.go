package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

func TestDomainRankOverviewInput_OmitsLanguageCode(t *testing.T) {
	locationCode := int64(2840)
	input := domainRankOverviewInput(&Domain{Host: "marcoschwartz.com"}, &SEOLocation{
		LocationCode: &locationCode,
		LanguageCode: "en",
	})
	if got := input["target"]; got != "marcoschwartz.com" {
		t.Fatalf("target = %v, want marcoschwartz.com", got)
	}
	if got := input["location_code"]; got != locationCode {
		t.Fatalf("location_code = %v, want %d", got, locationCode)
	}
	if _, ok := input["language_code"]; ok {
		t.Fatal("domain_rank_overview input must not include language_code")
	}
}

func TestDecodeKeywordVolumeItem_DirectResultRow(t *testing.T) {
	raw := json.RawMessage(`{
		"keyword": "seo",
		"location_code": 2840,
		"language_code": "en",
		"search_volume": 110000,
		"competition_index": 28,
		"cpc": 25.48,
		"monthly_searches": [
			{"year": 2026, "month": 5, "search_volume": 60500}
		]
	}`)

	item, err := decodeKeywordVolumeItem(raw, "seo")
	if err != nil {
		t.Fatalf("decodeKeywordVolumeItem returned error: %v", err)
	}
	if item.Keyword != "seo" {
		t.Fatalf("keyword = %q, want seo", item.Keyword)
	}
	if item.SearchVolume == nil || *item.SearchVolume != 110000 {
		t.Fatalf("search_volume = %v, want 110000", item.SearchVolume)
	}
	if len(item.MonthlySearches) != 1 || item.MonthlySearches[0].Volume != 60500 {
		t.Fatalf("monthly_searches = %+v, want one 60500 row", item.MonthlySearches)
	}
}

func TestDecodeKeywordVolumeItem_ItemsWrapper(t *testing.T) {
	raw := json.RawMessage(`{
		"items": [{
			"keyword": "seo",
			"location_code": 2840,
			"language_code": "en",
			"search_volume": 110000
		}]
	}`)

	item, err := decodeKeywordVolumeItem(raw, "seo")
	if err != nil {
		t.Fatalf("decodeKeywordVolumeItem returned error: %v", err)
	}
	if item.Keyword != "seo" {
		t.Fatalf("keyword = %q, want seo", item.Keyword)
	}
	if item.SearchVolume == nil || *item.SearchVolume != 110000 {
		t.Fatalf("search_volume = %v, want 110000", item.SearchVolume)
	}
}

func TestDecodeKeywordVolumeItem_Empty(t *testing.T) {
	if _, err := decodeKeywordVolumeItem(json.RawMessage(`{}`), "seo"); err == nil {
		t.Fatal("expected empty keyword volume row to fail")
	}
}

func TestSyncDataForSEOGoogleLocationRows_CommitsGoogleRows(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")

	rows := []json.RawMessage{json.RawMessage(`{
		"location_code": 2826,
		"location_name": "United Kingdom",
		"country_iso_code": "GB",
		"language_code": "en",
		"language_name": "English",
		"available_sources": ["google"]
	}`)}
	upserts, skipped, err := syncDataForSEOGoogleLocationRows(db, rows, 12345)
	if err != nil {
		t.Fatalf("syncDataForSEOGoogleLocationRows returned error: %v", err)
	}
	if upserts != 1 || skipped != 0 {
		t.Fatalf("upserts/skipped = %d/%d, want 1/0", upserts, skipped)
	}

	var count int
	err = db.QueryRow(`
		SELECT count(*)
		  FROM seo_locations
		 WHERE provider = 'dataforseo'
		   AND search_engine = 'google'
		   AND location_code = 2826
		   AND country_iso = 'GB'
		   AND language_code = 'en'
		   AND synced_at = 12345`).Scan(&count)
	if err != nil {
		t.Fatalf("count synced location: %v", err)
	}
	if count != 1 {
		t.Fatalf("synced google location count = %d, want 1", count)
	}
}

func TestUpsertSearchEntity_ReturnsCanonicalIDAfterConflict(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql", "migrations/004_search_entities.sql")
	if _, err := db.Exec(`INSERT INTO seo_locations
		(provider, search_engine, location_code, location_name, country_iso, language_code, language_name)
		VALUES ('dataforseo', 'google', 2840, 'United States', 'US', 'en', 'English')`); err != nil {
		t.Fatalf("insert location: %v", err)
	}

	entityID, err := upsertSearchEntity(db, "project-1", "google", "page",
		"https://example.com/a", "Original", "https://example.com/a", int64(1), "{}")
	if err != nil {
		t.Fatalf("initial upsertSearchEntity returned error: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO search_serp_snapshots
		(project_id, search_engine, keyword_text, location_id, provider, ts, raw_json)
		VALUES ('project-1', 'google', 'p2p lending europe', 1, 'dataforseo', 12345, '{}')`); err != nil {
		t.Fatalf("insert dummy snapshot: %v", err)
	}
	res, err := tx.Exec(`INSERT INTO search_serp_snapshots
		(project_id, search_engine, keyword_text, location_id, provider, ts, raw_json)
		VALUES ('project-1', 'google', 'p2p lending europe', 1, 'dataforseo', 12346, '{}')`)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	snapshotID, _ := res.LastInsertId()
	if snapshotID == entityID {
		t.Fatalf("test setup expected snapshot id %d to differ from entity id %d", snapshotID, entityID)
	}

	gotID, err := upsertSearchEntity(tx, "project-1", "google", "page",
		"https://example.com/a", "Updated", "https://example.com/a", int64(1), `{"fresh":true}`)
	if err != nil {
		t.Fatalf("conflict upsertSearchEntity returned error: %v", err)
	}
	if gotID != entityID {
		t.Fatalf("conflict upsertSearchEntity id = %d, want canonical id %d", gotID, entityID)
	}
	if _, err := tx.Exec(`INSERT INTO search_serp_results
		(snapshot_id, entity_id, rank, result_type, title, url, identifier, raw_json)
		VALUES (?, ?, 1, 'organic', 'Updated', 'https://example.com/a', 'https://example.com/a', '{}')`,
		snapshotID, gotID); err != nil {
		t.Fatalf("insert result with returned entity id: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
}

func newSEOTestDB(t *testing.T, migrations ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	for _, migration := range migrations {
		body, err := os.ReadFile(migration)
		if err != nil {
			t.Fatalf("read %s: %v", migration, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	return db
}
