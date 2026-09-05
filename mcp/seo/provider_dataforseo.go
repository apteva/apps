package main

// DataForSEO-specific call wrappers + response normalisers.
//
// The integration runner does the HTTP plumbing (Basic auth from
// the bound connection's `login`/`password` fields, base_url
// resolution, request body assembly per the input_schema). Apps
// only call PlatformAPI().ExecuteIntegrationTool and parse the
// `data` payload — which is the raw DataForSEO response envelope.
//
// DataForSEO's response envelope:
//
//   {
//     "version": "...",
//     "status_code": 20000,
//     "tasks": [{
//       "id": "...",
//       "status_code": 20000,        // 20000 = task ok
//       "result": [{ <actual data> }]
//     }]
//   }
//
// Each tool below: build args → ExecuteIntegrationTool → unwrap to
// tasks[0].result[0] (or [...] for list endpoints) → translate to
// our row shape → write to DB → return summary.
//
// The whole tasks[0] payload is preserved on each snapshot row's
// raw_json so post-hoc analyses (or future typed columns) don't
// require re-spending API calls.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// dfsEnvelope mirrors the top-level shape of every /v3/.../live
// response we use. Endpoint-specific result shapes get decoded
// against tasks[0].result via json.RawMessage indirection.
type dfsEnvelope struct {
	StatusCode int       `json:"status_code"`
	StatusMsg  string    `json:"status_message"`
	Tasks      []dfsTask `json:"tasks"`
}

type dfsTask struct {
	ID         string            `json:"id"`
	StatusCode int               `json:"status_code"`
	StatusMsg  string            `json:"status_message"`
	Cost       float64           `json:"cost"`
	Result     []json.RawMessage `json:"result"`
}

func classifyProviderError(code int, message string) error {
	lower := strings.ToLower(message)
	switch {
	case code == 40202, code == 50301,
		strings.Contains(lower, "rate limit"), strings.Contains(lower, "rates limit"),
		strings.Contains(lower, "too many requests"):
		return &providerRequestError{Provider: "dataforseo", HTTPStatus: http.StatusTooManyRequests, ProviderCode: code, Message: message}
	case code == 40200, strings.Contains(lower, "payment required"),
		strings.Contains(lower, "insufficient") && (strings.Contains(lower, "credit") || strings.Contains(lower, "fund")),
		strings.Contains(lower, "balance") && strings.Contains(lower, "low"):
		return &providerRequestError{Provider: "dataforseo", HTTPStatus: http.StatusPaymentRequired, ProviderCode: code, Message: message}
	default:
		return fmt.Errorf("dataforseo: task status %d: %s", code, message)
	}
}

// callDfs wraps ExecuteIntegrationTool + envelope sanity-check. It
// returns the first task's first result row as raw JSON, plus the
// whole task[0] payload for raw_json archival.
func callDfs(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any) (resultRow []byte, taskRaw []byte, err error) {
	rows, taskRaw, err := callDfsRows(ctx, connID, tool, input)
	if err != nil || len(rows) == 0 {
		return nil, taskRaw, err
	}
	return rows[0], taskRaw, nil
}

// callDfsRows preserves every task result row. Single-keyword and overview
// callers use callDfs; bulk keyword metrics need the complete result array.
func callDfsRows(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any) (resultRows []json.RawMessage, taskRaw []byte, err error) {
	return callDfsRowsWithIntegrationInput(ctx, connID, tool, map[string]any{
		"tasks": []map[string]any{input},
	})
}

func callDfsRowsWithIntegrationInput(ctx *sdk.AppCtx, connID int64, tool string, integrationInput map[string]any) (resultRows []json.RawMessage, taskRaw []byte, err error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, integrationInput)
	if err != nil {
		return nil, nil, fmt.Errorf("dataforseo: ExecuteIntegrationTool(%s): %w", tool, err)
	}
	var env dfsEnvelope
	parseErr := json.Unmarshal(res.Data, &env)
	if parseErr == nil && env.StatusCode != 0 && env.StatusCode != 20000 {
		return nil, nil, classifyProviderError(env.StatusCode, env.StatusMsg)
	}
	if parseErr == nil && len(env.Tasks) > 0 && env.Tasks[0].StatusCode != 20000 {
		t := env.Tasks[0]
		return nil, nil, classifyProviderError(t.StatusCode, t.StatusMsg)
	}
	if !res.Success || res.Status >= 400 {
		status := res.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
			return nil, nil, &providerRequestError{Provider: "dataforseo", HTTPStatus: status, Message: fmt.Sprintf("%s returned HTTP %d", tool, status)}
		}
		return nil, nil, fmt.Errorf("dataforseo: %s returned HTTP %d", tool, status)
	}
	if parseErr != nil {
		return nil, nil, fmt.Errorf("dataforseo: parse envelope: %w", parseErr)
	}
	if len(env.Tasks) == 0 {
		return nil, nil, fmt.Errorf("dataforseo: %s returned zero tasks", tool)
	}
	t := env.Tasks[0]
	taskRaw, _ = json.Marshal(t)
	return t.Result, taskRaw, nil
}

func retryProviderCall(call func() ([]json.RawMessage, []byte, error)) ([]json.RawMessage, []byte, error) {
	delays := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}
	for attempt := 0; ; attempt++ {
		rows, raw, err := call()
		if err == nil {
			return rows, raw, nil
		}
		var providerErr *providerRequestError
		if !errors.As(err, &providerErr) || providerErr.HTTPStatus != http.StatusTooManyRequests || attempt >= len(delays) {
			return nil, nil, err
		}
		time.Sleep(delays[attempt])
	}
}

// ─── Domain rank overview ────────────────────────────────────────

// dfsDomainRankResult mirrors dataforseo_labs/google/domain_rank_overview/live.
// Only the columns we surface are typed; `raw_json` carries the rest.
type dfsDomainRankResult struct {
	Items []struct {
		SETopType    string `json:"se_type"`
		LocationCode int    `json:"location_code"`
		Metrics      struct {
			Organic struct {
				Count                int64   `json:"count"`
				ETV                  float64 `json:"etv"`
				EstimatedPaidTraffic float64 `json:"estimated_paid_traffic_cost"`
			} `json:"organic"`
			Paid struct {
				Count int64   `json:"count"`
				ETV   float64 `json:"etv"`
			} `json:"paid"`
		} `json:"metrics"`
		// DataForSEO's domain_rank_overview doesn't ship DR/DA itself —
		// those come from the dataforseo_labs/.../bulk_domain_ranks
		// endpoint. authority_score stays null until v0.2.1 wires that.
	} `json:"items"`
}

func refreshDomainViaDataForSEO(ctx *sdk.AppCtx, connID int64, d *Domain, loc *SEOLocation) (any, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("dataforseo refresh requires a location with location_code")
	}
	rowRaw, taskRaw, err := callDfs(ctx, connID, "domain_rank_overview", domainRankOverviewInput(d, loc))
	if err != nil {
		return nil, err
	}
	if rowRaw == nil {
		return nil, fmt.Errorf("dataforseo returned no rows for %s", d.Host)
	}
	var parsed dfsDomainRankResult
	if err := json.Unmarshal(rowRaw, &parsed); err != nil {
		return nil, fmt.Errorf("parse domain_rank_overview: %w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, fmt.Errorf("domain_rank_overview: zero items for %s", d.Host)
	}
	m := parsed.Items[0].Metrics
	now := time.Now().Unix()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO domain_metrics
		    (domain_id, location_id, provider, ts, country_iso, organic_traffic,
		     organic_keywords, paid_traffic, paid_keywords, raw_json)
		 VALUES (?, ?, 'dataforseo', ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, loc.ID, now, loc.CountryISO,
		int64(m.Organic.ETV), m.Organic.Count,
		int64(m.Paid.ETV), m.Paid.Count,
		string(taskRaw),
	)
	if err != nil {
		return nil, fmt.Errorf("insert domain_metrics: %w", err)
	}
	id, _ := res.LastInsertId()
	rankingSummary, err := refreshRankedKeywordsViaDataForSEO(ctx, connID, d, loc)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"domain_id":     d.ID,
		"location_id":   loc.ID,
		"snapshot_id":   id,
		"provider":      "dataforseo",
		"fetched_at":    now,
		"organic_kw":    m.Organic.Count,
		"organic_etv":   int64(m.Organic.ETV),
		"paid_kw":       m.Paid.Count,
		"paid_etv":      int64(m.Paid.ETV),
		"ranking_rows":  rankingSummary.RankingRows,
		"keyword_rows":  rankingSummary.KeywordRows,
		"page_rows":     rankingSummary.PageRows,
		"rankings_note": rankingSummary.Note,
	}, nil
}

func domainRankOverviewInput(d *Domain, loc *SEOLocation) map[string]any {
	input := map[string]any{"target": d.Host}
	if loc != nil && loc.LocationCode != nil {
		input["location_code"] = *loc.LocationCode
	}
	return input
}

type dfsRankedKeywordsResult struct {
	Items []dfsRankedKeywordItem `json:"items"`
}

type dfsRankedKeywordItem struct {
	KeywordData struct {
		Keyword           string   `json:"keyword"`
		SearchVolume      *int64   `json:"search_volume"`
		Competition       *float64 `json:"competition"`
		CompetitionIndex  *int64   `json:"competition_index"`
		CPC               *float64 `json:"cpc"`
		KeywordDifficulty *int64   `json:"keyword_difficulty"`
	} `json:"keyword_data"`
	RankedSERPElement struct {
		SEType       string   `json:"se_type"`
		RankGroup    *int64   `json:"rank_group"`
		RankAbsolute *int64   `json:"rank_absolute"`
		Position     *int64   `json:"position"`
		ETV          *float64 `json:"etv"`
		SerpItem     struct {
			Type  string `json:"type"`
			Rank  *int64 `json:"rank_group"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"serp_item"`
	} `json:"ranked_serp_element"`
}

type rankedKeywordRefreshSummary struct {
	RankingRows int    `json:"ranking_rows"`
	KeywordRows int    `json:"keyword_rows"`
	PageRows    int    `json:"page_rows"`
	Note        string `json:"note,omitempty"`
}

func refreshRankedKeywordsViaDataForSEO(ctx *sdk.AppCtx, connID int64, d *Domain, loc *SEOLocation) (rankedKeywordRefreshSummary, error) {
	if loc == nil || loc.LocationCode == nil {
		return rankedKeywordRefreshSummary{}, fmt.Errorf("dataforseo ranked keywords refresh requires a location with location_code")
	}
	rowRaw, _, err := callDfs(ctx, connID, "ranked_keywords", map[string]any{
		"target":        d.Host,
		"location_code": *loc.LocationCode,
		"language_code": strings.ToLower(loc.LanguageCode),
		"limit":         100,
		"offset":        0,
	})
	if err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	var parsed dfsRankedKeywordsResult
	if rowRaw != nil {
		if err := json.Unmarshal(rowRaw, &parsed); err != nil {
			return rankedKeywordRefreshSummary{}, fmt.Errorf("parse ranked_keywords: %w", err)
		}
	}
	if rowRaw == nil {
		parsed.Items = nil
	}
	now := time.Now().Unix()
	observedDate := time.Unix(now, 0).UTC().Format("2006-01-02")
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	defer tx.Rollback()

	summary := rankedKeywordRefreshSummary{}
	if err := replaceRankingObservation(tx, d.ID, loc.ID, "dataforseo", "desktop", observedDate, now); err != nil {
		return rankedKeywordRefreshSummary{}, fmt.Errorf("replace current ranking observation: %w", err)
	}
	for _, item := range parsed.Items {
		keywordText := normaliseKeyword(item.KeywordData.Keyword)
		rankURL := strings.TrimSpace(item.RankedSERPElement.SerpItem.URL)
		if keywordText == "" || rankURL == "" {
			continue
		}
		keywordID, createdKeyword, err := upsertRankedKeyword(tx, "dataforseo", "google", d.ProjectID, keywordText, loc, item.KeywordData.SearchVolume, item.KeywordData.KeywordDifficulty, item.KeywordData.CPC, item)
		if err != nil {
			return rankedKeywordRefreshSummary{}, err
		}
		if createdKeyword {
			summary.KeywordRows++
		}
		if createdPage, err := upsertPageForRankURL(tx, d.ID, rankURL, item.RankedSERPElement.SerpItem.Title); err != nil {
			return rankedKeywordRefreshSummary{}, err
		} else if createdPage {
			summary.PageRows++
		}
		rank := firstInt64Ptr(item.RankedSERPElement.RankGroup, item.RankedSERPElement.Position, item.RankedSERPElement.RankAbsolute, item.RankedSERPElement.SerpItem.Rank)
		raw, _ := json.Marshal(item)
		if _, err := tx.Exec(
			`INSERT INTO rankings
			   (domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url, device, serp_features_json)
			 VALUES (?, ?, ?, 'dataforseo', ?, ?, ?, ?, 'desktop', ?)
			 ON CONFLICT(domain_id, keyword_id, location_id, provider, rank_url, device, observed_date)
			 DO UPDATE SET
			    ts = excluded.ts,
			    rank = excluded.rank,
			    serp_features_json = excluded.serp_features_json`,
			d.ID, keywordID, loc.ID, now, observedDate, rank, rankURL, string(raw),
		); err != nil {
			return rankedKeywordRefreshSummary{}, fmt.Errorf("upsert ranking for %q: %w", keywordText, err)
		}
		summary.RankingRows++
	}
	if _, err := tx.Exec(
		`UPDATE ranking_observations
		    SET result_count = ?, ts = ?
		  WHERE domain_id = ? AND location_id = ? AND provider = 'dataforseo'
		    AND device = 'desktop' AND observed_date = ?`,
		summary.RankingRows, now, d.ID, loc.ID, observedDate); err != nil {
		return rankedKeywordRefreshSummary{}, fmt.Errorf("finalize ranking observation: %w", err)
	}
	if len(parsed.Items) == 0 {
		summary.Note = "ranked_keywords returned zero items"
	}
	if err := tx.Commit(); err != nil {
		return rankedKeywordRefreshSummary{}, err
	}
	return summary, nil
}

func replaceRankingObservation(tx *sql.Tx, domainID, locationID int64, provider, device, observedDate string, ts int64) error {
	if _, err := tx.Exec(
		`DELETE FROM rankings
		  WHERE domain_id = ? AND location_id = ? AND provider = ?
		    AND device = ? AND observed_date = ?`,
		domainID, locationID, provider, device, observedDate); err != nil {
		return err
	}
	_, err := tx.Exec(
		`INSERT INTO ranking_observations
		    (domain_id, location_id, provider, device, ts, observed_date, result_count)
		 VALUES (?, ?, ?, ?, ?, ?, 0)
		 ON CONFLICT(domain_id, location_id, provider, device, observed_date)
		 DO UPDATE SET ts = excluded.ts, result_count = 0`,
		domainID, locationID, provider, device, ts, observedDate)
	return err
}

func upsertRankedKeyword(tx *sql.Tx, provider, searchEngine, projectID, text string, loc *SEOLocation, volume *int64, difficulty *int64, cpc *float64, raw any) (id int64, created bool, err error) {
	country := ""
	if loc.CountryISO != nil {
		country = strings.ToUpper(*loc.CountryISO)
	}
	res, err := tx.Exec(
		`INSERT INTO keywords (project_id, search_engine, text, location_id, country_iso, language_iso)
		   VALUES (?, ?, ?, ?, ?, ?)
		   ON CONFLICT(project_id, text, location_id) DO NOTHING`,
		projectID, searchEngine, text, loc.ID, country, strings.ToLower(loc.LanguageCode))
	if err != nil {
		return 0, false, fmt.Errorf("upsert ranked keyword %q: %w", text, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		created = true
	}
	if err := tx.QueryRow(
		`SELECT id FROM keywords WHERE project_id = ? AND text = ? AND location_id = ?`,
		projectID, text, loc.ID,
	).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("lookup ranked keyword %q: %w", text, err)
	}
	rawText, _ := json.Marshal(raw)
	if _, err := tx.Exec(
		`INSERT INTO keyword_metrics
		   (keyword_id, location_id, provider, ts, volume, difficulty, cpc_usd, raw_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, loc.ID, provider, time.Now().Unix(), volume, difficulty, cpc, string(rawText),
	); err != nil {
		return 0, false, fmt.Errorf("insert ranked keyword metrics for %q: %w", text, err)
	}
	return id, created, nil
}

func upsertPageForRankURL(tx *sql.Tx, domainID int64, rawURL string, title string) (bool, error) {
	path := pagePathFromURL(rawURL)
	if path == "" {
		return false, nil
	}
	res, err := tx.Exec(
		`INSERT INTO pages (domain_id, path, label)
		   VALUES (?, ?, ?)
		   ON CONFLICT(domain_id, path) DO UPDATE SET
		     label = CASE WHEN excluded.label != '' THEN excluded.label ELSE pages.label END`,
		domainID, path, strings.TrimSpace(title))
	if err != nil {
		return false, fmt.Errorf("upsert page %q: %w", path, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func pagePathFromURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

func firstInt64Ptr(xs ...*int64) *int64 {
	for _, x := range xs {
		if x != nil {
			return x
		}
	}
	return nil
}

// ─── Keyword search-volume + monthly history ─────────────────────

// dfsKeywordVolumeItem is the per-keyword row inside the
// keywords_data/google_ads/search_volume/live result. We send one
// keyword per call; the monthly_searches array is the inline history
// we unfold into keyword_volume_history.
type dfsKeywordVolumeItem struct {
	Keyword          string          `json:"keyword"`
	LocationCode     int             `json:"location_code"`
	LanguageCode     string          `json:"language_code"`
	SearchVolume     *int64          `json:"search_volume"`
	CompetitionIdx   *int64          `json:"competition_index"`
	CPC              *float64        `json:"cpc"`
	LowTopOfPageBid  *float64        `json:"low_top_of_page_bid"`
	HighTopOfPageBid *float64        `json:"high_top_of_page_bid"`
	Raw              json.RawMessage `json:"-"`
	MonthlySearches  []struct {
		Year   int   `json:"year"`
		Month  int   `json:"month"`
		Volume int64 `json:"search_volume"`
	} `json:"monthly_searches"`
}

func decodeKeywordVolumeItem(rowRaw []byte, keyword string) (dfsKeywordVolumeItem, error) {
	var item dfsKeywordVolumeItem
	var wrapped struct {
		Items []dfsKeywordVolumeItem `json:"items"`
	}
	if err := json.Unmarshal(rowRaw, &wrapped); err != nil {
		return item, fmt.Errorf("parse keyword_search_volume: %w", err)
	}
	if len(wrapped.Items) > 0 {
		item = wrapped.Items[0]
	} else if err := json.Unmarshal(rowRaw, &item); err != nil {
		return item, fmt.Errorf("parse keyword_search_volume item: %w", err)
	}
	if item.Keyword == "" && item.SearchVolume == nil && item.CPC == nil && len(item.MonthlySearches) == 0 {
		return item, fmt.Errorf("keyword_search_volume: zero items for %q", keyword)
	}
	return item, nil
}

func decodeKeywordDifficulty(rowRaw []byte, keyword string) (*int64, error) {
	values, err := decodeKeywordDifficultyItems([]json.RawMessage{rowRaw})
	if err != nil {
		return nil, err
	}
	item, ok := values[normaliseKeyword(keyword)]
	if !ok {
		return nil, nil
	}
	return item.Difficulty, nil
}

func refreshKeywordViaDataForSEO(ctx *sdk.AppCtx, connID int64, k *Keyword, loc *SEOLocation) (any, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("dataforseo refresh requires a location with location_code")
	}
	if loc.ID != k.LocationID {
		return nil, fmt.Errorf("keyword %d belongs to location %d, not %d", k.ID, k.LocationID, loc.ID)
	}
	balance, err := preflightDataForSEO(ctx, connID)
	if err != nil {
		return nil, err
	}
	jobs, err := createKeywordMetricJobs(ctx.AppDB(), k.ProjectID, []int64{k.ID})
	if err != nil {
		return nil, err
	}
	job := jobs[0]
	if err := runKeywordMetricJob(ctx, job.ID); err != nil {
		return nil, err
	}
	completed, err := getKeywordMetricJob(ctx.AppDB(), k.ProjectID, job.ID)
	if err != nil {
		return nil, err
	}
	if completed.Status != "completed" {
		return nil, fmt.Errorf("keyword metrics refresh incomplete: %s", completed.LastError)
	}
	metrics, err := latestKeywordMetrics(ctx.AppDB(), k.ID, "dataforseo")
	if err != nil {
		return nil, err
	}
	var historyRows int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM keyword_volume_history
		  WHERE keyword_id = ? AND location_id = ? AND provider = 'dataforseo'`,
		k.ID, loc.ID).Scan(&historyRows); err != nil {
		return nil, err
	}
	return map[string]any{
		"keyword_id":      k.ID,
		"location_id":     loc.ID,
		"snapshot_id":     metrics.ID,
		"provider":        "dataforseo",
		"fetched_at":      metrics.TS,
		"volume":          valOr(metrics.Volume, 0),
		"difficulty":      valOr(metrics.Difficulty, 0),
		"history_rows":    historyRows,
		"metric_job_id":   job.ID,
		"account_balance": balance,
	}, nil
}

// ─── Backlinks list ──────────────────────────────────────────────

type dfsBacklinkItem struct {
	URLFrom        string   `json:"url_from"`
	URLTo          string   `json:"url_to"`
	Anchor         string   `json:"anchor"`
	Dofollow       bool     `json:"dofollow"`
	Attributes     []string `json:"attributes"`
	IsLost         bool     `json:"is_lost"`
	DomainFromRank *int64   `json:"domain_from_rank"`
	FirstSeen      string   `json:"first_seen"`
	LastSeen       string   `json:"last_seen"`
}

func refreshBacklinksViaDataForSEO(ctx *sdk.AppCtx, connID int64, d *Domain) (any, error) {
	rowRaw, _, err := callDfs(ctx, connID, "backlinks_list", map[string]any{
		"target":                d.Host,
		"mode":                  "as_is",
		"limit":                 100, // v0.2 cap; v0.3 paginates
		"backlinks_status_type": "all",
	})
	if err != nil {
		return nil, err
	}
	if rowRaw == nil {
		return map[string]any{"domain_id": d.ID, "rows_upserted": 0, "note": "no backlinks reported"}, nil
	}
	var parsed struct {
		Items []dfsBacklinkItem `json:"items"`
	}
	if err := json.Unmarshal(rowRaw, &parsed); err != nil {
		return nil, fmt.Errorf("parse backlinks_list: %w", err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	upserts := 0
	for _, b := range parsed.Items {
		// Per-row raw_json so post-hoc analyses (anchor diversity,
		// link types) don't require re-fetching.
		raw, _ := json.Marshal(b)
		isUGC := containsAttr(b.Attributes, "ugc")
		isSponsored := containsAttr(b.Attributes, "sponsored")
		isNofollow := !b.Dofollow
		_, err := tx.Exec(
			`INSERT INTO backlinks
			   (domain_id, provider, source_url, dest_url, anchor,
			    is_dofollow, is_nofollow, is_ugc, is_sponsored,
			    source_authority, first_seen, last_seen, is_lost, raw_json)
			 VALUES (?, 'dataforseo', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(domain_id, provider, source_url, dest_url, anchor)
			 DO UPDATE SET
			    last_seen = excluded.last_seen,
			    is_lost   = excluded.is_lost,
			    raw_json  = excluded.raw_json`,
			d.ID, b.URLFrom, b.URLTo, b.Anchor,
			boolToInt(b.Dofollow), boolToInt(isNofollow),
			boolToInt(isUGC), boolToInt(isSponsored),
			b.DomainFromRank,
			parseDfsTime(b.FirstSeen),
			parseDfsTime(b.LastSeen),
			boolToInt(b.IsLost),
			string(raw),
		)
		if err != nil {
			return nil, fmt.Errorf("upsert backlink: %w", err)
		}
		upserts++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"domain_id":     d.ID,
		"provider":      "dataforseo",
		"rows_upserted": upserts,
		"capped_at":     100,
	}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────

func syncDataForSEOLocations(ctx *sdk.AppCtx, connID int64) (any, error) {
	rows, err := dfsToolResultRows(ctx, connID, "locations_and_languages", map[string]any{})
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	upserts, skipped, err := syncDataForSEOGoogleLocationRows(ctx.AppDB(), rows, now)
	if err != nil {
		return nil, err
	}
	engineResults := map[string]any{
		"google": map[string]any{
			"rows_upserted": upserts,
			"rows_skipped":  skipped,
			"ok":            true,
		},
	}
	warnings := []string{}
	ytUpserts, ytSkipped, err := syncDataForSEOYouTubeLocations(ctx, connID, now)
	ytFallback := false
	if err != nil || ytUpserts == 0 {
		var syncErr error
		if err != nil {
			syncErr = err
			warnings = append(warnings, err.Error())
		} else {
			syncErr = fmt.Errorf("dataforseo: youtube_locations returned zero usable rows")
			warnings = append(warnings, syncErr.Error())
		}
		fallbackUpserts, fallbackSkipped, fallbackErr := seedYouTubeLocationsFromGoogle(ctx.AppDB(), now)
		if fallbackErr != nil {
			warnings = append(warnings, fallbackErr.Error())
			engineResults["youtube"] = map[string]any{
				"rows_upserted": 0,
				"rows_skipped":  0,
				"ok":            false,
				"error":         syncErr.Error(),
			}
		} else {
			ytUpserts = fallbackUpserts
			ytSkipped = fallbackSkipped
			ytFallback = true
			upserts += ytUpserts
			skipped += ytSkipped
			engineResults["youtube"] = map[string]any{
				"rows_upserted": ytUpserts,
				"rows_skipped":  ytSkipped,
				"ok":            true,
				"fallback":      "google_locations",
			}
		}
	} else {
		upserts += ytUpserts
		skipped += ytSkipped
		engineResults["youtube"] = map[string]any{
			"rows_upserted": ytUpserts,
			"rows_skipped":  ytSkipped,
			"ok":            true,
		}
	}
	if ytFallback {
		warnings = append(warnings, "youtube locations were seeded from DataForSEO Google locations because the full YouTube catalog was unavailable")
	}
	out := map[string]any{
		"provider":      "dataforseo",
		"rows_upserted": upserts,
		"rows_skipped":  skipped,
		"synced_at":     now,
		"engines":       engineResults,
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out, nil
}

func syncDataForSEOGoogleLocationRows(db *sql.DB, rows []json.RawMessage, syncedAt int64) (upserts int, skipped int, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	for _, raw := range rows {
		n, s, err := upsertDfsLocationRaw(tx, raw, syncedAt)
		if err != nil {
			return 0, 0, err
		}
		upserts += n
		skipped += s
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return upserts, skipped, nil
}

func syncDataForSEOYouTubeLocations(ctx *sdk.AppCtx, connID int64, syncedAt int64) (upserts int, skipped int, err error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	upserts, skipped, err = upsertDfsYouTubeLocations(ctx, tx, connID, syncedAt)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return upserts, skipped, nil
}

func dfsToolResultRows(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any) ([]json.RawMessage, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("dataforseo: ExecuteIntegrationTool(%s): %w", tool, err)
	}
	if !res.Success || res.Status >= 400 {
		return nil, &providerRequestError{Provider: "dataforseo", Status: res.Status, Message: tool + " request failed"}
	}
	var env dfsEnvelope
	if err := json.Unmarshal(res.Data, &env); err == nil && len(env.Tasks) > 0 {
		if env.StatusCode != 0 && env.StatusCode != 20000 {
			return nil, fmt.Errorf("dataforseo: %s status %d: %s", tool, env.StatusCode, env.StatusMsg)
		}
		out := []json.RawMessage{}
		for _, task := range env.Tasks {
			if task.StatusCode != 0 && task.StatusCode != 20000 {
				return nil, fmt.Errorf("dataforseo: %s task status %d: %s", tool, task.StatusCode, task.StatusMsg)
			}
			out = append(out, task.Result...)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("dataforseo: %s returned zero result rows", tool)
		}
		return out, nil
	}
	return []json.RawMessage{json.RawMessage(res.Data)}, nil
}

func upsertDfsYouTubeLocations(ctx *sdk.AppCtx, tx *sql.Tx, connID int64, syncedAt int64) (upserts int, skipped int, err error) {
	locationRows, err := dfsToolResultRows(ctx, connID, "youtube_locations", map[string]any{})
	if err != nil {
		return 0, 0, err
	}
	languageRows, err := dfsToolResultRows(ctx, connID, "youtube_languages", map[string]any{})
	if err != nil {
		return 0, 0, err
	}
	locations := flattenDfsObjects(locationRows)
	languages := flattenDfsObjects(languageRows)
	if len(languages) == 0 {
		languages = []map[string]any{{"language_code": "en", "language_name": "English"}}
	}
	for _, locObj := range locations {
		locCode, hasCode := numberField(locObj, "location_code")
		locName := firstString(locObj, "location_name", "location")
		country := strings.ToUpper(firstString(locObj, "country_iso_code", "country_iso", "country_code"))
		if !hasCode || locName == "" {
			skipped++
			continue
		}
		locRaw, _ := json.Marshal(locObj)
		for _, langObj := range languages {
			langCode := strings.ToLower(firstString(langObj, "language_code"))
			langName := firstString(langObj, "language_name")
			if langCode == "" {
				skipped++
				continue
			}
			langRaw, _ := json.Marshal(langObj)
			rawText := fmt.Sprintf(`{"location":%s,"language":%s}`, locRaw, langRaw)
			var countryArg any
			if country != "" {
				countryArg = country
			}
			_, err := tx.Exec(
				`INSERT INTO seo_locations
				   (provider, search_engine, location_code, location_name, country_iso,
				    language_code, language_name, is_active, raw_json, synced_at)
				 VALUES ('dataforseo', 'youtube', ?, ?, ?, ?, ?, 1, ?, ?)
				 ON CONFLICT(provider, search_engine, location_code, language_code)
				 DO UPDATE SET
				    location_name = excluded.location_name,
				    country_iso = excluded.country_iso,
				    language_name = excluded.language_name,
				    is_active = 1,
				    raw_json = excluded.raw_json,
				    synced_at = excluded.synced_at`,
				locCode, locName, countryArg, langCode, langName, rawText, syncedAt)
			if err != nil {
				return upserts, skipped, fmt.Errorf("upsert dataforseo youtube location: %w", err)
			}
			upserts++
		}
	}
	return upserts, skipped, nil
}

func seedYouTubeLocationsFromGoogle(db *sql.DB, syncedAt int64) (upserts int, skipped int, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(
		`SELECT location_code, location_name, country_iso, language_code, language_name, raw_json
		   FROM seo_locations
		  WHERE provider = 'dataforseo'
		    AND search_engine = 'google'
		    AND is_active = 1
		    AND location_code IS NOT NULL
		    AND country_iso IS NOT NULL
		    AND country_iso != ''
		    AND language_code != ''`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var locCode int64
		var locName, country, langCode, langName, rawText string
		if err := rows.Scan(&locCode, &locName, &country, &langCode, &langName, &rawText); err != nil {
			return 0, 0, err
		}
		if locCode == 0 || strings.TrimSpace(locName) == "" || strings.TrimSpace(langCode) == "" {
			skipped++
			continue
		}
		if !json.Valid([]byte(rawText)) {
			rawText = "{}"
		}
		fallbackRaw, _ := json.Marshal(map[string]any{
			"fallback": "google_location_for_youtube",
			"source":   json.RawMessage(rawText),
		})
		if _, err := tx.Exec(
			`INSERT INTO seo_locations
			   (provider, search_engine, location_code, location_name, country_iso,
			    language_code, language_name, is_active, raw_json, synced_at)
			 VALUES ('dataforseo', 'youtube', ?, ?, ?, ?, ?, 1, ?, ?)
			 ON CONFLICT(provider, search_engine, location_code, language_code)
			 DO UPDATE SET
			    location_name = excluded.location_name,
			    country_iso = excluded.country_iso,
			    language_name = excluded.language_name,
			    is_active = 1,
			    raw_json = excluded.raw_json,
			    synced_at = excluded.synced_at`,
			locCode, locName, strings.ToUpper(country), strings.ToLower(langCode), langName, string(fallbackRaw), syncedAt); err != nil {
			return upserts, skipped, fmt.Errorf("seed youtube location from google location: %w", err)
		}
		upserts++
	}
	if err := rows.Err(); err != nil {
		return upserts, skipped, err
	}
	if upserts == 0 {
		return upserts, skipped, fmt.Errorf("youtube fallback found zero active google locations to copy")
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return upserts, skipped, nil
}

func flattenDfsObjects(rows []json.RawMessage) []map[string]any {
	out := []map[string]any{}
	var walk func(json.RawMessage)
	walk = func(raw json.RawMessage) {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil && arr != nil {
			for _, item := range arr {
				walk(item)
			}
			return
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return
		}
		for _, key := range []string{"items", "result", "results"} {
			if xs, ok := obj[key].([]any); ok {
				for _, x := range xs {
					itemRaw, _ := json.Marshal(x)
					walk(itemRaw)
				}
				return
			}
		}
		out = append(out, obj)
	}
	for _, row := range rows {
		walk(row)
	}
	return out
}

func upsertDfsLocationRaw(tx *sql.Tx, raw json.RawMessage, syncedAt int64) (upserts int, skipped int, err error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && arr != nil {
		for _, item := range arr {
			n, s, err := upsertDfsLocationRaw(tx, item, syncedAt)
			if err != nil {
				return upserts, skipped, err
			}
			upserts += n
			skipped += s
		}
		return upserts, skipped, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, 0, fmt.Errorf("parse dataforseo location row: %w", err)
	}
	if xs, ok := obj["items"].([]any); ok {
		for _, x := range xs {
			itemRaw, _ := json.Marshal(x)
			n, s, err := upsertDfsLocationRaw(tx, itemRaw, syncedAt)
			if err != nil {
				return upserts, skipped, err
			}
			upserts += n
			skipped += s
		}
		return upserts, skipped, nil
	}
	locCode, hasCode := numberField(obj, "location_code")
	locName := firstString(obj, "location_name", "location")
	country := strings.ToUpper(firstString(obj, "country_iso_code", "country_iso", "country_code"))
	if languages, ok := obj["available_languages"].([]any); ok && len(languages) > 0 {
		for _, rawLang := range languages {
			langObj, ok := rawLang.(map[string]any)
			if !ok {
				skipped++
				continue
			}
			langCode := strings.ToLower(firstString(langObj, "language_code"))
			langName := firstString(langObj, "language_name")
			if !hasCode || locName == "" || langCode == "" {
				skipped++
				continue
			}
			sources := dfsLocationSources(langObj)
			if len(sources) == 0 {
				sources = dfsLocationSources(obj)
			}
			if len(sources) == 0 {
				sources = []string{"google"}
			}
			langRaw, _ := json.Marshal(langObj)
			rawText := fmt.Sprintf(`{"location":%s,"language":%s}`, raw, langRaw)
			n, err := upsertDfsLocationEntry(tx, locCode, locName, country, langCode, langName, sources, rawText, syncedAt)
			if err != nil {
				return upserts, skipped, err
			}
			upserts += n
		}
		return upserts, skipped, nil
	}
	langCode := strings.ToLower(firstString(obj, "language_code"))
	langName := firstString(obj, "language_name")
	if !hasCode || locName == "" || langCode == "" {
		return 0, 1, nil
	}
	sources := dfsLocationSources(obj)
	if len(sources) == 0 {
		sources = []string{"google"}
	}
	rawText := string(raw)
	n, err := upsertDfsLocationEntry(tx, locCode, locName, country, langCode, langName, sources, rawText, syncedAt)
	if err != nil {
		return upserts, skipped, err
	}
	return n, skipped, nil
}

func upsertDfsLocationEntry(tx *sql.Tx, locCode int64, locName, country, langCode, langName string, sources []string, rawText string, syncedAt int64) (upserts int, err error) {
	for _, source := range sources {
		if source == "" {
			continue
		}
		var countryArg any
		if country != "" {
			countryArg = country
		}
		_, err := tx.Exec(
			`INSERT INTO seo_locations
			   (provider, search_engine, location_code, location_name, country_iso,
			    language_code, language_name, is_active, raw_json, synced_at)
			 VALUES ('dataforseo', ?, ?, ?, ?, ?, ?, 1, ?, ?)
			 ON CONFLICT(provider, search_engine, location_code, language_code)
			 DO UPDATE SET
			    location_name = excluded.location_name,
			    country_iso = excluded.country_iso,
			    language_name = excluded.language_name,
			    is_active = 1,
			    raw_json = excluded.raw_json,
			    synced_at = excluded.synced_at`,
			source, locCode, locName, countryArg, langCode, langName, rawText, syncedAt)
		if err != nil {
			return upserts, fmt.Errorf("upsert dataforseo location: %w", err)
		}
		upserts++
	}
	return upserts, nil
}

func dfsLocationSources(obj map[string]any) []string {
	out := []string{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			return
		}
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}
	add(firstString(obj, "source", "search_engine", "se"))
	if xs, ok := obj["available_sources"].([]any); ok {
		for _, x := range xs {
			switch v := x.(type) {
			case string:
				add(v)
			case map[string]any:
				add(firstString(v, "source", "search_engine", "se"))
			}
		}
	}
	if xs, ok := obj["sources"].([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := obj[key]; ok {
			switch x := v.(type) {
			case string:
				if strings.TrimSpace(x) != "" {
					return strings.TrimSpace(x)
				}
			}
		}
	}
	return ""
}

func numberField(obj map[string]any, key string) (int64, bool) {
	v, ok := obj[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case int:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

// parseDfsTime parses DataForSEO's "YYYY-MM-DD HH:MM:SS +00:00"
// timestamps to unix seconds. Returns nil on any parse failure so
// the column lands NULL rather than 0.
func parseDfsTime(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return nil
}

func containsAttr(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

func valOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// Compile-time safety: ensure we still pull in database/sql even if
// later edits drop other usages. (Build will fail loud if not.)
var _ = sql.ErrNoRows
