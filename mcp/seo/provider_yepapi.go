package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type yepAPIProvider struct{ connID int64 }

func (p *yepAPIProvider) Slug() string        { return "yepapi" }
func (p *yepAPIProvider) ConnectionID() int64 { return p.connID }

type yepEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func callYepAPI(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any) (dataRaw, envelopeRaw []byte, err error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return nil, nil, fmt.Errorf("yepapi: ExecuteIntegrationTool(%s): %w", tool, err)
	}
	var envelope yepEnvelope
	_ = json.Unmarshal(res.Data, &envelope)
	message := "provider request failed"
	if envelope.Error != nil {
		message = strings.TrimSpace(envelope.Error.Message)
		if message == "" {
			message = envelope.Error.Code
		}
	}
	if !res.Success || res.Status >= 400 {
		return nil, res.Data, &providerRequestError{Provider: "yepapi", Status: res.Status, Message: message}
	}
	if err := json.Unmarshal(res.Data, &envelope); err != nil {
		return nil, res.Data, fmt.Errorf("yepapi: parse %s response: %w", tool, err)
	}
	if !envelope.OK {
		return nil, res.Data, &providerRequestError{Provider: "yepapi", Message: message}
	}
	return envelope.Data, res.Data, nil
}

type yepKeywordMetric struct {
	Keyword      string   `json:"keyword"`
	Volume       *int64   `json:"volume"`
	SearchVolume *int64   `json:"searchVolume"`
	Difficulty   *int64   `json:"difficulty"`
	CPC          *float64 `json:"cpc"`
	Intent       string   `json:"intent"`
	SERPFeatures []string `json:"serpFeatures"`
	Trend        []struct {
		Month  string `json:"month"`
		Volume int64  `json:"volume"`
	} `json:"trend"`
}

func (m yepKeywordMetric) effectiveVolume() *int64 {
	if m.Volume != nil {
		return m.Volume
	}
	return m.SearchVolume
}

func decodeYepKeywordMetrics(dataRaw []byte) ([]yepKeywordMetric, error) {
	var wrapped struct {
		Keywords []yepKeywordMetric `json:"keywords"`
	}
	if err := json.Unmarshal(dataRaw, &wrapped); err == nil && wrapped.Keywords != nil {
		return wrapped.Keywords, nil
	}
	var direct []yepKeywordMetric
	if err := json.Unmarshal(dataRaw, &direct); err != nil {
		return nil, fmt.Errorf("yepapi: parse keyword rows: %w", err)
	}
	return direct, nil
}

func (p *yepAPIProvider) RefreshKeyword(ctx *sdk.AppCtx, k *Keyword, loc *SEOLocation) (any, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("yepapi keyword refresh requires a location with location_code")
	}
	dataRaw, envelopeRaw, err := callYepAPI(ctx, p.connID, "seo_keywords", map[string]any{
		"keywords":      []string{k.Text},
		"location_code": *loc.LocationCode,
		"language":      strings.ToLower(loc.LanguageCode),
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeYepKeywordMetrics(dataRaw)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("yepapi: zero rows for keyword %q", k.Text)
	}
	item := rows[0]
	now := time.Now().Unix()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intentJSON, _ := json.Marshal(nonEmptyStrings([]string{strings.ToLower(item.Intent)}))
	featuresJSON, _ := json.Marshal(item.SERPFeatures)
	res, err := tx.Exec(
		`INSERT INTO keyword_metrics
		   (keyword_id, location_id, provider, ts, volume, difficulty, cpc_usd,
		    intent_json, serp_features_json, raw_json)
		 VALUES (?, ?, 'yepapi', ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, loc.ID, now, item.effectiveVolume(), item.Difficulty, item.CPC,
		string(intentJSON), string(featuresJSON), string(envelopeRaw))
	if err != nil {
		return nil, fmt.Errorf("insert yepapi keyword_metrics: %w", err)
	}
	snapshotID, _ := res.LastInsertId()
	historyRows := 0
	for _, trend := range item.Trend {
		year, month, ok := parseYearMonth(trend.Month)
		if !ok {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO keyword_volume_history
			   (keyword_id, location_id, provider, year, month, volume)
			 VALUES (?, ?, 'yepapi', ?, ?, ?)
			 ON CONFLICT(keyword_id, location_id, provider, year, month)
			 DO UPDATE SET volume = excluded.volume`,
			k.ID, loc.ID, year, month, trend.Volume); err != nil {
			return nil, fmt.Errorf("upsert yepapi volume history (%s): %w", trend.Month, err)
		}
		historyRows++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"keyword_id": k.ID, "location_id": loc.ID, "snapshot_id": snapshotID,
		"provider": "yepapi", "fetched_at": now, "volume": valOr(item.effectiveVolume(), 0),
		"difficulty": valOr(item.Difficulty, 0), "history_rows": historyRows,
	}, nil
}

func parseYearMonth(value string) (int, int, bool) {
	parsed, err := time.Parse("2006-01", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, false
	}
	return parsed.Year(), int(parsed.Month()), true
}

type yepDomainOverview struct {
	Domain           string  `json:"domain"`
	DomainRank       float64 `json:"domainRank"`
	OrganicTraffic   int64   `json:"organicTraffic"`
	OrganicKeywords  int64   `json:"organicKeywords"`
	Backlinks        int64   `json:"backlinks"`
	ReferringDomains int64   `json:"referringDomains"`
}

type yepRankedKeyword struct {
	Keyword      string   `json:"keyword"`
	Position     *int64   `json:"position"`
	SearchVolume *int64   `json:"searchVolume"`
	Volume       *int64   `json:"volume"`
	Difficulty   *int64   `json:"difficulty"`
	CPC          *float64 `json:"cpc"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Traffic      *float64 `json:"traffic"`
}

func decodeYepRankedKeywords(dataRaw []byte) ([]yepRankedKeyword, error) {
	var wrapped struct {
		Keywords []yepRankedKeyword `json:"keywords"`
	}
	if err := json.Unmarshal(dataRaw, &wrapped); err == nil && wrapped.Keywords != nil {
		return wrapped.Keywords, nil
	}
	var direct []yepRankedKeyword
	if err := json.Unmarshal(dataRaw, &direct); err != nil {
		return nil, fmt.Errorf("yepapi: parse domain keywords: %w", err)
	}
	return direct, nil
}

func (p *yepAPIProvider) RefreshDomain(ctx *sdk.AppCtx, d *Domain, loc *SEOLocation) (any, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("yepapi domain refresh requires a location with location_code")
	}
	input := map[string]any{
		"domain": d.Host, "location_code": *loc.LocationCode,
		"language": strings.ToLower(loc.LanguageCode),
	}
	overviewRaw, overviewEnvelope, err := callYepAPI(ctx, p.connID, "seo_domain_overview", input)
	if err != nil {
		return nil, err
	}
	var overview yepDomainOverview
	if err := json.Unmarshal(overviewRaw, &overview); err != nil {
		return nil, fmt.Errorf("yepapi: parse domain overview: %w", err)
	}
	now := time.Now().Unix()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO domain_metrics
		   (domain_id, location_id, provider, ts, country_iso, organic_traffic,
		    organic_keywords, backlinks_count, referring_domains_count, raw_json)
		 VALUES (?, ?, 'yepapi', ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, loc.ID, now, loc.CountryISO, overview.OrganicTraffic, overview.OrganicKeywords,
		overview.Backlinks, overview.ReferringDomains, string(overviewEnvelope))
	if err != nil {
		return nil, fmt.Errorf("insert yepapi domain_metrics: %w", err)
	}
	snapshotID, _ := res.LastInsertId()

	rankedRaw, _, err := callYepAPI(ctx, p.connID, "seo_domain_keywords", map[string]any{
		"domain": d.Host, "location_code": *loc.LocationCode,
		"language": strings.ToLower(loc.LanguageCode), "limit": 100,
	})
	if err != nil {
		return nil, err
	}
	ranked, err := decodeYepRankedKeywords(rankedRaw)
	if err != nil {
		return nil, err
	}
	summary, err := persistYepRankedKeywords(ctx.AppDB(), d, loc, ranked, now)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"domain_id": d.ID, "location_id": loc.ID, "snapshot_id": snapshotID,
		"provider": "yepapi", "fetched_at": now,
		"organic_kw": overview.OrganicKeywords, "organic_etv": overview.OrganicTraffic,
		"ranking_rows": summary.RankingRows, "keyword_rows": summary.KeywordRows,
		"page_rows": summary.PageRows, "rankings_note": summary.Note,
	}, nil
}

func persistYepRankedKeywords(db *sql.DB, d *Domain, loc *SEOLocation, rows []yepRankedKeyword, now int64) (rankedKeywordRefreshSummary, error) {
	observedDate := time.Unix(now, 0).UTC().Format("2006-01-02")
	tx, err := db.Begin()
	if err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	defer tx.Rollback()
	if err := replaceRankingObservation(tx, d.ID, loc.ID, "yepapi", "desktop", observedDate, now); err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	summary := rankedKeywordRefreshSummary{}
	for _, item := range rows {
		keywordText := normaliseKeyword(item.Keyword)
		rankURL := strings.TrimSpace(item.URL)
		if keywordText == "" || rankURL == "" {
			continue
		}
		volume := item.SearchVolume
		if volume == nil {
			volume = item.Volume
		}
		keywordID, created, err := upsertRankedKeyword(tx, "yepapi", "google", d.ProjectID, keywordText, loc, volume, item.Difficulty, item.CPC, item)
		if err != nil {
			return rankedKeywordRefreshSummary{}, err
		}
		if created {
			summary.KeywordRows++
		}
		if created, err := upsertPageForRankURL(tx, d.ID, rankURL, item.Title); err != nil {
			return rankedKeywordRefreshSummary{}, err
		} else if created {
			summary.PageRows++
		}
		raw, _ := json.Marshal(item)
		if _, err := tx.Exec(
			`INSERT INTO rankings
			   (domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url, device, serp_features_json)
			 VALUES (?, ?, ?, 'yepapi', ?, ?, ?, ?, 'desktop', ?)
			 ON CONFLICT(domain_id, keyword_id, location_id, provider, rank_url, device, observed_date)
			 DO UPDATE SET ts = excluded.ts, rank = excluded.rank, serp_features_json = excluded.serp_features_json`,
			d.ID, keywordID, loc.ID, now, observedDate, item.Position, rankURL, string(raw)); err != nil {
			return rankedKeywordRefreshSummary{}, fmt.Errorf("upsert yepapi ranking for %q: %w", keywordText, err)
		}
		summary.RankingRows++
	}
	if _, err := tx.Exec(
		`UPDATE ranking_observations SET result_count = ?, ts = ?
		  WHERE domain_id = ? AND location_id = ? AND provider = 'yepapi'
		    AND device = 'desktop' AND observed_date = ?`,
		summary.RankingRows, now, d.ID, loc.ID, observedDate); err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	if len(rows) == 0 {
		summary.Note = "seo_domain_keywords returned zero items"
	}
	if err := tx.Commit(); err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	return summary, nil
}

type yepBacklink struct {
	From            string   `json:"from"`
	SourceURL       string   `json:"sourceUrl"`
	To              string   `json:"to"`
	Anchor          string   `json:"anchor"`
	AnchorText      string   `json:"anchorText"`
	Dofollow        bool     `json:"dofollow"`
	IsDofollow      bool     `json:"isDofollow"`
	DomainAuthority *float64 `json:"domainAuthority"`
	FirstSeen       string   `json:"firstSeen"`
	LastSeen        string   `json:"lastSeen"`
	Status          string   `json:"status"`
}

func decodeYepBacklinks(dataRaw []byte) ([]yepBacklink, error) {
	var wrapped struct {
		Backlinks []yepBacklink `json:"backlinks"`
	}
	if err := json.Unmarshal(dataRaw, &wrapped); err == nil && wrapped.Backlinks != nil {
		return wrapped.Backlinks, nil
	}
	var direct []yepBacklink
	if err := json.Unmarshal(dataRaw, &direct); err != nil {
		return nil, fmt.Errorf("yepapi: parse backlinks: %w", err)
	}
	return direct, nil
}

func (p *yepAPIProvider) RefreshBacklinks(ctx *sdk.AppCtx, d *Domain) (any, error) {
	dataRaw, _, err := callYepAPI(ctx, p.connID, "seo_backlinks_list", map[string]any{"target": d.Host, "limit": 100})
	if err != nil {
		return nil, err
	}
	rows, err := decodeYepBacklinks(dataRaw)
	if err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	upserts := 0
	for _, item := range rows {
		sourceURL := strings.TrimSpace(item.From)
		if sourceURL == "" {
			sourceURL = strings.TrimSpace(item.SourceURL)
		}
		if sourceURL == "" {
			continue
		}
		destURL := strings.TrimSpace(item.To)
		if destURL == "" {
			destURL = "https://" + d.Host
		}
		anchor := item.Anchor
		if anchor == "" {
			anchor = item.AnchorText
		}
		dofollow := item.Dofollow || item.IsDofollow
		var authority any
		if item.DomainAuthority != nil {
			authority = int64(*item.DomainAuthority)
		}
		raw, _ := json.Marshal(item)
		_, err := tx.Exec(
			`INSERT INTO backlinks
			   (domain_id, provider, source_url, dest_url, anchor, is_dofollow, is_nofollow,
			    source_authority, first_seen, last_seen, is_lost, raw_json)
			 VALUES (?, 'yepapi', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(domain_id, provider, source_url, dest_url, anchor)
			 DO UPDATE SET last_seen = excluded.last_seen, is_lost = excluded.is_lost, raw_json = excluded.raw_json`,
			d.ID, sourceURL, destURL, anchor, boolToInt(dofollow), boolToInt(!dofollow),
			authority, parseDfsTime(item.FirstSeen), parseDfsTime(item.LastSeen),
			boolToInt(strings.EqualFold(item.Status, "lost")), string(raw))
		if err != nil {
			return nil, fmt.Errorf("upsert yepapi backlink: %w", err)
		}
		upserts++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"domain_id": d.ID, "provider": "yepapi", "rows_upserted": upserts, "capped_at": 100}, nil
}

func (p *yepAPIProvider) SERPSearch(ctx *sdk.AppCtx, engine, keyword string, loc *SEOLocation, depth int, _ string) (*providerSERPResponse, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("yepapi SERP search requires a location with location_code")
	}
	tool := "serp_google"
	if engine == "youtube" {
		tool = "serp_youtube"
	}
	dataRaw, envelopeRaw, err := callYepAPI(ctx, p.connID, tool, map[string]any{
		"query": keyword, "depth": depth, "location_code": *loc.LocationCode,
		"language": strings.ToLower(loc.LanguageCode),
	})
	if err != nil {
		return nil, err
	}
	return &providerSERPResponse{Tool: tool, ResultRaw: dataRaw, Raw: envelopeRaw}, nil
}

func (p *yepAPIProvider) KeywordIdeas(ctx *sdk.AppCtx, seeds []string, loc *SEOLocation, limit int) (*providerKeywordIdeasResponse, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("yepapi keyword ideas require a location with location_code")
	}
	items := make([]map[string]any, 0, limit)
	seen := map[string]bool{}
	rawResponses := make([]json.RawMessage, 0, len(seeds))
	for _, seed := range seeds {
		dataRaw, envelopeRaw, err := callYepAPI(ctx, p.connID, "seo_keywords_ideas", map[string]any{
			"keyword": seed, "limit": limit, "location_code": *loc.LocationCode,
			"language": strings.ToLower(loc.LanguageCode),
		})
		if err != nil {
			return nil, err
		}
		rawResponses = append(rawResponses, json.RawMessage(envelopeRaw))
		rows, err := decodeMapItems(dataRaw, "keywords")
		if err != nil {
			return nil, err
		}
		for _, item := range rows {
			keyword := normaliseKeyword(firstString(item, "keyword"))
			if keyword == "" || seen[keyword] {
				continue
			}
			seen[keyword] = true
			item["keyword"] = keyword
			idea := normalizeProviderKeywordIdea(item, seed)
			if idea == nil {
				continue
			}
			items = append(items, idea)
			if len(items) >= limit {
				break
			}
		}
		if len(items) >= limit {
			break
		}
	}
	raw, _ := json.Marshal(rawResponses)
	return &providerKeywordIdeasResponse{Tool: "seo_keywords_ideas", Items: items, Raw: raw}, nil
}

func decodeMapItems(dataRaw []byte, key string) ([]map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(dataRaw, &obj); err == nil {
		if rawItems, ok := obj[key].([]any); ok {
			return mapsFromAny(rawItems), nil
		}
	}
	var direct []any
	if err := json.Unmarshal(dataRaw, &direct); err != nil {
		return nil, fmt.Errorf("yepapi: parse %s items: %w", key, err)
	}
	return mapsFromAny(direct), nil
}

func mapsFromAny(values []any) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

type yepLocale struct {
	Code int64
	Name string
	ISO  string
	Lang string
}

var yepCoreLocales = []yepLocale{
	{2840, "United States", "US", "en"}, {2826, "United Kingdom", "GB", "en"},
	{2724, "Spain", "ES", "es"}, {2276, "Germany", "DE", "de"}, {2250, "France", "FR", "fr"},
	{2380, "Italy", "IT", "it"}, {2528, "Netherlands", "NL", "nl"}, {2056, "Belgium", "BE", "nl"},
	{2620, "Portugal", "PT", "pt"}, {2616, "Poland", "PL", "pl"}, {2752, "Sweden", "SE", "sv"},
	{2578, "Norway", "NO", "no"}, {2208, "Denmark", "DK", "da"}, {2246, "Finland", "FI", "fi"},
	{2040, "Austria", "AT", "de"}, {2756, "Switzerland", "CH", "de"}, {2372, "Ireland", "IE", "en"},
	{2203, "Czechia", "CZ", "cs"}, {2642, "Romania", "RO", "ro"}, {2100, "Bulgaria", "BG", "bg"},
	{2300, "Greece", "GR", "el"}, {2348, "Hungary", "HU", "hu"}, {2191, "Croatia", "HR", "hr"},
	{2233, "Estonia", "EE", "et"}, {2428, "Latvia", "LV", "lv"}, {2440, "Lithuania", "LT", "lt"},
	{2703, "Slovakia", "SK", "sk"}, {2705, "Slovenia", "SI", "sl"}, {2196, "Cyprus", "CY", "el"},
	{2470, "Malta", "MT", "en"}, {2499, "Montenegro", "ME", "sr"}, {2688, "Serbia", "RS", "sr"},
	{2807, "North Macedonia", "MK", "mk"}, {2070, "Bosnia and Herzegovina", "BA", "bs"},
	{2008, "Albania", "AL", "sq"}, {2352, "Iceland", "IS", "is"}, {2804, "Ukraine", "UA", "uk"},
	{2792, "Turkiye", "TR", "tr"}, {2124, "Canada", "CA", "en"}, {2036, "Australia", "AU", "en"},
	{2554, "New Zealand", "NZ", "en"}, {2356, "India", "IN", "en"}, {2702, "Singapore", "SG", "en"},
	{2392, "Japan", "JP", "ja"}, {2410, "South Korea", "KR", "ko"}, {2076, "Brazil", "BR", "pt"},
	{2484, "Mexico", "MX", "es"}, {2032, "Argentina", "AR", "es"}, {2170, "Colombia", "CO", "es"},
	{2152, "Chile", "CL", "es"}, {2604, "Peru", "PE", "es"}, {2704, "Vietnam", "VN", "vi"},
	{2764, "Thailand", "TH", "th"}, {2360, "Indonesia", "ID", "id"}, {2458, "Malaysia", "MY", "ms"},
	{2608, "Philippines", "PH", "en"}, {2710, "South Africa", "ZA", "en"}, {2566, "Nigeria", "NG", "en"},
	{2404, "Kenya", "KE", "en"}, {2784, "United Arab Emirates", "AE", "ar"}, {2682, "Saudi Arabia", "SA", "ar"},
	{2376, "Israel", "IL", "he"}, {2818, "Egypt", "EG", "ar"}, {2504, "Morocco", "MA", "ar"},
}

func (p *yepAPIProvider) SyncLocations(ctx *sdk.AppCtx) (any, error) {
	now := time.Now().Unix()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// YepAPI and DataForSEO use the same Google geo-target constants. Reuse
	// any richer catalog already cached by another provider, then guarantee a
	// useful standalone catalog for fresh YepAPI-only installs.
	if _, err := tx.Exec(
		`INSERT INTO seo_locations
		   (provider, search_engine, location_code, location_name, country_iso,
		    language_code, language_name, is_active, raw_json, synced_at)
		 SELECT 'yepapi', search_engine, location_code, location_name, country_iso,
		        language_code, language_name, 1, '{"source":"shared_google_geo_catalog"}', ?
		   FROM seo_locations
		  WHERE provider <> 'yepapi' AND search_engine IN ('google','youtube')
		    AND location_code IS NOT NULL
		 ON CONFLICT(provider, search_engine, location_code, language_code)
		 DO UPDATE SET location_name = excluded.location_name, country_iso = excluded.country_iso,
		               language_name = excluded.language_name, is_active = 1, synced_at = excluded.synced_at`, now); err != nil {
		return nil, fmt.Errorf("copy shared YepAPI locations: %w", err)
	}
	upserts := 0
	for _, locale := range yepCoreLocales {
		languages := []string{locale.Lang}
		if locale.Lang != "en" {
			languages = append(languages, "en")
		}
		for _, engine := range []string{"google", "youtube"} {
			for _, language := range languages {
				if _, err := tx.Exec(
					`INSERT INTO seo_locations
					   (provider, search_engine, location_code, location_name, country_iso,
					    language_code, language_name, is_active, raw_json, synced_at)
					 VALUES ('yepapi', ?, ?, ?, ?, ?, ?, 1, '{"source":"yepapi_static_catalog"}', ?)
					 ON CONFLICT(provider, search_engine, location_code, language_code)
					 DO UPDATE SET location_name = excluded.location_name, country_iso = excluded.country_iso,
					               language_name = excluded.language_name, is_active = 1, synced_at = excluded.synced_at`,
					engine, locale.Code, locale.Name, locale.ISO, language, languageName(language), now); err != nil {
					return nil, fmt.Errorf("upsert YepAPI location %s/%s: %w", locale.ISO, language, err)
				}
				upserts++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var total int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM seo_locations WHERE provider = 'yepapi' AND is_active = 1`).Scan(&total)
	return map[string]any{
		"provider": "yepapi", "rows_upserted": upserts, "rows_available": total,
		"synced_at": now, "engines": map[string]any{"google": map[string]any{"ok": true}, "youtube": map[string]any{"ok": true}},
	}, nil
}

func languageName(code string) string {
	names := map[string]string{
		"ar": "Arabic", "bg": "Bulgarian", "bs": "Bosnian", "cs": "Czech", "da": "Danish",
		"de": "German", "el": "Greek", "en": "English", "es": "Spanish", "et": "Estonian",
		"fi": "Finnish", "fr": "French", "he": "Hebrew", "hr": "Croatian", "hu": "Hungarian",
		"id": "Indonesian", "is": "Icelandic", "it": "Italian", "ja": "Japanese", "ko": "Korean",
		"lt": "Lithuanian", "lv": "Latvian", "mk": "Macedonian", "ms": "Malay", "nl": "Dutch",
		"no": "Norwegian", "pl": "Polish", "pt": "Portuguese", "ro": "Romanian", "sk": "Slovak",
		"sl": "Slovenian", "sq": "Albanian", "sr": "Serbian", "sv": "Swedish", "th": "Thai",
		"tr": "Turkish", "uk": "Ukrainian", "vi": "Vietnamese",
	}
	if name := names[code]; name != "" {
		return name
	}
	return strings.ToUpper(code)
}
