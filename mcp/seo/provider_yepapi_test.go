package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

type yepPlatformStub struct {
	testkit.BasePlatformClient
	responses   map[string]json.RawMessage
	results     map[string]*sdk.ExecuteResult
	calls       []string
	identity    *sdk.InstallIdentity
	connections map[int64]*sdk.PlatformConnection
}

func (s *yepPlatformStub) ExecuteIntegrationTool(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	s.calls = append(s.calls, tool)
	if result := s.results[tool]; result != nil {
		return result, nil
	}
	data := s.responses[tool]
	if data == nil {
		data = json.RawMessage(`{"ok":true,"data":{}}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}

func TestCachedReadsUseDefaultProviderUnlessAllRequested(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql")
	dataForSEOLocationID := insertProviderTestLocation(t, db, "dataforseo", "google", 2840, "United States", "US", "en")
	yepLocationID := insertProviderTestLocation(t, db, "yepapi", "google", 2840, "United States", "US", "en")
	domainID, err := upsertDomainRecord(db, "project-1", "example.com", "Example", yepLocationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domain_metrics (domain_id, location_id, provider, ts, organic_traffic)
		VALUES (?, ?, 'dataforseo', 200, 100), (?, ?, 'yepapi', 100, 200)`,
		domainID, dataForSEOLocationID, domainID, yepLocationID); err != nil {
		t.Fatal(err)
	}
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
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, stub, nil)

	readTraffic := func(provider string) (*DomainMetrics, error) {
		args := map[string]any{"id": domainID, "_project_id": "project-1"}
		if provider != "" {
			args["provider"] = provider
		}
		out, err := (&App{}).toolDomainsGet(ctx, args)
		if err != nil {
			return nil, err
		}
		return out.(map[string]any)["metrics"].(*DomainMetrics), nil
	}
	metrics, err := readTraffic("")
	if err != nil || metrics.Provider != "yepapi" || metrics.OrganicTraffic == nil || *metrics.OrganicTraffic != 200 {
		t.Fatalf("default metrics = %+v, err = %v", metrics, err)
	}
	metrics, err = readTraffic("all")
	if err != nil || metrics.Provider != "dataforseo" || metrics.OrganicTraffic == nil || *metrics.OrganicTraffic != 100 {
		t.Fatalf("all-provider metrics = %+v, err = %v", metrics, err)
	}
}

func TestKeywordsAddInfersSearchEngineFromLocation(t *testing.T) {
	db := newSEOTestDB(t, "migrations/001_init.sql", "migrations/004_search_entities.sql", "migrations/005_search_engine_keyword_backfill.sql")
	locID := insertProviderTestLocation(t, db, "yepapi", "youtube", 2840, "United States", "US", "en")
	ctx := sdk.NewAppCtxForTest(nil, db, nil, &yepPlatformStub{}, nil)
	out, err := (&App{}).toolKeywordsAdd(ctx, map[string]any{
		"text": "hypnosis trigger", "location_id": locID, "_project_id": "project-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if keyword := out.(*Keyword); keyword.SearchEngine != "youtube" {
		t.Fatalf("search_engine = %q, want youtube", keyword.SearchEngine)
	}

	_, err = (&App{}).toolKeywordsAdd(ctx, map[string]any{
		"text": "mismatched keyword", "location_id": locID, "search_engine": "google", "_project_id": "project-1",
	})
	var validationErr *requestValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("mismatch error = %T %v, want requestValidationError", err, err)
	}
}

func TestWriteJSONOrErrPreservesProviderStatusAndRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSONOrErr(recorder, nil, &providerRequestError{
		Provider: "yepapi", HTTPStatus: http.StatusBadGateway, RetryAfter: 60, Message: "upstream unavailable",
	})
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
}

func TestYepAPIYouTubeSERPFallsBackToVideoSearch(t *testing.T) {
	stub := &yepPlatformStub{results: map[string]*sdk.ExecuteResult{
		"serp_youtube": {
			Success: true, Status: http.StatusOK,
			Data: json.RawMessage(`{"ok":false,"error":{"code":"UPSTREAM_ERROR","message":"origin bad gateway","retry_after":60}}`),
		},
		"youtube_search": {
			Success: true, Status: http.StatusOK,
			Data: json.RawMessage(`{"ok":true,"data":{"data":[{"type":"video","videoId":"abc123","title":"Trigger Guide","channelId":"channel-1","channelTitle":"Creator","publishedAt":"2026-08-01T00:00:00Z"}]}}`),
		},
	}}
	ctx := sdk.NewAppCtxForTest(nil, nil, nil, stub, nil)
	code := int64(2840)
	country := "US"
	response, err := (&yepAPIProvider{connID: 22}).SERPSearch(ctx, "youtube", "hypnosis trigger", &SEOLocation{
		LocationCode: &code, CountryISO: &country, LanguageCode: "en",
	}, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if response.Tool != "youtube_search" || len(stub.calls) != 2 {
		t.Fatalf("response tool/calls = %q/%v", response.Tool, stub.calls)
	}
	items, err := decodeSERPItems(response.ResultRaw)
	if err != nil || len(items) != 1 {
		t.Fatalf("fallback items = %+v, err = %v", items, err)
	}
	item := normalizeSERPItem("youtube", items[0])
	if item.ResultType != "video" || item.Identifier != "abc123" || item.URL != "https://www.youtube.com/watch?v=abc123" || item.ChannelTitle != "Creator" {
		t.Fatalf("normalized fallback item = %+v", item)
	}
	if got := yepRetryAfter(stub.results["serp_youtube"].Data); got != 60 {
		t.Fatalf("retry_after = %d, want 60", got)
	}
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
				"keyword": "seo api", "volume": 390, "difficulty": 0, "cpc": 3.5,
				"intent": "commercial", "serpFeatures": ["video"],
				"trend": [{"month": "2026-07", "volume": 320}]
			}]}
		}`),
		"seo_keywords_difficulty": json.RawMessage(`{
			"ok": true,
			"data": {"keywords": [{"keyword": "seo api", "difficulty": 27}]}
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
	if len(stub.calls) != 2 || stub.calls[1] != "seo_keywords_difficulty" {
		t.Fatalf("provider calls = %v, want conditional difficulty enrichment", stub.calls)
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
