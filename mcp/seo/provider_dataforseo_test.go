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
		"location_type": "Country",
		"available_languages": [
			{
				"available_sources": ["google"],
				"language_code": "en",
				"language_name": "English",
				"keywords": 123,
				"serps": 456
			}
		]
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

func TestSeedYouTubeLocationsFromGoogle_CopiesResolvableLocales(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")
	if _, err := db.Exec(`INSERT INTO seo_locations
		(provider, search_engine, location_code, location_name, country_iso, language_code, language_name, raw_json)
		VALUES
		('dataforseo', 'google', 2840, 'United States', 'US', 'en', 'English', '{"source":"google"}'),
		('dataforseo', 'google', 2826, 'United Kingdom', 'GB', 'en', 'English', '{"source":"google"}')`); err != nil {
		t.Fatalf("insert google locations: %v", err)
	}

	upserts, skipped, err := seedYouTubeLocationsFromGoogle(db, 22222)
	if err != nil {
		t.Fatalf("seedYouTubeLocationsFromGoogle returned error: %v", err)
	}
	if upserts != 2 || skipped != 0 {
		t.Fatalf("upserts/skipped = %d/%d, want 2/0", upserts, skipped)
	}

	loc, err := resolveLocationFromArgs(db, map[string]any{
		"search_engine": "youtube",
		"country_iso":   "US",
		"language_code": "en",
	}, nil)
	if err != nil {
		t.Fatalf("resolve youtube US/en location: %v", err)
	}
	if loc == nil || loc.SearchEngine != "youtube" || loc.LocationCode == nil || *loc.LocationCode != 2840 {
		t.Fatalf("resolved location = %+v, want youtube 2840", loc)
	}

	var raw string
	if err := db.QueryRow(`SELECT raw_json FROM seo_locations WHERE search_engine = 'youtube' AND country_iso = 'US' AND language_code = 'en'`).Scan(&raw); err != nil {
		t.Fatalf("read fallback raw_json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("fallback raw_json is invalid: %v", err)
	}
	if payload["fallback"] != "google_location_for_youtube" {
		t.Fatalf("fallback marker = %v, want google_location_for_youtube", payload["fallback"])
	}
}

func TestSeedYouTubeLocationsFromGoogle_RequiresGoogleRows(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")
	if _, _, err := seedYouTubeLocationsFromGoogle(db, 22222); err == nil {
		t.Fatal("expected empty google location catalog to fail")
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

func TestNormalizeSERPItem_YouTubeClassifiesMixedResults(t *testing.T) {
	video := normalizeSERPItem("youtube", map[string]any{
		"type":         "youtube_video",
		"title":        "Obedient Trigger - Hypnosis",
		"url":          "https://www.youtube.com/watch?v=LJ-MYX3YNjg",
		"channel_name": "Nimja Hypnosis",
	})
	if video.ResultType != "video" || video.EntityType != "video" || video.Identifier != "LJ-MYX3YNjg" {
		t.Fatalf("video item = %+v, want video entity with video id", video)
	}

	channel := normalizeSERPItem("youtube", map[string]any{
		"type": "youtube_channel",
		"name": "UltraHypnosis",
		"url":  "https://www.youtube.com/@UltraHypnosis",
	})
	if channel.ResultType != "channel" || channel.EntityType != "channel" || channel.Identifier != "@UltraHypnosis" {
		t.Fatalf("channel item = %+v, want channel entity with handle", channel)
	}

	playlist := normalizeSERPItem("youtube", map[string]any{
		"type":  "youtube_playlist",
		"title": "Slave/Control Hypnosis Videos",
		"url":   "https://www.youtube.com/playlist?list=PL9EKyZt9BIs-WWV5OfWGZqvZm4ewvvFEF",
	})
	if playlist.ResultType != "playlist" || playlist.EntityType != "" || playlist.Identifier != "PL9EKyZt9BIs-WWV5OfWGZqvZm4ewvvFEF" {
		t.Fatalf("playlist item = %+v, want playlist result without tracked entity", playlist)
	}
}

func TestYouTubeIdeasFromCachedSERPs_UsesVideoRowsOnly(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql", "migrations/004_search_entities.sql")
	if _, err := db.Exec(`INSERT INTO seo_locations
		(provider, search_engine, location_code, location_name, country_iso, language_code, language_name)
		VALUES ('dataforseo', 'youtube', 2840, 'United States', 'US', 'en', 'English')`); err != nil {
		t.Fatalf("insert location: %v", err)
	}
	res, err := db.Exec(`INSERT INTO search_serp_snapshots
		(project_id, search_engine, keyword_text, location_id, provider, ts, raw_json)
		VALUES ('project-1', 'youtube', 'hypnosis obedience trigger', 1, 'dataforseo', 12345, '{}')`)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	snapshotID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO search_serp_results
		(snapshot_id, rank, result_type, title, url, channel_title, raw_json)
		VALUES
		(?, 1, 'channel', 'UltraHypnosis', 'https://www.youtube.com/@UltraHypnosis', '', '{}'),
		(?, 2, 'video', 'Obedient Trigger - Hypnosis', 'https://www.youtube.com/watch?v=LJ-MYX3YNjg', 'Nimja Hypnosis', '{}')`,
		snapshotID, snapshotID); err != nil {
		t.Fatalf("insert SERP rows: %v", err)
	}

	got, err := youtubeIdeasFromCachedSERPs(db, "project-1", []string{"hypnosis obedience trigger"}, 20)
	if err != nil {
		t.Fatalf("youtubeIdeasFromCachedSERPs returned error: %v", err)
	}
	payload := got.(map[string]any)
	items := payload["items"].([]*keywordIdea)
	for _, item := range items {
		if item.Keyword == "ultrahypnosis" {
			t.Fatalf("channel title leaked into keyword ideas: %+v", item)
		}
	}
	if len(items) == 0 || items[0].Keyword != "hypnosis obedience trigger" {
		t.Fatalf("items = %+v, want video-derived seed idea", items)
	}
}

func TestYouTubeRankingsForKeyword_ReturnsLatestVideoRowsOnly(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql", "migrations/004_search_entities.sql", "migrations/005_search_engine_keyword_backfill.sql")
	if _, err := db.Exec(`INSERT INTO seo_locations
		(provider, search_engine, location_code, location_name, country_iso, language_code, language_name)
		VALUES ('dataforseo', 'youtube', 2840, 'United States', 'US', 'en', 'English')`); err != nil {
		t.Fatalf("insert location: %v", err)
	}
	res, err := db.Exec(`INSERT INTO keywords
		(project_id, search_engine, text, location_id, country_iso, language_iso)
		VALUES ('project-1', 'youtube', 'hypnosis obedience trigger', 1, 'US', 'en')`)
	if err != nil {
		t.Fatalf("insert keyword: %v", err)
	}
	keywordID, _ := res.LastInsertId()
	insertSnapshot := func(ts int64) int64 {
		t.Helper()
		res, err := db.Exec(`INSERT INTO search_serp_snapshots
			(project_id, search_engine, keyword_id, keyword_text, location_id, provider, ts, raw_json)
			VALUES ('project-1', 'youtube', ?, 'hypnosis obedience trigger', 1, 'dataforseo', ?, '{}')`,
			keywordID, ts)
		if err != nil {
			t.Fatalf("insert snapshot: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	oldSnapshot := insertSnapshot(100)
	latestSnapshot := insertSnapshot(200)
	if _, err := db.Exec(`INSERT INTO search_serp_results
		(snapshot_id, rank, result_type, title, url, channel_title, raw_json)
		VALUES
		(?, 1, 'video', 'Old Video', 'https://www.youtube.com/watch?v=old', 'Old Channel', '{}'),
		(?, 1, 'channel', 'UltraHypnosis', 'https://www.youtube.com/@UltraHypnosis', '', '{}'),
		(?, 2, 'playlist', 'Hypnosis Playlist', 'https://www.youtube.com/playlist?list=abc', '', '{}'),
		(?, 3, 'video', 'Obedient Trigger - Hypnosis', 'https://www.youtube.com/watch?v=LJ-MYX3YNjg', 'Nimja Hypnosis', '{}')`,
		oldSnapshot, latestSnapshot, latestSnapshot, latestSnapshot); err != nil {
		t.Fatalf("insert SERP rows: %v", err)
	}

	k := &Keyword{ID: keywordID, SearchEngine: "youtube", Text: "hypnosis obedience trigger", LocationID: 1}
	current, err := youtubeRankingsForKeyword(db, "project-1", k, 0, 20, false)
	if err != nil {
		t.Fatalf("youtubeRankingsForKeyword current returned error: %v", err)
	}
	if len(current) != 1 || current[0].Title != "Obedient Trigger - Hypnosis" || current[0].ResultType != "video" {
		t.Fatalf("current rows = %+v, want latest video row only", current)
	}

	history, err := youtubeRankingsForKeyword(db, "project-1", k, 0, 20, true)
	if err != nil {
		t.Fatalf("youtubeRankingsForKeyword history returned error: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history rows = %+v, want old and latest video rows only", history)
	}
}

func TestSearchEngineKeywordBackfillMigration_CleansLegacyYouTubeRows(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql", "migrations/004_search_entities.sql")
	if _, err := db.Exec(`INSERT INTO seo_locations
		(provider, search_engine, location_code, location_name, country_iso, language_code, language_name)
		VALUES ('dataforseo', 'youtube', 2840, 'United States', 'US', 'en', 'English')`); err != nil {
		t.Fatalf("insert location: %v", err)
	}
	res, err := db.Exec(`INSERT INTO search_serp_snapshots
		(project_id, search_engine, keyword_text, location_id, provider, ts, raw_json)
		VALUES ('project-1', 'youtube', 'sleep hypnosis', 1, 'dataforseo', 200, '{}')`)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	snapshotID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO search_serp_results
		(snapshot_id, rank, result_type, title, url, raw_json)
		VALUES
		(?, 1, 'youtube_channel', 'Channel Result', 'https://www.youtube.com/@channel', '{}'),
		(?, 2, 'youtube_playlist', 'Playlist Result', 'https://www.youtube.com/playlist?list=abc', '{}'),
		(?, 3, 'youtube_video', 'Video Result', 'https://www.youtube.com/watch?v=abc', '{}')`,
		snapshotID, snapshotID, snapshotID); err != nil {
		t.Fatalf("insert legacy rows: %v", err)
	}

	applySEOMigration(t, db, "migrations/005_search_engine_keyword_backfill.sql")

	var keywordID int64
	if err := db.QueryRow(`SELECT keyword_id FROM search_serp_snapshots WHERE id = ?`, snapshotID).Scan(&keywordID); err != nil {
		t.Fatalf("read snapshot keyword_id: %v", err)
	}
	if keywordID == 0 {
		t.Fatalf("snapshot keyword_id was not backfilled")
	}
	var searchEngine string
	if err := db.QueryRow(`SELECT search_engine FROM keywords WHERE id = ?`, keywordID).Scan(&searchEngine); err != nil {
		t.Fatalf("read keyword search_engine: %v", err)
	}
	if searchEngine != "youtube" {
		t.Fatalf("keyword search_engine = %q, want youtube", searchEngine)
	}
	var total, bad int
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_serp_results WHERE snapshot_id = ?`, snapshotID).Scan(&total); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_serp_results WHERE snapshot_id = ? AND result_type <> 'video'`, snapshotID).Scan(&bad); err != nil {
		t.Fatalf("count bad rows: %v", err)
	}
	if total != 1 || bad != 0 {
		t.Fatalf("after cleanup total=%d bad=%d, want one video row only", total, bad)
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
		applySEOMigration(t, db, migration)
	}
	return db
}

func applySEOMigration(t *testing.T, db *sql.DB, migration string) {
	t.Helper()
	body, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read %s: %v", migration, err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply %s: %v", migration, err)
	}
}
