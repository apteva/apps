package main

import (
	"database/sql"
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

type yepPlatformStub struct {
	testkit.BasePlatformClient
	responses   map[string]json.RawMessage
	calls       []string
	identity    *sdk.InstallIdentity
	connections map[int64]*sdk.PlatformConnection
}

func (s *yepPlatformStub) ExecuteIntegrationTool(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	s.calls = append(s.calls, tool)
	data := s.responses[tool]
	if data == nil {
		data = json.RawMessage(`{"ok":true,"data":{}}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func (s *yepPlatformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	return s.identity, nil
}

func (s *yepPlatformStub) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return s.connections[id], nil
}

func TestProviderSelection_SupportsMultipleBindingsAndDefault(t *testing.T) {
	manifest := (&App{}).Manifest()
	stub := &yepPlatformStub{
		identity: &sdk.InstallIdentity{Bindings: map[string]any{
			providerRole: map[string]any{"ids": []int64{11, 22}, "default_id": int64(22)},
		}},
		connections: map[int64]*sdk.PlatformConnection{
			11: {ID: 11, AppSlug: "dataforseo"},
			22: {ID: 22, AppSlug: "yepapi"},
		},
	}
	ctx := sdk.NewAppCtxForTest(&manifest, nil, nil, stub, nil)
	selected, err := selectProvider(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Slug() != "yepapi" || selected.ConnectionID() != 22 {
		t.Fatalf("default provider = %s/%d, want yepapi/22", selected.Slug(), selected.ConnectionID())
	}
	explicit, err := selectProvider(ctx, "dataforseo")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Slug() != "dataforseo" || explicit.ConnectionID() != 11 {
		t.Fatalf("explicit provider = %s/%d, want dataforseo/11", explicit.Slug(), explicit.ConnectionID())
	}
}

func TestDecodeYepKeywordMetrics_WrappedAndDirect(t *testing.T) {
	wrapped, err := decodeYepKeywordMetrics([]byte(`{"keywords":[{"keyword":"seo api","volume":390,"difficulty":27}]}`))
	if err != nil || len(wrapped) != 1 || wrapped[0].Difficulty == nil || *wrapped[0].Difficulty != 27 {
		t.Fatalf("wrapped metrics = %+v, err = %v", wrapped, err)
	}
	direct, err := decodeYepKeywordMetrics([]byte(`[{"keyword":"seo api","searchVolume":480}]`))
	if err != nil || len(direct) != 1 || direct[0].effectiveVolume() == nil || *direct[0].effectiveVolume() != 480 {
		t.Fatalf("direct metrics = %+v, err = %v", direct, err)
	}
}

func TestNormalizeProviderKeywordIdea_UsesUniformShape(t *testing.T) {
	idea := normalizeProviderKeywordIdea(map[string]any{
		"keyword_data": map[string]any{
			"keyword":            "SEO API",
			"keyword_info":       map[string]any{"search_volume": float64(390), "cpc": float64(3.5)},
			"keyword_properties": map[string]any{"keyword_difficulty": float64(27)},
			"search_intent_info": map[string]any{"main_intent": "commercial"},
		},
	}, "api tools")
	if idea == nil || idea["keyword"] != "seo api" || idea["source_keyword"] != "api tools" || idea["difficulty"] != float64(27) {
		t.Fatalf("normalized idea = %+v", idea)
	}
}

func TestYepAPIRefreshKeyword_PersistsUniformMetricsAndHistory(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql", "migrations/004_search_entities.sql", "migrations/005_search_engine_keyword_backfill.sql")
	locID := insertProviderTestLocation(t, db, "yepapi", "google", 2724, "Spain", "ES", "es")
	res, err := db.Exec(`INSERT INTO keywords
		(project_id, search_engine, text, location_id, country_iso, language_iso)
		VALUES ('project-1', 'google', 'seo api', ?, 'ES', 'es')`, locID)
	if err != nil {
		t.Fatal(err)
	}
	keywordID, _ := res.LastInsertId()
	stub := &yepPlatformStub{responses: map[string]json.RawMessage{
		"seo_keywords": json.RawMessage(`{
			"ok": true,
			"data": {"keywords": [{
				"keyword": "seo api", "volume": 390, "difficulty": 27, "cpc": 3.5,
				"intent": "commercial", "serpFeatures": ["video"],
				"trend": [{"month": "2026-07", "volume": 320}]
			}]}
		}`),
	}}
	ctx := sdk.NewAppCtxForTest(nil, db, nil, stub, nil)
	loc, err := getLocation(db, locID)
	if err != nil {
		t.Fatal(err)
	}
	keyword, err := getKeyword(db, "project-1", keywordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&yepAPIProvider{connID: 42}).RefreshKeyword(ctx, keyword, loc); err != nil {
		t.Fatalf("RefreshKeyword returned error: %v", err)
	}
	metrics, err := latestKeywordMetrics(db, keywordID, "yepapi")
	if err != nil {
		t.Fatal(err)
	}
	if metrics == nil || metrics.Volume == nil || *metrics.Volume != 390 || metrics.Difficulty == nil || *metrics.Difficulty != 27 {
		t.Fatalf("persisted metrics = %+v", metrics)
	}
	var historyVolume int64
	if err := db.QueryRow(`SELECT volume FROM keyword_volume_history
		WHERE keyword_id = ? AND provider = 'yepapi' AND year = 2026 AND month = 7`, keywordID).Scan(&historyVolume); err != nil {
		t.Fatal(err)
	}
	if historyVolume != 320 {
		t.Fatalf("history volume = %d, want 320", historyVolume)
	}
}

func TestYepAPISERPResponse_NormalizesGoogleAndYouTube(t *testing.T) {
	data := []byte(`{"query":"seo","results":[{"position":2,"type":"organic","title":"Guide","url":"https://example.com/guide"}]}`)
	items, err := decodeSERPItems(data)
	if err != nil || len(items) != 1 {
		t.Fatalf("decodeSERPItems = %+v, err = %v", items, err)
	}
	google := normalizeSERPItem("google", items[0])
	if google.Rank == nil || *google.Rank != 2 || google.EntityType != "page" {
		t.Fatalf("google item = %+v", google)
	}
	youtube := normalizeSERPItem("youtube", map[string]any{
		"position": float64(1), "type": "youtubeSearch", "title": "SEO Video",
		"url": "https://www.youtube.com/watch?v=abc123",
	})
	if youtube.ResultType != "video" || youtube.Identifier != "abc123" {
		t.Fatalf("youtube item = %+v", youtube)
	}
}

func TestSyncYepAPILocations_SeedsBothSearchEngines(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")
	ctx := sdk.NewAppCtxForTest(nil, db, nil, &yepPlatformStub{}, nil)
	out, err := (&yepAPIProvider{connID: 42}).SyncLocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["provider"] != "yepapi" {
		t.Fatalf("sync result = %+v", out)
	}
	for _, engine := range []string{"google", "youtube"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM seo_locations
			WHERE provider = 'yepapi' AND search_engine = ? AND country_iso = 'ES' AND language_code = 'es'`, engine).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s Spain/es locations = %d, want 1", engine, count)
		}
	}
}

func TestPersistYepRankedKeywords_UsesYepProvider(t *testing.T) {
	db := newSEOTestDB(t,
		"migrations/001_init.sql",
		"migrations/003_rankings_daily_history.sql",
		"migrations/004_search_entities.sql",
		"migrations/005_search_engine_keyword_backfill.sql",
		"migrations/006_serp_consistency_and_retention.sql",
	)
	locID := insertProviderTestLocation(t, db, "yepapi", "google", 2840, "United States", "US", "en")
	domainID, err := upsertDomainRecord(db, "project-1", "example.com", "Example", locID)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := getLocation(db, locID)
	position, volume, difficulty := int64(3), int64(1000), int64(22)
	summary, err := persistYepRankedKeywords(db, &Domain{ID: domainID, ProjectID: "project-1", Host: "example.com"}, loc, []yepRankedKeyword{{
		Keyword: "example guide", Position: &position, SearchVolume: &volume,
		Difficulty: &difficulty, URL: "https://example.com/guide", Title: "Guide",
	}}, 1_786_406_400)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RankingRows != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	var provider string
	if err := db.QueryRow(`SELECT provider FROM rankings LIMIT 1`).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if provider != "yepapi" {
		t.Fatalf("ranking provider = %q, want yepapi", provider)
	}
}

func insertProviderTestLocation(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, provider, engine string, code int64, name, country, language string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO seo_locations
		(provider, search_engine, location_code, location_name, country_iso, language_code, language_name)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, provider, engine, code, name, country, language, languageName(language))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
